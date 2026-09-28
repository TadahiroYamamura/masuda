package microvm

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"time"
)

// MACFor derives a deterministic MAC address for VM id's network interface,
// mirroring TapName's "one name per VM, derived from the id alone"
// approach. 52:54:00 is the well-known
// QEMU/KVM locally-administered prefix (matches what the Issue #31 manual
// spikes already used); the remaining three bytes come from a hash of id,
// which is all this needs -- collisions only matter among the handful of
// VMs actually running at once, not across id-space as a whole.
func MACFor(id string) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", sum[0], sum[1], sum[2])
}

// guestIPPollInterval is how often LookupGuestIP re-reads the lease file
// while waiting for the guest's DHCP client to complete negotiation.
const guestIPPollInterval = 200 * time.Millisecond

// LookupGuestIP polls dnsmasq's lease file (leaseFilePath, see
// docs/INSTALLATION.md's dnsmasq setup) for an entry matching mac, up to
// timeout. Nothing here allocates the guest's IP itself -- with multiple
// VMs potentially running concurrently, DHCP's own lease/conflict
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

// FindLeaseMAC is findLeaseIP's inverse: given an IP, finds the MAC dnsmasq
// currently has it leased to (Issue #11's egress proxy needs this direction
// -- it sees a connecting VM's IP and has to work backward to "which VM is
// this").
func FindLeaseMAC(ip, leaseFilePath string) (string, error) {
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

// sshBaseArgs returns the ssh argv up to and including the target
// (user@guestIP), shared by AttachArgs and Shutdown's non-interactive
// `sudo systemctl poweroff` -- both need the identical connection options,
// just a different trailing remote command.
//
// StrictHostKeyChecking=no + UserKnownHostsFile=/dev/null: this connection
// authenticates the *client* via privateKeyPath (the host's own VM SSH key,
// Host.EnsureSSHKeypair) -- the property that matters is "only this host,
// holding that key, can get in", which the key itself provides. Guest IPs
// are DHCP-assigned and get reused across different VMs over time (Issue #31
// M5-5), so pinning a host key would just produce spurious "REMOTE HOST
// IDENTIFICATION HAS CHANGED" warnings for a guest that's legitimately not
// the same VM as before.
func sshBaseArgs(user, guestIP, privateKeyPath string) []string {
	return []string{
		"ssh",
		"-i", privateKeyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		user + "@" + guestIP,
	}
}
