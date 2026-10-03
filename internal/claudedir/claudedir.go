// Package claudedir は対象リポジトリの`.masuda/claude/`（共有）と`.masuda/claude.local/`（個人）を
// 合成して、ゲストの`~/.claude/`へ置くもの（CLAUDE.md・rules・skills）を作る。
package claudedir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// SharedDir・LocalDir は`.masuda/`の下のディレクトリ名。
const (
	SharedDir = "claude"
	LocalDir  = "claude.local"
)

// ClaudeMD はゲストのループ規約の後ろに連結するファイルの相対パス。
const ClaudeMD = "CLAUDE.md"

// Merge はmasudaDir（`.masuda/`かその写し）の`claude/`に`claude.local/`を重ねた、
// 相対パス→中身を返す。置くのはCLAUDE.md・rules/*.md・skills/<name>/以下だけで、
// それ以外（agents/・settings.json等）は`.masuda/`からの相対パスでignoredに返す。
// 同じ相対パスは`.local`が勝つ（CLAUDE.mdも連結せず置き換える）。
func Merge(masudaDir string) (files map[string][]byte, ignored []string, err error) {
	files = map[string][]byte{}
	for _, name := range []string{SharedDir, LocalDir} {
		ig, err := collect(filepath.Join(masudaDir, name), name, files)
		if err != nil {
			return nil, nil, err
		}
		ignored = append(ignored, ig...)
	}
	return files, ignored, nil
}

func collect(dir, label string, files map[string][]byte) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var ignored []string
	ignore := func(rel string, d fs.DirEntry) {
		if d.IsDir() {
			rel += "/"
		}
		ignored = append(ignored, label+"/"+rel)
	}
	read := func(rel string) error {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		files[rel] = b
		return nil
	}
	for _, e := range entries {
		switch {
		case e.Name() == ClaudeMD && e.Type().IsRegular():
			if err := read(ClaudeMD); err != nil {
				return nil, err
			}
		case e.Name() == "rules" && e.IsDir():
			rules, err := os.ReadDir(filepath.Join(dir, "rules"))
			if err != nil {
				return nil, err
			}
			for _, r := range rules {
				rel := "rules/" + r.Name()
				if !r.Type().IsRegular() || path.Ext(r.Name()) != ".md" || strings.HasPrefix(r.Name(), ".") {
					ignore(rel, r)
					continue
				}
				if err := read(rel); err != nil {
					return nil, err
				}
			}
		case e.Name() == "skills" && e.IsDir():
			skills, err := os.ReadDir(filepath.Join(dir, "skills"))
			if err != nil {
				return nil, err
			}
			for _, s := range skills {
				rel := "skills/" + s.Name()
				if !s.IsDir() || strings.HasPrefix(s.Name(), ".") {
					ignore(rel, s)
					continue
				}
				ig, err := collectSkill(dir, rel, files)
				if err != nil {
					return nil, err
				}
				for _, p := range ig {
					ignored = append(ignored, label+"/"+p)
				}
			}
		default:
			ignore(e.Name(), e)
		}
	}
	return ignored, nil
}

// collectSkill はスキルのディレクトリを再帰的に読む。スキルはSKILL.mdから同じディレクトリの
// スクリプトや参照文書を指すことがあるので、ディレクトリごと写す。シンボリックリンクは
// リポジトリの外を指しうるので写さない。
func collectSkill(dir, rel string, files map[string][]byte) ([]string, error) {
	var ignored []string
	root := filepath.Join(dir, filepath.FromSlash(rel))
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		sub, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		sub = filepath.ToSlash(sub)
		switch {
		case d.IsDir():
			return nil
		case d.Type().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files[sub] = b
		default:
			ignored = append(ignored, sub)
		}
		return nil
	})
	return ignored, err
}

// Warning は無視したものを伝える1行。無ければ空。
func Warning(ignored []string) string {
	if len(ignored) == 0 {
		return ""
	}
	return fmt.Sprintf("masuda: warning: ignored .masuda/%s (only CLAUDE.md, rules/*.md and skills/ are copied to the guest; agents and settings.json are owned by masuda)",
		strings.Join(ignored, ", .masuda/"))
}

// Write はfilesをdstの下へ書く。filesが空なら何も作らない。
func Write(dst string, files map[string][]byte) error {
	for rel, b := range files {
		if err := checkRel(rel); err != nil {
			return err
		}
		p := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Load はWriteで書いたdirを読む。dirが無ければ空。
func Load(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == dir {
			return fs.SkipAll
		} else if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	return files, err
}

// Paths はfilesの相対パスを並べたもの。
func Paths(files map[string][]byte) []string {
	out := make([]string, 0, len(files))
	for p := range files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func checkRel(rel string) error {
	if rel == "" || path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("invalid .masuda/claude path %q", rel)
	}
	return nil
}
