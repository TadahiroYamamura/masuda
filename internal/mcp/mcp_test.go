package mcp

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// blockingHost はnext_taskとrun_privileged_commandで、ctxが取り消されるか上限まで待ち、
// 取り消されたかどうかをgotへ送る。
type blockingHost struct{ got chan bool }

func (h blockingHost) wait(ctx context.Context) (any, error) {
	select {
	case <-ctx.Done():
		h.got <- true
	case <-time.After(3 * time.Second):
		h.got <- false
	}
	return map[string]any{}, nil
}

func (h blockingHost) NextTask(ctx context.Context, _ string) (any, error) { return h.wait(ctx) }
func (h blockingHost) RunPrivilegedCommand(ctx context.Context, _ string) (any, error) {
	return h.wait(ctx)
}
func (blockingHost) WriteOutput(context.Context, string, string, string) (any, error) {
	return nil, nil
}
func (blockingHost) ReportResult(context.Context, string, string, string, string) (any, error) {
	return nil, nil
}
func (blockingHost) ReportConcern(context.Context, string, string) (any, error) { return nil, nil }
func (blockingHost) AskHuman(context.Context, string, []Question) (any, error)  { return nil, nil }
func (blockingHost) Hook([]byte)                                                {}

// ゲストが応答を待たずに切断すると、next_taskのctxは取り消される（Hostが、結果が届かないことに
// 気づけるように）。run_privileged_commandのctxは取り消さない（切断しても最後まで動かす）。
func Testゲストが切断するとnext_taskのctxだけが取り消される(t *testing.T) {
	cases := []struct {
		tool      string
		cancelled bool
	}{
		{"next_task", true},
		{"run_privileged_command", false},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			h := blockingHost{got: make(chan bool, 1)}
			s, err := Start(h)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			req, _ := http.NewRequest("POST", "http://"+s.Addr()+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+c.tool+`","arguments":{"name":"x"}}}`))
			req.Header.Set("content-type", "application/json")
			req.Header.Set("accept", "application/json, text/event-stream")
			if _, err := (&http.Client{Timeout: 200 * time.Millisecond}).Do(req); err == nil {
				t.Fatal("the client must give up before the tool returns")
			}
			if got := <-h.got; got != c.cancelled {
				t.Fatalf("ctx cancelled = %v, want %v", got, c.cancelled)
			}
		})
	}
}
