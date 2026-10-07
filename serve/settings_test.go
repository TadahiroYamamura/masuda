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
	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/secrets"
)

func writeRepoFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	p := filepath.Join(repo, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// netWorkflow は1つ目のノードがegressを選ぶワークフロー。
const netWorkflow = `version: 1
inputs: [instructions]
start: echo
nodes:
  echo: {type: agent, role: agents/echo, inputs: [instructions], outputs: [echo], egress: [api.linear.app], next: finish}
  finish: {type: discard, export: [echo], next: end}
`

func TestRunPlacesEnvFilesChecksAndClaudeSettings(t *testing.T) {
	dataDir := t.TempDir()
	_, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.json", `{
		"egress": ["api.linear.app"],
		"secrets": [{"name": "LINEAR_API_KEY", "hosts": ["api.linear.app"]}, {"name": "LEGACY", "mode": "plaintext"}],
		"envFiles": [{"path": "config/.env", "vars": ["LINEAR_API_KEY", "LEGACY", "PUBLIC_URL"]}],
		"checks": {"test": "go test ./..."},
		"claudeSettings": {"theme": "dark", "hooks": {"Notification": [], "PreToolUse": [{"hooks": [{"type": "command", "command": "true"}]}]}}
	}`)
	if err := config.SaveLocal(repo, config.LocalSettings{
		SecretsApproved: []string{"LEGACY"},
		Vars:            map[string]string{"PUBLIC_URL": `http://x/"q"`},
	}); err != nil {
		t.Fatal(err)
	}
	store := secrets.New(dataDir)
	_ = store.Set(repo, "LINEAR_API_KEY", "lin_real")
	_ = store.Set(repo, "LEGACY", "legacy_real")

	res, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/env", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	root := filepath.Join(FakeDir(dataDir), id, "root")

	env, err := os.ReadFile(filepath.Join(root, "workspace/config/.env"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(env)
	if strings.Contains(s, "lin_real") || !strings.Contains(s, `LINEAR_API_KEY="masuda-fake-placeholder-`) ||
		!strings.Contains(s, `LEGACY="legacy_real"`) || !strings.Contains(s, `PUBLIC_URL="http://x/\"q\""`) {
		t.Fatalf(".env:\n%s", s)
	}
	exclude, _ := os.ReadFile(filepath.Join(root, "workspace/.git/info/exclude"))
	if !strings.Contains(string(exclude), "/config/.env") {
		t.Fatalf("env file not excluded from git:\n%s", exclude)
	}

	check := filepath.Join(root, "masuda/checks/test")
	b, err := os.ReadFile(check)
	if err != nil || string(b) != "#!/bin/sh -el\ncd /workspace\ngo test ./...\n" {
		t.Fatalf("check script %q %v", b, err)
	}
	if st, _ := os.Stat(check); st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("check script is not executable: %v", st.Mode())
	}

	var settings map[string]any
	b, _ = os.ReadFile(filepath.Join(root, "home/ubuntu/.claude/settings.json"))
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]any)
	if settings["theme"] != "dark" || hooks["PreToolUse"] == nil || len(hooks["Notification"].([]any)) != 1 {
		t.Fatalf("claude settings: %s", b)
	}
	// 値はリポジトリに書かれない。
	for _, f := range []string{".masuda/settings.json", ".masuda/settings.local.json"} {
		b, _ := os.ReadFile(filepath.Join(repo, f))
		if strings.Contains(string(b), "lin_real") || strings.Contains(string(b), "legacy_real") {
			t.Fatalf("secret value leaked into %s", f)
		}
	}
}

func TestRunRefusesMissingChecksAndValues(t *testing.T) {
	dataDir := t.TempDir()
	_, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.json", `{"secrets": [{"name": "API_KEY", "hosts": ["a.example"]}], "envFiles": [{"path": ".env", "vars": ["PUBLIC"]}], "checks": {}}`)
	_, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/develop", Branch: "feat/c", Inputs: map[string][]byte{"instructions": []byte("x")}}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{"checks.test", "API_KEY", "PUBLIC"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s: %v", want, err)
		}
	}
	list, _ := ws.List(context.Background(), connect.NewRequest(&apiv1.ListWorkspacesRequest{}))
	if len(list.Msg.Workspaces) != 0 {
		t.Fatalf("a refused Run must not leave a workspace")
	}
}

