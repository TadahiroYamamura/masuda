#!/usr/bin/env bash
# One-time host setup for masuda's VM backend (Issue #31). Safe to re-run:
# every step checks whether it's already done before acting.
#
# This is NOT invoked by masuda itself -- masuda's own code never runs sudo
# on its own initiative (established while designing the VM backend: masuda
# should never silently self-elevate). This script exists for a human to
# read and run explicitly. See docs/design/networking.md for the design/
# rationale behind each step below (docs/INSTALLATION.md has the plain
# instructions for running this script, not the why).
#
# Requires: linux-image-generic and Cloud Hypervisor/virtiofsd already
# installed (docs/INSTALLATION.md) -- this script doesn't fetch those
# itself; the former is a simple apt package this script *does* install,
# the latter two have no standard apt package and need a manual download,
# which this script won't guess a URL for.

set -euo pipefail

BRIDGE=br-masuda0
BRIDGE_ADDR=192.168.200.1
BRIDGE_CIDR="$BRIDGE_ADDR/24"
BRIDGE_SUBNET=192.168.200.0/24
DHCP_RANGE_START=192.168.200.10
DHCP_RANGE_END=192.168.200.200
DATA_HOME="${XDG_DATA_HOME:-$HOME/.local/share}/masuda"
NET_HELPER="$HOME/.local/bin/masuda-net-helper"
# Must match internal/sandbox.egressProxyPort (Issue #11).
EGRESS_PROXY_PORT=39218
EGRESS_PROXY_MARK=0x1
EGRESS_PROXY_RT_TABLE=100

log() { echo "[setup-vm-host] $*"; }

require_cmd() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "error: $1 not found on PATH. $2" >&2
		exit 1
	fi
}

if [ ! -f go.mod ] || [ ! -d cmd/masuda-net-helper ]; then
	echo "error: run this from the masuda repository root" >&2
	exit 1
fi

require_cmd sudo "this script needs sudo for a handful of one-time host-level steps (see docs/INSTALLATION.md)"
require_cmd ip "install iproute2"
require_cmd iptables "install iptables"
require_cmd go "install the Go toolchain first"
require_cmd cloud-hypervisor "install Cloud Hypervisor first (docs/INSTALLATION.md) -- no standard apt package, this script won't guess a download URL"
require_cmd virtiofsd "install virtiofsd first (docs/INSTALLATION.md) -- same reason as cloud-hypervisor above"

# fakeroot/e2fsprogs: internal/rootfs.Build's dependencies (Issue #31 M2).
# Ordinary apt packages, safe for this script to install directly (unlike
# cloud-hypervisor/virtiofsd above).
step_rootfs_build_deps() {
	local missing=()
	command -v fakeroot >/dev/null 2>&1 || missing+=(fakeroot)
	command -v mkfs.ext4 >/dev/null 2>&1 || missing+=(e2fsprogs)
	if [ "${#missing[@]}" -gt 0 ]; then
		log "installing ${missing[*]}"
		sudo apt-get update
		sudo apt-get install -y "${missing[@]}"
	else
		log "fakeroot/e2fsprogs already installed"
	fi
}

# Kernel: linux-image-generic + a copy of vmlinuz masuda's own user can
# read. /boot/vmlinuz-* is root:root mode 600 immediately after install
# (Issue #31 M3's discovery).
step_kernel() {
	local latest version dest
	latest=$(ls /boot/vmlinuz-*-generic 2>/dev/null | sort -V | tail -1 || true)
	if [ -z "$latest" ]; then
		log "linux-image-generic not found, installing..."
		sudo apt-get update
		sudo apt-get install -y linux-image-generic
		latest=$(ls /boot/vmlinuz-*-generic 2>/dev/null | sort -V | tail -1)
	fi
	version=$(basename "$latest" | sed 's/^vmlinuz-//')
	dest="$DATA_HOME/vmlinuz-$version"
	mkdir -p "$DATA_HOME"
	if [ -r "$dest" ]; then
		log "kernel already accessible at $dest"
	else
		log "copying $latest to $dest (readable by $USER)"
		sudo install -m 0644 -o "$USER" -g "$USER" "$latest" "$dest"
	fi
}

