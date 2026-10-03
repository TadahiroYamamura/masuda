// Package live は実物のsandbox service（QEMUのVM）と本物のClaude Codeで、同梱のワークフローを
// 1周させるテストを持つ。M8の段階1（使い捨てのPythonリポジトリでdevelopを1周、ゲートはAPIで承認）を
// 自動化したもの。
//
// 既定ではskipする。動かすには次が要る。
//   - MASUDA_LIVE_TEST=1
//   - `masuda-sandbox serve`が$XDG_RUNTIME_DIR/masuda-sandbox.sock（MASUDA_SANDBOX_SOCKETで上書き可）で
//     待ち受けていること
//   - Claude APIのトークン: MASUDA_LIVE_CLAUDE_TOKEN、無ければmasudaの既定のデータディレクトリの
//     `claude-oauth-token`
//
// 1周に15〜20分かかるので、`go test`の既定のタイムアウト（10分）では足りない:
//
//	MASUDA_LIVE_TEST=1 go test -count=1 -timeout 60m -v ./live/
//
// TestGuestSubagentContinuation（continuation_test.go）は、ゲストのClaude Codeがサブエージェントに
// SendMessageで続きを送ったとき前の文脈が残るかを確かめる。1〜2分で終わる。ゲストのClaude Codeの版
// （guest.ClaudeCodeVersion。liveのDockerfileもこの版を入れる）を上げたら、1周より先に単独で回す: `MASUDA_LIVE_TEST=1 go test -count=1 -timeout 20m -v -run TestGuestSubagentContinuation ./live/`
//
// TestEngineContinuationKeepsMemory・TestEngineContinuationFallsBackAfterResume
// （engine_continuation_test.go）は、ワークフローの`continues`がゲストのサブエージェントまで届くか
// （続きが成り立つ場合と、再開でVMを作り直して新しいサブエージェントが入力だけで進む場合）を確かめる。
// 1本1分前後: `MASUDA_LIVE_TEST=1 go test -count=1 -timeout 20m -v -run TestEngineContinuation ./live/`
//
// TestClaudeDirReachesSubagent（claude_dir_test.go）は、`.masuda/claude/`のルール・スキルと
// `.masuda/claude.local/`のCLAUDE.mdがゲストのサブエージェントまで届くかを確かめる。1分前後。
//
// 失敗したときは、データディレクトリ（ワークスペースの記録・stagingを含む）と対象リポジトリを
// 消さずに残し、パスをログに出す。MASUDA_LIVE_KEEP=1なら成功しても残す。VMはどちらでも壊す。
//
// TestFixLapOnPythonRepoは同じ課題を同梱のfixで1周させる。VMを使うテストは並列にしない
// （`-run 'TestDevelopLapOnPythonRepo|TestFixLapOnPythonRepo'`で直列に回る）。
package live

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
	"github.com/TadahiroYamamura/masuda/internal/guest"
	"github.com/TadahiroYamamura/masuda/serve"
)

// lapBudget は1周にかけてよい時間。M8では約18分だった。
const lapBudget = 45 * time.Minute

func sandboxSocket() string {
	if s := os.Getenv("MASUDA_SANDBOX_SOCKET"); s != "" {
		return s
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("masuda-%d", os.Getuid()))
	}
	return filepath.Join(dir, "masuda-sandbox.sock")
}

