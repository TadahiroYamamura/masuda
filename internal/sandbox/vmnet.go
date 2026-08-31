package sandbox

import (
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// netHelperBinary is the setcap'd, CAP_NET_ADMIN-carrying binary EnsureTap
// and ReleaseTap shell out to (cmd/masuda-net-helper) -- see that binary's
// doc comment for why TAP management lives in a separate binary rather than
// as a masuda subcommand.
const netHelperBinary = "masuda-net-helper"

// TapName derives the persistent TAP device name for a workspace id: one
// deterministic name per workspace, derived from the id alone. Keeping it
// deterministic is what lets EnsureTap's "delete any stale leftover, then
// create" sequence serve as the sole mechanism for allocation, release,
// *and* crash recovery, with no separate pool or PID-liveness bookkeeping
// (docs/adr/0048-vm-network-shared-bridge-dynamic-tap-privileged-helper.md).
//
// Linux interface names are capped at IFNAMSIZ-1 (15) bytes; "tap-" plus a
// workspace id (6 hex chars, internal/workspace.NewID) leaves comfortable
// headroom even after sanitization.
func TapName(id string) string {
	return "tap-" + nameSanitizer.ReplaceAllString(id, "-")
}

// EnsureTap creates a persistent TAP device for workspace id, attached to
// bridge and owned (openable without CAP_NET_ADMIN, per the standard
// `ip tuntap ... user <owner>` pattern) by ownerUser. Idempotent and safe
// to call on every VM start: any stale TAP left behind by a crashed
// previous run is deleted first, so this doubles as the "stale reclaim"
// step, not just allocation.
func EnsureTap(id, bridge, ownerUser string) (string, error) {
	if err := requireBridge(bridge); err != nil {
		return "", err
	}
	name := TapName(id)
	if _, err := runNetHelper("delete-tap", name); err != nil {
		return "", fmt.Errorf("clearing any stale tap device %s: %w", name, err)
	}
	if _, err := runNetHelper("create-tap", name, bridge, ownerUser); err != nil {
		return "", fmt.Errorf("creating tap device %s: %w", name, err)
	}
	return name, nil
}

// ReleaseTap deletes workspace id's TAP device. Not an error if it's
// already gone.
func ReleaseTap(id string) error {
	_, err := runNetHelper("delete-tap", TapName(id))
	return err
}

// requireBridge fails with the one instruction that fixes it when the shared
// bridge is missing.
//
// Everything scripts/setup-vm-host.sh sets up on the network side is kernel
// runtime state that a reboot discards -- and on WSL2, so does an idle
// shutdown, which happens far more often than a reboot (Issue #40). Without
// this check the first symptom is masuda-net-helper failing to attach a TAP
// to a bridge that is not there, which reads as a masuda bug rather than as
// "the host setup is gone". That misdirection has cost real time more than
// once.
//
// net.InterfaceByName rather than shelling out to `ip link show`: it needs
// no privileges, no external binary, and distinguishes "no such interface"
// from every other failure without parsing anyone's output.
func requireBridge(bridge string) error {
	if _, err := net.InterfaceByName(bridge); err != nil {
		return fmt.Errorf(
			"bridge %s does not exist -- the host's VM networking is not set up, or a reboot/WSL shutdown discarded it (Issue #40).\n"+
				"  Re-apply it with:  sudo systemctl restart masuda-vm-host\n"+
				"  If that unit does not exist yet, run ./scripts/setup-vm-host.sh from the masuda repository root once; it installs the unit.",
			bridge)
	}
	return nil
}

func runNetHelper(args ...string) (string, error) {
	if _, err := exec.LookPath(netHelperBinary); err != nil {
		return "", fmt.Errorf("%s not found on PATH (required for VM networking, Issue #31 -- see docs/INSTALLATION.md): %w", netHelperBinary, err)
	}
	cmd := exec.Command(netHelperBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", netHelperBinary, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}
