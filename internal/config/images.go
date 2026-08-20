// images.go handles .masuda/images/<entry>/: one directory per VM image the
// target repository declares, holding that image's Dockerfile and its build
// parameters (ADR-0054). This replaces the single fixed .masuda/Dockerfile
// path ADR-0032 introduced -- both the main sandbox VM's image and the
// disposable privileged VM images (ADR-0053) are declared the same way, so
// there is one mechanism to maintain rather than one per kind of VM.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ImagesDirName is the subdirectory of DirName holding one directory per
// declared image.
const ImagesDirName = "images"

// DefaultImageEntry is the entry name `masuda init` materializes and that
// Config.Image falls back to: the main sandbox VM's image.
const DefaultImageEntry = "default"

// ImageSettingsFileName is the per-image settings file's name within an
// image's directory. Deliberately the same name as the repository-level
// SettingsFileName -- the directory it sits in says which one it is -- but
// a different schema (ImageConfig, not Config).
const ImageSettingsFileName = "settings.json"

// ImagesDir returns the absolute path to repoRoot's .masuda/images/.
func ImagesDir(repoRoot string) string {
	return filepath.Join(repoRoot, DirName, ImagesDirName)
}

// ImageDir returns the absolute path to one entry's directory.
func ImageDir(repoRoot, entry string) string {
	return filepath.Join(ImagesDir(repoRoot), entry)
}

// ImageDockerfilePath returns the absolute path to one entry's Dockerfile.
func ImageDockerfilePath(repoRoot, entry string) string {
	return filepath.Join(ImageDir(repoRoot, entry), "Dockerfile")
}

// ImageSettingsPath returns the absolute path to one entry's settings file.
func ImageSettingsPath(repoRoot, entry string) string {
	return filepath.Join(ImageDir(repoRoot, entry), ImageSettingsFileName)
}

// ImageConfig is the on-disk shape of .masuda/images/<entry>/settings.json:
// the parameters masuda needs to build this image's rootfs, as opposed to
// the image's contents (its Dockerfile, next to this file).
type ImageConfig struct {
	// RootfsSizeMiB raises the ext4 image size internal/rootfs.Build would
	// otherwise compute from the exported image's own contents. Treated as
	// a floor, not a replacement (see rootfs.Build): a value below what the
	// contents need could only produce a VM that fails to build or boots
	// with no free space. Zero means "whatever Build computes."
	RootfsSizeMiB int `json:"rootfsSizeMiB,omitempty"`
}

// LoadImage reads one entry's settings file. A missing file is not an error
// -- it returns the zero value, mirroring Load/LoadLocal, so every field
// means "unset, use what masuda computes."
func LoadImage(repoRoot, entry string) (ImageConfig, error) {
	path := ImageSettingsPath(repoRoot, entry)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ImageConfig{}, nil
	}
	if err != nil {
		return ImageConfig{}, err
	}
	var cfg ImageConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ImageConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}

// ListImageEntries returns the declared entry names, sorted. A missing
// images directory yields no entries and no error (a repository predating
// ADR-0054, or one never `masuda init`'d). Names that don't satisfy
// ValidateImageEntry are skipped rather than reported: the directory is
// walked to find work to do (rebuild every image), and an unrelated
// directory someone left there must not fail the whole command.
func ListImageEntries(repoRoot string) ([]string, error) {
	dirents, err := os.ReadDir(ImagesDir(repoRoot))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []string
	for _, d := range dirents {
		if !d.IsDir() || ValidateImageEntry(d.Name()) != nil {
			continue
		}
		entries = append(entries, d.Name())
	}
	sort.Strings(entries)
	return entries, nil
}

var imageEntryRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidateImageEntry rejects entry names masuda can't use safely. The
// charset is constrained on two independent grounds (ADR-0054): the name
// becomes part of a derived Docker reference (ImageTag), and it becomes a
// path component -- both this repository's .masuda/images/<entry>/ and, for
// a privileged command's results, a directory under the workspace state
// directory. Rejecting "." and ".." is the point of requiring the first
// character to be alphanumeric.
func ValidateImageEntry(entry string) error {
	if entry == "" {
		return fmt.Errorf("image entry name is empty")
	}
	if !imageEntryRe.MatchString(entry) {
		return fmt.Errorf("image entry name %q must match [a-z0-9][a-z0-9-]*", entry)
	}
	return nil
}

var tagSanitizer = regexp.MustCompile(`[^a-z0-9]+`)

// ImageTag derives the local Docker reference masuda builds one entry as.
// Users name entries, never tags (ADR-0054): the tag is an internal detail,
// so it can carry what it needs to stay unique.
//
// The repository component includes both repoRoot's directory name (so
// `docker images` stays readable) and a short digest of its absolute path.
// The digest is what actually prevents collisions: before ADR-0054 the
// image field held a tag name directly, which made "two projects both using
// the default image" the user's problem to notice and rename around; now
// that every project's main image is the same entry name ("default"), it
// would be masuda silently overwriting one project's image with another's.
// The path -- not just the directory name -- is hashed so two checkouts of
// the same repository (~/work/app and ~/tmp/app) stay distinct.
func ImageTag(repoRoot, entry string) (string, error) {
	if err := ValidateImageEntry(entry); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	name := tagSanitizer.ReplaceAllString(strings.ToLower(filepath.Base(abs)), "-")
	name = strings.Trim(name, "-")
	if name == "" {
		name = "repo"
	}
	return fmt.Sprintf("masuda-%s-%s:%s", name, hex.EncodeToString(sum[:])[:6], entry), nil
}

// ImageDigest fingerprints one entry's declared contents: its Dockerfile
// and its settings file. A privileged command's approval is pinned to this
// alongside the command's own declaration (see PrivilegedCommandHash) --
// without it, an approval granted for "run this command in this image"
// would survive the image being replaced with something else entirely.
//
// A missing file is folded into the digest as a distinct state rather than
// treated as an error, so "no settings file" and "an empty settings file"
// hash differently and neither can be introduced after approval unnoticed.
func ImageDigest(repoRoot, entry string) (string, error) {
	if err := ValidateImageEntry(entry); err != nil {
		return "", err
	}
	h := sha256.New()
	for _, path := range []string{ImageDockerfilePath(repoRoot, entry), ImageSettingsPath(repoRoot, entry)} {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			fmt.Fprintf(h, "%s\x00absent\x00", filepath.Base(path))
			continue
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.Base(path), len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
