// Package microvm boots and tears down Cloud Hypervisor microVMs built from a
// Docker image, with host directories shared in over virtiofs and networking
// through a pre-configured bridge. It knows nothing about masuda's
// workspaces, configuration files, or what runs inside the guest: callers
// describe a VM as a Spec, and everything masuda-specific (credentials, the
// MCP relay, which image entry to use, egress allowlists) lives in
// internal/sandbox, which builds those Specs. Keeping that boundary is what
// lets this package be lifted out into a standalone tool later without first
// untangling it from masuda -- TestNoMasudaImports enforces it.
//
// The guest image has to provide, on its own: systemd, an sshd that accepts
// Host.GuestUser's authorized_keys, a DHCP client on the first NIC, fstab
// entries mounting each Share's tag, and a passwordless `sudo systemctl
// poweroff` for Host.GuestUser (Shutdown relies on it -- killing the VMM
// outright corrupts the disk image).
package microvm

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/rootfs"
)

// Host describes the one-time host setup every VM on this machine relies on
// (for masuda, what scripts/setup-vm-host.sh creates). None of it is
// per-VM.
type Host struct {
	// DataDir holds the guest kernel (vmlinuz-*), the SSH keypair, and one
	// working directory per VM under vm/<id>.
	DataDir string
	// Bridge is the existing bridge each VM's TAP device is attached to.
	Bridge string
	// LeaseFile is the DHCP server's lease file (dnsmasq format) on Bridge,
	// the only place a guest's IP address can be learned from.
	LeaseFile string
	// NetHelper is the CAP_NET_ADMIN-carrying binary TAP devices are created
	// and deleted through (its create-tap/delete-tap subcommands).
	NetHelper string
	// BridgeMissingHint is appended to the error returned when Bridge does
	// not exist, and should tell the user how to recreate it.
	BridgeMissingHint string
	// SSHKeyComment is written into the generated SSH keypair.
	SSHKeyComment string
	GuestUser     GuestUser
}

// GuestUser is the account in the guest image that receives the host's SSH
// public key and that SSH sessions log in as.
type GuestUser struct {
	Name string
	UID  int
	GID  int
}

// Share exposes HostDir to the guest over virtiofs under Tag. The guest's
// fstab decides where it is mounted.
type Share struct {
	Tag     string
	HostDir string
}

// ModuleSet selects which of the host kernel's modules go into the guest's
// rootfs. The guest runs the host's own kernel (see findKernel), so its
// modules have to come from the host too.
type ModuleSet int

const (
	// VirtiofsModuleOnly injects only virtiofs.ko: enough to mount Shares,
	// and small enough to add to every rootfs build.
	VirtiofsModuleOnly ModuleSet = iota
	// WholeModuleTree injects all of /lib/modules/<version>, for workloads
	// (a Docker daemon, for one) whose module needs cannot be known in
	// advance.
	WholeModuleTree
)

// Spec is one VM.
type Spec struct {
	// ID keys the VM's working directory, TAP device, and MAC address. It has
	// to be short enough for TapName to fit an interface name.
	ID string
	// Image is a local Docker image reference the rootfs is exported from.
	Image string
	// Rootfs carries the files the caller wants injected and the minimum
	// image size; the SSH public key and kernel modules are added to it.
	Rootfs  rootfs.Options
	Modules ModuleSet
	Shares  []Share
	// KernelArgs are appended to the kernel command line.
	KernelArgs []string
	// SerialLog is where the guest's serial console is written. Defaults to
	// console.log in the VM's working directory.
	SerialLog string
}

const cloudHypervisorBinary = "cloud-hypervisor"

// Check fails if this host cannot boot a VM at all, so a caller can refuse
// early -- before allocating anything of its own that would then have to be
// cleaned up.
func (h Host) Check() error {
	if _, err := exec.LookPath(cloudHypervisorBinary); err != nil {
		return fmt.Errorf("%s not found on PATH: %w", cloudHypervisorBinary, err)
	}
	_, err := h.findKernel()
	return err
}

