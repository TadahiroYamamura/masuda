package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/claudedir"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/guest"
	"github.com/TadahiroYamamura/masuda/internal/perspectives"
)

//go:embed templates/Dockerfile templates/settings.json templates/prime.md
var templates embed.FS

// localIgnores は対象リポジトリの.gitignoreに足す行。利用者ごとの承認と、個人用の
// `~/.claude/`の中身（`.masuda/claude.local/`）をコミットさせない。
var localIgnores = []struct{ line, parent string }{
	{config.DirName + "/" + config.SettingsLocalFileName, config.DirName},
	{config.DirName + "/" + claudedir.LocalDir + "/", config.DirName},
	{primeClaudeMDFile, ""},
	{primeSettingsFile, ".claude"},
}

// ホストのエージェントにmasudaの使い方を読ませる先。共有のCLAUDE.md・.claude/settings.jsonに
// 書かないのは、コミットされたものは/workspaceのcloneでゲストにも届くため。ゲストにはmasudaの
// バイナリが無く、フックは失敗し、ゲストのエージェントがホストの使い方を読むことになる。
const (
	primeClaudeMDFile = "CLAUDE.local.md"
	primeSettingsFile = ".claude/settings.local.json"
	primeHookCommand  = "masuda prime --hook-json"
	primeBeginMarker  = "<!-- BEGIN MASUDA -->"
)

// primeDenyRulesは、人間が判断することを前提にしたコマンド。ホストのエージェントに打たせない。
// denyはallowで例外を作れないので、`list`・`show`を残すためにサブコマンドごとに並べる。
var primeDenyRules = []string{
	"Bash(masuda secret set *)",
	"Bash(masuda secret approve *)",
	"Bash(masuda secret reject *)",
	"Bash(masuda egress approve *)",
	"Bash(masuda egress reject *)",
	"Bash(masuda gate approve *)",
	"Bash(masuda gate reject *)",
	"Bash(masuda gate comment *)",
	"Bash(masuda gate dismiss *)",
	"Bash(masuda gate halt *)",
	"Bash(masuda gate redo *)",
	"Bash(masuda privileged-command approve *)",
	"Bash(masuda remove *)",
}

// primeAskRulesは、エージェントが答えてよいが、打つ前に人間に確かめさせるコマンド。
var primeAskRules = []string{
	"Bash(masuda question answer *)",
}

const primeClaudeMDSection = primeBeginMarker + `
## masuda

このリポジトリではmasudaのワークフローでエージェントの作業をVMに委ねられる。使い方は` + "`masuda prime`" + `が出す（セッションの開始時にフックで読み込まれる）。
<!-- END MASUDA -->
`

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
		return nil
	}
	// settings.jsonはコメントを書けないJSONなので、雛形のegressが空である理由をここで伝える。
	fmt.Println("note: the Claude API (api.anthropic.com) is always reachable; list other hosts the VM needs in egress of .masuda/settings.json")
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
		data = renderTemplate(data)
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
	if added, err := ensurePrimeSection(filepath.Join(root, primeClaudeMDFile)); err != nil {
		return created, err
	} else if added {
		created = append(created, primeClaudeMDFile)
	}
	added, err := ensureClaudeLocalSettings(filepath.Join(root, primeSettingsFile))
	if err != nil {
		return created, err
	}
	for _, a := range added {
		created = append(created, primeSettingsFile+" ("+a+")")
	}
	for _, ig := range localIgnores {
		added, err := ensureIgnored(filepath.Join(root, ".gitignore"), ig.line, ig.parent)
		if err != nil {
			return created, err
		}
		if added {
			created = append(created, ".gitignore ("+ig.line+")")
		}
	}
	return created, nil
}

