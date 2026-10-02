package serve

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	"github.com/TadahiroYamamura/masuda-engine/engine"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/guest"
	"github.com/TadahiroYamamura/masuda/internal/mcp"
	"github.com/TadahiroYamamura/masuda/internal/runner"
	"github.com/TadahiroYamamura/masuda/internal/staging"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// claudeAPIHost はClaude APIのホスト。どのノードの方針でも許可し、トークンの置換先にする。
const claudeAPIHost = "api.anthropic.com"

// tokenFile はClaude APIのトークンを置くファイル（DataDirの下）。秘密ストア（M6）に
// 統合するまでの暫定の置き場所。
const tokenFile = "claude-oauth-token"

// loadDefinitions はdir（対象リポジトリの`.masuda/`か、その写し）と同梱の定義を読み込む。
// reviewsはdirの`reviews/`（無ければnil）。
func loadDefinitions(dir string) (set *engine.Set, reviews fs.FS, err error) {
	var repo fs.FS
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		repo = os.DirFS(dir)
		if st, err := os.Stat(filepath.Join(dir, "reviews")); err == nil && st.IsDir() {
			reviews = os.DirFS(filepath.Join(dir, "reviews"))
		}
	}
	set, err = engine.Load(repo, engine.Bundled())
	return set, reviews, err
}

// copyDefinitions は対象リポジトリの`.masuda/`をdstへ写す（無ければ空のdstを作る）。
// 実行は写しの定義で進めるので、実行中に作業ツリーの定義を書き換えても、再開やserveの
// 再起動の後に別の定義で組み直されることがない。シンボリックリンクは写さない
// （リポジトリの外を指していても読まないため）。
func copyDefinitions(repoRoot, dst string) error {
	src := filepath.Join(repoRoot, ".masuda")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return nil
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case d.Type().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, b, 0o600)
		default:
			return nil
		}
	})
}

// newRunCtl はワークスペースwの実行の窓口（engine・Runner・MCPサーバー）を組み立てて登録する。
// 記録（records/engine.json）が既にあれば、engineはそこから位置を計算するので、新しい実行にも
// 再開にも同じ組み立てを使う。sandboxはまだ作らない（bootが作る）。
//
// MCPはsandboxより先に待ち受ける。tcp_mapsに渡すポートが要るのと、ゲストのclaudeが
// 起動直後に呼んでも、フックが起動の途中で届いても受けられるようにするため。
func (b *backend) newRunCtl(w *workspace.Workspace, set *engine.Set, reviews fs.FS) (*runCtl, error) {
	id := w.ID
	store, err := runner.OpenFileStore(filepath.Join(w.RecordsDir(), "engine.json"))
	if err != nil {
		return nil, err
	}
	author := gitIdentity(b.ctx, w.RepoRoot)
	r := runner.New(runner.Options{
		Workspace:     w,
		Sandbox:       b.sandbox,
		SandboxID:     id,
		Author:        author,
		Reviews:       reviews,
		AlwaysHosts:   []string{claudeAPIHost},
		AlwaysSecrets: []string{guest.TokenEnv},
		OnLog:         func(e engine.Event) { b.publishEngine(id, e) },
	})
	ctx, cancel := context.WithCancel(b.ctx)
	c := &runCtl{
		b: b, id: id, set: set, runner: r, author: author,
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
// 失敗したらsandboxを壊してBLOCKEDにし、理由をReasonに残す。Stopで取り消されたときは
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
			w.State = workspace.StateBlocked
			w.Reason = "sandbox boot failed: " + err.Error()
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
	if err := b.createSandbox(ctx, w, c.mcp.Addr()); err != nil {
		return err
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.watchSandbox(ctx, c.id)
	}()
	if err := b.prepareGuest(ctx, w, c.set); err != nil {
		return err
	}
	if b.fake {
		return nil
	}
	sb, err := b.sandbox.GetSandbox(ctx, connect.NewRequest(&sandboxv1.GetSandboxRequest{Id: c.id}))
	if err != nil {
		return err
	}
	return guest.Launch(ctx, b.sandbox, guest.LaunchOptions{
		SandboxID: c.id,
		Token:     sb.Msg.Placeholders[guest.TokenEnv],
		GitName:   c.author.Name,
		GitEmail:  c.author.Email,
	})
}

// createSandbox はワークスペースのsandboxを作る。Claude APIのトークンだけを秘密として宣言し、
// 初期の方針でAPIへの経路を開ける。ゲストから`masuda.internal:7000`をMCPのポートへ対応付ける。
func (b *backend) createSandbox(ctx context.Context, w *workspace.Workspace, mcpAddr string) error {
	token := ""
	if !b.fake {
		t, err := os.ReadFile(filepath.Join(b.dataDir, tokenFile))
		if err != nil {
			return fmt.Errorf("reading the Claude API token: %w", err)
		}
		token = strings.TrimSpace(string(t))
	}
	// build_idはまだ決めない。イメージのビルドと解決（.masuda/images/<entry>）は後の項目で入れる。
	_, err := b.sandbox.CreateSandbox(ctx, connect.NewRequest(&sandboxv1.CreateSandboxRequest{
		Id:          w.ID,
		DefaultUser: guest.User,
		Secrets: []*sandboxv1.SecretDecl{{
			Name:              guest.TokenEnv,
			Value:             token,
			Hosts:             []string{claudeAPIHost},
			SubstituteIn:      []sandboxv1.SubstituteIn{sandboxv1.SubstituteIn_SUBSTITUTE_IN_HEADER},
			PlaceholderPrefix: "sk-ant-oat01-",
		}},
		Policy:  &sandboxv1.Policy{AllowedHosts: []string{claudeAPIHost}, EnabledSecrets: []string{guest.TokenEnv}},
		TcpMaps: []*sandboxv1.TcpMap{{Host: guest.MCPHost, Port: guest.MCPPort, Upstream: mcpAddr}},
	}))
	if err != nil {
		return fmt.Errorf("creating sandbox: %w", err)
	}
	return nil
}

// prepareGuest はstagingのブランチをゲストへcloneさせ、ループ規約・サブエージェント定義・
// フック設定を置く。サブエージェントは、この実行のワークフローが使うものだけを定義から生成する。
func (b *backend) prepareGuest(ctx context.Context, w *workspace.Workspace, set *engine.Set) error {
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
		agents = append(agents, guest.AgentFile(a))
	}
	return guest.Prepare(ctx, b.sandbox, guest.Layout{
		SandboxID: w.ID,
		Branch:    w.Branch,
		Bundle:    bundle,
		Agents:    agents,
	})
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
