package staging

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

func write(t *testing.T, dir, p, content string) {
	t.Helper()
	full := filepath.Join(dir, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "README.md", "# repo\n")
	write(t, dir, "a.go", "package a\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")
	return dir
}

func newStaging(t *testing.T, repo, branch string) (*Repo, CreateResult) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "staging.git")
	res, err := Create(context.Background(), CreateOptions{RepoRoot: repo, Dir: dir, Branch: branch})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return Open(dir), res
}

// guestSnapshot はゲストがやる「cloneして編集し、WIPをbundleにする」をホストで再現し、
// stagingへ取り込んだWIPのrefを返す。
func guestSnapshot(t *testing.T, s *Repo, branch, occ string, files map[string]string, remove ...string) string {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	bundle := filepath.Join(tmp, "in.bundle")
	if err := s.CreateBundle(ctx, bundle, BranchRef(branch)); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(tmp, "wt")
	git(t, tmp, "clone", "-q", "-b", branch, bundle, wt)
	for p, c := range files {
		write(t, wt, p, c)
	}
	for _, p := range remove {
		_ = os.Remove(filepath.Join(wt, p))
	}
	git(t, wt, "add", "-A")
	git(t, wt, "commit", "-q", "--allow-empty", "-m", "wip")
	git(t, wt, "update-ref", "refs/wip", "HEAD")
	out := filepath.Join(tmp, "out.bundle")
	git(t, wt, "bundle", "create", "-q", out, "refs/wip")
	if _, err := s.ImportBundle(ctx, out, "refs/wip", occ); err != nil {
		t.Fatalf("ImportBundle: %v", err)
	}
	return WIPRef(occ)
}

func TestCreateLeavesRealRepoAlone(t *testing.T) {
	repo := newRepo(t)
	before := git(t, repo, "for-each-ref")
	s, res := newStaging(t, repo, "feat/x")
	if res.Base != "main" || res.BaseCommit != git(t, repo, "rev-parse", "main") {
		t.Fatalf("result %+v", res)
	}
	refs, err := s.ListRefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range refs {
		got[r.Name] = r.Commit
	}
	if got[BaseRef] != res.BaseCommit || got["refs/heads/feat/x"] != res.BaseCommit {
		t.Fatalf("refs %v", got)
	}
	if after := git(t, repo, "for-each-ref"); after != before {
		t.Fatalf("real repo refs changed:\n%s\n---\n%s", before, after)
	}
	if out, _ := exec.Command("git", "-C", s.Dir, "remote").Output(); len(out) != 0 {
		t.Fatalf("staging keeps a remote: %s", out)
	}
}

func TestCreateRefusesExistingBranchAndBadNames(t *testing.T) {
	repo := newRepo(t)
	git(t, repo, "branch", "taken")
	for _, b := range []string{"taken", "-x", "bad..name", ""} {
		_, err := Create(context.Background(), CreateOptions{RepoRoot: repo, Dir: filepath.Join(t.TempDir(), "s.git"), Branch: b})
		if err == nil {
			t.Fatalf("Create with branch %q succeeded", b)
		}
	}
}

