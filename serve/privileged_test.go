package serve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/privileged"
)

func TestPrivilegedCommandFailuresAndApproval(t *testing.T) {
	dataDir := t.TempDir()
	srv, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.json", `{"checks": {"test": "true"}, "privilegedCommands": {
		"fail":  {"image": "default", "command": "echo to-log; echo err >&2; touch /workspace/partial.txt; exit 3", "outputs": ["partial.txt", "missing/**"]},
		"other": {"image": "default", "command": "true"},
		"bad":   {"image": "nope", "command": "", "inputs": ["../x"]}
	}}`)
	cs := &configService{backend: srv.backend}
	ctx := context.Background()
	approve := func(name string) error {
		_, err := cs.ApprovePrivilegedCommand(ctx, connect.NewRequest(&apiv1.NameRequest{RepoRoot: repo, Name: name}))
		return err
	}
	if err := approve("fail"); err != nil {
		t.Fatal(err)
	}
	if err := approve("bad"); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "command is empty") {
		t.Fatalf("approving a malformed declaration: %v", err)
	}

	res, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/p", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	c := srv.backend.runFor(id)

	if _, err := c.RunPrivilegedCommand(ctx, "other"); err == nil || !strings.Contains(err.Error(), "masuda privileged-command approve other") {
		t.Fatalf("unapproved command: %v", err)
	}

	out, err := c.RunPrivilegedCommand(ctx, "fail")
	if err != nil {
		t.Fatal(err)
	}
	r := out.(*privileged.Result)
	if r.ExitCode != 3 || r.TimedOut || !strings.Contains(r.Log, "to-log") || !strings.Contains(r.Log, "err") {
		t.Fatalf("result %+v", r)
	}
	if len(r.Outputs) != 1 || r.Outputs[0] != "partial.txt" || !strings.Contains(r.OutputsError, `"missing/**"`) {
		t.Fatalf("outputs %v / %q", r.Outputs, r.OutputsError)
	}
	w, _ := srv.backend.store.Get(id)
	host := filepath.Join(privilegedRecordsDir(w), "0001")
	for _, f := range []string{"exit-code", "log", "result.json", "outputs/partial.txt"} {
		if _, err := os.Stat(filepath.Join(host, f)); err != nil {
			t.Errorf("host record %s: %v", f, err)
		}
	}
	var rec map[string]any
	if b, err := os.ReadFile(filepath.Join(host, "result.json")); err != nil || json.Unmarshal(b, &rec) != nil {
		t.Fatalf("result.json: %v", err)
	}
	if _, ok := rec["occurrence"]; ok || rec["name"] != "fail" {
		t.Fatalf("MCPから呼んだ記録には出現を書かない: %v", rec)
	}
	guestCode, _ := os.ReadFile(filepath.Join(FakeDir(dataDir), id, "root", "masuda/privileged/0001/exit-code"))
	if string(guestCode) != "3\n" {
		t.Fatalf("guest exit-code %q", guestCode)
	}

	// 承認の後に宣言が変わったら（ここでは承認の記録を古いハッシュに差し替えて模す）断る。
	local, _ := config.LoadLocal(repo)
	local.PrivilegedCommandsApproved["fail"] = config.PrivilegedCommandApproval{DeclHash: "old"}
	if err := config.SaveLocal(repo, local); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunPrivilegedCommand(ctx, "fail"); err == nil || !strings.Contains(err.Error(), "changed since it was approved") {
		t.Fatalf("stale approval: %v", err)
	}
}

// 特権コマンドの宣言は定義の写しからゲストの`/masuda/privileged-commands.json`へ置かれる。承認の状態は
// 写さず、timeoutSecondsは既定を埋める。再開では、作業ツリーの宣言が変わっていても実行開始時の写しから置き直す。
func TestRunPlacesPrivilegedCommandsInGuest(t *testing.T) {
	dataDir := t.TempDir()
	srv, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.json", `{"checks": {"test": "true"}, "privilegedCommands": {
		"itest": {"description": "DBを立てて結合テストを流す", "image": "default", "command": "make itest", "inputs": ["build/**"], "outputs": ["report.xml"], "timeoutSeconds": 600},
		"lint":  {"image": "default", "command": "make lint"}
	}}`)
	cs := &configService{backend: srv.backend}
	ctx := context.Background()
	if _, err := cs.ApprovePrivilegedCommand(ctx, connect.NewRequest(&apiv1.NameRequest{RepoRoot: repo, Name: "itest"})); err != nil {
		t.Fatal(err)
	}
	const want = `{
  "itest": {
    "description": "DBを立てて結合テストを流す",
    "command": "make itest",
    "image": "default",
    "inputs": [
      "build/**"
    ],
    "outputs": [
      "report.xml"
    ],
    "timeoutSeconds": 600
  },
  "lint": {
    "command": "make lint",
    "image": "default",
    "inputs": [],
    "outputs": [],
    "timeoutSeconds": 3600
  }
}
`
	res, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/pc", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	guestFile := filepath.Join(FakeDir(dataDir), id, "root", "masuda", "privileged-commands.json")
	t.Run("宣言を名前ごとに置き、承認の状態は含めず、timeoutSecondsの省略は既定の3600で埋める", func(t *testing.T) {
		got, err := os.ReadFile(guestFile)
		if err != nil || string(got) != want {
			t.Fatalf("guest file %v:\n%s", err, got)
		}
	})
	t.Run("再開では作業ツリーの宣言が変わっていても実行開始時の写しから置き直す", func(t *testing.T) {
		writeRepoFile(t, repo, ".masuda/settings.json", `{"checks": {"test": "true"}, "privilegedCommands": {"other": {"image": "default", "command": "true"}}}`)
		stopAndResume(t, ws, id)
		waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
		got, err := os.ReadFile(guestFile)
		if err != nil || string(got) != want {
			t.Fatalf("guest file after resume %v:\n%s", err, got)
		}
	})
}

func TestRunWithoutPrivilegedCommandsPlacesNothing(t *testing.T) {
	dataDir := t.TempDir()
	_, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	res, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/pc", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, ws, res.Msg.Id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	t.Run("宣言が無ければ/masuda/privileged-commands.jsonを置かない", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(FakeDir(dataDir), res.Msg.Id, "root", "masuda", "privileged-commands.json")); !os.IsNotExist(err) {
			t.Fatalf("placed: %v", err)
		}
	})
}

// precheckWorkflow は特権ノードを、自身と呼び出す部品のワークフローの両方に持つ。
const precheckWorkflow = `version: 1
inputs: [instructions]
start: a
nodes:
  a: {type: privileged, name: ok, next: {done: b, failed: end}}
  b: {type: workflow, workflow: workflows/precheck-part, next: {done: end}}
