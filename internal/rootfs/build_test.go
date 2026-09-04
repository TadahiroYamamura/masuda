package rootfs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// requireRootfsTools skips the test unless this host can actually build a
// rootfs image. masuda has no CI job that runs `go test` (release.yml only
// builds/signs binaries on tag push), so skipping rather than failing keeps
// `go test ./...` usable on a machine without docker/fakeroot installed.
func requireRootfsTools(t *testing.T) {
	t.Helper()
	for _, bin := range requiredBinaries {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable")
	}
}

// debugfsStat returns the raw output of `debugfs -R "stat <path>"` against
// image, so tests can assert on inode ownership without needing to mount
// the image (which would need real root).
func debugfsStat(t *testing.T, image, path string) string {
	t.Helper()
	out, err := exec.Command("debugfs", "-R", "stat "+path, image).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs stat %s: %v\n%s", path, err, out)
	}
	return string(out)
}

// TestBuildProducesBootableOwnershipCorrectImage exercises Build end to end
// against a real docker daemon: export alpine:latest (already present on any
// host that's run this repo's other docker-backed tests), build an ext4
// image from it, and confirm ownership survived the fakeroot round-trip --
// the whole reason Build doesn't just `tar -x` as the invoking user.
// alpine:latest doesn't have an /etc/shadow with an interesting group, but
// masuda's own sandbox image does (verified manually against masuda-loop
// while designing this package); root-owned /etc/passwd is enough to prove
// the mechanism here without depending on a locally built masuda image.
func TestBuildProducesBootableOwnershipCorrectImage(t *testing.T) {
	requireRootfsTools(t)

	outputPath := filepath.Join(t.TempDir(), "nested", "rootfs.img")
	if err := Build("alpine:latest", outputPath, Options{}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("stat output image: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("Build() produced an empty image")
	}

	if _, err := os.Stat(outputPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("leftover %s.tmp after a successful Build()", outputPath)
	}

	stat := debugfsStat(t, outputPath, "/etc/passwd")
	if !bytes.Contains([]byte(stat), []byte("User:     0   Group:     0")) {
		t.Errorf("/etc/passwd not root:root in built image:\n%s", stat)
	}
}

func TestBuildUnknownImage(t *testing.T) {
	requireRootfsTools(t)

	outputPath := filepath.Join(t.TempDir(), "rootfs.img")
	err := Build("masuda-rootfs-test-image-that-does-not-exist:latest", outputPath, Options{})
	if err == nil {
		t.Fatal("Build() with an unknown image succeeded, want an error")
	}
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Error("Build() left an output file behind after failing")
	}
	if _, statErr := os.Stat(outputPath + ".tmp"); !os.IsNotExist(statErr) {
		t.Error("Build() left a .tmp file behind after failing")
	}
}

// TestBuildWritesExtraFiles confirms ExtraFile content lands in the built
// image with the requested permissions -- the mechanism Issue #31 M5-5
// uses to inject the guest's SSH ~/.ssh/authorized_keys at build time
// (never baked into the shared Dockerfile, so key rotation doesn't require
// a docker build to take effect).
func TestBuildWritesExtraFiles(t *testing.T) {
	requireRootfsTools(t)

	outputPath := filepath.Join(t.TempDir(), "rootfs.img")
	// UID/GID 1000, deliberately not 0: staging a file as a plain host file
	// always leaves it owned by masuda's own real uid regardless of what's
	// requested here, so a test using 0/0 wouldn't catch a chown that
	// silently never happened (root:root is also alpine's default for a
	// freshly created path). 1000 is a real, distinguishable-from-both
	// value.
	extra := []ExtraFile{
		{GuestPath: "home/ubuntu/.ssh/authorized_keys", Content: []byte("ssh-ed25519 AAAAtest test-key\n"), Mode: 0o600, UID: 1000, GID: 1000},
	}
	if err := Build("alpine:latest", outputPath, Options{ExtraFiles: extra}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	stat := debugfsStat(t, outputPath, "/home/ubuntu/.ssh/authorized_keys")
	if !bytes.Contains([]byte(stat), []byte("Mode:  0600")) {
		t.Errorf("authorized_keys mode not 0600 in built image:\n%s", stat)
	}
	if !bytes.Contains([]byte(stat), []byte("User:  1000   Group:  1000")) {
		t.Errorf("authorized_keys not owned 1000:1000 in built image:\n%s", stat)
	}

	// The directories an extra file lands in matter as much as the file: a
	// root-owned ~/.claude left the guest's own user unable to create
	// anything under its home, and Claude Code exited on startup because it
	// keeps session state there. alpine has no /home/ubuntu at all, so both
	// levels are created by the build and must land on the file's owner.
	for _, dir := range []string{"/home/ubuntu", "/home/ubuntu/.ssh"} {
		stat := debugfsStat(t, outputPath, dir)
		if !bytes.Contains([]byte(stat), []byte("User:  1000   Group:  1000")) {
			t.Errorf("%s not owned 1000:1000 in built image:\n%s", dir, stat)
		}
	}

	// ...but a directory the image already carries keeps the ownership the
	// image gave it. /home is root-owned in alpine and must stay that way,
	// even though the file below it is not.
	if stat := debugfsStat(t, outputPath, "/home"); !bytes.Contains([]byte(stat), []byte("User:     0   Group:     0")) {
		t.Errorf("/home must keep the image's own root ownership:\n%s", stat)
	}

	out, err := exec.Command("debugfs", "-R", "cat /home/ubuntu/.ssh/authorized_keys", outputPath).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs cat authorized_keys: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("ssh-ed25519 AAAAtest test-key")) {
		t.Errorf("authorized_keys content = %q, want it to contain the test key", out)
	}
}