// claudeToken はトークンを環境変数か、masudaの既定のデータディレクトリの暫定ファイルから読む。
// 値はログに出さない。
func claudeToken() string {
	if v := strings.TrimSpace(os.Getenv("MASUDA_LIVE_CLAUDE_TOKEN")); v != "" {
		return v
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	b, err := os.ReadFile(filepath.Join(dataHome, "masuda", "claude-oauth-token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=masuda live", "GIT_AUTHOR_EMAIL=live@example.invalid",
		"GIT_COMMITTER_NAME=masuda live", "GIT_COMMITTER_EMAIL=live@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// keepOnFailure はテスト用の一時ディレクトリを作り、成功したときだけ消す。MASUDA_LIVE_KEEP=1なら
// 成功しても残す（1周の記録、例えば計画の`checks`やレビューの指摘を後から読むため）。
func keepOnFailure(t *testing.T, pattern string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", pattern)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() || os.Getenv("MASUDA_LIVE_KEEP") == "1" {
			t.Logf("kept %s for investigation", dir)
			return
		}
		_ = os.RemoveAll(dir)
	})
	return dir
}

// destroyVMOnCleanup は、テストがどう終わっても（失敗・タイムアウト・MASUDA_LIVE_KEEP=1でも）
// ワークスペースのVMを後始末で壊す。DONEになればserveが壊すが、途中で失敗・タイムアウトした
// テストではVMが残る。データを残さないときはRemove（VMも壊す）を呼ぶ。データを残すときは
// Removeが記録を消してしまうので、Stopに加えてsandbox serviceへ直接DestroySandboxする。
// DONEで既に壊れていれば、Stop（DONEには効かない）とDestroyのNotFoundは失敗にしない。
// serveとそのctxより先に動くよう、それらもt.Cleanupで、これより前に登録すること。
func destroyVMOnCleanup(t *testing.T, api clients, sbSock, id string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if !t.Failed() && os.Getenv("MASUDA_LIVE_KEEP") != "1" {
			if _, err := api.ws.Remove(ctx, connect.NewRequest(&apiv1.RemoveRequest{Id: id, Force: true})); err != nil {
				t.Errorf("removing workspace %s: %v", id, err)
			}
			return
		}
		_, _ = api.ws.Stop(ctx, connect.NewRequest(&apiv1.StopRequest{Id: id}))
		if _, err := sandboxClient(sbSock).DestroySandbox(ctx, connect.NewRequest(&sandboxv1.DestroySandboxRequest{Id: id})); err != nil && connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("destroying the sandbox of %s: %v", id, err)
		}
	})
}

func sandboxClient(sbSock string) sandboxv1connect.SandboxServiceClient {
	httpc := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sbSock)
		},
	}}
	return sandboxv1connect.NewSandboxServiceClient(httpc, "http://masuda-sandbox")
}

func assertCleanedUpAtDone(t *testing.T, sbSock, id, wsDir string) {
	t.Helper()
	_, err := sandboxClient(sbSock).GetSandbox(context.Background(), connect.NewRequest(&sandboxv1.GetSandboxRequest{Id: id}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("the sandbox of a done workspace must be destroyed: %v", err)
	}
	var transcripts []string
	_ = filepath.WalkDir(filepath.Join(wsDir, "exports", "transcripts"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			transcripts = append(transcripts, p)
		}
		return nil
	})
	if len(transcripts) == 0 {
		t.Errorf("no transcripts in exports/transcripts of a done workspace")
	}
	t.Logf("exported transcripts: %d", len(transcripts))
}

// newPythonRepo はM8の段階1と同じ使い捨てのリポジトリを作る。`.masuda/`はsettings.jsonと
// Dockerfileだけをコミットし、reviewsは置かない（観点がmasudaの同梱から揃うことも確かめる）。
func newPythonRepo(t *testing.T) string {
	t.Helper()
	repo := keepOnFailure(t, "masuda-live-repo-")
	run(t, repo, "git", "init", "-q", "-b", "main")
	writeFiles(t, repo, pythonRepoFiles)
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "commit", "-qm", "initial shapes")
	return repo
}

type clients struct {
	ws        apiv1connect.WorkspaceServiceClient
	gates     apiv1connect.GateServiceClient
	questions apiv1connect.QuestionServiceClient
	config    apiv1connect.ConfigServiceClient
}

