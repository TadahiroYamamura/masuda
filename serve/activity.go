package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/guest"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// DefaultStallAfter は無活動がこれだけ続いたらSTALLEDとみなす既定のしきい値。
const DefaultStallAfter = config.DefaultStallAfter

// stallFor はwの無活動のしきい値。serveの--stall-afterが指定されていればそれ、無ければ
// 実行を組み立てたときに読んだsettings.local.jsonのstallAfter、それも無ければconfig.jsonの
// stallAfter（既定10分）。
func (b *backend) stallFor(w *workspace.Workspace) time.Duration {
	if b.stallOverride > 0 {
		return b.stallOverride
	}
	if c := b.runFor(w.ID); c != nil && c.plan != nil && c.plan.stallAfter > 0 {
		return c.plan.stallAfter
	}
	return b.stallDefault
}

// patrolTick は見回りの間隔。最も短いしきい値の1/4（1秒〜30秒）にして、STALLEDになるのが
// しきい値からその程度の遅れで済むようにする。
func (b *backend) patrolTick() time.Duration {
	shortest := b.stallDefault
	if b.stallOverride > 0 {
		shortest = b.stallOverride
	} else {
		for _, c := range b.allRuns() {
			if c.plan != nil && c.plan.stallAfter > 0 && c.plan.stallAfter < shortest {
				shortest = c.plan.stallAfter
			}
		}
	}
	return min(max(shortest/4, time.Second), livenessEvery)
}

// inflightStale は進行中とみなすHTTPリクエストの寿命。sandboxはレスポンスのヘッダーを受けた時点で
// http_finishedを出す（ストリーミングの本文の長さは含まない）ので、M8の実機では最長でも約20秒だった。
// 一方、クライアントが応答前に切ったリクエストにはhttp_finishedが来ず、進行中のまま残って
// 活動をWORKINGに張り付かせた（人間への問いかけで止まっていてもinput_waitが見えなかった）。
// それより十分長く、無活動のしきい値より短い値で打ち切る。
const inflightStale = 2 * time.Minute

// idlePromptAfter はClaude Codeが入力待ちを続けてからNotification（idle_prompt）を出すまでの時間。
// idle_promptが来た時点で、これより前に始まって終わっていないリクエストは切られたものとみなせる。
const idlePromptAfter = 60 * time.Second

// inflightReq はsandboxが観測した進行中のHTTPリクエスト1つ。
type inflightReq struct {
	http    *apiv1.HttpActivity
	started time.Time
}

// activity は1ワークスペースの活動の観測（docs/design/overview.md「活動の観測と停止の検知」）。
// メモリにだけ持つ。serveを再起動したワークスペースはSTOPPED（活動はIDLE）になり、
// Resumeで観測をやり直すので、持ち越す意味が無い。
type activity struct {
	last time.Time
	// inflight はsandboxが観測した進行中のHTTPリクエスト（request_id → 要約）。
	inflight map[uint64]*inflightReq
	// inputWait はゲストのNotificationフックが言う待ち（"idle"・"permission"・"question"）。
	// その後に活動（HTTP・ツール・MCP）があれば消す。
	inputWait string
	detail    string
	// dead はclaude（tmuxのセッション）が無いと分かったこと。SessionEndフック、sandboxの
	// 停止・失敗、Execでの生存確認のどれかで立つ。
	dead bool
	// authRejected はClaude APIがトークンを拒んだ応答のステータス（401・403）。0なら拒まれていない。
	authRejected uint32
}

type activities struct {
	mu sync.Mutex
	m  map[string]*activity
}

func newActivities() *activities { return &activities{m: map[string]*activity{}} }

// reset はidの観測を始め直す（実行の開始・再開のとき）。
func (a *activities) reset(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.m[id] = &activity{last: time.Now().UTC(), inflight: map[uint64]*inflightReq{}}
}

func (a *activities) drop(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.m, id)
}

func (a *activities) update(id string, f func(*activity)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	act := a.m[id]
	if act == nil {
		return
	}
	f(act)
}

// dropInflightBefore はt以前に始まった進行中のリクエストを捨てる（終わりの来ないものの掃除）。
func (act *activity) dropInflightBefore(t time.Time) {
	for id, r := range act.inflight {
		if !r.started.After(t) {
			delete(act.inflight, id)
		}
	}
}

// observeClaudeAuth は、終わったリクエストがトークンを拒まれたかどうかを覚える。見るのは
// 会話の本体（/v1/messages）だけ。Claude Codeは起動時に設定などの補助のパスも呼び、
// 正しいトークンでもそれらが401・403を返さないとは確かめていないため。成功すれば忘れる。
func (act *activity) observeClaudeAuth(h *apiv1.HttpActivity) {
	if h.Host != claudeAPIHost || !strings.HasPrefix(h.Path, "/v1/messages") {
		return
	}
	switch {
	case h.Status == 401 || h.Status == 403:
		act.authRejected = h.Status
	case h.Status >= 200 && h.Status < 300:
		act.authRejected = 0
	}
}

