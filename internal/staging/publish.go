package staging

import (
	"context"
	"fmt"
	"strings"
)

// checkHead はstagingのbranchがちょうどcommitを指していることを確かめる。publishするのは
// review gateで承認されたコミットだけで、その後にブランチが動いていたら反映しない。
func (r *Repo) checkHead(ctx context.Context, branch, commit string) error {
	head, err := r.ResolveCommit(ctx, BranchRef(branch))
	if err != nil {
		return err
	}
	if head != commit {
		return fmt.Errorf("staging %s is at %s, not the approved %s", branch, head, commit)
	}
	return nil
}

// PublishLocal は実リポジトリrepoRootの同名ブランチを、stagingのbranch（= commit）へ
// fast-forwardする。ブランチが無ければ作る。fast-forwardにならなければ何も変えない。
func (r *Repo) PublishLocal(ctx context.Context, repoRoot, branch, commit string) error {
	if err := r.checkHead(ctx, branch, commit); err != nil {
		return err
	}
	ref := BranchRef(branch)
	// 実リポジトリへ持ち込むのはオブジェクトとFETCH_HEADだけで、refは後で検査してから動かす。
	if _, err := runGit(ctx, repoRoot, "fetch", "--quiet", "--no-tags", "--", r.Dir, ref); err != nil {
		return fmt.Errorf("fetching %s from staging: %w", branch, err)
	}
	fetched, err := resolve(ctx, repoRoot, "FETCH_HEAD", "commit")
	if err != nil {
		return err
	}
	if fetched != commit {
		return fmt.Errorf("staging %s moved to %s during publish; approved %s", branch, fetched, commit)
	}

	current, _ := runGit(ctx, repoRoot, "symbolic-ref", "--quiet", "--short", "HEAD")
	if strings.TrimSpace(current) == branch {
		// チェックアウト中のブランチはrefだけ動かすと作業ツリーとずれるので、
		// 作業ツリーごとfast-forwardする。衝突する未コミットの変更があればgitが拒否する。
		_, err := runGit(ctx, repoRoot, "merge", "--ff-only", "--quiet", commit)
		return err
	}
	old, err := resolve(ctx, repoRoot, ref, "commit")
	if err != nil {
		old = "" // update-refの旧値に空を渡すと「まだ存在しないこと」を条件にできる
	} else if _, err := runGit(ctx, repoRoot, "merge-base", "--is-ancestor", old, commit); err != nil {
		return fmt.Errorf("%s in %s has diverged from staging; not a fast-forward", branch, repoRoot)
	}
	_, err = runGit(ctx, repoRoot, "update-ref", "-m", "masuda publish", ref, commit, old)
	return err
}

// PushRemote はstagingのbranch（= commit）をremoteURLの同名ブランチへpushする。
// 強制pushはしないので、リモートが先に進んでいればリモート側が拒否する。
func (r *Repo) PushRemote(ctx context.Context, remoteURL, branch, commit string) error {
	if err := r.checkHead(ctx, branch, commit); err != nil {
		return err
	}
	if remoteURL == "" || strings.HasPrefix(remoteURL, "-") {
		return fmt.Errorf("invalid remote %q", remoteURL)
	}
	_, err := r.git(ctx, "push", "--quiet", "--", remoteURL, commit+":"+BranchRef(branch))
	return err
}

// RemoteURL は実リポジトリに設定されたremote nameのURLを返す。stagingからは
// originを外しているので、push先は実リポジトリの設定から引く。
func RemoteURL(ctx context.Context, repoRoot, name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "-") {
		return "", fmt.Errorf("invalid remote name %q", name)
	}
	out, err := runGit(ctx, repoRoot, "remote", "get-url", "--push", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
