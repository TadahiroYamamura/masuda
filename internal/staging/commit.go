package staging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Identity はstagingに作るコミットの作者兼コミッター。
type Identity struct {
	Name  string
	Email string
}

func (id Identity) env() []string {
	name, email := id.Name, id.Email
	if name == "" {
		name = "masuda"
	}
	if email == "" {
		email = "masuda@localhost"
	}
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email,
	}
}

// CommitOptions はCommitの入力。
type CommitOptions struct {
	Branch string // 進めるブランチ
	WIP    string // 作業ツリー全体を表すrev（通常refs/masuda/wip/<occ>）
	// Allowed はコミットに含めてよいパス（計画の対象ファイル＋承認された逸脱）。
	Allowed []string
	// Byproducts はコミットにも逸脱にも数えないパス。ブランチ上の内容のまま残す。
	Byproducts []string
	Message    string
	Author     Identity
}

// CommitResult はCommitがしたこと。
type CommitResult struct {
	// Deviations はAllowedにもByproductsにも当たらない変更パス。空でなければ何もコミットしていない。
	Deviations []string
	// Commit はブランチの新しい先頭。Allowedに当たる変更が無ければ元の先頭のまま。
	Commit string
}

// matchPath はpがpatternsのどれかに当たるかを返す。パターンは完全一致か、doublestarの
// グロブ（`*`は`/`をまたがず、`**`は0階層以上をまたぐ）。engineもコミット前の逸脱の判定で
// Byproductsを同じ規則で照合する（masuda-engineのmatchesByproduct）。規則がずれると、
// 片方が通した副産物をもう片方が逸脱にする。
func matchPath(p string, patterns []string) bool {
	for _, pat := range patterns {
		if pat == p {
			return true
		}
		if ok, err := doublestar.Match(pat, p); err == nil && ok {
			return true
		}
	}
	return false
}

// Commit はWIPとブランチ先頭の差分のうち、Allowedに当たるパスだけを先頭のtreeに
// 重ねたtreeを作り、commit-treeでブランチを進める。
//
// 作業ツリー全体をコミットしないのは、ゲストが計画外のファイルを黙ってブランチへ
// 混ぜられないようにするため。計画外の変更（Deviations）があれば何も書かずに返し、
// 人間の判断（deviationゲート）を待つ。
func (r *Repo) Commit(ctx context.Context, o CommitOptions) (CommitResult, error) {
	branchRef := BranchRef(o.Branch)
	head, err := r.ResolveCommit(ctx, branchRef)
	if err != nil {
		return CommitResult{}, err
	}
	wipTree, err := resolve(ctx, r.Dir, o.WIP, "tree")
	if err != nil {
		return CommitResult{}, err
	}
	changed, err := r.changedPaths(ctx, head, wipTree)
	if err != nil {
		return CommitResult{}, err
	}
	var take, deviations []string
	for _, p := range changed {
		switch {
		case matchPath(p, o.Allowed):
			take = append(take, p)
		case matchPath(p, o.Byproducts):
		default:
			deviations = append(deviations, p)
		}
	}
	if len(deviations) > 0 {
		return CommitResult{Deviations: deviations}, nil
	}
	if len(take) == 0 {
		return CommitResult{Commit: head}, nil
	}

	tree, err := r.overlayTree(ctx, head, wipTree, take)
	if err != nil {
		return CommitResult{}, err
	}
	msg := o.Message
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	out, err := gitCmd{
		dir:   r.Dir,
		args:  []string{"commit-tree", tree, "-p", head, "-F", "-"},
		env:   o.Author.env(),
		stdin: strings.NewReader(msg),
	}.run(ctx)
	if err != nil {
		return CommitResult{}, err
	}
	commit := strings.TrimSpace(out)
	// 旧値を渡して、計算の間に誰かがブランチを動かしていたら失敗させる。
	if _, err := r.git(ctx, "update-ref", "-m", "masuda commit", branchRef, commit, head); err != nil {
		return CommitResult{}, err
	}
	return CommitResult{Commit: commit}, nil
}

// changedPaths はfromとtoの間で内容かモードが違うパスを返す。
func (r *Repo) changedPaths(ctx context.Context, from, to string) ([]string, error) {
	out, err := r.git(ctx, "diff-tree", "-r", "--no-renames", "--name-only", "-z", from, to)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// overlayTree はbaseのtreeに、pathsだけsrcTreeの内容（無ければ削除）を重ねたtreeを作る。
// 作業ツリーの無いbareリポジトリなので、使い捨てのindexファイルで組み立てる。
func (r *Repo) overlayTree(ctx context.Context, base, srcTree string, paths []string) (string, error) {
	tmp, err := os.MkdirTemp(r.Dir, "commit-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}

	if _, err := (gitCmd{dir: r.Dir, args: []string{"read-tree", base}, env: env}).run(ctx); err != nil {
		return "", err
	}
	// -rで再帰するので、ファイルからディレクトリへ変わったパスは「無い」側として扱われ削除になる
	args := append([]string{"ls-tree", "-r", "-z", "--full-tree", srcTree, "--"}, paths...)
	out, err := r.git(ctx, args...)
	if err != nil {
		return "", err
	}
	present := map[string]string{} // path -> "mode sha"
	for _, rec := range strings.Split(out, "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta) // mode type sha
		if len(f) != 3 {
			continue
		}
		present[p] = f[0] + " " + f[2]
	}
	zero, err := r.zeroOID(ctx)
	if err != nil {
		return "", err
	}
	var info strings.Builder
	for _, p := range paths {
		if ms, ok := present[p]; ok {
			fmt.Fprintf(&info, "%s\t%s\x00", ms, p)
		} else {
			fmt.Fprintf(&info, "0 %s\t%s\x00", zero, p)
		}
	}
	if _, err := (gitCmd{dir: r.Dir, args: []string{"update-index", "-z", "--index-info"}, env: env, stdin: strings.NewReader(info.String())}).run(ctx); err != nil {
		return "", err
	}
	tree, err := (gitCmd{dir: r.Dir, args: []string{"write-tree"}, env: env}).run(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tree), nil
}

// zeroOID はこのリポジトリのハッシュ方式（SHA-1/SHA-256）でのゼロOIDを返す。
func (r *Repo) zeroOID(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "rev-parse", "--show-object-format")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "sha256" {
		return strings.Repeat("0", 64), nil
	}
	return strings.Repeat("0", 40), nil
}

// Changes はfromからtoへ内容かモードが変わったパスと、その変更の組を一意に表す
// ダイジェストを返す。ダイジェストはパスだけでなく変更後のblobも含むので、同じファイルが
// さらに書き換えられれば別の値になる（ゲートで承認した内容と後の内容を区別するため）。
func (r *Repo) Changes(ctx context.Context, from, to string) ([]string, string, error) {
	fromT, err := resolve(ctx, r.Dir, from, "tree")
	if err != nil {
		return nil, "", err
	}
	toT, err := resolve(ctx, r.Dir, to, "tree")
	if err != nil {
		return nil, "", err
	}
	raw, err := r.git(ctx, "diff-tree", "-r", "--no-renames", "--raw", "-z", "--abbrev=64", fromT, toT)
	if err != nil {
		return nil, "", err
	}
	paths, err := r.changedPaths(ctx, fromT, toT)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256([]byte(raw))
	return paths, hex.EncodeToString(sum[:]), nil
}