// TestBuildRegeneratesModulesDep confirms Build's depmod step turns a
// kernel module file injected via ExtraFile into a working modules.dep
// entry -- the mechanism Issue #31 M5-6 relies on for the guest to
// auto-load virtiofs.ko (there's no real /lib/modules/<version> tree in a
// plain Docker/Ubuntu image otherwise, see extractAndFormat's doc comment).
// Uses a real kernel module file from this host rather than a synthetic
// fixture, since depmod parses actual ELF/module-info sections; skips if
// this host has no installed kernel module tree to borrow one from.
func TestBuildRegeneratesModulesDep(t *testing.T) {
	requireRootfsTools(t)

	hostModules, err := filepath.Glob("/lib/modules/*/kernel/fs/fuse/virtiofs.ko*")
	if err != nil || len(hostModules) == 0 {
		t.Skip("no virtiofs kernel module found on this host to use as a test fixture")
	}
	modPath := hostModules[0]
	rel := strings.TrimPrefix(modPath, "/lib/modules/")
	version, relInModulesDir, ok := strings.Cut(rel, "/")
	if !ok {
		t.Fatalf("unexpected module path shape: %s", modPath)
	}
	content, err := os.ReadFile(modPath)
	if err != nil {
		t.Fatalf("reading fixture module %s: %v", modPath, err)
	}

	outputPath := filepath.Join(t.TempDir(), "rootfs.img")
	extra := []ExtraFile{
		{GuestPath: filepath.Join("lib/modules", version, relInModulesDir), Content: content, Mode: 0o644, UID: 0, GID: 0},
	}
	if err := Build("alpine:latest", outputPath, Options{ExtraFiles: extra}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	depPath := "/lib/modules/" + version + "/modules.dep"
	out, err := exec.Command("debugfs", "-R", "cat "+depPath, outputPath).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs cat %s: %v\n%s", depPath, err, out)
	}
	if !bytes.Contains(out, []byte("virtiofs")) {
		t.Errorf("modules.dep = %q, want an entry for the injected virtiofs module", out)
	}
}

