#!/usr/bin/env bash
# One-time host setup for masuda's VM backend (Issue #31). Safe to re-run:
# every step checks whether it's already done before acting.
#
# This is NOT invoked by masuda itself -- masuda's own code never runs sudo
# on its own initiative (established while designing the VM backend: masuda
# should never silently self-elevate). This script exists for a human to
# read and run explicitly. See docs/CONTRIBUTING.md for the design/rationale
# behind each step below.
#
# Requires: linux-image-generic and Cloud Hypervisor/virtiofsd already
# installed (docs/CONTRIBUTING.md) -- this script doesn't fetch those
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

require_cmd sudo "this script needs sudo for a handful of one-time host-level steps (see docs/CONTRIBUTING.md)"
require_cmd ip "install iproute2"
require_cmd iptables "install iptables"
require_cmd go "install the Go toolchain first"
require_cmd cloud-hypervisor "install Cloud Hypervisor first (docs/CONTRIBUTING.md) -- no standard apt package, this script won't guess a download URL"
require_cmd virtiofsd "install virtiofsd first (docs/CONTRIBUTING.md) -- same reason as cloud-hypervisor above"

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

# Bridge + outbound NAT: shared, host-level infrastructure (Issue #31 M4).
# Individual per-workspace TAP devices are *not* created here -- those are
# created/destroyed dynamically per VM by masuda-net-helper (M5-2), the
# bridge is the one thing all of them share.
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
		sudo iptables -A FORWARD -i "$BRIDGE" -o "$uplink" -j ACCEPT
		sudo iptables -A FORWARD -i "$uplink" -o "$BRIDGE" -m state --state RELATED,ESTABLISHED -j ACCEPT
	fi
}

# masuda-net-helper: build + setcap (Issue #31 M5-2). CAP_NET_ADMIN goes on
# this small, single-purpose binary -- never on masuda itself -- so a bug
# anywhere else in masuda's much larger codebase can't reach it. Rebuilding
# the binary always clears its capability (a Linux property, not a masuda
# choice), so this step re-applies setcap unconditionally every run.
step_net_helper() {
	log "building masuda-net-helper"
	mkdir -p "$(dirname "$NET_HELPER")"
	go build -o "$NET_HELPER" ./cmd/masuda-net-helper
	log "granting CAP_NET_ADMIN to $NET_HELPER"
	sudo setcap cap_net_admin+ep "$NET_HELPER"
}

# dnsmasq: DHCP for VM guests (Issue #31 M5-5). Bound only to $BRIDGE, so it
# has no bearing on the host's other networks. Guest IPs come from DHCP
# rather than static per-VM config so masuda doesn't need its own IP
# allocator with multiple workspaces potentially running concurrently.
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
step_net_helper
step_dnsmasq

log "done. See docs/CONTRIBUTING.md for what each step configured and why."
