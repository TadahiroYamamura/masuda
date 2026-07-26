// Package config reads .masuda.json, an optional, user-edited file
// committed at a target repository's root. It lets a repo declare defaults
// for masuda's own CLI flags — which Docker image to run, and which branch
// is the repo's trunk — so a user working in that repo doesn't have to
// repeat the same flags on every invocation.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FileName is the config file's name, expected at a repository's root.
const FileName = ".masuda.json"

// Config is the on-disk shape of .masuda.json. All fields are optional —
// an absent file, or an absent field within one, means "use masuda's
// built-in default."
type Config struct {
	// Image is the Docker image `masuda sandbox start` / `masuda review
	// start` run when --image isn't passed explicitly.
	Image string `json:"image,omitempty"`
	// Base is the repo's trunk branch, used as the default for both --base
	// (the branch new work starts from) and --into (the branch a merge
	// lands on) when neither is passed explicitly — the two are almost
	// always the same branch in practice.
	Base string `json:"base,omitempty"`
}

// Load reads .masuda.json from repoRoot. A missing file is not an error —
// it returns a zero-value Config, so callers can treat every field as
// "unset, fall back to the built-in default."
func Load(repoRoot string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, FileName))
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", FileName, err)
	}
	return cfg, nil
}
