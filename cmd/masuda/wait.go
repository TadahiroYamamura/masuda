package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
)

// waitEvents は`masuda wait`が終わるきっかけ。--ignoreで除ける名前でもある。
var waitEvents = []string{"gate", "question", "done", "stopped", "suspended", "blocked", "dead", "auth_rejected", "stalled"}

// waitTimeoutCode は--timeoutまでに何も起きなかったときの終了コード。出番が来たとき（0）・
// masuda自体の失敗（1）・使い方の誤り（2）と区別できるよう分ける。
const waitTimeoutCode = 3

// waitReconnect はserveとの接続が切れたときに繋ぎ直すまでの間。serveの再起動を待つ。
var waitReconnect = 2 * time.Second

// runWait は`masuda wait`。人の出番（ゲート・質問が開いた、終わった・止まった、エージェントが
// 動いていない）が来るまで待ち、1行出して終わる。呼んだ時点で当てはまればすぐ終わる。
// sleepとlistを繰り返す見張り方の代わりに、バックグラウンドで1回動かして終わりを待つためのもの。
func runWait(args []string) error {
	c := newCommand("wait", "wait <id> [--timeout <duration>] [--ignore <event>[,<event>...]]")
	timeout := c.fs.Duration("timeout", 0, "give up after this long and exit with code 3 (0: wait forever)")
	var ignores multiFlag
	c.fs.Var(&ignores, "ignore", "do not stop on these events (repeatable or comma-separated): "+strings.Join(waitEvents, ", "))
	pos, err := c.parse(args, 1, 1)
	if err != nil {
		return err
	}
	ignore, err := parseWaitIgnore(ignores)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	cl := c.clients()
	open := func(ctx context.Context) (statusStream, error) {
		st, err := cl.ws.Watch(ctx, connect.NewRequest(&apiv1.WatchRequest{Id: pos[0]}))
		if err != nil {
			return nil, err
		}
		return st, nil
	}
	w, event, err := waitFor(ctx, open, ignore)
	if errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintf(os.Stderr, "masuda wait: nothing happened to %s within %s\n", pos[0], *timeout)
		return exitCodeError{waitTimeoutCode}
	}
	if err != nil {
		return err
	}
	fmt.Println(waitLine(w, event, gateOccurrence(cl, w, event)))
	return nil
}

func parseWaitIgnore(flags []string) (map[string]bool, error) {
	ignore := map[string]bool{}
	for _, f := range flags {
		for _, name := range strings.Split(f, ",") {
			name = strings.TrimSpace(name)
			if !slices.Contains(waitEvents, name) {
				return nil, fmt.Errorf("--ignore %q: unknown event; use one of %s", name, strings.Join(waitEvents, ", "))
			}
			ignore[name] = true
		}
	}
	return ignore, nil
}

// statusStream は公開APIのWatchのストリームのうち、waitが使う分。
type statusStream interface {
	Receive() bool
	Msg() *apiv1.WorkspaceEvent
	Err() error
	Close() error
}

// waitFor はstatusを読み、除いていないきっかけに当たった時点のワークスペースを返す。
// 接続が切れたら（serveの再起動など）間を置いて繋ぎ直す。繋ぎ直すと最初に今の状態が届くので、
// 切れていた間の変化も取りこぼさない。1つでもイベントを受けた後の誤りは、要求は受け付けられて
// いたので接続が切れたものとみなす（serveが落ちると、切れ方によってはinvalid_argumentの
// 「incomplete envelope」になり、コードだけでは引数の誤りと区別できない）。最初のイベントより
// 前の誤りは、ワークスペースが無い等の繋ぎ直しても直らないものなら返す。
func waitFor(ctx context.Context, open func(context.Context) (statusStream, error), ignore map[string]bool) (*apiv1.Workspace, string, error) {
	for {
		st, err := open(ctx)
		received := false
		if err == nil {
			for st.Receive() {
				received = true
				s, ok := st.Msg().Event.(*apiv1.WorkspaceEvent_Status)
				if !ok {
					continue
				}
				if event := waitEvent(s.Status, ignore); event != "" {
					st.Close()
					return s.Status, event, nil
				}
			}
			err = st.Err()
			st.Close()
		}
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		if err != nil && !received && !retryableWaitError(err) {
			return nil, "", err
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(waitReconnect):
		}
	}
}

