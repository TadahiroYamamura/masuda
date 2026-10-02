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
	if err != nil || string(b) != "#!/bin/sh -e\ncd /workspace\ngo test ./...\n" {
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

func TestNodeEgressOutsideApprovalBlocksRun(t *testing.T) {
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
	got := waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_BLOCKED)
	if !strings.Contains(got.Reason, "api.linear.app") {
		t.Fatalf("reason should name the host: %q", got.Reason)
	}
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
