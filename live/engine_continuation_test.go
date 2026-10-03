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
	// firstとsecondの間でVMを作り直すために、tokenの承認ゲートで止める。
	".masuda/workflows/continuation-resume.yaml": `version: 1
inputs: [instructions]
start: first
nodes:
  first: {type: agent, role: agents/rememberer, next: hold}
  hold: {type: approval, gate: pause, target: token, next: {approved: second, rejected: end}}
  second: {type: agent, role: agents/recaller, continues: agents/rememberer, next: end}
`,
}

// startEngineContinuation はリポジトリとserveを用意してworkflowを始め、APIとワークスペースのIDと
// ディレクトリを返す。VMは後始末で止める。
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
		t.Skip("no Claude API token (MASUDA_LIVE_CLAUDE_TOKEN or <data dir>/claude-oauth-token)")
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
	// Cleanupは後に登録したものから動くので、srv.Stopより先にVMを止める。
	t.Cleanup(func() {
		_, _ = api.ws.Stop(context.Background(), connect.NewRequest(&apiv1.StopRequest{Id: id}))
	})
	return ctx, api, id, filepath.Join(dataDir, "workspaces", id)
}

// TestEngineContinuationKeepsMemory は、`continues`付きのノードのタスクが、続けられた役の
// サブエージェントにSendMessageで渡り、前の出現の記憶が残ることを確かめる。recallerは入力を
// 持たないので、tokenと同じ文字列を書けるのは記憶を持ったサブエージェントだけ。
func TestEngineContinuationKeepsMemory(t *testing.T) {
	ctx, api, id, wsDir := startEngineContinuation(t, "workflows/continuation")
	start := time.Now()
	final, err := driveLap(ctx, t, api, id)
	logEngineContinuation(t, wsDir)
	if err != nil {
		t.Fatal(err)
	}
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
// （firstの後のゲートで止めて再開し、VMを作り直した）とき、メインセッションが新しいサブエージェントを
// 起動し、入力だけでsecondを終えることを確かめる。新しいサブエージェントは文字列を知らないので
// `unknown`と書くはず。
func TestEngineContinuationFallsBackAfterResume(t *testing.T) {
	ctx, api, id, wsDir := startEngineContinuation(t, "workflows/continuation-resume")
	start := time.Now()
	if _, err := waitWorkspace(ctx, t, api, id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE); err != nil {
		logEngineContinuation(t, wsDir)
		t.Fatal(err)
	}
	t.Logf("gate opened after %v; stopping and resuming", time.Since(start).Round(time.Second))
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
	final, err := driveLap(ctx, t, api, id)
	logEngineContinuation(t, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("finished in %v: outcome %q", time.Since(start).Round(time.Second), final.Outcome)
	token, recall := strings.TrimSpace(findFile(filepath.Join(wsDir, "data"), "token")), strings.TrimSpace(findFile(filepath.Join(wsDir, "data"), "recall"))
	t.Logf("token %q, recall %q", token, recall)
	if outcome := nodeFinish(t, wsDir, "second"); outcome != "done" {
		t.Fatalf("second must finish done after the fallback, got %q", outcome)
	}
	if recall != "unknown" {
		t.Fatalf("a fresh subagent cannot know the token; recall %q (token %q)", recall, token)
	}
}

// waitWorkspace はワークスペースがwantになるまで待つ。BLOCKED・DONE・DEADや、wantより先に
// 終わったら失敗として返す。
func waitWorkspace(ctx context.Context, t *testing.T, api clients, id string, want apiv1.WorkspaceState) (*apiv1.Workspace, error) {
	lastLine := ""
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
		case apiv1.WorkspaceState_WORKSPACE_STATE_DONE, apiv1.WorkspaceState_WORKSPACE_STATE_BLOCKED:
			return nil, fmt.Errorf("workspace %s is %s before %s: %s", id, w.State, want, w.Reason)
		}
		if w.GetActivity().GetKind() == apiv1.ActivityKind_ACTIVITY_KIND_DEAD {
			return nil, fmt.Errorf("workspace %s: the guest's claude is gone: %s", id, w.GetActivity().GetDetail())
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
type toolUse struct{ Tool, Path string }

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
					FilePath string `json:"file_path"`
				} `json:"tool_input"`
			} `json:"input"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Input.Event == "PostToolUse" {
			out = append(out, toolUse{line.Input.Tool, line.Input.ToolInput.FilePath})
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
