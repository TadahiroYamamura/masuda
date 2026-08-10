package sandbox

import (
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

func TestGitIdentityEnvArgs(t *testing.T) {
	withIsolatedGlobalConfig(t, "user.name", "A Name", "user.email", "a@example.com")
	root := newBareRepoRoot(t)
	args := gitIdentityEnvArgs(root)
	want := []string{
		"-e", "GIT_AUTHOR_NAME=A Name", "-e", "GIT_COMMITTER_NAME=A Name",
		"-e", "GIT_AUTHOR_EMAIL=a@example.com", "-e", "GIT_COMMITTER_EMAIL=a@example.com",
	}
	if len(args) != len(want) {
		t.Fatalf("gitIdentityEnvArgs() = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("gitIdentityEnvArgs()[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestGitIdentityEnvArgsEmptyWhenNoIdentity(t *testing.T) {
	withIsolatedGlobalConfig(t)
	root := newBareRepoRoot(t)
	if args := gitIdentityEnvArgs(root); args != nil {
		t.Errorf("gitIdentityEnvArgs() = %v, want nil", args)
	}
}

