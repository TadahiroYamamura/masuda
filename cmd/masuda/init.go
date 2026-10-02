package main

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/perspectives"
)

//go:embed templates/Dockerfile templates/settings.json
var templates embed.FS

// localIgnore は対象リポジトリの.gitignoreに足す行。利用者ごとの承認をコミットさせない。
const localIgnore = config.DirName + "/" + config.SettingsLocalFileName

// runInit は対象リポジトリに`.masuda/`の雛形を置く。serveを介さずローカルで書く
// （公開APIにinitは無く、書く先は利用者の作業ツリーだけのため）。既にあるファイルは上書きせず、
// 足りないものだけ足す。
func runInit(args []string) error {
	c := newCommand("init", "init [--repo <dir>]")
	repo := c.fs.String("repo", ".", "対象リポジトリ（作業ツリーのトップ）")
	if _, err := c.parse(args, 0, 0); err != nil {
		return err
	}
	root, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	created, err := initRepo(root)
	for _, p := range created {
		fmt.Println("created", p)
	}
	if err != nil {
		return err
	}
	if len(created) == 0 {
		fmt.Println("nothing to do: .masuda/ is already complete")
	}
	return nil
}

// initRepo は雛形を書き、作った（書き足した）ファイルのrootからの相対パスを返す。
func initRepo(root string) ([]string, error) {
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	var created []string
	write := func(rel string, data []byte) error {
		p := filepath.Join(root, rel)
		if _, err := os.Lstat(p); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
		created = append(created, rel)
		return nil
	}
	for rel, tmpl := range map[string]string{
		filepath.Join(config.DirName, config.SettingsFileName):                     "templates/settings.json",
		filepath.Join(config.DirName, "images", config.DefaultImage, "Dockerfile"): "templates/Dockerfile",
	} {
		data, err := templates.ReadFile(tmpl)
		if err != nil {
			return created, err
		}
		if err := write(rel, data); err != nil {
			return created, err
		}
	}
	reviews := perspectives.Builtin()
	names, err := fs.Glob(reviews, "*.md")
	if err != nil {
		return created, err
	}
	for _, name := range names {
		data, err := fs.ReadFile(reviews, name)
		if err != nil {
			return created, err
		}
		if err := write(filepath.Join(config.DirName, "reviews", name), data); err != nil {
			return created, err
		}
	}
	added, err := ensureIgnored(filepath.Join(root, ".gitignore"), localIgnore)
	if err != nil {
		return created, err
	}
	if added {
		created = append(created, ".gitignore ("+localIgnore+")")
	}
	return created, nil
}

// ensureIgnored はgitignoreにlineが無ければ末尾に足す。同じパターンを先頭`/`付きで書いた行も
// 既にあるものとみなす。
func ensureIgnored(path, line string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l == line || l == "/"+line {
			return false, nil
		}
	}
	var b strings.Builder
	b.Write(data)
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		b.WriteString("\n")
	}
	b.WriteString(line + "\n")
	return true, os.WriteFile(path, []byte(b.String()), 0o644)
}
