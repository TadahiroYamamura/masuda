package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitIdentityFileName is where WriteGitIdentity writes the host's git
// identity for a VM guest to pick up -- see that function's doc comment.
const gitIdentityFileName = ".masuda-git-identity"

// gitConfigValue reads key from repoRoot's local git config, falling back to
// the host's global config (usually ~/.gitconfig) if the repo doesn't
// override it locally.
func gitConfigValue(repoRoot, key string) string {
	if out, err := exec.Command("git", "-C", repoRoot, "config", "--local", "--get", key).Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			return v
		}
	}
	out, err := exec.Command("git", "config", "--global", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// WriteGitIdentity resolves repoRoot's git identity (same precedence as
// DockerBackend used to pass via -e GIT_AUTHOR_NAME etc.: local config,
// falling back to global) and writes it to stateDir/.masuda-git-identity as
// two plain lines (name, then email; either may be empty), so
// runtime/entrypoint.sh can export GIT_AUTHOR_*/GIT_COMMITTER_* for the
// Build stage's per-step commits inside the guest -- a VM's rootfs (built
// from the same Docker image) has no ~/.gitconfig any more than a
// container did, so git itself has no identity to commit with otherwise.
//
// A plain two-line file, not a shell-sourceable one: the value may contain
// arbitrary characters (spaces, quotes) that would need careful escaping to
// embed safely in a script the guest sources -- reading it as inert data
// with `sed -n '1p'`/`'2p'` instead sidesteps that entirely. Not an error
// if either value is empty (mirrors DockerBackend: no identity configured
// anywhere means the guest can't commit either, same as today).
func WriteGitIdentity(stateDir, repoRoot string) error {
	name := gitConfigValue(repoRoot, "user.name")
	email := gitConfigValue(repoRoot, "user.email")
	content := name + "\n" + email + "\n"
	return os.WriteFile(filepath.Join(stateDir, gitIdentityFileName), []byte(content), 0o644)
}
