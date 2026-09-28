package sandbox

import (
	"os"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

// TestManualPrivilegedCommandOnLoopImage runs a privileged command on an
// image entry built FROM masuda-loop, not from masuda's docker template. Such
// an image's /etc/fstab has only the main VM's lines, so the results share
// used to go unmounted: masuda-run.service failed, the VM never powered off,
// and the run came back after the host's backstop with exit code -1 and an
// empty log. masuda now injects the mount units itself.
func TestManualPrivilegedCommandOnLoopImage(t *testing.T) {
	if os.Getenv("MASUDA_MANUAL_VM_TEST") == "" {
		t.Skip("set MASUDA_MANUAL_VM_TEST=1 to run this real-machine VM test")
	}
	repoRoot := t.TempDir()
	writeImageEntry(t, repoRoot, config.DefaultImageEntry, "FROM masuda-loop:latest\n")
	buildImageEntry(t, repoRoot, config.DefaultImageEntry)

	worktreeDir := t.TempDir()
	if err := os.WriteFile(worktreeDir+"/marker.txt", []byte("from-snapshot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RunPrivilegedCommand(PrivilegedRunRequest{
		Name: "loop",
		// Short: a hang shows up as a timeout instead of the old
		// seven-minute wait.
		Decl:        config.PrivilegedCommandDecl{Command: "cat marker.txt", Image: config.DefaultImageEntry, TimeoutSeconds: 60},
		RepoRoot:    repoRoot,
		WorktreeDir: worktreeDir,
		TrustedDir:  t.TempDir(),
		StateDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RunPrivilegedCommand: %v", err)
	}
	if result.TimedOut || result.ExitCode != 0 || !strings.Contains(result.Log, "from-snapshot") {
		console, _ := os.ReadFile(result.Dir + "/console.log")
		t.Fatalf("run = exit %d, timed out %v, log %q; want exit 0 with the snapshot's content\nconsole tail:\n%s",
			result.ExitCode, result.TimedOut, result.Log, tail(string(console), 30))
	}
}

func tail(s string, lines int) string {
	parts := strings.Split(s, "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}
