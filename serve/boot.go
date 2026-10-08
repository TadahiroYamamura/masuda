package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	"github.com/TadahiroYamamura/masuda-engine/engine"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/claudedir"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/guest"
	"github.com/TadahiroYamamura/masuda/internal/mcp"
	"github.com/TadahiroYamamura/masuda/internal/perspectives"
	"github.com/TadahiroYamamura/masuda/internal/pitfalls"
	"github.com/TadahiroYamamura/masuda/internal/privileged"
	"github.com/TadahiroYamamura/masuda/internal/runner"
	"github.com/TadahiroYamamura/masuda/internal/staging"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// claudeAPIHost はClaude APIのホスト。どのノードの方針でも許可し、トークンの置換先にする。
const claudeAPIHost = "api.anthropic.com"

// loadDefinitions はdir（対象リポジトリの`.masuda/`か、その写し）と同梱の定義を読み込む。
func loadDefinitions(dir string) (*engine.Set, error) {
	var repo fs.FS
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		repo = os.DirFS(dir)
	}
	return engine.Load(repo, engine.Bundled())
}

// snapshotReviews はwの観点の写し（ReviewsDir）を、無ければ定義の写しの`reviews/`と同梱の観点から
// 作って読む。ゲストのcloneの`.masuda/reviews/`を使わないのは、`.masuda/`をコミットしていない
// リポジトリでも観点が揃うようにするため（docs/guest-protocol.md）。写しが既にあれば
// 作り直さないので、再開しても実行開始時と同じ観点で続く。
func snapshotReviews(w *workspace.Workspace) (map[string][]byte, error) {
	var repo fs.FS
	dir := filepath.Join(w.DefinitionsDir(), "reviews")
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		repo = os.DirFS(dir)
	}
	if err := perspectives.Snapshot(repo, w.ReviewsDir()); err != nil {
		return nil, fmt.Errorf("snapshotting review perspectives: %w", err)
	}
	return perspectives.Load(w.ReviewsDir())
}

// loadPitfalls はdir（`.masuda/`かその写し）の落とし穴を読んで検査し、ゲストへ置く中身を返す。
// ファイルが無ければnil。観点と違って同梱のものは無く、写しは定義の写し（copyDefinitions）が
// そのまま兼ねるので、再開しても実行開始時と同じ中身になる。
func loadPitfalls(dir string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(dir, pitfalls.FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	out, err := pitfalls.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", config.DirName, pitfalls.FileName, err)
	}
	return out, nil
}

