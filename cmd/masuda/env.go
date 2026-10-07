package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/staging"
)

const envUsage = "env import <file> [--repo <dir>]"

func runEnv(args []string) error {
	return subcommand(args, envUsage, map[string]func([]string) error{
		"import": envImport,
	})
}

func envImport(args []string) error {
	c := newCommand("env import", "env import <file> [--repo <dir>]  (値は表示しない。秘密を含むならmasuda serveが要る)")
	repo := repoFlag(c)
	pos, err := c.parse(args, 1, 1)
	if err != nil {
		return err
	}
	root, err := absRepo(*repo)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	return importEnv(context.Background(), root, pos[0], data, c.clients().config, os.Stdout, os.Stderr)
}

// envEntry は.envの1行分の代入。
type envEntry struct {
	name  string
	value string
	line  int
}

// envItem は取り込み先を決めた代入。skipが空でなければ取り込まない。
type envItem struct {
	envEntry
	secret bool
	skip   string
}

const (
	envSkipClaudeToken = "skipped: Claude token (use masuda secret set)"
	envSkipUndeclared  = "skipped: not declared"
)

// importEnv は.envの中身を、宣言済みの秘密は秘密ストアへ（公開APIのSetSecret）、envFilesの
// 公開値はsettings.local.jsonのvarsへ入れる。書式・空の秘密・設定の読み込みの誤りは、
// 何も書かないうちにエラーにする。
//
// 秘密を先に、varsを後に書く。秘密の登録はmasuda serveへの接続を要して失敗しやすいので、
// 先に済ませれば、serveが動いていないときに何も書かずに終わる。秘密の途中で失敗したら
// 残りは書かずに止める（どちらの登録も上書きなので、直して同じコマンドを打ち直せば揃う）。
func importEnv(ctx context.Context, root, file string, data []byte, client apiv1connect.ConfigServiceClient, stdout, stderr io.Writer) error {
	entries, err := parseDotenv(data)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if err := requireWorkTreeTop(ctx, root); err != nil {
		return err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	local, err := config.LoadLocal(root)
	if err != nil {
		return err
	}
	items, err := classifyEnv(entries, cfg, local)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}

	status := make([]string, len(items))
	var vars []envItem
	for i, it := range items {
		switch {
		case it.skip != "":
			status[i] = it.skip
		default:
			status[i] = "not imported"
			if !it.secret {
				vars = append(vars, it)
			}
		}
	}

	var failure error
	for i, it := range items {
		if it.skip != "" || !it.secret {
			continue
		}
		req := &apiv1.SetSecretRequest{RepoRoot: root, Name: it.name, Value: it.value}
		if _, err := client.SetSecret(ctx, connect.NewRequest(req)); err != nil {
			status[i] = "failed"
			failure = fmt.Errorf("setting secret %s: %w", it.name, err)
			break
		}
		status[i] = "secret"
	}
	if failure == nil && len(vars) > 0 {
		if err := saveEnvVars(root, vars); err != nil {
			failure = fmt.Errorf("writing vars to %s: %w", config.SettingsLocalPath(root), err)
		} else {
			for i, it := range items {
				if it.skip == "" && !it.secret {
					status[i] = "var"
				}
			}
		}
	}

	t := newTable(stdout)
	fmt.Fprintln(t, "NAME\tRESULT")
	var undeclared []string
	for i, it := range items {
		fmt.Fprintf(t, "%s\t%s\n", it.name, status[i])
		if it.skip == envSkipUndeclared {
			undeclared = append(undeclared, it.name)
		}
	}
	t.Flush()
	if len(undeclared) > 0 {
		fmt.Fprintf(stderr, "not imported: %s\n  declare each in secrets of .masuda/settings.json (a secret) or add it to vars of an envFiles entry (not a secret), then run this again\n", strings.Join(undeclared, ", "))
	}
	return failure
}

// classifyEnv は名前ごとの取り込み先を決める。宣言済みの秘密の空の値はsecret setと同じく断る。
// Claudeのトークン（CLAUDE_CODE_OAUTH_TOKENと、claudeTokenで選んだ名前）を取り込まないのは、
// 既定の置き場所がユーザー単位で、リポジトリ単位で入れると意図せずユーザー単位の値を覆うため。
// envFilesのvarsにあっても公開値として平文で書かない。
func classifyEnv(entries []envEntry, cfg config.Settings, local config.LocalSettings) ([]envItem, error) {
	inVars := map[string]bool{}
	for _, f := range cfg.EnvFiles {
		for _, v := range f.Vars {
			inVars[v] = true
		}
	}
	items := make([]envItem, 0, len(entries))
	for _, e := range entries {
		it := envItem{envEntry: e}
		_, declared := cfg.Secret(e.name)
		switch {
		case e.name == config.ReservedSecret || e.name == local.ClaudeTokenName():
			it.skip = envSkipClaudeToken
		case declared:
			if e.value == "" {
				return nil, fmt.Errorf("line %d: %s is a declared secret but its value is empty", e.line, e.name)
			}
			it.secret = true
		case inVars[e.name]:
		default:
			it.skip = envSkipUndeclared
		}
		items = append(items, it)
	}
	return items, nil
}

