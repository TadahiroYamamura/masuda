package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/TadahiroYamamura/masuda-engine/engine"

	"github.com/TadahiroYamamura/masuda/internal/mcp"
	"github.com/TadahiroYamamura/masuda/internal/runner"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// maxBlock はnext_task・ask_humanが人間の判断を待つ上限（docs/guest-protocol.md「到達経路」）。
const maxBlock = 7 * 24 * time.Hour

// runCtl は1つのワークスペースで動いている実行。engineとRunner、ゲスト向けのMCPサーバーを束ね、
// MCPのツール（mcp.Host）と公開API（ゲートの判断）の両方からengineを呼ぶ窓口になる。
//
// engineは記録から位置を毎回計算するので並行に呼んでも壊れないが、Advanceの途中で
// 別の呼び出しが同じノード（execやcommit）を走らせないよう、engineの呼び出しはmuで直列にする。
type runCtl struct {
	b      *backend
	id     string
	set    *engine.Set
	eng    *engine.Engine
	runner *runner.Runner
	mcp    *mcp.Server

	mu sync.Mutex // engineの呼び出し

	chMu sync.Mutex
	// changed は人間の判断・エージェントの報告で記録が変わったら閉じて作り直す。
	// ブロック中のnext_task・ask_humanはこれで起きて位置を計算し直す。
	changed chan struct{}

	stateMu sync.Mutex // workspace.jsonの読み書き
}

func (c *runCtl) run() engine.RunID { return engine.RunID(c.id) }

func (c *runCtl) waitCh() <-chan struct{} {
	c.chMu.Lock()
	defer c.chMu.Unlock()
	return c.changed
}

func (c *runCtl) notify() {
	c.chMu.Lock()
	defer c.chMu.Unlock()
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *runCtl) close() {
	if c.mcp != nil {
		c.mcp.Close()
	}
}

// advance はengineを進め、その結果をワークスペースの状態に写す。リクエストのctxではなく
// backendのctxで進めるのは、ゲストやAPIクライアントが切断しても、commit・publishのような
// ホストのノードを途中で止めないため。
func (c *runCtl) advance() (engine.Status, error) {
	c.mu.Lock()
	st, err := c.eng.Advance(c.b.ctx, c.run())
	c.mu.Unlock()
	c.reflect(st, err)
	return st, err
}

func (c *runCtl) status(ctx context.Context) (engine.Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eng.Status(ctx, c.run())
}

// reflect はengineの状態をワークスペースの状態（公開APIのWorkspaceState）に写す。
func (c *runCtl) reflect(st engine.Status, err error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	w, gerr := c.b.store.Get(c.id)
	if gerr != nil {
		return
	}
	if err != nil {
		if c.b.ctx.Err() != nil {
			return // serveの停止で取り消されただけ
		}
		w.State = workspace.StateBlocked
		w.Reason = "engine: " + err.Error()
		_ = w.Save()
		return
	}
	switch st.Kind {
	case engine.StatusAgent:
		w.State, w.Reason = workspace.StateRunning, ""
	case engine.StatusGate:
		w.State, w.Reason = workspace.StateWaitingGate, ""
	case engine.StatusQuestion:
		w.State, w.Reason = workspace.StateWaitingQuestion, ""
	case engine.StatusDone:
		w.State, w.Outcome, w.Reason = workspace.StateDone, st.Outcome, ""
		// publishの時点の書き出しには、その後のfinish・endの行が入らないので写し直す。
		_ = c.runner.ExportLog()
	case engine.StatusBlocked:
		w.State, w.Reason = workspace.StateBlocked, st.Reason
		_ = c.runner.ExportLog()
	default:
		return
	}
	w.UpdatedAt = time.Now().UTC()
	_ = w.Save()
}

func (c *runCtl) setState(s workspace.State) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if w, err := c.b.store.Get(c.id); err == nil {
		w.State = s
		w.UpdatedAt = time.Now().UTC()
		_ = w.Save()
	}
}

