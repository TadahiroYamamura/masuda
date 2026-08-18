// Package rootfs converts a masuda sandbox Docker image into a bootable
// ext4 disk image for the Cloud Hypervisor microVM backend (Issue #31, M2).
//
// masuda's existing Dockerfile build (the base image plus the
// docker/{go,python,typescript,full} language variants) stays the single
// source of truth for what's inside a sandbox -- claude CLI version, LSP
// plugins, language toolchains. Rather than maintaining a second,
// VM-specific rootfs recipe that would drift from the Dockerfile over time,
// Build takes an already-built image and converts its filesystem straight
// into a disk image via `docker export`.
package rootfs

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// rootfsLabel is the ext4 volume label baked into every image Build
// produces, so a guest kernel command line can find its root device by
// LABEL= rather than an assumption about device ordering.
const rootfsLabel = "masuda-rootfs"

// minImageSizeMiB is a floor under the computed size, so a tiny image (e.g.
// a test fixture) still gets enough headroom for ext4's own metadata and a
// few writes during boot.
const minImageSizeMiB = 512

// sizeSlackNumerator/Denominator and sizeSlackFixedMiB pad the image beyond
// the exported content's exact byte count: ext4 metadata (inode tables,
// journal, block bitmaps) isn't part of that count, and the image needs
// room to boot and run, not just to hold a byte-for-byte copy of the image.
// 20% plus a fixed 256MiB was enough margin in practice (see
// internal/rootfs/build_test.go and the Issue #31 spike notes) without
// wasting excessive disk on every build.
const (
	sizeSlackNumerator   = 6
	sizeSlackDenominator = 5
	sizeSlackFixedMiB    = 256
)

// requiredBinaries lists external commands Build shells out to. Checked up
// front so a missing dependency fails fast with one clear error, not after
// minutes of `docker export`.
var requiredBinaries = []string{"docker", "fakeroot", "mkfs.ext4"}

// Build exports image's container filesystem and writes it as a raw ext4
// disk image at outputPath, ready to hand to a VM as a virtio-blk root
// device. outputPath is written atomically: the image is built at
// outputPath+".tmp" in the same directory (so the final rename can't cross
// a filesystem boundary) and only renamed into place once mkfs.ext4
// succeeds, so a failed or interrupted Build never leaves a partial image
// where a caller expects a finished one.
//
// Ownership matters for a real boot -- /etc/shadow must stay root:shadow,
// setuid binaries must stay root-owned -- but a plain `tar -x` run as an
// unprivileged user can't set arbitrary ownership, and masuda has no
// delegated subuid/subgid range to assume for a user-namespace remap (that
// would add a host setup step this package has no way to verify). Build
// instead runs both the tar extraction and the mkfs.ext4 build inside one
// fakeroot session: fakeroot fakes chown/stat within that session, so
// mkfs.ext4 -d sees -- and bakes into the image's inodes -- the tarball's
// real recorded ownership, not the invoking user's. Confirmed against a
// real masuda sandbox image with debugfs: /etc/shadow lands as
// user=0/group=42 (shadow), not the host user's uid/gid.
func Build(image, outputPath string) error {
	for _, bin := range requiredBinaries {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not found on PATH (required to build a rootfs image): %w", bin, err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	workDir, err := os.MkdirTemp("", "masuda-rootfs-*")
	if err != nil {
		return fmt.Errorf("creating work directory: %w", err)
	}
	defer os.RemoveAll(workDir)

	tarPath := filepath.Join(workDir, "rootfs.tar")
	if err := dockerExport(image, tarPath); err != nil {
		return err
	}

	sizeMiB, err := imageSizeMiB(tarPath)
	if err != nil {
		return fmt.Errorf("sizing image from %s: %w", tarPath, err)
	}

	extractDir := filepath.Join(workDir, "extracted")
	if err := os.Mkdir(extractDir, 0o755); err != nil {
		return fmt.Errorf("creating extraction directory: %w", err)
	}

	tmpImagePath := outputPath + ".tmp"
	defer os.Remove(tmpImagePath) // no-op once the rename below succeeds

	if err := extractAndFormat(extractDir, tarPath, tmpImagePath, sizeMiB); err != nil {
		return err
	}

	if err := os.Rename(tmpImagePath, outputPath); err != nil {
		return fmt.Errorf("moving finished image into place: %w", err)
	}
	return nil
}

// dockerExport writes image's merged container filesystem to tarPath as a
// tar archive. It creates a (never started) container to export from and
// always removes it again, even if the export itself fails.
func dockerExport(image, tarPath string) error {
	create := exec.Command("docker", "create", image)
	var stdout, stderr bytes.Buffer
	create.Stdout = &stdout
	create.Stderr = &stderr
	if err := create.Run(); err != nil {
		return fmt.Errorf("docker create %s: %w\n%s", image, err, stderr.String())
	}
	containerID := firstLine(stdout.String())
	defer func() { _ = exec.Command("docker", "rm", "-f", containerID).Run() }()

	stderr.Reset()
	export := exec.Command("docker", "export", containerID, "-o", tarPath)
	export.Stderr = &stderr
	if err := export.Run(); err != nil {
		return fmt.Errorf("docker export %s: %w\n%s", containerID, err, stderr.String())
	}
	return nil
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

// imageSizeMiB sums the regular-file content bytes recorded in the tar at
// tarPath and returns a padded image size in MiB (see the sizeSlack*
// constants). Reading tar headers directly, rather than extracting first,
// avoids doing the multi-gigabyte extraction twice (once to measure, once
// for real inside extractAndFormat's fakeroot session).
func imageSizeMiB(tarPath string) (int, error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var total int64
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		if hdr.Typeflag == tar.TypeReg {
			total += hdr.Size
		}
	}

	mib := int(total*sizeSlackNumerator/sizeSlackDenominator/(1024*1024)) + sizeSlackFixedMiB
	return max(mib, minImageSizeMiB), nil
}

// extractAndFormat extracts tarPath into extractDir and formats it as an
// ext4 image at imagePath sized sizeMiB, both inside a single fakeroot
// session so the ownership mkfs.ext4 bakes in is the tarball's real
// ownership (see Build's doc comment). Arguments are passed to the fakeroot
// shell as positional parameters ($1, $2, ...), not interpolated into the
// script string, so paths containing shell-special characters can't break
// or inject into the command.
func extractAndFormat(extractDir, tarPath, imagePath string, sizeMiB int) error {
	const script = `set -e
tar -C "$1" -xpf "$2"
mkfs.ext4 -q -F -d "$1" -L "$3" "$4" "$5"M
`
	cmd := exec.Command("fakeroot", "sh", "-c", script, "sh",
		extractDir, tarPath, rootfsLabel, imagePath, strconv.Itoa(sizeMiB))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("extracting and formatting rootfs image: %w\n%s", err, stderr.String())
	}
	return nil
}
