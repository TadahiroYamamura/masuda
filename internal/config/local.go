// local.go handles .masuda/settings.local.json: the per-user, gitignored
// counterpart to settings.json. Where settings.json is a project's
// committed *declaration* of what it wants (Issue #19: blindly trusted,
// must never carry secrets), this file is the *user's* explicit approval
// of those declarations plus the real secret values they need -- see
// MCPServerDecl's doc comment.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SettingsLocalFileName is the user-local settings file's name within
// DirName.
const SettingsLocalFileName = "settings.local.json"

// SettingsLocalPath returns the absolute path to repoRoot's user-local
// settings file.
func SettingsLocalPath(repoRoot string) string {
	return filepath.Join(repoRoot, DirName, SettingsLocalFileName)
}

// LocalSettings is the on-disk shape of .masuda/settings.local.json.
type LocalSettings struct {
	// MCPServers maps a server name (matching a Config.MCPServers key) to
	// this user's approval of it. A name absent here, or present with
	// Approved: false, means the daemon must never start that server --
	// see internal/statedaemon/mcpaggregator.
	MCPServers map[string]MCPServerApproval `json:"mcpServers,omitempty"`

	// EgressAllowlist is this user's approved subset of
	// Config.EgressAllowlist (Issue #11 M4). A hostname the sandbox VM
	// may reach is one that appears in *both* lists -- declared by the
	// repo and approved by the user -- see internal/sandbox's
	// resolveEgressAllowlist. Unlike MCPServers, this is a plain list,
	// not a map: there is no per-entry payload (env values, a decl hash)
	// to carry alongside the approval, just the hostname itself.
	EgressAllowlist []string `json:"egressAllowlist,omitempty"`
}

// MCPServerApproval is one user's decision about one declared MCP server.
type MCPServerApproval struct {
	Approved bool `json:"approved"`
	// DeclHash pins this approval to the exact Config.MCPServers[name]
	// declaration it was granted against (see DeclHash).
	DeclHash string `json:"declHash,omitempty"`
	// Env supplies real values for the names Config.MCPServers[name].Env
	// lists. This is the one place in masuda's config surface expected to
	// carry real credentials -- see SaveLocal's permissions.
	Env map[string]string `json:"env,omitempty"`
}

// LoadLocal reads repoRoot's .masuda/settings.local.json. A missing file
// is not an error -- zero value means "nothing approved yet," mirroring
// Load's treatment of a missing settings.json.
func LoadLocal(repoRoot string) (LocalSettings, error) {
	path := SettingsLocalPath(repoRoot)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return LocalSettings{}, nil
	}
	if err != nil {
		return LocalSettings{}, err
	}
	var s LocalSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return LocalSettings{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return s, nil
}

// SaveLocal atomically writes settings to repoRoot's
// .masuda/settings.local.json with 0600 permissions -- unlike
// settings.json's 0644, this file routinely carries real secret values
// (LocalSettings.MCPServers[*].Env), so it's locked to the owning user
// regardless of umask. Temp-file-plus-rename (same directory) so a crash
// mid-write never leaves a truncated file and a concurrent `masuda mcp
// approve` from two terminals never interleaves.
func SaveLocal(repoRoot string, settings LocalSettings) error {
	dir := filepath.Join(repoRoot, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+SettingsLocalFileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, SettingsLocalPath(repoRoot)); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
