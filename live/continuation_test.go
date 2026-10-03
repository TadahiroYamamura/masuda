package live

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/serve"
)

// continuationBudget はTestGuestSubagentContinuationにかけてよい時間。VMの起動込みで2〜4分を見込む。
const continuationBudget = 10 * time.Minute

// TestGuestSubagentContinuation は、ゲストのClaude Codeで、メインセッションがAgentツールで起動した
// サブエージェントに、別の作業を挟んでターンをまたいだあとSendMessageで続きを送り、サブエージェントが
// 前の文脈を覚えているかを確かめる。「実装したエージェントがレビュー指摘の修正も同じ記憶のまま担当する」
// 設計の前提で、Claude Codeの版で継続の仕組みが変わればここが落ちる。ゲストの版を上げたら回す。
//
// 検査はexecノード1つのワークフロー（scripts/continuation.sh）がゲストで行い、メインセッション
// （claude-work）とは別のtmuxセッションでclaudeを動かす。masudaの機能は増やさずに、今の公開APIと
// ゲストの配置のまま確かめるため。
func TestGuestSubagentContinuation(t *testing.T) {
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
	if dl, ok := t.Deadline(); ok && time.Until(dl) < continuationBudget {
		t.Fatalf("the test needs up to %v; run with -timeout 20m (the test deadline is in %v)", continuationBudget, time.Until(dl).Round(time.Second))
	}

	repo := keepOnFailure(t, "masuda-live-cont-repo-")
	run(t, repo, "git", "init", "-q", "-b", "main")
	writeFiles(t, repo, continuationRepoFiles)
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "commit", "-qm", "continuation check")

	dataDir := keepOnFailure(t, "masuda-live-cont-data-")
	sock := filepath.Join(dataDir, "masuda.sock")
	ctx, cancel := context.WithTimeout(context.Background(), continuationBudget)
	defer cancel()
	srv, err := serve.Start(ctx, serve.Options{Socket: sock, DataDir: dataDir, SandboxSocket: sbSock})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
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
		Workflow: "workflows/continuation",
		Branch:   "live/continuation",
		Inputs:   map[string][]byte{"instructions": []byte("（使わない）")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	t.Logf("workspace %s (data %s)", id, dataDir)
	defer func() {
		_, _ = api.ws.Stop(context.Background(), connect.NewRequest(&apiv1.StopRequest{Id: id}))
	}()

	final, err := driveLap(ctx, t, api, id)
	wsDir := filepath.Join(dataDir, "workspaces", id)
	if report := findFile(filepath.Join(wsDir, "data"), "continuation-report"); report != "" {
		t.Logf("continuation-report:\n%s", report)
	}
	if err != nil {
		logExecFinish(t, wsDir)
		t.Fatal(err)
	}
	t.Logf("finished in %v: outcome %q", time.Since(start).Round(time.Second), final.Outcome)
	if final.Outcome != "done" {
		detail := logExecFinish(t, wsDir)
		if strings.Contains(detail, "（exit 2）") {
			t.Fatalf("the exec could not get the Claude token in the guest (environment problem, not the answer of this test)")
		}
		t.Fatalf("the subagent did not keep its context across SendMessage (outcome %q); see the log above", final.Outcome)
	}
}

// logExecFinish はワークスペースの実行ログからexecノードのfinish行を探してテストログに出し、
// そのdetail（失敗ならコマンドの出力の末尾）を返す。
func logExecFinish(t *testing.T, wsDir string) string {
	t.Helper()
	for _, p := range []string{
		filepath.Join(wsDir, "records", "execution-log.jsonl"),
		filepath.Join(wsDir, "exports", "execution-log.jsonl"),
	} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 4<<20)
		for sc.Scan() {
			var ev struct{ Kind, Node, Outcome, Detail string }
			if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Kind != "finish" || ev.Node != "continuation" {
				continue
			}
			t.Logf("exec finished (%s, outcome %q):\n%s", p, ev.Outcome, ev.Detail)
			return ev.Detail
		}
	}
	t.Logf("no finish line for the exec node in %s", wsDir)
	return ""
}

// findFile はroot以下で最初に見つかったnameという名前のファイルの中身を返す。
func findFile(root, name string) string {
	var out string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || out != "" || d.IsDir() || d.Name() != name {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil {
			out = string(b)
		}
		return nil
	})
	return out
}

//go:embed testdata/continuation.sh
var continuationScript string

// continuationRepoFiles は継続の検査だけをする使い捨てのリポジトリ。settings.jsonとDockerfileは
// pythonRepoFilesと同じものを使う（同じイメージをキャッシュから使えるように）。
var continuationRepoFiles = map[string]string{
	".gitignore":                        ".masuda/settings.local.json\n",
	".masuda/settings.json":             pythonRepoFiles[".masuda/settings.json"],
	".masuda/images/default/Dockerfile": pythonRepoFiles[".masuda/images/default/Dockerfile"],
	".masuda/workflows/continuation.yaml": `version: 1
inputs: [instructions]
start: continuation
nodes:
  continuation:
    type: exec
    command: ["/bin/bash", "/workspace/scripts/continuation.sh"]
    outputs: [continuation-report]
    timeout: 8m
    next: {done: end, failed: end:not_continued}
`,
	"scripts/continuation.sh": continuationScript,
}
