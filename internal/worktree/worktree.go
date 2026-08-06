// Package worktree wraps the per-checkout operations masuda automates on the
// user's behalf (create, local merge, remove). Per ADR-0005, these are
// local-only operations — none of them ever push to a remote.
//
// Despite the package name, checkouts are `git clone --local` copies, not
// `git worktree add` linked worktrees. Confirmed empirically: a linked
// worktree's .git file points at an absolute host path
// (repoRoot/.git/worktrees/<name>), and that directory's own gitdir file
// points *back* at the worktree's absolute host path — bidirectional
// coupling that breaks the moment the checkout is bind-mounted into a Docker
// container at a different path (/workspace). A --local clone is a fully
// self-contained repository with no such dependency, at the cost of Merge
// needing an explicit fetch to pull the clone's branch back into repoRoot
// (a linked worktree shares repoRoot's object store and refs directly; a
// clone does not).
//
// Checkouts are keyed by workspace ID (see internal/workspace), not by
// branch name directly: roadmap step 7 introduced workspace IDs so that two
// workspaces targeting the same git branch (a full pipeline run and a
// `masuda review start` of that branch, or two parallel attempts at the same
// task) get independent checkout directories and never collide. The branch
// name a workspace targets is still an ordinary git branch and is what
// Merge/Remove operate on in repoRoot; only the on-disk clone path is keyed
// on id.
package worktree

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/perspectives"
)

