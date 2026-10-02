package privileged

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// ValidatePattern はinputs・outputsの1項目を検査する。/workspaceからの相対パスで、`..`・`.`・
// 空のセグメントを含まないこと。各セグメントはpath.Matchの構文か`**`（0個以上のセグメント）。
func ValidatePattern(p string) error {
	if strings.TrimSpace(p) == "" {
		return errors.New("empty pattern")
	}
	if path.IsAbs(p) || strings.Contains(p, `\`) {
		return fmt.Errorf("%q must be relative to /workspace", p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "":
			return fmt.Errorf("%q has an empty path segment", p)
		case ".", "..":
			return fmt.Errorf("%q must not contain %q", p, seg)
		case "**":
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("%q: %w", p, err)
		}
	}
	return nil
}

// Match はファイルのパスname（/workspaceからの相対、`/`区切り）がpatternに当たるかを返す。
// path.Matchはセグメントをまたぐ`**`を扱えないので、セグメント単位で自前で照合する。
// ディレクトリ名だけのパターンは中のファイルに当たらない（中身は`dir/**`で書く）。
func Match(pattern, name string) bool {
	return matchSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegs(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// 連続する`**`は1つと同じ。残りのパターンをnameの各位置から試す。
			for len(pat) > 0 && pat[0] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 0 {
				return len(name) > 0
			}
			for i := 0; i < len(name); i++ {
				if matchSegs(pat, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// baseDir はpatternのうちワイルドカードを含まない先頭のセグメント（findを始める場所）。
// 先頭からワイルドカードなら"."。
func baseDir(pattern string) string {
	var lit []string
	for _, seg := range strings.Split(pattern, "/") {
		if seg == "**" || strings.ContainsAny(seg, `*?[\`) {
			break
		}
		lit = append(lit, seg)
	}
	if len(lit) == 0 {
		return "."
	}
	return strings.Join(lit, "/")
}
