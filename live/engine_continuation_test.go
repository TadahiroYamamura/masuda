package live

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"
	"github.com/TadahiroYamamura/masuda-engine/engine"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/serve"
)

// engineContinuationBudget はTestEngineContinuation*の1本にかけてよい時間。VMの起動（再開での作り直しを含む）込み。
// 2本を続けて-timeout 20mで回せるよう、10分未満にする。
const engineContinuationBudget = 9 * time.Minute

// engineContinuationRepoFiles は、ワークフローの`continues`がゲストのサブエージェントまで届くかを
// 確かめる使い捨てのリポジトリ。両方の役を`tools: Read`（とmasudaが足すMCPツール）にするのは、
// タスクファイルを読む道具だけを持たせるため。`tools: Write`にするとengineが「書き込める役」と
// みなし、承認済みの計画を求めて拒否する。Readではゲストに残る前の出現の出力
// （`/masuda/out/<occ>/token`）を読んで当てられてしまうので、テストはそれをフックの記録で検査する。
// recallerにinputsが無いのも同じ理由で、記憶だけで答えさせる。settings.jsonとDockerfileはpythonRepoFilesと同じもの（イメージのキャッシュを使う）。
var engineContinuationRepoFiles = map[string]string{
	".gitignore":                        ".masuda/settings.local.json\n",
	".masuda/settings.json":             pythonRepoFiles[".masuda/settings.json"],
	".masuda/images/default/Dockerfile": pythonRepoFiles[".masuda/images/default/Dockerfile"],
	".masuda/agents/rememberer.md": `---
name: rememberer
description: 無作為な文字列を作って覚えておく
tools: Read
outputs: [token]
outcomes:
  done: 文字列を書いた
---
12文字の無作為な英数字の文字列を1つ作り、出力` + "`token`" + `にその文字列だけを書く。他には書かない。後のタスクでその文字列をもう一度書くことがあるので覚えておく。
`,
	".masuda/agents/recaller.md": `---
name: recaller
description: 前のタスクで書いた文字列をもう一度書く
tools: Read
outputs: [recall]
outcomes:
  done: 書いた
---
あなたが前のタスクで出力` + "`token`" + `に書いた文字列を、出力` + "`recall`" + `にそのまま書く。このやり取りの中にその文字列が無ければ（前のタスクを担当していなければ）` + "`unknown`" + `と書く。ファイルは読まない。
`,
	".masuda/workflows/continuation.yaml": `version: 1
inputs: [instructions]
start: first
nodes:
  first: {type: agent, role: agents/rememberer, next: second}
  second: {type: agent, role: agents/recaller, continues: agents/rememberer, next: end}
`,
	// firstとsecondの間でVMを作り直すために、tokenの承認ゲートで止める。secondは本番の続きの
	// タスクと同じく、記憶が無くても成り立つ入力（token）を受け取る。記憶の無いサブエージェントに
	// 「前に書いた文字列」を思い出させる課題は、APIの安全分類器（reasoning_extraction）に止められる
	// ため（2026-10-03の実機で3回とも）。
	".masuda/agents/copier.md": `---
name: copier
description: 入力の文字列を出力へ写す
tools: Read
inputs: [token]
outputs: [recall]
outcomes:
  done: 書いた
---
入力` + "`token`" + `に書かれた文字列を、出力` + "`recall`" + `にそのまま書く。他のファイルは読まない。
`,
	".masuda/workflows/continuation-resume.yaml": `version: 1
inputs: [instructions]
start: first
nodes:
  first: {type: agent, role: agents/rememberer, next: hold}
  hold: {type: approval, gate: pause, target: token, next: {approved: second, rejected: end}}
  second: {type: agent, role: agents/copier, continues: agents/rememberer, next: end}
`,
}

