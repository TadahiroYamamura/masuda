package live

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
const claudeDirBudget = 12 * time.Minute

// 印はゲストに置く`.masuda/claude/`・`.masuda/claude.local/`のそれぞれにしか書かず、役の本文にも
// 入力にも出さない。出力に印があれば、その置き場所がサブエージェントに読まれたことになる。
const (
	ruleMark  = "RULE-MARK-7f3a"
	skillMark = "SKILL-MARK-9c1d"
	localMark = "LOCAL-MARK-2b8e"
)

// claudeDirRepoFiles はコミットするもの。`.masuda/claude.local/`はgitignoreして作業ツリーにだけ置く
// （claudeDirLocalFiles）。DockerfileはpythonRepoFilesと同じもの（イメージのキャッシュを使う）。
// settings.jsonはpythonRepoFilesのものにclaudeSettings.modelを足し、役のmodelの有無で
// サブエージェントのモデルが分かれることを見る（proberはmodel・effortを書き、echoerは書かない）。
// claudeSettings.modelには、modelを書かない役が継承したことを区別できるよう、
// proberのmodelと違うものを選ぶ。
var claudeDirRepoFiles = map[string]string{
	".gitignore": ".masuda/settings.local.json\n.masuda/claude.local/\n",
	".masuda/settings.json": `{
  "image": "default",
  "egress": ["api.anthropic.com"],
  "claudeSettings": {"model": "opus"}
}
`,
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
model: sonnet
effort: low
outputs: [probe]
outcomes:
  done: 書いた
---
スキルecho-markを呼び、その結果と、ルールに従った印を出力` + "`probe`" + `に書く。
`,
	".masuda/agents/echoer.md": `---
name: echoer
description: 入力をそのまま写す
tools: Read
inputs: [probe]
outputs: [echo]
outcomes:
  done: 書いた
---
入力` + "`probe`" + `を読み、その中身をそのまま出力` + "`echo`" + `に書く。
`,
	".masuda/workflows/claude-dir.yaml": `version: 1
inputs: [instructions]
start: probe
nodes:
  probe: {type: agent, role: agents/prober, next: echo}
  echo: {type: agent, role: agents/echoer, next: end}
`,
}

var claudeDirLocalFiles = map[string]string{
	".masuda/claude.local/CLAUDE.md": "出力の末尾に`" + localMark + "`を含める。\n",
}

// TestClaudeDirReachesSubagent は、`.masuda/claude/`のルールとスキル、`.masuda/claude.local/`の
// CLAUDE.mdが、ゲストのサブエージェントまで届くかを確かめる。出力`probe`に3つの印があり、
// フックの記録にSkillの使用があれば合格。あわせて、役定義のmodel・effortと
// claudeSettings.modelがサブエージェントの会話ログに記録されたとおりに効いたかを見る。
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

	usage := subagentModels(t, filepath.Join(wsDir, "exports", "transcripts"))
	for role, u := range usage {
		t.Logf("subagent %s: model %v effort %v", role, u.models, u.efforts)
	}
	for _, p := range checkSubagentModel(usage, "prober", "claude-sonnet", "low") {
		t.Error(p)
	}
	for _, p := range checkSubagentModel(usage, "echoer", "claude-opus", "") {
		t.Error(p)
	}
}

// modelUsage は1つの役のサブエージェントの応答に記録されたmodel・effortの値と出現回数。
type modelUsage struct{ models, efforts map[string]int }

// subagentModels は書き出した会話ログのうちサブエージェントのもの（`subagents/*.jsonl`）から、
// 応答（type: assistant）の`message.model`と`effort`を役ごとに集める。役は応答の`attributionAgent`
// （サブエージェント定義のname）で見分ける。ゲストのClaude Code 2.1.287の会話ログには
// agentTypeが無く、records/subagents.jsonはDONEに至った最後のタスクのIDを持たないため。
func subagentModels(t *testing.T, transcripts string) map[string]modelUsage {
	t.Helper()
	out := map[string]modelUsage{}
	_ = filepath.WalkDir(transcripts, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(filepath.Dir(p)) != "subagents" || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			t.Errorf("reading %s: %v", p, err)
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 64<<20)
		for sc.Scan() {
			var line struct {
				Type             string `json:"type"`
				AttributionAgent string `json:"attributionAgent"`
				Effort           string `json:"effort"`
				Message          struct {
					Model string `json:"model"`
				} `json:"message"`
			}
			if json.Unmarshal(sc.Bytes(), &line) != nil || line.Type != "assistant" {
				continue
			}
			u, ok := out[line.AttributionAgent]
			if !ok {
				u = modelUsage{models: map[string]int{}, efforts: map[string]int{}}
				out[line.AttributionAgent] = u
			}
			u.models[line.Message.Model]++
			u.efforts[line.Effort]++
		}
		if err := sc.Err(); err != nil {
			t.Errorf("reading %s: %v", p, err)
		}
		return nil
	})
	return out
}

// checkSubagentModel は役roleの応答がすべてmodelPrefixで始まるモデルで、effortが空でなければ
// すべてそのeffortで記録されているかを確かめ、外れたものを返す。
func checkSubagentModel(usage map[string]modelUsage, role, modelPrefix, effort string) []string {
	u, ok := usage[role]
	if !ok {
		return []string{"no assistant turn of subagent " + role + " in the transcripts"}
	}
	var problems []string
	for m := range u.models {
		if !strings.HasPrefix(m, modelPrefix) {
			problems = append(problems, role+": model "+strconv.Quote(m)+", want "+modelPrefix+"*")
		}
	}
	if effort != "" {
		for e := range u.efforts {
			if e != effort {
				problems = append(problems, role+": effort "+strconv.Quote(e)+", want "+effort)
			}
		}
	}
	return problems
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
