package serve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/TadahiroYamamura/masuda-engine/engine"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
)

// startWaitingRun はsmokeの実行を始め、その実行の窓口とフェイクの起こした記録の数え方を返す。
func startWaitingRun(t *testing.T) (*runCtl, func() int) {
	t.Helper()
	dataDir := t.TempDir()
	srv, ws := startServe(t, dataDir)
	res, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: newSmokeRepo(t), Workflow: "workflows/smoke", Branch: "feat/wake", Inputs: smokeInputs}))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, ws, res.Msg.Id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
	c := srv.backend.runFor(res.Msg.Id)
	wakes := func() int {
		b, _ := os.ReadFile(filepath.Join(FakeDir(dataDir), res.Msg.Id, fakeWakesFile))
		return strings.Count(string(b), "\n")
	}
	return c, wakes
}

// waitWakes は起こした記録がn件になり、送り終えた後の記録（回数・時刻）まで済むのを待つ。
func waitWakes(t *testing.T, c *runCtl, wakes func() int, n int) {
	t.Helper()
	settled := func() bool {
		c.seqMu.Lock()
		defer c.seqMu.Unlock()
		return !c.waking && wakes() == n
	}
	deadline := time.Now().Add(5 * time.Second)
	for !settled() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := wakes(); got != n {
		t.Fatalf("woke %d times, want %d", got, n)
	}
}

func (c *runCtl) wantsWake() bool {
	c.seqMu.Lock()
	defer c.seqMu.Unlock()
	return c.wantWake
}

// `waiting`を返すのと状態の変化の、どちらが先でも、メインセッションが次にすることのある変化なら
// 起こし、そうでない変化（ゲートへ進んだ等）や`waiting`を返す前の変化では起こさない。
func TestWaitingを返すのと状態の変化の順によらず起こすべきときだけ起こす(t *testing.T) {
	cases := []struct {
		name string
		run  func(c *runCtl)
		wake bool
	}{
		{"waitingと決めた後、返す前に起こすべき変化があった", func(c *runCtl) {
			seen := c.currentSeq()
			c.bump(true)
			c.markWaiting(seen, "gate")
		}, true},
		{"waitingと決めた後、返す前に起こすべきでない変化があった（取りこぼしとみなして起こす）", func(c *runCtl) {
			seen := c.currentSeq()
			c.bump(false)
			c.markWaiting(seen, "running")
		}, true},
		{"waitingを返した後に起こすべき変化があった", func(c *runCtl) {
			c.markWaiting(c.currentSeq(), "gate")
			c.bump(true)
		}, true},
		{"waitingを返した後に起こすべきでない変化があった", func(c *runCtl) {
			c.markWaiting(c.currentSeq(), "running")
			c.bump(false)
		}, false},
		{"waitingを返した後、変化が無い", func(c *runCtl) {
			c.markWaiting(c.currentSeq(), "gate")
		}, false},
		{"waitingを返していないときに起こすべき変化があった", func(c *runCtl) {
			c.bump(true)
		}, false},
		{"waitingを返した後にnext_taskが来てから変化があった", func(c *runCtl) {
			c.markWaiting(c.currentSeq(), "gate")
			c.clearWaiting()
			c.bump(true)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, wakes := startWaitingRun(t)
			tc.run(c)
			if tc.wake {
				waitWakes(t, c, wakes, 1)
				return
			}
			if c.wantsWake() {
				t.Fatal("must not ask to wake the main session")
			}
			time.Sleep(100 * time.Millisecond)
			if n := wakes(); n != 0 {
				t.Fatalf("woke %d times", n)
			}
		})
	}
}

// 送ってもnext_taskが来なければ、間を置いて送り直し、wakeMaxSends回で打ち切る。next_taskが
// 来れば数え直す。
func TestNext_taskが来なければ間を置いて送り直し回数で打ち切る(t *testing.T) {
	c, wakes := startWaitingRun(t)
	c.markWaiting(c.currentSeq(), "gate")
	c.bump(true)
	waitWakes(t, c, wakes, 1)
	elapse := func() {
		c.seqMu.Lock()
		c.wakeSentAt = c.wakeSentAt.Add(-wakeRetryAfter)
		c.seqMu.Unlock()
	}

	c.tryWake()
	if n := wakes(); n != 1 {
		t.Fatalf("resent before wakeRetryAfter: %d", n)
	}
	for i := 2; i <= wakeMaxSends; i++ {
		elapse()
		c.tryWake()
		if n := wakes(); n != i {
			t.Fatalf("send %d: woke %d times", i, n)
		}
	}
	elapse()
	c.tryWake()
	if n := wakes(); n != wakeMaxSends {
		t.Fatalf("must stop after %d sends: %d", wakeMaxSends, n)
	}

	c.clearWaiting()
	c.markWaiting(c.currentSeq(), "gate")
	c.bump(true)
	waitWakes(t, c, wakes, wakeMaxSends+1)
}

func TestWakesMainSessionはタスクと終わりへ進んだときだけ起こす(t *testing.T) {
	cases := []struct {
		kind engine.StatusKind
		err  error
		want bool
	}{
		{engine.StatusAgent, nil, true},
		{engine.StatusDone, nil, true},
		{engine.StatusBlocked, nil, true},
		{engine.StatusGate, nil, false},
		{engine.StatusQuestion, nil, false},
		{engine.StatusPending, nil, false},
		{engine.StatusAgent, errors.New("exec failed"), false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s_err=%v", tc.kind, tc.err), func(t *testing.T) {
			if got := wakesMainSession(engine.Status{Kind: tc.kind}, tc.err); got != tc.want {
				t.Fatalf("wakesMainSession = %v, want %v", got, tc.want)
			}
		})
	}
}

// goneAfterResult は、待っている間には切れず、結果を返す時点では切れていると分かる接続。
// next_taskが結果を用意し終えた後でゲストが切断した場合を、順番を固定して作る。
type goneAfterResult struct{ context.Context }

func (goneAfterResult) Done() <-chan struct{} { return nil }
func (goneAfterResult) Err() error            { return context.Canceled }

// next_taskが結果を用意している間にゲストとの接続が切れていたら、結果が届かないので起こす。
func TestNext_taskが結果を用意している間に接続が切れたら起こす(t *testing.T) {
	c, wakes := startWaitingRun(t)
	// 切れた接続ではタスクファイルの書き出しも取り消されるので、結果はエラーになりうる。どちらでも起こす。
	_, _ = c.NextTask(goneAfterResult{context.Background()}, "")
	waitWakes(t, c, wakes, 1)
	if task, err := c.NextTask(context.Background(), ""); err != nil || task.(map[string]any)["kind"] != "task" {
		t.Fatalf("after waking, next_task hands out the task: %v %v", task, err)
	}
}
