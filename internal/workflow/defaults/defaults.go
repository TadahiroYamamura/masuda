// Package defaults holds the workflow and agent definitions shipped in the
// binary. A repository overrides any of them by placing a file at the same
// path under .masuda/ (ADR-0064).
package defaults

import (
	"embed"
	"io/fs"
)

//go:embed workflows agents
var files embed.FS

// FS returns the bundled definitions, rooted like a .masuda/ directory.
func FS() fs.FS { return files }
