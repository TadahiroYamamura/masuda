package staging

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorktreeSnapshotRef はSnapshotWorktreeが一時リポジトリに置くref。特権コマンドへ渡す
// スナップショット（privileged.Run）と同じWIPの形にする。
var WorktreeSnapshotRef = WIPRef("worktree")

// SnapshotWorktree は利用者のリポジトリrepoRootの作業ツリーを、dirに新しく作るbareリポジトリの
// WorktreeSnapshotRefにコミットとして置き、そのリポジトリを返す。revが空なら作業ツリーの今の状態
// （追跡しているファイルの未コミットの変更と、gitignoreされていない未追跡のファイル。ゲストの
// WIPスナップショットと同じ範囲）、空でなければrevのコミットそのもの。
//
// 利用者のリポジトリにはrefもオブジェクトも書かない。dirへcloneし、そのリポジトリをGIT_DIRに、
// 利用者の作業ツリーをGIT_WORK_TREEに、dirの中の一時ファイルをGIT_INDEX_FILEにして
// `add -A`→`write-tree`→`commit-tree`する。オブジェクトはdirにだけ増える。GIT_DIRが利用者の
// `.git`でないので、利用者の`.git/info/exclude`とリポジトリの設定の`core.excludesFile`は
// dirへ写して効かせる（作業ツリーの`.gitignore`と全体の設定はそのまま効く）。
func SnapshotWorktree(ctx context.Context, repoRoot, dir, rev string) (*Repo, error) {
	if err := Clone(ctx, repoRoot, dir); err != nil {
		return nil, err
	}
	r := Open(dir)
	var commit string
	var err error
	if rev != "" {
		// 利用者のリポジトリで解決する（読むだけ）。bareのcloneはrefs/remotes等を写さないので、
		// `origin/main`のような名前はcloneの側では引けない。オブジェクトは--localのcloneで揃っている。
		commit, err = resolve(ctx, repoRoot, rev, "commit")
	} else {
		commit, err = r.commitWorktree(ctx, repoRoot)
	}
	if err != nil {
		return nil, err
	}
	if err := r.SetRef(ctx, WorktreeSnapshotRef, commit); err != nil {
		return nil, err
	}
	return r, nil
}

// commitWorktree はrepoRootの作業ツリーをrのオブジェクトとしてコミットし、そのハッシュを返す。
// 親は利用者のHEAD（まだコミットが無ければ親なし）。
func (r *Repo) commitWorktree(ctx context.Context, repoRoot string) (string, error) {
	if err := r.copyExcludes(ctx, repoRoot); err != nil {
		return "", err
	}
	head, _ := resolve(ctx, repoRoot, "HEAD", "commit")
	env := []string{
		"GIT_DIR=" + r.Dir,
		"GIT_WORK_TREE=" + repoRoot,
		"GIT_INDEX_FILE=" + filepath.Join(r.Dir, "masuda-snapshot.index"),
	}
	run := func(args ...string) (string, error) {
		// cloneはbareなので、作業ツリーを扱うコマンドが断らないようcore.bareを打ち消す。
		return gitCmd{dir: repoRoot, args: append([]string{"-c", "core.bare=false"}, args...), env: env}.run(ctx)
	}
	// HEADのツリーから始めるのは、gitignoreに当たるが追跡しているファイルを`add -A`が落とさないように。
	if head != "" {
		if _, err := run("read-tree", head); err != nil {
			return "", err
		}
	}
	if _, err := run("add", "-A"); err != nil {
		return "", err
	}
	out, err := run("write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", strings.TrimSpace(out), "-m", "masuda: snapshot of the worktree"}
	if head != "" {
		args = append(args, "-p", head)
	}
	out, err = gitCmd{dir: repoRoot, args: args, env: append(env, Identity{}.env()...)}.run(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// copyExcludes は利用者の`info/exclude`と、リポジトリの設定（`.git/config`）のcore.excludesFileを
// rへ写す。
func (r *Repo) copyExcludes(ctx context.Context, repoRoot string) error {
	out, err := runGit(ctx, repoRoot, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	b, err := os.ReadFile(strings.TrimSpace(out))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		dst := filepath.Join(r.Dir, "info", "exclude")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
	}
	// 値が無ければ`git config --get`は1で終わる。全体の設定のものはrでもそのまま効くので、
	// リポジトリの設定だけを見る。
	if out, err := runGit(ctx, repoRoot, "config", "--local", "--get", "core.excludesFile"); err == nil {
		if v := strings.TrimSpace(out); v != "" {
			if _, err := r.git(ctx, "config", "core.excludesFile", v); err != nil {
				return fmt.Errorf("copying core.excludesFile: %w", err)
			}
		}
	}
	return nil
}

// IgnoredFiles はrepoRootの作業ツリーで、gitignoreされた未追跡のファイルのパス（`/`区切り、
// 作業ツリーのトップから）を返す。ignoreの判定は利用者のリポジトリの設定どおり。
// 中にリポジトリを持つディレクトリ（入れ子のリポジトリ）の中は含まない。
func IgnoredFiles(ctx context.Context, repoRoot string) ([]string, error) {
	out, err := runGit(ctx, repoRoot, "ls-files", "-z", "--others", "--ignored", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			files = append(files, p)
		}
	}
	return files, nil
}