// guestPrivilegedCommand はゲストの`/masuda/privileged-commands.json`の1項目。
type guestPrivilegedCommand struct {
	Description    string   `json:"description,omitempty"`
	Command        string   `json:"command"`
	Image          string   `json:"image"`
	Inputs         []string `json:"inputs"`
	Outputs        []string `json:"outputs"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
}

// privilegedCommandsForGuest はdir（定義の写し）の特権コマンドの宣言を、ゲストへ置くJSONにする。
// 宣言が無ければnil。承認の状態（settings.local.json）は写さない。timeoutSecondsは省略時の
// 既定を埋めた値にする（エージェントが実際の上限を知れるように）。写しから作るので、再開しても
// 実行開始時と同じ中身になる。
func privilegedCommandsForGuest(dir string) ([]byte, error) {
	cfg, err := config.LoadDir(dir)
	if err != nil {
		return nil, err
	}
	if len(cfg.PrivilegedCommands) == 0 {
		return nil, nil
	}
	out := map[string]guestPrivilegedCommand{}
	for name, d := range cfg.PrivilegedCommands {
		timeout := d.TimeoutSeconds
		if timeout == 0 {
			timeout = int(privileged.DefaultTimeout.Seconds())
		}
		out[name] = guestPrivilegedCommand{
			Description: d.Description, Command: d.Command, Image: d.Image,
			Inputs: append([]string{}, d.Inputs...), Outputs: append([]string{}, d.Outputs...), TimeoutSeconds: timeout,
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// copyDefinitions は対象リポジトリの`.masuda/`をdstへ写す（無ければ空のdstを作る）。
// 実行は写しの定義で進めるので、実行中に作業ツリーの定義を書き換えても、再開やserveの
// 再起動の後に別の定義で組み直されることがない。シンボリックリンクは写さない
// （リポジトリの外を指していても読まないため）。`claude/`と`claude.local/`はそのまま写さず、
// 重ねた結果を`claude/`に置く（無視したものは標準エラーに1行で伝える）。
func copyDefinitions(repoRoot, dst string) error {
	src := filepath.Join(repoRoot, ".masuda")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return nil
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir() && (rel == claudedir.SharedDir || rel == claudedir.LocalDir):
			return filepath.SkipDir
		// 権限は元のまま写す。写しはイメージのビルドコンテキストにもなり、DockerのCOPYは
		// コンテキストの権限を保つため、0600に絞るとUSERを切り替えた後の手順から読めない
		// （`masuda image build`は作業ツリーを直接ビルドするので、同じDockerfileがrunでだけ
		// 失敗する）。他のユーザーから読めないことは、写しを置くデータディレクトリ（0700）で守る。
		// ディレクトリは中へ写せるよう、持ち主の権限だけは必ず付ける。
		case d.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm()|0o700)
		case d.Type().IsRegular():
			if rel == config.SettingsLocalFileName {
				// 利用者ごとの承認は定義ではない。実行のたびに作業ツリーのものを読む。
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if err := os.WriteFile(target, b, 0o600); err != nil {
				return err
			}
			// WriteFileの権限はumaskで削られるので、作業ツリーと同じにするにはChmodが要る。
			return os.Chmod(target, info.Mode().Perm())
		default:
			return nil
		}
	})
	if err != nil {
		return err
	}
	files, ignored, err := claudedir.Merge(src)
	if err != nil {
		return err
	}
	if w := claudedir.Warning(ignored); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}
	return claudedir.Write(filepath.Join(dst, claudedir.SharedDir), files)
}

// newRunCtl はワークスペースwの実行の窓口（engine・Runner・MCPサーバー）を組み立てて登録する。
// 記録（records/engine.json）が既にあれば、engineはそこから位置を計算するので、新しい実行にも
// 再開にも同じ組み立てを使う。sandboxはまだ作らない（bootが作る）。
//
// MCPはsandboxより先に待ち受ける。tcp_mapsに渡すポートが要るのと、ゲストのclaudeが
// 起動直後に呼んでも、フックが起動の途中で届いても受けられるようにするため。
func (b *backend) newRunCtl(w *workspace.Workspace, set *engine.Set, plan *bootPlan) (*runCtl, error) {
	id := w.ID
	reviews, err := snapshotReviews(w)
	if err != nil {
		return nil, err
	}
	pits, err := loadPitfalls(w.DefinitionsDir())
	if err != nil {
		return nil, err
	}
	privCmds, err := privilegedCommandsForGuest(w.DefinitionsDir())
	if err != nil {
		return nil, err
	}
	claude, err := claudedir.Load(filepath.Join(w.DefinitionsDir(), claudedir.SharedDir))
	if err != nil {
		return nil, err
	}
	store, err := runner.OpenFileStore(filepath.Join(w.RecordsDir(), "engine.json"))
	if err != nil {
		return nil, err
	}
	author := gitIdentity(b.ctx, w.RepoRoot)
	var c *runCtl
	r := runner.New(runner.Options{
		Workspace:     w,
		Sandbox:       b.sandbox,
		SandboxID:     id,
		Author:        author,
		Reviews:       reviews,
		AlwaysHosts:   []string{claudeAPIHost},
		AlwaysSecrets: []string{guest.TokenEnv},
		Egress:        plan.egress,
		PublishRemote: plan.publishRemote,
		Secrets:       plan.placeholderNames,
		Plaintext:     sortedKeys(plan.plaintext),
		OnLog:         func(e engine.Event) { b.publishEngine(id, e) },
		RunPrivileged: func(ctx context.Context, occurrence, name string) (*privileged.Result, error) {
			return c.runPrivileged(ctx, occurrence, name)
		},
	})
	ctx, cancel := context.WithCancel(b.ctx)
	c = &runCtl{
		b: b, id: id, set: set, runner: r, author: author, plan: plan, reviews: reviews, pitfalls: pits, privilegedCommands: privCmds, claude: claude,
		changed: make(chan struct{}), ctx: ctx, cancel: cancel, bootDone: make(chan struct{}),
	}
	c.eng = engine.New(set, store, r, engine.Options{})
	if c.mcp, err = mcp.Start(c); err != nil {
		cancel()
		return nil, err
	}
	if b.fake {
		// フェイクではtcp_mapsが効かないので、契約テスト（ゲストの代役）がポートを引けるよう書き出す。
		dir := filepath.Join(FakeDir(b.dataDir), id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			c.close()
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "mcp.port"), []byte(strconv.Itoa(c.mcp.Port())+"\n"), 0o600); err != nil {
			c.close()
			return nil, err
		}
	}
	b.acts.reset(id)
	b.addRun(c)
	return c, nil
}

// boot はcのsandboxを作り、ゲストの初期配置をして（実VMならメインセッションも起動して）
// 実行を動かす。resumeなら前のsandboxを壊してから作り、engineを進めて今の位置を状態に写す。
// 失敗したらsandboxを壊してSUSPENDEDにし、理由をReasonに残す。Stopで取り消されたときは
// 状態に触らない（Stopが書く）。
func (b *backend) boot(c *runCtl, resume bool) {
	defer close(c.bootDone)
	id := c.id
	err := b.bootSandbox(c, resume)
	if c.ctx.Err() != nil {
		return
	}
	if _, gerr := b.store.Get(id); gerr != nil {
		// 起動中にワークスペースが消された。sandboxだけ残さないようにする。
		b.removeRun(id)
		b.destroySandbox(id)
		return
	}
	if err != nil {
		b.removeRun(id)
		b.destroySandbox(id)
		c.update(func(w *workspace.Workspace) {
			w.State = workspace.StateSuspended
			w.Reason = bootFailedReason + err.Error()
		})
		return
	}
	c.booted.Store(true)
	if resume {
		// 再開した位置がゲート・質問待ちならその状態に、エージェントのタスクならRUNNINGになる。
		_, _ = c.advance()
		return
	}
	c.setState(workspace.StateRunning)
}

// bootFailedReason は起動に失敗してSUSPENDEDにしたときのReasonの頭。
const bootFailedReason = "sandbox boot failed: "

// resumable はwをResumeできる状態か。止めたものと、SUSPENDED（起動の失敗、engineへの呼び出しの
// エラー。どちらもengineは何も記録していない）。engineが記録したBLOCKEDは含めない。
func resumable(w *workspace.Workspace) bool {
	return w.State == workspace.StateStopped || w.State == workspace.StateSuspended
}

func (b *backend) destroySandbox(id string) {
	// 呼び出し元のctxはStopで取り消されていることがあるので、後始末は切り離して行う。
	_, _ = b.sandbox.DestroySandbox(context.Background(), connect.NewRequest(&sandboxv1.DestroySandboxRequest{Id: id}))
}

func (b *backend) bootSandbox(c *runCtl, resume bool) error {
	ctx := c.ctx
	w, err := b.store.Get(c.id)
	if err != nil {
		return err
	}
	if resume {
		// serveの再起動で残ったsandboxがあれば作り直す前に壊す（無ければNotFoundで何もしない）。
		b.destroySandbox(c.id)
	}
	b.bootPhase(c.id, "building image (log: "+filepath.Join(w.RecordsDir(), "image-build.log")+")")
	buildID, err := b.buildWorkspaceImage(ctx, w, c.plan.image)
	if err != nil {
		return err
	}
	b.bootPhase(c.id, "booting the VM")
	sb, err := b.createSandbox(ctx, w, c.mcp.Addr(), c.plan, buildID)
	if err != nil {
		return err
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.watchSandbox(ctx, c.id)
	}()
	env := c.plan.guestEnv(sb.Placeholders)
	c.runner.SetGuestEnv(env)
	b.bootPhase(c.id, "preparing the guest")
	if err := b.prepareGuest(ctx, w, c, sb.Placeholders); err != nil {
		return err
	}
	if resume {
		if _, err := c.runner.RestoreWIP(ctx); err != nil {
			return err
		}
	}
	if b.fake {
		return nil
	}
	b.bootPhase(c.id, "starting Claude Code")
	return guest.Launch(ctx, b.sandbox, guest.LaunchOptions{
		SandboxID: c.id,
		Token:     sb.Placeholders[guest.TokenEnv],
		GitName:   c.author.Name,
		GitEmail:  c.author.Email,
		Env:       env,
	})
}

// bootPhase は起動の段階を活動のdetailに書き、状態のイベントで知らせる。
func (b *backend) bootPhase(id, detail string) {
	b.acts.update(id, func(act *activity) { act.detail = detail })
	b.statusChanged(id)
}

// buildWorkspaceImage はワークスペースの定義の写しにあるイメージのエントリをビルドし、
// build_idを返す。ビルドのログは`records/image-build.log`に残す。
func (b *backend) buildWorkspaceImage(ctx context.Context, w *workspace.Workspace, entry string) (string, error) {
	logf, err := os.OpenFile(filepath.Join(w.RecordsDir(), "image-build.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return "", err
	}
	defer logf.Close()
	dir := filepath.Join(w.DefinitionsDir(), "images", entry)
	id, err := b.buildImage(ctx, w.RepoRoot, entry, dir, func(line string) error {
		_, err := fmt.Fprintln(logf, line)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("building image %s: %w", entry, err)
	}
	return id, nil
}

// createSandbox はワークスペースのsandboxを作る。宣言した秘密（Claude APIのトークンを含む）を
// 渡し、初期の方針でAPIへの経路だけを開ける。plaintextの秘密は本物の値をenvで渡す。
// ゲストから`masuda.internal:7000`をMCPのポートへ対応付ける。
func (b *backend) createSandbox(ctx context.Context, w *workspace.Workspace, mcpAddr string, plan *bootPlan, buildID string) (*sandboxv1.Sandbox, error) {
	res, err := b.sandbox.CreateSandbox(ctx, connect.NewRequest(&sandboxv1.CreateSandboxRequest{
		Id:          w.ID,
		BuildId:     buildID,
		DefaultUser: guest.User,
		DiskMib:     plan.diskMiB,
		MemoryMib:   plan.memoryMiB,
		Cpus:        plan.cpus,
		Env:         plan.plaintext,
		Secrets:     plan.secrets,
		Policy:      &sandboxv1.Policy{AllowedHosts: []string{claudeAPIHost}, EnabledSecrets: []string{guest.TokenEnv}},
		TcpMaps:     []*sandboxv1.TcpMap{{Host: guest.MCPHost, Port: guest.MCPPort, Upstream: mcpAddr}},
	}))
	if err != nil {
		return nil, fmt.Errorf("creating sandbox: %w", err)
	}
	return res.Msg, nil
}

// prepareGuest はstagingのブランチをゲストへcloneさせ、ループ規約・サブエージェント定義・
// フック設定（claudeSettingsと合成）・envFilesから生成したファイル・チェック・観点と落とし穴と特権コマンドの宣言と
// `.masuda/claude/`の写しを置く。サブエージェントは、この実行のワークフローが使うものだけを定義から生成する。
func (b *backend) prepareGuest(ctx context.Context, w *workspace.Workspace, c *runCtl, placeholders map[string]string) error {
	set, plan := c.set, c.plan
	repo := staging.Open(w.StagingDir())
	// 毎回別のディレクトリに作る。Stopで取り消されたgitが`.lock`を残しても、再開を妨げないように。
	tmp, err := os.MkdirTemp(w.Dir, ".bootstrap-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bundle := filepath.Join(tmp, "bootstrap.bundle")
	if err := repo.CreateBundle(ctx, bundle, staging.BranchRef(w.Branch)); err != nil {
		return fmt.Errorf("creating bundle: %w", err)
	}
	refs, err := set.Reachable(w.Workflow)
	if err != nil {
		return err
	}
	var agents []guest.Agent
	for _, ref := range refs {
		name, ok := strings.CutPrefix(ref, "agents/")
		if !ok {
			continue
		}
		a := set.Agents[name]
		if a == nil {
			return fmt.Errorf("agent %s is not loaded", ref)
		}
		agents = append(agents, guest.AgentFile(withOverride(a, plan.agents[name])))
	}
	return guest.Prepare(ctx, b.sandbox, guest.Layout{
		SandboxID:          w.ID,
		Branch:             w.Branch,
		Bundle:             bundle,
		Agents:             agents,
		ClaudeSettings:     plan.claudeSettings,
		EnvFiles:           plan.guestEnvFiles(placeholders),
		Checks:             plan.checks,
		Reviews:            c.reviews,
		Pitfalls:           c.pitfalls,
		PrivilegedCommands: c.privilegedCommands,
		Claude:             c.claude,
	})
}

// withOverride はaの写しにsettings.jsonの`agents`の上書きを重ねて返す。engineの定義そのものは
// 書き換えない（engineの検査や`continues`の判定は役定義のままの値で行わせるため）。
func withOverride(a *engine.Agent, o config.AgentOverride) *engine.Agent {
	cp := *a
	if o.Model != nil {
		cp.Model = *o.Model
	}
	if o.Effort != nil {
		cp.Effort = *o.Effort
	}
	return &cp
}

// gitIdentity は実リポジトリのgit設定のuser.name・user.emailを返す。stagingのコミットは
// publishで利用者のブランチに入るので、利用者が自分で作ったコミットと同じ作者にする。
// 設定が無ければ空（staging側の既定の"masuda"になる）。
func gitIdentity(ctx context.Context, repoRoot string) staging.Identity {
	get := func(key string) string {
		out, err := exec.CommandContext(ctx, "git", "-C", repoRoot, "config", "--get", key).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return staging.Identity{Name: get("user.name"), Email: get("user.email")}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
