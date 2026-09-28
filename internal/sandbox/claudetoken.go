package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// A host can hold several `claude setup-token` OAuth tokens under names, and
// each repository picks one in its settings.local.json (config.LocalSettings
// .ClaudeToken) -- a work repository on the work account, a personal one on
// the personal account. The choice is made by the user per repository, never
// switched automatically. Tokens stay on the host: each VM's
// `masuda internal api-gateway` reads the chosen one and puts it on the
// guest's requests in place of a placeholder.
//
// `claude setup-token` exists for exactly this shape of problem
// (CI/headless environments without interactive browser login): a single
// opaque, long-lived (1 year) token string, with no need to keep a file in
// sync afterward. A VM guest is a different kernel that cannot share the
// host's ~/.claude credential files the way the Docker path's bind mounts
// did.

// DefaultClaudeToken is the name used when a repository names none. Its file
// is the one path masuda used before tokens had names, so a host set up
// then keeps working unchanged.
const DefaultClaudeToken = "default"

const (
	defaultClaudeTokenFileName = "claude-oauth-token"
	namedClaudeTokenDirName    = "claude-tokens"
)

// claudeTokenName limits names to what is safe as a single path component.
var claudeTokenName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidateClaudeTokenName rejects a name that could not be used as a file
// name under the token directory as-is.
func ValidateClaudeTokenName(name string) error {
	if !claudeTokenName.MatchString(name) {
		return fmt.Errorf("invalid Claude token name %q: use 1-64 letters, digits, '-' or '_'", name)
	}
	return nil
}

// ClaudeTokenPath returns where the token called name is stored, under
// masuda's data directory -- mode 0600, a bearer credential for the user's
// Claude subscription.
func ClaudeTokenPath(name string) (string, error) {
	if err := ValidateClaudeTokenName(name); err != nil {
		return "", err
	}
	dir, err := workspace.DataHome()
	if err != nil {
		return "", err
	}
	if name == DefaultClaudeToken {
		return filepath.Join(dir, defaultClaudeTokenFileName), nil
	}
	return filepath.Join(dir, namedClaudeTokenDirName, name), nil
}

// HasClaudeToken reports whether a non-empty token called name is registered.
func HasClaudeToken(name string) bool {
	path, err := ClaudeTokenPath(name)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// SetClaudeToken saves token (trimmed of surrounding whitespace) under name.
func SetClaudeToken(name, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("token is empty")
	}
	path, err := ClaudeTokenPath(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return fmt.Errorf("writing token to %s: %w", path, err)
	}
	return nil
}

// ListClaudeTokens returns the names of the registered tokens, sorted.
func ListClaudeTokens() ([]string, error) {
	var names []string
	if HasClaudeToken(DefaultClaudeToken) {
		names = append(names, DefaultClaudeToken)
	}
	dir, err := workspace.DataHome()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, namedClaudeTokenDirName))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && ValidateClaudeTokenName(e.Name()) == nil && e.Name() != DefaultClaudeToken && HasClaudeToken(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// ResolveRepoClaudeToken returns the token name repoRoot's settings.local.json
// selects (DefaultClaudeToken when it names none) and that token's path.
//
// A name that is selected but not registered is an error, not a fallback to
// the default: the likely cause is a typo or a token registered on another
// machine, and quietly using the default would run this repository's work
// on an account the user deliberately did not pick for it. The default
// itself being unregistered is not an error here -- a VM without any token
// still boots, and callers warn about it instead.
func ResolveRepoClaudeToken(repoRoot string) (string, string, error) {
	local, err := config.LoadLocal(repoRoot)
	if err != nil {
		return "", "", err
	}
	name := local.ClaudeToken
	if name == "" {
		name = DefaultClaudeToken
	}
	path, err := ClaudeTokenPath(name)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", config.SettingsLocalPath(repoRoot), err)
	}
	if name != DefaultClaudeToken && !HasClaudeToken(name) {
		return "", "", fmt.Errorf("this repository uses the Claude token %q (%s), which is not registered on this host -- run `masuda claude set-token --name %s`, or pick another with `masuda claude use`",
			name, config.SettingsLocalPath(repoRoot), name)
	}
	return name, path, nil
}
