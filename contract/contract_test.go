// Package contract holds masuda's contract tests: the definition of done for
// each work order in docs/work-orders.md. They start `serve` in-process with
// the fake sandbox (no VM) and the real engine, and drive it only through the
// public API (proto/masuda/api/v1) and the guest protocol (docs/guest-protocol.md).
//
// Owned by the supervisor. Implementers do not edit assertions; if a test is
// wrong, say so in HANDOFF.md.
package contract

import (
	"context"
	"crypto/tls"
	"encoding/json"
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
	"github.com/TadahiroYamamura/masuda/serve"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type harness struct {
	t         *testing.T
	dataDir   string
	repo      string // a throwaway git repository the workspace targets
	ws        apiv1connect.WorkspaceServiceClient
	gates     apiv1connect.GateServiceClient
	questions apiv1connect.QuestionServiceClient
	staging   apiv1connect.StagingServiceClient
	config    apiv1connect.ConfigServiceClient
	workflows apiv1connect.WorkflowServiceClient
	httpc     *http.Client
	baseURL   string
	srv       *serve.Server
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	for p, c := range files {
		full := filepath.Join(dir, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")
	return dir
}

func start(t *testing.T, repoFiles map[string]string) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dataDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "masuda.sock")
	srv, err := serve.Start(ctx, serve.Options{Socket: sock, DataDir: dataDir, FakeSandbox: true})
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })
	httpc := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	base := "http://masuda"
	h := &harness{
		t: t, dataDir: dataDir, httpc: httpc, baseURL: base, srv: srv,
		ws:        apiv1connect.NewWorkspaceServiceClient(httpc, base, connect.WithGRPC()),
		gates:     apiv1connect.NewGateServiceClient(httpc, base, connect.WithGRPC()),
		questions: apiv1connect.NewQuestionServiceClient(httpc, base, connect.WithGRPC()),
		staging:   apiv1connect.NewStagingServiceClient(httpc, base, connect.WithGRPC()),
		config:    apiv1connect.NewConfigServiceClient(httpc, base, connect.WithGRPC()),
		workflows: apiv1connect.NewWorkflowServiceClient(httpc, base, connect.WithGRPC()),
	}
	if repoFiles != nil {
		h.repo = newRepo(t, repoFiles)
	}
	return h
}

// in returns the harness reporting failures to t, for use inside a subtest:
// a subtest must not call FailNow on its parent's t.
func (h *harness) in(t *testing.T) *harness {
	c := *h
	c.t = t
	return &c
}

// mcp calls one tool on the workspace's guest-facing MCP endpoint, the way the
// guest would (the fake sandbox exposes the per-workspace port on loopback and
// GetWorkspace reports it in position metadata; here we read it from the
// fake's registry file written by serve).
func (h *harness) mcp(wsID, tool string, args map[string]any) map[string]any {
	h.t.Helper()
	portFile := filepath.Join(h.dataDir, "fake", wsID, "mcp.port")
	var port string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(portFile); err == nil {
			port = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if port == "" {
		h.t.Fatalf("mcp port for %s not published at %s", wsID, portFile)
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	req, _ := http.NewRequest("POST", "http://127.0.0.1:"+port+"/mcp", strings.NewReader(string(body)))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json, text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("mcp %s: %v", tool, err)
	}
	defer res.Body.Close()
	var rpc struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&rpc); err != nil {
		h.t.Fatalf("mcp %s decode: %v", tool, err)
	}
	if rpc.Result.IsError {
		return map[string]any{"error": rpc.Result.Content[0].Text}
	}
	out := map[string]any{}
	if len(rpc.Result.Content) > 0 {
		_ = json.Unmarshal([]byte(rpc.Result.Content[0].Text), &out)
	}
	return out
}

