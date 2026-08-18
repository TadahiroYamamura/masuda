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

// tapAliasMarker is written to every tap this helper creates (as the
// interface's `ip link ... alias`, not part of its name) and checked before
// ever touching an existing link by name. Workspace ids are only 6 hex
// characters, and "tap-" is a common enough prefix (libvirt/QEMU setups use
// it too) that a name collision with something unrelated on a shared host
// isn't purely theoretical. The alias is what actually proves a link is
// ours, independent of what it happens to be named -- name collisions get
// refused loudly instead of silently deleting or reusing someone else's
// interface.
const tapAliasMarker = "masuda-managed-tap"

func isMasudaTap(link netlink.Link) bool {
	return link.Type() == "tuntap" && link.Attrs().Alias == tapAliasMarker
}

// createTap is idempotent: if name already exists *and is ours* (see
// isMasudaTap), it's left alone. This is a defensive fallback only --
// internal/sandbox.EnsureTap always deletes any stale leftover before
// calling this, so the "already exists" path here is normally unreachable;
// it's here so a racing/duplicate call can't fail loudly for no operational
// reason. If it exists and isn't ours, that's a name collision with
// something unrelated -- refuse rather than silently proceeding as if it
// were fine.
func createTap(name, bridge, ownerUser string) error {
	if existing, err := netlink.LinkByName(name); err == nil {
		if !isMasudaTap(existing) {
			return fmt.Errorf("refusing to reuse %s: exists but isn't a masuda-managed tap (type=%s alias=%q)", name, existing.Type(), existing.Attrs().Alias)
		}
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
		LinkAttrs: netlink.LinkAttrs{Name: name, MasterIndex: br.Attrs().Index, Alias: tapAliasMarker},
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
	// Belt and suspenders: some netlink kernel paths don't apply
	// LinkAttrs.Alias from LinkAdd for a tuntap link, so set it explicitly
	// too and fail loudly if it didn't take -- the alias is the safety
	// mechanism deleteTap relies on, it must actually be there.
	if err := netlink.LinkSetAlias(tap, tapAliasMarker); err != nil {
		return fmt.Errorf("marking tap %s as masuda-managed: %w", name, err)
	}
	if err := netlink.LinkSetUp(tap); err != nil {
		return fmt.Errorf("bringing up tap %s: %w", name, err)
	}
	return nil
}

// deleteTap is not an error if name is already gone -- this is what makes
// it safe to call unconditionally as the "clear any stale leftover" half of
// EnsureTap's create sequence. If a link with that name exists but isn't a
// masuda-managed tap (see isMasudaTap), it refuses to touch it: a name
// collision with something unrelated must fail loudly, not delete whatever
// happens to be sitting there.
func deleteTap(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil
	}
	if !isMasudaTap(link) {
		return fmt.Errorf("refusing to delete %s: not a masuda-managed tap (type=%s alias=%q) -- this looks like it belongs to something else", name, link.Type(), link.Attrs().Alias)
	}
	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("deleting tap %s: %w", name, err)
	}
	return nil
}
