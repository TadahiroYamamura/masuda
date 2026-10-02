// Package staging は、ワークスペースごとのstaging bareリポジトリ
// （`$XDG_DATA_HOME/masuda/workspaces/<id>/staging.git`）を扱う。
// masudaだけが読み書きし、実リポジトリに触るのはpublishだけ
// （docs/design/overview.md「4. ワークスペースとstaging」）。
//
// M1時点では旧internal/worktreeから写した種（bare cloneとfast-forward）だけを置く。
// refの設計・bundle・commit-treeはM2で足す。
package staging

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

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

// Clone は実リポジトリrepoRootからstagingをdirへ作る。
// --localは同一ファイルシステムならオブジェクトをハードリンクするので、
// 全履歴のcloneでも安い。
func Clone(repoRoot, dir string) error {
	_, err := runGit(repoRoot, "clone", "--bare", "--local", "--quiet", repoRoot, dir)
	return err
}

// FastForward はstagingのbranchを実リポジトリの同名ブランチへfast-forwardで反映する。
// 分岐した履歴はgit自身が拒否するので（非0終了・部分的な状態は残らない）、
// 強制的に解決することはない。
func FastForward(repoRoot, stagingDir, branch string) error {
	current, err := runGit(repoRoot, "branch", "--show-current")
	if err != nil {
		return err
	}
	if strings.TrimSpace(current) == branch {
		// チェックアウト中のブランチのrefへは直接fetchできないため、
		// FETCH_HEAD経由で作業ツリーごとfast-forwardする。
		if _, err := runGit(repoRoot, "fetch", stagingDir, branch); err != nil {
			return fmt.Errorf("fetching %s from staging: %w", branch, err)
		}
		_, err := runGit(repoRoot, "merge", "--ff-only", "FETCH_HEAD")
		return err
	}
	// 強制でないrefspecは、無ければ作り、あればfast-forwardし、
	// fast-forwardでない更新はgit自身が拒否する。
	_, err = runGit(repoRoot, "fetch", stagingDir, branch+":"+branch)
	return err
}
