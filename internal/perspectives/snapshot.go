package perspectives

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Merge は同梱の観点に、対象リポジトリの観点（repo、nil可）を重ねた`<id>.md`→中身を返す。
// repoの`<id>.md`は同じidの同梱観点を丸ごと置き換え、同梱に無いidは足される。
// 直下の`.md`の通常ファイルだけを読む（サブディレクトリは観点ではない）。
func Merge(repo fs.FS) (map[string][]byte, error) {
	all := map[string][]byte{}
	for _, src := range []fs.FS{Builtin(), repo} {
		if src == nil {
			continue
		}
		entries, err := fs.ReadDir(src, ".")
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || path.Ext(e.Name()) != ".md" || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			b, err := fs.ReadFile(src, e.Name())
			if err != nil {
				return nil, err
			}
			all[e.Name()] = b
		}
	}
	return all, nil
}

// Snapshot はMerge(repo)の結果をdstへ書く。実行はこの写しだけを観点として使う
// （ホストの`Runner.Items(perspectives)`とゲストの`/masuda/reviews/`の両方）。
// dstが既にあれば作り直さない: 再開しても、実行開始時と同じ観点で続けるため。
func Snapshot(repo fs.FS, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	all, err := Merge(repo)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".reviews-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for name, b := range all {
		if err := os.WriteFile(filepath.Join(tmp, name), b, 0o600); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dst)
}

// Load はSnapshotで書いたdirの`<id>.md`→中身を返す。
func Load(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if !e.Type().IsRegular() || path.Ext(e.Name()) != ".md" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = b
	}
	return out, nil
}
