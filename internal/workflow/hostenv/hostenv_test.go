package hostenv

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/workflow/data"
	"github.com/TadahiroYamamura/masuda/internal/workflow/engine"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T) *Env {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "config", "user.name", "t")
	git(t, repo, "config", "user.email", "t@example.com")
	write(t, filepath.Join(repo, "README.md"), "x\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "init")
	e := &Env{
		WorkspaceID: "ws1", RepoRoot: repo, Worktree: repo, StateDir: t.TempDir(),
		BaseRef: "main", Branch: "main", Store: engine.NewMemStore(), ExportDir: filepath.Join(t.TempDir(), "exports"),
	}
	plan := `{"summary": "方針", "steps": [
	  {"description": "first step", "files": [{"path": "a.go", "description": ""}]},
	  {"description": "second step", "files": [{"path": "b.go", "description": ""}]}
	], "expected_byproducts": ["*.tmp"]}`
	if _, err := e.data().Write("0000002", "plan", []byte(plan)); err != nil {
		t.Fatal(err)
	}
	if err := e.OutputsDone(engine.OutputContext{Occurrence: "0000002"}, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestStepsCommitTagAndResume(t *testing.T) {
	e := newEnv(t)
	items, err := e.Items("steps", "", "")
	if err != nil || len(items) != 2 {
		t.Fatalf("items = %v %v", items, err)
	}
	write(t, filepath.Join(e.Worktree, "a.go"), "package a\n")
	write(t, filepath.Join(e.Worktree, "junk.tmp"), "byproduct")
	if dev, _, err := e.Deviations("step", items[0].Path); err != nil || len(dev) != 0 {
		t.Fatalf("deviations = %v %v, want none (a.go planned, junk.tmp a byproduct)", dev, err)
	}
	if _, err := e.data().Write("0000009", "commit-message", []byte("feat: a を追加")); err != nil {
		t.Fatal(err)
	}
	if err := e.Commit("step", items[0].Path, nil); err != nil {
		t.Fatal(err)
	}
	if msg := git(t, e.Worktree, "log", "-1", "--format=%s"); strings.TrimSpace(msg) != "feat: a を追加" {
		t.Fatalf("commit message = %q", msg)
	}
	if done, _ := e.StepDone(items[0].Key); !done {
		t.Fatal("step 1 not recorded as done")
	}
	if done, _ := e.StepDone(items[1].Key); done {
		t.Fatal("step 2 recorded as done")
	}
	// The same commit-message is not reused: step 2 falls back to its description.
	write(t, filepath.Join(e.Worktree, "b.go"), "package b\n")
	if err := e.Commit("step", items[1].Path, nil); err != nil {
		t.Fatal(err)
	}
	if msg := git(t, e.Worktree, "log", "-1", "--format=%s"); strings.TrimSpace(msg) != "second step" {
		t.Fatalf("second commit message = %q", msg)
	}
}

func TestDeviationsAndApprovedCommit(t *testing.T) {
	e := newEnv(t)
	items, _ := e.Items("steps", "", "")
	write(t, filepath.Join(e.Worktree, "a.go"), "package a\n")
	write(t, filepath.Join(e.Worktree, "extra.go"), "package extra\n")
	dev, hash, err := e.Deviations("step", items[0].Path)
	if err != nil || strings.Join(dev, ",") != "extra.go" || hash == "" {
		t.Fatalf("deviations = %v %q %v", dev, hash, err)
	}
	if err := e.Commit("step", items[0].Path, dev); err != nil {
		t.Fatal(err)
	}
	if left, _, _ := e.Deviations("plan", ""); len(left) != 0 {
		t.Fatalf("after approval, deviations = %v", left)
	}
	if files := git(t, e.Worktree, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(files, "extra.go") {
		t.Fatalf("approved deviation not committed: %s", files)
	}
}

func TestSnapshotChangedSince(t *testing.T) {
	e := newEnv(t)
	write(t, filepath.Join(e.Worktree, "pre.go"), "existing change")
	snap, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if files, _, _ := e.ChangedSince(snap); len(files) != 0 {
		t.Fatalf("changed = %v right after the snapshot", files)
	}
	write(t, filepath.Join(e.Worktree, "README.md"), "edited by a read-only agent\n")
	files, hash, err := e.ChangedSince(snap)
	if err != nil || strings.Join(files, ",") != "README.md" || hash == "" {
		t.Fatalf("changed = %v %q %v", files, hash, err)
	}
}

func TestFindingsImportFixAndStatus(t *testing.T) {
	e := newEnv(t)
	fs := `[{"file": "a.go", "startLine": 1, "endLine": 1, "severity": "低", "description": "typo", "autofix": true},
	        {"file": "a.go", "startLine": 3, "endLine": 4, "severity": "高", "description": "design", "autofix": false}]`
	if _, err := e.data().Write("0000012", "findings", []byte(fs)); err != nil {
		t.Fatal(err)
	}
	ctx := engine.OutputContext{Occurrence: "0000012", Frame: "0000010.0", Workflow: "workflows/review/perspective-review", Node: "review",
		Inputs: map[string]string{"perspective": "/x/wf/items/perspective-naming.md"}, AgentID: "a123"}
	if err := e.OutputsDone(ctx, []string{"findings"}); err != nil {
		t.Fatal(err)
	}
	items, err := e.Items("findings", "", "0000011")
	if err != nil || len(items) != 1 {
		t.Fatalf("findings to fix = %v %v, want the one auto-fixable", items, err)
	}
	var rec data.Record
	b, _ := os.ReadFile(items[0].Path)
	_ = json.Unmarshal(b, &rec)
	if rec.Perspective != "naming" || rec.AgentID != "a123" {
		t.Fatalf("record = %+v", rec)
	}
	if err := e.ItemFinished("findings", items[0].Key, "unresolved"); err != nil {
		t.Fatal(err)
	}
	if again, _ := e.Items("findings", "", "0000011"); len(again) != 0 {
		t.Fatalf("an unresolved finding is offered again: %v", again)
	}
}

func TestExportCopiesRegularFilesOnly(t *testing.T) {
	e := newEnv(t)
	if _, err := e.data().Write("0000020", "report", []byte("# report")); err != nil {
		t.Fatal(err)
	}
	e.Log(engine.Event{Kind: "enter", Node: "x"})
	secret := filepath.Join(t.TempDir(), "secret")
	write(t, secret, "do not copy")
	if err := os.Symlink(secret, e.data().Path("0000021", "notes")); err == nil {
		t.Fatal("expected the directory to be missing so the symlink fails")
	}
	if err := os.MkdirAll(filepath.Dir(e.data().Path("0000021", "notes")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, e.data().Path("0000021", "notes")); err != nil {
		t.Fatal(err)
	}
	if err := e.Export([]string{"report", "notes"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.ExportDir, "report.md")); err != nil {
		t.Fatal("report not exported")
	}
	if _, err := os.Stat(filepath.Join(e.ExportDir, "execution-log.jsonl")); err != nil {
		t.Fatal("execution log not exported")
	}
	if _, err := os.Stat(filepath.Join(e.ExportDir, "notes.md")); err == nil {
		t.Fatal("a symlink in the state directory was followed")
	}
}

func TestTargetHashTracksContent(t *testing.T) {
	e := newEnv(t)
	h1, err := e.TargetHash("diff")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(e.Worktree, "a.go"), "package a\n")
	h2, _ := e.TargetHash("diff")
	if h1 == h2 {
		t.Fatal("diff hash did not change with the worktree")
	}
	if p, err := e.TargetHash("plan"); err != nil || len(p) != 64 {
		t.Fatalf("plan hash = %q %v", p, err)
	}
}
