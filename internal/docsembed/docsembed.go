// Package docsembed は配布するmasudaに埋め込む文書。中身はリリースのビルドが写す
// （content/README.md）。
package docsembed

import (
	"embed"
	"io/fs"
)

//go:embed all:content
var content embed.FS

// Markdown は準備後のdocs/のMarkdown（パスはdocs/からの相対）。埋め込まれていなければfalse。
func Markdown() (fs.FS, bool) { return sub("content/md", "index.md") }

// Site はmkdocsで組んだサイト。埋め込まれていなければfalse。
func Site() (fs.FS, bool) { return sub("content/site", "index.html") }

func sub(dir, marker string) (fs.FS, bool) {
	f, err := fs.Sub(content, dir)
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(f, marker); err != nil {
		return nil, false
	}
	return f, true
}
