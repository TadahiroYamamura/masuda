// Package privileged は特権コマンド（docs/design/overview.md「特権コマンド」）の実行手順。
// 宣言済み・承認済みのコマンドを、メインのsandboxとは別の使い捨てsandbox（root）で動かし、
// 結果をホストの記録とメインのゲストの`/masuda/privileged/<run-id>/`へ置く。
//
// 宣言の読み込み・承認の確認・イメージのビルドは呼び出し側（serve）が行い、このパッケージは
// sandboxとstagingの操作だけを受け持つ。
package privileged

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/guest"
	"github.com/TadahiroYamamura/masuda/internal/staging"
)

const (
	// User は特権sandboxでコマンドを動かすユーザー。
	User = "root"
	// GuestResultsRoot はメインのゲストで結果の写しを置く場所。
	GuestResultsRoot = "/masuda/privileged"
	// LogTailBytes はログのうち返し・残す末尾の長さ。
	LogTailBytes = 200 << 10
	// DefaultTimeout はtimeoutSecondsが0のときの上限。宣言を書き忘れたコマンドが特権VMを
	// 握り続けないよう、無制限にはしない。
	DefaultTimeout = time.Hour

	guestWorkspace = "/workspace"
	bundleGuest    = "/masuda/privileged.bundle"
)