`

const precheckPart = `version: 1
start: x
nodes:
  x: {type: privileged, name: undeclared, next: {done: y, failed: end}}
  y: {type: privileged, name: unapproved, next: {done: z, failed: end}}
  z: {type: privileged, name: changed, next: {done: end, failed: end}}
`

func TestRunAndResumeCheckPrivilegedNodesFirst(t *testing.T) {
	dataDir := t.TempDir()
	srv, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/workflows/precheck.yaml", precheckWorkflow)
	writeRepoFile(t, repo, ".masuda/workflows/precheck-part.yaml", precheckPart)
	settings := func(changedCommand string) string {
		return `{"privilegedCommands": {
			"ok":         {"image": "default", "command": "true"},
			"unapproved": {"image": "default", "command": "true"},
			"changed":    {"image": "default", "command": "` + changedCommand + `"}
		}}`
	}
	writeRepoFile(t, repo, ".masuda/settings.json", settings("true"))
	cs := &configService{backend: srv.backend}
	ctx := context.Background()
	for _, name := range []string{"ok", "changed"} {
		if _, err := cs.ApprovePrivilegedCommand(ctx, connect.NewRequest(&apiv1.NameRequest{RepoRoot: repo, Name: name})); err != nil {
			t.Fatal(err)
		}
	}
	writeRepoFile(t, repo, ".masuda/settings.json", settings("false"))
	run := func() error {
		_, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/precheck", Branch: "feat/pre", Inputs: smokeInputs}))
		return err
	}

	t.Run("届く特権ノードの未宣言・未承認・承認後の変更をまとめて断り、ワークスペースを作らない", func(t *testing.T) {
		err := run()
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("Run: %v", err)
		}
		for _, want := range []string{
			`"undeclared" is not declared`,
			"masuda privileged-command approve unapproved",
			`"changed" changed since it was approved`,
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("理由に%qが無い: %v", want, err)
			}
		}
		if strings.Contains(err.Error(), `command "ok"`) {
			t.Errorf("承認済みのokを挙げている: %v", err)
		}
		list, _ := ws.List(ctx, connect.NewRequest(&apiv1.ListWorkspacesRequest{}))
		if len(list.Msg.Workspaces) != 0 {
			t.Fatalf("断ったRunがワークスペースを残した")
		}
	})

	t.Run("再開でも作業ツリーの承認を読み直し、取り消されていれば断ってSUSPENDEDのままにする", func(t *testing.T) {
		writeRepoFile(t, repo, ".masuda/settings.json", `{"privilegedCommands": {"ok": {"image": "default", "command": "true"}}}`)
		writeRepoFile(t, repo, ".masuda/workflows/precheck.yaml", "version: 1\ninputs: [instructions]\nstart: a\nnodes:\n  a: {type: privileged, name: ok, next: {done: end, failed: end}}\n")
		res, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/precheck", Branch: "feat/pre2", Inputs: smokeInputs}))
		if err != nil {
			t.Fatal(err)
		}
		id := res.Msg.Id
		waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
		// 承認を取り消してからengineを進め、ノードに着いて止まる（SUSPENDED）ようにする。
		if err := config.SaveLocal(repo, config.LocalSettings{}); err != nil {
			t.Fatal(err)
		}
		_, _ = srv.backend.runFor(id).advance()
		waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_SUSPENDED)
		_, err = ws.Resume(ctx, connect.NewRequest(&apiv1.ResumeRequest{Id: id}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "masuda privileged-command approve ok") {
			t.Fatalf("Resume: %v", err)
		}
		got, _ := ws.Get(ctx, connect.NewRequest(&apiv1.GetWorkspaceRequest{Id: id}))
		if got.Msg.State != apiv1.WorkspaceState_WORKSPACE_STATE_SUSPENDED {
			t.Fatalf("断ったResumeが状態を変えた: %v", got.Msg.State)
		}
		if srv.backend.runFor(id) == nil {
			t.Fatalf("断ったResumeがVMと実行の窓口を片付けた（chatで中を見られなくなる）")
		}
	})
}
