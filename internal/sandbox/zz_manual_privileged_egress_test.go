package sandbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	masuda "github.com/TadahiroYamamura/masuda"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// TestManualPrivilegedCommandEgressAlongsideMainVM covers the two things the
// other manual tests deliberately do not: the disposable VM reaching the
// network, and it doing so while the workspace's own VM is running.
//
// Both matter because they are the normal case, not an edge one. A
// privileged command exists to run something like testcontainers, whose
// first act is pulling an image through the declared egress allowlist -- and
// the request to run it comes from the AI session, which is by definition
// live in the main VM at that moment. The disposable VM is not a workspace,
// so the egress proxy can only place its traffic through the registry
// RunPrivilegedCommand writes (ADR-0053); nothing else exercises that path
// on the wire.
func TestManualPrivilegedCommandEgressAlongsideMainVM(t *testing.T) {
	if os.Getenv("MASUDA_MANUAL_VM_TEST") == "" {
		t.Skip("set MASUDA_MANUAL_VM_TEST=1 to run this real-machine VM test")
	}

	// The workspace's VM starts an mcp-relay by re-execing masuda
	// (resolveMasudaExe), and inside `go test` the running executable is the
	// test binary, which has no such subcommand. The disposable VM needs no
	// relay at all -- it gets no MCP path by design -- which is why the
	// other manual tests do not need this.
	exe := buildMasudaForTest(t)
	originalResolve := resolveMasudaExe
	resolveMasudaExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { resolveMasudaExe = originalResolve })

	repoRoot := t.TempDir()
	// The main VM's image, from whatever base this host has built locally:
	// this test is about networking, not about where the base came from.
	writeImageEntry(t, repoRoot, config.DefaultImageEntry, "FROM masuda-loop:latest\n")
	writeImageEntry(t, repoRoot, "docker", string(masuda.DockerTemplate))
	buildImageEntry(t, repoRoot, config.DefaultImageEntry)
	buildImageEntry(t, repoRoot, "docker")

	// What `docker run hello-world` reaches for: the token endpoint, the
	// registry, and the CDN the blob download redirects to. All three have
	// to be declared -- a pull is three TLS connections to three different
	// hostnames, and the allowlist is per hostname.
	egressHosts := []string{"auth.docker.io", "registry-1.docker.io", "production.cloudfront.docker.com"}
	decl := config.PrivilegedCommandDecl{
		Command:        "docker run --rm hello-world",
		Image:          "docker",
		TimeoutSeconds: 600,
	}
	writeSettings(t, repoRoot, config.Config{
		Image:              config.DefaultImageEntry,
		EgressAllowlist:    egressHosts,
		PrivilegedCommands: map[string]config.PrivilegedCommandDecl{"pull": decl},
	})
	hash, err := config.PrivilegedCommandHash(repoRoot, decl)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveLocal(repoRoot, config.LocalSettings{
		EgressAllowlist:    egressHosts,
		PrivilegedCommands: map[string]config.PrivilegedCommandApproval{"pull": {Approved: true, DeclHash: hash}},
	}); err != nil {
		t.Fatal(err)
	}

	id, err := workspace.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Create(repoRoot, id, "manual-egress-branch", "develop", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Remove(id) })
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		t.Fatal(err)
	}
	worktreeDir := t.TempDir()

	// The main VM stays up for the whole run: that is what makes this a
	// concurrency check and not just an egress one.
	var backend Backend = VMBackend{}
	t.Cleanup(func() { _ = backend.Stop(id) })
	if _, err := backend.Start(id, worktreeDir, stateDir, repoRoot, config.DefaultImageEntry); err != nil {
		t.Fatalf("starting the workspace's own VM: %v", err)
	}
	if !backend.IsRunning(id) {
		t.Fatal("the workspace's VM is not running before the privileged command")
	}

	result, err := RunPrivilegedCommand(PrivilegedRunRequest{
		Name: "pull", Decl: decl, RepoRoot: repoRoot, WorktreeDir: worktreeDir, StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("RunPrivilegedCommand: %v", err)
	}
	t.Logf("run %s -> exit %d, timed out: %v\n%s", result.RunID, result.ExitCode, result.TimedOut, result.Log)

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 -- an image pull through the declared allowlist should succeed", result.ExitCode)
	}
	if !strings.Contains(result.Log, "Hello from Docker") {
		t.Errorf("log does not show the pulled image running:\n%s", result.Log)
	}

	// Both VMs coexisted: the workspace's own VM must be untouched by a
	// disposable VM starting, running and being torn down next to it.
	if !backend.IsRunning(id) {
		t.Error("the workspace's VM stopped while the privileged command ran")
	}
}

func writeImageEntry(t *testing.T, repoRoot, entry, dockerfile string) {
	t.Helper()
	if err := os.MkdirAll(config.ImageDir(repoRoot, entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ImageDockerfilePath(repoRoot, entry), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
}

func buildImageEntry(t *testing.T, repoRoot, entry string) {
	t.Helper()
	tag, err := config.ImageTag(repoRoot, entry)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "build", "-f", config.ImageDockerfilePath(repoRoot, entry), "-t", tag, repoRoot).CombinedOutput()
	if err != nil {
		t.Fatalf("docker build %s: %v\n%s", entry, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })
}

func writeSettings(t *testing.T, repoRoot string, cfg config.Config) {
	t.Helper()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, config.DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.SettingsPath(repoRoot), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
