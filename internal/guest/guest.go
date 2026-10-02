// Package guest は、sandboxを作った直後にホストがゲストへ置くもの
// （docs/guest-protocol.md「起動時にホストがゲストへ置くもの」）を扱う。
// M3時点ではclone・ループ規約・サブエージェント定義・フック設定まで。
// .envの生成・MCPサーバー設定・環境変数・tmuxでのメインセッション起動はまだしない。
package guest

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
)

// User はゲストでエージェントが動くユーザー。イメージの要件（uid 1000の`ubuntu`）に合わせる。
const User = "ubuntu"

// Home はUserのホームディレクトリ。
const Home = "/home/" + User

// HooksURL はゲストのClaude Codeフックの送り先。
const HooksURL = "http://masuda.internal:7000/hooks"

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
}

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
	settings, err := Settings()
	if err != nil {
		return err
	}
	return WriteBytes(ctx, c, l.SandboxID, Home+"/.claude/settings.json", settings, 0o644)
}

// Settings はゲストの`~/.claude/settings.json`。フックはいずれもstdinのJSONを
// そのまま`/hooks`へ送る。対象リポジトリの`claudeSettings`との合成はまだしない。
func Settings() ([]byte, error) {
	cmd := "curl -s -X POST " + HooksURL + " -d @-"
	hook := []map[string]any{{"hooks": []map[string]any{{"type": "command", "command": cmd}}}}
	hooks := map[string]any{}
	for _, ev := range []string{"Notification", "PostToolUse", "Stop", "SubagentStop", "SessionEnd"} {
		hooks[ev] = hook
	}
	b, err := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