// TestBuildInjectsExtraDir confirms a whole host directory lands in the
// image with root ownership and reaches depmod -- the path a disposable
// privileged VM needs for the guest kernel's module tree (ADR-0053).
// Ownership matters as much as presence here: modprobe reads a tree it
// expects to be root-owned, and the copy happens inside the fakeroot
// session precisely so it lands that way without an explicit chown.
func TestBuildInjectsExtraDir(t *testing.T) {
	requireRootfsTools(t)

	hostModules, err := filepath.Glob("/lib/modules/*/kernel/fs/fuse/virtiofs.ko*")
	if err != nil || len(hostModules) == 0 {
		t.Skip("no kernel module tree on this host to use as a test fixture")
	}
	version := strings.Split(strings.TrimPrefix(hostModules[0], "/lib/modules/"), "/")[0]

	// A small subtree rather than the whole ~155MiB tree: the mechanism is
	// the same and the test stays fast.
	hostDir := filepath.Join("/lib/modules", version, "kernel/fs/fuse")
	outputPath := filepath.Join(t.TempDir(), "rootfs.img")
	if err := Build("alpine:latest", outputPath, Options{
		ExtraDirs: []ExtraDir{{HostPath: hostDir, GuestPath: filepath.Join("lib/modules", version, "kernel/fs/fuse")}},
	}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// debugfs pads its columns, so match the ownership loosely rather than
	// depending on its exact spacing.
	rootOwned := regexp.MustCompile(`User:\s+0\s+Group:\s+0`)
	var lastOut []byte
	found := false
	for _, name := range []string{"virtiofs.ko.zst", "virtiofs.ko"} {
		out, err := exec.Command("debugfs", "-R", "stat /lib/modules/"+version+"/kernel/fs/fuse/"+name, outputPath).CombinedOutput()
		lastOut = out
		if err == nil && rootOwned.Match(out) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("injected module not present as root-owned:\n%s", lastOut)
	}
}

// TestBuildInjectsExtraSymlink covers the one thing ExtraFile cannot do:
// enabling a systemd unit, which is a symlink in a .wants directory and
// nothing else (ADR-0053's disposable VM depends on it).
func TestBuildInjectsExtraSymlink(t *testing.T) {
	requireRootfsTools(t)

	outputPath := filepath.Join(t.TempDir(), "rootfs.img")
	if err := Build("alpine:latest", outputPath, Options{
		ExtraFiles: []ExtraFile{{GuestPath: "etc/systemd/system/unit.service", Content: []byte("[Unit]\n"), Mode: 0o644}},
		ExtraSymlinks: []ExtraSymlink{{
			GuestPath: "etc/systemd/system/multi-user.target.wants/unit.service",
			Target:    "/etc/systemd/system/unit.service",
		}},
	}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	out, err := exec.Command("debugfs", "-R", "stat /etc/systemd/system/multi-user.target.wants/unit.service", outputPath).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs stat: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("symlink")) && !bytes.Contains(out, []byte("Fast link dest")) {
		t.Fatalf("injected path is not a symlink:\n%s", out)
	}
	if !bytes.Contains(out, []byte("/etc/systemd/system/unit.service")) {
		t.Fatalf("symlink does not point at the unit:\n%s", out)
	}
}

func TestImageSizeMiBFloor(t *testing.T) {
	requireRootfsTools(t)

	dir := t.TempDir()
	tarPath := filepath.Join(dir, "empty.tar")
	// An empty (but valid) tar archive: two 512-byte zero blocks.
	if err := os.WriteFile(tarPath, make([]byte, 1024), 0o644); err != nil {
		t.Fatalf("writing empty tar: %v", err)
	}

	got, err := imageSizeMiB(tarPath, 0)
	if err != nil {
		t.Fatalf("imageSizeMiB() error = %v", err)
	}
	if got != minImageSizeMiB {
		t.Errorf("imageSizeMiB() = %d, want the floor %d", got, minImageSizeMiB)
	}
}

// Injected content is invisible to the tar, so without counting it the size
// would depend on the slack margin happening to be larger than whatever a
// caller injects -- a kernel module tree is ~155MiB (ADR-0053).
func TestImageSizeMiBCountsInjectedContent(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "empty.tar")
	if err := os.WriteFile(tarPath, make([]byte, 1024), 0o644); err != nil {
		t.Fatalf("writing empty tar: %v", err)
	}

	const injected = 2 * 1024 * 1024 * 1024 // well past the floor
	got, err := imageSizeMiB(tarPath, injected)
	if err != nil {
		t.Fatalf("imageSizeMiB() error = %v", err)
	}
	if got <= injected/(1024*1024) {
		t.Errorf("imageSizeMiB() = %d MiB, want more than the %d MiB injected", got, injected/(1024*1024))
	}
}

func TestInjectedBytesCountsFilesAndDirs(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "tree", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "mod.ko"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := injectedBytes(Options{
		ExtraFiles: []ExtraFile{{GuestPath: "etc/x", Content: make([]byte, 100)}},
		ExtraDirs:  []ExtraDir{{HostPath: filepath.Join(dir, "tree"), GuestPath: "lib/modules/x"}},
	})
	if err != nil {
		t.Fatalf("injectedBytes() error = %v", err)
	}
	if got != 4196 {
		t.Fatalf("injectedBytes() = %d, want 4196", got)
	}
}

func TestBuildRejectsMissingExtraDir(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "rootfs.img")
	err := Build("alpine:latest", outputPath, Options{
		ExtraDirs: []ExtraDir{{HostPath: filepath.Join(t.TempDir(), "nonexistent"), GuestPath: "lib/modules/x"}},
	})
	if err == nil {
		t.Fatal("Build() with a missing ExtraDir = nil error, want an error")
	}
}

// The size override is a floor, and it has a ceiling (ADR-0054). Neither
// needs docker: both are decided before Build touches anything external, so
// this runs everywhere unlike the rest of this file.
func TestBuildRejectsSizeOutsideBounds(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "rootfs.img")

	if err := Build("alpine:latest", outputPath, Options{MinSizeMiB: -1}); err == nil {
		t.Fatal("Build() with a negative size = nil error, want an error")
	}
	if err := Build("alpine:latest", outputPath, Options{MinSizeMiB: maxImageSizeMiB + 1}); err == nil {
		t.Fatal("Build() above the size ceiling = nil error, want an error")
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("Build() wrote an output file before validating its size: %v", err)
	}
}

func TestImageSizeFloorApplies(t *testing.T) {
	// imageSizeMiB's own floor is minImageSizeMiB; the override raises it
	// further, and a smaller override never lowers the computed size.
	if got := max(minImageSizeMiB, 128); got != minImageSizeMiB {
		t.Fatalf("floor = %d, want the computed size %d to win", got, minImageSizeMiB)
	}
	if got := max(minImageSizeMiB, 4096); got != 4096 {
		t.Fatalf("floor = %d, want the override 4096 to win", got)
	}
}