func (h *harness) guestWrite(wsID, guestPath, content string) {
	h.t.Helper()
	full := filepath.Join(h.dataDir, "fake", wsID, "root", guestPath)
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) waitState(wsID string, want apiv1.WorkspaceState, within time.Duration) *apiv1.Workspace {
	h.t.Helper()
	deadline := time.Now().Add(within)
	var last *apiv1.Workspace
	for time.Now().Before(deadline) {
		res, err := h.ws.Get(context.Background(), connect.NewRequest(&apiv1.GetWorkspaceRequest{Id: wsID}))
		if err == nil {
			last = res.Msg
			if last.State == want {
				return last
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	h.t.Fatalf("workspace %s did not reach %v; last=%+v", wsID, want, last)
	return nil
}

const smokeWorkflow = `version: 1
inputs: [instructions]
start: plan
nodes:
  plan: {type: agent, role: agents/smoke-planner, inputs: [instructions], outputs: [plan], next: approve}
  approve: {type: approval, gate: plan, target: plan, next: {approved: step, rejected: plan}}
  step: {type: agent, role: agents/smoke-implementer, outputs: [commit-message], next: commit}
  commit: {type: commit, scope: plan, next: {done: review, rejected: step}}
  review: {type: approval, gate: review, target: diff, next: {approved: publish, rejected: step}}
  publish: {type: publish, target: local, next: end}
`

const smokePlanner = `---
name: smoke-planner
description: writes a fixed plan
tools: Read
outputs: [plan]
outcomes:
  done: planned
---
Write the plan.
`

const smokeImplementer = `---
name: smoke-implementer
description: edits a file
tools: Read, Edit, Bash
outputs: [commit-message]
outcomes:
  done: edited
---
Edit the file.
`

func smokeRepo() map[string]string {
	return map[string]string{
		"README.md":                           "# repo\n",
		"a.go":                                "package a\n",
		".masuda/settings.json":               `{"image":"default","egress":[],"secrets":[],"checks":{}}`,
		".masuda/images/default/Dockerfile":   "FROM ubuntu:24.04\n",
		".masuda/workflows/smoke.yaml":        smokeWorkflow,
		".masuda/agents/smoke-planner.md":     smokePlanner,
		".masuda/agents/smoke-implementer.md": smokeImplementer,
	}
}

// ---------------------------------------------------------------------------
// C-M1: serve starts, API answers
// ---------------------------------------------------------------------------

func TestCM1_ServeAnswersOverSocket(t *testing.T) {
	h := start(t, nil)
	res, err := h.ws.List(context.Background(), connect.NewRequest(&apiv1.ListWorkspacesRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Msg.Workspaces) != 0 {
		t.Fatalf("fresh data dir lists %d workspaces", len(res.Msg.Workspaces))
	}
	_, err = h.ws.Get(context.Background(), connect.NewRequest(&apiv1.GetWorkspaceRequest{Id: "nope"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("Get(nope) = %v, want NotFound", err)
	}
	// HTTP+JSON works on the same socket (what a browser UI does).
	req, _ := http.NewRequest("POST", h.baseURL+"/masuda.api.v1.WorkspaceService/List", strings.NewReader(`{}`))
	req.Header.Set("content-type", "application/json")
	r, err := h.httpc.Do(req)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("HTTP+JSON List: %v %v", err, r)
	}
}

// ---------------------------------------------------------------------------
// C-M2: staging
// ---------------------------------------------------------------------------

func TestCM2_RunCreatesStagingAndRefs(t *testing.T) {
	h := start(t, smokeRepo())
	res, err := h.ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{
		RepoRoot: h.repo, Workflow: "workflows/smoke", Branch: "feat/smoke", Inputs: map[string][]byte{"instructions": []byte("add b.go")},
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	ws := res.Msg
	if ws.Branch != "feat/smoke" || ws.Base == "" {
		t.Fatalf("workspace: %+v", ws)
	}
	refs, err := h.staging.ListRefs(context.Background(), connect.NewRequest(&apiv1.ListRefsRequest{WorkspaceId: ws.Id}))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, r := range refs.Msg.Refs {
		names[r.Name] = r.Commit
	}
	if names["refs/heads/feat/smoke"] == "" || names["refs/masuda/base"] == "" || names["refs/heads/feat/smoke"] != names["refs/masuda/base"] {
		t.Fatalf("staging refs after Run: %v", names)
	}
	// The real repository is untouched: no feat/smoke branch there yet.
	if out := git(t, h.repo, "branch", "--list", "feat/smoke"); out != "" {
		t.Fatalf("Run must not touch the real repository; found %q", out)
	}
	c, err := h.staging.GetCommit(context.Background(), connect.NewRequest(&apiv1.GetCommitRequest{WorkspaceId: ws.Id, Rev: "refs/masuda/base"}))
	if err != nil || c.Msg.Message == "" {
		t.Fatalf("GetCommit: %v %+v", err, c)
	}
}

// ---------------------------------------------------------------------------
// C-M3: fake sandbox
// ---------------------------------------------------------------------------

func TestCM3_FakeSandboxBootsGuestLayout(t *testing.T) {
	h := start(t, smokeRepo())
	res, err := h.ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{
		RepoRoot: h.repo, Workflow: "workflows/smoke", Branch: "feat/smoke", Inputs: map[string][]byte{"instructions": []byte("x")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ws := h.waitState(res.Msg.Id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second)
	root := filepath.Join(h.dataDir, "fake", ws.Id, "root")
	for _, p := range []string{"workspace/.git", "workspace/README.md", "home/ubuntu/.claude/CLAUDE.md", "home/ubuntu/.claude/agents/smoke-planner.md", "home/ubuntu/.claude/settings.json"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Fatalf("guest layout missing %s: %v", p, err)
		}
	}
	// The guest clone is at the staging branch head, and is not the real repo.
	head := git(t, filepath.Join(root, "workspace"), "rev-parse", "HEAD")
	refs, _ := h.staging.ListRefs(context.Background(), connect.NewRequest(&apiv1.ListRefsRequest{WorkspaceId: ws.Id}))
	var branchHead string
	for _, r := range refs.Msg.Refs {
		if r.Name == "refs/heads/feat/smoke" {
			branchHead = r.Commit
		}
	}
	if head != branchHead {
		t.Fatalf("guest HEAD %s != staging branch %s", head, branchHead)
	}
	// No real token in the guest: the OAuth env var is a placeholder.
	settings, _ := os.ReadFile(filepath.Join(root, "home/ubuntu/.claude/settings.json"))
	if strings.Contains(string(settings), "sk-ant-oat01-") && !strings.Contains(string(settings), "placeholder") {
		t.Fatalf("settings must not carry a real token")
	}
}

// ---------------------------------------------------------------------------
// C-M4: one lap through the guest protocol
// ---------------------------------------------------------------------------

func TestCM4_GuestProtocolLapToPublish(t *testing.T) {
	h := start(t, smokeRepo())
	ctx := context.Background()
	res, err := h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{
		RepoRoot: h.repo, Workflow: "workflows/smoke", Branch: "feat/smoke", Inputs: map[string][]byte{"instructions": []byte("add b.go")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ws := h.waitState(res.Msg.Id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second)
	id := ws.Id

	// 1. planner
	task := h.mcp(id, "next_task", nil)
	if task["kind"] != "task" || task["role"] != "smoke-planner" {
		t.Fatalf("next_task: %v", task)
	}
	occ := task["occurrence"].(string)
	taskPath := task["task_path"].(string)
	if !strings.HasPrefix(taskPath, "/masuda/in/"+occ+"/") {
		t.Fatalf("task_path %q not under /masuda/in/<occ>/", taskPath)
	}
	if _, err := os.Stat(filepath.Join(h.dataDir, "fake", id, "root", taskPath)); err != nil {
		t.Fatalf("task file not materialized in guest: %v", err)
	}
	// Wrong occurrence is refused; wrong outcome is refused.
	if r := h.mcp(id, "report_result", map[string]any{"occurrence": "9999", "outcome": "done"}); r["error"] == nil && r["accepted"] != false {
		t.Fatalf("report_result for wrong occurrence accepted: %v", r)
	}
	if r := h.mcp(id, "report_result", map[string]any{"occurrence": occ, "outcome": "banana"}); r["error"] == nil && r["accepted"] != false {
		t.Fatalf("undeclared outcome accepted: %v", r)
	}
	// Output that fails the schema is rejected with problems.
	if r := h.mcp(id, "write_output", map[string]any{"occurrence": occ, "name": "plan", "content": `{"nope":1}`}); r["accepted"] != false {
		t.Fatalf("invalid plan accepted: %v", r)
	}
	plan := `{"goal":"b.goを足す","summary":"add b","steps":[{"number":1,"title":"bの追加","description":"add b.go","tests":[],"files":["b.go"]}],"alternatives":[],"risks":[],"expected_byproducts":[],"checks":[]}`
	if r := h.mcp(id, "write_output", map[string]any{"occurrence": occ, "name": "plan", "content": plan}); r["accepted"] != true {
		t.Fatalf("valid plan rejected: %v", r)
	}
	if r := h.mcp(id, "report_result", map[string]any{"occurrence": occ, "outcome": "done"}); r["accepted"] != true {
		t.Fatalf("report_result: %v", r)
	}

	// 2. plan gate: next_task blocks; approve via API with the right hash.
	ws = h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE, 10*time.Second)
	open, err := h.gates.ListOpen(ctx, connect.NewRequest(&apiv1.ListOpenGatesRequest{WorkspaceId: id}))
	if err != nil || len(open.Msg.Gates) != 1 || open.Msg.Gates[0].Gate != "plan" {
		t.Fatalf("open gates: %v %+v", err, open)
	}
	g := open.Msg.Gates[0]
	if _, err := h.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{WorkspaceId: id, Occurrence: g.Occurrence, Decision: &apiv1.Decision{Outcome: "approved", TargetHash: "wrong"}})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("approval with wrong hash: %v, want FailedPrecondition", err)
	}
	if _, err := h.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{WorkspaceId: id, Occurrence: g.Occurrence, Decision: &apiv1.Decision{Outcome: "approved", TargetHash: g.TargetHash}})); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	// 3. implementer edits the guest clone, reports; commit lands in staging only.
	task = h.mcp(id, "next_task", nil)
	if task["role"] != "smoke-implementer" {
		t.Fatalf("next_task after approval: %v", task)
	}
	occ = task["occurrence"].(string)
	h.guestWrite(id, "workspace/b.go", "package b\n")
	h.guestWrite(id, "workspace/notes.txt", "scratch\n") // outside the plan → deviation
	h.mcp(id, "write_output", map[string]any{"occurrence": occ, "name": "commit-message", "content": "feat: add b"})
	h.mcp(id, "report_result", map[string]any{"occurrence": occ, "outcome": "done"})
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE, 10*time.Second)
	open, _ = h.gates.ListOpen(ctx, connect.NewRequest(&apiv1.ListOpenGatesRequest{WorkspaceId: id}))
	if len(open.Msg.Gates) != 1 || open.Msg.Gates[0].Gate != "deviation" || !strings.Contains(string(open.Msg.Gates[0].Subject), "notes.txt") {
		t.Fatalf("want deviation gate for notes.txt, got %+v", open.Msg.Gates)
	}
	g = open.Msg.Gates[0]
	// Approve only b.go's plan; reject notes.txt by not listing it → it stays uncommitted.
	if _, err := h.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{WorkspaceId: id, Occurrence: g.Occurrence, Decision: &apiv1.Decision{Outcome: "approved", TargetHash: g.TargetHash, ApprovedFiles: []string{}}})); err != nil {
		t.Fatal(err)
	}
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE, 10*time.Second)
	open, _ = h.gates.ListOpen(ctx, connect.NewRequest(&apiv1.ListOpenGatesRequest{WorkspaceId: id}))
	if len(open.Msg.Gates) != 1 || open.Msg.Gates[0].Gate != "review" {
		t.Fatalf("want review gate, got %+v", open.Msg.Gates)
	}
	rg := open.Msg.Gates[0]
	c, err := h.staging.GetCommit(ctx, connect.NewRequest(&apiv1.GetCommitRequest{WorkspaceId: id, Rev: "refs/heads/feat/smoke"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Msg.Files, ",") != "b.go" || !strings.HasPrefix(c.Msg.Message, "feat: add b") {
		t.Fatalf("commit must contain exactly the plan's files with the agent's message: %+v", c.Msg)
	}
	if rg.StagingCommit != c.Msg.Hash {
		t.Fatalf("review gate must point at the staging commit it reviews: %s vs %s", rg.StagingCommit, c.Msg.Hash)
	}
	// The subject is what publish will land (base..branch head), with the
	// uncommitted leftovers listed separately, never mixed into the diff.
	subj := string(rg.Subject)
	if !strings.Contains(subj, "package b") || !strings.Contains(subj, "publishされない") || !strings.Contains(subj, "notes.txt") {
		t.Fatalf("review subject must show the committed diff and list notes.txt as not published:\n%s", subj)
	}
	if i := strings.Index(subj, "publishされない"); strings.Contains(subj[:i], "scratch") {
		t.Fatalf("uncommitted content leaked into the committed diff part of the subject")
	}
	if git(t, h.repo, "branch", "--list", "feat/smoke") != "" {
		t.Fatalf("real repo touched before publish")
	}
	diff, err := h.staging.Diff(ctx, connect.NewRequest(&apiv1.DiffRequest{WorkspaceId: id, From: "refs/masuda/base", To: "refs/heads/feat/smoke"}))
	if err != nil || !strings.Contains(diff.Msg.Unified, "package b") {
		t.Fatalf("Diff: %v %q", err, diff.Msg.GetUnified())
	}

	// 4. approve review → publish fast-forwards the real repo to that exact commit.
	if _, err := h.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{WorkspaceId: id, Occurrence: rg.Occurrence, Decision: &apiv1.Decision{Outcome: "approved", TargetHash: rg.TargetHash}})); err != nil {
		t.Fatal(err)
	}
	done := h.mcp(id, "next_task", nil)
	if done["kind"] != "done" {
		t.Fatalf("next_task after publish: %v", done)
	}
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_DONE, 15*time.Second)
	if got := git(t, h.repo, "rev-parse", "feat/smoke"); got != c.Msg.Hash {
		t.Fatalf("published %s, approved %s", got, c.Msg.Hash)
	}
	if git(t, h.repo, "show", "feat/smoke", "--stat", "--format=") == "" || strings.Contains(git(t, h.repo, "ls-tree", "-r", "--name-only", "feat/smoke"), "notes.txt") {
		t.Fatalf("notes.txt must not have been published")
	}
	if _, err := os.Stat(filepath.Join(h.dataDir, "workspaces", id, "exports", "execution-log.jsonl")); err != nil {
		t.Fatalf("exports must include the execution log: %v", err)
	}
}