// saveEnvVars はsettings.local.jsonのvarsに書き足す。他の項目を保つため、書く直前に読み直す。
// masuda serveも承認のときにこのファイルを書くが、serveの排他は別プロセスのここには効かない。
// 読み直してから書くまでの間にserveが承認を書くと、その承認は失われる。varsを書くRPCは
// 公開APIの契約に無いので、窓を短くするに留めている。
func saveEnvVars(root string, vars []envItem) error {
	local, err := config.LoadLocal(root)
	if err != nil {
		return err
	}
	if local.Vars == nil {
		local.Vars = map[string]string{}
	}
	for _, v := range vars {
		local.Vars[v.name] = v.value
	}
	return config.SaveLocal(root, local)
}

// requireWorkTreeTop はrootが作業ツリーのトップであることを確かめる。varsだけならserveを
// 通らないので、他のコマンドでserveがしている検査をここでする（サブディレクトリに
// .masuda/settings.local.jsonを作らないように）。
func requireWorkTreeTop(ctx context.Context, root string) error {
	top, err := staging.TopLevel(ctx, root)
	if err != nil {
		return err
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	got, err := filepath.EvalSymlinks(top)
	if err != nil {
		return err
	}
	if filepath.Clean(want) != filepath.Clean(got) {
		return fmt.Errorf("%s is not the top of its work tree (%s)", root, top)
	}
	return nil
}

// parseDotenv は.envを読む。誤りは行番号と名前だけで知らせ、行の中身（値）は出さない。
//
// 書式:
//   - 空行と、空白を除いて#で始まる行は無視する。先頭の`export `は読み飛ばす
//   - `NAME=VALUE`。NAMEと=の前後の空白は無視する
//   - クォートしない値は前後の空白を落とし、空白に続く#から後をコメントとして捨てる
//   - '...'は中身をそのまま使う。"..."はmasudaが生成する.env（guest.DotEnv）と同じエスケープ
//     （\n \r \t \\ \" \$ \`）だけを解く。それ以外の\はエラー（黙って別の値にしないため）
//   - クォートは1行で閉じる（複数行の値は扱わない）。閉じた後には空白と#のコメントだけを置ける
//   - ${VAR}等の展開はしない
//   - 同じ名前が2回出たらエラー（後勝ちにすると、古い値が残った行に気づけない）
func parseDotenv(data []byte) ([]envEntry, error) {
	var out []envEntry
	seen := map[string]int{}
	for i, raw := range strings.Split(string(data), "\n") {
		n := i + 1
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimSpace(rest)
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected NAME=VALUE", n)
		}
		key = strings.TrimSpace(key)
		if !config.ValidName(key) {
			return nil, fmt.Errorf("line %d: invalid variable name", n)
		}
		value, err := parseDotenvValue(val)
		if err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", n, key, err)
		}
		if prev, dup := seen[key]; dup {
			return nil, fmt.Errorf("line %d: %s is already set on line %d", n, key, prev)
		}
		seen[key] = n
		out = append(out, envEntry{name: key, value: value, line: n})
	}
	return out, nil
}

var dotenvEscapes = map[byte]byte{'n': '\n', 'r': '\r', 't': '\t', '\\': '\\', '"': '"', '$': '$', '`': '`'}

func parseDotenvValue(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	switch s[0] {
	case '\'':
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", errors.New("unterminated single quote")
		}
		return s[1 : 1+end], afterQuote(s[2+end:])
	case '"':
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			switch c := s[i]; c {
			case '"':
				return b.String(), afterQuote(s[i+1:])
			case '\\':
				i++
				if i == len(s) {
					return "", errors.New("unterminated double quote")
				}
				r, ok := dotenvEscapes[s[i]]
				if !ok {
					return "", errors.New("unsupported escape in double quotes (allowed: \\n \\r \\t \\\\ \\\" \\$ \\`)")
				}
				b.WriteByte(r)
			default:
				b.WriteByte(c)
			}
		}
		return "", errors.New("unterminated double quote")
	}
	for i := 1; i < len(raw); i++ {
		if raw[i] == '#' && (raw[i-1] == ' ' || raw[i-1] == '\t') {
			raw = raw[:i]
			break
		}
	}
	return strings.TrimSpace(raw), nil
}

func afterQuote(rest string) error {
	rest = strings.TrimSpace(rest)
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return errors.New("unexpected text after the closing quote")
	}
	return nil
}