# Bridge + outbound NAT: shared, host-level infrastructure. Individual
# per-workspace TAP devices are *not* created here -- those are created and
# destroyed dynamically per VM by masuda-net-helper. Why the split:
# docs/adr/0048-vm-network-shared-bridge-dynamic-tap-privileged-helper.md
step_network() {
	if ip link show "$BRIDGE" >/dev/null 2>&1; then
		log "bridge $BRIDGE already exists"
	else
		log "creating bridge $BRIDGE"
		sudo ip link add "$BRIDGE" type bridge
		sudo ip addr add "$BRIDGE_CIDR" dev "$BRIDGE"
		sudo ip link set "$BRIDGE" up
	fi

	sudo sysctl -w net.ipv4.ip_forward=1 >/dev/null

	local uplink
	uplink=$(ip route show default | awk '{print $5; exit}')
	if [ -z "$uplink" ]; then
		echo "error: could not detect a default route interface for NAT" >&2
		exit 1
	fi
	if sudo iptables -t nat -C POSTROUTING -s "$BRIDGE_SUBNET" -o "$uplink" -j MASQUERADE 2>/dev/null; then
		log "NAT rule via $uplink already present"
	else
		log "adding outbound NAT via $uplink"
		sudo iptables -t nat -A POSTROUTING -s "$BRIDGE_SUBNET" -o "$uplink" -j MASQUERADE
	fi
	# Return traffic for connections the bridge side originated (DNS
	# queries, and redirected 443 traffic once step_egress_filtering runs)
	# needs this regardless of what step_egress_filtering restricts on
	# the outbound side.
	if sudo iptables -C FORWARD -i "$uplink" -o "$BRIDGE" -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null; then
		log "established-traffic FORWARD rule already present"
	else
		sudo iptables -A FORWARD -i "$uplink" -o "$BRIDGE" -m state --state RELATED,ESTABLISHED -j ACCEPT
	fi
}