// WorkDir is where everything this package manages for VM id lives (rootfs
// image, virtiofsd sockets and logs, the VMM's pidfile), created if absent.
// Callers may keep their own per-VM files here too; Remove deletes them
// along with the rest.
func (h Host) WorkDir(id string) (string, error) {
	dir := filepath.Join(h.DataDir, "vm", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// hostModulesRoot is where the host's kernel modules live; a variable so
// tests can point it at a fixture tree.
var hostModulesRoot = "/lib/modules"

// findKernel returns the newest vmlinuz-* under DataDir whose modules are
// still installed on the host. The one under /boot is typically readable by
// root only, so host setup copies it here -- and that copy outlives the
// kernel it came from: when a package upgrade removes an old kernel's
// modules (#57), its vmlinuz stays behind here, and booting it fails for
// want of a virtiofs.ko to inject. Such a copy is skipped rather than
// chosen.
//
// Newest is by version, not by name: "6.8.0-99" sorts after "6.8.0-142" as
// a string. scripts/setup-vm-host.sh picks with `sort -V`, and the two have
// to agree.
func (h Host) findKernel() (string, error) {
	matches, err := filepath.Glob(filepath.Join(h.DataDir, "vmlinuz-*"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no vmlinuz-* found under %s -- run scripts/setup-vm-host.sh first", h.DataDir)
	}
	sort.Slice(matches, func(i, j int) bool {
		return compareVersions(kernelVersionFromPath(matches[i]), kernelVersionFromPath(matches[j])) < 0
	})
	for i := len(matches) - 1; i >= 0; i-- {
		if info, err := os.Stat(filepath.Join(hostModulesRoot, kernelVersionFromPath(matches[i]))); err == nil && info.IsDir() {
			return matches[i], nil
		}
	}
	return "", fmt.Errorf("none of the kernels copied under %s (%s) has its modules under %s any more -- "+
		"the host kernel was probably upgraded and the old one removed. "+
		"Copy the current one by running ./scripts/setup-vm-host.sh from the masuda repository root (its kernel step does this)",
		h.DataDir, strings.Join(kernelVersions(matches), ", "), hostModulesRoot)
}

func kernelVersions(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = kernelVersionFromPath(p)
	}
	return out
}

// compareVersions orders version strings the way `sort -V` does for kernel
// release names: runs of digits compare as numbers, everything else as text.
func compareVersions(a, b string) int {
	for a != "" && b != "" {
		ra, restA := leadingRun(a)
		rb, restB := leadingRun(b)
		if c := compareRuns(ra, rb); c != 0 {
			return c
		}
		a, b = restA, restB
	}
	return strings.Compare(a, b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// leadingRun splits off s's leading run of all-digit or all-non-digit bytes.
func leadingRun(s string) (string, string) {
	i := 1
	for i < len(s) && isDigit(s[i]) == isDigit(s[0]) {
		i++
	}
	return s[:i], s[i:]
}

func compareRuns(a, b string) int {
	if isDigit(a[0]) && isDigit(b[0]) {
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if len(a) != len(b) {
			return len(a) - len(b)
		}
	}
	return strings.Compare(a, b)
}

func currentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("determining current user: %w", err)
	}
	return u.Username, nil
}

// kernelVersionFromPath derives the `uname -r`-style version string from a
// vmlinuz-<version> path (see findKernel), e.g.
// "/…/vmlinuz-6.8.0-138-generic" -> "6.8.0-138-generic". This is also the
// exact host /lib/modules/<version> directory name -- scripts/setup-vm-host.sh
// installs the kernel via linux-image-generic, which always installs a
// matching linux-modules-<version>-generic package alongside it.
func kernelVersionFromPath(kernelPath string) string {
	return strings.TrimPrefix(filepath.Base(kernelPath), "vmlinuz-")
}

// addModules adds the host kernel modules set selects to opts.
//
// GuestPath is usr/lib/modules/..., not lib/modules/...: an Ubuntu 24.04
// image is usrmerged, so /lib is a symlink to /usr/lib, and staging a real
// directory tree at "lib/..." makes rootfs.Build's `cp -a` fail trying to
// overwrite that symlink with a directory (confirmed live -- alpine, used in
// internal/rootfs's own unit tests, has no such symlink, so this only
// surfaces against a real usrmerge image). depmod/modprobe resolve straight
// through the symlink either way.
func addModules(opts *rootfs.Options, set ModuleSet, kernelVersion string) error {
	switch set {
	case WholeModuleTree:
		modulesDir := filepath.Join(hostModulesRoot, kernelVersion)
		if _, err := os.Stat(modulesDir); err != nil {
			return fmt.Errorf("no kernel module tree at %s -- is linux-modules-%s installed?: %w", modulesDir, kernelVersion, err)
		}
		opts.ExtraDirs = append(opts.ExtraDirs, rootfs.ExtraDir{HostPath: modulesDir, GuestPath: filepath.Join("usr/lib/modules", kernelVersion)})
		return nil
	default:
		f, err := virtiofsModuleExtraFile(kernelVersion)
		if err != nil {
			return fmt.Errorf("locating virtiofs kernel module: %w", err)
		}
		opts.ExtraFiles = append(opts.ExtraFiles, f)
		return nil
	}
}

// virtiofsModuleExtraFile locates this host's copy of the guest kernel's
// virtiofs.ko and returns it as an ExtraFile -- see internal/rootfs.Build's
// depmod step for why this alone is enough for the guest to auto-load it
// (Issue #31 M5-6). A plain Docker image has no kernel module tree at all
// otherwise, and virtio-fs support isn't built into a generic distro kernel,
// so no Share would mount without this.
func virtiofsModuleExtraFile(kernelVersion string) (rootfs.ExtraFile, error) {
	pattern := filepath.Join(hostModulesRoot, kernelVersion, "kernel/fs/fuse/virtiofs.ko*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return rootfs.ExtraFile{}, err
	}
	if len(matches) == 0 {
		return rootfs.ExtraFile{}, fmt.Errorf("no virtiofs kernel module found at %s -- is linux-modules-%s installed?", pattern, kernelVersion)
	}
	content, err := os.ReadFile(matches[0])
	if err != nil {
		return rootfs.ExtraFile{}, fmt.Errorf("reading %s: %w", matches[0], err)
	}
	rel, err := filepath.Rel(filepath.Join(hostModulesRoot, kernelVersion), matches[0])
	if err != nil {
		return rootfs.ExtraFile{}, err
	}
	return rootfs.ExtraFile{
		GuestPath: filepath.Join("usr/lib/modules", kernelVersion, rel),
		Content:   content,
		Mode:      0o644,
		UID:       0,
		GID:       0,
	}, nil
}
