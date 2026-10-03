package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
)

const goodPitfall = `{"id":"tz-naive","category":"data","trigger":"日時を保存・比較する変更","question":"タイムゾーン無しの日時が混ざらないか","background":"夏時間の切り替えで集計がずれた"}`

// 落とし穴は定義の写し（records/definitions/）と一緒に写り、注釈を除いてゲストの
// `/masuda/pitfalls.jsonl`へ置かれる。
func TestRunPlacesPitfallsInGuest(t *testing.T) {
	dataDir := t.TempDir()
	_, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/pitfalls.jsonl", "# チームの落とし穴\n"+goodPitfall+"\n\n")

	res, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/pf", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Msg.Id
	waitFor(t, ws, id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)

	guest, err := os.ReadFile(filepath.Join(FakeDir(dataDir), id, "root", "masuda", "pitfalls.jsonl"))
	if err != nil || string(guest) != goodPitfall+"\n" {
		t.Fatalf("guest /masuda/pitfalls.jsonl: %q %v", guest, err)
	}
	host, err := os.ReadFile(filepath.Join(dataDir, "workspaces", id, "records", "definitions", "pitfalls.jsonl"))
	if err != nil || !strings.Contains(string(host), goodPitfall) {
		t.Fatalf("host copy: %q %v", host, err)
	}
}

func TestRunWithoutPitfallsPlacesNothing(t *testing.T) {
	dataDir := t.TempDir()
	_, ws := startServe(t, dataDir)
	repo := newSmokeRepo(t)
	res, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/pf", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, ws, res.Msg.Id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	if _, err := os.Stat(filepath.Join(FakeDir(dataDir), res.Msg.Id, "root", "masuda", "pitfalls.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("without .masuda/pitfalls.jsonl nothing is placed: %v", err)
	}
}

// 不正な落とし穴はワークスペースを作る前に断り、workflow checkでも行ごとに問題として出る。
func TestInvalidPitfallsRefuseRunAndShowInCheck(t *testing.T) {
	dataDir := t.TempDir()
	cl := startClients(t, dataDir, Options{})
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/pitfalls.jsonl", goodPitfall+"\n"+strings.Replace(goodPitfall, `"data"`, `"edge"`, 1)+"\n{}\n")
	ctx := context.Background()

	_, err := cl.ws.Run(ctx, connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/pf", Inputs: smokeInputs}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "pitfalls.jsonl") || !strings.Contains(err.Error(), "line 2:") || !strings.Contains(err.Error(), "line 3:") {
		t.Fatalf("Run with invalid pitfalls: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dataDir, "workspaces")); len(entries) != 0 {
		t.Fatalf("no workspace must be created: %v", entries)
	}

	check, err := cl.workflows.Check(ctx, connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: repo, Workflow: "workflows/smoke"}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range check.Msg.Problems {
		if p.Path == "pitfalls.jsonl" {
			got = append(got, p.Message)
		}
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "line 2: category \"edge\"") || !strings.HasPrefix(got[1], "line 3: ") {
		t.Fatalf("check problems: %v", check.Msg.Problems)
	}
}
