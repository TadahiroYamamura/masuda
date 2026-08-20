package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

func runImageCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newImageCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// Only the docker template is exercised here: the default one pins its FROM
// to the current published release, so materializing it needs the network
// (imageTemplate).
func TestImageAddDockerTemplateWritesEntry(t *testing.T) {
	root := newTestRepo(t)

	if out, err := runImageCommand(t, "add", "privileged", "--template", "docker"); err != nil {
		t.Fatalf("add error = %v, out = %s", err, out)
	}

	dockerfile, err := os.ReadFile(config.ImageDockerfilePath(root, "privileged"))
	if err != nil {
		t.Fatal(err)
	}
	// The disposable VM's whole point is a working Docker daemon in an
	// image that isn't the AI session's own (ADR-0053).
	if !strings.Contains(string(dockerfile), "docker.io") {
		t.Fatalf("Dockerfile = %q, want it to install Docker", dockerfile)
	}
	if strings.Contains(string(dockerfile), "FROM tadahiroyamamura/masuda") {
		t.Fatal("the privileged template must not derive from masuda's sandbox base image (ADR-0054)")
	}
	if _, err := os.Stat(config.ImageSettingsPath(root, "privileged")); err != nil {
		t.Fatalf("image settings.json not written: %v", err)
	}
}

func TestImageAddRefusesExistingEntry(t *testing.T) {
	newTestRepo(t)
	if _, err := runImageCommand(t, "add", "privileged", "--template", "docker"); err != nil {
		t.Fatal(err)
	}
	if _, err := runImageCommand(t, "add", "privileged", "--template", "docker"); err == nil {
		t.Fatal("add over an existing entry: error = nil, want an error")
	}
}

func TestImageAddRejectsUnsafeEntryName(t *testing.T) {
	newTestRepo(t)
	if _, err := runImageCommand(t, "add", "../escape", "--template", "docker"); err == nil {
		t.Fatal("add with an unsafe entry name: error = nil, want an error")
	}
}

func TestImageAddRejectsUnknownTemplate(t *testing.T) {
	newTestRepo(t)
	if _, err := runImageCommand(t, "add", "extra", "--template", "nonexistent"); err == nil {
		t.Fatal("add with an unknown template: error = nil, want an error")
	}
}

func TestImageListShowsEntryAndDerivedTag(t *testing.T) {
	newTestRepo(t)
	if _, err := runImageCommand(t, "add", "privileged", "--template", "docker"); err != nil {
		t.Fatal(err)
	}

	out, err := runImageCommand(t, "list")
	if err != nil {
		t.Fatalf("list error = %v, out = %s", err, out)
	}
	if !strings.Contains(out, "privileged") || !strings.Contains(out, "masuda-") || !strings.Contains(out, "rootfs=auto") {
		t.Fatalf("list output = %q, want the entry, its derived tag, and its rootfs size", out)
	}
}

func TestImageListWithNoEntriesReportsEmpty(t *testing.T) {
	newTestRepo(t)
	out, err := runImageCommand(t, "list")
	if err != nil {
		t.Fatalf("list error = %v, out = %s", err, out)
	}
	if !strings.Contains(out, "no image entries") {
		t.Fatalf("list output = %q, want a no-entries message", out)
	}
}
