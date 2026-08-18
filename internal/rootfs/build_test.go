package rootfs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
	if err := Build("alpine:latest", outputPath); err != nil {
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
	err := Build("masuda-rootfs-test-image-that-does-not-exist:latest", outputPath)
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
