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
	"sync/atomic"
	"time"

	"github.com/TadahiroYamamura/masuda-engine/engine"

	"github.com/TadahiroYamamura/masuda/internal/mcp"
	"github.com/TadahiroYamamura/masuda/internal/runner"
	"github.com/TadahiroYamamura/masuda/internal/staging"
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
	author staging.Identity
	// plan はsandboxとゲストの組み立て方（Run・Resumeの受け付け時に設定から作ったもの）。
	plan *bootPlan
	// reviews は実行開始時に固定した観点（`<id>.md`→中身）。ゲストの`/masuda/reviews/`へ置く。
	reviews map[string][]byte

	// ctx はこの実行の寿命。Stop・Remove・serveの停止で取り消され、ブロック中のnext_task・
	// ask_human、起動の途中、engineの進行を止める。
	ctx    context.Context
	cancel context.CancelFunc
	// bootDone はbootが戻ったら閉じる。Stopはこれを待ってから状態を書く。
	bootDone chan struct{}
	// booted はsandboxとゲストの用意が済んだこと（Execでの生存確認はこの後だけ行う）。
	booted atomic.Bool

	mu sync.Mutex // engineの呼び出し
	// privMu は特権コマンドを1つずつ動かす（serve/privileged.go）。
	privMu sync.Mutex

	chMu sync.Mutex
	// changed は人間の判断・エージェントの報告で記録が変わったら閉じて作り直す。
	// ブロック中のnext_task・ask_humanはこれで起きて位置を計算し直す。
	changed chan struct{}

	stateMu sync.Mutex // workspace.jsonの読み書き

	// endFeedback は最後に受け付けたエージェントの報告のfeedback。engineのStatusは終わり方の
	// ラベルしか持たないので、`end:needs_human`等で終わったときに人間へ見せる理由をここから取る。
	fbMu        sync.Mutex
	endFeedback string
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

func (c *runCtl) isBooted() bool { return c.booted.Load() }

func (c *runCtl) close() {
	c.cancel()
	if c.mcp != nil {
		c.mcp.Close()
	}
}

// advance はengineを進め、その結果をワークスペースの状態に写す。リクエストのctxではなく
// 実行のctxで進めるのは、ゲストやAPIクライアントが切断しても、commit・publishのような
// ホストのノードを途中で止めないため（止めるのはStopとserveの停止だけ）。
func (c *runCtl) advance() (engine.Status, error) {
	c.mu.Lock()
	st, err := c.eng.Advance(c.ctx, c.run())
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
	if err != nil && c.ctx.Err() != nil {
		return // Stop・serveの停止で取り消されただけ
	}
	if err == nil && (st.Kind == engine.StatusDone || st.Kind == engine.StatusBlocked) {
		// publishの時点の書き出しには、その後のfinish・endの行が入らないので写し直す。
		_ = c.runner.ExportLog()
	}
	c.update(func(w *workspace.Workspace) {
		if err != nil {
			w.State = workspace.StateBlocked
			w.Reason = "engine: " + err.Error()
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
			w.State, w.Outcome = workspace.StateDone, st.Outcome
			// done以外の終わり方（needs_human・out_of_scope・stuck等）は、終わらせたエージェントの
			// feedbackが人間への疑問や理由になる。serveを起こし直した後は手元に無いので、
			// 空なら前に書いた理由を残す。
			if st.Outcome == engine.OutcomeDone {
				w.Reason = ""
			} else if fb := c.lastFeedback(); fb != "" {
				w.Reason = fb
			}
		case engine.StatusBlocked:
			w.State, w.Reason = workspace.StateBlocked, st.Reason
		default:
			return
		}
		w.Position = positionOf(st)
	})
}

func (c *runCtl) lastFeedback() string {
	c.fbMu.Lock()
	defer c.fbMu.Unlock()
	return c.endFeedback
}

// positionOf はengineの位置を人間向けに書く（"agent planner (occ 0042)"）。
func positionOf(st engine.Status) string {
	switch st.Kind {
	case engine.StatusAgent:
		if st.Task != nil && st.Task.Agent != nil {
			return fmt.Sprintf("agent %s (occ %s)", st.Task.Agent.Name, st.Occurrence)
		}
	case engine.StatusGate:
		if st.Gate != nil {
			return fmt.Sprintf("gate %s (occ %s)", st.Gate.Gate, st.Occurrence)
		}
	case engine.StatusQuestion:
		return fmt.Sprintf("question (occ %s)", st.Occurrence)
	case engine.StatusDone:
		return "done"
	case engine.StatusBlocked:
		return "blocked"
	}
	return string(st.Kind)
}

