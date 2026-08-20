package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/perspectives"
)

// initTestRepo creates a git repo at a fresh temp dir with one commit on
// branch base, ready for Create to clone from.
func initTestRepo(t *testing.T, base string) string {
	t.Helper()
	dir := t.TempDir()
	runGitT(t, dir, "init", "-q", "-b", base)
	runGitT(t, dir, "config", "user.name", "test")
	runGitT(t, dir, "config", "user.email", "test@example.com")
	writeFile(t, filepath.Join(dir, "README.md"), "init\n")
	runGitT(t, dir, "add", "README.md")
	runGitT(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

func TestCreateCopiesUncommittedSettings(t *testing.T) {
	repoRoot := initTestRepo(t, "main")
	writeFile(t, config.SettingsPath(repoRoot), `{"image": "default"}`)

	dir, err := Create(repoRoot, "ws1", "feature", "main")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got := readFile(t, config.SettingsPath(dir))
	want := `{"image": "default"}`
	if got != want {
		t.Fatalf("clone settings.json = %q, want %q", got, want)
	}
}

func TestCreateOverwritesLocallyModifiedCommittedSettings(t *testing.T) {
	repoRoot := initTestRepo(t, "main")
	writeFile(t, config.SettingsPath(repoRoot), `{"image": "committed"}`)
	runGitT(t, repoRoot, "add", ".masuda/settings.json")
	runGitT(t, repoRoot, "commit", "-q", "-m", "commit settings.json")

	// Uncommitted local edit in repoRoot after the commit above — `git
	// clone` alone would only ever see the committed value.
	writeFile(t, config.SettingsPath(repoRoot), `{"image": "local-wip"}`)

	dir, err := Create(repoRoot, "ws1", "feature", "main")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got := readFile(t, config.SettingsPath(dir))
	want := `{"image": "local-wip"}`
	if got != want {
		t.Fatalf("clone settings.json = %q, want %q (repoRoot's current working tree, not the committed value)", got, want)
	}
}

func TestCreateCopiesMultipleReviewFiles(t *testing.T) {
	repoRoot := initTestRepo(t, "main")
	reviewsDir := perspectives.ReviewsDir(repoRoot)
	writeFile(t, filepath.Join(reviewsDir, "a.md"), "# a\n")
	writeFile(t, filepath.Join(reviewsDir, "b.md"), "# b\n")

	dir, err := Create(repoRoot, "ws1", "feature", "main")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	cloneReviewsDir := perspectives.ReviewsDir(dir)
	if got := readFile(t, filepath.Join(cloneReviewsDir, "a.md")); got != "# a\n" {
		t.Fatalf("a.md = %q, want %q", got, "# a\n")
	}
	if got := readFile(t, filepath.Join(cloneReviewsDir, "b.md")); got != "# b\n" {
		t.Fatalf("b.md = %q, want %q", got, "# b\n")
	}
}

func TestCreateCopiesGitignoreWhenPresent(t *testing.T) {
	repoRoot := initTestRepo(t, "main")
	writeFile(t, config.GitignorePath(repoRoot), "*\n")

	dir, err := Create(repoRoot, "ws1", "feature", "main")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got := readFile(t, config.GitignorePath(dir))
	if got != "*\n" {
		t.Fatalf("clone .masuda/.gitignore = %q, want %q", got, "*\n")
	}
}

func TestCreateSkipsGitignoreWhenAbsent(t *testing.T) {
	repoRoot := initTestRepo(t, "main")
	writeFile(t, config.SettingsPath(repoRoot), `{"image": "masuda-loop"}`)

	dir, err := Create(repoRoot, "ws1", "feature", "main")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := os.Stat(config.GitignorePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("clone's .masuda/.gitignore stat error = %v, want IsNotExist", err)
	}
}

func TestCreateExcludesImagesAndWorktrees(t *testing.T) {
	repoRoot := initTestRepo(t, "main")
	writeFile(t, config.ImageDockerfilePath(repoRoot, "default"), "FROM masuda-loop:latest\n")
	// Simulate another, already-existing sibling workspace living under
	// repoRoot/.masuda/worktrees/ — this must never end up inside the new
	// clone's own .masuda/ directory.
	writeFile(t, filepath.Join(repoRoot, ".masuda", "worktrees", "other-ws", "marker.txt"), "sibling workspace\n")

	dir, err := Create(repoRoot, "ws1", "feature", "main")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := os.Stat(config.ImagesDir(dir)); !os.IsNotExist(err) {
		t.Fatalf("clone's .masuda/images stat error = %v, want IsNotExist", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".masuda", "worktrees")); !os.IsNotExist(err) {
		t.Fatalf("clone's .masuda/worktrees stat error = %v, want IsNotExist", err)
	}
}

func TestCreateIdempotentDoesNotResync(t *testing.T) {
	repoRoot := initTestRepo(t, "main")
	writeFile(t, config.SettingsPath(repoRoot), `{"image": "first"}`)

	dir, err := Create(repoRoot, "ws1", "feature", "main")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// A local edit made directly inside the clone, as masuda's own
	// orchestrator or a human might do mid-workflow.
	writeFile(t, config.SettingsPath(dir), `{"image": "edited-in-clone"}`)

	// repoRoot moves on too, but a second Create for the same id must hit
	// the early-return (dir already exists) path and leave the clone's own
	// edit alone.
	writeFile(t, config.SettingsPath(repoRoot), `{"image": "second"}`)

	dir2, err := Create(repoRoot, "ws1", "feature", "main")
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}
	if dir2 != dir {
		t.Fatalf("second Create() dir = %q, want %q", dir2, dir)
	}

	got := readFile(t, config.SettingsPath(dir))
	want := `{"image": "edited-in-clone"}`
	if got != want {
		t.Fatalf("clone settings.json = %q, want %q (idempotent path must not re-sync)", got, want)
	}
}