// touch は活動があったことを記録する。待ちの表示は活動で上書きされる。
func (act *activity) touch(detail string) {
	act.last = time.Now().UTC()
	act.inputWait = ""
	if detail != "" {
		act.detail = detail
	}
}

// compute はワークスペースの状態と観測からActivityを決める。優先順は、実行の状態
// （終わった・止めた・人間の判断待ち）→プロセスの死→進行中のAPIリクエスト→ゲストの言う待ち→
// 無活動の長さ、の順。APIリクエストを入力待ちより先に見るのは、フックはゲストの協力が前提の
// 補助情報で、ホストが観測したリクエストの方が確かなため。
func (a *activities) compute(w *workspace.Workspace, stallAfter time.Duration, now time.Time) *apiv1.Activity {
	a.mu.Lock()
	act := a.m[w.ID]
	var cp activity
	inflight := 0
	if act != nil {
		cp = *act
		act.dropInflightBefore(now.Add(-inflightStale))
		inflight = len(act.inflight)
	}
	a.mu.Unlock()

	out := &apiv1.Activity{}
	if act != nil && !cp.last.IsZero() {
		out.LastActivity = timestamppb.New(cp.last)
	}
	switch w.State {
	case workspace.StateDone, workspace.StateStopped, workspace.StateBlocked, workspace.StateSuspended:
		out.Kind = apiv1.ActivityKind_ACTIVITY_KIND_IDLE
		return out
	case workspace.StateWaitingGate:
		out.Kind = apiv1.ActivityKind_ACTIVITY_KIND_WAITING_GATE
		return out
	case workspace.StateWaitingQuestion:
		out.Kind = apiv1.ActivityKind_ACTIVITY_KIND_WAITING_QUESTION
		return out
	case workspace.StateStarting:
		// エージェントがまだ動いておらず、契約の「まだ観測していない」にあたる。段階はdetailで見せる。
		out.Kind = apiv1.ActivityKind_ACTIVITY_KIND_IDLE
		out.Detail = cp.detail
		return out
	}
	if act == nil {
		out.Kind = apiv1.ActivityKind_ACTIVITY_KIND_IDLE
		return out
	}
	out.InputWait = cp.inputWait
	out.Detail = cp.detail
	switch {
	case cp.dead:
		out.Kind = apiv1.ActivityKind_ACTIVITY_KIND_DEAD
	case cp.authRejected != 0:
		// 進行中のリクエストより先に見る。Claude Codeは拒まれても呼び直すので、
		// 進行中を先に見るとWORKINGと行き来して原因が見えなくなる。
		out.Kind = apiv1.ActivityKind_ACTIVITY_KIND_AUTH_REJECTED
		out.Detail = fmt.Sprintf("the Claude API rejected the token (HTTP %d); register it again with 'masuda secret set', then 'masuda stop' and 'masuda resume'", cp.authRejected)
	case inflight > 0:
		out.Kind = apiv1.ActivityKind_ACTIVITY_KIND_WORKING
	case cp.inputWait != "":
		out.Kind = apiv1.ActivityKind_ACTIVITY_KIND_WAITING_INPUT
	case w.State == workspace.StateRunning && stallAfter > 0 && now.Sub(cp.last) > stallAfter:
		out.Kind = apiv1.ActivityKind_ACTIVITY_KIND_STALLED
	default:
		out.Kind = apiv1.ActivityKind_ACTIVITY_KIND_WORKING
	}
	return out
}

// ---------------------------------------------------------------------------
// 情報源: ゲストのフック
// ---------------------------------------------------------------------------

// hookInput はClaude Codeのフック入力JSONのうち活動の判定に使う項目。
type hookInput struct {
	Event            string `json:"hook_event_name"`
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
	ToolName         string `json:"tool_name"`
}

// notificationWait はNotificationフックのnotification_typeを公開APIのinput_waitへ写す。
// 載っていない種類（auth_success等）は待ちではないので写さない。
var notificationWait = map[string]string{
	"idle_prompt":        "idle",
	"permission_prompt":  "permission",
	"elicitation_dialog": "question",
}

// observeHook はフック1件を活動に反映し、guest_hookイベントを流す。
func (b *backend) observeHook(id string, body []byte) {
	var in hookInput
	_ = json.Unmarshal(body, &in)
	detail := ""
	switch in.Event {
	case "Notification":
		detail = in.NotificationType
		wait := notificationWait[in.NotificationType]
		b.acts.update(id, func(act *activity) {
			if wait != "" {
				act.inputWait = wait
			}
			if in.NotificationType == "idle_prompt" {
				act.dropInflightBefore(time.Now().UTC().Add(-idlePromptAfter))
			}
			if in.Message != "" {
				act.detail = in.Message
			}
		})
	case "PostToolUse":
		detail = in.ToolName
		b.acts.update(id, func(act *activity) { act.touch("tool " + in.ToolName) })
	case "Stop", "SubagentStop":
		b.acts.update(id, func(act *activity) {
			act.last = time.Now().UTC()
			act.detail = "turn ended (" + in.Event + ")"
		})
	case "SessionEnd":
		b.acts.update(id, func(act *activity) {
			act.dead = true
			act.detail = "claude session ended"
		})
	}
	b.events.publish(&apiv1.WorkspaceEvent{
		WorkspaceId: id,
		Event:       &apiv1.WorkspaceEvent_GuestHook{GuestHook: &apiv1.GuestHookEvent{Hook: in.Event, Detail: detail}},
	})
	b.statusChanged(id)
}

