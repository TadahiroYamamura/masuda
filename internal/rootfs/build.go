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
	"strings"
)

// rootfsLabel is the ext4 volume label baked into every image Build
// produces, so a guest kernel command line can find its root device by
// LABEL= rather than an assumption about device ordering.
const rootfsLabel = "masuda-rootfs"

// minImageSizeMiB is a floor under the computed size, so a tiny image (e.g.
// a test fixture) still gets enough headroom for ext4's own metadata and a
// few writes during boot.
const minImageSizeMiB = 512

// maxImageSizeMiB caps what a caller may ask for (Build's minSizeMiB), and
// therefore what a target repository's own .masuda/images/<entry>/
// settings.json can ask for (ADR-0054). Every other field a repository
// declares only changes what happens inside the sandbox; a rootfs size
// consumes host disk directly. mkfs.ext4 leaves the image sparse, but its
// metadata (inode tables, bitmaps, journal) is written for real and scales
// with the declared size, so a value off by a few digits costs real disk
// immediately. This is a guard against a typo, not a considered ceiling on
// legitimate use -- raise it when a real workload needs more.
const maxImageSizeMiB = 64 * 1024

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
//
// depmod is here unconditionally, not only when a caller actually injects
// kernel modules via ExtraFile (e.g. VMBackend's virtiofs.ko, Issue #31
// M5-6): this package only exists to build VM boot images, so a host able
// to run Build at all is already expected to have the VM toolchain
// (scripts/setup-vm-host.sh's kmod install) present.
var requiredBinaries = []string{"docker", "fakeroot", "mkfs.ext4", "depmod"}

// ExtraFile is a small file Build writes into the image's filesystem in
// addition to whatever comes from the Docker image itself -- content that
// only makes sense for a VM-boot rootfs (the guest's SSH
// ~/.ssh/authorized_keys, Issue #31 M5-5) and has no business in the
// shared Dockerfile/Docker image the export itself is built from.
type ExtraFile struct {
	// GuestPath is relative to the image's root, e.g.
	// "home/ubuntu/.ssh/authorized_keys" (no leading slash).
	GuestPath string
	Content   []byte
	Mode      os.FileMode
	// UID/GID are the owner baked into the image's inode -- not applied by
	// staging the file on the host (see stageExtraFiles's doc comment for
	// why that alone isn't reliable) but by an explicit chown inside
	// extractAndFormat's fakeroot session, the same session that already
	// makes ownership like root:shadow on /etc/shadow possible for content
	// coming from the Docker image itself.
	UID, GID int
}

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
// minSizeMiB raises the size Build would otherwise compute from the
// exported content. It is a floor, not a replacement: a value below what
// the content needs could only produce a failed mkfs.ext4 or a VM that
// boots with no free space, so there is nothing to gain from honouring it
// literally (ADR-0054). Zero means "use the computed size." Values above
// maxImageSizeMiB are rejected rather than clamped -- a caller asking for
// 4TiB has a typo, and silently building 64GiB instead would hide it.
func Build(image, outputPath string, extra []ExtraFile, minSizeMiB int) error {
	if minSizeMiB < 0 {
		return fmt.Errorf("rootfs size %d MiB is negative", minSizeMiB)
	}
	if minSizeMiB > maxImageSizeMiB {
		return fmt.Errorf("rootfs size %d MiB exceeds masuda's %d MiB ceiling", minSizeMiB, maxImageSizeMiB)
	}
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
	sizeMiB = max(sizeMiB, minSizeMiB)

	extractDir := filepath.Join(workDir, "extracted")
	if err := os.Mkdir(extractDir, 0o755); err != nil {
		return fmt.Errorf("creating extraction directory: %w", err)
	}

	// Staged on the host first, then overlaid onto extractDir *inside* the
	// same fakeroot session extractAndFormat runs (see its doc comment), so
	// mkfs.ext4 -d sees the final tree in one pass, same as everything from
	// the Docker image itself. ownerManifestPath carries each file's
	// intended UID/GID separately -- content staged as a plain host file
	// always lands owned by masuda's own real uid regardless of what
	// ExtraFile.UID/GID asked for, so ownership has to be fixed up
	// explicitly afterward (see extractAndFormat's doc comment for why
	// `cp -a`'s own ownership-preservation can't be trusted here).
	stagingDir := filepath.Join(workDir, "extra")
	ownerManifestPath := filepath.Join(workDir, "extra-owners.tsv")
	if err := stageExtraFiles(stagingDir, ownerManifestPath, extra); err != nil {
		return fmt.Errorf("staging extra files: %w", err)
	}

	tmpImagePath := outputPath + ".tmp"
	defer os.Remove(tmpImagePath) // no-op once the rename below succeeds

	if err := extractAndFormat(extractDir, tarPath, stagingDir, ownerManifestPath, tmpImagePath, sizeMiB); err != nil {
		return err
	}

	if err := os.Rename(tmpImagePath, outputPath); err != nil {
		return fmt.Errorf("moving finished image into place: %w", err)
	}
	return nil
}

