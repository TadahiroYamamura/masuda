// Package runner はengine.Runnerの実装。engineが自分の記録の外で行うこと
// （sandboxでのコマンド実行・ゲストとのファイルのやり取り・stagingの操作・データの保存・
// ゲートの記録・実行ログ）を、1つのワークスペースについて受け持つ。
//
// ゲストで動かすコマンドはcwdを決めて相対パスで書く。フェイクsandboxはcwdだけを
// ゲストroot下へ写像し、コマンド文字列中の絶対パスは写像しないので、こう書けば
// 実VMとフェイクの両方で同じコマンドが通る。
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/TadahiroYamamura/masuda-engine/engine"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
	"github.com/TadahiroYamamura/masuda/internal/guest"
	"github.com/TadahiroYamamura/masuda/internal/perspectives"
	"github.com/TadahiroYamamura/masuda/internal/staging"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// Options はRunnerを作るのに要るもの。
type Options struct {
	Workspace *workspace.Workspace
	Sandbox   sandboxv1connect.SandboxServiceClient
	// SandboxID はワークスペースのsandbox（ワークスペースIDと同じ）。
	SandboxID string
	// Author はstagingのコミットとゲストのWIPスナップショットの作者。
	Author staging.Identity
	// Reviews は対象リポジトリの`.masuda/reviews`（無ければnil）。同じ名前の同梱観点を置き換える。
	Reviews fs.FS
	// AlwaysHosts・AlwaysSecrets は、どのノードの方針にも足すもの（Claude APIへの経路）。
	AlwaysHosts   []string
	AlwaysSecrets []string
	// OnLog は実行記録を1行書くたびに呼ばれる（公開APIのWatchへ流す）。nilなら呼ばない。
	OnLog func(engine.Event)
}

// Runner は1つのワークスペースのengine.Runner。
type Runner struct {
	o       Options
	ws      *workspace.Workspace
	repo    *staging.Repo
	logMu   sync.Mutex
	guestMu sync.Mutex // ゲストのgit操作（index・refs/masuda/snapshot）を直列にする
}

var _ engine.Runner = (*Runner)(nil)

// New はRunnerを作る。
func New(o Options) *Runner {
	return &Runner{o: o, ws: o.Workspace, repo: staging.Open(o.Workspace.StagingDir())}
}

// ゲスト内の受け渡しの場所（docs/design/overview.md「ゲスト内の配置」）。
const (
	guestIn        = "/masuda/in"
	guestOut       = "/masuda/out"
	guestWorkspace = "/workspace"
	// snapshotRef はゲストでWIPスナップショットを一時的に指すref。bundleに載せるためだけに使う。
	snapshotRef = "refs/masuda/snapshot"
	// worktreeRef はstagingで「最後に取り込んだ作業ツリー」を指すref。出現の境界でない
	// 取り込み（ChangedSince・Diff・Commitの直前）の置き場所。
	worktreeRef = "refs/masuda/worktree"
	// logTailBytes はexecノードの出力のうちフィードバックへ渡す末尾の長さ。
	logTailBytes = 8 << 10
)

// InDir・OutDir はその出現の入力・出力のゲストのパス。
func InDir(occ string) string  { return guestIn + "/" + occ }
func OutDir(occ string) string { return guestOut + "/" + occ }

