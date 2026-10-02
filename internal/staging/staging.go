// Package staging は、ワークスペースごとのstaging bareリポジトリ
// （`$XDG_DATA_HOME/masuda/workspaces/<id>/staging.git`）を扱う。
// masudaだけが読み書きし、実リポジトリに触るのはpublishだけ
// （docs/design/overview.md「4. ワークスペースとstaging」）。
package staging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// BaseRef は分岐元を指すref。diffの基準になる。
const BaseRef = "refs/masuda/base"

// WIPRef はノード境界のWIPスナップショットのref名を返す。
func WIPRef(occurrence string) string { return "refs/masuda/wip/" + occurrence }

// BranchRef はワークスペースのブランチのref名を返す。
func BranchRef(branch string) string { return "refs/heads/" + branch }

// ErrNotFound はrevやパスがstagingに無いことを表す。
var ErrNotFound = errors.New("not found")

// ErrInvalid は引数（rev・ブランチ名等）が受け付けられない形であることを表す。
var ErrInvalid = errors.New("invalid argument")

// ErrBranchExists は作ろうとしたブランチが実リポジトリに既にあることを表す。
var ErrBranchExists = errors.New("branch already exists")

// Repo は1つのstaging bareリポジトリ。
type Repo struct {
	Dir string
}

// Open は既存のstagingを開く。
func Open(dir string) *Repo { return &Repo{Dir: dir} }

// repoLocatingEnv は、masuda serve自身がgitのフック等から起動されたときに
// 引き継いでしまうと、-Cで指定したのとは別のリポジトリを操作させる環境変数。
// GIT_SSH_COMMAND・GIT_ASKPASS等の認証まわりはpushで要るので落とさない。
var repoLocatingEnv = []string{
	"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_OBJECT_DIRECTORY=",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_COMMON_DIR=", "GIT_NAMESPACE=",
}

func gitEnv(extra []string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, kv := range os.Environ() {
		drop := false
		for _, p := range repoLocatingEnv {
			if strings.HasPrefix(kv, p) {
				drop = true
				break
			}
		}
		if !drop {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

type gitCmd struct {
	dir   string
	args  []string
	env   []string
	stdin io.Reader
}

func (g gitCmd) run(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.dir}, g.args...)...)
	cmd.Env = gitEnv(g.env)
	cmd.Stdin = g.stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(g.args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	return gitCmd{dir: dir, args: args}.run(ctx)
}

func (r *Repo) git(ctx context.Context, args ...string) (string, error) {
	return runGit(ctx, r.Dir, args...)
}

// validRev はrevがgitのオプションとして解釈されないことを確かめる。APIから来た
// 文字列をそのままgitの引数に渡すので、`--output=...`のような値を弾く。
func validRev(rev string) error {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return fmt.Errorf("rev %q: %w", rev, ErrInvalid)
	}
	return nil
}

// resolve はrevをstaging内のオブジェクトハッシュへ解決する。kindは"commit"・"tree"等。
// 無ければErrNotFound。
func resolve(ctx context.Context, dir, rev, kind string) (string, error) {
	if err := validRev(rev); err != nil {
		return "", err
	}
	out, err := runGit(ctx, dir, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{"+kind+"}")
	if err != nil {
		return "", fmt.Errorf("%s %q: %w", kind, rev, ErrNotFound)
	}
	return strings.TrimSpace(out), nil
}

// ResolveCommit はrevをコミットハッシュへ解決する。
func (r *Repo) ResolveCommit(ctx context.Context, rev string) (string, error) {
	return resolve(ctx, r.Dir, rev, "commit")
}

// Clone は実リポジトリrepoRootからstagingをdirへ作る。
// --localは同一ファイルシステムならオブジェクトをハードリンクするので、
// 全履歴のcloneでも安い。
func Clone(ctx context.Context, repoRoot, dir string) error {
	_, err := runGit(ctx, repoRoot, "clone", "--bare", "--local", "--quiet", "--", repoRoot, dir)
	return err
}

// CreateOptions はCreateの入力。
type CreateOptions struct {
	RepoRoot string // 実リポジトリ（作業ツリーのトップ）
	Dir      string // 作るstaging.git
	Branch   string // ワークスペースのブランチ。stagingにだけ作る
	Base     string // 分岐元のrev。空なら実リポジトリのHEADが指すブランチ
}

// CreateResult はCreateが決めたもの。
type CreateResult struct {
	Base       string // 解決後の分岐元の名前（Baseが空なら既定ブランチ名）
	BaseCommit string
}

// Create はstagingを作り、refs/masuda/baseとrefs/heads/<branch>を分岐元に置く。
// 実リポジトリは読むだけで、ブランチもrefも作らない。
func Create(ctx context.Context, o CreateOptions) (CreateResult, error) {
	if _, err := runGit(ctx, o.RepoRoot, "check-ref-format", "--branch", o.Branch); err != nil || strings.HasPrefix(o.Branch, "-") {
		return CreateResult{}, fmt.Errorf("branch name %q: %w", o.Branch, ErrInvalid)
	}
	base := o.Base
	if base == "" {
		// 実リポジトリにoriginがあってもorigin/HEADは使わない。ローカルの既定ブランチの
		// 方が利用者の手元の状態に近く、publish先（同名ローカルブランチ）とも揃う。
		// HEADがdetachedならそのコミットを分岐元にする。
		out, err := runGit(ctx, o.RepoRoot, "symbolic-ref", "--quiet", "--short", "HEAD")
		if err == nil {
			base = strings.TrimSpace(out)
		} else {
			base = "HEAD"
		}
	}
	baseCommit, err := resolve(ctx, o.RepoRoot, base, "commit")
	if err != nil {
		return CreateResult{}, fmt.Errorf("base: %w", err)
	}
	if base == "HEAD" {
		base = baseCommit
	}
	if _, err := resolve(ctx, o.RepoRoot, BranchRef(o.Branch), "commit"); err == nil {
		return CreateResult{}, fmt.Errorf("%q in %s: %w", o.Branch, o.RepoRoot, ErrBranchExists)
	}

	if err := Clone(ctx, o.RepoRoot, o.Dir); err != nil {
		return CreateResult{}, err
	}
	r := Open(o.Dir)
	// cloneが残すoriginは実リポジトリを指す。stagingから実リポジトリへ書けるのは
	// publishだけにしたいので、名前で辿れる経路を残さない。
	if _, err := r.git(ctx, "remote", "remove", "origin"); err != nil {
		return CreateResult{}, err
	}
	if _, err := r.git(ctx, "cat-file", "-e", baseCommit+"^{commit}"); err != nil {
		// --localはobjectsディレクトリごと写すので通常ここには来ない。alternates越しで
		// 届かないなどの例外に備え、分岐元のrefだけを取り直す。
		full, ferr := runGit(ctx, o.RepoRoot, "rev-parse", "--symbolic-full-name", "--end-of-options", base)
		if ferr != nil || strings.TrimSpace(full) == "" {
			return CreateResult{}, fmt.Errorf("base commit %s not in staging", baseCommit)
		}
		if _, err := r.git(ctx, "fetch", "--quiet", "--no-tags", "--", o.RepoRoot, strings.TrimSpace(full)); err != nil {
			return CreateResult{}, err
		}
	}
	if _, err := r.git(ctx, "update-ref", BaseRef, baseCommit, ""); err != nil {
		return CreateResult{}, err
	}
	if _, err := r.git(ctx, "update-ref", BranchRef(o.Branch), baseCommit, ""); err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Base: base, BaseCommit: baseCommit}, nil
}

