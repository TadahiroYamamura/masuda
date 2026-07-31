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
		return dir, nil
	}

	if _, err := runGit(repoRoot, "clone", "--local", "--branch", base, repoRoot, dir); err != nil {
		return "", err
	}
	if _, err := runGit(dir, "checkout", "-b", branch); err != nil {
		return "", err
	}
	return dir, nil
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
	if err := os.RemoveAll(Dir(repoRoot, id)); err != nil {
		return err
	}
	if deleteBranch {
		_, _ = runGit(repoRoot, "branch", "-D", branch)
	}
	return nil
}
