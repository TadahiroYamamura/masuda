// Package guest は、sandboxを作った直後にホストがゲストへ置くもの
// （docs/guest-protocol.md「起動時にホストがゲストへ置くもの」）と、メインセッションの起動を扱う。
package guest

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/TadahiroYamamura/masuda-engine/engine"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
)

// User はゲストでエージェントが動くユーザー。イメージの要件（uid 1000の`ubuntu`）に合わせる。
const User = "ubuntu"

// Home はUserのホームディレクトリ。
const Home = "/home/" + User

// HooksURL はゲストのClaude Codeフックの送り先。
const HooksURL = "http://masuda.internal:7000/hooks"

// MCPURL はゲストから見たmasudaのMCPサーバー。
const MCPURL = "http://masuda.internal:7000/mcp"

// MCPHost・MCPPort はsandboxのtcp_mapsでワークスペースのMCPポートへ対応付ける宛先。
const (
	MCPHost = "masuda.internal"
	MCPPort = 7000
)

// TokenEnv はClaude APIのトークン（ゲストではプレースホルダ）を入れる環境変数。
const TokenEnv = "CLAUDE_CODE_OAUTH_TOKEN"

// TmuxSession はメインセッションを動かすtmuxのセッション名。
const TmuxSession = "claude-work"

//go:embed loop-claude.md
var loopRules []byte

// Agent はゲストの`~/.claude/agents/<Name>`へ置くサブエージェント定義。
type Agent struct {
	Name    string // ファイル名（"smoke-planner.md"）
	Content []byte
}

// Layout はPrepareに渡すもの。
type Layout struct {
	SandboxID string
	// Branch はcloneしてチェックアウトするブランチ（bundleに入っているもの）。
	Branch string
	// Bundle はstagingから作ったbundleのホスト側パス。refs/heads/<Branch>を含むこと。
	Bundle string
	Agents []Agent
	// ClaudeSettings は対象リポジトリのclaudeSettings（JSONオブジェクト、無ければnil）。
	// `~/.claude/settings.json`へフック設定と合成する。
	ClaudeSettings json.RawMessage
	// EnvFiles は作業ツリーに生成するファイル。Pathは/workspaceからの相対パス（検査済みであること）。
	EnvFiles []EnvFile
	// Checks はチェック名→シェルコマンド。`/masuda/checks/<名前>`に実行可能スクリプトとして置く。
	Checks map[string]string
}

// EnvFile は作業ツリーに生成するdotenv形式のファイル1つ。
type EnvFile struct {
	Path string
	Vars []EnvVar
}

// EnvVar はEnvFileの1行。
type EnvVar struct{ Name, Value string }

// ChecksDir はチェックのスクリプトを置くゲストのディレクトリ。
const ChecksDir = "/masuda/checks"

// bundleGuestPath はbundleを置くゲストのパス。cloneが終わったら消す。
const bundleGuestPath = "/masuda/bootstrap.bundle"

// Prepare はゲストの初期配置を行う。
//
// ゲストで動かすコマンドはcwdを`/`にして相対パスで書く。フェイクsandboxはcwdだけを
// ゲストroot下へ写像し、コマンド文字列中の絶対パスは写像できないので、こう書けば
// 実VMとフェイクの両方で同じコマンドが通る。
func Prepare(ctx context.Context, c sandboxv1connect.SandboxServiceClient, l Layout) error {
	f, err := os.Open(l.Bundle)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := WriteFile(ctx, c, l.SandboxID, bundleGuestPath, f, 0o644); err != nil {
		return err
	}
	clone := fmt.Sprintf("git clone --quiet -b %s %s workspace && rm -f %s",
		shellQuote(l.Branch), shellQuote(strings.TrimPrefix(bundleGuestPath, "/")), shellQuote(strings.TrimPrefix(bundleGuestPath, "/")))
	if _, err := Shell(ctx, c, l.SandboxID, "/", clone); err != nil {
		return fmt.Errorf("cloning into /workspace: %w", err)
	}

	if err := WriteBytes(ctx, c, l.SandboxID, Home+"/.claude/CLAUDE.md", loopRules, 0o644); err != nil {
		return err
	}
	for _, a := range l.Agents {
		if a.Name == "" || a.Name != path.Base(a.Name) || strings.HasPrefix(a.Name, ".") {
			return fmt.Errorf("invalid agent file name %q", a.Name)
		}
		if err := WriteBytes(ctx, c, l.SandboxID, Home+"/.claude/agents/"+a.Name, a.Content, 0o644); err != nil {
			return err
		}
	}
	settings, err := Settings(l.ClaudeSettings)
	if err != nil {
		return err
	}
	if err := WriteBytes(ctx, c, l.SandboxID, Home+"/.claude/settings.json", settings, 0o644); err != nil {
		return err
	}
	if err := WriteBytes(ctx, c, l.SandboxID, Home+"/.claude.json", ClaudeJSON(), 0o600); err != nil {
		return err
	}
	if err := writeEnvFiles(ctx, c, l.SandboxID, l.EnvFiles); err != nil {
		return err
	}
	return writeChecks(ctx, c, l.SandboxID, l.Checks)
}