// waitingAgent はoccの出現がエージェントのタスクとして待たれているなら、その状態を返す。
func (c *runCtl) waitingAgent(ctx context.Context, occ string) (engine.Status, string) {
	st, err := c.eng.Status(ctx, c.run())
	if err != nil {
		return st, err.Error()
	}
	if st.Kind != engine.StatusAgent || st.Occurrence != occ {
		return st, fmt.Sprintf("occurrence %q is not the task being waited for", occ)
	}
	return st, ""
}

// ---------------------------------------------------------------------------
// mcp.Host
// ---------------------------------------------------------------------------

func (c *runCtl) NextTask(ctx context.Context) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, maxBlock)
	defer cancel()
	for {
		ch := c.waitCh()
		st, err := c.advance()
		if err != nil {
			return nil, err
		}
		switch st.Kind {
		case engine.StatusAgent:
			p, err := c.runner.MaterializeTask(ctx, st.Task)
			if err != nil {
				return nil, err
			}
			return map[string]any{"kind": "task", "occurrence": st.Occurrence, "role": st.Task.Agent.Name, "task_path": p}, nil
		case engine.StatusDone:
			return map[string]any{"kind": "done", "outcome": st.Outcome}, nil
		case engine.StatusBlocked:
			return map[string]any{"kind": "blocked", "reason": st.Reason}, nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.b.ctx.Done():
			return nil, errors.New("masuda serve is stopping")
		}
	}
}

func (c *runCtl) WriteOutput(ctx context.Context, occ, name, content string) (any, error) {
	c.mu.Lock()
	st, reason := c.waitingAgent(ctx, occ)
	c.mu.Unlock()
	reject := func(problems ...string) (any, error) {
		return map[string]any{"accepted": false, "problems": problems}, nil
	}
	if reason != "" {
		return reject(reason)
	}
	if !slices.Contains(st.Task.Outputs, name) {
		return reject(fmt.Sprintf("%q is not an output of this task (outputs: %v)", name, st.Task.Outputs))
	}
	if problems := runner.Validate(c.set.Schemas, name, []byte(content)); len(problems) > 0 {
		return reject(problems...)
	}
	if err := c.runner.WriteOutput(ctx, occ, name, []byte(content)); err != nil {
		return nil, err
	}
	return map[string]any{"accepted": true}, nil
}

