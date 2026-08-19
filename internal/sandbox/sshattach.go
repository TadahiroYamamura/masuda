package sandbox

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"time"
)

// MACFor derives a deterministic MAC address for workspace id's VM network
// interface, mirroring ContainerName/TapName's "one name per workspace,
// derived from the id alone" approach. 52:54:00 is the well-known
// QEMU/KVM locally-administered prefix (matches what the Issue #31 manual
// spikes already used); the remaining three bytes come from a hash of id,
// which is all this needs -- collisions only matter among the handful of
// workspaces actually running at once (docs/backlog-agent.md's guidance is
// ~3 concurrent), not across id-space as a whole.
func MACFor(id string) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", sum[0], sum[1], sum[2])
}

// guestIPPollInterval is how often LookupGuestIP re-reads the lease file
// while waiting for the guest's DHCP client to complete negotiation.
const guestIPPollInterval = 200 * time.Millisecond

// LookupGuestIP polls dnsmasq's lease file (leaseFilePath, see
// docs/CONTRIBUTING.md's dnsmasq setup) for an entry matching mac, up to
// timeout. masuda doesn't allocate the guest's IP itself -- with multiple
// workspaces potentially running concurrently, DHCP's own lease/conflict
// handling is what actually solves that (Issue #31 M5-5); this just reads
// the result back out by the MAC address masuda already knows
// deterministically (MACFor), once the guest's systemd-networkd DHCP
// client has actually completed negotiation after boot.
func LookupGuestIP(mac, leaseFilePath string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		ip, err := findLeaseIP(mac, leaseFilePath)
		if err == nil {
			return ip, nil
		}
		lastErr = err
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(guestIPPollInterval)
	}
	return "", fmt.Errorf("no DHCP lease for %s in %s after %s: %w", mac, leaseFilePath, timeout, lastErr)
}

// findLeaseIP does one pass over dnsmasq's lease file. Its line format is
// "<expiry-epoch> <mac> <ip> <hostname-or-*> <client-id-or-*>", one lease
// per line.
func findLeaseIP(mac, leaseFilePath string) (string, error) {
	data, err := os.ReadFile(leaseFilePath)
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if strings.EqualFold(fields[1], mac) {
			return fields[2], nil
		}
	}
	return "", fmt.Errorf("mac %s not found in %s", mac, leaseFilePath)
}

// findLeaseMAC is findLeaseIP's inverse: given an IP, finds the MAC dnsmasq
// currently has it leased to (Issue #11's egress proxy needs this direction
// -- it sees a connecting VM's IP and has to work backward to "which
// workspace is this").
func findLeaseMAC(ip, leaseFilePath string) (string, error) {
	data, err := os.ReadFile(leaseFilePath)
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if fields[2] == ip {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("ip %s not found in %s", ip, leaseFilePath)
}

// SSHAttachArgs returns the argv for interactively attaching to a VM
// guest's tmux session over SSH -- the VM path's equivalent of
// DockerBackend's AttachArgs (`docker exec -it ... tmux attach`). Callers
// exec this directly (not via exec.Command's Output/Run) so the user's
// terminal is wired straight through, same as DockerBackend.AttachArgs.
//
// StrictHostKeyChecking=no + UserKnownHostsFile=/dev/null: this connection
// authenticates the *client* via privateKeyPath (masuda's own VM SSH key,
// internal/sandbox.EnsureSSHKeypair) -- the property that matters is "only
// masuda, holding that key, can get in", which the key itself provides.
// Guest IPs are DHCP-assigned and get reused across different VMs over a
// workspace's lifetime (Issue #31 M5-5), so pinning a host key on a
// residential-style known_hosts would just produce spurious "REMOTE HOST
// IDENTIFICATION HAS CHANGED" warnings for a guest that's legitimately not
// the same VM as before -- verifying the guest's own host key identity
// isn't a security property this internal, masuda-controlled connection
// relies on the way an interactive SSH session to an arbitrary host would.
func SSHAttachArgs(guestIP, privateKeyPath string) []string {
	return append(sshBaseArgs(guestIP, privateKeyPath), "tmux", "attach", "-t", tmuxSession)
}

// sshBaseArgs returns the ssh argv up to and including the target
// (ubuntu@guestIP), shared by SSHAttachArgs and VMBackend.Stop's own
// non-interactive `sudo systemctl poweroff` -- both need the identical
// connection options, just a different trailing remote command.
func sshBaseArgs(guestIP, privateKeyPath string) []string {
	return []string{
		"ssh",
		"-i", privateKeyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"ubuntu@" + guestIP,
	}
}