// ---------------------------------------------------------------------------
// 情報源: sandboxが観測したHTTP
// ---------------------------------------------------------------------------

// watchSandbox はsandboxのWatchEventsを読み、HTTPの開始・完了・拒否を活動とhttpイベントに、
// sandboxの停止・失敗をDEADに写す。ctxが終わるか、sandboxが壊されてストリームが終わるまで続く。
func (b *backend) watchSandbox(ctx context.Context, id string) {
	st, err := b.sandbox.WatchEvents(ctx, connect.NewRequest(&sandboxv1.WatchEventsRequest{Id: id}))
	if err != nil {
		return
	}
	defer st.Close()
	for st.Receive() {
		ev := st.Msg()
		var http *apiv1.HttpActivity
		switch e := ev.Event.(type) {
		case *sandboxv1.SandboxEvent_HttpStarted:
			http = &apiv1.HttpActivity{Method: e.HttpStarted.Method, Host: e.HttpStarted.Host, Path: e.HttpStarted.Path}
			b.acts.update(id, func(act *activity) {
				act.inflight[e.HttpStarted.RequestId] = &inflightReq{http: http, started: time.Now().UTC()}
				act.touch("")
			})
		case *sandboxv1.SandboxEvent_HttpFinished:
			http = &apiv1.HttpActivity{Status: e.HttpFinished.Status, DurationMs: e.HttpFinished.DurationMs}
			b.acts.update(id, func(act *activity) {
				if started := act.inflight[e.HttpFinished.RequestId]; started != nil {
					http.Method, http.Host, http.Path = started.http.Method, started.http.Host, started.http.Path
				}
				act.observeClaudeAuth(http)
				delete(act.inflight, e.HttpFinished.RequestId)
				act.touch("")
			})
		case *sandboxv1.SandboxEvent_HttpDenied:
			http = &apiv1.HttpActivity{Host: e.HttpDenied.Host, Denied: true}
			b.acts.update(id, func(act *activity) { act.detail = "denied " + e.HttpDenied.Host + ": " + e.HttpDenied.Reason })
		case *sandboxv1.SandboxEvent_StateChanged_:
			switch e.StateChanged.State {
			case sandboxv1.SandboxState_SANDBOX_STATE_STOPPED, sandboxv1.SandboxState_SANDBOX_STATE_FAILED:
				if ctx.Err() == nil {
					b.acts.update(id, func(act *activity) {
						act.dead = true
						act.detail = "sandbox " + e.StateChanged.State.String() + " " + e.StateChanged.Detail
					})
					b.statusChanged(id)
				}
			}
			continue
		default:
			continue
		}
		b.events.publish(&apiv1.WorkspaceEvent{WorkspaceId: id, Time: ev.Time, Event: &apiv1.WorkspaceEvent_Http{Http: http}})
		b.statusChanged(id)
	}
}

// ---------------------------------------------------------------------------
// 定期の見回り: 無活動（STALLED）とプロセスの生存（DEAD）
// ---------------------------------------------------------------------------

// livenessEvery はExecでtmuxのセッションを確かめる間隔。
const livenessEvery = 30 * time.Second

// patrol は動いているワークスペースを定期的に見て、時間の経過だけで変わる活動
// （STALLED）と、Execで分かるプロセスの死（DEAD）をstatusイベントに反映する。
func (b *backend) patrol(ctx context.Context) {
	t := time.NewTimer(b.patrolTick())
	defer t.Stop()
	lastLiveness := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(b.patrolTick())
		checkLiveness := !b.fake && time.Since(lastLiveness) >= livenessEvery
		if checkLiveness {
			lastLiveness = time.Now()
		}
		for _, c := range b.allRuns() {
			if checkLiveness && c.isBooted() && !b.alive(ctx, c.id) {
				b.acts.update(c.id, func(act *activity) {
					act.dead = true
					act.detail = "tmux session " + guest.TmuxSession + " is gone"
				})
			}
			b.statusChanged(c.id)
		}
	}
}

// alive はゲストでメインセッションのtmuxが生きているかを確かめる。claudeはtmuxの
// セッションのコマンドとして直接起動している（guest.Launch）ので、claudeが終わると
// セッションも消える。プロセス名で探さないのは、Claude Codeの入れ方（ネイティブ・npm）で
// プロセス名が変わるため。確認そのものに失敗したときは生きているとみなす（DEADは確かなときだけ）。
func (b *backend) alive(ctx context.Context, id string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := guest.Exec(ctx, b.sandbox, &sandboxv1.ExecRequest{
		Id:   id,
		Argv: []string{"/usr/bin/tmux", "has-session", "-t", guest.TmuxSession},
		Cwd:  "/",
	})
	if err != nil {
		return true
	}
	return res.ExitCode == 0
}
