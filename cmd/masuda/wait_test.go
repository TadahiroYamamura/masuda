package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
)

// 状態と活動の組み合わせすべてで、きっかけが仕様どおりに決まる。
// 期待値は仕様（終わった・止まったが先、次にゲート・質問、次に活動）から手で書く。
func TestWaitEventは状態と活動のすべての組み合わせで仕様どおりのきっかけを返す(t *testing.T) {
	stateEvent := map[apiv1.WorkspaceState]string{
		apiv1.WorkspaceState_WORKSPACE_STATE_DONE:      "done",
		apiv1.WorkspaceState_WORKSPACE_STATE_STOPPED:   "stopped",
		apiv1.WorkspaceState_WORKSPACE_STATE_SUSPENDED: "suspended",
		apiv1.WorkspaceState_WORKSPACE_STATE_BLOCKED:   "blocked",
	}
	activityEvent := map[apiv1.ActivityKind]string{
		apiv1.ActivityKind_ACTIVITY_KIND_DEAD:          "dead",
		apiv1.ActivityKind_ACTIVITY_KIND_AUTH_REJECTED: "auth_rejected",
		apiv1.ActivityKind_ACTIVITY_KIND_STALLED:       "stalled",
	}
	for s := range apiv1.WorkspaceState_name {
		state := apiv1.WorkspaceState(s)
		for k := range apiv1.ActivityKind_name {
			kind := apiv1.ActivityKind(k)
			for _, gate := range []bool{false, true} {
				w := &apiv1.Workspace{State: state, Activity: &apiv1.Activity{Kind: kind}}
				if gate {
					w.OpenGates = []string{"plan"}
				}
				want := stateEvent[state]
				if want == "" && gate {
					want = "gate"
				}
				if want == "" {
					want = activityEvent[kind]
				}
				if got := waitEvent(w, nil); got != want {
					t.Errorf("state %v, activity %v, gate %v: event = %q, want %q", state, kind, gate, got, want)
				}
			}
		}
	}
	q := &apiv1.Workspace{State: apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_QUESTION, OpenQuestions: []string{"0000006"}, Activity: &apiv1.Activity{Kind: apiv1.ActivityKind_ACTIVITY_KIND_WAITING_QUESTION}}
	if got := waitEvent(q, nil); got != "question" {
		t.Errorf("open question: event = %q", got)
	}
}

func TestWaitEventは除いたきっかけを飛ばして次に当たるものを返す(t *testing.T) {
	w := &apiv1.Workspace{State: apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING, OpenGates: []string{"plan"}, Activity: &apiv1.Activity{Kind: apiv1.ActivityKind_ACTIVITY_KIND_STALLED}}
	cases := []struct {
		ignore map[string]bool
		want   string
	}{
		{nil, "gate"},
		{map[string]bool{"gate": true}, "stalled"},
		{map[string]bool{"gate": true, "stalled": true}, ""},
	}
	for _, c := range cases {
		if got := waitEvent(w, c.ignore); got != c.want {
			t.Errorf("ignore %v: event = %q, want %q", c.ignore, got, c.want)
		}
	}
	// 終わったワークスペースのきっかけを除いても、残ったゲートの記録や活動では終わらない（待っているのは人ではない）。
	done := &apiv1.Workspace{State: apiv1.WorkspaceState_WORKSPACE_STATE_DONE, OpenGates: []string{"plan"}, Activity: &apiv1.Activity{Kind: apiv1.ActivityKind_ACTIVITY_KIND_STALLED}}
	if got := waitEvent(done, map[string]bool{"done": true}); got != "" {
		t.Errorf("ignored done: event = %q, want none", got)
	}
}