// Dir returns the on-disk path of the checkout for workspace id, rooted
// under repoRoot.
func Dir(repoRoot, id string) string {
	return filepath.Join(repoRoot, ".masuda", "worktrees", id)
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

func branchExists(repoRoot, branch string) bool {
	_, err := runGit(repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// BranchExists reports whether branch is an existing local branch in
// repoRoot. Exported for `masuda review start`, which — unlike `masuda plan
// start` — must target an existing branch (there's nothing to review on one
// Create would silently create fresh from base).
func BranchExists(repoRoot, branch string) bool {
	return branchExists(repoRoot, branch)
}

// Create makes a new self-contained local clone for branch under workspace
// id, rooted at Dir(repoRoot, id). If the branch doesn't exist yet it's
// created from base inside the clone; if it already exists, base is ignored
// and the clone checks it out directly. --local hardlinks objects when
// repoRoot and dir share a filesystem, so this stays cheap despite being a
// full clone rather than a linked worktree.
func Create(repoRoot, id, branch, base string) (string, error) {
	dir := Dir(repoRoot, id)

	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}

	if branchExists(repoRoot, branch) {
		if _, err := runGit(repoRoot, "clone", "--local", "--branch", branch, repoRoot, dir); err != nil {
			return "", err
		}
		if err := syncMasudaConfig(repoRoot, dir); err != nil {
			return "", err
		}
		return dir, nil
	}

	if _, err := runGit(repoRoot, "clone", "--local", "--branch", base, repoRoot, dir); err != nil {
		return "", err
	}
	if _, err := runGit(dir, "checkout", "-b", branch); err != nil {
		return "", err
	}
	if err := syncMasudaConfig(repoRoot, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// syncMasudaConfig unconditionally overwrites dir's .masuda/settings.json,
// .masuda/reviews/, and (if present) .masuda/.gitignore with repoRoot's
// current working-tree copies. These are ordinarily committed config
// (internal/config, internal/perspectives), in which case `git clone` above
// already reproduced them and this just rewrites identical content -- but a
// repo that hasn't committed .masuda/ yet (e.g. dogfooding masuda solo
// before sharing it with a team) would otherwise get a clone with no config
// at all, since `git clone` only reproduces committed history. Overwriting
// rather than copying-if-missing is deliberate: even once .masuda/ is
// committed, repoRoot may carry local, not-yet-committed edits to these
// files (settings/perspectives being tried out before sharing with the
// team), and those should still reach every new workspace.
//
// Copying .masuda/.gitignore (when repoRoot has one) matters beyond mere
// consistency: masuda's own Commit runs `git add -A` inside the clone for
// phase 4/5 step commits, so without it, the settings.json/reviews/ files
// this function just wrote would show up as ordinary untracked files in the
// clone and get swept into the workspace's own branch history. A repoRoot
// that already commits .masuda/ typically keeps .masuda/.gitignore itself
// committed too, so `git clone` reproduces it there without help; this only
// matters for the same not-yet-committed .masuda/ case as settings.json.
//
// .masuda/Dockerfile is excluded -- docker build always reads it from
// repoRoot directly (cmd/masuda/sandbox.go, update.go), never from a clone.
// .masuda/worktrees/ (sibling workspaces' own clones, including dir itself)
// is excluded to avoid copying it into itself.
func syncMasudaConfig(repoRoot, dir string) error {
	if err := copyFileIfExists(config.SettingsPath(repoRoot), config.SettingsPath(dir)); err != nil {
		return fmt.Errorf("syncing %s: %w", config.SettingsFileName, err)
	}
	if err := copyDirIfExists(perspectives.ReviewsDir(repoRoot), perspectives.ReviewsDir(dir)); err != nil {
		return fmt.Errorf("syncing %s: %w", perspectives.ReviewsDirName, err)
	}
	if err := copyFileIfExists(config.GitignorePath(repoRoot), config.GitignorePath(dir)); err != nil {
		return fmt.Errorf("syncing %s: %w", config.GitignoreFileName, err)
	}
	return nil
}

func copyFileIfExists(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// copyDirIfExists mirrors src onto dst, replacing whatever dst previously
// held (a no-op if src doesn't exist -- e.g. no .masuda/reviews/ at all).
func copyDirIfExists(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDirIfExists(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Merge fast-forwards or merges branch into into, entirely within repoRoot's
// working tree. It refuses to run unless repoRoot already has into checked out —
// masuda never switches the user's own checkout out from under them.
//
// branch lives only in its own clone (Dir(repoRoot, id)) until this fetch
// pulls it into repoRoot — unlike a linked worktree, a clone's branch isn't
// automatically visible to repoRoot's own git commands.
func Merge(repoRoot, id, branch, into string) error {
	current, err := runGit(repoRoot, "branch", "--show-current")
	if err != nil {
		return err
	}
	current = strings.TrimSpace(current)
	if current != into {
		return fmt.Errorf("refusing to merge: %s has %q checked out, not %q (run `git checkout %s` first)", repoRoot, current, into, into)
	}

	cloneDir := Dir(repoRoot, id)
	if _, err := runGit(repoRoot, "fetch", cloneDir, "+"+branch+":"+branch); err != nil {
		return fmt.Errorf("fetching %s from its clone: %w", branch, err)
	}

	_, err = runGit(repoRoot, "merge", "--no-ff", branch, "-m", fmt.Sprintf("merge: %s into %s", branch, into))
	return err
}

// hasStagedChanges reports whether dir's index differs from HEAD, via `git
// diff --cached --quiet`'s exit code (0 = clean, 1 = staged changes, other =
// a real error worth surfacing).
func hasStagedChanges(dir string) (bool, error) {
	cmd := exec.Command("git", "-C", dir, "diff", "--cached", "--quiet")
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

// Commit stages and commits everything currently sitting in workspace id's
// clone, if anything has changed. masuda's phase 4/5 (orchestrator/*.py's
// _compute_diff) deliberately never commits on its own — it diffs staged,
// uncommitted changes throughout implementation and review — so without
// this, the only record of the work is the clone's uncommitted working
// tree, which Remove deletes right after Pull runs. Called from
// finalizeReviewApproval before Pull, so the work becomes part of the
// branch's history before it's brought home.
func Commit(repoRoot, id, message string) error {
	dir := Dir(repoRoot, id)
	if _, err := runGit(dir, "add", "-A"); err != nil {
		return err
	}
	dirty, err := hasStagedChanges(dir)
	if err != nil {
		return err
	}
	if !dirty {
		return nil
	}
	args := append(identityOverride(repoRoot), "commit", "-m", message)
	_, err = runGit(dir, args...)
	return err
}

// identityOverride returns `-c user.name=... -c user.email=...` git global
// options for any of repoRoot's *local* (not global) user.name/user.email
// config -- `git clone` never copies the source's local config, so a commit
// made in the clone would otherwise silently fall back to the global
// identity even when repoRoot deliberately overrides it per-project (e.g. a
// work email distinct from a personal one). Passed as -c rather than written
// into the clone's own config, since it should apply to just this commit.
func identityOverride(repoRoot string) []string {
	var args []string
	for _, key := range []string{"user.name", "user.email"} {
		if v := localConfig(repoRoot, key); v != "" {
			args = append(args, "-c", key+"="+v)
		}
	}
	return args
}

func localConfig(repoRoot, key string) string {
	out, err := exec.Command("git", "-C", repoRoot, "config", "--local", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Pull fast-forwards repoRoot's own branch ref to match its clone's tip —
// the ADR-0023 replacement for Merge in the automatic `review approve` flow.
// Unlike Merge, it never merges into a separate integration branch (that's a
// PR's job in a real GitHub workflow, not masuda's); it just brings the
// clone's commits back into repoRoot, creating branch there if it doesn't
// exist yet. Fast-forward only: a diverged history is rejected by git itself
// (non-zero exit, no partial state), never force-resolved.
func Pull(repoRoot, id, branch string) error {
	cloneDir := Dir(repoRoot, id)

	current, err := runGit(repoRoot, "branch", "--show-current")
	if err != nil {
		return err
	}
	if strings.TrimSpace(current) == branch {
		// git refuses to fetch straight into the ref of the currently
		// checked-out branch, so go through FETCH_HEAD and a working-tree
		// fast-forward merge instead.
		if _, err := runGit(repoRoot, "fetch", cloneDir, branch); err != nil {
			return fmt.Errorf("fetching %s from its clone: %w", branch, err)
		}
		_, err := runGit(repoRoot, "merge", "--ff-only", "FETCH_HEAD")
		return err
	}

	// branch is either absent from repoRoot or checked out nowhere: a plain
	// (non-force) refspec creates it if new, fast-forwards it if not, and
	// git itself rejects a non-fast-forward update.
	_, err = runGit(repoRoot, "fetch", cloneDir, branch+":"+branch)
	return err
}

// Remove deletes the clone directory for workspace id and, if deleteBranch
// is set, the branch ref in repoRoot (a no-op if Merge never fetched it
// there — e.g. an abandoned, never-merged task).
func Remove(repoRoot, id, branch string, deleteBranch bool) error {
	if err := removeLeakedStepTags(repoRoot, id); err != nil {
		return err
	}
	if err := os.RemoveAll(Dir(repoRoot, id)); err != nil {
		return err
	}
	if deleteBranch {
		_, _ = runGit(repoRoot, "branch", "-D", branch)
	}
	return nil
}

// removeLeakedStepTags deletes any masuda-step-<id>-* tags (TDD mode's
// per-step boundary markers, orchestrator/implement_review_graph.py) that
// ended up in repoRoot. These normally only ever exist inside the clone's
// own .git (wiped by the os.RemoveAll above), but Merge/Pull's `git fetch`
// auto-follows tags reachable from newly-fetched commits (neither passes
// --no-tags), so a workspace that was merged/pulled before removal -- the
// normal path through `masuda review approve`, which calls Pull then this
// function -- can leave its step-boundary tags behind in repoRoot. Since
// tags are scoped by workspace id, this is best-effort cleanup rather than a
// correctness requirement: no matches is not an error.
func removeLeakedStepTags(repoRoot, id string) error {
	out, err := runGit(repoRoot, "tag", "--list", fmt.Sprintf("masuda-step-%s-*", id))
	if err != nil {
		return err
	}
	names := strings.Fields(out)
	if len(names) == 0 {
		return nil
	}
	_, err = runGit(repoRoot, append([]string{"tag", "-d"}, names...)...)
	return err
}

// Rebase replays workspace id's clone on top of repoRoot's current tip for
// branch — the inverse direction of Pull, for the case Pull's fast-forward
// rejects: repoRoot moved on (e.g. another workspace already landed) while
// this one was still in flight, so their histories diverged. This is always
// a human-invoked, separate step (never run automatically from `review
// approve`, per ADR-0023's explicit rejection of that) since resolving a
// real divergence -- as opposed to fast-forwarding a clean history -- means
// judging whether two independent changes are still compatible together,
// which isn't masuda's call to make silently.
//
// The clone's `origin` remote is repoRoot's own path (set by `git clone` at
// Create time), so this is a plain local fetch, not a network operation.
//
// On a rebase conflict, this returns git's own error as-is and leaves the
// clone exactly as `git rebase` left it (mid-conflict, nothing auto-resolved
// or aborted) -- the caller is expected to point the human at the clone
// (`masuda workspace info <id>`) to resolve it there.
func Rebase(repoRoot, id, branch string) error {
	dir := Dir(repoRoot, id)
	if _, err := runGit(dir, "fetch", "origin", branch); err != nil {
		return fmt.Errorf("fetching %s from repoRoot: %w", branch, err)
	}
	if _, err := runGit(dir, "rebase", "FETCH_HEAD"); err != nil {
		return fmt.Errorf(
			"rebase onto repoRoot's current %s stopped, likely on a conflict: %w\n\n"+
				"resolve it directly in the clone (see `masuda workspace info %s` for its path), "+
				"then `git rebase --continue` (or `--abort` to give up) and retry",
			branch, err, id,
		)
	}
	return nil
}