// C-M4の差し戻し: review gateを却下すると、承認対象のコミットへの人間の行コメントが
// 理由の本文とともに差し戻し先のタスクファイルへ届く。ゲートの記録の本文は人間が送ったまま。
func TestCM4_RejectedReviewCarriesLineComments(t *testing.T) {
	h := start(t, smokeRepo())
	ctx := context.Background()
	res, err := h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{
		RepoRoot: h.repo, Workflow: "workflows/smoke", Branch: "feat/lc", Inputs: map[string][]byte{"instructions": []byte("add b.go")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := h.waitState(res.Msg.Id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second).Id
	task := h.mcp(id, "next_task", nil)
	occ := task["occurrence"].(string)
	plan := `{"goal":"b.goを足す","summary":"add b","steps":[{"number":1,"title":"bの追加","description":"add b.go","tests":[],"files":["b.go"]}],"alternatives":[],"risks":[],"expected_byproducts":[],"checks":[]}`
	h.mcp(id, "write_output", map[string]any{"occurrence": occ, "name": "plan", "content": plan})
	h.mcp(id, "report_result", map[string]any{"occurrence": occ, "outcome": "done"})
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE, 10*time.Second)
	open, _ := h.gates.ListOpen(ctx, connect.NewRequest(&apiv1.ListOpenGatesRequest{WorkspaceId: id}))
	g := open.Msg.Gates[0]
	if _, err := h.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{WorkspaceId: id, Occurrence: g.Occurrence, Decision: &apiv1.Decision{Outcome: "approved", TargetHash: g.TargetHash}})); err != nil {
		t.Fatal(err)
	}
	task = h.mcp(id, "next_task", nil)
	occ = task["occurrence"].(string)
	h.guestWrite(id, "workspace/b.go", "package b\n")
	h.mcp(id, "write_output", map[string]any{"occurrence": occ, "name": "commit-message", "content": "feat: add b"})
	h.mcp(id, "report_result", map[string]any{"occurrence": occ, "outcome": "done"})
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE, 10*time.Second)
	open, _ = h.gates.ListOpen(ctx, connect.NewRequest(&apiv1.ListOpenGatesRequest{WorkspaceId: id}))
	if len(open.Msg.Gates) != 1 || open.Msg.Gates[0].Gate != "review" {
		t.Fatalf("want review gate, got %+v", open.Msg.Gates)
	}
	rg := open.Msg.Gates[0]
	if _, err := h.staging.AddComment(ctx, connect.NewRequest(&apiv1.AddCommentRequest{WorkspaceId: id, Commit: rg.StagingCommit, Path: "b.go", Line: 1, Body: "パッケージ名をbetaにする"})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{WorkspaceId: id, Occurrence: rg.Occurrence, Decision: &apiv1.Decision{Outcome: "rejected", Comment: "直して"}})); err != nil {
		t.Fatal(err)
	}
	task = h.mcp(id, "next_task", nil)
	if task["role"] != "smoke-implementer" {
		t.Fatalf("next_task after reject: %v", task)
	}
	b, err := os.ReadFile(filepath.Join(h.dataDir, "fake", id, "root", task["task_path"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	if want := "直して\n\n## 差分への行コメント\n- b.go:1: パッケージ名をbetaにする"; !strings.Contains(string(b), want) {
		t.Fatalf("the rework task must carry the line comment:\n%s", b)
	}
	got, err := h.gates.Get(ctx, connect.NewRequest(&apiv1.GetGateRequest{WorkspaceId: id, Occurrence: rg.Occurrence}))
	if err != nil || got.Msg.Decision.GetComment() != "直して" {
		t.Fatalf("recorded comment: %v %q", err, got.Msg.Decision.GetComment())
	}
}

// ---------------------------------------------------------------------------
// C-M5: watch, activity, questions
// ---------------------------------------------------------------------------

func TestCM5_WatchStreamsStatusAndHookActivity(t *testing.T) {
	h := start(t, smokeRepo())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: h.repo, Workflow: "workflows/smoke", Branch: "feat/w", Inputs: map[string][]byte{"instructions": []byte("x")}}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	// While STARTING the activity is IDLE (not observed yet), so the hook is posted once RUNNING.
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 10*time.Second)
	stream, err := h.ws.Watch(ctx, connect.NewRequest(&apiv1.WatchRequest{Id: id}))
	if err != nil {
		t.Fatal(err)
	}
	// A guest hook arrives (as the guest's curl would post it).
	port, _ := os.ReadFile(filepath.Join(h.dataDir, "fake", id, "mcp.port"))
	hook := `{"hook_event_name":"Notification","notification_type":"idle_prompt","message":"waiting"}`
	r, err := http.Post("http://127.0.0.1:"+strings.TrimSpace(string(port))+"/hooks", "application/json", strings.NewReader(hook))
	if err != nil || r.StatusCode >= 300 {
		t.Fatalf("hooks POST: %v %v", err, r)
	}
	sawStatus, sawHook := false, false
	for stream.Receive() {
		ev := stream.Msg()
		if ev.WorkspaceId != id {
			t.Fatalf("event for another workspace: %+v", ev)
		}
		switch e := ev.Event.(type) {
		case *apiv1.WorkspaceEvent_Status:
			sawStatus = true
			if e.Status.Activity != nil && e.Status.Activity.InputWait == "idle" {
				sawHook = true
			}
		case *apiv1.WorkspaceEvent_GuestHook:
			sawHook = true
		}
		if sawStatus && sawHook {
			break
		}
	}
	if !sawStatus || !sawHook {
		t.Fatalf("Watch: status=%v hook=%v err=%v", sawStatus, sawHook, stream.Err())
	}
	ws, _ := h.ws.Get(context.Background(), connect.NewRequest(&apiv1.GetWorkspaceRequest{Id: id}))
	if ws.Msg.Activity == nil || ws.Msg.Activity.Kind != apiv1.ActivityKind_ACTIVITY_KIND_WAITING_INPUT {
		t.Fatalf("idle_prompt hook must surface as WAITING_INPUT: %+v", ws.Msg.Activity)
	}
	// Stop keeps records; Resume brings it back RUNNING with a new (fake) sandbox.
	if _, err := h.ws.Stop(context.Background(), connect.NewRequest(&apiv1.StopRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_STOPPED, 10*time.Second)
	if _, err := h.ws.Resume(context.Background(), connect.NewRequest(&apiv1.ResumeRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second)
	if task := h.mcp(id, "next_task", nil); task["role"] != "smoke-planner" {
		t.Fatalf("after Resume the run continues at the same position: %v", task)
	}
}

// ---------------------------------------------------------------------------
// C-M6: config and secrets
// ---------------------------------------------------------------------------

func TestCM6_EgressAndSecretsDeclaredApprovedStored(t *testing.T) {
	files := smokeRepo()
	files[".masuda/settings.json"] = `{"image":"default","egress":["api.linear.app"],"secrets":[{"name":"LINEAR_API_KEY","hosts":["api.linear.app"]},{"name":"LEGACY","hosts":["x.example.com"],"mode":"plaintext"}],"envFiles":[{"path":".env","vars":["LINEAR_API_KEY","PUBLIC_URL"]}],"checks":{}}`
	h := start(t, files)
	ctx := context.Background()
	eg, err := h.config.ListEgress(ctx, connect.NewRequest(&apiv1.RepoRequest{RepoRoot: h.repo}))
	if err != nil || len(eg.Msg.Entries) != 1 || eg.Msg.Entries[0].Approved {
		t.Fatalf("declared but unapproved egress expected: %v %+v", err, eg)
	}
	if _, err := h.config.ApproveEgress(ctx, connect.NewRequest(&apiv1.HostRequest{RepoRoot: h.repo, Host: "not-declared.example"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("approving an undeclared host: %v", err)
	}
	eg, _ = h.config.ApproveEgress(ctx, connect.NewRequest(&apiv1.HostRequest{RepoRoot: h.repo, Host: "api.linear.app"}))
	if !eg.Msg.Entries[0].Approved {
		t.Fatalf("approval not recorded")
	}
	local, _ := os.ReadFile(filepath.Join(h.repo, ".masuda/settings.local.json"))
	if !strings.Contains(string(local), "api.linear.app") {
		t.Fatalf("approval must live in settings.local.json")
	}
	sec, _ := h.config.ListSecrets(ctx, connect.NewRequest(&apiv1.RepoRequest{RepoRoot: h.repo}))
	if len(sec.Msg.Entries) != 2 || sec.Msg.Entries[0].ValueSet {
		t.Fatalf("secrets: %+v", sec.Msg.Entries)
	}
	sec, err = h.config.SetSecret(ctx, connect.NewRequest(&apiv1.SetSecretRequest{RepoRoot: h.repo, Name: "LINEAR_API_KEY", Value: "lin_real"}))
	if err != nil {
		t.Fatal(err)
	}
	var set bool
	for _, e := range sec.Msg.Entries {
		if e.Name == "LINEAR_API_KEY" {
			set = e.ValueSet
		}
	}
	if !set {
		t.Fatalf("value_set after SetSecret")
	}
	// The value is stored outside the repository, mode 0600, never in the repo.
	matches, _ := filepath.Glob(filepath.Join(h.dataDir, "secrets", "*", "LINEAR_API_KEY"))
	if len(matches) != 1 {
		t.Fatalf("secret file: %v", matches)
	}
	if st, _ := os.Stat(matches[0]); st.Mode().Perm() != 0o600 {
		t.Fatalf("secret file mode %v", st.Mode().Perm())
	}
	if b, _ := exec.Command("grep", "-r", "lin_real", h.repo).Output(); len(b) != 0 {
		t.Fatalf("secret leaked into the repository")
	}
	// plaintext mode without approval refuses to run.
	_, err = h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: h.repo, Workflow: "workflows/smoke", Branch: "feat/s", Inputs: map[string][]byte{"instructions": []byte("x")}}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "LEGACY") {
		t.Fatalf("plaintext secret without approval must refuse Run naming it: %v", err)
	}
}

// ---------------------------------------------------------------------------
// C-M7: privileged command (fake sandbox)
// ---------------------------------------------------------------------------

func TestCM7_PrivilegedCommandRunsInSecondSandbox(t *testing.T) {
	files := smokeRepo()
	files[".masuda/settings.json"] = `{"image":"default","egress":[],"secrets":[],"checks":{},"privilegedCommands":{"itest":{"image":"default","command":"id -u > /workspace/uid.txt; cat /workspace/build/artifact.txt > /workspace/out.txt","inputs":["build/**"],"outputs":["uid.txt","out.txt"],"timeoutSeconds":60}}}`
	files[".gitignore"] = "build/\n"
	h := start(t, files)
	ctx := context.Background()
	if _, err := h.config.ApprovePrivilegedCommand(ctx, connect.NewRequest(&apiv1.NameRequest{RepoRoot: h.repo, Name: "itest"})); err != nil {
		t.Fatal(err)
	}
	res, err := h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: h.repo, Workflow: "workflows/smoke", Branch: "feat/p", Inputs: map[string][]byte{"instructions": []byte("x")}}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second)
	_ = h.mcp(id, "next_task", nil)
	// An ignored build artifact only travels because `inputs` names it.
	h.guestWrite(id, "workspace/build/artifact.txt", "built\n")
	if r := h.mcp(id, "run_privileged_command", map[string]any{"name": "nope"}); r["error"] == nil {
		t.Fatalf("undeclared command accepted: %v", r)
	}
	r := h.mcp(id, "run_privileged_command", map[string]any{"name": "itest"})
	if r["error"] != nil || r["exit_code"] != float64(0) {
		t.Fatalf("run_privileged_command: %v", r)
	}
	dir := r["results_dir"].(string)
	if !strings.HasPrefix(dir, "/masuda/privileged/") {
		t.Fatalf("results_dir %q", dir)
	}
	root := filepath.Join(h.dataDir, "fake", id, "root")
	uid, _ := os.ReadFile(filepath.Join(root, dir, "outputs", "uid.txt"))
	out, _ := os.ReadFile(filepath.Join(root, dir, "outputs", "out.txt"))
	if strings.TrimSpace(string(uid)) != "0" || string(out) != "built\n" {
		t.Fatalf("privileged run: uid=%q out=%q", uid, out)
	}
	// The main guest's worktree was not written by the privileged run.
	if _, err := os.Stat(filepath.Join(root, "workspace", "uid.txt")); err == nil {
		t.Fatalf("privileged command must not write into the main worktree")
	}
}

