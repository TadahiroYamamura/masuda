package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/privileged"
	"github.com/TadahiroYamamura/masuda/internal/secrets"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// privilegedRecordsDir はワークスペースの特権コマンドの記録の置き場所。
func privilegedRecordsDir(w *workspace.Workspace) string {
	return filepath.Join(w.RecordsDir(), "privileged")
}

// imageExistsIn はdir（`.masuda/`かその写し）にイメージのエントリのDockerfileがあるかを返す関数。
func imageExistsIn(dir string) func(string) bool {
	return func(entry string) bool {
		st, err := os.Stat(filepath.Join(dir, "images", entry, "Dockerfile"))
		return err == nil && st.Mode().IsRegular()
	}
}

// RunPrivilegedCommand はMCPのrun_privileged_commandの口。ノードからの呼び出し（runner.Options.RunPrivileged）と
// 同じrunPrivilegedを通る。
func (c *runCtl) RunPrivilegedCommand(ctx context.Context, name string) (any, error) {
	c.touch("run_privileged_command")
	res, err := c.runPrivileged(ctx, "", name, "")
	if err != nil {
		return nil, err
	}
	return res, nil
}

// runPrivileged は宣言済み・承認済みの特権コマンドを2つ目のsandboxで動かす。occurrenceは
// privilegedノードから呼ばれたときのその出現（MCPからなら空）で、記録に残す。approvedCommitは
// engineが渡す、今publishが着地させるコミット（MCPからなら空）。
// 宣言は定義の写し（実行の開始時点のもの）から、承認は作業ツリーのsettings.local.jsonから読む。
// 定義の写しから読むのは、実行中にエージェントが作業ツリーの宣言を書き換えても、
// 承認済みのハッシュと食い違って断られるだけで、書き換えた宣言が動くことはないようにするため。
//
// privilegedノードはadvanceがc.muを持ったまま呼ぶので、ここからc.muを取るもの（c.status・
// waitingAgent・engineの呼び出し）を呼ばない。
func (c *runCtl) runPrivileged(ctx context.Context, occurrence, name, approvedCommit string) (*privileged.Result, error) {
	if !c.isBooted() {
		return nil, errors.New("the workspace is still starting")
	}
	w, err := c.b.store.Get(c.id)
	if err != nil {
		return nil, err
	}
	cfg, err := config.LoadDir(w.DefinitionsDir())
	if err != nil {
		return nil, err
	}
	local, err := config.LoadLocal(w.RepoRoot)
	if err != nil {
		return nil, err
	}
	cmd, err := privileged.Resolve(cfg, local, name, imageExistsIn(w.DefinitionsDir()))
	if err != nil {
		return nil, err
	}
	withSecrets := len(cmd.Decl.Secrets) > 0
	if withSecrets {
		if occurrence == "" {
			return nil, fmt.Errorf("privileged command %q uses secrets, so it runs only from a privileged node of the workflow after the review is approved", name)
		}
		if approvedCommit == "" {
			return nil, fmt.Errorf("privileged command %q uses secrets, but there is no approved commit to run it on (the diff is not approved, or a commit was made after the approval)", name)
		}
	}
	secretDecls, err := c.privilegedSecrets(w.RepoRoot, cmd.Decl.Secrets)
	if err != nil {
		return nil, err
	}

	// 同じワークスペースでは1つずつ動かす。run-idの採番と、メインの作業ツリーのスナップショットが
	// 重ならないように。
	c.privMu.Lock()
	defer c.privMu.Unlock()
	// ゲストが切断しても、Stop・serveの停止でも止まるようにする。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(c.ctx, cancel)()

	runID, hostDir, err := nextPrivilegedRun(w)
	if err != nil {
		return nil, err
	}
	started := time.Now().UTC()
	var ref string
	if withSecrets {
		ref, err = c.pinApproved(ctx, w, runID, approvedCommit)
	} else {
		ref, err = c.runner.SnapshotNamed(ctx, "privileged-"+runID)
	}
	if err != nil {
		return nil, err
	}
	buildID, err := c.b.buildWorkspaceImage(ctx, w, cmd.Decl.Image)
	if err != nil {
		return nil, err
	}
	// 秘密を使うコマンドの結果はメインのゲストに置かない。宛先の応答（アクセストークン等）が
	// ログや出力に含まれうるので、エージェントからは読めないようにする。
	mainID := c.id
	if withSecrets {
		mainID = ""
	}
	res, err := privileged.Run(ctx, privileged.Options{
		Sandbox:     c.b.sandbox,
		MainID:      mainID,
		RunID:       runID,
		BuildID:     buildID,
		DiskMiB:     cfg.DiskMiB(cmd.Decl.Image),
		MemoryMiB:   cfg.MemoryMiB(cmd.Decl.Image),
		CPUs:        cfg.CPUs(cmd.Decl.Image),
		Egress:      cmd.Egress,
		Secrets:     secretDecls,
		Decl:        cmd.Decl,
		Prepare:     cmd.Prepare,
		Staging:     c.runner.Staging(),
		SnapshotRef: ref,
		HostDir:     hostDir,
		Heartbeat:   func() { c.touch("run_privileged_command " + name) },
	})
	rec := privilegedRecord{Name: name, Occurrence: occurrence, DeclHash: cmd.Hash, StartedAt: started, FinishedAt: time.Now().UTC()}
	if withSecrets {
		rec.ApprovedCommit = approvedCommit
	} else {
		rec.Snapshot = ref
	}
	if err != nil {
		rec.Error = err.Error()
	} else {
		rec.ExitCode, rec.TimedOut, rec.Signal, rec.Outputs, rec.OutputsError = &res.ExitCode, res.TimedOut, res.Signal, res.Outputs, res.OutputsError
	}
	if b, merr := json.MarshalIndent(rec, "", "  "); merr == nil {
		_ = os.WriteFile(filepath.Join(hostDir, "result.json"), b, 0o600)
	}
	if err != nil {
		return nil, err
	}
	if withSecrets {
		// ログはホストの記録に書き終えている。engineは戻りのログの末尾をエージェントへの差し戻しに
		// 載せるので、決まった文に差し替える。
		res.Log, res.Truncated = withheldLog(runID), false
	}
	return res, nil
}