# Egress filtering (Issue #11): every VM's outbound TLS (port 443) traffic
# is redirected to masuda-egress-proxy, which only forwards connections
# whose SNI hostname is on the connecting workspace's allowlist
# (internal/sandbox.NewEgressAllowlistFunc). Nothing else may leave the
# bridge except DNS -- default-deny, not an opt-out list. See
# internal/egressproxy.Proxy's own doc comment for the design rationale
# (one shared proxy process, not one per workspace, chosen specifically so
# this could be a single static ruleset rather than rules added/removed
# dynamically per VM).
#
# REDIRECT (nat/PREROUTING), not TPROXY: an earlier revision used TPROXY,
# which needs a fwmark + policy-routing table trick to make the kernel
# treat a packet addressed to some arbitrary external IP as "local
# delivery" -- confirmed live, with a minimal veth reproduction pinned
# directly to $BRIDGE (no VM involved), that this trick does not work on
# this project's WSL2 development host: the TPROXY rule showed matched
# packets in `iptables -v`, but they never reached masuda-egress-proxy,
# and the filter/INPUT chain's own policy counter stayed at zero the
# entire time regardless of `ip route get ... mark` simulating the correct
# local route. No interfering nftables/rp_filter/FORWARD rule was found;
# root cause undetermined beyond "not this project's problem to fix in a
# WSL2 guest kernel". REDIRECT sidesteps the whole question by rewriting
# the destination address itself before the routing decision ever runs,
# so the resulting packet is delivered locally for the ordinary reason a
# packet addressed to this host's own address always is.
step_egress_filtering() {
	# Remove artifacts from the earlier TPROXY-based revision, if present.
	if sudo iptables -t mangle -C PREROUTING -i "$BRIDGE" -p tcp --dport 443 \
		-j TPROXY --tproxy-mark "$EGRESS_PROXY_MARK/$EGRESS_PROXY_MARK" --on-port "$EGRESS_PROXY_PORT" --on-ip "$BRIDGE_ADDR" 2>/dev/null; then
		log "removing old TPROXY rule"
		sudo iptables -t mangle -D PREROUTING -i "$BRIDGE" -p tcp --dport 443 \
			-j TPROXY --tproxy-mark "$EGRESS_PROXY_MARK/$EGRESS_PROXY_MARK" --on-port "$EGRESS_PROXY_PORT" --on-ip "$BRIDGE_ADDR"
	fi
	if sudo iptables -t mangle -C PREROUTING -p tcp -m socket --transparent -j DIVERT 2>/dev/null; then
		log "removing old DIVERT jump"
		sudo iptables -t mangle -D PREROUTING -p tcp -m socket --transparent -j DIVERT
	fi
	if sudo iptables -t mangle -C PREROUTING -p tcp -m socket -j DIVERT 2>/dev/null; then
		log "removing old DIVERT jump (pre---transparent variant)"
		sudo iptables -t mangle -D PREROUTING -p tcp -m socket -j DIVERT
	fi
	if sudo iptables -t mangle -L DIVERT >/dev/null 2>&1; then
		log "flushing and removing old DIVERT chain"
		sudo iptables -t mangle -F DIVERT
		sudo iptables -t mangle -X DIVERT
	fi
	if ip rule show | grep -q "fwmark $EGRESS_PROXY_MARK lookup $EGRESS_PROXY_RT_TABLE"; then
		log "removing old policy route for fwmark $EGRESS_PROXY_MARK"
		sudo ip rule del fwmark "$EGRESS_PROXY_MARK" lookup "$EGRESS_PROXY_RT_TABLE"
	fi
	if ip route show table "$EGRESS_PROXY_RT_TABLE" 2>/dev/null | grep -q "^local default "; then
		log "removing old local route in table $EGRESS_PROXY_RT_TABLE"
		sudo ip route del local 0.0.0.0/0 dev lo table "$EGRESS_PROXY_RT_TABLE"
	fi

	if sudo iptables -t nat -C PREROUTING -i "$BRIDGE" -p tcp --dport 443 \
		-j REDIRECT --to-port "$EGRESS_PROXY_PORT" 2>/dev/null; then
		log "REDIRECT rule for port 443 already present"
	else
		log "adding REDIRECT rule: bridge port 443 -> masuda-egress-proxy on :$EGRESS_PROXY_PORT"
		sudo iptables -t nat -A PREROUTING -i "$BRIDGE" -p tcp --dport 443 \
			-j REDIRECT --to-port "$EGRESS_PROXY_PORT"
	fi

	local uplink
	uplink=$(ip route show default | awk '{print $5; exit}')
	# Remove the wide-open "ACCEPT everything from $BRIDGE" FORWARD rule
	# step_network used to add before Issue #11 existed, on a host that
	# was set up before this step was added -- it would make everything
	# below moot.
	if sudo iptables -C FORWARD -i "$BRIDGE" -o "$uplink" -j ACCEPT 2>/dev/null; then
		log "removing the old unrestricted bridge->uplink FORWARD rule"
		sudo iptables -D FORWARD -i "$BRIDGE" -o "$uplink" -j ACCEPT
	fi
	if sudo iptables -C FORWARD -i "$BRIDGE" -o "$uplink" -p udp --dport 53 -j ACCEPT 2>/dev/null; then
		log "DNS FORWARD rules already present"
	else
		log "restricting bridge egress to DNS only (everything else denied by default -- Issue #11)"
		sudo iptables -A FORWARD -i "$BRIDGE" -o "$uplink" -p udp --dport 53 -j ACCEPT
		sudo iptables -A FORWARD -i "$BRIDGE" -o "$uplink" -p tcp --dport 53 -j ACCEPT
	fi

	# Explicit catch-all: everything from the bridge that didn't match one
	# of the ACCEPT rules above (443 doesn't need one -- REDIRECT above
	# hands it to the local proxy before FORWARD ever sees it) is dropped
	# here, scoped to $BRIDGE only (not a global FORWARD policy change,
	# which would reach unrelated Docker workloads on a shared host).
	# Without this rule, "everything else denied by default" depended
	# entirely on this host's FORWARD chain already defaulting to DROP --
	# true here only as a side effect of Docker's own installation setting
	# it, not because this script asked for it. This rule makes the
	# default-deny masuda's own, independent of whatever else happens to
	# be installed on the host.
	if sudo iptables -C FORWARD -i "$BRIDGE" -j DROP 2>/dev/null; then
		log "bridge catch-all DROP rule already present"
	else
		log "adding explicit catch-all DROP for bridge egress not otherwise accepted"
		sudo iptables -A FORWARD -i "$BRIDGE" -j DROP
	fi
}

