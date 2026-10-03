package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/guest"
)

// claudeInstallRe は雛形の形（`curl -fsSL https://claude.ai/install.sh | bash -s -- <版>`）の
// install行を拾う。引数はinstall.shが受け付ける`stable`・`latest`・版のいずれか。
var claudeInstallRe = regexp.MustCompile(`claude\.ai/install\.sh[ \t]*\|[ \t]*bash(?:[ \t]+-s(?:[ \t]+--)?[ \t]+([^\s\\]+))?`)

// pinnedClaudeCode はDockerfileのinstall行がinstall.shへ渡す引数を返す。引数が無ければ""。
// install行が無ければfoundはfalse。
func pinnedClaudeCode(dockerfile []byte) (arg string, found bool) {
	m := claudeInstallRe.FindSubmatch(dockerfile)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

// claudeCodeNote は、Dockerfileが入れるClaude Codeの版がこのmasudaの検証した版と違うときに
// 知らせる1行を返す（同じなら""）。install行が無いものは、利用者が別の方法で入れているとみなして
// 口を出さない。止めはしない: 版を上げて試すのは利用者の自由で、知らせるだけにする。
func claudeCodeNote(rel string, dockerfile []byte) string {
	arg, found := pinnedClaudeCode(dockerfile)
	switch {
	case !found || arg == guest.ClaudeCodeVersion:
		return ""
	case arg == "" || arg == "stable" || arg == "latest":
		return fmt.Sprintf("note: %s installs Claude Code without a pinned version; masuda %s was verified with %s (pin it: install.sh | bash -s -- %s)",
			rel, version, guest.ClaudeCodeVersion, guest.ClaudeCodeVersion)
	default:
		return fmt.Sprintf("note: %s installs Claude Code %s; masuda %s was verified with %s",
			rel, arg, version, guest.ClaudeCodeVersion)
	}
}

// imageClaudeCodeNote はリポジトリrootのイメージentry（""ならsettings.jsonのimage）について
// claudeCodeNoteを返す。読めないときは""（ビルドそのものはserveが判断して失敗を返す）。
func imageClaudeCodeNote(root, entry string) string {
	if entry == "" {
		cfg, err := config.Load(root)
		if err != nil {
			return ""
		}
		entry = cfg.ImageEntry()
	}
	rel := filepath.Join(config.DirName, "images", entry, "Dockerfile")
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return ""
	}
	return claudeCodeNote(rel, data)
}