// ---------------------------------------------------------------------------
// C-M8: a workflow without publish may run on an existing branch
// ---------------------------------------------------------------------------

const inspectWorkflow = `version: 1
inputs: [instructions]
start: look
nodes:
  look: {type: agent, role: agents/smoke-planner, inputs: [instructions], outputs: [plan], next: finish}
  finish: {type: discard, export: [plan], next: end}
`

func TestCM8_PublishLessWorkflowRunsOnExistingBranch(t *testing.T) {
	files := smokeRepo()
	files[".masuda/workflows/inspect.yaml"] = inspectWorkflow
	h := start(t, files)
	ctx := context.Background()
	// An existing branch with one commit beyond main.
	git(t, h.repo, "checkout", "-q", "-b", "feat/existing")
	if err := os.WriteFile(filepath.Join(h.repo, "existing.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, h.repo, "add", "-A")
	git(t, h.repo, "commit", "-qm", "existing work")
	want := git(t, h.repo, "rev-parse", "HEAD")
	git(t, h.repo, "checkout", "-q", "main")

	// develop publishes, so it must refuse an existing branch.
	_, err := h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: h.repo, Workflow: "workflows/smoke", Branch: "feat/existing", Inputs: map[string][]byte{"instructions": []byte("x")}}))
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("publishing workflow on an existing branch: %v, want AlreadyExists", err)
	}
	// inspect does not publish, so it may.
	res, err := h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: h.repo, Workflow: "workflows/inspect", Branch: "feat/existing", Inputs: map[string][]byte{"instructions": []byte("look")}}))
	if err != nil {
		t.Fatalf("Run(inspect on existing branch): %v", err)
	}
	ws := h.waitState(res.Msg.Id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second)
	refs, _ := h.staging.ListRefs(ctx, connect.NewRequest(&apiv1.ListRefsRequest{WorkspaceId: ws.Id}))
	got := map[string]string{}
	for _, r := range refs.Msg.Refs {
		got[r.Name] = r.Commit
	}
	if got["refs/heads/feat/existing"] != want {
		t.Fatalf("staging branch must be the existing branch head %s, got %s", want, got["refs/heads/feat/existing"])
	}
	if got["refs/masuda/base"] == want {
		t.Fatalf("base must be the repository's default branch, not the branch itself")
	}
	diff, err := h.staging.Diff(ctx, connect.NewRequest(&apiv1.DiffRequest{WorkspaceId: ws.Id, From: "refs/masuda/base", To: "refs/heads/feat/existing"}))
	if err != nil || !strings.Contains(diff.Msg.Unified, "existing.go") {
		t.Fatalf("the existing changes must be visible as the branch diff: %v %q", err, diff.Msg.GetUnified())
	}
	// Unknown workflow names are InvalidArgument on Run (unified codes).
	_, err = h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: h.repo, Workflow: "workflows/nope", Branch: "feat/z", Inputs: map[string][]byte{"instructions": []byte("x")}}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown workflow: %v, want InvalidArgument", err)
	}
}

