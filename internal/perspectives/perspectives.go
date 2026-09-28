// Package perspectives holds masuda's 14 built-in review perspectives as
// the canonical source CI zips
// into a GitHub Release asset (ADR-0033; internal/selfupdate.SyncReviews
// fetches and extracts it) for `masuda init`/`masuda update` to materialize
// into a target repository's .masuda/reviews/ (ADR-0024). Each file is
// Markdown with YAML frontmatter (name — ADR-0025 dropped category/severity,
// both unused dead data; trigger — ADR-0027's natural language condition
// for whether the interim review should run this perspective against a
// single implementation step, in the same style as a
// Claude Skill's description field; enable — ADR-0033's opt-out flag,
// defaults to true) plus a free-text body that becomes the perspective's
// review_prompt verbatim; the filename (minus extension) is the
// perspective's stable ID.
package perspectives

import (
	"bytes"
	"fmt"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

// ReviewsDirName is the subdirectory of config.DirName that holds
// perspective files.
const ReviewsDirName = "reviews"

// ReviewsDir returns the absolute path to repoRoot's perspective directory
// (<repoRoot>/.masuda/reviews).
func ReviewsDir(repoRoot string) string {
	return filepath.Join(repoRoot, config.DirName, ReviewsDirName)
}

// Enabled reports whether perspective file src is switched on: its
// frontmatter's enable, true when absent (ADR-0033). A file without
// frontmatter counts as enabled; frontmatter that does not parse is an
// error, since guessing either way would silently run or skip a review.
func Enabled(src []byte) (bool, error) {
	src = bytes.TrimPrefix(src, []byte{0xEF, 0xBB, 0xBF})
	if !bytes.HasPrefix(src, []byte("---\n")) {
		return true, nil
	}
	rest := src[len("---\n"):]
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return false, fmt.Errorf("frontmatter is not closed with ---")
	}
	var front struct {
		Enable *bool `yaml:"enable"`
	}
	if err := yaml.Unmarshal(rest[:end], &front); err != nil {
		return false, fmt.Errorf("parsing frontmatter: %w", err)
	}
	return front.Enable == nil || *front.Enable, nil
}