func connectAPI(sock string) clients {
	httpc := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	const base = "http://masuda"
	return clients{
		ws:        apiv1connect.NewWorkspaceServiceClient(httpc, base, connect.WithGRPC()),
		gates:     apiv1connect.NewGateServiceClient(httpc, base, connect.WithGRPC()),
		questions: apiv1connect.NewQuestionServiceClient(httpc, base, connect.WithGRPC()),
		config:    apiv1connect.NewConfigServiceClient(httpc, base, connect.WithGRPC()),
	}
}

// TestDevelopLapOnPythonRepo は同梱のdevelopを使い捨てのPythonリポジトリで1周させる。
// plan・review・interimのゲートはAPIで承認し、計画の問い（plan-interviewerの質問）には固定の
// 答えを返す。それ以外のゲート（deviation・triage等）が開いたら失敗にする。publishで実リポジトリの
// ブランチに着地し、そのブランチでテストが通ることまで確かめる。
func TestDevelopLapOnPythonRepo(t *testing.T) {
	runLap(t, "workflows/develop", "feat/live")
}

// TestFixLapOnPythonRepo は同梱のfix（調査と計画を1セッションで済ませる軽いワークフロー）を、
// developと同じリポジトリ・同じ課題で1周させる。検査もdevelopと同じ。
func TestFixLapOnPythonRepo(t *testing.T) {
	runLap(t, "workflows/fix", "feat/live-fix")
}