// Validate は宣言の形を検査する。imageExistsはイメージのエントリがあるか（Dockerfileがあるか）。
func Validate(d config.PrivilegedCommandDecl, imageExists func(entry string) bool) error {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if strings.TrimSpace(d.Command) == "" {
		add("command is empty")
	}
	switch {
	case d.Image == "":
		add("image is empty")
	case !config.ValidCheckName(d.Image):
		add("image %q is not a valid entry name", d.Image)
	case !imageExists(d.Image):
		add("image %q has no .masuda/images/%s/Dockerfile", d.Image, d.Image)
	}
	if d.TimeoutSeconds < 0 {
		add("timeoutSeconds must not be negative")
	}
	for _, p := range d.Inputs {
		if err := ValidatePattern(p); err != nil {
			add("inputs: %v", err)
		}
	}
	for _, p := range d.Outputs {
		if err := ValidatePattern(p); err != nil {
			add("outputs: %v", err)
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// Options は1回の実行に要るもの。
type Options struct {
	Sandbox sandboxv1connect.SandboxServiceClient
	// MainID はメインのsandbox（inputsの読み出し元、結果の写しの置き先）。
	MainID string
	// SandboxID は作る特権sandboxのid。
	SandboxID string
	RunID     string
	BuildID   string
	// DiskMiB は特権sandboxのルートディスクの最小容量（宣言のイメージのエントリの設定）。
	DiskMiB uint32
	// Egress は特権sandboxに許すホスト（宣言と承認の積）。
	Egress []string
	Decl   config.PrivilegedCommandDecl
	// Staging と SnapshotRef は、メインの作業ツリーを取り込んだstagingとそのref。
	Staging     *staging.Repo
	SnapshotRef string
	// HostDir は結果を残すホストのディレクトリ（`records/privileged/<run-id>`）。
	HostDir string
	// Heartbeat は実行中に定期的に呼ばれる（活動の記録用）。nilなら呼ばない。
	Heartbeat func()
}

// Result はrun_privileged_commandの戻り値（docs/guest-protocol.md）。
type Result struct {
	ExitCode     int      `json:"exit_code"`
	Log          string   `json:"log"`
	Truncated    bool     `json:"truncated"`
	ResultsDir   string   `json:"results_dir"`
	Outputs      []string `json:"outputs"`
	OutputsError string   `json:"outputs_error,omitempty"`
	TimedOut     bool     `json:"timed_out"`
	Signal       string   `json:"signal,omitempty"`
}

// ResultsDir はメインのゲストでの結果の写しの場所。
func ResultsDir(runID string) string { return GuestResultsRoot + "/" + runID + "/" }

// Run は特権sandboxを作って宣言のコマンドを動かし、結果を回収して返す。特権sandboxは
// 成否に関わらず壊す。コマンドが0以外で終わってもエラーにはしない（exit_codeで返す）。
func Run(ctx context.Context, o Options) (*Result, error) {
	if err := os.MkdirAll(filepath.Join(o.HostDir, "outputs"), 0o700); err != nil {
		return nil, err
	}
	if _, err := o.Sandbox.CreateSandbox(ctx, connect.NewRequest(&sandboxv1.CreateSandboxRequest{
		Id:          o.SandboxID,
		BuildId:     o.BuildID,
		DefaultUser: User,
		DiskMib:     o.DiskMiB,
		// 秘密・tcp_maps・MCPは渡さない。特権VMはAPIトークンもmasudaへの経路も持たない。
		Policy: &sandboxv1.Policy{AllowedHosts: o.Egress},
	})); err != nil {
		return nil, fmt.Errorf("creating the privileged sandbox: %w", err)
	}
	defer func() {
		_, _ = o.Sandbox.DestroySandbox(context.WithoutCancel(ctx), connect.NewRequest(&sandboxv1.DestroySandboxRequest{Id: o.SandboxID}))
	}()

	if err := o.checkout(ctx); err != nil {
		return nil, err
	}
	if err := o.copyInputs(ctx); err != nil {
		return nil, err
	}
	exited, log, err := o.exec(ctx)
	if err != nil {
		return nil, err
	}
	res := &Result{
		ExitCode:   int(exited.ExitCode),
		Signal:     exited.Signal,
		TimedOut:   exited.TimedOut,
		Log:        string(log.bytes()),
		Truncated:  log.truncated,
		ResultsDir: ResultsDir(o.RunID),
		Outputs:    []string{},
	}
	if exited.Signal != "" && res.ExitCode == 0 {
		res.ExitCode = -1
	}
	outputs, outErr := o.collectOutputs(ctx)
	res.Outputs = append(res.Outputs, outputs...)
	if outErr != nil {
		res.OutputsError = outErr.Error()
	}
	if err := o.place(ctx, res); err != nil {
		return nil, err
	}
	return res, nil
}

// checkout はstagingのスナップショットをbundleで特権sandboxへ渡し、`/workspace`に展開する。
// cloneでなくinit→fetchにしているのは、スナップショットのrefが`refs/masuda/wip/…`で、
// cloneが既定で取ってくる範囲（ブランチとタグ）に入らないため。
func (o *Options) checkout(ctx context.Context) error {
	tmp, err := os.CreateTemp(o.HostDir, ".bundle-*")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if err := o.Staging.CreateBundle(ctx, tmp.Name(), o.SnapshotRef); err != nil {
		return fmt.Errorf("bundling the snapshot: %w", err)
	}
	f, err := os.Open(tmp.Name())
	if err != nil {
		return err
	}
	defer f.Close()
	if err := guest.WriteFile(ctx, o.Sandbox, o.SandboxID, bundleGuest, f, 0o644); err != nil {
		return err
	}
	ref := shellQuote(o.SnapshotRef)
	bundle := shellQuote(strings.TrimPrefix(bundleGuest, "/"))
	script := strings.Join([]string{
		"set -e",
		// 実VMのWriteFileは書き手に関わらず所有者を付けるので、所有者の食い違いでgitが止まらないように。
		"git config --global --add safe.directory " + guestWorkspace,
		"git init -q workspace",
		"git -C workspace fetch -q --no-tags ../" + bundle + " +" + ref + ":" + ref,
		"git -C workspace checkout -q --detach " + ref,
		"rm -f " + bundle,
	}, "\n")
	if _, err := o.shell(ctx, o.SandboxID, "/", script); err != nil {
		return fmt.Errorf("checking out the snapshot in the privileged sandbox: %w", err)
	}
	return nil
}

// fileEntry はゲストのファイル1つ（/workspaceからの相対パスと許可ビット）。
type fileEntry struct {
	rel  string
	mode uint32
}

// listFiles はsandbox idの/workspaceの下で、patternsのどれかに当たる通常ファイルを返す。
// 各パターンのワイルドカードを含まない先頭のディレクトリからfindし、`.git`の下は見ない。
// シンボリックリンクは辿らず、返しもしない（/workspaceの外を指していても読まないため）。
func (o *Options) listFiles(ctx context.Context, id string, patterns []string) ([]fileEntry, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	bases := map[string]bool{}
	for _, p := range patterns {
		bases[baseDir(p)] = true
	}
	var quoted []string
	for b := range bases {
		quoted = append(quoted, shellQuote(b))
	}
	sort.Strings(quoted)
	script := "for b in " + strings.Join(quoted, " ") + `; do [ -e "$b" ] || continue; find "$b" -name .git -prune -o -type f -printf '%m %p\0'; done`
	res, err := o.shell(ctx, id, guestWorkspace, script)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []fileEntry
	for _, rec := range bytes.Split(res.Stdout, []byte{0}) {
		modeStr, p, ok := strings.Cut(string(rec), " ")
		if !ok {
			continue
		}
		rel := strings.TrimPrefix(path.Clean(p), "./")
		if seen[rel] || rel == "." || strings.HasPrefix(rel, "../") {
			continue
		}
		mode, err := strconv.ParseUint(modeStr, 8, 32)
		if err != nil {
			continue
		}
		for _, pat := range patterns {
			if Match(pat, rel) {
				seen[rel] = true
				out = append(out, fileEntry{rel: rel, mode: uint32(mode) & 0o777})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// copyInputs はinputsに当たるファイルをメインのゲストから特権sandboxの/workspaceへ運ぶ。
func (o *Options) copyInputs(ctx context.Context) error {
	files, err := o.listFiles(ctx, o.MainID, o.Decl.Inputs)
	if err != nil {
		return fmt.Errorf("listing inputs: %w", err)
	}
	for _, f := range files {
		b, err := guest.ReadFile(ctx, o.Sandbox, o.MainID, guestWorkspace+"/"+f.rel, 0)
		if err != nil {
			return fmt.Errorf("reading input %s: %w", f.rel, err)
		}
		if err := guest.WriteBytes(ctx, o.Sandbox, o.SandboxID, guestWorkspace+"/"+f.rel, b, f.mode); err != nil {
			return fmt.Errorf("writing input %s: %w", f.rel, err)
		}
	}
	return nil
}

// exec は宣言のコマンドをrootで動かし、stdoutとstderrを届いた順に1本のログにまとめる。
func (o *Options) exec(ctx context.Context) (*sandboxv1.ExecEvent_Exited, *tailBuffer, error) {
	timeout := time.Duration(o.Decl.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if o.Heartbeat != nil {
		hbCtx, stop := context.WithCancel(ctx)
		defer stop()
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-hbCtx.Done():
					return
				case <-t.C:
					o.Heartbeat()
				}
			}
		}()
	}
	st, err := o.Sandbox.Exec(ctx, connect.NewRequest(&sandboxv1.ExecRequest{
		Id: o.SandboxID, Shell: o.Decl.Command, User: User, Cwd: guestWorkspace,
		TimeoutMs: uint32(min(timeout.Milliseconds(), int64(^uint32(0)))),
	}))
	if err != nil {
		return nil, nil, fmt.Errorf("running the privileged command: %w", err)
	}
	defer st.Close()
	log := &tailBuffer{max: LogTailBytes}
	var exited *sandboxv1.ExecEvent_Exited
	for st.Receive() {
		switch ev := st.Msg().Event.(type) {
		case *sandboxv1.ExecEvent_Stdout:
			log.write(ev.Stdout)
		case *sandboxv1.ExecEvent_Stderr:
			log.write(ev.Stderr)
		case *sandboxv1.ExecEvent_Exited_:
			exited = ev.Exited
		}
	}
	if err := st.Err(); err != nil {
		return nil, nil, fmt.Errorf("running the privileged command: %w", err)
	}
	if exited == nil {
		return nil, nil, errors.New("running the privileged command: the exec stream ended without an exit")
	}
	return exited, log, nil
}

// collectOutputs はoutputsに当たるファイルを特権sandboxから読み、ホストの`outputs/`に置く。
// 1つも当たらなかったパターンや読めなかったファイルはエラーにまとめるが、読めたものは返す。
// コマンドが失敗しても途中までの出力（テストのレポート等）は役に立つため、全か無かにしない。
func (o *Options) collectOutputs(ctx context.Context) ([]string, error) {
	files, err := o.listFiles(ctx, o.SandboxID, o.Decl.Outputs)
	if err != nil {
		return nil, fmt.Errorf("listing outputs: %w", err)
	}
	var problems []string
	for _, pat := range o.Decl.Outputs {
		hit := false
		for _, f := range files {
			if Match(pat, f.rel) {
				hit = true
				break
			}
		}
		if !hit {
			problems = append(problems, fmt.Sprintf("no file matched %q", pat))
		}
	}
	var got []string
	for _, f := range files {
		b, err := guest.ReadFile(ctx, o.Sandbox, o.SandboxID, guestWorkspace+"/"+f.rel, 0)
		if err != nil {
			problems = append(problems, fmt.Sprintf("reading %s: %v", f.rel, err))
			continue
		}
		dst := filepath.Join(o.HostDir, "outputs", filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return got, err
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return got, err
		}
		got = append(got, f.rel)
	}
	if len(problems) > 0 {
		return got, errors.New(strings.Join(problems, "; "))
	}
	return got, nil
}

// place は終了コード・ログをホストに書き、ホストの結果（outputsを含む）の写しをメインの
// ゲストの`/masuda/privileged/<run-id>/`へ置く。
func (o *Options) place(ctx context.Context, res *Result) error {
	exitCode := []byte(strconv.Itoa(res.ExitCode) + "\n")
	if err := os.WriteFile(filepath.Join(o.HostDir, "exit-code"), exitCode, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(o.HostDir, "log"), []byte(res.Log), 0o600); err != nil {
		return err
	}
	dir := strings.TrimSuffix(res.ResultsDir, "/")
	if err := guest.WriteBytes(ctx, o.Sandbox, o.MainID, dir+"/exit-code", exitCode, 0o644); err != nil {
		return err
	}
	if err := guest.WriteBytes(ctx, o.Sandbox, o.MainID, dir+"/log", []byte(res.Log), 0o644); err != nil {
		return err
	}
	for _, rel := range res.Outputs {
		b, err := os.ReadFile(filepath.Join(o.HostDir, "outputs", filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if err := guest.WriteBytes(ctx, o.Sandbox, o.MainID, dir+"/outputs/"+rel, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (o *Options) shell(ctx context.Context, id, cwd, script string) (guest.ExecResult, error) {
	res, err := guest.Exec(ctx, o.Sandbox, &sandboxv1.ExecRequest{Id: id, Shell: script, Cwd: cwd})
	if err != nil {
		return res, err
	}
	if res.ExitCode != 0 || res.Signal != "" || res.TimedOut {
		return res, fmt.Errorf("guest command failed (exit %d %s): %s", res.ExitCode, res.Signal, bytes.TrimSpace(res.Stderr))
	}
	return res, nil
}

// tailBuffer は書かれたものの末尾maxバイトだけを持つ。書くたびに切り詰めると大量の出力で
// コピーが嵩むので、2倍まで溜めてから詰める。
type tailBuffer struct {
	mu        sync.Mutex
	max       int
	buf       []byte
	truncated bool
}

func (t *tailBuffer) write(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*t.max {
		t.buf = append(t.buf[:0:0], t.buf[len(t.buf)-t.max:]...)
	}
	if len(t.buf) > t.max {
		t.truncated = true
	}
}

func (t *tailBuffer) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.buf
	if len(b) > t.max {
		b = b[len(b)-t.max:]
	}
	return append([]byte(nil), b...)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
