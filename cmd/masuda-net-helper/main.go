// Command masuda-net-helper performs the CAP_NET_ADMIN-requiring TAP
// device operations Issue #31's VM backend needs (create+attach-to-bridge,
// delete), so masuda itself never needs elevated privileges.
//
// Deliberately its own small binary, not a masuda subcommand: a Linux file
// capability granted via setcap applies to a binary, not to one code path
// within it. Folding this into masuda's single large CLI binary would hand
// CAP_NET_ADMIN to all of masuda's code -- every subcommand, every
// subagent-invoked helper -- not just TAP management. Keeping this binary
// tiny and single-purpose keeps that blast radius small and auditable.
//
// All TAP/bridge operations go through the netlink library directly
// (github.com/vishvananda/netlink), not by shelling out to `ip`: a setcap'd
// binary's capability is only in *its own* effective set, and does not
// propagate to a plain exec.Command child (that needs the capability to
// already be in the calling process's inheritable set too, which a normal
// unprivileged shell never has -- confirmed live: PR_CAP_AMBIENT_RAISE
// fails with EPERM from a plain shell-launched process no matter what
// setcap flags are used). Doing the netlink calls in-process, where
// CAP_NET_ADMIN is actually held, sidesteps that entirely.
//
// Not part of the public CLI surface -- masuda's Go code invokes it (see
// internal/sandbox.EnsureTap/ReleaseTap), a human isn't meant to type it
// directly. Requires, once, after building it:
//
//	sudo setcap cap_net_admin+ep <path to this binary>
//
// See docs/CONTRIBUTING.md.
package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"

	"github.com/vishvananda/netlink"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "create-tap":
		if len(os.Args) != 5 {
			usage()
		}
		err = createTap(os.Args[2], os.Args[3], os.Args[4])
	case "delete-tap":
		if len(os.Args) != 3 {
			usage()
		}
		err = deleteTap(os.Args[2])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "masuda-net-helper:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: masuda-net-helper create-tap <name> <bridge> <owner-user>")
	fmt.Fprintln(os.Stderr, "       masuda-net-helper delete-tap <name>")
	os.Exit(2)
}

// createTap is idempotent: if name already exists, it's left alone. This is
// a defensive fallback only -- internal/sandbox.EnsureTap always deletes any
// stale leftover before calling this, so the "already exists" path here is
// normally unreachable; it's here so a racing/duplicate call can't fail
// loudly for no operational reason.
func createTap(name, bridge, ownerUser string) error {
	if _, err := netlink.LinkByName(name); err == nil {
		return nil
	}

	br, err := netlink.LinkByName(bridge)
	if err != nil {
		return fmt.Errorf("bridge %s not found: %w", bridge, err)
	}

	u, err := user.Lookup(ownerUser)
	if err != nil {
		return fmt.Errorf("looking up user %s: %w", ownerUser, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return fmt.Errorf("parsing uid %q for %s: %w", u.Uid, ownerUser, err)
	}

	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: name, MasterIndex: br.Attrs().Index},
		Mode:      netlink.TUNTAP_MODE_TAP,
		Owner:     uint32(uid),
		// NonPersist defaults to false, i.e. persistent -- matches `ip
		// tuntap add` (no `one_queue`/`pi` flags either, matching the
		// prior `ip tuntap add dev ... mode tap user ...` invocation this
		// replaces).
	}
	if err := netlink.LinkAdd(tap); err != nil {
		return fmt.Errorf("creating tap %s: %w", name, err)
	}
	if err := netlink.LinkSetUp(tap); err != nil {
		return fmt.Errorf("bringing up tap %s: %w", name, err)
	}
	return nil
}

// deleteTap is not an error if name is already gone -- this is what makes
// it safe to call unconditionally as the "clear any stale leftover" half of
// EnsureTap's create sequence.
func deleteTap(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil
	}
	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("deleting tap %s: %w", name, err)
	}
	return nil
}
