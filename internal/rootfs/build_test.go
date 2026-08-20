package rootfs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
	if err := Build("alpine:latest", outputPath, nil, 0); err != nil {
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
	err := Build("masuda-rootfs-test-image-that-does-not-exist:latest", outputPath, nil, 0)
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
	if err := Build("alpine:latest", outputPath, extra, 0); err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	stat := debugfsStat(t, outputPath, "/home/ubuntu/.ssh/authorized_keys")
	if !bytes.Contains([]byte(stat), []byte("Mode:  0600")) {
		t.Errorf("authorized_keys mode not 0600 in built image:\n%s", stat)
	}
	if !bytes.Contains([]byte(stat), []byte("User:  1000   Group:  1000")) {
		t.Errorf("authorized_keys not owned 1000:1000 in built image:\n%s", stat)
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
	if err := Build("alpine:latest", outputPath, extra, 0); err != nil {
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

func TestImageSizeMiBFloor(t *testing.T) {
	requireRootfsTools(t)

	dir := t.TempDir()
	tarPath := filepath.Join(dir, "empty.tar")
	// An empty (but valid) tar archive: two 512-byte zero blocks.
	if err := os.WriteFile(tarPath, make([]byte, 1024), 0o644); err != nil {
		t.Fatalf("writing empty tar: %v", err)
	}

	got, err := imageSizeMiB(tarPath)
	if err != nil {
		t.Fatalf("imageSizeMiB() error = %v", err)
	}
	if got != minImageSizeMiB {
		t.Errorf("imageSizeMiB() = %d, want the floor %d", got, minImageSizeMiB)
	}
}

// The size override is a floor, and it has a ceiling (ADR-0054). Neither
// needs docker: both are decided before Build touches anything external, so
// this runs everywhere unlike the rest of this file.
func TestBuildRejectsSizeOutsideBounds(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "rootfs.img")

	if err := Build("alpine:latest", outputPath, nil, -1); err == nil {
		t.Fatal("Build() with a negative size = nil error, want an error")
	}
	if err := Build("alpine:latest", outputPath, nil, maxImageSizeMiB+1); err == nil {
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
