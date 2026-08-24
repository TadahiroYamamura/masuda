package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

// TestManualPrivilegedCommand is a throwaway real-machine check of the whole
// disposable-VM path (ADR-0053): a declared command runs as root inside a VM
// built from a declared image entry, against a *copy* of the workspace, and
// its exit code and log come back on the results share.
//
// Needs everything a normal VM start needs (scripts/setup-vm-host.sh) plus
// docker, and it builds a ~630MiB image the first time, so it stays behind
// the same env-var switch as TestManualEgressFiltering rather than running
// as part of `go test ./...`.
func TestManualPrivilegedCommand(t *testing.T) {
	if os.Getenv("MASUDA_MANUAL_VM_TEST") == "" {
		t.Skip("set MASUDA_MANUAL_VM_TEST=1 to run this real-machine VM test")
	}

	repoRoot := t.TempDir()
	entry := "docker"
	if err := os.MkdirAll(config.ImageDir(repoRoot, entry), 0o755); err != nil {
		t.Fatal(err)
	}
	template, err := os.ReadFile(manualTemplatePath(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ImageDockerfilePath(repoRoot, entry), template, 0o644); err != nil {
		t.Fatal(err)
	}

	tag, err := config.ImageTag(repoRoot, entry)
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("docker", "build", "-f", config.ImageDockerfilePath(repoRoot, entry), "-t", tag, repoRoot)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })

	// A file only the snapshot should ever see written to: the command
	// writes into /workspace, and the live worktree must come back
	// untouched.
	worktreeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktreeDir, "marker.txt"), []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The run's console log and whatever the guest managed to write are the
	// only evidence when a VM misbehaves, and t.TempDir() deletes them the
	// moment the test ends -- exactly when a failure most needs looking at.
	// MASUDA_MANUAL_KEEP_DIR keeps them.
	stateDir := t.TempDir()
	if keep := os.Getenv("MASUDA_MANUAL_KEEP_DIR"); keep != "" {
		if err := os.MkdirAll(keep, 0o755); err != nil {
			t.Fatal(err)
		}
		stateDir = keep
	}
	t.Logf("state directory: %s", stateDir)

	result, err := RunPrivilegedCommand(PrivilegedRunRequest{
		Name: "e2e",
		Decl: config.PrivilegedCommandDecl{
			// Proves the three things the design promises: root, a working
			// Docker daemon, and the workspace tree.
			Command:        "id -u && docker info --format '{{.ServerVersion}}' && cat marker.txt && echo overwritten > marker.txt && mkdir -p coverage && echo '<html/>' > coverage/index.html && echo done > report.txt",
			Image:          entry,
			TimeoutSeconds: 600,
			// One file and one directory, to exercise both shapes of
			// collection (ADR-0053).
			Outputs: []string{"report.txt", "coverage"},
		},
		RepoRoot:    repoRoot,
		WorktreeDir: worktreeDir,
		StateDir:    stateDir,
	})
	if err != nil {
		t.Fatalf("RunPrivilegedCommand: %v", err)
	}
	t.Logf("run %s -> exit %d, timed out: %v\n%s", result.RunID, result.ExitCode, result.TimedOut, result.Log)

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(result.Log, "\n0\n") && !strings.HasPrefix(result.Log, "0\n") {
		t.Errorf("log does not show uid 0 (the command must run as root):\n%s", result.Log)
	}
	if !strings.Contains(result.Log, "original") {
		t.Errorf("log does not show the workspace snapshot's content:\n%s", result.Log)
	}

	// The live worktree must be untouched: the VM only ever saw a copy.
	marker, err := os.ReadFile(filepath.Join(worktreeDir, "marker.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(marker) != "original\n" {
		t.Errorf("live worktree was modified by the privileged command: %q", marker)
	}

	// Results stay on disk for the AI session to read (ADR-0053: never
	// auto-deleted, one directory per run).
	if _, err := os.Stat(filepath.Join(result.Dir, "log")); err != nil {
		t.Errorf("log not kept at %s: %v", result.Dir, err)
	}

	// Declared artifacts come back out of the snapshot, into the run
	// directory -- never onto the live worktree.
	if result.OutputsError != "" {
		t.Errorf("OutputsError = %q, want everything declared to be collected", result.OutputsError)
	}
	for _, rel := range []string{"report.txt", "coverage/index.html"} {
		if _, err := os.Stat(filepath.Join(result.Dir, "outputs", rel)); err != nil {
			t.Errorf("declared output %s not collected: %v", rel, err)
		}
		if _, err := os.Stat(filepath.Join(worktreeDir, rel)); !os.IsNotExist(err) {
			t.Errorf("%s leaked into the live worktree", rel)
		}
	}
	t.Logf("collected: %v", result.Outputs)
}

// manualTemplatePath locates masuda's own docker image template in the
// source tree. The test builds from the template rather than an embedded
// copy so that editing templates/docker.Dockerfile is what this exercises.
func manualTemplatePath(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not in a git checkout, cannot locate the image template: %v", err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "templates", "docker.Dockerfile")
}
