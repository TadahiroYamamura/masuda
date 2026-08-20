package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeImage(t *testing.T, repoRoot, entry, dockerfile, settings string) {
	t.Helper()
	if err := os.MkdirAll(ImageDir(repoRoot, entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if dockerfile != "" {
		if err := os.WriteFile(ImageDockerfilePath(repoRoot, entry), []byte(dockerfile), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if settings != "" {
		if err := os.WriteFile(ImageSettingsPath(repoRoot, entry), []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadImageReadsRootfsSize(t *testing.T) {
	dir := t.TempDir()
	writeImage(t, dir, "default", "FROM scratch\n", `{"rootfsSizeMiB": 4096}`)

	cfg, err := LoadImage(dir, "default")
	if err != nil {
		t.Fatalf("LoadImage() error = %v, want nil", err)
	}
	if cfg.RootfsSizeMiB != 4096 {
		t.Fatalf("RootfsSizeMiB = %d, want 4096", cfg.RootfsSizeMiB)
	}
}

func TestLoadImageMissingFileReturnsZeroValue(t *testing.T) {
	dir := t.TempDir()
	writeImage(t, dir, "default", "FROM scratch\n", "")

	cfg, err := LoadImage(dir, "default")
	if err != nil {
		t.Fatalf("LoadImage() error = %v, want nil", err)
	}
	if cfg.RootfsSizeMiB != 0 {
		t.Fatalf("RootfsSizeMiB = %d, want 0", cfg.RootfsSizeMiB)
	}
}

func TestListImageEntriesSortedAndFiltered(t *testing.T) {
	dir := t.TempDir()
	writeImage(t, dir, "privileged", "FROM scratch\n", "")
	writeImage(t, dir, "default", "FROM scratch\n", "")
	// Not a valid entry name, so not something masuda will try to build.
	writeImage(t, dir, "Not_An_Entry", "FROM scratch\n", "")

	entries, err := ListImageEntries(dir)
	if err != nil {
		t.Fatalf("ListImageEntries() error = %v, want nil", err)
	}
	if len(entries) != 2 || entries[0] != "default" || entries[1] != "privileged" {
		t.Fatalf("entries = %v, want [default privileged]", entries)
	}
}

func TestListImageEntriesMissingDirIsNotAnError(t *testing.T) {
	entries, err := ListImageEntries(t.TempDir())
	if err != nil {
		t.Fatalf("ListImageEntries() error = %v, want nil", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %v, want none", entries)
	}
}

func TestValidateImageEntryRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "-leading", "Upper", "has_underscore", "has/slash", "tag:like"} {
		if err := ValidateImageEntry(name); err == nil {
			t.Errorf("ValidateImageEntry(%q) = nil, want an error", name)
		}
	}
	for _, name := range []string{"default", "privileged", "go-1-26", "0"} {
		if err := ValidateImageEntry(name); err != nil {
			t.Errorf("ValidateImageEntry(%q) = %v, want nil", name, err)
		}
	}
}

// The tag has to stay stable for one checkout (so a rebuild replaces the
// same image) and differ between checkouts (so two projects using the same
// entry name don't overwrite each other -- see ImageTag).
func TestImageTagStablePerCheckoutAndDistinctAcross(t *testing.T) {
	tag, err := ImageTag("/home/someone/work/myapp", "default")
	if err != nil {
		t.Fatalf("ImageTag() error = %v, want nil", err)
	}
	again, err := ImageTag("/home/someone/work/myapp", "default")
	if err != nil {
		t.Fatal(err)
	}
	if tag != again {
		t.Fatalf("ImageTag() not stable: %q != %q", tag, again)
	}
	if !strings.HasPrefix(tag, "masuda-myapp-") || !strings.HasSuffix(tag, ":default") {
		t.Fatalf("ImageTag() = %q, want masuda-myapp-<hash>:default", tag)
	}

	elsewhere, err := ImageTag("/tmp/myapp", "default")
	if err != nil {
		t.Fatal(err)
	}
	if elsewhere == tag {
		t.Fatalf("ImageTag() = %q for two different checkouts of the same name", tag)
	}

	other, err := ImageTag("/home/someone/work/myapp", "privileged")
	if err != nil {
		t.Fatal(err)
	}
	if other == tag {
		t.Fatalf("ImageTag() = %q for two different entries", tag)
	}
}

func TestImageTagSanitizesDirectoryName(t *testing.T) {
	tag, err := ImageTag("/home/someone/work/oncall_pf_template", "default")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tag, "masuda-oncall-pf-template-") {
		t.Fatalf("ImageTag() = %q, want the underscores replaced", tag)
	}
}

func TestImageDigestSensitiveToBothFiles(t *testing.T) {
	dir := t.TempDir()
	writeImage(t, dir, "default", "FROM scratch\n", `{"rootfsSizeMiB": 4096}`)

	base, err := ImageDigest(dir, "default")
	if err != nil {
		t.Fatalf("ImageDigest() error = %v, want nil", err)
	}

	writeImage(t, dir, "default", "FROM scratch\nRUN echo other\n", `{"rootfsSizeMiB": 4096}`)
	afterDockerfile, err := ImageDigest(dir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if afterDockerfile == base {
		t.Fatal("ImageDigest() unchanged after the Dockerfile changed")
	}

	writeImage(t, dir, "default", "FROM scratch\nRUN echo other\n", `{"rootfsSizeMiB": 8192}`)
	afterSettings, err := ImageDigest(dir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if afterSettings == afterDockerfile {
		t.Fatal("ImageDigest() unchanged after the image settings changed")
	}
}

// "no settings file" and "an empty settings file" must not hash the same:
// otherwise one could be swapped for the other after an approval.
func TestImageDigestDistinguishesAbsentFromEmpty(t *testing.T) {
	absentDir := t.TempDir()
	writeImage(t, absentDir, "default", "FROM scratch\n", "")
	absent, err := ImageDigest(absentDir, "default")
	if err != nil {
		t.Fatal(err)
	}

	emptyDir := t.TempDir()
	writeImage(t, emptyDir, "default", "FROM scratch\n", "{}")
	empty, err := ImageDigest(emptyDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if absent == empty {
		t.Fatal("ImageDigest() treats a missing settings file the same as an empty one")
	}
}

func TestPrivilegedCommandHashCoversDeclAndImage(t *testing.T) {
	dir := t.TempDir()
	writeImage(t, dir, "privileged", "FROM scratch\n", "")
	decl := PrivilegedCommandDecl{Command: "go test ./...", Image: "privileged"}

	base, err := PrivilegedCommandHash(dir, decl)
	if err != nil {
		t.Fatalf("PrivilegedCommandHash() error = %v, want nil", err)
	}

	changedDecl := decl
	changedDecl.Outputs = []string{"coverage/"}
	if h, err := PrivilegedCommandHash(dir, changedDecl); err != nil {
		t.Fatal(err)
	} else if h == base {
		t.Fatal("PrivilegedCommandHash() unchanged after the declaration changed")
	}

	writeImage(t, dir, "privileged", "FROM scratch\nRUN echo other\n", "")
	if h, err := PrivilegedCommandHash(dir, decl); err != nil {
		t.Fatal(err)
	} else if h == base {
		t.Fatal("PrivilegedCommandHash() unchanged after the image changed")
	}
}

func TestPrivilegedCommandHashRejectsUnsafeImageEntry(t *testing.T) {
	dir := t.TempDir()
	if _, err := PrivilegedCommandHash(dir, PrivilegedCommandDecl{Command: "x", Image: "../escape"}); err == nil {
		t.Fatal("PrivilegedCommandHash() with an unsafe entry name = nil error, want an error")
	}
}

func TestImagePathsAreUnderMasudaDir(t *testing.T) {
	if got, want := ImagesDir("/repo"), filepath.Join("/repo", DirName, ImagesDirName); got != want {
		t.Fatalf("ImagesDir() = %q, want %q", got, want)
	}
}