// writeEnvFiles は生成したファイルを作業ツリーへ置き、ゲストの`.git/info/exclude`に足す。
// 対象リポジトリが`.env`をgitignoreしていなくても、生成物（プレースホルダ入り）がスナップショットに
// 入って計画外の変更として現れたり、commitに載ったりしないようにするため。
func writeEnvFiles(ctx context.Context, c sandboxv1connect.SandboxServiceClient, id string, files []EnvFile) error {
	if len(files) == 0 {
		return nil
	}
	var exclude []string
	for _, f := range files {
		p := path.Clean(f.Path)
		if p == "." || path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") {
			return fmt.Errorf("env file path %q escapes /workspace", f.Path)
		}
		if err := WriteBytes(ctx, c, id, "/workspace/"+p, DotEnv(f.Vars), 0o600); err != nil {
			return err
		}
		exclude = append(exclude, "/"+p)
	}
	script := "printf '%s\\n' " + strings.Join(quoteAll(exclude), " ") + " >> workspace/.git/info/exclude"
	if res, err := Shell(ctx, c, id, "/", "mkdir -p workspace/.git/info && "+script); err != nil {
		return fmt.Errorf("excluding env files: %w", err)
	} else if res.ExitCode != 0 {
		return fmt.Errorf("excluding env files: exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// writeChecks はチェックをゲストの`/masuda/checks/<名前>`へ実行可能スクリプトとして置く。
// コマンドはsettings.jsonにシェルの1行として書かれるので、`sh -e`で/workspaceから動かす。
func writeChecks(ctx context.Context, c sandboxv1connect.SandboxServiceClient, id string, checks map[string]string) error {
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "" || name != path.Base(name) || strings.HasPrefix(name, ".") {
			return fmt.Errorf("invalid check name %q", name)
		}
		if err := WriteBytes(ctx, c, id, ChecksDir+"/"+name, CheckScript(checks[name]), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// CheckScript はチェック1つのスクリプトの中身。
func CheckScript(command string) []byte {
	return []byte("#!/bin/sh -e\ncd /workspace\n" + strings.TrimRight(command, "\n") + "\n")
}

// DotEnv はvarsをdotenv形式にする。値は常にダブルクォートで囲み、`\`・`"`・`$`・バッククォートを
// エスケープする（展開や変数参照をする方言でも値がそのまま読めるように）。改行は`\n`と書く
// （dotenvの多くは戻すが、shの`.`で読むと2文字のまま残る）。
func DotEnv(vars []EnvVar) []byte {
	var b strings.Builder
	for _, v := range vars {
		r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "\n", `\n`, "`", "\\`")
		fmt.Fprintf(&b, "%s=\"%s\"\n", v.Name, r.Replace(v.Value))
	}
	return []byte(b.String())
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = shellQuote(s)
	}
	return out
}

// ClaudeJSON はゲストの`~/.claude.json`。MCPサーバー`masuda`を利用者スコープで登録する。
// `/workspace/.mcp.json`（プロジェクトスコープ）に置かないのは、対象リポジトリの作業ツリーへ
// masudaのファイルを混ぜるとスナップショットに入り、計画外の変更として現れるため。
// hasCompletedOnboardingは、まっさらなHOMEで起動したclaudeが初回の対話画面で止まらないようにする。
func ClaudeJSON() []byte {
	b, _ := json.MarshalIndent(map[string]any{
		"hasCompletedOnboarding": true,
		"mcpServers": map[string]any{
			"masuda": map[string]any{"type": "http", "url": MCPURL},
		},
	}, "", "  ")
	return append(b, '\n')
}

// AgentFile はエンジンのエージェント定義から、ゲストの`~/.claude/agents/<name>.md`の中身を作る。
// Claude Codeが読むのはname・description・toolsと本文だけで、inputs・outputs・outcomesは
// タスクファイルで伝えるのでここには写さない。
func AgentFile(a *engine.Agent) Agent {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", yamlString(a.Name))
	fmt.Fprintf(&b, "description: %s\n", yamlString(a.Description))
	if a.Tools != nil {
		fmt.Fprintf(&b, "tools: %s\n", yamlString(strings.Join(a.Tools, ", ")))
	}
	b.WriteString("---\n")
	b.WriteString(a.Body)
	if !strings.HasSuffix(a.Body, "\n") {
		b.WriteString("\n")
	}
	return Agent{Name: a.Name + ".md", Content: []byte(b.String())}
}

// yamlString はsをYAMLのダブルクォート文字列にする。JSONの文字列はYAMLとしても読めるので
// エスケープはJSONに任せる。
func yamlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// LaunchOptions はメインセッションの起動に要るもの。
type LaunchOptions struct {
	SandboxID string
	// Token はCLAUDE_CODE_OAUTH_TOKENに入れる値（sandboxが返したプレースホルダ）。
	Token string
	// GitName・GitEmail はゲストでのコミットの作者。
	GitName, GitEmail string
	// Env はそのほかにメインセッションへ渡す環境変数（宣言した秘密のプレースホルダ）。
	Env map[string]string
}

// StartPrompt はメインセッションに最初に渡すプロンプト。
const StartPrompt = "~/.claude/CLAUDE.md のmasudaのループ規約に従い、MCPサーバーmasudaのnext_taskを呼んで始めてください。"

// Launch はtmuxでメインセッション（claude）を起動する。環境変数はtmuxサーバーを起こす
// このExecに渡し、セッション内のclaudeへ継承させる。フェイクsandboxでは呼ばないこと
// （ExecがホストでそのままtmuxとClaude Codeを起動してしまう）。
func Launch(ctx context.Context, c sandboxv1connect.SandboxServiceClient, o LaunchOptions) error {
	env := map[string]string{}
	for k, v := range o.Env {
		env[k] = v
	}
	for k, v := range map[string]string{
		TokenEnv: o.Token,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		// next_taskはゲート・質問の間ブロックするので、MCPのツール呼び出しを7日まで待たせる。
		"MCP_TOOL_TIMEOUT":    "604800000",
		"GIT_AUTHOR_NAME":     o.GitName,
		"GIT_AUTHOR_EMAIL":    o.GitEmail,
		"GIT_COMMITTER_NAME":  o.GitName,
		"GIT_COMMITTER_EMAIL": o.GitEmail,
	} {
		env[k] = v
	}
	cmd := "claude --dangerously-skip-permissions -- " + shellQuote(StartPrompt)
	res, err := Exec(ctx, c, &sandboxv1.ExecRequest{
		Id:   o.SandboxID,
		Argv: []string{"tmux", "new-session", "-d", "-s", TmuxSession, cmd},
		Cwd:  "/workspace",
		Env:  env,
	})
	if err != nil {
		return fmt.Errorf("starting tmux: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("starting tmux: exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// Settings はゲストの`~/.claude/settings.json`。対象リポジトリのclaudeSettings（nil可）に
// masudaのフックを重ねる。フックはいずれもstdinのJSONをそのまま`/hooks`へ送る。
//
// masudaが使うイベントはmasudaのフックで置き換え、それ以外のイベントのフックは残す。
// 同じイベントに対象リポジトリのフックを並べて残さないのは、活動の判定（Notification等）に
// 使う入力の前に別のフックが失敗・遅延して、観測が欠けるのを避けるため。
func Settings(claudeSettings json.RawMessage) ([]byte, error) {
	out := map[string]any{}
	if len(claudeSettings) > 0 {
		if err := json.Unmarshal(claudeSettings, &out); err != nil || out == nil {
			return nil, fmt.Errorf("claudeSettings must be a JSON object: %v", err)
		}
	}
	hooks, _ := out["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	cmd := "curl -s -X POST " + HooksURL + " -d @-"
	hook := []map[string]any{{"hooks": []map[string]any{{"type": "command", "command": cmd}}}}
	for _, ev := range []string{"Notification", "PostToolUse", "Stop", "SubagentStop", "SessionEnd"} {
		hooks[ev] = hook
	}
	out["hooks"] = hooks
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
