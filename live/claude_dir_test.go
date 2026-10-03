package live

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"
	"github.com/TadahiroYamamura/masuda-engine/engine"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/serve"
)

// claudeDirBudget はTestClaudeDirReachesSubagentにかけてよい時間。VMの起動込み。
const claudeDirBudget = 9 * time.Minute

// 印はゲストに置く`.masuda/claude/`・`.masuda/claude.local/`のそれぞれにしか書かず、役の本文にも
// 入力にも出さない。出力に印があれば、その置き場所がサブエージェントに読まれたことになる。
const (
	ruleMark  = "RULE-MARK-7f3a"
	skillMark = "SKILL-MARK-9c1d"
	localMark = "LOCAL-MARK-2b8e"
)

// claudeDirRepoFiles はコミットするもの。`.masuda/claude.local/`はgitignoreして作業ツリーにだけ置く
// （claudeDirLocalFiles）。settings.jsonとDockerfileはpythonRepoFilesと同じもの（イメージのキャッシュを使う）。
var claudeDirRepoFiles = map[string]string{
	".gitignore":                        ".masuda/settings.local.json\n.masuda/claude.local/\n",
	".masuda/settings.json":             pythonRepoFiles[".masuda/settings.json"],
	".masuda/images/default/Dockerfile": pythonRepoFiles[".masuda/images/default/Dockerfile"],
	".masuda/claude/rules/marker.md":    "出力を書くときは本文の先頭に`" + ruleMark + "`を必ず含める。\n",
	".masuda/claude/skills/echo-mark/SKILL.md": `---
name: echo-mark
description: 確認用の印の文字列を返す
---
` + "`" + skillMark + "`" + `という文字列を返す。
`,
	".masuda/agents/prober.md": `---
name: prober
description: スキルとルールが届いているかを確かめる
tools: Read, Skill
outputs: [probe]
outcomes:
  done: 書いた
---
スキルecho-markを呼び、その結果と、ルールに従った印を出力` + "`probe`" + `に書く。
`,
	".masuda/workflows/claude-dir.yaml": `version: 1
inputs: [instructions]
start: probe
nodes:
  probe: {type: agent, role: agents/prober, next: end}
`,
}

var claudeDirLocalFiles = map[string]string{
	".masuda/claude.local/CLAUDE.md": "出力の末尾に`" + localMark + "`を含める。\n",
}

// TestClaudeDirReachesSubagent は、`.masuda/claude/`のルールとスキル、`.masuda/claude.local/`の
// CLAUDE.mdが、ゲストのサブエージェントまで届くかを確かめる。出力`probe`に3つの印があり、
// フックの記録にSkillの使用があれば合格。
func TestClaudeDirReachesSubagent(t *testing.T) {
	if os.Getenv("MASUDA_LIVE_TEST") != "1" {
		t.Skip("set MASUDA_LIVE_TEST=1 to run against the real sandbox service")
	}
	sbSock := sandboxSocket()
	if st, err := os.Stat(sbSock); err != nil || st.Mode()&os.ModeSocket == 0 {
		t.Skipf("the sandbox service is not listening on %s", sbSock)
	}
	token := claudeToken()
	if token == "" {
		t.Skip("no Claude API token (MASUDA_LIVE_CLAUDE_TOKEN or <data dir>/claude-oauth-token)")
	}
	if dl, ok := t.Deadline(); ok && time.Until(dl) < claudeDirBudget {
		t.Fatalf("the test needs up to %v; run with -timeout 20m (the test deadline is in %v)", claudeDirBudget, time.Until(dl).Round(time.Second))
	}

	repo := keepOnFailure(t, "masuda-live-claudedir-repo-")
	run(t, repo, "git", "init", "-q", "-b", "main")
	writeFiles(t, repo, claudeDirRepoFiles)
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "commit", "-qm", "claude dir check")
	writeFiles(t, repo, claudeDirLocalFiles)

	dataDir := keepOnFailure(t, "masuda-live-claudedir-data-")
	sock := filepath.Join(dataDir, "masuda.sock")
	ctx, cancel := context.WithTimeout(context.Background(), claudeDirBudget)
	t.Cleanup(cancel)
	srv, err := serve.Start(ctx, serve.Options{Socket: sock, DataDir: dataDir, SandboxSocket: sbSock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	api := connectAPI(sock)
	if _, err := api.config.ApproveEgress(ctx, connect.NewRequest(&apiv1.HostRequest{RepoRoot: repo, Host: "api.anthropic.com"})); err != nil {
		t.Fatal(err)
	}
	if _, err := api.config.SetSecret(ctx, connect.NewRequest(&apiv1.SetSecretRequest{RepoRoot: repo, Name: "CLAUDE_CODE_OAUTH_TOKEN", Value: token})); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := api.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{
		RepoRoot: repo,
		Workflow: "workflows/claude-dir",
		Branch:   "live/claude-dir",
		Inputs:   map[string][]byte{"instructions": []byte("（使わない）")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	t.Logf("workspace %s (data %s)", id, dataDir)
	destroyVMOnCleanup(t, api, sbSock, id)
	wsDir := filepath.Join(dataDir, "workspaces", id)

	final, err := waitWorkspace(ctx, t, api, id, apiv1.WorkspaceState_WORKSPACE_STATE_DONE)
	logEngineContinuation(t, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	assertCleanedUpAtDone(t, sbSock, id, wsDir)
	probe := findFile(filepath.Join(wsDir, "data"), "probe")
	t.Logf("finished in %v: outcome %q; probe:\n%s", time.Since(start).Round(time.Second), final.Outcome, probe)
	for name, mark := range map[string]string{"rules/marker.md": ruleMark, "skills/echo-mark": skillMark, "claude.local/CLAUDE.md": localMark} {
		if !strings.Contains(probe, mark) {
			t.Errorf("probe lacks %s from %s", mark, name)
		}
	}
	if countTool(toolUses(t, wsDir), "Skill") == 0 {
		t.Errorf("no Skill use in the hook records")
	}
}

// claude-dirのワークフローは、engineの読み込み時の検査を通る（VMを使わないので常に走る）。
func TestClaudeDirDefinitionsCheck(t *testing.T) {
	repo := fstest.MapFS{}
	for p, c := range claudeDirRepoFiles {
		if rel, ok := strings.CutPrefix(p, ".masuda/"); ok {
			repo[rel] = &fstest.MapFile{Data: []byte(c)}
		}
	}
	set, err := engine.Load(repo, engine.Bundled())
	if err != nil {
		t.Fatal(err)
	}
	if problems := set.Check("workflows/claude-dir"); len(problems) > 0 {
		t.Errorf("workflows/claude-dir: %v", problems)
	}
}