// runLap はworkflowを三角形の課題で1周させ、branchに着地してテストが通ることを確かめる。
func runLap(t *testing.T, workflow, branch string) {
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
	if dl, ok := t.Deadline(); ok && time.Until(dl) < lapBudget {
		t.Fatalf("the lap needs up to %v; run with -timeout 60m (the test deadline is in %v)", lapBudget, time.Until(dl).Round(time.Second))
	}
	started := time.Now()

	repo := newPythonRepo(t)
	dataDir := keepOnFailure(t, "masuda-live-data-")
	sock := filepath.Join(dataDir, "masuda.sock")
	ctx, cancel := context.WithTimeout(context.Background(), lapBudget)
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
		Branch:   branch,
		Inputs:   map[string][]byte{"instructions": []byte(triangleTask)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	t.Logf("workspace %s (data %s)", id, dataDir)
	destroyVMOnCleanup(t, api, sbSock, id)

	final, err := driveLap(ctx, t, api, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("done: outcome %q in %v", final.Outcome, time.Since(started).Round(time.Second))

	// publishで実リポジトリのブランチに着地し、そのブランチでテストが通る。
	files := run(t, repo, "git", "ls-tree", "-r", "--name-only", branch)
	for _, want := range []string{"shapes/triangle.py", "tests/test_triangle.py"} {
		if !strings.Contains(files, want) {
			t.Fatalf("%s lacks %s:\n%s", branch, want, files)
		}
	}
	if _, err := exec.LookPath("python3"); err == nil {
		check := keepOnFailure(t, "masuda-live-check-")
		run(t, repo, "git", "worktree", "add", "-q", check, branch)
		defer run(t, repo, "git", "worktree", "remove", "--force", check)
		run(t, check, "python3", "-m", "unittest", "discover", "-s", "tests")
	} else {
		t.Log("python3 is not on the host; skipped running the published tests")
	}
	exports := filepath.Join(dataDir, "workspaces", id, "exports", "execution-log.jsonl")
	if _, err := os.Stat(exports); err != nil {
		t.Fatalf("exports must include the execution log: %v", err)
	}
}

// approvable は人間の代わりにこのテストが承認してよいゲート。
var approvable = map[string]bool{"plan": true, "review": true, "interim": true}

// driveLap はワークスペースがDONEになるまで状態を見て、開いたゲートを承認し、開いた質問に答える。
// BLOCKED・STOPPED、承認してよくないゲート、DEADは失敗として返す。
func driveLap(ctx context.Context, t *testing.T, api clients, id string) (*apiv1.Workspace, error) {
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
		switch w.State {
		case apiv1.WorkspaceState_WORKSPACE_STATE_DONE:
			return w, nil
		case apiv1.WorkspaceState_WORKSPACE_STATE_BLOCKED, apiv1.WorkspaceState_WORKSPACE_STATE_STOPPED:
			return nil, fmt.Errorf("workspace %s is %s: %s", id, w.State, w.Reason)
		case apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_QUESTION:
			if err := answerOpenQuestions(ctx, t, api, id); err != nil {
				return nil, err
			}
		case apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE:
			if err := approveOpenGates(ctx, t, api, id); err != nil {
				return nil, err
			}
		}
		if w.GetActivity().GetKind() == apiv1.ActivityKind_ACTIVITY_KIND_DEAD {
			return nil, fmt.Errorf("workspace %s: the guest's claude is gone: %s", id, w.GetActivity().GetDetail())
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("workspace %s did not finish within %v (last: %s)", id, lapBudget, lastLine)
		case <-time.After(5 * time.Second):
		}
	}
}

// liveAnswer はこのテストが人間の代わりに返す答え。developのplan-interviewerは計画の問いのうち
// 計画を直す役が判断できなかったものを聞くので、計画を変えずに進められる答えにする。
const liveAnswer = "計画のとおりでよい。判断に迷う点は安全側に倒す"

// answerOpenQuestions は開いている質問のすべての問いにliveAnswerを返し、問いと答えをログに出す。
// 選択肢のある問いには最初の選択肢を返す（選択肢の外の答えは断られるため）。
func answerOpenQuestions(ctx context.Context, t *testing.T, api clients, id string) error {
	open, err := api.questions.ListOpen(ctx, connect.NewRequest(&apiv1.ListOpenQuestionsRequest{WorkspaceId: id}))
	if err != nil {
		return err
	}
	for _, q := range open.Msg.Questions {
		answers := map[string]string{}
		for _, it := range q.Items {
			a := liveAnswer
			if len(it.Options) > 0 {
				a = it.Options[0]
			}
			answers[it.Id] = a
			t.Logf("question %s (occ %s):\n%s\n  -> %s", it.Id, q.Occurrence, it.Text, a)
		}
		_, err := api.questions.Answer(ctx, connect.NewRequest(&apiv1.AnswerRequest{WorkspaceId: id, Occurrence: q.Occurrence, Answers: answers}))
		// 見ている間に閉じた質問は、次の周回で見直す。
		if code := connect.CodeOf(err); code == connect.CodeFailedPrecondition || code == connect.CodeNotFound {
			t.Logf("question occ %s: %v", q.Occurrence, err)
		} else if err != nil {
			return err
		}
	}
	return nil
}

func approveOpenGates(ctx context.Context, t *testing.T, api clients, id string) error {
	open, err := api.gates.ListOpen(ctx, connect.NewRequest(&apiv1.ListOpenGatesRequest{WorkspaceId: id}))
	if err != nil {
		return err
	}
	for _, g := range open.Msg.Gates {
		if !approvable[g.Gate] {
			return fmt.Errorf("gate %s (occ %s) opened; this test approves only %v:\n%s", g.Gate, g.Occurrence, approvable, g.Subject)
		}
		t.Logf("approving gate %s (occ %s)", g.Gate, g.Occurrence)
		_, err := api.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{
			WorkspaceId: id,
			Occurrence:  g.Occurrence,
			Decision:    &apiv1.Decision{Outcome: "approved", TargetHash: g.TargetHash, Comment: "approved by the live test"},
		}))
		// 見ている間に閉じた・変わったゲートは、次の周回で見直す。
		if connect.CodeOf(err) == connect.CodeFailedPrecondition {
			t.Logf("gate %s: %v", g.Gate, err)
		} else if err != nil {
			return err
		}
	}
	return nil
}

