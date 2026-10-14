// Package perspectives はレビュー観点を持つ。各ファイルはYAML frontmatter
// （name・trigger・enable）付きのMarkdownで、本文がそのまま観点のプロンプトになり、
// ファイル名（拡張子を除く）が観点のIDになる。観点は段階ごとの集合
// （`.masuda/reviews/<集合>/`）に分けて置く。同梱の観点は実装のレビューの集合（ImplementSet）に
// 属し、`masuda init`が対象リポジトリの`.masuda/reviews/implement/`へ書き出す。同じパスの
// ファイルがあればそちらが丸ごと優先される。
package perspectives

import (
	"embed"
	"io/fs"
)

//go:embed builtin/*.md
var builtin embed.FS

// Builtin は同梱の観点ファイル（ImplementSetの集合）を`<id>.md`の名前で並べたFSを返す。
func Builtin() fs.FS {
	sub, err := fs.Sub(builtin, "builtin")
	if err != nil {
		panic(err)
	}
	return sub
}
