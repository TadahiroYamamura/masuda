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
	plan := `{"summary":"add b","steps":[{"number":1,"description":"add b.go","files":["b.go"]}],"expected_byproducts":[]}`
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
