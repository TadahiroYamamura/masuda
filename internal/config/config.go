// Package config reads .masuda/settings.json, an optional, user-edited file
// committed at a target repository's root. It lets a repo declare defaults
// for masuda's own CLI flags — which Docker image to run, and which branch
// is the repo's trunk — so a user working in that repo doesn't have to
// repeat the same flags on every invocation.
//
// .masuda/settings.json supersedes the older single-file .masuda.json
// (ADR-0024): masuda init now populates a .masuda/ directory (this file
// plus .masuda/reviews/, internal/perspectives), and reading .masuda.json
// was deliberately dropped rather than kept as a fallback — a breaking
// change, not a migration.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DirName is the config directory's name, expected at a repository's root.
const DirName = ".masuda"

// SettingsFileName is the settings file's name within DirName.
const SettingsFileName = "settings.json"

// SettingsPath returns the absolute path to repoRoot's settings file.
func SettingsPath(repoRoot string) string {
	return filepath.Join(repoRoot, DirName, SettingsFileName)
}

// DockerfileName is the optional per-project sandbox Dockerfile's name
// within DirName (ADR-0032). masuda init materializes a starting template
// here (mirroring .masuda/reviews/); masuda update rebuilds it if present.
const DockerfileName = "Dockerfile"

// DockerfilePath returns the absolute path to repoRoot's .masuda/Dockerfile.
func DockerfilePath(repoRoot string) string {
	return filepath.Join(repoRoot, DirName, DockerfileName)
}

// GitignoreFileName is the optional, user-authored .gitignore's name within
// DirName -- not written by masuda init, but some repos add one (typically
// `*`) to keep DirName's own contents out of git entirely (ADR-0036).
const GitignoreFileName = ".gitignore"

// GitignorePath returns the absolute path to repoRoot's .masuda/.gitignore.
func GitignorePath(repoRoot string) string {
	return filepath.Join(repoRoot, DirName, GitignoreFileName)
}

// Config is the on-disk shape of .masuda/settings.json. All fields are
// optional — an absent file, or an absent field within one, means "use
// masuda's built-in default."
type Config struct {
	// Image is the Docker image `masuda sandbox start` / `masuda review
	// start` run when --image isn't passed explicitly.
	Image string `json:"image,omitempty"`
	// Base is the repo's trunk branch, used as the default for both --base
	// (the branch new work starts from) and --into (the branch a merge
	// lands on) when neither is passed explicitly — the two are almost
	// always the same branch in practice.
	Base string `json:"base,omitempty"`
	// ClaudeSettings is passed verbatim to the `claude` CLI's --settings
	// flag for every session masuda launches (phase 1-2 on the host, phase
	// 3-5 in the sandbox). Unlike Image/Base, this has no masuda-side
	// built-in default: masuda itself must not carry implicit Claude Code
	// settings, so an absent field means no --settings flag is added at
	// all, not "fall back to some default." `masuda init` populates it
	// with a starting value the user can freely edit (see cmd/masuda
	// init.go), the same way it materializes .masuda/reviews/ instead of
	// keeping built-in content implicit. masuda never interprets this
	// value — it's an opaque payload for Claude Code, not masuda.
	ClaudeSettings json.RawMessage `json:"claudeSettings,omitempty"`

	// MCPServers declares child MCP servers the per-workspace state daemon
	// should aggregate into its Claude-facing curated tool set (Issue #35,
	// ADR-0041's forward-pointer). The map key is the server's name --
	// used both as the `masuda mcp approve <name>` argument and as the
	// proxied tool name's namespace prefix ("<name>__<tool>").
	//
	// Declaring a server here does nothing on its own: this file is
	// committed to the target repository and so, like every other field
	// here, is not unconditionally trusted (Issue #19). The daemon only
	// starts a declared server once a matching, hash-pinned approval
	// exists in repoRoot/.masuda/settings.local.json (see LoadLocal) --
	// see DeclHash.
	MCPServers map[string]MCPServerDecl `json:"mcpServers,omitempty"`
}

// MCPServerDecl is one entry in Config.MCPServers: how to launch a child
// MCP server and which of its tools may ever reach Claude. It never
// carries secret values -- only the *names* of environment variables the
// child needs (Env); actual values live in the user-owned, gitignored
// settings.local.json (see LocalSettings.MCPServers[name].Env).
type MCPServerDecl struct {
	// Command is the executable to exec (e.g. "npx").
	Command string `json:"command"`
	// Args are passed to Command verbatim.
	Args []string `json:"args,omitempty"`
	// Env lists the names (never values) of environment variables the
	// child process needs. A name here with no corresponding value in the
	// user's approval blocks the daemon from starting this server at all.
	Env []string `json:"env,omitempty"`
	// Tools is the allowlist of this child server's own tool names that
	// may be proxied onto the curated set. Default-deny: any tool the
	// child reports that isn't listed here is never registered, no matter
	// what the user approved -- the declaration-side half of a two-guard
	// model (the user-side half is MCPServerApproval.Approved).
	Tools []string `json:"tools,omitempty"`
}

// DeclHash returns a stable fingerprint of decl (sha256 of its canonical
// JSON encoding). `masuda mcp approve` records this alongside a user's
// approval (MCPServerApproval.DeclHash) so the daemon can tell whether
// repoRoot's settings.json changed a server's declaration since it was
// last approved -- e.g. a malicious commit swapping an already-approved
// server's command/args. A hash mismatch is treated the same as "never
// approved" (see internal/statedaemon/mcpaggregator), without which
// approval-by-name alone would let a project-side change silently
// escalate a previously-reviewed declaration, reintroducing the
// "settings.json blindly trusted" problem this project/user split exists
// to avoid (Issue #19).
func DeclHash(decl MCPServerDecl) (string, error) {
	data, err := json.Marshal(decl)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Load reads .masuda/settings.json from repoRoot. A missing file is not an
// error — it returns a zero-value Config, so callers can treat every field
// as "unset, fall back to the built-in default."
func Load(repoRoot string) (Config, error) {
	path := SettingsPath(repoRoot)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}
