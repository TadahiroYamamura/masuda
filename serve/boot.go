package serve

import (
	"context"
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

// startRequest はbootに渡す、Runの時点で固定したもの。
type startRequest struct {
	set    *engine.Set
	inputs map[string][]byte
}

// loadDefinitions は実リポジトリの`.masuda/`と同梱の定義を読み込む。ゲストのcloneではなく
// 実リポジトリの作業ツリーから読むのは、定義を実行前に検査して、問題があればRunの時点で
// 止めるため（ゲストはまだ無い）。
func loadDefinitions(repoRoot string) (*engine.Set, fs.FS, error) {
	var repo fs.FS
	dir := filepath.Join(repoRoot, ".masuda")
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		repo = os.DirFS(dir)
	}
	set, err := engine.Load(repo, engine.Bundled())
	return set, repo, err
}

// boot はワークスペースidのsandboxを作り、ゲストの初期配置をしてengineの実行を始め、RUNNINGにする。
// 失敗したらsandboxを壊してBLOCKEDにし、理由をReasonに残す。
func (b *backend) boot(ctx context.Context, id string, req startRequest) {
	err := b.startRun(ctx, id, req)
	w, gerr := b.store.Get(id)
	if gerr != nil {
		// 起動中にワークスペースが消された。sandboxだけ残さないようにする。
		b.removeRun(id)
		if err == nil {
			b.destroySandbox(id)
		}
		return
	}
	if err != nil {
		b.removeRun(id)
		b.destroySandbox(id)
		w.State = workspace.StateBlocked
		w.Reason = "sandbox boot failed: " + err.Error()
	} else {
		w.State = workspace.StateRunning
	}
	_ = w.Save()
}

func (b *backend) destroySandbox(id string) {
	// 呼び出し元のctxはStopで取り消されていることがあるので、後始末は切り離して行う。
	_, _ = b.sandbox.DestroySandbox(context.Background(), connect.NewRequest(&sandboxv1.DestroySandboxRequest{Id: id}))
}

func (b *backend) startRun(ctx context.Context, id string, req startRequest) error {
	w, err := b.store.Get(id)
	if err != nil {
		return err
	}
	_, reviews, err := loadDefinitions(w.RepoRoot)
	if err != nil {
		return err
	}
	if reviews != nil {
		if sub, err := fs.Sub(reviews, "reviews"); err == nil {
			reviews = sub
		}
	}
	author := gitIdentity(ctx, w.RepoRoot)
	store, err := runner.OpenFileStore(filepath.Join(w.RecordsDir(), "engine.json"))
	if err != nil {
		return err
	}
	r := runner.New(runner.Options{
		Workspace:     w,
		Sandbox:       b.sandbox,
		SandboxID:     id,
		Author:        author,
		Reviews:       reviews,
		AlwaysHosts:   []string{claudeAPIHost},
		AlwaysSecrets: []string{guest.TokenEnv},
	})
	c := &runCtl{b: b, id: id, set: req.set, runner: r, changed: make(chan struct{})}
	c.eng = engine.New(req.set, store, r, engine.Options{})
	// MCPはsandboxより先に待ち受ける。tcp_mapsに渡すポートが要るのと、ゲストのclaudeが
	// 起動直後に呼んでも届くようにするため。
	if c.mcp, err = mcp.Start(c); err != nil {
		return err
	}
	b.addRun(c)

	if err := b.createSandbox(ctx, w, c.mcp.Addr()); err != nil {
		return err
	}
	if b.fake {
		// フェイクではtcp_mapsが効かないので、契約テスト（ゲストの代役）がポートを引けるよう書き出す。
		portFile := filepath.Join(FakeDir(b.dataDir), id, "mcp.port")
		if err := os.WriteFile(portFile, []byte(strconv.Itoa(c.mcp.Port())+"\n"), 0o600); err != nil {
			return err
		}
	}
	if err := b.prepareGuest(ctx, w, req.set); err != nil {
		return err
	}

	names := make([]string, 0, len(req.inputs))
	for name, content := range req.inputs {
		if err := r.PutData(ctx, c.run(), engine.DataRef{Name: name}, content); err != nil {
			return fmt.Errorf("input %s: %w", name, err)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if err := c.eng.Start(ctx, c.run(), w.Workflow, names); err != nil {
		return err
	}
	if b.fake {
		return nil
	}
	sb, err := b.sandbox.GetSandbox(ctx, connect.NewRequest(&sandboxv1.GetSandboxRequest{Id: id}))
	if err != nil {
		return err
	}
	return guest.Launch(ctx, b.sandbox, guest.LaunchOptions{
		SandboxID: id,
		Token:     sb.Msg.Placeholders[guest.TokenEnv],
		GitName:   author.Name,
		GitEmail:  author.Email,
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
	bundle := filepath.Join(w.Dir, "bootstrap.bundle")
	defer os.Remove(bundle)
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
