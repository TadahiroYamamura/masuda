// Package perspectives は同梱のレビュー観点を持つ。各ファイルはYAML frontmatter
// （name・trigger・enable）付きのMarkdownで、本文がそのまま観点のプロンプトになり、
// ファイル名（拡張子を除く）が観点のIDになる。`masuda init`が対象リポジトリの
// `.masuda/reviews/`へ書き出し、同じパスのファイルがあればそちらが丸ごと優先される。
package perspectives

import (
	"embed"
	"io/fs"
)

//go:embed builtin/*.md
var builtin embed.FS

// Builtin は同梱の観点ファイルを`<id>.md`の名前で並べたFSを返す。
func Builtin() fs.FS {
	sub, err := fs.Sub(builtin, "builtin")
	if err != nil {
		panic(err)
	}
	return sub
}
