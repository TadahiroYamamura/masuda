package serve

import (
	"context"
	"fmt"
	"sync"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/TadahiroYamamura/masuda-engine/engine"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
)

// eventBufferSize はWatchの再送のために持っておくイベントの数（全ワークスペース合計）。
// メモリにだけ持つので、serveを再起動すると再送できるのは再起動後のイベントだけになる。
// 実行記録そのものは`records/execution-log.jsonl`に残るので、取りこぼしても失われるのは
// 流れの途中経過（HTTP・フック）だけという割り切り。
const eventBufferSize = 10000

// eventBus はWatchへ流すイベントの通し番号付けと保持。seqは全ワークスペースで1本なので、
// ワークスペースを指定しないWatchでもafter_seqで続きから受け取れる。
type eventBus struct {
	mu     sync.Mutex
	seq    uint64
	events []*apiv1.WorkspaceEvent
	// wake は新しいイベントが入ったら閉じて作り直す。待っているWatchはこれで起きる。
	wake chan struct{}
	// lastStatus はワークスペースごとに最後に流したstatusの比較用の形。同じ内容のstatusを
	// 繰り返し流さないために使う。
	lastStatus map[string]*apiv1.Workspace
}

func newEventBus() *eventBus {
	return &eventBus{wake: make(chan struct{}), lastStatus: map[string]*apiv1.Workspace{}}
}

func (e *eventBus) publish(ev *apiv1.WorkspaceEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.publishLocked(ev)
}

func (e *eventBus) publishLocked(ev *apiv1.WorkspaceEvent) {
	e.seq++
	ev.Seq = e.seq
	if ev.Time == nil {
		ev.Time = timestamppb.Now()
	}
	e.events = append(e.events, ev)
	if len(e.events) > eventBufferSize {
		e.events = append([]*apiv1.WorkspaceEvent(nil), e.events[len(e.events)-eventBufferSize:]...)
	}
	close(e.wake)
	e.wake = make(chan struct{})
}

// publishStatus はwの状態が前に流したものと違えばstatusイベントを流す。時刻（updated_at・
// last_activity）だけの違いでは流さない。HTTPの1往復ごとにstatusが流れると多すぎるため。
func (e *eventBus) publishStatus(w *apiv1.Workspace) {
	key := proto.Clone(w).(*apiv1.Workspace)
	key.UpdatedAt = nil
	if key.Activity != nil {
		key.Activity.LastActivity = nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if prev := e.lastStatus[w.Id]; prev != nil && proto.Equal(prev, key) {
		return
	}
	e.lastStatus[w.Id] = key
	e.publishLocked(&apiv1.WorkspaceEvent{WorkspaceId: w.Id, Event: &apiv1.WorkspaceEvent_Status{Status: w}})
}

func (e *eventBus) forget(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.lastStatus, id)
}

// since はseqより後のイベントのうちidのもの（idが空なら全部）と、次を待つチャネルを返す。
// workspace_idが空のイベント（ディスク使用量の警告等、serve全体のこと）はどのidにも流す。
func (e *eventBus) since(seq uint64, id string) ([]*apiv1.WorkspaceEvent, uint64, <-chan struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*apiv1.WorkspaceEvent
	for _, ev := range e.events {
		if ev.Seq > seq && (id == "" || ev.WorkspaceId == id || ev.WorkspaceId == "") {
			out = append(out, ev)
		}
	}
	return out, e.seq, e.wake
}

func (e *eventBus) head() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.seq
}

// ---------------------------------------------------------------------------
// 発行の窓口
// ---------------------------------------------------------------------------

// statusChanged はワークスペースidの今の状態をstatusイベントとして流す（変化があれば）。
// 状態（workspace.json）・活動・開いたゲートや質問を書き換えたら呼ぶ。
func (b *backend) statusChanged(id string) {
	w, err := b.store.Get(id)
	if err != nil {
		return
	}
	b.events.publishStatus(b.toProto(w))
}

func (b *backend) publishEngine(id string, e engine.Event) {
	b.events.publish(&apiv1.WorkspaceEvent{
		WorkspaceId: id,
		Time:        timestamppb.New(e.Time),
		Event: &apiv1.WorkspaceEvent_Engine{Engine: &apiv1.EngineEvent{
			Kind: e.Kind, Occurrence: e.Occurrence, Workflow: e.Workflow,
			Node: e.Node, Outcome: e.Outcome, Detail: e.Detail,
		}},
	})
}

// ---------------------------------------------------------------------------
// Watch
// ---------------------------------------------------------------------------

func (s *workspaceService) Watch(ctx context.Context, req *connect.Request[apiv1.WatchRequest], stream *connect.ServerStream[apiv1.WorkspaceEvent]) error {
	id := req.Msg.Id
	if id != "" {
		if _, err := s.lookup(id); err != nil {
			return err
		}
	}
	b := s.backend
	next := req.Msg.AfterSeq
	if head := b.events.head(); next > head {
		// serveの再起動で番号が振り直された後に古い番号を渡されると、黙って待つと番号が
		// 追いつくまで何も届かない。クライアントにafter_seq: 0での繋ぎ直しを求める。
		return connect.NewError(connect.CodeOutOfRange, fmt.Errorf("after_seq %d is beyond the latest seq %d (the server may have restarted); watch again with after_seq 0", next, head))
	}
	if next == 0 {
		// 新しいものだけを流す指定でも、最初に今の状態を1つ送る。購読の開始と状態の変化が
		// 前後したときに、変化を取りこぼしたまま次の変化まで何も見えなくなるのを避けるため。
		// 新しい番号は振らず、seqには今の最新を入れる（その値をafter_seqに渡せば続きから受け取れる）。
		next = b.events.head()
		ws, err := s.watchTargets(id)
		if err != nil {
			return err
		}
		for _, w := range ws {
			if err := stream.Send(&apiv1.WorkspaceEvent{
				Seq: next, Time: timestamppb.Now(), WorkspaceId: w.Id,
				Event: &apiv1.WorkspaceEvent_Status{Status: w},
			}); err != nil {
				return err
			}
		}
	}
	for {
		evs, head, wake := b.events.since(next, id)
		for _, ev := range evs {
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
		next = head
		select {
		case <-wake:
		case <-ctx.Done():
			return nil
		case <-b.ctx.Done():
			return nil
		}
	}
}

func (s *workspaceService) watchTargets(id string) ([]*apiv1.Workspace, error) {
	if id != "" {
		w, err := s.lookup(id)
		if err != nil {
			return nil, err
		}
		return []*apiv1.Workspace{s.backend.toProto(w)}, nil
	}
	all, err := s.store.List("")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*apiv1.Workspace, 0, len(all))
	for _, w := range all {
		out = append(out, s.backend.toProto(w))
	}
	return out, nil
}