// ---------------------------------------------------------------------------
// C-M9: continues reaches the guest
// ---------------------------------------------------------------------------

const continueWorkflow = `version: 1
inputs: [instructions]
start: first
nodes:
  first: {type: agent, role: agents/c-first, inputs: [instructions], outputs: [token], next: second}
  second: {type: agent, role: agents/c-second, continues: agents/c-first, outputs: [recall], next: finish}
  finish: {type: discard, export: [recall], next: end}
`

const continueFirst = `---
name: c-first
description: thinks of a token
tools: Read
outputs: [token]
outcomes:
  done: thought
---
Think of a token.
`

const continueSecond = `---
name: c-second
description: recalls the token
tools: Read
outputs: [recall]
outcomes:
  done: recalled
---
Recall the token.
`

// The task of a node with continues carries the occurrence it continues, and the
// subagent id the main session reported with next_task for that occurrence. A
// resume recreates the VM, so the ids reported before it are no longer offered.
func TestCM9_ContinuesCarriesReportedAgentID(t *testing.T) {
	files := smokeRepo()
	files[".masuda/workflows/continue.yaml"] = continueWorkflow
	files[".masuda/agents/c-first.md"] = continueFirst
	files[".masuda/agents/c-second.md"] = continueSecond
	h := start(t, files)
	ctx := context.Background()
	res, err := h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: h.repo, Workflow: "workflows/continue", Branch: "feat/cont", Inputs: map[string][]byte{"instructions": []byte("x")}}))
	if err != nil {
		t.Fatal(err)
	}
	id := h.waitState(res.Msg.Id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second).Id

	first := h.mcp(id, "next_task", nil)
	if first["role"] != "c-first" || first["continues"] != nil {
		t.Fatalf("first task: %v", first)
	}
	occ1 := first["occurrence"].(string)
	h.mcp(id, "write_output", map[string]any{"occurrence": occ1, "name": "token", "content": "abc"})
	if r := h.mcp(id, "report_result", map[string]any{"occurrence": occ1, "outcome": "done"}); r["accepted"] != true {
		t.Fatalf("report_result: %v", r)
	}

	second := h.mcp(id, "next_task", map[string]any{"agent_id": "agent-a1"})
	if second["role"] != "c-second" {
		t.Fatalf("second task: %v", second)
	}
	cont, _ := second["continues"].(map[string]any)
	if cont["occurrence"] != occ1 || cont["agent_id"] != "agent-a1" {
		t.Fatalf("continues must point at %s with the reported id: %v", occ1, second)
	}
	task, err := os.ReadFile(filepath.Join(h.dataDir, "fake", id, "root", second["task_path"].(string)))
	if err != nil || !strings.Contains(string(task), "## 続き") || !strings.Contains(string(task), occ1) {
		t.Fatalf("task file must name the occurrence it continues: %v\n%s", err, task)
	}

	if _, err := h.ws.Stop(ctx, connect.NewRequest(&apiv1.StopRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ws.Resume(ctx, connect.NewRequest(&apiv1.ResumeRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second)
	// The new main session has no previous task in this VM; an id it passes is not bound.
	again := h.mcp(id, "next_task", map[string]any{"agent_id": "agent-stale"})
	cont, _ = again["continues"].(map[string]any)
	if again["occurrence"] != second["occurrence"] || cont["occurrence"] != occ1 || cont["agent_id"] != nil {
		t.Fatalf("after resume the task continues %s without an id: %v", occ1, again)
	}
	occ2 := again["occurrence"].(string)
	h.mcp(id, "write_output", map[string]any{"occurrence": occ2, "name": "recall", "content": "unknown"})
	if r := h.mcp(id, "report_result", map[string]any{"occurrence": occ2, "outcome": "done"}); r["accepted"] != true {
		t.Fatalf("report_result after resume: %v", r)
	}
	if done := h.mcp(id, "next_task", map[string]any{"agent_id": "agent-b1"}); done["kind"] != "done" {
		t.Fatalf("next_task at the end: %v", done)
	}
}

// ---------------------------------------------------------------------------
// C-M10: .masuda/claude/ and .masuda/claude.local/ in the guest's ~/.claude/
// ---------------------------------------------------------------------------

// The shared and personal directories are merged (the local one wins per path),
// CLAUDE.md is appended after the loop rules, rules and skills land under
// ~/.claude/, and what masuda owns (agents, settings.json) is not copied. The
// merged copy is fixed at Run: a resume places the same content even if the
// working tree changed.
func TestCM10_ClaudeDirPlacedInGuest(t *testing.T) {
	files := smokeRepo()
	files[".masuda/claude/CLAUDE.md"] = "shared rules\n"
	files[".masuda/claude/rules/style.md"] = "style\n"
	files[".masuda/claude/rules/test.md"] = "shared test\n"
	files[".masuda/claude/skills/lint/SKILL.md"] = "---\nname: lint\ndescription: d\n---\nlint\n"
	files[".masuda/claude/skills/lint/scripts/run.sh"] = "echo run\n"
	files[".masuda/claude/agents/evil.md"] = "---\nname: evil\n---\n"
	files[".masuda/claude/settings.json"] = `{"hooks":{}}`
	h := start(t, files)
	// The personal directory is gitignored in a real repository; it is read from the working tree.
	for p, c := range map[string]string{
		".masuda/claude.local/CLAUDE.md":     "local rules\n",
		".masuda/claude.local/rules/test.md": "local test\n",
	} {
		full := filepath.Join(h.repo, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	res, err := h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: h.repo, Workflow: "workflows/smoke", Branch: "feat/claude", Inputs: map[string][]byte{"instructions": []byte("x")}}))
	if err != nil {
		t.Fatal(err)
	}
	id := h.waitState(res.Msg.Id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second).Id
	home := filepath.Join(h.dataDir, "fake", id, "root", "home/ubuntu/.claude")
	check := func() {
		t.Helper()
		claudeMD, _ := os.ReadFile(filepath.Join(home, "CLAUDE.md"))
		s := string(claudeMD)
		loop := strings.Index(s, "next_task")
		heading := strings.Index(s, "# プロジェクトのルール（.masuda/claude）")
		if loop < 0 || heading < loop || !strings.Contains(s[heading:], "ループ規約が優先") || !strings.HasSuffix(s, "local rules\n") || strings.Contains(s, "shared rules") {
			t.Fatalf("~/.claude/CLAUDE.md must be the loop rules followed by the local CLAUDE.md:\n%s", s)
		}
		for p, want := range map[string]string{
			"rules/style.md":             "style\n",
			"rules/test.md":              "local test\n",
			"skills/lint/SKILL.md":       files[".masuda/claude/skills/lint/SKILL.md"],
			"skills/lint/scripts/run.sh": "echo run\n",
		} {
			if b, err := os.ReadFile(filepath.Join(home, p)); err != nil || string(b) != want {
				t.Fatalf("~/.claude/%s = %q %v, want %q", p, b, err, want)
			}
		}
		if _, err := os.Stat(filepath.Join(home, "agents/evil.md")); !os.IsNotExist(err) {
			t.Fatalf(".masuda/claude/agents must not reach the guest: %v", err)
		}
		settings, _ := os.ReadFile(filepath.Join(home, "settings.json"))
		if !strings.Contains(string(settings), "PostToolUse") {
			t.Fatalf(".masuda/claude/settings.json must not replace masuda's settings: %s", settings)
		}
	}
	check()

	if err := os.WriteFile(filepath.Join(h.repo, ".masuda/claude.local/rules/test.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ws.Stop(ctx, connect.NewRequest(&apiv1.StopRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ws.Resume(ctx, connect.NewRequest(&apiv1.ResumeRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second)
	check()
}

// ---------------------------------------------------------------------------
// C-M11: privileged node (fake sandbox)
// ---------------------------------------------------------------------------

const privilegedWorkflow = `version: 1
inputs: [instructions]
start: plan
nodes:
  plan: {type: agent, role: agents/smoke-planner, inputs: [instructions], outputs: [plan], next: approve}
  approve: {type: approval, gate: plan, target: plan, next: {approved: work, rejected: plan}}
  work: {type: agent, role: agents/smoke-implementer, outputs: [commit-message], next: verify}
  verify: {type: privileged, name: itest, next: {done: end, failed: work}}
`

const privilegedSettings = `{"image":"default","egress":[],"secrets":[],"checks":{},"privilegedCommands":{"itest":{"image":"default","command":"echo answer=$(cat answer.txt); test \"$(cat answer.txt)\" = ok"}}}`

// privilegedResult reads records/privileged/<runID>/result.json of the workspace.
func (h *harness) privilegedResult(wsID, runID string) map[string]any {
	h.t.Helper()
	var found string
	_ = filepath.WalkDir(h.dataDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, filepath.Join(wsID, "records", "privileged", runID, "result.json")) {
			found = p
		}
		return nil
	})
	b, err := os.ReadFile(found)
	if err != nil {
		h.t.Fatalf("result.json of privileged run %s: %v", runID, err)
	}
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

// planAndApprove gets the run past the planner and the plan gate.
func (h *harness) planAndApprove(id string) {
	h.t.Helper()
	ctx := context.Background()
	task := h.mcp(id, "next_task", nil)
	occ := task["occurrence"].(string)
	plan := `{"goal":"answer","summary":"answer","steps":[{"number":1,"title":"answer","description":"write answer.txt","tests":[],"files":["answer.txt"]}],"alternatives":[],"risks":[],"expected_byproducts":[],"checks":[]}`
	if r := h.mcp(id, "write_output", map[string]any{"occurrence": occ, "name": "plan", "content": plan}); r["accepted"] != true {
		h.t.Fatalf("plan rejected: %v", r)
	}
	if r := h.mcp(id, "report_result", map[string]any{"occurrence": occ, "outcome": "done"}); r["accepted"] != true {
		h.t.Fatalf("report_result: %v", r)
	}
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE, 10*time.Second)
	open, err := h.gates.ListOpen(ctx, connect.NewRequest(&apiv1.ListOpenGatesRequest{WorkspaceId: id}))
	if err != nil || len(open.Msg.Gates) != 1 {
		h.t.Fatalf("open gates: %v %+v", err, open)
	}
	g := open.Msg.Gates[0]
	if _, err := h.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{WorkspaceId: id, Occurrence: g.Occurrence, Decision: &apiv1.Decision{Outcome: "approved", TargetHash: g.TargetHash}})); err != nil {
		h.t.Fatalf("Decide: %v", err)
	}
}

