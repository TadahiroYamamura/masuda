// Package privileged は特権コマンド（docs/design/overview.md「特権コマンド」）の方針と実行手順。
// 宣言済み・承認済みのコマンドを、sandbox serviceのRunJob（使い捨てのVM、root）で動かし、
// 結果をホストの記録とメインのゲストの`/masuda/privileged/<run-id>/`へ置く。
//
// 宣言の照合（Resolve）はこのパッケージ、宣言の読み込みとイメージのビルドは呼び出し側（serve）が行う。
// VMの作成・ファイルの投入・回収・破棄はsandbox serviceの仕事で、ここは何をどこへ渡すかだけを決める。
package privileged

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
	heartbeatEvery = 30 * time.Second
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

// Approval は宣言1つの承認の状態。
type Approval int

const (
	// NotApproved は承認の記録が無いこと。
	NotApproved Approval = iota
	// Approved は今の宣言のハッシュで承認されていること。
	Approved
	// Stale は承認の後に宣言が変わったこと（未承認と同じに扱う）。
	Stale
)

// ApprovalOf は宣言dのハッシュと、localでのnameの承認の状態を返す。
func ApprovalOf(name string, d config.PrivilegedCommandDecl, local config.LocalSettings) (string, Approval, error) {
	hash, err := config.DeclHash(d)
	if err != nil {
		return "", NotApproved, err
	}
	a, recorded := local.PrivilegedCommandsApproved[name]
	switch {
	case !recorded:
		return hash, NotApproved, nil
	case a.DeclHash != hash:
		return hash, Stale, nil
	}
	return hash, Approved, nil
}

// Resolved は動かしてよいと確かめた特権コマンド1つ。
type Resolved struct {
	Name string
	Decl config.PrivilegedCommandDecl
	// Hash は承認と一致した宣言のハッシュ。
	Hash string
	// Egress は特権VMに許すホスト（宣言と承認の積）。
	Egress []string
}

