// Package perspectives embeds masuda's built-in review perspectives — the
// same 14 checks that used to live hardcoded in
// orchestrator/perspectives/config.py — as the canonical source `masuda
// init` copies into a target repository's .masuda/reviews/ (ADR-0024). Each
// file is Markdown with YAML frontmatter (name only — ADR-0025 dropped
// category/severity, both unused dead data) plus a free-text body that
// becomes the perspective's review_prompt verbatim; the filename (minus
// extension) is the perspective's stable ID.
package perspectives

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

//go:embed builtin/*.md
var builtinFS embed.FS

const builtinDir = "builtin"

// ReviewsDirName is the subdirectory of config.DirName that holds
// perspective files.
const ReviewsDirName = "reviews"

// ReviewsDir returns the absolute path to repoRoot's perspective directory
// (<repoRoot>/.masuda/reviews).
func ReviewsDir(repoRoot string) string {
	return filepath.Join(repoRoot, config.DirName, ReviewsDirName)
}

// WriteBuiltins writes every embedded built-in perspective file into
// reviewsDir (created if it doesn't already exist), preserving filenames —
// the one-time seed `masuda init` performs. Callers are responsible for not
// calling this against an already-initialized .masuda/ (ADR-0024: masuda
// init is a one-shot operation, never a merge back into a project's
// possibly user-edited .masuda/reviews/).
func WriteBuiltins(reviewsDir string) error {
	entries, err := fs.ReadDir(builtinFS, builtinDir)
	if err != nil {
		return fmt.Errorf("reading embedded builtin perspectives: %w", err)
	}
	if err := os.MkdirAll(reviewsDir, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		data, err := fs.ReadFile(builtinFS, filepath.Join(builtinDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(reviewsDir, entry.Name()), data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", entry.Name(), err)
		}
	}
	return nil
}
