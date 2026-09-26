package microvm

import (
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
)

// nameSanitizer keeps TapName within the character set interface names
// allow; callers' ids are expected to be safe already, but this stays
// defensive in case one isn't.
var nameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// TapName derives the persistent TAP device name for VM id: one
// deterministic name per VM, derived from the id alone. Keeping it
// deterministic is what lets ensureTap's "delete any stale leftover, then
// create" sequence serve as the sole mechanism for allocation, release,
// *and* crash recovery, with no separate pool or PID-liveness bookkeeping
// (docs/adr/0048-vm-network-shared-bridge-dynamic-tap-privileged-helper.md).
//
// Linux interface names are capped at IFNAMSIZ-1 (15) bytes, so ids longer
// than 11 bytes do not fit.
func TapName(id string) string {
	return "tap-" + nameSanitizer.ReplaceAllString(id, "-")
}

// ensureTap creates a persistent TAP device for VM id, attached to
// h.Bridge and owned (openable without CAP_NET_ADMIN, per the standard
// `ip tuntap ... user <owner>` pattern) by ownerUser. Idempotent and safe
// to call on every VM start: any stale TAP left behind by a crashed
// previous run is deleted first, so this doubles as the "stale reclaim"
// step, not just allocation.
func (h Host) ensureTap(id, ownerUser string) (string, error) {
	if err := h.requireBridge(); err != nil {
		return "", err
	}
	name := TapName(id)
	if _, err := h.runNetHelper("delete-tap", name); err != nil {
		return "", fmt.Errorf("clearing any stale tap device %s: %w", name, err)
	}
	if _, err := h.runNetHelper("create-tap", name, h.Bridge, ownerUser); err != nil {
		return "", fmt.Errorf("creating tap device %s: %w", name, err)
	}
	return name, nil
}

// releaseTap deletes VM id's TAP device. Not an error if it's already gone.
func (h Host) releaseTap(id string) error {
	_, err := h.runNetHelper("delete-tap", TapName(id))
	return err
}

// requireBridge fails with h.BridgeMissingHint when the shared bridge is
// missing.
//
// Bridge setup is kernel runtime state that a reboot discards -- and on
// WSL2, so does an idle shutdown, which happens far more often than a
// reboot (Issue #40). Without this check the first symptom is the net helper
// failing to attach a TAP to a bridge that is not there, which reads as a
// bug rather than as "the host setup is gone". That misdirection has cost
// real time more than once.
//
// net.InterfaceByName rather than shelling out to `ip link show`: it needs
// no privileges, no external binary, and distinguishes "no such interface"
// from every other failure without parsing anyone's output.
func (h Host) requireBridge() error {
	if _, err := net.InterfaceByName(h.Bridge); err != nil {
		return fmt.Errorf(
			"bridge %s does not exist -- the host's VM networking is not set up, or a reboot/WSL shutdown discarded it (Issue #40).\n%s",
			h.Bridge, h.BridgeMissingHint)
	}
	return nil
}

func (h Host) runNetHelper(args ...string) (string, error) {
	if _, err := exec.LookPath(h.NetHelper); err != nil {
		return "", fmt.Errorf("%s not found on PATH (required for VM networking, Issue #31 -- see docs/INSTALLATION.md): %w", h.NetHelper, err)
	}
	cmd := exec.Command(h.NetHelper, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", h.NetHelper, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}