func TestCommitTakesOnlyAllowedAndReportsDeviations(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	s, res := newStaging(t, repo, "feat/x")
	wip := guestSnapshot(t, s, "feat/x", "0001",
		map[string]string{"b.go": "package b\n", "notes.txt": "scratch\n", "go.sum": "x\n"}, "a.go")

	// notes.txtは計画外なので何もコミットしない。
	r, err := s.Commit(ctx, CommitOptions{Branch: "feat/x", WIP: wip, Allowed: []string{"b.go", "a.go"}, Byproducts: []string{"go.*"}, Message: "feat: add b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.Deviations, ",") != "notes.txt" || r.Commit != "" {
		t.Fatalf("want deviation notes.txt, got %+v", r)
	}
	if h, _ := s.ResolveCommit(ctx, "refs/heads/feat/x"); h != res.BaseCommit {
		t.Fatalf("branch moved on deviation")
	}

	// 逸脱をByproductsへ回せばAllowedだけが入る（削除も含む）。
	r, err = s.Commit(ctx, CommitOptions{Branch: "feat/x", WIP: wip, Allowed: []string{"b.go", "a.go"}, Byproducts: []string{"go.*", "notes.txt"}, Message: "feat: add b"})
	if err != nil || len(r.Deviations) != 0 || r.Commit == "" {
		t.Fatalf("Commit: %v %+v", err, r)
	}
	c, err := s.GetCommit(ctx, "refs/heads/feat/x")
	if err != nil {
		t.Fatal(err)
	}
	if c.Hash != r.Commit || strings.Join(c.Files, ",") != "a.go,b.go" || c.Message != "feat: add b\n" || len(c.Parents) != 1 || c.Parents[0] != res.BaseCommit {
		t.Fatalf("commit %+v", c)
	}
	tree := git(t, s.Dir, "ls-tree", "-r", "--name-only", r.Commit)
	if tree != "README.md\nb.go" {
		t.Fatalf("tree %q", tree)
	}

	// 変更が無ければブランチは動かない。
	r2, err := s.Commit(ctx, CommitOptions{Branch: "feat/x", WIP: wip, Allowed: []string{"b.go", "a.go"}, Byproducts: []string{"go.*", "notes.txt"}, Message: "again"})
	if err != nil || r2.Commit != r.Commit {
		t.Fatalf("no-op commit: %v %+v", err, r2)
	}
}

func TestDiffAndBlob(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	s, _ := newStaging(t, repo, "feat/x")
	wip := guestSnapshot(t, s, "feat/x", "1", map[string]string{"b.go": "package b\n", "c.go": "package c\n"})
	if _, err := s.Commit(ctx, CommitOptions{Branch: "feat/x", WIP: wip, Allowed: []string{"*.go"}, Message: "m"}); err != nil {
		t.Fatal(err)
	}
	d, err := s.Diff(ctx, BaseRef, "refs/heads/feat/x", nil)
	if err != nil || !strings.Contains(d, "+package b") || !strings.Contains(d, "+package c") {
		t.Fatalf("Diff: %v %q", err, d)
	}
	d, err = s.Diff(ctx, "", "refs/heads/feat/x", []string{"c.go"})
	if err != nil || strings.Contains(d, "package b") || !strings.Contains(d, "+package c") {
		t.Fatalf("Diff with paths/parent: %v %q", err, d)
	}
	rc, err := s.Blob(ctx, "refs/heads/feat/x", "b.go")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	if err := rc.Close(); err != nil || string(b) != "package b\n" {
		t.Fatalf("Blob: %v %q", err, b)
	}
	if _, err := s.Blob(ctx, "refs/heads/feat/x", "nope"); err == nil {
		t.Fatal("Blob of missing path succeeded")
	}
	if _, err := s.GetCommit(ctx, "--output=/tmp/x"); err == nil {
		t.Fatal("option-like rev accepted")
	}
	// ルートコミットは空のtreeとの差分になる。
	c, err := s.GetCommit(ctx, git(t, repo, "rev-parse", "main"))
	if err != nil || strings.Join(c.Files, ",") != "README.md,a.go" {
		t.Fatalf("root commit: %v %+v", err, c)
	}
}

func TestPublishLocalFastForwardsOnly(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	s, _ := newStaging(t, repo, "feat/x")
	wip := guestSnapshot(t, s, "feat/x", "1", map[string]string{"b.go": "package b\n"})
	r, err := s.Commit(ctx, CommitOptions{Branch: "feat/x", WIP: wip, Allowed: []string{"b.go"}, Message: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishLocal(ctx, repo, "feat/x", "deadbeef"); err == nil {
		t.Fatal("published a commit other than the approved one")
	}
	if err := s.PublishLocal(ctx, repo, "feat/x", r.Commit); err != nil {
		t.Fatal(err)
	}
	if got := git(t, repo, "rev-parse", "feat/x"); got != r.Commit {
		t.Fatalf("real branch at %s", got)
	}

	// 実リポジトリ側が分岐していたら何も変えない。
	git(t, repo, "checkout", "-q", "feat/x")
	write(t, repo, "d.go", "package d\n")
	git(t, repo, "add", "d.go")
	git(t, repo, "commit", "-qm", "diverge")
	diverged := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "checkout", "-q", "main")
	wip = guestSnapshot(t, s, "feat/x", "2", map[string]string{"e.go": "package e\n"})
	r, err = s.Commit(ctx, CommitOptions{Branch: "feat/x", WIP: wip, Allowed: []string{"e.go"}, Message: "m2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishLocal(ctx, repo, "feat/x", r.Commit); err == nil {
		t.Fatal("non-fast-forward publish succeeded")
	}
	if got := git(t, repo, "rev-parse", "feat/x"); got != diverged {
		t.Fatalf("diverged branch moved to %s", got)
	}
}

func TestPublishLocalCheckedOutBranchUpdatesWorktree(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	s, _ := newStaging(t, repo, "feat/x")
	git(t, repo, "branch", "feat/x", "main") // 利用者が先に同名ブランチを作ってチェックアウトしている
	git(t, repo, "checkout", "-q", "feat/x")
	wip := guestSnapshot(t, s, "feat/x", "1", map[string]string{"b.go": "package b\n"})
	r, err := s.Commit(ctx, CommitOptions{Branch: "feat/x", WIP: wip, Allowed: []string{"b.go"}, Message: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishLocal(ctx, repo, "feat/x", r.Commit); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(repo, "b.go")); err != nil || string(b) != "package b\n" {
		t.Fatalf("worktree not fast-forwarded: %v %q", err, b)
	}
}

func TestPushRemote(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t, repo, "init", "-q", "--bare", remote)
	git(t, repo, "remote", "add", "origin", remote)
	s, _ := newStaging(t, repo, "feat/x")
	wip := guestSnapshot(t, s, "feat/x", "1", map[string]string{"b.go": "package b\n"})
	r, err := s.Commit(ctx, CommitOptions{Branch: "feat/x", WIP: wip, Allowed: []string{"b.go"}, Message: "m"})
	if err != nil {
		t.Fatal(err)
	}
	url, err := RemoteURL(ctx, repo, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PushRemote(ctx, url, "feat/x", r.Commit); err != nil {
		t.Fatal(err)
	}
	if got := git(t, remote, "rev-parse", "feat/x"); got != r.Commit {
		t.Fatalf("remote at %s", got)
	}
	if git(t, repo, "branch", "--list", "feat/x") != "" {
		t.Fatal("remote push touched the local branch")
	}
}

func TestListBlobs(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	write(t, repo, ".masuda/agents/a.md", "a")
	write(t, repo, ".masuda/agents/b.md", "b")
	write(t, repo, ".masuda/agents/sub/c.md", "c")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "agents")
	s, _ := newStaging(t, repo, "feat/x")
	got, err := s.ListBlobs(ctx, "refs/heads/feat/x", ".masuda/agents")
	if err != nil || strings.Join(got, ",") != ".masuda/agents/a.md,.masuda/agents/b.md" {
		t.Fatalf("ListBlobs: %v %q", err, got)
	}
	got, err = s.ListBlobs(ctx, "refs/heads/feat/x", "nope")
	if err != nil || len(got) != 0 {
		t.Fatalf("ListBlobs(missing): %v %q", err, got)
	}
}

func TestCreateOnExistingBranch(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	git(t, repo, "checkout", "-q", "-b", "feat/x")
	write(t, repo, "x.go", "package x\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "x")
	head := git(t, repo, "rev-parse", "HEAD")
	fork := git(t, repo, "rev-parse", "main")
	// 分岐の後にmainが進んでも、差分はブランチの変更だけになる。
	git(t, repo, "checkout", "-q", "main")
	write(t, repo, "later.go", "package later\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "later")
	git(t, repo, "checkout", "-q", "feat/x") // 今いるブランチがfeat/xでも既定はmain

	dir := filepath.Join(t.TempDir(), "staging.git")
	if _, err := Create(ctx, CreateOptions{RepoRoot: repo, Dir: dir, Branch: "feat/x"}); err == nil {
		t.Fatal("existing branch accepted without AllowExisting")
	}
	res, err := Create(ctx, CreateOptions{RepoRoot: repo, Dir: dir, Branch: "feat/x", AllowExisting: true})
	if err != nil {
		t.Fatal(err)
	}
	s := Open(dir)
	if res.Base != "main" || res.BaseCommit != fork {
		t.Fatalf("base %+v, want main at %s", res, fork)
	}
	if c, _ := s.ResolveCommit(ctx, BranchRef("feat/x")); c != head {
		t.Fatalf("branch %s, want %s", c, head)
	}
	d, err := s.Diff(ctx, BaseRef, BranchRef("feat/x"), nil)
	if err != nil || !strings.Contains(d, "x.go") || strings.Contains(d, "later.go") {
		t.Fatalf("diff: %v\n%s", err, d)
	}
}
