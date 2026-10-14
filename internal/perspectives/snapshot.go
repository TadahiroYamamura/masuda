package perspectives

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ImplementSet は実装のレビューの集合の名前。同梱の観点はこの集合に重なり、同梱の
// reviewer・review-checkerと`Runner.Items(perspectives)`はこの集合を読む。
const ImplementSet = "implement"

// Merge は対象リポジトリの観点（repo、nil可）を集合ごとに読み、`<集合>/<id>.md`→中身を返す。
// ImplementSetだけは同梱の観点に重ね、repoの`<id>.md`が同じidの同梱観点を丸ごと置き換える。
// ほかの集合は同梱と重ねない（計画向けの集合に、コードの粒度の同梱の観点が混ざらないように）。
//
// repoの直下の`.md`・2段以上の深さ・`.`で始まる集合はエラーにする。読み飛ばすと、置いたつもりの
// 観点が黙って使われなくなるため。集合の中の`.md`以外のファイルは今までどおり無視する。
func Merge(repo fs.FS) (map[string][]byte, error) {
	all := map[string][]byte{}
	if err := readSet(Builtin(), ".", ImplementSet, all); err != nil {
		return nil, err
	}
	if repo != nil {
		entries, err := fs.ReadDir(repo, ".")
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		for _, e := range entries {
			switch {
			case e.IsDir():
				if strings.HasPrefix(e.Name(), ".") {
					return nil, fmt.Errorf("%s/: a set name must not start with \".\"", e.Name())
				}
				if err := readSet(repo, e.Name(), e.Name(), all); err != nil {
					return nil, err
				}
			case isPerspective(e):
				return nil, fmt.Errorf("%s: perspectives are grouped into sets; move it to %s/ (the set for reviewing the implementation) or another set directory", e.Name(), ImplementSet)
			}
		}
	}
	// 重ねた後で外すので、リポジトリの同名ファイルを`enable: false`にすれば同梱の観点も外せる。
	for name, b := range all {
		on, err := enabled(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if !on {
			delete(all, name)
		}
	}
	return all, nil
}

// readSet はsrcのdirの`.md`をsetの観点としてallへ読む。dirの中のディレクトリはエラー。
func readSet(src fs.FS, dir, set string, all map[string][]byte) error {
	entries, err := fs.ReadDir(src, dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			return fmt.Errorf("%s/%s/: sets cannot be nested", set, e.Name())
		}
		if !isPerspective(e) {
			continue
		}
		b, err := fs.ReadFile(src, path.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		all[set+"/"+e.Name()] = b
	}
	return nil
}

// isPerspective はeが観点のファイル（`.`で始まらない`.md`の通常ファイル）か。
func isPerspective(e fs.DirEntry) bool {
	return e.Type().IsRegular() && path.Ext(e.Name()) == ".md" && !strings.HasPrefix(e.Name(), ".")
}

// enabled は観点ファイルのfrontmatterの`enable`を返す。frontmatterやキーが無ければ有効。
func enabled(b []byte) (bool, error) {
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return true, nil
	}
	front, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return true, nil
	}
	var fm struct {
		Enable *bool `yaml:"enable"`
	}
	if err := yaml.Unmarshal([]byte(front), &fm); err != nil {
		return false, fmt.Errorf("frontmatter: %w", err)
	}
	return fm.Enable == nil || *fm.Enable, nil
}

// Snapshot はMerge(repo)の結果をdstへ`<集合>/<id>.md`の形で書く。実行はこの写しだけを観点として使う
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
		p := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dst)
}

// Load はSnapshotで書いたdirの`<集合>/<id>.md`→中身を返す。
func Load(dir string) (map[string][]byte, error) {
	sets, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, set := range sets {
		entries, err := os.ReadDir(filepath.Join(dir, set.Name()))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !isPerspective(e) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, set.Name(), e.Name()))
			if err != nil {
				return nil, err
			}
			out[set.Name()+"/"+e.Name()] = b
		}
	}
	return out, nil
}
