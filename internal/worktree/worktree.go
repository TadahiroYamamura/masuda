// Package worktree wraps the git worktree operations masuda automates on the
// user's behalf (create, local merge, remove). Per ADR-0005, these are local-only
// operations — none of them ever push to a remote.
package worktree

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Dir returns the on-disk path of the worktree for branch, rooted under repoRoot.
func Dir(repoRoot, branch string) string {
	return filepath.Join(repoRoot, ".masuda", "worktrees", branch)
}

func runGit(repoRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
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

// Create makes a new worktree for branch, rooted at Dir(repoRoot, branch). If the
// branch doesn't exist yet it's created from base; if it already exists, base is
// ignored and the worktree is checked out at the branch's current tip.
func Create(repoRoot, branch, base string) (string, error) {
	dir := Dir(repoRoot, branch)

	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}

	if branchExists(repoRoot, branch) {
		if _, err := runGit(repoRoot, "worktree", "add", dir, branch); err != nil {
			return "", err
		}
		return dir, nil
	}

	if _, err := runGit(repoRoot, "worktree", "add", "-b", branch, dir, base); err != nil {
		return "", err
	}
	return dir, nil
}

// Merge fast-forwards or merges branch into into, entirely within repoRoot's
// working tree. It refuses to run unless repoRoot already has into checked out —
// masuda never switches the user's own checkout out from under them.
func Merge(repoRoot, branch, into string) error {
	current, err := runGit(repoRoot, "branch", "--show-current")
	if err != nil {
		return err
	}
	current = strings.TrimSpace(current)
	if current != into {
		return fmt.Errorf("refusing to merge: %s has %q checked out, not %q (run `git checkout %s` first)", repoRoot, current, into, into)
	}

	_, err = runGit(repoRoot, "merge", "--no-ff", branch, "-m", fmt.Sprintf("merge: %s into %s", branch, into))
	return err
}

// Remove deletes the worktree for branch and, if deleteBranch is set, the branch
// ref itself. It's the caller's responsibility to have already merged or otherwise
// disposed of the branch's content before deleting the branch.
func Remove(repoRoot, branch string, deleteBranch bool) error {
	dir := Dir(repoRoot, branch)
	if _, err := runGit(repoRoot, "worktree", "remove", dir, "--force"); err != nil {
		return err
	}
	if deleteBranch {
		if _, err := runGit(repoRoot, "branch", "-D", branch); err != nil {
			return err
		}
	}
	return nil
}
