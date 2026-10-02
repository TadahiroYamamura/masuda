package serve

import (
	"context"
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