func TestNodeEgressOutsideApprovalSuspendsRun(t *testing.T) {
	dataDir := t.TempDir()
	srv, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.json", `{"egress": ["api.linear.app"]}`)
	writeRepoFile(t, repo, ".masuda/workflows/net.yaml", netWorkflow)
	res, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/net", Branch: "feat/n", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	// 宣言はあるが未承認なので、ノードに入る前（SetPolicyの前）に止まる。
	_, _ = srv.backend.runFor(id).advance()
	got := waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_SUSPENDED)
	if !strings.Contains(got.Reason, "api.linear.app") {
		t.Fatalf("reason should name the host: %q", got.Reason)
	}
	ctx := context.Background()
	t.Run("SUSPENDEDではVMと実行の窓口が残る", func(t *testing.T) {
		if _, err := srv.backend.sandbox.GetSandbox(ctx, connect.NewRequest(&sandboxv1.GetSandboxRequest{Id: id})); err != nil {
			t.Fatalf("sandbox of a suspended workspace: %v", err)
		}
		if srv.backend.runFor(id) == nil {
			t.Fatal("no run for a suspended workspace")
		}
	})
	t.Run("StopするとVMを片付けてSTOPPEDになる", func(t *testing.T) {
		st, err := ws.Stop(ctx, connect.NewRequest(&apiv1.StopRequest{Id: id}))
		if err != nil || st.Msg.State != apiv1.WorkspaceState_WORKSPACE_STATE_STOPPED {
			t.Fatalf("Stop of a suspended workspace: %v %v", err, st)
		}
		if _, err := srv.backend.sandbox.GetSandbox(ctx, connect.NewRequest(&sandboxv1.GetSandboxRequest{Id: id})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("sandbox after Stop: %v", err)
		}
	})
}

func TestNodeEgressWithinApprovalRuns(t *testing.T) {
	dataDir := t.TempDir()
	srv, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.json", `{"egress": ["api.linear.app"]}`)
	writeRepoFile(t, repo, ".masuda/workflows/net.yaml", netWorkflow)
	if err := config.SaveLocal(repo, config.LocalSettings{EgressApproved: []string{"api.linear.app"}}); err != nil {
		t.Fatal(err)
	}
	res, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/net", Branch: "feat/n", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	if _, err := srv.backend.runFor(id).advance(); err != nil {
		t.Fatalf("approved egress must pass: %v", err)
	}
}