// workLap does one lap of the agent node: writes answer.txt in the guest
// worktree, reports done, and returns what next_task gives next.
func (h *harness) workLap(id, answer string) map[string]any {
	h.t.Helper()
	task := h.mcp(id, "next_task", nil)
	if task["role"] != "smoke-implementer" {
		h.t.Fatalf("expected the work task: %v", task)
	}
	occ := task["occurrence"].(string)
	h.guestWrite(id, "workspace/answer.txt", answer)
	h.mcp(id, "write_output", map[string]any{"occurrence": occ, "name": "commit-message", "content": "feat: answer"})
	if r := h.mcp(id, "report_result", map[string]any{"occurrence": occ, "outcome": "done"}); r["accepted"] != true {
		h.t.Fatalf("report_result: %v", r)
	}
	return h.mcp(id, "next_task", nil)
}

// A privileged node runs the declared command by name and branches on its exit:
// non-zero goes to failed with the log tail as the feedback of the next task,
// zero goes to done. The record names the occurrence that ran it.
func TestCM11_PrivilegedNodeBranchesOnExit(t *testing.T) {
	files := smokeRepo()
	files[".masuda/settings.json"] = privilegedSettings
	files[".masuda/workflows/verify.yaml"] = privilegedWorkflow
	h := start(t, files)
	ctx := context.Background()
	if _, err := h.config.ApprovePrivilegedCommand(ctx, connect.NewRequest(&apiv1.NameRequest{RepoRoot: h.repo, Name: "itest"})); err != nil {
		t.Fatal(err)
	}
	res, err := h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: h.repo, Workflow: "workflows/verify", Branch: "feat/v", Inputs: map[string][]byte{"instructions": []byte("x")}}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second)
	h.planAndApprove(id)

	retry := h.workLap(id, "bad")
	if retry["role"] != "smoke-implementer" {
		t.Fatalf("a failed privileged command must send the run back to work: %v", retry)
	}
	task, _ := os.ReadFile(filepath.Join(h.dataDir, "fake", id, "root", retry["task_path"].(string)))
	if !strings.Contains(string(task), "前回からの差し戻し") || !strings.Contains(string(task), `特権コマンド"itest"`) || !strings.Contains(string(task), "answer=bad") {
		t.Fatalf("the retry's feedback must carry the log tail:\n%s", task)
	}
	if rec := h.privilegedResult(id, "0001"); rec["occurrence"] == nil || rec["occurrence"] == "" || rec["exit_code"] != float64(1) {
		t.Fatalf("record of the node's run: %v", rec)
	}

	// The retried task is the one next_task already returned; finish it.
	occ := retry["occurrence"].(string)
	h.guestWrite(id, "workspace/answer.txt", "ok")
	h.mcp(id, "write_output", map[string]any{"occurrence": occ, "name": "commit-message", "content": "feat: answer"})
	if r := h.mcp(id, "report_result", map[string]any{"occurrence": occ, "outcome": "done"}); r["accepted"] != true {
		t.Fatalf("report_result: %v", r)
	}
	if done := h.mcp(id, "next_task", nil); done["kind"] != "done" {
		t.Fatalf("a passing privileged command must end the run: %v", done)
	}
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_DONE, 15*time.Second)
	if rec := h.privilegedResult(id, "0002"); rec["exit_code"] != float64(0) {
		t.Fatalf("record of the passing run: %v", rec)
	}
}

