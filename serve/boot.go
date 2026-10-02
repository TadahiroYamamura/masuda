package serve

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"connectrpc.com/connect"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/guest"
	"github.com/TadahiroYamamura/masuda/internal/staging"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// agentsDir は対象リポジトリでサブエージェント定義を置くディレクトリ。
const agentsDir = ".masuda/agents"

// boot はワークスペースidのsandboxを作り、ゲストの初期配置をしてRUNNINGにする。
// 失敗したらsandboxを壊してBLOCKEDにし、理由をReasonに残す。
func (b *backend) boot(ctx context.Context, id string) {
	err := b.startSandbox(ctx, id)
	w, gerr := b.store.Get(id)
	if gerr != nil {
		// 起動中にワークスペースが消された。sandboxだけ残さないようにする。
		if err == nil {
			b.destroySandbox(id)
		}
		return
	}
	if err != nil {
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

func (b *backend) startSandbox(ctx context.Context, id string) error {
	w, err := b.store.Get(id)
	if err != nil {
		return err
	}
	repo := staging.Open(w.StagingDir())
	branchRef := staging.BranchRef(w.Branch)

	bundle := filepath.Join(w.Dir, "bootstrap.bundle")
	defer os.Remove(bundle)
	if err := repo.CreateBundle(ctx, bundle, branchRef); err != nil {
		return fmt.Errorf("creating bundle: %w", err)
	}
	agents, err := readAgents(ctx, repo, branchRef)
	if err != nil {
		return err
	}

	// build_idはまだ決めない。イメージのビルドと解決（.masuda/images/<entry>）は後の項目で入れる。
	if _, err := b.sandbox.CreateSandbox(ctx, connect.NewRequest(&sandboxv1.CreateSandboxRequest{
		Id:          id,
		DefaultUser: guest.User,
	})); err != nil {
		return fmt.Errorf("creating sandbox: %w", err)
	}
	return guest.Prepare(ctx, b.sandbox, guest.Layout{
		SandboxID: id,
		Branch:    w.Branch,
		Bundle:    bundle,
		Agents:    agents,
	})
}

// readAgents はstagingのブランチ先頭（ゲストがcloneするのと同じコミット）から
// `.masuda/agents/*.md`を読む。実リポジトリの作業ツリーから読まないのは、未コミットの
// 変更や起動後の編集がゲストの/workspaceと食い違わないようにするため。
func readAgents(ctx context.Context, repo *staging.Repo, rev string) ([]guest.Agent, error) {
	paths, err := repo.ListBlobs(ctx, rev, agentsDir)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", agentsDir, err)
	}
	var agents []guest.Agent
	for _, p := range paths {
		if path.Ext(p) != ".md" {
			continue
		}
		rc, err := repo.Blob(ctx, rev, p)
		if err != nil {
			return nil, err
		}
		b, rerr := io.ReadAll(rc)
		if cerr := rc.Close(); rerr == nil {
			rerr = cerr
		}
		if rerr != nil {
			return nil, fmt.Errorf("reading %s: %w", p, rerr)
		}
		agents = append(agents, guest.Agent{Name: path.Base(p), Content: b})
	}
	return agents, nil
}