const triangleTask = "`shapes/` に3つめのモジュール `shapes/triangle.py` を追加してください。\n\n" +
	"- `area(a, b, c)`: 3辺の長さから面積を返す（ヘロンの公式）\n" +
	"- `perimeter(a, b, c)`: 周の長さを返す\n" +
	"- 既存の `circle.py`・`rectangle.py` と同じく、負の辺には `ValueError` を投げる。三角形が成り立たない辺（三角不等式を満たさない）にも `ValueError` を投げる\n" +
	"- `tests/test_triangle.py` に unittest のテストを足す（既存テストと同じ書き方）\n\n" +
	"標準ライブラリだけを使うこと。他のファイルは変更しないこと。\n"

// pythonRepoFiles はM8の段階1で使ったリポジトリ。バイトコードのキャッシュは追跡しない
// （読み取り専用のエージェントがテストを走らせて書き換えると計画外の変更になるため）。
var pythonRepoFiles = map[string]string{
	".gitignore": "__pycache__/\n*.pyc\n.masuda/settings.local.json\n",
	"README.md":  "# shapes\n\nSmall geometry helpers. Run tests with `python3 -m unittest discover -s tests -v`.\n",
	".masuda/settings.json": `{
  "image": "default",
  "egress": ["api.anthropic.com"],
  "checks": {"test": "python3 -m unittest discover -s tests -v"}
}
`,
	".masuda/images/default/Dockerfile": `FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git tmux openssh-server python3 \
 && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /workspace /masuda && chown ubuntu:ubuntu /workspace /masuda
USER ubuntu
WORKDIR /home/ubuntu
RUN curl -fsSL https://claude.ai/install.sh | bash -s -- ` + guest.ClaudeCodeVersion + `
ENV PATH=/home/ubuntu/.local/bin:$PATH
USER root
`,
	"shapes/__init__.py": "",
	"shapes/circle.py": `import math


def area(radius):
    if radius < 0:
        raise ValueError("radius must be non-negative")
    return math.pi * radius * radius


def perimeter(radius):
    if radius < 0:
        raise ValueError("radius must be non-negative")
    return 2 * math.pi * radius
`,
	"shapes/rectangle.py": `def area(width, height):
    if width < 0 or height < 0:
        raise ValueError("sides must be non-negative")
    return width * height


def perimeter(width, height):
    if width < 0 or height < 0:
        raise ValueError("sides must be non-negative")
    return 2 * (width + height)
`,
	"tests/__init__.py": "",
	"tests/test_circle.py": `import math
import unittest

from shapes import circle


class CircleTest(unittest.TestCase):
    def test_area(self):
        self.assertAlmostEqual(circle.area(2), 4 * math.pi)

    def test_perimeter(self):
        self.assertAlmostEqual(circle.perimeter(1), 2 * math.pi)

    def test_negative(self):
        with self.assertRaises(ValueError):
            circle.area(-1)


if __name__ == "__main__":
    unittest.main()
`,
	"tests/test_rectangle.py": `import unittest

from shapes import rectangle


class RectangleTest(unittest.TestCase):
    def test_area(self):
        self.assertEqual(rectangle.area(3, 4), 12)

    def test_perimeter(self):
        self.assertEqual(rectangle.perimeter(3, 4), 14)

    def test_negative(self):
        with self.assertRaises(ValueError):
            rectangle.perimeter(-1, 2)


if __name__ == "__main__":
    unittest.main()
`,
}

// liveのイメージは、masudaが検証した版として出すClaude Codeを入れる（VMを使わないので常に走る）。
func TestLiveDockerfilePinsClaudeCode(t *testing.T) {
	want := "claude.ai/install.sh | bash -s -- " + guest.ClaudeCodeVersion + "\n"
	for name, files := range map[string]map[string]string{"python": pythonRepoFiles, "continuation": continuationRepoFiles} {
		if !strings.Contains(files[".masuda/images/default/Dockerfile"], want) {
			t.Errorf("%s: Dockerfile lacks %q", name, want)
		}
	}
}
