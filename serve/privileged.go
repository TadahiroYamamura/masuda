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

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/privileged"
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
	res, err := c.runPrivileged(ctx, "", name)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// runPrivileged は宣言済み・承認済みの特権コマンドを2つ目のsandboxで動かす。occurrenceは
// privilegedノードから呼ばれたときのその出現（MCPからなら空）で、記録に残す。
// 宣言は定義の写し（実行の開始時点のもの）から、承認は作業ツリーのsettings.local.jsonから読む。
// 定義の写しから読むのは、実行中にエージェントが作業ツリーの宣言を書き換えても、
// 承認済みのハッシュと食い違って断られるだけで、書き換えた宣言が動くことはないようにするため。
//
// privilegedノードはadvanceがc.muを持ったまま呼ぶので、ここからc.muを取るもの（c.status・
// waitingAgent・engineの呼び出し）を呼ばない。
func (c *runCtl) runPrivileged(ctx context.Context, occurrence, name string) (*privileged.Result, error) {
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
	ref, err := c.runner.SnapshotNamed(ctx, "privileged-"+runID)
	if err != nil {
		return nil, err
	}
	buildID, err := c.b.buildWorkspaceImage(ctx, w, cmd.Decl.Image)
	if err != nil {
		return nil, err
	}
	res, err := privileged.Run(ctx, privileged.Options{
		Sandbox:     c.b.sandbox,
		MainID:      c.id,
		RunID:       runID,
		BuildID:     buildID,
		DiskMiB:     cfg.DiskMiB(cmd.Decl.Image),
		MemoryMiB:   cfg.MemoryMiB(cmd.Decl.Image),
		CPUs:        cfg.CPUs(cmd.Decl.Image),
		Egress:      cmd.Egress,
		Decl:        cmd.Decl,
		Staging:     c.runner.Staging(),
		SnapshotRef: ref,
		HostDir:     hostDir,
		Heartbeat:   func() { c.touch("run_privileged_command " + name) },
	})
	rec := privilegedRecord{Name: name, Occurrence: occurrence, DeclHash: cmd.Hash, Snapshot: ref, StartedAt: started, FinishedAt: time.Now().UTC()}
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
	return res, nil
}

// privilegedRecord は`records/privileged/<run-id>/result.json`。ログと出力は同じディレクトリの別ファイル。
type privilegedRecord struct {
	Name string `json:"name"`
	// Occurrence はprivilegedノードから呼ばれたときのその出現。MCPから呼ばれたときは空。
	Occurrence   string    `json:"occurrence,omitempty"`
	DeclHash     string    `json:"decl_hash"`
	Snapshot     string    `json:"snapshot"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	ExitCode     *int      `json:"exit_code,omitempty"`
	Signal       string    `json:"signal,omitempty"`
	TimedOut     bool      `json:"timed_out,omitempty"`
	Outputs      []string  `json:"outputs,omitempty"`
	OutputsError string    `json:"outputs_error,omitempty"`
	Error        string    `json:"error,omitempty"`
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
