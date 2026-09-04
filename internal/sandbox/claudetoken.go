package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// claudeOAuthTokenFileName is the fixed, host-level (not per-workspace)
// location of the long-lived `claude setup-token` OAuth token VMBackend
// hands guests (Issue #31 M5-6). One token per masuda installation, not per
// workspace -- same reasoning as sshkey.go's VM SSH keypair: workspaces are
// ephemeral, and the security boundary that matters is "does this host's
// masuda installation have a token at all", not "which workspace".
//
// Unlike the Docker path's hostCredentialMounts (bind-mounting the host's
// own ~/.claude/.credentials.json and ~/.claude.json read-write), a VM
// guest is a different kernel that can't share those files live -- virtiofs
// can't carry a Unix domain socket, but more fundamentally, replicating a
// bind mount's read-write semantics for two arbitrary files (not a whole
// directory) isn't something virtiofs offers either. `claude setup-token`
// exists specifically for this shape of problem (CI/headless environments
// without interactive browser login): a single opaque, long-lived (1 year)
// token string, handed to the guest as CLAUDE_CODE_OAUTH_TOKEN, with no
// need to keep a file in sync afterward.
const claudeOAuthTokenFileName = "claude-oauth-token"

// ClaudeOAuthTokenPath returns the path masuda stores the `claude
// setup-token` output at, under masuda's own XDG data directory
// (workspace.DataHome) -- the same base directory the VM SSH keypair and
// vmlinuz already live under.
func ClaudeOAuthTokenPath() (string, error) {
	dir, err := workspace.DataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, claudeOAuthTokenFileName), nil
}

// HasClaudeOAuthToken reports whether a token has been registered on this
// host. Callers use it to warn before starting a VM that would otherwise
// boot into a guest whose `claude` exits immediately for lack of
// credentials -- a failure that surfaces only as the loop service
// reporting a completed loop it never ran.
func HasClaudeOAuthToken() bool {
	path, err := ClaudeOAuthTokenPath()
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// SetClaudeOAuthToken saves token (the output of `claude setup-token`,
// trimmed of surrounding whitespace) to ClaudeOAuthTokenPath, mode 0600 --
// this is a bearer credential for the user's Claude subscription, same
// sensitivity class as the private half of the VM SSH keypair.
func SetClaudeOAuthToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("token is empty")
	}
	path, err := ClaudeOAuthTokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return fmt.Errorf("writing token to %s: %w", path, err)
	}
	return nil
}