// ensurePrimeSection はCLAUDE.local.mdにmasudaの節が無ければ末尾に足す。
func ensurePrimeSection(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if strings.Contains(string(data), primeBeginMarker) {
		return false, nil
	}
	var b strings.Builder
	b.Write(data)
	if len(data) > 0 {
		if !strings.HasSuffix(string(data), "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(primeClaudeMDSection)
	return true, os.WriteFile(path, []byte(b.String()), 0o644)
}

// ensureClaudeLocalSettings はsettings.local.jsonに、masuda primeのSessionStartフックと
// masudaのコマンドの権限の規則のうち無いものを足し、足したものの説明を返す。他の設定・フック・
// 規則はそのまま残す（キーの並びはencoding/jsonの順になる）。読めないJSONは書き換えずにエラーにする。
func ensureClaudeLocalSettings(path string) ([]string, error) {
	settings := map[string]any{}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &settings); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	hooks, err := jsonObject(settings, "hooks")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	perms, err := jsonObject(settings, "permissions")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	groups, err := jsonArray(hooks, "SessionStart")
	if err != nil {
		return nil, fmt.Errorf("%s: hooks.%w", path, err)
	}
	var added []string
	if !hasHookCommand(groups, primeHookCommand) {
		hooks["SessionStart"] = append(groups, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": primeHookCommand}},
		})
		settings["hooks"] = hooks
		added = append(added, "SessionStart: "+primeHookCommand)
	}
	for _, r := range []struct {
		key   string
		rules []string
	}{{"deny", primeDenyRules}, {"ask", primeAskRules}} {
		list, err := jsonArray(perms, r.key)
		if err != nil {
			return nil, fmt.Errorf("%s: permissions.%w", path, err)
		}
		n := 0
		for _, rule := range r.rules {
			if !slices.Contains(list, any(rule)) {
				list = append(list, rule)
				n++
			}
		}
		if n > 0 {
			perms[r.key] = list
			settings["permissions"] = perms
			unit := "rules"
			if n == 1 {
				unit = "rule"
			}
			added = append(added, fmt.Sprintf("permissions.%s: %d %s", r.key, n, unit))
		}
	}
	if len(added) == 0 {
		return nil, nil
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return added, os.WriteFile(path, append(out, '\n'), 0o644)
}

func hasHookCommand(groups []any, command string) bool {
	for _, g := range groups {
		g, _ := g.(map[string]any)
		hs, _ := g["hooks"].([]any)
		for _, h := range hs {
			if h, _ := h.(map[string]any); h["command"] == command {
				return true
			}
		}
	}
	return false
}

// jsonObject はm[key]のオブジェクトを返す。無ければ空のオブジェクト。
func jsonObject(m map[string]any, key string) (map[string]any, error) {
	switch v := m[key].(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return v, nil
	default:
		return nil, fmt.Errorf("%s is not an object", key)
	}
}

// jsonArray はm[key]の配列を返す。無ければnil。
func jsonArray(m map[string]any, key string) ([]any, error) {
	switch v := m[key].(type) {
	case nil:
		return nil, nil
	case []any:
		return v, nil
	default:
		return nil, fmt.Errorf("%s is not an array", key)
	}
}

// renderTemplate は雛形の印を埋める。text/templateにしないのは、Dockerfileに利用者が
// `{{`を含む行（Goのテンプレートを使うツールの例など）を書き写したときに壊れないようにするため。
func renderTemplate(data []byte) []byte {
	return []byte(strings.NewReplacer(
		"__CLAUDE_CODE_VERSION__", guest.ClaudeCodeVersion,
		"__MASUDA_VERSION__", version,
	).Replace(string(data)))
}

// ensureIgnored はgitignoreにlineが無ければ末尾に足す。同じパターンを先頭`/`付きで書いた行と、
// lineを含むディレクトリparent（`.masuda`・`.masuda/`・`.masuda/*`、先頭`/`付きも。リポジトリ直下の
// ファイルでは空）を無視する行も既にあるものとみなす。gitignoreの規則をすべて解釈するのではなく、
// 利用者が`.masuda/`ごと無視している（M8の段階2）ときに重複した行を足さないためのもの。
// `git check-ignore`に頼らないのは、利用者のグローバルな除外設定に左右されず、
// リポジトリの.gitignoreに書いてあるかだけで決めたいため（チームメイトの環境には無い）。
func ensureIgnored(path, line, parent string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	covering := map[string]bool{line: true, strings.TrimSuffix(line, "/"): true}
	if parent != "" {
		for _, p := range []string{parent, parent + "/", parent + "/*", parent + "/**"} {
			covering[p] = true
		}
	}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimPrefix(strings.TrimSpace(l), "/")
		if covering[l] {
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