func runSmokeGuestAgent(t *testing.T, repo string) string {
	t.Helper()
	dataDir := t.TempDir()
	_, ws := startServe(t, dataDir)
	res, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/agents", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, ws, res.Msg.Id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	b, err := os.ReadFile(filepath.Join(FakeDir(dataDir), res.Msg.Id, "root", "home/ubuntu/.claude/agents/echo.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRunAppliesAgentOverridesToGuestAgents(t *testing.T) {
	t.Run("設定のagentsで上書きした同梱の役は、ゲストの定義にmodelとeffortの行が出る", func(t *testing.T) {
		repo := newSmokeRepo(t)
		writeRepoFile(t, repo, ".masuda/settings.json", `{"agents": {"echo": {"model": "haiku", "effort": "high"}}}`)
		got := runSmokeGuestAgent(t, repo)
		if !strings.Contains(got, "\nmodel: \"haiku\"\n") || !strings.Contains(got, "\neffort: \"high\"\n") {
			t.Fatalf("guest echo.md:\n%s", got)
		}
	})
	t.Run("役定義のfrontmatterにmodelがあっても設定の値が勝ち、設定に無いeffortはfrontmatterのまま", func(t *testing.T) {
		repo := newSmokeRepo(t)
		writeRepoFile(t, repo, ".masuda/agents/echo.md", `---
name: echo
description: 書き返す
tools: Read
model: sonnet
effort: low
inputs: [instructions]
outputs: [echo]
outcomes:
  done: 書き返した
---
本文
`)
		writeRepoFile(t, repo, ".masuda/settings.json", `{"agents": {"echo": {"model": "opus"}}}`)
		got := runSmokeGuestAgent(t, repo)
		if !strings.Contains(got, "\nmodel: \"opus\"\n") || strings.Contains(got, "sonnet") || !strings.Contains(got, "\neffort: \"low\"\n") {
			t.Fatalf("guest echo.md:\n%s", got)
		}
	})
	t.Run("設定のagentsが無ければ同梱の役にmodelもeffortも書かない", func(t *testing.T) {
		got := runSmokeGuestAgent(t, newSmokeRepo(t))
		if strings.Contains(got, "\nmodel:") || strings.Contains(got, "\neffort:") {
			t.Fatalf("guest echo.md:\n%s", got)
		}
	})
}

func TestUnknownAgentOverrideRefusesRunAndShowsInCheck(t *testing.T) {
	dataDir := t.TempDir()
	cl := startClients(t, dataDir, Options{})
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.json", `{"agents": {"echo": {"model": "opus"}, "agents/reviewer": {"model": "opus"}}}`)
	ctx := context.Background()

	t.Run("定義に無い役の名前があればRunはInvalidArgumentで断り、知っている役の名前を並べる", func(t *testing.T) {
		_, err := cl.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/agents", Inputs: smokeInputs}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "agents.agents/reviewer") ||
			!strings.Contains(err.Error(), "reviewer, ") || strings.Contains(err.Error(), "agents.echo ") {
			t.Fatalf("Run: %v", err)
		}
		if entries, _ := os.ReadDir(filepath.Join(dataDir, "workspaces")); len(entries) != 0 {
			t.Fatalf("no workspace must be created: %v", entries)
		}
	})
	t.Run("workflow checkは同じ食い違いをsettings.jsonの問題として出す", func(t *testing.T) {
		check, err := cl.workflows.Check(ctx, connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: repo, Workflow: "workflows/smoke"}))
		if err != nil {
			t.Fatal(err)
		}
		if len(check.Msg.Problems) != 1 || check.Msg.Problems[0].Path != "settings.json" || !strings.Contains(check.Msg.Problems[0].Message, "agents.agents/reviewer") {
			t.Fatalf("problems: %v", check.Msg.Problems)
		}
	})
	t.Run("役の名前がすべて定義にあればworkflow checkは問題を出さない", func(t *testing.T) {
		writeRepoFile(t, repo, ".masuda/settings.json", `{"agents": {"echo": {"model": "opus"}, "reviewer": {"effort": "high"}}}`)
		check, err := cl.workflows.Check(ctx, connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: repo, Workflow: "workflows/smoke"}))
		if err != nil || len(check.Msg.Problems) != 0 {
			t.Fatalf("problems: %v %v", check.Msg.Problems, err)
		}
	})
	t.Run("settings.jsonが壊れたJSONのときworkflow checkはエラーにせず、Pathがsettings.jsonの問題を1件返す", func(t *testing.T) {
		writeRepoFile(t, repo, ".masuda/settings.json", `{`)
		check, err := cl.workflows.Check(ctx, connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: repo, Workflow: "workflows/smoke"}))
		if err != nil {
			t.Fatal(err)
		}
		if len(check.Msg.Problems) != 1 || check.Msg.Problems[0].Path != "settings.json" {
			t.Fatalf("problems: %v", check.Msg.Problems)
		}
	})
	t.Run("定義もsettings.jsonも壊れているときworkflow checkは定義の読み込み失敗とsettings.jsonの問題の両方を返す", func(t *testing.T) {
		writeRepoFile(t, repo, ".masuda/agents/broken.md", ``)
		writeRepoFile(t, repo, ".masuda/settings.json", `{`)
		check, err := cl.workflows.Check(ctx, connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: repo, Workflow: "workflows/smoke"}))
		if err != nil {
			t.Fatal(err)
		}
		var loading, settings int
		for _, p := range check.Msg.Problems {
			if strings.HasPrefix(p.Message, "loading definitions") {
				loading++
			}
			if p.Path == "settings.json" {
				settings++
			}
		}
		if loading != 1 || settings != 1 {
			t.Fatalf("problems: %v", check.Msg.Problems)
		}
	})
	t.Run("定義だけが壊れていてsettings.jsonが正常なときはsettings.jsonの問題を出さない", func(t *testing.T) {
		writeRepoFile(t, repo, ".masuda/settings.json", `{"agents": {"nosuch": {"model": "opus"}}}`)
		check, err := cl.workflows.Check(ctx, connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: repo, Workflow: "workflows/smoke"}))
		if err != nil {
			t.Fatal(err)
		}
		if len(check.Msg.Problems) != 1 || !strings.HasPrefix(check.Msg.Problems[0].Message, "loading definitions") {
			t.Fatalf("problems: %v", check.Msg.Problems)
		}
	})
}

func TestResumeSuspendedWorkspaceRebuildsItsSandbox(t *testing.T) {
	dataDir := t.TempDir()
	srv, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.json", `{"egress": ["api.linear.app"]}`)
	writeRepoFile(t, repo, ".masuda/workflows/net.yaml", netWorkflow)
	ctx := context.Background()
	res, err := ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/net", Branch: "feat/n", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	old := srv.backend.runFor(id)
	_, _ = old.advance()
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_SUSPENDED)
	if err := config.SaveLocal(repo, config.LocalSettings{EgressApproved: []string{"api.linear.app"}}); err != nil {
		t.Fatal(err)
	}
	t.Run("原因を直せばStopを挟まずにResumeでき、残っていた実行の窓口を片付けて作り直す", func(t *testing.T) {
		if _, err := ws.Resume(ctx, connect.NewRequest(&apiv1.ResumeRequest{Id: id})); err != nil {
			t.Fatalf("Resume of a suspended workspace: %v", err)
		}
		waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
		if old.ctx.Err() == nil {
			t.Fatal("the run of the suspended workspace was not stopped")
		}
		if c := srv.backend.runFor(id); c == nil || c == old {
			t.Fatalf("no new run: %v", c)
		}
	})
}
