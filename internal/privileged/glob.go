package privileged

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/staging"
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

// WorktreeInputs はホストの作業ツリーrepoRootから、inputsのpatternsに当たるgitignoreされた
// 通常ファイルを返す（`masuda privileged-command run`）。gitignoreされていないファイルはツリーの
// スナップショットに入るので含めない。メインのゲストからの写し（RunJobのFromSandbox）と同じく、
// `.git`の中は見ず、シンボリックリンクは辿らず、許可ビットを保つ。
func WorktreeInputs(ctx context.Context, repoRoot string, patterns []string) ([]HostInput, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	files, err := staging.IgnoredFiles(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	var out []HostInput
	for _, rel := range files {
		if slices.Contains(strings.Split(rel, "/"), ".git") {
			continue
		}
		if !slices.ContainsFunc(patterns, func(p string) bool { return Match(p, rel) }) {
			continue
		}
		p := filepath.Join(repoRoot, filepath.FromSlash(rel))
		st, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() {
			continue
		}
		out = append(out, HostInput{HostPath: p, Rel: rel, Mode: st.Mode().Perm()})
	}
	return out, nil
}