// stageExtraFiles writes each of extra's content to stagingDir/GuestPath and
// records its intended ownership as one "<uid>\t<gid>\t<guest path>" line
// per file in ownerManifestPath, for extractAndFormat's chown pass to read.
// Always creates stagingDir, even if extra is empty, so extractAndFormat's
// overlay step has a directory to check for (rather than needing to
// special-case "were there any").
func stageExtraFiles(stagingDir, ownerManifestPath string, extra []ExtraFile) error {
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return err
	}
	var manifest strings.Builder
	for _, f := range extra {
		dest := filepath.Join(stagingDir, f.GuestPath)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, f.Content, f.Mode); err != nil {
			return err
		}
		fmt.Fprintf(&manifest, "%d\t%d\t%s\n", f.UID, f.GID, f.GuestPath)
	}
	return os.WriteFile(ownerManifestPath, []byte(manifest.String()), 0o644)
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

// extractAndFormat extracts tarPath into extractDir, overlays stagingDir's
// extra files (see Build's ExtraFile handling) on top, applies each extra
// file's intended ownership from ownerManifestPath, regenerates the kernel
// module database for any /lib/modules/<version> tree an ExtraFile added
// (see the depmod step below), and formats the result as an ext4 image at
// imagePath sized sizeMiB -- all inside a single fakeroot session so the
// ownership mkfs.ext4 bakes in is real (see Build's doc comment). Arguments
// are passed to the fakeroot shell as positional parameters ($1, $2, ...),
// not interpolated into the script string, so paths containing
// shell-special characters can't break or inject into the command.
//
// depmod -b: a plain Docker/Ubuntu userland has no /lib/modules/<version>
// tree of its own (containers don't carry kernel modules), so any module
// files present here came entirely from an ExtraFile a caller injected
// (e.g. VMBackend staging virtiofs.ko.zst so the guest's systemd-udevd can
// auto-load it on PCI device detection, the same mechanism the Dockerfile's
// kmod install already assumes -- Issue #31 M5-6). depmod only needs to see
// whatever module files actually exist under the tree it's pointed at; it
// doesn't require the rest of a real /lib/modules/<version> install to
// resolve dependencies for modules that aren't present at all.
//
// The chown pass is not optional polish: confirmed live that `cp -a`'s own
// ownership-preservation can't be trusted here. A staged extra file's real
// on-disk owner (masuda's own host uid, set before fakeroot ever starts) is
// exactly what `cp -a` should propagate, but fakeroot's fake-ownership
// tracking treats a freshly created destination file as owned by whatever
// uid it *thinks* is doing the copying (root, since fakeroot fakes that
// too) rather than what `cp` actually asked it to chown to -- observed
// directly by comparing `ls -la` run inside vs. outside the same fakeroot
// session on the same copy. Fixed by chowning explicitly afterward, the
// same way ownership already has to be set explicitly for anything else
// this session didn't inherit correctly on its own.
func extractAndFormat(extractDir, tarPath, stagingDir, ownerManifestPath, imagePath string, sizeMiB int) error {
	const script = `set -e
tar -C "$1" -xpf "$2"
if [ -n "$(ls -A "$3" 2>/dev/null)" ]; then
  cp -a "$3"/. "$1"/
  while IFS="$(printf '\t')" read -r owner_uid owner_gid rel; do
    [ -z "$rel" ] && continue
    chown "$owner_uid:$owner_gid" "$1/$rel"
  done < "$7"
fi
if [ -d "$1/lib/modules" ]; then
  for moddir in "$1"/lib/modules/*/; do
    [ -d "$moddir" ] || continue
    depmod -b "$1" "$(basename "$moddir")"
  done
fi
mkfs.ext4 -q -F -d "$1" -L "$4" "$5" "$6"M
`
	cmd := exec.Command("fakeroot", "sh", "-c", script, "sh",
		extractDir, tarPath, stagingDir, rootfsLabel, imagePath, strconv.Itoa(sizeMiB), ownerManifestPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("extracting and formatting rootfs image: %w\n%s", err, stderr.String())
	}
	return nil
}