// Resolve はcfgの宣言からnameを取り出し、形を検査し、localの承認と照らして、動かしてよければ返す。
// 未宣言・形の誤り・未承認・承認後の変更はエラーにする（人間が何をすればよいかを書く）。
func Resolve(cfg config.Settings, local config.LocalSettings, name string, imageExists func(entry string) bool) (*Resolved, error) {
	decl, ok := cfg.PrivilegedCommands[name]
	if !ok {
		names := make([]string, 0, len(cfg.PrivilegedCommands))
		for n := range cfg.PrivilegedCommands {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("privileged command %q is not declared in privilegedCommands of .masuda/settings.json (declared: %v)", name, names)
	}
	if err := Validate(decl, imageExists); err != nil {
		return nil, fmt.Errorf("privileged command %q: %w", name, err)
	}
	hash, state, err := ApprovalOf(name, decl, local)
	if err != nil {
		return nil, err
	}
	switch state {
	case NotApproved:
		return nil, fmt.Errorf("privileged command %q is not approved; a human must run `masuda privileged-command approve %s`", name, name)
	case Stale:
		return nil, fmt.Errorf("privileged command %q changed since it was approved; a human must run `masuda privileged-command approve %s` again", name, name)
	}
	return &Resolved{Name: name, Decl: decl, Hash: hash, Egress: config.AllowedEgress(cfg, local)}, nil
}

// Options は1回の実行に要るもの。
type Options struct {
	Sandbox sandboxv1connect.SandboxServiceClient
	// MainID はメインのsandbox（inputsの写し元、結果の写しの置き先）。空ならメインのゲストは無い
	// （`masuda privileged-command run`）。そのときinputsはHostInputsで渡し、結果はホストにだけ置く。
	MainID  string
	RunID   string
	BuildID string
	// DiskMiB は特権sandboxのルートディスクの最小容量（宣言のイメージのエントリの設定）。
	DiskMiB uint32
	// MemoryMiB と CPUs は特権sandboxのメモリ（MiB）とCPU数（同じくイメージのエントリの設定）。
	MemoryMiB uint32
	CPUs      uint32
	// Egress は特権sandboxに許すホスト（宣言と承認の積）。
	Egress []string
	Decl   config.PrivilegedCommandDecl
	// Staging と SnapshotRef は、メインの作業ツリーを取り込んだstagingとそのref。
	Staging     *staging.Repo
	SnapshotRef string
	// HostDir は結果を残すホストのディレクトリ（`records/privileged/<run-id>`）。
	HostDir string
	// HostInputs はホストから/workspaceの下へ置くファイル。
	HostInputs []HostInput
	// Stdout と Stderr は、コマンドの出力を届いたそばから書く先（nilなら書かない）。Result.Logは
	// これとは別に末尾を持つ。
	Stdout, Stderr io.Writer
	// Heartbeat は実行中に定期的に呼ばれる（活動の記録用）。nilなら呼ばない。
	Heartbeat func()
}

// HostInput はホストの1ファイルを、特権sandboxの/workspaceからの相対パスRelへ置く指定。
type HostInput struct {
	HostPath string // 絶対パス
	Rel      string // `/`区切り
	Mode     fs.FileMode
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
	// DeniedHosts は実行中に拒否した通信先。run_privileged_commandの戻り（docs/guest-protocol.md）には含めない。
	DeniedHosts []*sandboxv1.RunJobEvent_DeniedHost `json:"-"`
}

// ResultsDir はメインのゲストでの結果の写しの場所。
func ResultsDir(runID string) string { return GuestResultsRoot + "/" + runID + "/" }

// snapshotRefPattern はsetup_shellに埋め込むref。staging.WIPRefが作る形だけを通し、
// シェルの引用を要らなくする。
var snapshotRefPattern = regexp.MustCompile(`^refs/masuda/wip/[0-9A-Za-z][0-9A-Za-z._-]*$`)

// Run はRunJobで宣言のコマンドを動かし、結果を置いて返す。コマンドが0以外で終わってもエラーには
// しない（exit_codeで返す）。スナップショットを展開できなかったときは、コマンドを動かせなかった
// 基盤の失敗としてエラーを返す。
func Run(ctx context.Context, o Options) (*Result, error) {
	if !snapshotRefPattern.MatchString(o.SnapshotRef) {
		return nil, fmt.Errorf("snapshot ref %q is not a WIP ref", o.SnapshotRef)
	}
	hostDir, err := filepath.Abs(o.HostDir)
	if err != nil {
		return nil, err
	}
	outputsDir := filepath.Join(hostDir, "outputs")
	if err := os.MkdirAll(outputsDir, 0o700); err != nil {
		return nil, err
	}
	bundle, err := o.bundle(ctx, hostDir)
	if err != nil {
		return nil, err
	}
	defer os.Remove(bundle)

	timeout := time.Duration(o.Decl.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	inputs := []*sandboxv1.RunJobRequest_Input{{Source: &sandboxv1.RunJobRequest_Input_HostFile{HostFile: &sandboxv1.RunJobRequest_HostFile{
		HostPath: bundle, GuestPath: bundleGuest, Mode: 0o644,
	}}}}
	for _, in := range o.HostInputs {
		inputs = append(inputs, &sandboxv1.RunJobRequest_Input{Source: &sandboxv1.RunJobRequest_Input_HostFile{HostFile: &sandboxv1.RunJobRequest_HostFile{
			HostPath: in.HostPath, GuestPath: guestWorkspace + "/" + in.Rel, Mode: uint32(in.Mode.Perm()),
		}}})
	}
	if len(o.Decl.Inputs) > 0 && o.MainID != "" {
		inputs = append(inputs, &sandboxv1.RunJobRequest_Input{Source: &sandboxv1.RunJobRequest_Input_FromSandbox{FromSandbox: &sandboxv1.RunJobRequest_FromSandbox{
			Id: o.MainID, Root: guestWorkspace, Patterns: o.Decl.Inputs, DestRoot: guestWorkspace,
		}}})
	}
	fin, log, setupLog, err := o.runJob(ctx, &sandboxv1.RunJobRequest{
		BuildId:   o.BuildID,
		MemoryMib: o.MemoryMiB,
		Cpus:      o.CPUs,
		DiskMib:   o.DiskMiB,
		// RunJobのVMは秘密・tcp_mapsを持たない。特権VMはAPIトークンもmasudaへの経路も持たない。
		AllowedHosts:   o.Egress,
		User:           User,
		Cwd:            guestWorkspace,
		SetupShell:     checkoutScript(o.SnapshotRef),
		Shell:          o.Decl.Command,
		TimeoutMs:      uint32(min(timeout.Milliseconds(), int64(^uint32(0)))),
		Inputs:         inputs,
		Outputs:        o.Decl.Outputs,
		OutputsHostDir: outputsDir,
	})
	if err != nil {
		return nil, err
	}
	if s := fin.Setup; s == nil || s.ExitCode != 0 || s.Signal != "" || s.TimedOut {
		return nil, fmt.Errorf("checking out the snapshot in the privileged sandbox failed (%s): %s", describeSetup(fin), strings.TrimSpace(string(setupLog.bytes())))
	}

	res := &Result{
		Log:          string(log.bytes()),
		Truncated:    log.truncated,
		Outputs:      append([]string{}, fin.Outputs...),
		OutputsError: fin.OutputsError,
		DeniedHosts:  fin.DeniedHosts,
	}
	if o.MainID != "" {
		res.ResultsDir = ResultsDir(o.RunID)
	}
	switch {
	case fin.Exited != nil:
		res.ExitCode, res.Signal, res.TimedOut = int(fin.Exited.ExitCode), fin.Exited.Signal, fin.Exited.TimedOut
		if res.Signal != "" && res.ExitCode == 0 {
			res.ExitCode = -1
		}
	case fin.JobTimedOut:
		// コマンドの期限より後に来るはずのジョブ全体の期限が先に過ぎた（VMごと壊され、終了コードも
		// outputsも無い）。エージェントから見ればコマンドの時間切れと同じなので、timed_outに寄せる。
		res.ExitCode, res.TimedOut = -1, true
		if len(o.Decl.Outputs) > 0 && res.OutputsError == "" {
			res.OutputsError = "outputs were not collected: the job deadline passed"
		}
	default:
		return nil, errors.New("running the privileged command: the job finished without running the command")
	}
	if err := placeHost(hostDir, res); err != nil {
		return nil, err
	}
	if o.MainID != "" {
		if err := o.placeGuest(ctx, outputsDir, res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// bundle はstagingのスナップショットをホストの一時ファイルのbundleにする。sandbox serviceが
// HostFileとして読むので、serviceと同じアカウントが読める記録のディレクトリに置く。
func (o *Options) bundle(ctx context.Context, hostDir string) (string, error) {
	tmp, err := os.CreateTemp(hostDir, ".bundle-*")
	if err != nil {
		return "", err
	}
	tmp.Close()
	if err := o.Staging.CreateBundle(ctx, tmp.Name(), o.SnapshotRef); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("bundling the snapshot: %w", err)
	}
	return tmp.Name(), nil
}

// checkoutScript はbundleのスナップショットを/workspace（RunJobのcwd）へ展開するsetup_shell。
// cloneでなくinit→fetchにしているのは、スナップショットのrefが`refs/masuda/wip/…`で、
// cloneが既定で取ってくる範囲（ブランチとタグ）に入らないため。checkoutに-fを付けるのは、
// RunJobがsetup_shellより先にinputsを/workspaceへ置くため。inputsが追跡対象のファイルに
// 当たると、-fが無ければcheckoutが「上書きされる未追跡のファイル」で止まる。そのファイルは
// スナップショットの中身になるが、どちらも同じ時点のメインの作業ツリーから来ている。
func checkoutScript(ref string) string {
	return strings.Join([]string{
		"set -e",
		// 実VMは書き手に関わらずファイルに所有者を付けるので、所有者の食い違いでgitが止まらないように。
		"git config --global --add safe.directory " + guestWorkspace,
		"git init -q .",
		"git fetch -q --no-tags " + bundleGuest + " +" + ref + ":" + ref,
		"git checkout -q -f --detach " + ref,
		"rm -f " + bundleGuest,
	}, "\n")
}

func describeSetup(fin *sandboxv1.RunJobEvent_Finished) string {
	s := fin.Setup
	switch {
	case s == nil && fin.JobTimedOut:
		return "the job deadline passed"
	case s == nil:
		return "it did not run"
	case s.TimedOut:
		return "timed out"
	case s.Signal != "":
		return "signal " + s.Signal
	}
	return "exit " + strconv.Itoa(int(s.ExitCode))
}

// runJob はRunJobを呼び、shellの出力（log）とsetup_shellの出力（setupLog）を分けて集める。
// どちらもstdoutとstderrを届いた順に1本にまとめる。
func (o *Options) runJob(ctx context.Context, req *sandboxv1.RunJobRequest) (*sandboxv1.RunJobEvent_Finished, *tailBuffer, *tailBuffer, error) {
	// Heartbeatはイベントの到着ではなく一定の間隔で呼ぶ。テストやビルドは何分も出力せずに動くことが
	// あり、イベントのたびに呼ぶとその間は活動が途絶えたように見えるため。
	if o.Heartbeat != nil {
		hbCtx, stop := context.WithCancel(ctx)
		defer stop()
		go func() {
			t := time.NewTicker(heartbeatEvery)
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
	st, err := o.Sandbox.RunJob(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("running the privileged command: %w", err)
	}
	defer st.Close()
	log := &tailBuffer{max: LogTailBytes}
	setupLog := &tailBuffer{max: LogTailBytes}
	cur := setupLog
	var fin *sandboxv1.RunJobEvent_Finished
	for st.Receive() {
		switch ev := st.Msg().Event.(type) {
		case *sandboxv1.RunJobEvent_Phase_:
			if ev.Phase.Name == "running" {
				cur = log
			}
		case *sandboxv1.RunJobEvent_Stdout:
			cur.write(ev.Stdout)
			if cur == log && o.Stdout != nil {
				_, _ = o.Stdout.Write(ev.Stdout)
			}
		case *sandboxv1.RunJobEvent_Stderr:
			cur.write(ev.Stderr)
			if cur == log && o.Stderr != nil {
				_, _ = o.Stderr.Write(ev.Stderr)
			}
		case *sandboxv1.RunJobEvent_Finished_:
			fin = ev.Finished
		}
	}
	if err := st.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("running the privileged command: %w", err)
	}
	if fin == nil {
		return nil, nil, nil, errors.New("running the privileged command: the job stream ended without Finished")
	}
	return fin, log, setupLog, nil
}

// placeHost は終了コード・ログをホストの記録に書く。outputsはRunJobが既に`outputs/`へ書いている。
func placeHost(hostDir string, res *Result) error {
	if err := os.WriteFile(filepath.Join(hostDir, "exit-code"), exitCodeFile(res), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(hostDir, "log"), []byte(res.Log), 0o600)
}

// placeGuest はホストの結果（outputsを含む）の写しをメインのゲストの`/masuda/privileged/<run-id>/`へ置く。
func (o *Options) placeGuest(ctx context.Context, outputsDir string, res *Result) error {
	dir := strings.TrimSuffix(res.ResultsDir, "/")
	if err := guest.WriteBytes(ctx, o.Sandbox, o.MainID, dir+"/exit-code", exitCodeFile(res), 0o644); err != nil {
		return err
	}
	if err := guest.WriteBytes(ctx, o.Sandbox, o.MainID, dir+"/log", []byte(res.Log), 0o644); err != nil {
		return err
	}
	for _, rel := range res.Outputs {
		b, err := os.ReadFile(filepath.Join(outputsDir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if err := guest.WriteBytes(ctx, o.Sandbox, o.MainID, dir+"/outputs/"+rel, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func exitCodeFile(res *Result) []byte { return []byte(strconv.Itoa(res.ExitCode) + "\n") }

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
