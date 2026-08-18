package sandbox

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerBackendSatisfiesBackend(t *testing.T) {
	var _ Backend = DockerBackend{}
}

// testImageTag names the throwaway image built by buildTestImage. Reused
// across test runs (docker build re-tags identical content from cache
// almost instantly), so it's never explicitly removed.
const testImageTag = "masuda-sandbox-test:latest"

// testDockerfile satisfies Start's two undocumented requirements a generic
// image wouldn't: a pre-existing /home/ubuntu/.claude directory (Start
// docker-cp's CLAUDE.md there before the container's own CMD ever runs, so
// the directory must already exist in the image itself) and a CMD that keeps
// running so IsRunning has something to observe.
const testDockerfile = `FROM alpine:latest
RUN mkdir -p /home/ubuntu/.claude
CMD ["sleep", "infinity"]
`

// requireDockerSandbox skips the test unless this host can actually run the
// sandbox lifecycle: a reachable docker daemon, and the host Claude Code
// credentials Start unconditionally requires (hostCredentialMounts). Skipping
// rather than failing keeps `go test ./...` usable on a machine that hasn't
// logged into `claude` yet -- masuda has no CI job that runs `go test`
// (release.yml only builds/signs binaries on tag push), so this is purely a
// local developer convenience, not a CI gate to keep green.
func requireDockerSandbox(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}
	for _, p := range []string{
		filepath.Join(home, ".claude", ".credentials.json"),
		filepath.Join(home, ".claude.json"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("host Claude Code credentials not found at %s (log into `claude` on this host first)", p)
		}
	}
}

func buildTestImage(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("docker", "build", "-t", testImageTag, "-")
	cmd.Stdin = strings.NewReader(testDockerfile)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("building test image: %v\n%s", err, stderr.String())
	}
	return testImageTag
}

// TestDockerBackendLifecycle exercises DockerBackend through the Backend
// interface end to end against a real docker daemon: start, the
// already-running resume path, and stop. This is the "現状皆無の統合テスト"
// roadmap M1 (Issue #31) calls for -- until now internal/sandbox had only
// unit tests for its git-identity helpers, nothing that actually drives
// `docker create`/`start`/`stop`.
func TestDockerBackendLifecycle(t *testing.T) {
	requireDockerSandbox(t)
	image := buildTestImage(t)

	var backend Backend = DockerBackend{}
	id := "sandboxbackendtest"
	worktreeDir := t.TempDir()
	stateDir := t.TempDir()
	repoRoot := t.TempDir()
	t.Cleanup(func() { _ = backend.Stop(id) })

	h, err := backend.Start(id, worktreeDir, stateDir, repoRoot, image)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if want := ContainerName(id); h.ContainerName != want {
		t.Errorf("Start() ContainerName = %q, want %q", h.ContainerName, want)
	}
	if h.HostPort == 0 {
		t.Error("Start() HostPort = 0, want a nonzero allocated port")
	}
	if !backend.IsRunning(id) {
		t.Fatal("IsRunning() = false right after Start()")
	}

	// Calling Start again while already running must resume (not error or
	// recreate), reporting the same port the first call allocated.
	h2, err := backend.Start(id, worktreeDir, stateDir, repoRoot, image)
	if err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	if h2.HostPort != h.HostPort {
		t.Errorf("second Start() HostPort = %d, want %d (unchanged)", h2.HostPort, h.HostPort)
	}

	wantAttach := []string{"docker", "exec", "-it", h.ContainerName, "tmux", "attach", "-t", tmuxSession}
	got, err := backend.AttachArgs(id)
	if err != nil {
		t.Fatalf("AttachArgs() error = %v", err)
	}
	if strings.Join(got, " ") != strings.Join(wantAttach, " ") {
		t.Errorf("AttachArgs() = %v, want %v", got, wantAttach)
	}

	if err := backend.Stop(id); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if backend.IsRunning(id) {
		t.Error("IsRunning() = true after Stop()")
	}
}
