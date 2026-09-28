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
	"path/filepath"

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