// The privileged nodes a workflow can reach are checked before anything is
// created: an unapproved one refuses Run, naming the approval to give.
// If the approval is revoked mid-run, reaching the node stops the run as
// SUSPENDED without recording a result; Resume refuses until it is approved
// again, and then continues from the same node.
func TestCM11_PrivilegedNodeSuspendsUntilApproved(t *testing.T) {
	files := smokeRepo()
	files[".masuda/settings.json"] = privilegedSettings
	files[".masuda/workflows/verify.yaml"] = privilegedWorkflow
	h := start(t, files)
	ctx := context.Background()
	run := func() (*connect.Response[apiv1.Workspace], error) {
		return h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: h.repo, Workflow: "workflows/verify", Branch: "feat/v", Inputs: map[string][]byte{"instructions": []byte("x")}}))
	}
	approve := func(t *testing.T) {
		t.Helper()
		if _, err := h.config.ApprovePrivilegedCommand(ctx, connect.NewRequest(&apiv1.NameRequest{RepoRoot: h.repo, Name: "itest"})); err != nil {
			t.Fatal(err)
		}
	}
	revoke := func() {
		t.Helper()
		if err := os.WriteFile(filepath.Join(h.repo, ".masuda", "settings.local.json"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("未承認の特権ノードに届くワークフローはRunの時点で断られ、承認のコマンドが案内される", func(t *testing.T) {
		_, err := run()
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "masuda privileged-command approve itest") {
			t.Fatalf("Run with an unapproved privileged node: %v", err)
		}
		list, _ := h.ws.List(ctx, connect.NewRequest(&apiv1.ListWorkspacesRequest{}))
		if len(list.Msg.Workspaces) != 0 {
			t.Fatalf("a refused Run must not leave a workspace")
		}
	})

	approve(t)
	res, err := run()
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second)
	h.planAndApprove(id)
	revoke()

	t.Run("実行中に承認を取り消すと特権ノードでSUSPENDEDになり、理由に承認のコマンドが出る", func(t *testing.T) {
		h := h.in(t)
		// next_task reports the engine's error, as it does for any host node that could not run.
		if stopped := h.workLap(id, "ok"); stopped["error"] == nil {
			t.Fatalf("an unapproved privileged command must stop the run: %v", stopped)
		}
		w := h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_SUSPENDED, 15*time.Second)
		if !strings.Contains(w.Reason, "masuda privileged-command approve itest") {
			t.Fatalf("reason must name the approval: %q", w.Reason)
		}
	})

	t.Run("承認が無いままのResumeは断られ、SUSPENDEDのまま残る", func(t *testing.T) {
		_, err := h.ws.Resume(ctx, connect.NewRequest(&apiv1.ResumeRequest{Id: id}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "masuda privileged-command approve itest") {
			t.Fatalf("Resume without the approval: %v", err)
		}
		got, _ := h.ws.Get(ctx, connect.NewRequest(&apiv1.GetWorkspaceRequest{Id: id}))
		if got.Msg.State != apiv1.WorkspaceState_WORKSPACE_STATE_SUSPENDED {
			t.Fatalf("a refused Resume changed the state: %v", got.Msg.State)
		}
	})

	t.Run("承認してからResumeすると同じ特権ノードから進んで終わる", func(t *testing.T) {
		h := h.in(t)
		approve(t)
		if _, err := h.ws.Resume(ctx, connect.NewRequest(&apiv1.ResumeRequest{Id: id})); err != nil {
			t.Fatalf("Resume after approval: %v", err)
		}
		// The resumed run is at the privileged node, not back at the agent: it ends without another task.
		h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_DONE, 15*time.Second)
		// The refused attempt took no run id: the first record is the approved run.
		if rec := h.privilegedResult(id, "0001"); rec["exit_code"] != float64(0) || rec["occurrence"] == nil {
			t.Fatalf("record of the approved run: %v", rec)
		}
	})
}

