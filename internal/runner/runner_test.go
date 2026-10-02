package runner

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda-engine/engine"

	"github.com/TadahiroYamamura/masuda/internal/staging"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

func TestFileStoreApplyAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.json")
	s, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := s.Apply([]engine.Op{{Kind: engine.OpCheck, Key: "r/a"}, {Kind: engine.OpPut, Key: "r/a", Value: []byte("1")}})
	if err != nil || !ok {
		t.Fatalf("create: %v %v", ok, err)
	}
	// 既にあるキーに「無いこと」を条件にした書き込みは何も書かない。
	ok, err = s.Apply([]engine.Op{{Kind: engine.OpCheck, Key: "r/a"}, {Kind: engine.OpPut, Key: "r/b", Value: []byte("2")}})
	if err != nil || ok {
		t.Fatalf("second create: %v %v", ok, err)
	}
	if _, found, _ := s.Get("r/b"); found {
		t.Fatal("failed Apply must not write")
	}
	s2, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	kvs, err := s2.List("r/")
	if err != nil || len(kvs) != 1 || string(kvs[0].Value) != "1" {
		t.Fatalf("reopened: %v %+v", err, kvs)
	}
}

func TestValidate(t *testing.T) {
	schemas := map[string][]byte{
		"msg": []byte(`{"type":"string","minLength":3}`),
		"obj": []byte(`{"type":"object","required":["a"]}`),
	}
	if p := Validate(schemas, "msg", []byte("feat: x")); p != nil {
		t.Fatalf("string schema takes the raw content: %v", p)
	}
	if p := Validate(schemas, "obj", []byte(`{"b":1}`)); len(p) == 0 {
		t.Fatal("missing required property accepted")
	}
	if p := Validate(schemas, "obj", []byte(`not json`)); len(p) == 0 {
		t.Fatal("non-JSON accepted")
	}
	if p := Validate(schemas, "free", []byte("  \n")); len(p) == 0 {
		t.Fatal("empty data without a schema accepted")
	}
}

func TestImportFindingsAndSupersede(t *testing.T) {
	ws, err := workspace.NewStore(t.TempDir()).Create(workspace.Meta{RepoRoot: "/r", Branch: "b", Workflow: "workflows/w", State: workspace.StateRunning})
	if err != nil {
		t.Fatal(err)
	}
	r := New(Options{Workspace: ws})
	findings := `[{"id":"security-0000004-1","file":"a.go","line":3,"severity":"高","autofix":false,"message":"m","suggestion":"s"},
{"id":"cross-cutting-0000009-2","file":"b.go","line":1,"severity":"低","autofix":false,"message":"n"}]`
	if err := r.PutData(context.Background(), "", engine.DataRef{Name: "findings", Occurrence: "0000009"}, []byte(findings)); err != nil {
		t.Fatal(err)
	}
	for range 2 { // 開き直しても二重に取り込まない
		if err := r.importFindings("c1"); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := ws.Comments("c1")
	if err != nil || len(cs) != 2 {
		t.Fatalf("comments: %v %+v", err, cs)
	}
	if cs[0].Author != "security" || cs[0].Severity != "高" || cs[0].Path != "a.go" || cs[0].Line != 3 || !strings.Contains(cs[0].Body, "s") {
		t.Fatalf("first: %+v", cs[0])
	}
	if cs[1].Author != "cross-cutting" {
		t.Fatalf("second: %+v", cs[1])
	}

	if err := ws.AddGate(&workspace.GateRecord{Occurrence: "0000010", Gate: "deviation"}); err != nil {
		t.Fatal(err)
	}
	r.Log(engine.Event{Kind: "decision", Occurrence: "0000010", Outcome: "superseded", Detail: "deviation: triageで無効になった"})
	open, err := ws.OpenGates()
	if err != nil || len(open) != 0 {
		t.Fatalf("superseded gate still open: %v %+v", err, open)
	}
}

// step-diffのゲートは、承認対象を作った作業ツリーのスナップショットをstaging_commitにして
// refs/masuda/gates/<occ>に留め、findingsをそのコミットへのコメントとして取り込む。
func TestOpenGateStepDiffPinsSnapshot(t *testing.T) {
	ws, err := workspace.NewStore(t.TempDir()).Create(workspace.Meta{RepoRoot: "/r", Branch: "b", Workflow: "workflows/w", State: workspace.StateRunning})
	if err != nil {
		t.Fatal(err)
	}
	dir := ws.StagingDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	tree := git("mktree")
	snap := git("commit-tree", tree, "-m", "masuda snapshot worktree")
	r := New(Options{Workspace: ws})
	r.stepDiffs = map[string]string{"0000012": snap}
	if err := r.PutData(context.Background(), "", engine.DataRef{Name: "findings", Occurrence: "0000011"}, []byte(`[{"id":"security-0000011-1","file":"a.go","line":3,"severity":"高","message":"m"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := r.OpenGate(context.Background(), engine.GateRequest{Occurrence: "0000012", Gate: "interim", Target: string(engine.DiffFromHead), TargetHash: "h", Subject: []byte("d")}); err != nil {
		t.Fatal(err)
	}
	if got := git("rev-parse", staging.GateRef("0000012")); got != snap {
		t.Fatalf("gate ref = %s, want %s", got, snap)
	}
	gates, err := ws.OpenGates()
	if err != nil || len(gates) != 1 || gates[0].StagingCommit != snap {
		t.Fatalf("gates: %v %+v", err, gates)
	}
	if cs, err := ws.Comments(snap); err != nil || len(cs) != 1 || cs[0].Line != 3 {
		t.Fatalf("comments: %v %+v", err, cs)
	}
}