// startEngineContinuation はリポジトリとserveを用意してworkflowを始め、APIとワークスペースのIDと
// ディレクトリを返す。VMは後始末で壊す。
func startEngineContinuation(t *testing.T, workflow string) (context.Context, clients, string, string) {
	t.Helper()
	if os.Getenv("MASUDA_LIVE_TEST") != "1" {
		t.Skip("set MASUDA_LIVE_TEST=1 to run against the real sandbox service")
	}
	sbSock := sandboxSocket()
	if st, err := os.Stat(sbSock); err != nil || st.Mode()&os.ModeSocket == 0 {
		t.Skipf("the sandbox service is not listening on %s", sbSock)
	}
	token := claudeToken()
	if token == "" {
		t.Skip("no Claude API token (MASUDA_LIVE_CLAUDE_TOKEN, or register it with masuda secret set CLAUDE_CODE_OAUTH_TOKEN)")
	}
	if dl, ok := t.Deadline(); ok && time.Until(dl) < engineContinuationBudget {
		t.Fatalf("the test needs up to %v; run with -timeout 20m (the test deadline is in %v)", engineContinuationBudget, time.Until(dl).Round(time.Second))
	}

	repo := keepOnFailure(t, "masuda-live-econt-repo-")
	run(t, repo, "git", "init", "-q", "-b", "main")
	writeFiles(t, repo, engineContinuationRepoFiles)
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "commit", "-qm", "engine continuation check")

	dataDir := keepOnFailure(t, "masuda-live-econt-data-")
	sock := filepath.Join(dataDir, "masuda.sock")
	ctx, cancel := context.WithTimeout(context.Background(), engineContinuationBudget)
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
	res, err := api.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{
		RepoRoot: repo,
		Workflow: workflow,
		Branch:   "live/econt",
		Inputs:   map[string][]byte{"instructions": []byte("（使わない）")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	t.Logf("workspace %s (data %s)", id, dataDir)
	destroyVMOnCleanup(t, api, sbSock, id)
	return ctx, api, id, filepath.Join(dataDir, "workspaces", id)
}

// TestEngineContinuationKeepsMemory は、`continues`付きのノードのタスクが、続けられた役の
// サブエージェントにSendMessageで渡り、前の出現の記憶が残ることを確かめる。recallerは入力を
// 持たないので、tokenと同じ文字列を書けるのは記憶を持ったサブエージェントだけ。
func TestEngineContinuationKeepsMemory(t *testing.T) {
	ctx, api, id, wsDir := startEngineContinuation(t, "workflows/continuation")
	start := time.Now()
	final, err := waitWorkspace(ctx, t, api, id, apiv1.WorkspaceState_WORKSPACE_STATE_DONE)
	logEngineContinuation(t, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	assertCleanedUpAtDone(t, sandboxSocket(), id, wsDir)
	t.Logf("finished in %v: outcome %q", time.Since(start).Round(time.Second), final.Outcome)
	token, recall := strings.TrimSpace(findFile(filepath.Join(wsDir, "data"), "token")), strings.TrimSpace(findFile(filepath.Join(wsDir, "data"), "recall"))
	t.Logf("token %q, recall %q", token, recall)
	uses := toolUses(t, wsDir)
	for _, u := range uses {
		if u.Tool == "Read" && strings.Contains(u.Path, "/masuda/out/") {
			t.Fatalf("a subagent read an output file (%s); the recall does not show memory", u.Path)
		}
	}
	if countTool(uses, "SendMessage") == 0 {
		t.Fatalf("the main session did not use SendMessage; the task was not continued")
	}
	if token == "" || token != recall {
		t.Fatalf("the continued subagent did not recall its token (token %q, recall %q)", token, recall)
	}
}

// TestEngineContinuationFallsBackAfterResume は、続けられる側のサブエージェントがいなくなった
// （firstの後のゲートで止めて再開し、VMを作り直した）とき、メインセッションがSendMessageではなく
// 新しいサブエージェントを起動し、続きのタスクの入力だけでsecondを終えることを確かめる。
// 新しいサブエージェントで進んだ証拠は、再開で消えたIDの記録にsecondの出現が前と違うIDで
// 載ることと、フックの記録でSendMessageが使われずAgentでcopierが起動されたこと。
func TestEngineContinuationFallsBackAfterResume(t *testing.T) {
	ctx, api, id, wsDir := startEngineContinuation(t, "workflows/continuation-resume")
	start := time.Now()
	if _, err := waitWorkspace(ctx, t, api, id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE); err != nil {
		logEngineContinuation(t, wsDir)
		t.Fatal(err)
	}
	// 前のVMのメインセッションは、ゲートで止まるnext_taskにfirstのサブエージェントのIDを渡している。
	before := waitSubagentIDs(ctx, t, wsDir, func(m map[string]string) bool { return m["0000001"] != "" })
	t.Logf("gate opened after %v; ids before resume %v; stopping and resuming", time.Since(start).Round(time.Second), before)
	if _, err := api.ws.Stop(ctx, connect.NewRequest(&apiv1.StopRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if _, err := api.ws.Resume(ctx, connect.NewRequest(&apiv1.ResumeRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if _, err := waitWorkspace(ctx, t, api, id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE); err != nil {
		logEngineContinuation(t, wsDir)
		t.Fatal(err)
	}
	if ids, err := os.ReadFile(filepath.Join(wsDir, "records", "subagents.json")); err == nil {
		t.Fatalf("resume must clear the subagent ids, got %s", ids)
	}
	open, err := api.gates.ListOpen(ctx, connect.NewRequest(&apiv1.ListOpenGatesRequest{WorkspaceId: id}))
	if err != nil || len(open.Msg.Gates) != 1 || open.Msg.Gates[0].Gate != "pause" {
		t.Fatalf("want the pause gate after resume: %v %+v", err, open)
	}
	g := open.Msg.Gates[0]
	if _, err := api.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{
		WorkspaceId: id, Occurrence: g.Occurrence,
		Decision: &apiv1.Decision{Outcome: "approved", TargetHash: g.TargetHash},
	})); err != nil {
		t.Fatal(err)
	}
	final, err := waitWorkspace(ctx, t, api, id, apiv1.WorkspaceState_WORKSPACE_STATE_DONE)
	if err != nil {
		logEngineContinuation(t, wsDir)
		t.Fatal(err)
	}
	t.Logf("finished in %v: outcome %q", time.Since(start).Round(time.Second), final.Outcome)
	// secondのIDは、メインセッションがDONEの後に呼ぶ最後のnext_taskで報告される。
	after := waitSubagentIDs(ctx, t, wsDir, func(m map[string]string) bool { return len(m) > 0 })
	logEngineContinuation(t, wsDir)
	token, recall := strings.TrimSpace(findFile(filepath.Join(wsDir, "data"), "token")), strings.TrimSpace(findFile(filepath.Join(wsDir, "data"), "recall"))
	t.Logf("token %q, recall %q; ids after resume %v", token, recall, after)
	if outcome := nodeFinish(t, wsDir, "second"); outcome != "done" {
		t.Fatalf("second must finish done after the fallback, got %q", outcome)
	}
	if token == "" || recall != token {
		t.Fatalf("second must copy its input token (token %q, recall %q)", token, recall)
	}
	if _, ok := after["0000001"]; ok || len(after) != 1 {
		t.Fatalf("after resume only the second occurrence may be recorded: %v", after)
	}
	for occ, aid := range after {
		if aid == "" || aid == before["0000001"] {
			t.Fatalf("second (occ %s) must be a new subagent, got id %q (first was %q)", occ, aid, before["0000001"])
		}
	}
	uses := toolUses(t, wsDir)
	if n := countTool(uses, "SendMessage"); n != 0 {
		t.Fatalf("the main session used SendMessage %d times; it must start a new subagent after resume", n)
	}
	copier := false
	for _, u := range uses {
		copier = copier || (u.Tool == "Agent" && u.SubagentType == "copier")
	}
	if !copier {
		t.Fatalf("the main session did not start a copier with the Agent tool")
	}
}

// waitSubagentIDs はIDの記録がokを満たすまで（最大1分）待って返す。満たさなければ最後に読めたものを返す。
func waitSubagentIDs(ctx context.Context, t *testing.T, wsDir string, ok func(map[string]string) bool) map[string]string {
	t.Helper()
	m := map[string]string{}
	deadline := time.Now().Add(time.Minute)
	for {
		if b, err := os.ReadFile(filepath.Join(wsDir, "records", "subagents.json")); err == nil {
			m = map[string]string{}
			_ = json.Unmarshal(b, &m)
		}
		if ok(m) || time.Now().After(deadline) || ctx.Err() != nil {
			return m
		}
		time.Sleep(2 * time.Second)
	}
}

// stuckAfter は、メインセッションの入力待ち（WAITING_INPUT）がこれだけ続いたら止まったとみなす長さ。
// メインセッションが会話で問いかけたり、APIの安全分類器に応答を止められたりすると、誰も答えずに
// 上限まで待つことになるため。
const stuckAfter = 60 * time.Second

// waitWorkspace はワークスペースがwantになるまで待つ。wantより先にDONE・BLOCKED・STOPPEDや別の待ち
// （ゲート・質問）になった、ゲストのclaudeがいなくなった、入力待ちがstuckAfter続いた、のどれかなら
// 失敗として返す。
func waitWorkspace(ctx context.Context, t *testing.T, api clients, id string, want apiv1.WorkspaceState) (*apiv1.Workspace, error) {
	lastLine := ""
	var waitingSince time.Time
	for {
		got, err := api.ws.Get(ctx, connect.NewRequest(&apiv1.GetWorkspaceRequest{Id: id}))
		if err != nil {
			return nil, err
		}
		w := got.Msg
		line := fmt.Sprintf("%s %s %s", w.State, w.GetActivity().GetKind(), w.Position)
		if line != lastLine {
			t.Logf("%s %s", time.Now().Format("15:04:05"), line)
			lastLine = line
		}
		if w.State == want {
			return w, nil
		}
		switch w.State {
		case apiv1.WorkspaceState_WORKSPACE_STATE_DONE, apiv1.WorkspaceState_WORKSPACE_STATE_BLOCKED,
			apiv1.WorkspaceState_WORKSPACE_STATE_STOPPED, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE,
			apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_QUESTION:
			return nil, fmt.Errorf("workspace %s is %s while waiting for %s: %s", id, w.State, want, w.Reason)
		}
		switch w.GetActivity().GetKind() {
		case apiv1.ActivityKind_ACTIVITY_KIND_DEAD:
			return nil, fmt.Errorf("workspace %s: the guest's claude is gone: %s", id, w.GetActivity().GetDetail())
		case apiv1.ActivityKind_ACTIVITY_KIND_WAITING_INPUT:
			if waitingSince.IsZero() {
				waitingSince = time.Now()
			} else if time.Since(waitingSince) >= stuckAfter {
				return nil, fmt.Errorf("workspace %s: the main session has been waiting for input for %v (%s)", id, stuckAfter, w.GetActivity().GetDetail())
			}
		default:
			waitingSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("workspace %s did not reach %s (last: %s)", id, want, lastLine)
		case <-time.After(3 * time.Second):
		}
	}
}

// nodeFinish は実行ログからnodeの最後のfinish行のoutcomeを返す（無ければ空）。
func nodeFinish(t *testing.T, wsDir, node string) string {
	t.Helper()
	f, err := os.Open(filepath.Join(wsDir, "records", "execution-log.jsonl"))
	if err != nil {
		t.Logf("execution log: %v", err)
		return ""
	}
	defer f.Close()
	outcome := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	for sc.Scan() {
		var ev struct{ Kind, Node, Outcome string }
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Kind == "finish" && ev.Node == node {
			outcome = ev.Outcome
		}
	}
	return outcome
}

// logEngineContinuation は調べるときに要るもの（サブエージェントのIDの記録、メインセッションが
// 使ったツールの回数）をテストログに出す。
func logEngineContinuation(t *testing.T, wsDir string) {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join(wsDir, "records", "subagents.json")); err == nil {
		t.Logf("records/subagents.json: %s", strings.TrimSpace(string(b)))
	} else {
		t.Logf("records/subagents.json: %v", err)
	}
	tools := map[string]int{}
	for _, u := range toolUses(t, wsDir) {
		tools[u.Tool]++
	}
	names := make([]string, 0, len(tools))
	for n := range tools {
		names = append(names, fmt.Sprintf("%s=%d", n, tools[n]))
	}
	sort.Strings(names)
	t.Logf("tools used (PostToolUse): %s", strings.Join(names, ", "))
}

// toolUse はフックの記録（PostToolUse）から読んだツールの使用1回。
type toolUse struct{ Tool, Path, SubagentType string }

// toolUses はワークスペースのフックの記録からPostToolUseを順に読む。サブエージェントの使用も含む。
func toolUses(t *testing.T, wsDir string) []toolUse {
	t.Helper()
	f, err := os.Open(filepath.Join(wsDir, "records", "hooks.jsonl"))
	if err != nil {
		t.Logf("hooks: %v", err)
		return nil
	}
	defer f.Close()
	var out []toolUse
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var line struct {
			Input struct {
				Event     string `json:"hook_event_name"`
				Tool      string `json:"tool_name"`
				ToolInput struct {
					FilePath     string `json:"file_path"`
					SubagentType string `json:"subagent_type"`
				} `json:"tool_input"`
			} `json:"input"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Input.Event == "PostToolUse" {
			out = append(out, toolUse{line.Input.Tool, line.Input.ToolInput.FilePath, line.Input.ToolInput.SubagentType})
		}
	}
	return out
}

func countTool(uses []toolUse, tool string) int {
	n := 0
	for _, u := range uses {
		if u.Tool == tool {
			n++
		}
	}
	return n
}

// liveの続きのワークフローは、engineの読み込み時の検査（toolsの部分集合等）を通る（VMを使わないので常に走る）。
func TestEngineContinuationDefinitionsCheck(t *testing.T) {
	repo := fstest.MapFS{}
	for p, c := range engineContinuationRepoFiles {
		if rel, ok := strings.CutPrefix(p, ".masuda/"); ok {
			repo[rel] = &fstest.MapFile{Data: []byte(c)}
		}
	}
	set, err := engine.Load(repo, engine.Bundled())
	if err != nil {
		t.Fatal(err)
	}
	for _, wf := range []string{"workflows/continuation", "workflows/continuation-resume"} {
		if problems := set.Check(wf); len(problems) > 0 {
			t.Errorf("%s: %v", wf, problems)
		}
	}
}
