package worktree

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// The functions below work on a clone directory directly. They serve the
// workflow engine's commit node and change measurements (ADR-0058,
// ADR-0081), which only ever deal with one workspace's clone.

// ChangedFiles lists every path that differs from HEAD in dir: modified,
// added, deleted or untracked, excluding what .gitignore ignores. Both
// sides of a rename are listed.
func ChangedFiles(dir string) ([]string, error) {
	out, err := runGit(dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		seen[f[3:]] = true
		if f[0] == 'R' || f[0] == 'C' {
			// -z puts the rename source in the next field.
			if i+1 < len(fields) {
				seen[fields[i+1]] = true
				i++
			}
		}
	}
	files := make([]string, 0, len(seen))
	for p := range seen {
		files = append(files, p)
	}
	sort.Strings(files)
	return files, nil
}

// Digests returns a content hash per changed file ("deleted" for a file
// that no longer exists), so two measurements can be compared without
// touching git's index.
func Digests(dir string) (map[string]string, error) {
	files, err := ChangedFiles(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(files))
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f))
		switch {
		case os.IsNotExist(err):
			out[f] = "deleted"
		case err != nil:
			return nil, err
		default:
			sum := sha256.Sum256(b)
			out[f] = hex.EncodeToString(sum[:])
		}
	}
	return out, nil
}

// HashDigests identifies a set of file states, for binding an approval to
// exactly the changes a human looked at (ADR-0066).
func HashDigests(d map[string]string) string {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%s\n", k, d[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CommitFiles commits exactly files (which may include deletions) and
// nothing else, whatever else is changed or staged in dir. It returns
// false when none of files had changes, in which case nothing is
// committed. repoRoot supplies the committer identity (identityOverride).
func CommitFiles(dir, repoRoot string, files []string, message string) (bool, error) {
	if _, err := runGit(dir, "reset", "-q"); err != nil {
		return false, err
	}
	if len(files) > 0 {
		args := append([]string{"add", "-A", "--"}, files...)
		if _, err := runGit(dir, args...); err != nil {
			return false, err
		}
	}
	dirty, err := hasStagedChanges(dir)
	if err != nil || !dirty {
		return false, err
	}
	args := append(identityOverride(repoRoot), "commit", "-q", "-m", message)
	if _, err := runGit(dir, args...); err != nil {
		return false, err
	}
	return true, nil
}

// Tag points a lightweight tag at HEAD.
func Tag(dir, name string) error {
	_, err := runGit(dir, "tag", name, "HEAD")
	return err
}

// TagExists reports whether a tag exists in dir.
func TagExists(dir, name string) bool {
	_, err := runGit(dir, "rev-parse", "--verify", "--quiet", "refs/tags/"+name)
	return err == nil
}

// DiffAgainst returns the diff from rev to the working tree, untracked
// files included. It stages into a throwaway index so the real index is
// left exactly as it was.
func DiffAgainst(dir, rev string) (string, error) {
	tmp, err := os.CreateTemp("", "masuda-index-*")
	if err != nil {
		return "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	env := append(os.Environ(), "GIT_INDEX_FILE="+tmp.Name())
	run := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
		}
		return stdout.String(), nil
	}
	if _, err := run("read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := run("add", "-A"); err != nil {
		return "", err
	}
	return run("diff", "--cached", rev)
}

// SnapshotTree records the working tree, untracked files included, as a
// git tree object and returns its ID. DiffAgainst(dir, id) later shows what
// changed since, which is how a fixer's own change is isolated for the
// recheck (ADR-0074). The real index is not touched.
func SnapshotTree(dir string) (string, error) {
	tmp, err := os.CreateTemp("", "masuda-index-*")
	if err != nil {
		return "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	env := append(os.Environ(), "GIT_INDEX_FILE="+tmp.Name())
	for _, args := range [][]string{{"read-tree", "HEAD"}, {"add", "-A"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
		}
	}
	cmd := exec.Command("git", "-C", dir, "write-tree")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git write-tree: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// BaseRev names base as a revision dir can resolve. A workspace cloned
// from an existing branch has only that branch locally (Create), so its
// base exists there only as origin/<base>.
func BaseRev(dir, base string) string {
	if _, err := runGit(dir, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err == nil {
		return base
	}
	if _, err := runGit(dir, "rev-parse", "--verify", "--quiet", "origin/"+base+"^{commit}"); err == nil {
		return "origin/" + base
	}
	return base
}