// occPattern はengineの出現ID・フレームID（"0003"、"0003.1"）。dataNamePatternはデータ名。
// どちらもホストとゲストのパスに使うので、パスを壊す文字を通さない。
var (
	occPattern      = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]*$`)
	dataNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

func checkOcc(occ string) error {
	if !occPattern.MatchString(occ) || strings.Contains(occ, "..") {
		return fmt.Errorf("occurrence %q is not a valid id", occ)
	}
	return nil
}

func checkName(name string) error {
	if !dataNamePattern.MatchString(name) {
		return fmt.Errorf("data name %q is not valid", name)
	}
	return nil
}

func (r *Runner) gitEnv() map[string]string {
	name, email := r.o.Author.Name, r.o.Author.Email
	if name == "" {
		name = "masuda"
	}
	if email == "" {
		email = "masuda@localhost"
	}
	return map[string]string{
		"GIT_AUTHOR_NAME": name, "GIT_AUTHOR_EMAIL": email,
		"GIT_COMMITTER_NAME": name, "GIT_COMMITTER_EMAIL": email,
	}
}

func (r *Runner) shell(ctx context.Context, cwd, script string) (guest.ExecResult, error) {
	res, err := guest.Exec(ctx, r.o.Sandbox, &sandboxv1.ExecRequest{Id: r.o.SandboxID, Shell: script, Cwd: cwd, Env: r.gitEnv()})
	if err != nil {
		return res, err
	}
	if res.ExitCode != 0 || res.Signal != "" || res.TimedOut {
		return res, fmt.Errorf("guest command failed (exit %d %s): %s", res.ExitCode, res.Signal, bytes.TrimSpace(res.Stderr))
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// Policy
// ---------------------------------------------------------------------------

func (r *Runner) SetPolicy(ctx context.Context, _ engine.RunID, p engine.Policy) error {
	_, err := r.o.Sandbox.SetPolicy(ctx, connect.NewRequest(&sandboxv1.SetPolicyRequest{
		Id: r.o.SandboxID,
		Policy: &sandboxv1.Policy{
			AllowedHosts:   union(r.o.AlwaysHosts, p.Egress),
			EnabledSecrets: union(r.o.AlwaysSecrets, p.Secrets),
		},
	}))
	return err
}

func union(a, b []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range append(append([]string(nil), a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Data
// ---------------------------------------------------------------------------

// runInputsDir は実行開始時の入力（Occurrence ""）を置く`data/`の下の名前。
// 出現IDは英数字で始まるので衝突しない。
const runInputsDir = "_run"

func (r *Runner) dataPath(ref engine.DataRef) (string, error) {
	if err := checkName(ref.Name); err != nil {
		return "", err
	}
	dir := runInputsDir
	if ref.Occurrence != "" {
		if err := checkOcc(ref.Occurrence); err != nil {
			return "", err
		}
		dir = ref.Occurrence
	}
	return filepath.Join(r.ws.DataDir(), dir, ref.Name), nil
}

func (r *Runner) PutData(_ context.Context, _ engine.RunID, ref engine.DataRef, content []byte) error {
	p, err := r.dataPath(ref)
	if err != nil {
		return err
	}
	return writeFileAtomic(p, content)
}

func (r *Runner) GetData(_ context.Context, _ engine.RunID, ref engine.DataRef) ([]byte, error) {
	p, err := r.dataPath(ref)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// latestData はnameの値のうち最も新しい出現のもの（無ければ実行開始時の入力）を返す。
// exportの書き出し用。出現IDはゼロ埋めの通し番号なので文字列順が時刻順になる。
func (r *Runner) latestData(name string) ([]byte, bool) {
	entries, err := os.ReadDir(r.ws.DataDir())
	if err != nil {
		return nil, false
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != runInputsDir {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	dirs = append([]string{runInputsDir}, dirs...)
	for i := len(dirs) - 1; i >= 0; i-- {
		if b, err := os.ReadFile(filepath.Join(r.ws.DataDir(), dirs[i], name)); err == nil {
			return b, true
		}
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// Agent tasks
// ---------------------------------------------------------------------------

func (r *Runner) ReadOutput(ctx context.Context, _ engine.RunID, occ, name string) ([]byte, bool, error) {
	if err := checkOcc(occ); err != nil {
		return nil, false, err
	}
	if err := checkName(name); err != nil {
		return nil, false, err
	}
	b, err := guest.ReadFile(ctx, r.o.Sandbox, r.o.SandboxID, OutDir(occ)+"/"+name, 0)
	if guest.IsNotFound(err) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// WriteOutput はエージェントがMCPのwrite_outputで渡した内容を、その出現の出力として
// ゲストの`/masuda/out/<occ>/<name>`へ置く。検証は呼び出し側（engineのスキーマ）が先に行う。
func (r *Runner) WriteOutput(ctx context.Context, occ, name string, content []byte) error {
	if err := checkOcc(occ); err != nil {
		return err
	}
	if err := checkName(name); err != nil {
		return err
	}
	return guest.WriteBytes(ctx, r.o.Sandbox, r.o.SandboxID, OutDir(occ)+"/"+name, content, 0o644)
}

// writeInputs はinputsの値をゲストの`/masuda/in/<occ>/<name>`へ置き、名前→ゲストのパスを返す。
func (r *Runner) writeInputs(ctx context.Context, occ string, inputs map[string]engine.DataRef) (map[string]string, error) {
	paths := map[string]string{}
	for name, ref := range inputs {
		if err := checkName(name); err != nil {
			return nil, err
		}
		b, err := r.GetData(ctx, "", ref)
		if err != nil {
			return nil, fmt.Errorf("input %s: %w", name, err)
		}
		p := InDir(occ) + "/" + name
		if err := guest.WriteBytes(ctx, r.o.Sandbox, r.o.SandboxID, p, b, 0o644); err != nil {
			return nil, err
		}
		paths[name] = p
	}
	return paths, nil
}

// MaterializeTask はエージェントのタスクの入力とタスクファイルをゲストへ置き、
// タスクファイルのゲストのパス（`/masuda/in/<occ>/task.md`）を返す。何度呼んでも同じものを書く。
func (r *Runner) MaterializeTask(ctx context.Context, t *engine.AgentTask) (string, error) {
	if err := checkOcc(t.Occurrence); err != nil {
		return "", err
	}
	paths, err := r.writeInputs(ctx, t.Occurrence, t.Inputs)
	if err != nil {
		return "", err
	}
	p := InDir(t.Occurrence) + "/task.md"
	if err := guest.WriteBytes(ctx, r.o.Sandbox, r.o.SandboxID, p, TaskFile(t, paths), 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// Exec nodes
// ---------------------------------------------------------------------------

func (r *Runner) RunCommand(ctx context.Context, t engine.CommandTask) (engine.CommandResult, error) {
	if err := checkOcc(t.Occurrence); err != nil {
		return engine.CommandResult{}, err
	}
	if len(t.Command) == 0 {
		return engine.CommandResult{}, errors.New("exec: empty command")
	}
	if _, err := r.writeInputs(ctx, t.Occurrence, t.Inputs); err != nil {
		return engine.CommandResult{}, err
	}
	// 出力ディレクトリはコマンドが書けるよう先に作る。cwd `/`から相対パスで書く（パッケージの説明）。
	if _, err := r.shell(ctx, "/", "mkdir -p "+shellQuote(strings.TrimPrefix(OutDir(t.Occurrence), "/"))); err != nil {
		return engine.CommandResult{}, err
	}
	env := r.gitEnv()
	env["MASUDA_OCCURRENCE"] = t.Occurrence
	env["MASUDA_IN"] = InDir(t.Occurrence)
	env["MASUDA_OUT"] = OutDir(t.Occurrence)
	res, err := guest.Exec(ctx, r.o.Sandbox, &sandboxv1.ExecRequest{
		Id:        r.o.SandboxID,
		Argv:      t.Command,
		Cwd:       guestWorkspace,
		Env:       env,
		TimeoutMs: uint32(min(t.Timeout.Milliseconds(), int64(^uint32(0)))),
	})
	if err != nil {
		return engine.CommandResult{}, err
	}
	out := engine.CommandResult{ExitCode: int(res.ExitCode), TimedOut: res.TimedOut, Outputs: map[string][]byte{}}
	if res.Signal != "" && out.ExitCode == 0 {
		out.ExitCode = -1
	}
	log := append(append([]byte(nil), res.Stdout...), res.Stderr...)
	if len(log) > logTailBytes {
		log = log[len(log)-logTailBytes:]
	}
	out.LogTail = string(log)
	for _, name := range t.Outputs {
		b, ok, err := r.ReadOutput(ctx, "", t.Occurrence, name)
		if err != nil {
			return engine.CommandResult{}, err
		}
		if ok {
			out.Outputs[name] = b
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Snapshots and diffs
// ---------------------------------------------------------------------------

// capture はゲストの作業ツリー全体（gitignore対象を除く）をコミットにしてstagingのdstRefへ
// 取り込み、そのコミットを返す。ゲストで`git add -A`→`write-tree`→`commit-tree`（親はHEAD）
// してbundleにし、ホストがReadFileで読む。bundleはHEADを前提に差分だけを載せる
// （ゲストのHEADは必ずstagingにあるコミット: cloneした先頭か、commitで知らせた先頭）。
func (r *Runner) capture(ctx context.Context, label, dstRef string) (string, error) {
	r.guestMu.Lock()
	defer r.guestMu.Unlock()
	bundleName := "snapshot-" + label + ".bundle"
	script := strings.Join([]string{
		"set -e",
		"git add -A",
		`tree=$(git write-tree)`,
		`commit=$(git commit-tree "$tree" -p HEAD -m ` + shellQuote("masuda snapshot "+label) + `)`,
		`git update-ref ` + snapshotRef + ` "$commit"`,
		"mkdir -p ../masuda/snapshots",
		"git bundle create ../masuda/snapshots/" + bundleName + " " + snapshotRef + " ^HEAD 2>/dev/null",
	}, "\n")
	if _, err := r.shell(ctx, guestWorkspace, script); err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	guestPath := "/masuda/snapshots/" + bundleName
	defer func() {
		_, _ = r.shell(context.WithoutCancel(ctx), "/", "rm -f "+shellQuote(strings.TrimPrefix(guestPath, "/")))
	}()
	b, err := guest.ReadFile(ctx, r.o.Sandbox, r.o.SandboxID, guestPath, 0)
	if err != nil {
		return "", fmt.Errorf("snapshot: reading bundle: %w", err)
	}
	tmp, err := os.CreateTemp(r.ws.Dir, ".snapshot-*.bundle")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return r.repo.FetchBundle(ctx, tmp.Name(), snapshotRef, dstRef)
}

// Snapshot はノード境界のWIPスナップショットを`refs/masuda/wip/<occ>`に置き、そのコミットの
// ハッシュを返す。refでなくハッシュを返すのは、engineが同じ出現で進入時と終了時の2回
// スナップショットを取ることがあり、refは後の方で上書きされるため。
func (r *Runner) Snapshot(ctx context.Context, _ engine.RunID, occ string) (engine.SnapshotRef, error) {
	if err := checkOcc(occ); err != nil {
		return "", err
	}
	c, err := r.capture(ctx, occ, staging.WIPRef(occ))
	if err != nil {
		return "", err
	}
	return engine.SnapshotRef(c), nil
}

// current は今のゲストの作業ツリーを取り込む。ChangedSince・Diff・Commitは直近の
// 境界のスナップショットではなくこれを見る。engineは書き込めないエージェントの終了時に
// スナップショットより先にChangedSinceを呼ぶので、境界のものを使うと変更を見落とす。
func (r *Runner) current(ctx context.Context) (string, error) {
	return r.capture(ctx, "worktree", worktreeRef)
}

func (r *Runner) Diff(ctx context.Context, run engine.RunID, kind engine.DiffKind, from engine.SnapshotRef, into engine.DataRef) error {
	cur, err := r.current(ctx)
	if err != nil {
		return err
	}
	var base string
	switch kind {
	case engine.DiffFromBase:
		base = staging.BaseRef
	case engine.DiffFromHead:
		base = staging.BranchRef(r.ws.Branch)
	case engine.DiffFromRef:
		if from == "" {
			return errors.New("fix-diff needs a snapshot to start from")
		}
		base = string(from)
	default:
		return fmt.Errorf("unknown diff kind %q", kind)
	}
	d, err := r.repo.Diff(ctx, base, cur, nil)
	if err != nil {
		return err
	}
	return r.PutData(ctx, run, into, []byte(d))
}

func (r *Runner) ChangedSince(ctx context.Context, _ engine.RunID, from engine.SnapshotRef) ([]string, string, error) {
	cur, err := r.current(ctx)
	if err != nil {
		return nil, "", err
	}
	base := string(from)
	if base == "" {
		base = staging.BranchRef(r.ws.Branch)
	}
	return r.repo.Changes(ctx, base, cur)
}

// ---------------------------------------------------------------------------
// Items
// ---------------------------------------------------------------------------

func (r *Runner) Items(ctx context.Context, run engine.RunID, over string, from engine.DataRef) ([]engine.Item, error) {
	switch {
	case over == "steps":
		return r.stepItems(ctx, run, from)
	case over == "findings":
		return r.findingItems(ctx, run, from)
	case over == "perspectives":
		return r.perspectiveItems(nil)
	case strings.HasPrefix(over, "perspectives(from="):
		b, err := r.GetData(ctx, run, from)
		if err != nil {
			return nil, err
		}
		var ids []string
		if err := json.Unmarshal(b, &ids); err != nil {
			return nil, fmt.Errorf("%s: %w", from.Name, err)
		}
		return r.perspectiveItems(ids)
	case strings.HasSuffix(over, "[]"):
		b, err := r.GetData(ctx, run, from)
		if err != nil {
			return nil, err
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(b, &arr); err != nil {
			return nil, fmt.Errorf("%s is not a JSON array: %w", from.Name, err)
		}
		items := make([]engine.Item, 0, len(arr))
		for i, raw := range arr {
			content := []byte(raw)
			// 文字列の要素はJSONの引用符を外して渡す。本文として読むものが大半のため。
			var s string
			if json.Unmarshal(raw, &s) == nil {
				content = []byte(s)
			}
			items = append(items, engine.Item{Key: strconv.Itoa(i + 1), Content: content})
		}
		return items, nil
	}
	return nil, fmt.Errorf("foreach over %q is not supported", over)
}

func (r *Runner) stepItems(ctx context.Context, run engine.RunID, from engine.DataRef) ([]engine.Item, error) {
	b, err := r.GetData(ctx, run, from)
	if err != nil {
		return nil, err
	}
	var plan struct {
		Steps []json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(b, &plan); err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	done, err := r.committedSteps()
	if err != nil {
		return nil, err
	}
	var items []engine.Item
	for i, raw := range plan.Steps {
		var s struct {
			Number int `json:"number"`
		}
		_ = json.Unmarshal(raw, &s)
		key := strconv.Itoa(s.Number)
		if s.Number == 0 {
			key = strconv.Itoa(i + 1)
		}
		items = append(items, engine.Item{Key: key, Input: "step", Content: raw, Done: done[key]})
	}
	return items, nil
}

func (r *Runner) findingItems(ctx context.Context, run engine.RunID, from engine.DataRef) ([]engine.Item, error) {
	b, err := r.GetData(ctx, run, from)
	if err != nil {
		return nil, err
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		return nil, fmt.Errorf("findings: %w", err)
	}
	var items []engine.Item
	for i, raw := range arr {
		var f struct {
			Autofix bool `json:"autofix"`
		}
		_ = json.Unmarshal(raw, &f)
		// 自動修正の対象（autofix: true）だけを回す。残りは人間が読むための指摘。
		if !f.Autofix {
			continue
		}
		items = append(items, engine.Item{Key: strconv.Itoa(i + 1), Input: "finding", Content: raw})
	}
	return items, nil
}

// perspectiveItems は観点を項目にする。idsがnilなら全観点、そうでなければその順に。
// 対象リポジトリの`.masuda/reviews/<id>.md`は同じidの同梱観点を丸ごと置き換える。
func (r *Runner) perspectiveItems(ids []string) ([]engine.Item, error) {
	all := map[string][]byte{}
	for _, src := range []fs.FS{perspectives.Builtin(), r.o.Reviews} {
		if src == nil {
			continue
		}
		entries, err := fs.ReadDir(src, ".")
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || path.Ext(e.Name()) != ".md" {
				continue
			}
			b, err := fs.ReadFile(src, e.Name())
			if err != nil {
				return nil, err
			}
			all[strings.TrimSuffix(e.Name(), ".md")] = b
		}
	}
	if ids == nil {
		for id := range all {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	var items []engine.Item
	for _, id := range ids {
		b, ok := all[id]
		if !ok {
			return nil, fmt.Errorf("perspective %q does not exist", id)
		}
		items = append(items, engine.Item{Key: id, Input: "perspective", Content: b})
	}
	return items, nil
}

// committedSteps はscope: stepのcommitで記録したステップ（`records/committed-steps.json`）。
// 再開時に、コミット済みのステップをforeachが飛ばせるようにする。
func (r *Runner) committedSteps() (map[string]bool, error) {
	done := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(r.ws.RecordsDir(), "committed-steps.json"))
	if errors.Is(err, os.ErrNotExist) {
		return done, nil
	} else if err != nil {
		return nil, err
	}
	var keys []string
	if err := json.Unmarshal(b, &keys); err != nil {
		return nil, err
	}
	for _, k := range keys {
		done[k] = true
	}
	return done, nil
}

func (r *Runner) markStepCommitted(step string) error {
	done, err := r.committedSteps()
	if err != nil {
		return err
	}
	done[step] = true
	var keys []string
	for k := range done {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(r.ws.RecordsDir(), "committed-steps.json"), b)
}

// ---------------------------------------------------------------------------
// Humans
// ---------------------------------------------------------------------------

func (r *Runner) OpenGate(ctx context.Context, g engine.GateRequest) error {
	rec := &workspace.GateRecord{
		Occurrence: g.Occurrence,
		Gate:       g.Gate,
		Target:     g.Target,
		TargetHash: g.TargetHash,
		Subject:    g.Subject,
		OpenedAt:   time.Now().UTC(),
	}
	if g.Target == string(engine.DiffFromBase) {
		c, err := r.repo.ResolveCommit(ctx, staging.BranchRef(r.ws.Branch))
		if err != nil {
			return err
		}
		rec.StagingCommit = c
	}
	return r.ws.AddGate(rec)
}

func (r *Runner) OpenQuestion(_ context.Context, q engine.QuestionRequest) error {
	rec := &workspace.QuestionRecord{Occurrence: q.Occurrence, OpenedAt: time.Now().UTC()}
	for _, it := range q.Questions {
		rec.Questions = append(rec.Questions, workspace.QuestionItem{ID: it.ID, Text: it.Text, Options: it.Options})
	}
	return r.ws.AddQuestion(rec)
}

// ---------------------------------------------------------------------------
// Commit, publish, discard
// ---------------------------------------------------------------------------

func (r *Runner) Commit(ctx context.Context, c engine.CommitRequest) (engine.CommitResult, error) {
	branchRef := staging.BranchRef(r.ws.Branch)
	head, err := r.repo.ResolveCommit(ctx, branchRef)
	if err != nil {
		return engine.CommitResult{}, err
	}
	cur, err := r.current(ctx)
	if err != nil {
		return engine.CommitResult{}, err
	}
	res, err := r.repo.Commit(ctx, staging.CommitOptions{
		Branch:     r.ws.Branch,
		WIP:        cur,
		Allowed:    c.Allowed,
		Byproducts: c.Byproducts,
		Message:    c.Message,
		Author:     r.o.Author,
	})
	if err != nil {
		return engine.CommitResult{}, err
	}
	if len(res.Deviations) > 0 {
		_, hash, err := r.repo.Changes(ctx, head, cur)
		if err != nil {
			return engine.CommitResult{}, err
		}
		return engine.CommitResult{Deviations: res.Deviations, DeviationsHash: hash}, nil
	}
	if res.Commit != head {
		if err := r.syncGuestHead(ctx, head); err != nil {
			return engine.CommitResult{}, fmt.Errorf("telling the guest about %s: %w", res.Commit, err)
		}
	}
	if c.Scope == "step" && c.Step != "" {
		if err := r.markStepCommitted(c.Step); err != nil {
			return engine.CommitResult{}, err
		}
	}
	return engine.CommitResult{Commit: res.Commit}, nil
}

// syncGuestHead はstagingのブランチの新しい先頭をゲストへ渡し、`fetch`と`reset --soft`で
// ゲストのHEADを合わせる。作業ツリーとindexは触らない（コミットに入らなかった変更を残す）。
func (r *Runner) syncGuestHead(ctx context.Context, oldHead string) error {
	r.guestMu.Lock()
	defer r.guestMu.Unlock()
	branchRef := staging.BranchRef(r.ws.Branch)
	tmp, err := os.CreateTemp(r.ws.Dir, ".sync-*.bundle")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	// git bundle createは既存のファイルがあると失敗するので、名前だけ確保して消しておく。
	os.Remove(tmp.Name())
	if err := r.repo.CreateBundle(ctx, tmp.Name(), branchRef, "^"+oldHead); err != nil {
		return err
	}
	f, err := os.Open(tmp.Name())
	if err != nil {
		return err
	}
	defer f.Close()
	const guestBundle = "/masuda/sync.bundle"
	if err := guest.WriteFile(ctx, r.o.Sandbox, r.o.SandboxID, guestBundle, f, 0o644); err != nil {
		return err
	}
	rel := "../" + strings.TrimPrefix(guestBundle, "/")
	script := "git fetch --quiet --no-tags " + rel + " " + shellQuote(branchRef) +
		" && git reset --quiet --soft FETCH_HEAD && rm -f " + rel
	_, err = r.shell(ctx, guestWorkspace, script)
	return err
}

func (r *Runner) Publish(ctx context.Context, p engine.PublishRequest) error {
	switch p.Target {
	case "", "local":
		if err := r.repo.PublishLocal(ctx, r.ws.RepoRoot, r.ws.Branch, p.Commit); err != nil {
			return err
		}
	case "remote":
		// 送り先のremoteを選ぶ設定は設定の項目（M6）で入れる。それまではoriginに送る。
		url, err := staging.RemoteURL(ctx, r.ws.RepoRoot, "origin")
		if err != nil {
			return err
		}
		if err := r.repo.PushRemote(ctx, url, r.ws.Branch, p.Commit); err != nil {
			return err
		}
	default:
		return fmt.Errorf("publish target %q is not supported", p.Target)
	}
	return r.finish(ctx, p.Export)
}

func (r *Runner) Discard(ctx context.Context, _ engine.RunID, export []string) error {
	return r.finish(ctx, export)
}

// finish はexportsを書き出してからsandboxを壊す。VMを先に壊すと、書き出しに失敗したときに
// 調べる手がかりが残らないため。
func (r *Runner) finish(ctx context.Context, export []string) error {
	if err := r.WriteExports(export); err != nil {
		return err
	}
	_, err := r.o.Sandbox.DestroySandbox(ctx, connect.NewRequest(&sandboxv1.DestroySandboxRequest{Id: r.o.SandboxID}))
	if connect.CodeOf(err) == connect.CodeNotFound {
		err = nil
	}
	return err
}

// WriteExports は`exports/`へexportで指定されたデータ（最新の値）と実行ログを書き出す。
// 実行ログは書き出した時点までのもの。実行が終わったときに呼び直せば最後まで写る。
func (r *Runner) WriteExports(export []string) error {
	dir := r.ws.ExportsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, name := range export {
		if err := checkName(name); err != nil {
			return err
		}
		b, ok := r.latestData(name)
		if !ok {
			return fmt.Errorf("export %q: no such data", name)
		}
		if err := writeFileAtomic(filepath.Join(dir, name), b); err != nil {
			return err
		}
	}
	return r.ExportLog()
}

// ExportLog は実行ログを`exports/execution-log.jsonl`へ写す。
func (r *Runner) ExportLog() error {
	r.logMu.Lock()
	b, err := os.ReadFile(r.logPath())
	r.logMu.Unlock()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeFileAtomic(filepath.Join(r.ws.ExportsDir(), "execution-log.jsonl"), b)
}

// ---------------------------------------------------------------------------
// Log
// ---------------------------------------------------------------------------

func (r *Runner) logPath() string { return filepath.Join(r.ws.RecordsDir(), "execution-log.jsonl") }

// Log は実行記録を1行追記する。engine.Runner.Logはエラーを返せないので、書けなかった
// 行は落とす（engine自身の記録はStoreにあり、これは人間とexports向けの写し）。
func (r *Runner) Log(e engine.Event) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	r.logMu.Lock()
	defer r.logMu.Unlock()
	f, err := os.OpenFile(r.logPath(), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	f.Close()
	if r.o.OnLog != nil {
		r.o.OnLog(e)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