// withheldLog は秘密を使うコマンドのログの代わりにエージェントへ返す文。
func withheldLog(runID string) string {
	return "秘密を使うコマンドのため、ログは人間だけが読める（records/privileged/" + runID + "/log）"
}

// privilegedRecord は`records/privileged/<run-id>/result.json`。ログと出力は同じディレクトリの別ファイル。
type privilegedRecord struct {
	Name string `json:"name"`
	// Occurrence はprivilegedノードから呼ばれたときのその出現。MCPから呼ばれたときは空。
	Occurrence string `json:"occurrence,omitempty"`
	DeclHash   string `json:"decl_hash"`
	// Snapshot は作業ツリーのスナップショットのref。秘密を持つ宣言では空で、代わりにApprovedCommitを持つ。
	Snapshot       string    `json:"snapshot"`
	ApprovedCommit string    `json:"approved_commit,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
	ExitCode       *int      `json:"exit_code,omitempty"`
	Signal         string    `json:"signal,omitempty"`
	TimedOut       bool      `json:"timed_out,omitempty"`
	Outputs        []string  `json:"outputs,omitempty"`
	OutputsError   string    `json:"outputs_error,omitempty"`
	Error          string    `json:"error,omitempty"`
}

// privilegedSecrets は特権コマンドの宣言の秘密に、秘密ストアの値を入れる。
func (c *runCtl) privilegedSecrets(repoRoot string, decls []config.SecretDecl) ([]*sandboxv1.SecretDecl, error) {
	store := secrets.New(c.b.dataDir)
	var out []*sandboxv1.SecretDecl
	for _, d := range decls {
		value, ok, err := store.Get(repoRoot, d.Name)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("secret %s has no value (masuda secret set %s)", d.Name, d.Name)
		}
		out = append(out, sandboxSecret(d, value))
	}
	return out, nil
}

// pinApproved はstagingのブランチの先頭が承認済みのコミットであることを確かめ、そのコミットを
// 実行ごとのrefに張って返す。engineの判定に加えてstagingも見るのは、publishの直前と同じ確かめで、
// 承認したものと違うツリーに秘密を渡さないため。
func (c *runCtl) pinApproved(ctx context.Context, w *workspace.Workspace, runID, approvedCommit string) (string, error) {
	repo := c.runner.Staging()
	if err := repo.CheckHead(ctx, w.Branch, approvedCommit); err != nil {
		return "", err
	}
	ref := privileged.ApprovedRef(runID)
	if err := repo.SetRef(ctx, ref, approvedCommit); err != nil {
		return "", err
	}
	return ref, nil
}

// nextPrivilegedRun は記録にある最大の番号の次を4桁で採番し、その記録ディレクトリを作る。
// 再開しても記録は残るので、同じワークスペースで番号が重なることはない。
func nextPrivilegedRun(w *workspace.Workspace) (string, string, error) {
	root := privilegedRecordsDir(w)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", "", err
	}
	last := 0
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err == nil && n > last {
			last = n
		}
	}
	id := fmt.Sprintf("%04d", last+1)
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", "", err
	}
	return id, dir, nil
}