// ---------------------------------------------------------------------------
// C-M12: SUSPENDED (resumable) vs BLOCKED (a dead end)
// ---------------------------------------------------------------------------

func TestCM12_BootFailureSuspendsAndResumes(t *testing.T) {
	h := start(t, smokeRepo())
	ctx := context.Background()
	// The host cannot record the image build (a file where serve keeps its image
	// records), so the sandbox fails to boot.
	images := filepath.Join(h.dataDir, "images")
	if err := os.WriteFile(images, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: h.repo, Workflow: "workflows/smoke", Branch: "feat/b", Inputs: map[string][]byte{"instructions": []byte("x")}}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id

	t.Run("起動に失敗するとSUSPENDEDになり、理由に起動の失敗が出る", func(t *testing.T) {
		w := h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_SUSPENDED, 15*time.Second)
		if !strings.Contains(w.Reason, "sandbox boot failed") {
			t.Fatalf("reason: %q", w.Reason)
		}
	})

	t.Run("原因を直せばStopを挟まずにResumeでき、最初のタスクから進む", func(t *testing.T) {
		if err := os.Remove(images); err != nil {
			t.Fatal(err)
		}
		if _, err := h.ws.Resume(ctx, connect.NewRequest(&apiv1.ResumeRequest{Id: id})); err != nil {
			t.Fatalf("Resume after a boot failure: %v", err)
		}
		w := h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second)
		if w.Reason != "" {
			t.Fatalf("reason must be cleared: %q", w.Reason)
		}
		if task := h.mcp(id, "next_task", nil); task["role"] != "smoke-planner" {
			t.Fatalf("first task after resume: %v", task)
		}
	})
}

func TestCM12_EngineBlockedCannotBeResumed(t *testing.T) {
	h := start(t, smokeRepo())
	ctx := context.Background()
	res, err := h.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: h.repo, Workflow: "workflows/smoke", Branch: "feat/h", Inputs: map[string][]byte{"instructions": []byte("x")}}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, 15*time.Second)
	occ := h.mcp(id, "next_task", nil)["occurrence"].(string)
	if r := h.mcp(id, "report_concern", map[string]any{"occurrence": occ, "text": "suspicious"}); r["recorded"] != true {
		t.Fatalf("report_concern: %v", r)
	}
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE, 10*time.Second)
	if _, err := h.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{WorkspaceId: id, Occurrence: occ, Decision: &apiv1.Decision{Outcome: "halt"}})); err != nil {
		t.Fatalf("halt: %v", err)
	}
	h.waitState(id, apiv1.WorkspaceState_WORKSPACE_STATE_BLOCKED, 10*time.Second)

	t.Run("engineが記録したBLOCKEDはResumeが断られ、理由にsuspendedとの違いが出る", func(t *testing.T) {
		_, err := h.ws.Resume(ctx, connect.NewRequest(&apiv1.ResumeRequest{Id: id}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "dead end") || !strings.Contains(err.Error(), "suspended") {
			t.Fatalf("Resume of a blocked workspace: %v", err)
		}
	})

	t.Run("StopしてもBLOCKEDのままでResumeできない", func(t *testing.T) {
		st, err := h.ws.Stop(ctx, connect.NewRequest(&apiv1.StopRequest{Id: id}))
		if err != nil || st.Msg.State != apiv1.WorkspaceState_WORKSPACE_STATE_BLOCKED {
			t.Fatalf("Stop of a blocked workspace: %v %v", err, st)
		}
		if _, err := h.ws.Resume(ctx, connect.NewRequest(&apiv1.ResumeRequest{Id: id})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("Resume after Stop of a blocked workspace: %v", err)
		}
	})
}
