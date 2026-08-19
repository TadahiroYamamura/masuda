package sandbox

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// requireEgressProxyBinary skips the test unless masuda-egress-proxy is on
// PATH. Skipping (not failing) keeps `go test ./...` usable on a machine
// that hasn't run scripts/setup-vm-host.sh -- same reasoning as
// requireNetHelper (masuda has no CI job that runs `go test`).
func requireEgressProxyBinary(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(egressProxyBinary); err != nil {
		t.Skipf("%s not installed (see docs/INSTALLATION.md)", egressProxyBinary)
	}
}

// TestResolveWorkspaceByIP confirms the IP->MAC->workspace resolution
// chain: a lease file entry for a workspace's deterministic MAC (MACFor)
// resolves back to that workspace's ID and RepoRoot.
func TestResolveWorkspaceByIP(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	repoRoot := t.TempDir()
	info, err := workspace.Create(repoRoot, "abc123", "some-branch", "develop", "")
	if err != nil {
		t.Fatalf("workspace.Create() error = %v", err)
	}

	leasePath := filepath.Join(t.TempDir(), "dnsmasq.leases")
	content := "1787000000 " + MACFor(info.ID) + " 192.168.200.42 guest-abc123 *\n"
	if err := os.WriteFile(leasePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	id, gotRepoRoot, ok, err := ResolveWorkspaceByIP(net.ParseIP("192.168.200.42"), leasePath)
	if err != nil {
		t.Fatalf("ResolveWorkspaceByIP() error = %v", err)
	}
	if !ok {
		t.Fatal("ResolveWorkspaceByIP() ok = false, want true")
	}
	if id != info.ID {
		t.Errorf("id = %q, want %q", id, info.ID)
	}
	if gotRepoRoot != repoRoot {
		t.Errorf("repoRoot = %q, want %q", gotRepoRoot, repoRoot)
	}
}

// TestResolveWorkspaceByIPUnknown confirms an IP with no lease, and an IP
// leased to a MAC that doesn't belong to any known workspace, both resolve
// to ok=false rather than an error -- neither is exceptional, they're just
// "not one of ours" (a stray host on the bridge, a stale lease from an
// already-removed workspace, ...).
func TestResolveWorkspaceByIPUnknown(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	leasePath := filepath.Join(t.TempDir(), "dnsmasq.leases")
	content := "1787000000 aa:bb:cc:dd:ee:ff 192.168.200.7 someone-else *\n"
	if err := os.WriteFile(leasePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, ok, err := ResolveWorkspaceByIP(net.ParseIP("192.168.200.99"), leasePath); err != nil || ok {
		t.Errorf("no-lease IP: ok = %v, err = %v, want ok=false, err=nil", ok, err)
	}
	if _, _, ok, err := ResolveWorkspaceByIP(net.ParseIP("192.168.200.7"), leasePath); err != nil || ok {
		t.Errorf("leased-but-unknown-MAC IP: ok = %v, err = %v, want ok=false, err=nil", ok, err)
	}
}

// TestEnsureEgressProxyIsIdempotent exercises EnsureEgressProxy end to end
// against the real masuda-egress-proxy binary (see docs/INSTALLATION.md):
// the first call starts it, a second call finds it already running rather
// than erroring or starting a duplicate.
func TestEnsureEgressProxyIsIdempotent(t *testing.T) {
	requireTestBridge(t, testBridge) // egressProxyBind is the bridge gateway IP
	requireEgressProxyBinary(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if err := EnsureEgressProxy(); err != nil {
		t.Fatalf("EnsureEgressProxy() first call error = %v", err)
	}
	t.Cleanup(func() {
		pidPathVal, err := egressProxyPIDPath()
		if err == nil {
			killStalePID(pidPathVal)
			_ = os.Remove(pidPathVal)
		}
	})

	if err := EnsureEgressProxy(); err != nil {
		t.Fatalf("EnsureEgressProxy() second call error = %v", err)
	}

	conn, err := net.Dial("tcp", net.JoinHostPort(egressProxyBind, strconv.Itoa(egressProxyPort)))
	if err != nil {
		t.Fatalf("dialing egress-proxy: %v", err)
	}
	conn.Close()
}

// TestResolveEgressAllowlistRequiresBothDeclarationAndApproval confirms the
// declare/approve intersection (Issue #11 M4): a hostname the repo declares
// but the user hasn't approved, or the user has approved but the repo no
// longer declares, is denied either way -- only the overlap is allowed.
func TestResolveEgressAllowlistRequiresBothDeclarationAndApproval(t *testing.T) {
	repoRoot := t.TempDir()
	writeSettingsJSON(t, repoRoot, `{"egressAllowlist": ["github.com", "declared-not-approved.example"]}`)
	if err := config.SaveLocal(repoRoot, config.LocalSettings{
		EgressAllowlist: []string{"github.com", "approved-not-declared.example"},
	}); err != nil {
		t.Fatalf("SaveLocal() error = %v", err)
	}

	got, err := resolveEgressAllowlist(repoRoot)
	if err != nil {
		t.Fatalf("resolveEgressAllowlist() error = %v", err)
	}
	if len(got) != 1 || got[0] != "github.com" {
		t.Fatalf("resolveEgressAllowlist() = %v, want [github.com]", got)
	}
}

// TestResolveEgressAllowlistNoFilesReturnsEmpty confirms a repo with
// neither settings.json nor settings.local.json (both files entirely
// absent, the state of a freshly cloned repo before anyone runs `masuda
// egress approve`) resolves to an empty allowlist rather than an error.
func TestResolveEgressAllowlistNoFilesReturnsEmpty(t *testing.T) {
	got, err := resolveEgressAllowlist(t.TempDir())
	if err != nil {
		t.Fatalf("resolveEgressAllowlist() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("resolveEgressAllowlist() = %v, want empty", got)
	}
}

func writeSettingsJSON(t *testing.T, repoRoot, content string) {
	t.Helper()
	dir := filepath.Join(repoRoot, config.DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.SettingsPath(repoRoot), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