# masuda-net-helper: build + setcap. CAP_NET_ADMIN goes on this small,
# single-purpose binary -- never on masuda itself
# (docs/adr/0048-vm-network-shared-bridge-dynamic-tap-privileged-helper.md).
# Rebuilding the binary always clears its capability (a Linux property, not a
# masuda choice), so this step re-applies setcap unconditionally every run.
step_net_helper() {
	log "building masuda-net-helper"
	mkdir -p "$(dirname "$NET_HELPER")"
	go build -o "$NET_HELPER" ./cmd/masuda-net-helper
	log "granting CAP_NET_ADMIN to $NET_HELPER"
	sudo setcap cap_net_admin+ep "$NET_HELPER"
}

# masuda-egress-proxy: build only (Issue #11). Connections reach it via an
# iptables REDIRECT rule (step_egress_filtering), so unlike
# masuda-net-helper it needs no special capability -- an ordinary bound
# listener is enough.
step_egress_proxy() {
	local egress_proxy="$HOME/.local/bin/masuda-egress-proxy"
	log "building masuda-egress-proxy"
	mkdir -p "$(dirname "$egress_proxy")"
	go build -o "$egress_proxy" ./cmd/masuda-egress-proxy
}

# dnsmasq: DHCP for VM guests. Bound only to $BRIDGE, so it has no bearing on
# the host's other networks. Guest IPs come from DHCP rather than static
# per-VM config:
# docs/adr/0048-vm-network-shared-bridge-dynamic-tap-privileged-helper.md
step_dnsmasq() {
	if ! command -v dnsmasq >/dev/null 2>&1; then
		log "installing dnsmasq"
		sudo apt-get update
		sudo apt-get install -y dnsmasq
	fi
	local conf=/etc/dnsmasq.d/masuda-vm.conf
	local desired
	desired=$(
		cat <<CONF
interface=$BRIDGE
bind-interfaces
except-interface=lo
dhcp-range=$DHCP_RANGE_START,$DHCP_RANGE_END,12h
dhcp-leasefile=/var/lib/misc/masuda-dnsmasq.leases
# The Ubuntu dnsmasq package's own systemd unit hardcodes
# "-r /run/dnsmasq/resolv.conf" as the upstream DNS source, a file
# normally generated by the resolvconf package -- not installed/wired up
# on this host (confirmed live: that file never gets created, and every
# non-local query dnsmasq receives comes back REFUSED because it has no
# upstream server to forward to at all). Point it at the host's own
# /etc/resolv.conf directly instead: confirmed live this actually
# overrides the -r flag rather than being silently ignored by it.
resolv-file=/etc/resolv.conf
CONF
	)
	if [ -f "$conf" ] && diff -q <(echo "$desired") "$conf" >/dev/null 2>&1; then
		log "$conf already up to date"
	else
		log "writing $conf"
		echo "$desired" | sudo tee "$conf" >/dev/null
	fi
	sudo systemctl enable --now dnsmasq
	sudo systemctl restart dnsmasq
}

step_rootfs_build_deps
step_kernel
step_network
step_egress_filtering
step_net_helper
step_egress_proxy
step_dnsmasq

log "done. See docs/design/networking.md for what each step configured and why."
