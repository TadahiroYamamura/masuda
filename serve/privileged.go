package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// RunPrivilegedCommand は宣言済み・承認済みの特権コマンドを2つ目のsandboxで動かす。
// 宣言は定義の写し（実行の開始時点のもの）から、承認は作業ツリーのsettings.local.jsonから読む。
// 定義の写しから読むのは、実行中にエージェントが作業ツリーの宣言を書き換えても、
// 承認済みのハッシュと食い違って断られるだけで、書き換えた宣言が動くことはないようにするため。
func (c *runCtl) RunPrivilegedCommand(ctx context.Context, name string) (any, error) {
	c.touch("run_privileged_command")
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
	decl, ok := cfg.PrivilegedCommands[name]
	if !ok {
		names := make([]string, 0, len(cfg.PrivilegedCommands))
		for n := range cfg.PrivilegedCommands {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("privileged command %q is not declared in privilegedCommands of .masuda/settings.json (declared: %v)", name, names)
	}
	if err := privileged.Validate(decl, imageExistsIn(w.DefinitionsDir())); err != nil {
		return nil, fmt.Errorf("privileged command %q: %w", name, err)
	}
	local, err := config.LoadLocal(w.RepoRoot)
	if err != nil {
		return nil, err
	}
	hash, err := config.DeclHash(decl)
	if err != nil {
		return nil, err
	}
	switch a, recorded := local.PrivilegedCommandsApproved[name]; {
	case !recorded:
		return nil, fmt.Errorf("privileged command %q is not approved; a human must run `masuda privileged-command approve %s`", name, name)
	case a.DeclHash != hash:
		return nil, fmt.Errorf("privileged command %q changed since it was approved; a human must run `masuda privileged-command approve %s` again", name, name)
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
	buildID, err := c.b.buildWorkspaceImage(ctx, w, decl.Image)
	if err != nil {
		return nil, err
	}
	res, err := privileged.Run(ctx, privileged.Options{
		Sandbox:     c.b.sandbox,
		MainID:      c.id,
		SandboxID:   c.id + "-p" + runID,
		RunID:       runID,
		BuildID:     buildID,
		DiskMiB:     cfg.DiskMiB(decl.Image),
		Egress:      config.AllowedEgress(cfg, local),
		Decl:        decl,
		Staging:     c.runner.Staging(),
		SnapshotRef: ref,
		HostDir:     hostDir,
		Heartbeat:   func() { c.touch("run_privileged_command " + name) },
	})
	rec := privilegedRecord{Name: name, DeclHash: hash, Snapshot: ref, StartedAt: started, FinishedAt: time.Now().UTC()}
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
	Name         string    `json:"name"`
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