// update はworkspace.jsonを読み直してfで書き換え、保存してstatusイベントを流す。
func (c *runCtl) update(f func(*workspace.Workspace)) {
	c.stateMu.Lock()
	w, err := c.b.store.Get(c.id)
	if err != nil {
		c.stateMu.Unlock()
		return
	}
	f(w)
	w.UpdatedAt = time.Now().UTC()
	_ = w.Save()
	c.stateMu.Unlock()
	c.b.statusChanged(c.id)
}

func (c *runCtl) setState(s workspace.State) {
	c.update(func(w *workspace.Workspace) { w.State = s })
}

// touch はゲストからMCPの呼び出しがあったことを活動として記録する。
func (c *runCtl) touch(tool string) {
	c.b.acts.update(c.id, func(a *activity) { a.touch("mcp " + tool) })
	c.b.statusChanged(c.id)
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
	c.touch("next_task")
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
		case <-c.ctx.Done():
			return nil, errors.New("the workspace is stopping")
		}
	}
}

func (c *runCtl) WriteOutput(ctx context.Context, occ, name, content string) (any, error) {
	c.touch("write_output")
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
	c.touch("report_result")
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
	err := c.eng.ReportResult(c.ctx, c.run(), occ, outcome, feedback)
	c.mu.Unlock()
	if err != nil {
		return reject(err.Error())
	}
	c.fbMu.Lock()
	c.endFeedback = feedback
	c.fbMu.Unlock()
	c.notify()
	// 次の待ち（ゲート等）まで進めておく。APIから見える状態がnext_taskを待たずに変わるように。
	_, _ = c.advance()
	return map[string]any{"accepted": true}, nil
}

func (c *runCtl) ReportConcern(ctx context.Context, occ, text string) (any, error) {
	c.touch("report_concern")
	c.mu.Lock()
	err := c.eng.ReportConcern(c.ctx, c.run(), occ, text)
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
	c.touch("ask_human")
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
		case <-c.ctx.Done():
			return nil, errors.New("the workspace is stopping")
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

// Hook はゲストのClaude Codeフックの入力JSONを`records/hooks.jsonl`へ受け取った時刻と共に残し、
// 活動に反映してguest_hookイベントを流す。
func (c *runCtl) Hook(body []byte) {
	defer c.b.observeHook(c.id, body)
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
	w, err := c.b.store.Get(c.id)
	if err != nil {
		return err
	}
	// engineへ渡す判断だけに行コメントを合成し、記録（rec.Decision）には人間が送った本文を残す。
	// 合成した文字列を記録に書くと、APIのDecision.commentが人間の入力と食い違うため。
	toEngine := d
	if d.Outcome == engine.OutcomeRejected && rec.StagingCommit != "" {
		cs, err := w.Comments(rec.StagingCommit)
		if err != nil {
			return err
		}
		toEngine.Comment = rejectFeedback(d.Comment, cs)
	}
	c.mu.Lock()
	err = c.eng.Decide(c.ctx, c.run(), rec.Occurrence, toEngine)
	c.mu.Unlock()
	if err != nil {
		if errors.Is(err, engine.ErrNotImplemented) {
			return err
		}
		return fmt.Errorf("%w: %v", errDecision, err)
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

// ---------------------------------------------------------------------------
// Questions
// ---------------------------------------------------------------------------

// answer はrecの質問への答えをengineへ渡し、記録に書いて、待っている側（ask_human・
// next_task）を起こし、次の待ちまで進める。engineを先に呼ぶのは、engineが受け付けなかった
// 答えを記録に残さないため（ゲートの判断と同じ順）。
func (c *runCtl) answer(rec *workspace.QuestionRecord, answers map[string]string) error {
	c.mu.Lock()
	err := c.eng.Answer(c.ctx, c.run(), rec.Occurrence, engine.Answer{Answers: answers})
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
	now := time.Now().UTC()
	rec.Answers = answers
	rec.AnsweredAt = &now
	if err := w.SaveQuestion(rec); err != nil {
		return err
	}
	c.notify()
	_, _ = c.advance()
	c.b.statusChanged(c.id)
	return nil
}
