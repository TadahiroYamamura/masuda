package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// withIsolatedGlobalConfig points git's global config at an empty file for
// the duration of the test, so gitConfigValue's global fallback never reads
// the real host ~/.gitconfig (which would make the test's outcome depend on
// whoever's machine it runs on).
func withIsolatedGlobalConfig(t *testing.T, kv ...string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", path)
	for i := 0; i+1 < len(kv); i += 2 {
		if err := exec.Command("git", "config", "--file", path, kv[i], kv[i+1]).Run(); err != nil {
			t.Fatalf("seeding global config %s: %v", kv[i], err)
		}
	}
}

func newBareRepoRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	return dir
}

func TestGitConfigValuePrefersLocalOverGlobal(t *testing.T) {
	withIsolatedGlobalConfig(t, "user.name", "Global Name")
	root := newBareRepoRoot(t)
	if err := exec.Command("git", "-C", root, "config", "--local", "user.name", "Local Name").Run(); err != nil {
		t.Fatalf("seeding local config: %v", err)
	}
	if got := gitConfigValue(root, "user.name"); got != "Local Name" {
		t.Errorf("gitConfigValue() = %q, want %q", got, "Local Name")
	}
}

func TestGitConfigValueFallsBackToGlobal(t *testing.T) {
	withIsolatedGlobalConfig(t, "user.email", "global@example.com")
	root := newBareRepoRoot(t)
	if got := gitConfigValue(root, "user.email"); got != "global@example.com" {
		t.Errorf("gitConfigValue() = %q, want %q", got, "global@example.com")
	}
}

func TestGitConfigValueEmptyWhenUnset(t *testing.T) {
	withIsolatedGlobalConfig(t)
	root := newBareRepoRoot(t)
	if got := gitConfigValue(root, "user.name"); got != "" {
		t.Errorf("gitConfigValue() = %q, want empty", got)
	}
}

// TestWriteGitIdentity confirms the two-line file format WriteGitIdentity
// produces (see its doc comment for why it's plain lines, not a
// shell-sourceable file) round-trips a name containing a space intact --
// runtime/entrypoint.sh reads this with `sed -n '1p'`/`'2p'`.
func TestWriteGitIdentity(t *testing.T) {
	withIsolatedGlobalConfig(t)
	root := newBareRepoRoot(t)
	if err := exec.Command("git", "-C", root, "config", "--local", "user.name", "A Name With Spaces").Run(); err != nil {
		t.Fatalf("seeding local config: %v", err)
	}
	if err := exec.Command("git", "-C", root, "config", "--local", "user.email", "a@example.com").Run(); err != nil {
		t.Fatalf("seeding local config: %v", err)
	}

	stateDir := t.TempDir()
	if err := WriteGitIdentity(stateDir, root); err != nil {
		t.Fatalf("WriteGitIdentity() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(stateDir, gitIdentityFileName))
	if err != nil {
		t.Fatalf("reading identity file: %v", err)
	}
	want := "A Name With Spaces\na@example.com\n"
	if string(content) != want {
		t.Errorf("identity file content = %q, want %q", content, want)
	}
}

func TestWriteGitIdentityEmptyWhenNoIdentity(t *testing.T) {
	withIsolatedGlobalConfig(t)
	root := newBareRepoRoot(t)

	stateDir := t.TempDir()
	if err := WriteGitIdentity(stateDir, root); err != nil {
		t.Fatalf("WriteGitIdentity() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(stateDir, gitIdentityFileName))
	if err != nil {
		t.Fatalf("reading identity file: %v", err)
	}
	if string(content) != "\n\n" {
		t.Errorf("identity file content = %q, want two empty lines", content)
	}
}
