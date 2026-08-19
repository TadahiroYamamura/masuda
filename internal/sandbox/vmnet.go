package sandbox

import (
	"bytes"
	"fmt"
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

func runNetHelper(args ...string) (string, error) {
	if _, err := exec.LookPath(netHelperBinary); err != nil {
		return "", fmt.Errorf("%s not found on PATH (required for VM networking, Issue #31 -- see docs/CONTRIBUTING.md): %w", netHelperBinary, err)
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
