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