func retryableWaitError(err error) bool {
	var unreachable *serveUnreachableError
	if errors.As(err, &unreachable) {
		return true
	}
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeUnknown, connect.CodeOutOfRange:
		return true
	}
	return false
}

// waitEvent はワークスペースが当てはまるきっかけを返す（無ければ""）。終わった・止まったを先に
// 見るのは、そのときの活動はidleで、開いたゲートの記録が残っていても待っているのは人ではないため。
func waitEvent(w *apiv1.Workspace, ignore map[string]bool) string {
	var candidates []string
	switch w.State {
	case apiv1.WorkspaceState_WORKSPACE_STATE_DONE:
		candidates = append(candidates, "done")
	case apiv1.WorkspaceState_WORKSPACE_STATE_STOPPED:
		candidates = append(candidates, "stopped")
	case apiv1.WorkspaceState_WORKSPACE_STATE_SUSPENDED:
		candidates = append(candidates, "suspended")
	case apiv1.WorkspaceState_WORKSPACE_STATE_BLOCKED:
		candidates = append(candidates, "blocked")
	}
	if len(candidates) == 0 {
		if len(w.OpenGates) > 0 {
			candidates = append(candidates, "gate")
		}
		if len(w.OpenQuestions) > 0 {
			candidates = append(candidates, "question")
		}
		switch w.Activity.GetKind() {
		case apiv1.ActivityKind_ACTIVITY_KIND_DEAD:
			candidates = append(candidates, "dead")
		case apiv1.ActivityKind_ACTIVITY_KIND_AUTH_REJECTED:
			candidates = append(candidates, "auth_rejected")
		case apiv1.ActivityKind_ACTIVITY_KIND_STALLED:
			candidates = append(candidates, "stalled")
		}
	}
	for _, c := range candidates {
		if !ignore[c] {
			return c
		}
	}
	return ""
}

// gateOccurrence はゲートで終わったときに、開いているゲートの出現IDを引く（statusにはゲートの
// 名前しか無い）。引けなければ""で、案内はgate listになる。
func gateOccurrence(cl *clients, w *apiv1.Workspace, event string) string {
	if event != "gate" {
		return ""
	}
	res, err := cl.gates.ListOpen(context.Background(), connect.NewRequest(&apiv1.ListOpenGatesRequest{WorkspaceId: w.Id}))
	if err != nil || len(res.Msg.Gates) == 0 {
		return ""
	}
	return res.Msg.Gates[0].Occurrence
}

// waitLine は終わったときの1行。先頭はきっかけの名前で、最後に次に打つコマンドを添える。
func waitLine(w *apiv1.Workspace, event, gateOcc string) string {
	id := w.Id
	var detail, next string
	switch event {
	case "gate":
		detail = strings.Join(w.OpenGates, ",")
		next = "masuda gate list " + id
		if gateOcc != "" {
			next = "masuda gate show " + id + " " + gateOcc
		}
	case "question":
		detail = strings.Join(w.OpenQuestions, ",")
		next = "masuda question list " + id
	case "done":
		detail = "outcome=" + orDash(w.Outcome)
		if w.Reason != "" {
			detail += " " + firstLine(w.Reason)
		}
		next = "nothing; the run has finished"
	case "stopped":
		next = "masuda resume " + id
	case "suspended":
		detail = firstLine(w.Reason)
		next = "fix the cause, then masuda resume " + id
	case "blocked":
		detail = firstLine(w.Reason)
		next = "masuda remove " + id + " (a blocked run cannot be resumed)"
	case "dead":
		detail = w.Activity.GetDetail()
		next = "masuda stop " + id + " && masuda resume " + id
	case "auth_rejected":
		detail = w.Activity.GetDetail()
		next = "register the token again, then masuda stop " + id + " && masuda resume " + id
	case "stalled":
		next = "masuda chat " + id + " to look (a long test or build also shows as stalled)"
	}
	s := event + " " + id + " " + shortState(w.State) + " " + shortActivity(w.Activity)
	if detail != "" {
		s += " " + detail
	}
	return s + "; next: " + next
}