func TestParseWaitIgnoreは繰り返しとカンマ区切りを受け知らない名前を断る(t *testing.T) {
	got, err := parseWaitIgnore([]string{"stalled", "dead, auth_rejected"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !got["stalled"] || !got["dead"] || !got["auth_rejected"] {
		t.Fatalf("ignore = %v", got)
	}
	if _, err := parseWaitIgnore([]string{"stall"}); err == nil || !strings.Contains(err.Error(), `unknown event`) {
		t.Fatalf("a typo must be refused: %v", err)
	}
}

type fakeStatusStream struct {
	events []*apiv1.WorkspaceEvent
	err    error
	i      int
}

func (f *fakeStatusStream) Receive() bool {
	if f.i >= len(f.events) {
		return false
	}
	f.i++
	return true
}
func (f *fakeStatusStream) Msg() *apiv1.WorkspaceEvent { return f.events[f.i-1] }
func (f *fakeStatusStream) Err() error                 { return f.err }
func (f *fakeStatusStream) Close() error               { return nil }

func statusEvent(state apiv1.WorkspaceState, kind apiv1.ActivityKind, gates ...string) *apiv1.WorkspaceEvent {
	return &apiv1.WorkspaceEvent{Event: &apiv1.WorkspaceEvent_Status{Status: &apiv1.Workspace{Id: "w", State: state, Activity: &apiv1.Activity{Kind: kind}, OpenGates: gates}}}
}

func TestWaitForは接続が切れたら繋ぎ直して最初の状態から見直す(t *testing.T) {
	defer func(d time.Duration) { waitReconnect = d }(waitReconnect)
	waitReconnect = time.Millisecond
	running := apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING
	working := apiv1.ActivityKind_ACTIVITY_KIND_WORKING
	streams := []*fakeStatusStream{
		// 1本目: 作業中の状態とhttpのイベントの後、serveが落ちて切れる（強制終了ではinvalid_argumentになる）
		{events: []*apiv1.WorkspaceEvent{
			statusEvent(running, working),
			{Event: &apiv1.WorkspaceEvent_Http{Http: &apiv1.HttpActivity{Host: "api.anthropic.com"}}},
		}, err: connect.NewError(connect.CodeInvalidArgument, errors.New("protocol error: incomplete envelope: unexpected EOF"))},
		// 2本目: 繋ぎ直した最初の状態でゲートが開いている
		{events: []*apiv1.WorkspaceEvent{statusEvent(apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE, apiv1.ActivityKind_ACTIVITY_KIND_WAITING_GATE, "plan")}},
	}
	opened := 0
	open := func(context.Context) (statusStream, error) {
		if opened == 0 {
			opened++
			return nil, &serveUnreachableError{socket: "s", err: errors.New("refused")}
		}
		st := streams[opened-1]
		opened++
		return st, nil
	}
	w, event, err := waitFor(context.Background(), open, nil)
	if err != nil {
		t.Fatal(err)
	}
	if event != "gate" || w.OpenGates[0] != "plan" || opened != 3 {
		t.Fatalf("event %q, gates %v, opened %d", event, w.OpenGates, opened)
	}
}

func TestWaitForは繋ぎ直しても直らない誤りと時間切れを返す(t *testing.T) {
	notFound := func(context.Context) (statusStream, error) {
		return &fakeStatusStream{err: connect.NewError(connect.CodeNotFound, errors.New("no such workspace"))}, nil
	}
	bounded, cancelBounded := context.WithTimeout(context.Background(), time.Second)
	defer cancelBounded()
	if _, _, err := waitFor(bounded, notFound, nil); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	idle := func(ctx context.Context) (statusStream, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, _, err := waitFor(ctx, idle, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitLineは先頭にきっかけを置き次に打つコマンドを添える(t *testing.T) {
	gate := &apiv1.Workspace{Id: "w1", State: apiv1.WorkspaceState_WORKSPACE_STATE_WAITING_GATE, OpenGates: []string{"plan"}, Activity: &apiv1.Activity{Kind: apiv1.ActivityKind_ACTIVITY_KIND_WAITING_GATE}}
	cases := []struct {
		name, event, occ, want string
		w                      *apiv1.Workspace
	}{
		{"ゲートの出現が分かる", "gate", "0000006", "gate w1 waiting_gate waiting_gate plan; next: masuda gate show w1 0000006", gate},
		{"ゲートの出現が分からない", "gate", "", "gate w1 waiting_gate waiting_gate plan; next: masuda gate list w1", gate},
		{"止まった理由", "suspended", "", "suspended w1 suspended idle privileged command x is not approved; next: fix the cause, then masuda resume w1",
			&apiv1.Workspace{Id: "w1", State: apiv1.WorkspaceState_WORKSPACE_STATE_SUSPENDED, Reason: "privileged command x is not approved\nmore", Activity: &apiv1.Activity{Kind: apiv1.ActivityKind_ACTIVITY_KIND_IDLE}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := waitLine(c.w, c.event, c.occ); got != c.want {
				t.Fatalf("line = %q\nwant   %q", got, c.want)
			}
		})
	}
}