// ImportBundle はゲストが作ったbundleからsrcRefを取り込み、refs/masuda/wip/<occurrence>に置く。
// WIPスナップショットはノード境界ごとに作り直すので、同じ出現IDの再取り込みは上書きする。
func (r *Repo) ImportBundle(ctx context.Context, bundlePath, srcRef, occurrence string) (string, error) {
	if occurrence == "" || strings.ContainsAny(occurrence, "/ ") {
		return "", fmt.Errorf("occurrence %q: %w", occurrence, ErrInvalid)
	}
	return r.FetchBundle(ctx, bundlePath, srcRef, WIPRef(occurrence))
}

// FetchBundle はbundleのsrcRefをstagingのdstRefへ取り込み、そのコミットを返す。dstRefは上書きする。
func (r *Repo) FetchBundle(ctx context.Context, bundlePath, srcRef, dstRef string) (string, error) {
	if err := validRev(srcRef); err != nil {
		return "", err
	}
	if !strings.HasPrefix(dstRef, "refs/") {
		return "", fmt.Errorf("ref %q: %w", dstRef, ErrInvalid)
	}
	if _, err := r.git(ctx, "bundle", "verify", "--quiet", "--", bundlePath); err != nil {
		return "", err
	}
	if _, err := r.git(ctx, "fetch", "--quiet", "--no-tags", "--", bundlePath, "+"+srcRef+":"+dstRef); err != nil {
		return "", err
	}
	return r.ResolveCommit(ctx, dstRef)
}

// CreateBundle はrefs（stagingのref名）を含むbundleをoutPathに書く。ゲストへ渡す用。
func (r *Repo) CreateBundle(ctx context.Context, outPath string, refs ...string) error {
	if len(refs) == 0 {
		return fmt.Errorf("bundle needs at least one ref: %w", ErrInvalid)
	}
	for _, ref := range refs {
		if err := validRev(ref); err != nil {
			return err
		}
	}
	_, err := r.git(ctx, append([]string{"bundle", "create", "--quiet", outPath}, refs...)...)
	return err
}

// Ref は1つのrefとその指すコミット。
type Ref struct {
	Name   string
	Commit string
}

// ListRefs はstagingのrefをすべて返す。注釈付きタグはコミットへ剥がす。
func (r *Repo) ListRefs(ctx context.Context) ([]Ref, error) {
	out, err := r.git(ctx, "for-each-ref", "--format=%(refname) %(if)%(*objectname)%(then)%(*objectname)%(else)%(objectname)%(end)")
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, commit, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		refs = append(refs, Ref{Name: name, Commit: commit})
	}
	return refs, nil
}

// TopLevel はdirを含む作業ツリーのトップを返す。gitの作業ツリーでなければErrInvalid。
func TopLevel(ctx context.Context, dir string) (string, error) {
	out, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("%s is not a git work tree: %w", dir, ErrInvalid)
	}
	return strings.TrimSpace(out), nil
}