func (c *runCtl) ReportResult(ctx context.Context, occ, outcome, feedback, _ string) (any, error) {
	reject := func(reason string) (any, error) {
		return map[string]any{"accepted": false, "reason": reason}, nil
	}
	c.mu.Lock()
	st, reason := c.waitingAgent(ctx, occ)
	if reason != "" {
		c.mu.Unlock()
		return reject(reason)
	}
	// 書かれていない出力はengineに渡す前に断る。engineは差し戻し（同じノードへの再進入）として
	// 受け付けてしまうので、まだ書けるうちにエージェントへ知らせる方が早い。
	if outcome == engine.OutcomeDone {
		var missing []string
		for _, name := range st.Task.Outputs {
			_, ok, err := c.runner.ReadOutput(ctx, c.run(), occ, name)
			if err != nil {
				c.mu.Unlock()
				return nil, err
			}
			if !ok {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			c.mu.Unlock()
			return reject(fmt.Sprintf("outputs not written yet: %v (use write_output)", missing))
		}
	}
	err := c.eng.ReportResult(c.b.ctx, c.run(), occ, outcome, feedback)
	c.mu.Unlock()
	if err != nil {
		return reject(err.Error())
	}
	c.notify()
	// 次の待ち（ゲート等）まで進めておく。APIから見える状態がnext_taskを待たずに変わるように。
	_, _ = c.advance()
	return map[string]any{"accepted": true}, nil
}

func (c *runCtl) ReportConcern(ctx context.Context, occ, text string) (any, error) {
	c.mu.Lock()
	err := c.eng.ReportConcern(c.b.ctx, c.run(), occ, text)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	c.notify()
	_, _ = c.advance()
	return map[string]any{"recorded": true}, nil
}

// AskHuman は質問を記録して答えを待つ。答えは公開APIのQuestionService.Answer（M5）が
// engine.Answerへ渡したうえで記録に書き、notifyで知らせる。
func (c *runCtl) AskHuman(ctx context.Context, occ string, questions []mcp.Question) (any, error) {
	if len(questions) == 0 {
		return nil, errors.New("questions is empty")
	}
	c.mu.Lock()
	st, reason := c.waitingAgent(ctx, occ)
	c.mu.Unlock()
	if reason != "" {
		return nil, errors.New(reason)
	}
	if n := c.node(st.Task); n == nil || n.Type != engine.NodeQuestion {
		return nil, fmt.Errorf("occurrence %s is not a question node; ask_human is only for question tasks", occ)
	}
	w, err := c.b.store.Get(c.id)
	if err != nil {
		return nil, err
	}
	rec := &workspace.QuestionRecord{Occurrence: occ, OpenedAt: time.Now().UTC()}
	for _, q := range questions {
		rec.Questions = append(rec.Questions, workspace.QuestionItem{ID: q.ID, Text: q.Text, Options: q.Options})
	}
	if err := w.AddQuestion(rec); err != nil {
		return nil, err
	}
	c.setState(workspace.StateWaitingQuestion)
	ctx, cancel := context.WithTimeout(ctx, maxBlock)
	defer cancel()
	for {
		ch := c.waitCh()
		qs, err := w.Questions()
		if err != nil {
			return nil, err
		}
		for _, q := range qs {
			if q.Occurrence == rec.Occurrence && q.Seq == rec.Seq && q.Answers != nil {
				c.setState(workspace.StateRunning)
				return map[string]any{"answers": q.Answers}, nil
			}
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (c *runCtl) node(t *engine.AgentTask) *engine.Node {
	if t == nil {
		return nil
	}
	if wf := c.set.Workflows[t.Workflow]; wf != nil {
		return wf.Nodes[t.Node]
	}
	return nil
}

func (c *runCtl) RunPrivilegedCommand(context.Context, string) (any, error) {
	return nil, fmt.Errorf("run_privileged_command: %w (privileged commands arrive with M7)", mcp.ErrNotImplemented)
}

// Hook はゲストのClaude Codeフックの入力JSONを`records/hooks.jsonl`へ受け取った時刻と共に残す。
// 活動の判定（M5）はこの記録を読む。
func (c *runCtl) Hook(body []byte) {
	w, err := c.b.store.Get(c.id)
	if err != nil {
		return
	}
	raw := json.RawMessage(body)
	if !json.Valid(body) {
		b, _ := json.Marshal(string(body))
		raw = b
	}
	line, err := json.Marshal(struct {
		Time  time.Time       `json:"time"`
		Input json.RawMessage `json:"input"`
	}{time.Now().UTC(), raw})
	if err != nil {
		return
	}
	c.b.hookMu.Lock()
	defer c.b.hookMu.Unlock()
	f, err := os.OpenFile(filepath.Join(w.RecordsDir(), "hooks.jsonl"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// ---------------------------------------------------------------------------
// Gates
// ---------------------------------------------------------------------------

// errDecision はengineがその判断を受け付けなかったこと（待っていない出現、決定済み等）を表す。
var errDecision = errors.New("decision refused")

// decide はrecのゲートへの判断をengineへ渡し、記録して、次の待ちまで進める。
func (c *runCtl) decide(rec *workspace.GateRecord, d engine.Decision) error {
	c.mu.Lock()
	err := c.eng.Decide(c.b.ctx, c.run(), rec.Occurrence, d)
	c.mu.Unlock()
	if err != nil {
		if errors.Is(err, engine.ErrNotImplemented) {
			return err
		}
		return fmt.Errorf("%w: %v", errDecision, err)
	}
	w, err := c.b.store.Get(c.id)
	if err != nil {
		return err
	}
	rec.Decision = &workspace.DecisionRecord{
		Outcome: d.Outcome, Comment: d.Comment, TargetHash: d.TargetHash,
		ApprovedFiles: d.ApprovedFiles, DecidedAt: time.Now().UTC(),
	}
	if err := w.SaveGate(rec); err != nil {
		return err
	}
	c.notify()
	// 判断の後の位置（次のゲート、publish等）まで進めてから返す。呼び出し側が戻りの直後に
	// 状態やゲートの一覧を見たとき、判断が反映されているようにするため。
	_, _ = c.advance()
	return nil
}
