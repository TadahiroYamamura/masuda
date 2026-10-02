package staging

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CommitInfo は1つのコミットの要約。
type CommitInfo struct {
	Hash    string
	Parents []string
	Author  string // "name <email>"
	Time    time.Time
	Message string
	// Files は第1親（無ければ空のtree）から変わったパス。
	Files []string
}

// GetCommit はrevのコミットを返す。
func (r *Repo) GetCommit(ctx context.Context, rev string) (CommitInfo, error) {
	hash, err := r.ResolveCommit(ctx, rev)
	if err != nil {
		return CommitInfo{}, err
	}
	out, err := r.git(ctx, "show", "-s", "--no-show-signature", "--format=%P%x00%an <%ae>%x00%at%x00%B", hash)
	if err != nil {
		return CommitInfo{}, err
	}
	f := strings.SplitN(out, "\x00", 4)
	if len(f) != 4 {
		return CommitInfo{}, fmt.Errorf("unexpected git show output for %s", hash)
	}
	sec, _ := strconv.ParseInt(f[2], 10, 64)
	c := CommitInfo{
		Hash:    hash,
		Parents: strings.Fields(f[0]),
		Author:  f[1],
		Time:    time.Unix(sec, 0),
		Message: strings.TrimRight(f[3], "\n") + "\n",
	}
	from, err := r.parentOrEmpty(ctx, c.Parents)
	if err != nil {
		return CommitInfo{}, err
	}
	if c.Files, err = r.changedPaths(ctx, from, hash); err != nil {
		return CommitInfo{}, err
	}
	return c, nil
}

// parentOrEmpty は第1親、ルートコミットなら空のtreeを返す。
func (r *Repo) parentOrEmpty(ctx context.Context, parents []string) (string, error) {
	if len(parents) > 0 {
		return parents[0], nil
	}
	out, err := gitCmd{dir: r.Dir, args: []string{"hash-object", "-t", "tree", "--stdin"}, stdin: strings.NewReader("")}.run(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Diff はfromからtoへのunified diffを返す。fromが空ならtoの第1親から。
// pathsが空でなければそのパスに絞る（グロブ等のpathspec記法は解釈しない）。
func (r *Repo) Diff(ctx context.Context, from, to string, paths []string) (string, error) {
	toC, err := r.ResolveCommit(ctx, to)
	if err != nil {
		return "", err
	}
	var fromX string
	if from == "" {
		c, err := r.git(ctx, "show", "-s", "--format=%P", toC)
		if err != nil {
			return "", err
		}
		if fromX, err = r.parentOrEmpty(ctx, strings.Fields(c)); err != nil {
			return "", err
		}
	} else if fromX, err = r.ResolveCommit(ctx, from); err != nil {
		return "", err
	}
	// porcelainのgit diffは利用者の設定（diff.external・textconv・色）に左右されるので、
	// 配管コマンドのdiff-treeで出す。
	args := append([]string{"--literal-pathspecs", "diff-tree", "-r", "-p", "--no-color", "--no-ext-diff", fromX, toC, "--"}, paths...)
	return r.git(ctx, args...)
}

// Blob はrevの時点のpathの内容を読むReaderを返す。呼び出し側がCloseする。
// pathが無いかblobでなければErrNotFound。
func (r *Repo) Blob(ctx context.Context, rev, path string) (io.ReadCloser, error) {
	commit, err := r.ResolveCommit(ctx, rev)
	if err != nil {
		return nil, err
	}
	obj := commit + ":" + strings.TrimPrefix(path, "/")
	t, err := r.git(ctx, "cat-file", "-t", obj)
	if err != nil || strings.TrimSpace(t) != "blob" {
		return nil, fmt.Errorf("blob %s: %w", obj, ErrNotFound)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", r.Dir, "cat-file", "blob", obj)
	cmd.Env = gitEnv(nil)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &cmdReader{ReadCloser: stdout, cmd: cmd, stderr: &stderr}, nil
}

type cmdReader struct {
	io.ReadCloser
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	once   sync.Once
	err    error
}

// Close は何度呼んでもよい（2回目以降は1回目の結果を返す）。
func (c *cmdReader) Close() error {
	c.once.Do(func() {
		_ = c.ReadCloser.Close()
		if err := c.cmd.Wait(); err != nil {
			c.err = fmt.Errorf("git cat-file: %w\n%s", err, c.stderr.String())
		}
	})
	return c.err
}
