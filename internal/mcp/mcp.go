// Package mcp は、ゲストのエージェントが`http://masuda.internal:7000/`で話す相手
// （docs/guest-protocol.md）をホストのループバックのポートで提供する。`/mcp`がMCP
// （Streamable HTTP）、`/hooks`がClaude Codeフックの受け口。ワークスペースごとに1つ動かし、
// sandboxのtcp_mapsで`masuda.internal:7000`をこのポートへ対応付ける。
//
// ツールの中身（engineとの仲介）はHostが持ち、このパッケージはプロトコルの形
// （引数の読み取りと戻り値のJSON）だけを受け持つ。
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Question はask_humanの質問1つ。
type Question struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Options []string `json:"options,omitempty"`
}

// Host はツールの実体。戻り値はそのままJSONにしてツールの結果のテキストにする。
// エラーはツールのエラー（isError）として返す。プロトコル上の拒否（`accepted: false`等）は
// エラーでなく戻り値で表すこと。
type Host interface {
	// NextTask のagentIDは、メインセッションが直前に受け取ったタスクを担当したサブエージェントのID
	// （空なら報告なし）。
	NextTask(ctx context.Context, agentID string) (any, error)
	WriteOutput(ctx context.Context, occurrence, name, content string) (any, error)
	ReportResult(ctx context.Context, occurrence, outcome, feedback, agentID string) (any, error)
	ReportConcern(ctx context.Context, occurrence, text string) (any, error)
	AskHuman(ctx context.Context, occurrence string, questions []Question) (any, error)
	RunPrivilegedCommand(ctx context.Context, name string) (any, error)
	// Hook はPOST /hooksで届いたJSONをそのまま受け取る。
	Hook(body []byte)
}

// ErrNotImplemented はまだ実装していないツールを表す。
var ErrNotImplemented = errors.New("not implemented")

// maxHookBytes はフック1件の上限。フックはstdinのJSONをそのまま送るので、ツールの入出力を
// 含むPostToolUseは大きくなりうる。記録に残すのは補助情報なので上限で切る。
const maxHookBytes = 1 << 20

// Server は1ワークスペースのMCPとフックの受け口。
type Server struct {
	ln   net.Listener
	http *http.Server
	done chan struct{}
}

// Start は127.0.0.1の空いているポートで待ち受けを始める。
func Start(host Host) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: "masuda", Version: "1"}, nil)
	register(srv, host)
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, &sdk.StreamableHTTPOptions{
		// セッションを持たない。ゲストのclaudeが再起動しても、セッションIDの食い違いで
		// 呼び出しが拒否されないようにするため。next_taskの位置はengineの記録から決まるので、
		// サーバー側に会話の状態は要らない。
		Stateless: true,
		// 長くブロックするnext_taskの結果を1つのJSONで返す。
		JSONResponse: true,
		// ゲストからはHostが`masuda.internal:7000`のままループバックへ届く。既定のDNSリバインディング
		// 対策はそれを拒否してしまう。このポートへ届く経路はsandboxのtcp_mapsだけ（別のVMや
		// ブラウザから来る想定はない）なので外す。
		DisableLocalhostProtection: true,
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.HandleFunc("/hooks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, maxHookBytes))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		host.Hook(b)
		w.WriteHeader(http.StatusNoContent)
	})
	s := &Server{ln: ln, http: &http.Server{Handler: mux}, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		_ = s.http.Serve(ln)
	}()
	return s, nil
}

// Port は待ち受けているポート。
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Addr は"127.0.0.1:<port>"。
func (s *Server) Addr() string { return "127.0.0.1:" + strconv.Itoa(s.Port()) }

// Close は待ち受けを止める。ブロック中のnext_task等はリクエストのctxが取り消されて戻る。
func (s *Server) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.http.Shutdown(ctx); err != nil {
		_ = s.http.Close()
	}
	<-s.done
}

func schema(props string, required ...string) json.RawMessage {
	req, _ := json.Marshal(required)
	if required == nil {
		req = []byte("[]")
	}
	return json.RawMessage(`{"type":"object","properties":{` + props + `},"required":` + string(req) + `}`)
}

const (
	occProp = `"occurrence":{"type":"string","description":"タスクファイルにある出現ID"}`
)

func register(srv *sdk.Server, host Host) {
	add := func(name, desc string, in json.RawMessage, f func(ctx context.Context, args json.RawMessage) (any, error)) {
		srv.AddTool(&sdk.Tool{Name: name, Description: desc, InputSchema: in}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			out, err := f(ctx, req.Params.Arguments)
			if err != nil {
				return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: err.Error()}}}, nil
			}
			b, err := json.Marshal(out)
			if err != nil {
				return nil, err
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}}, nil
		})
	}
	add("next_task", "次のタスクを受け取る。人間の判断（ゲート・質問）を待つ間はブロックする。",
		schema(`"agent_id":{"type":"string","description":"直前に完了したタスクを担当したサブエージェントのID（Agentツールの結果のagentId）。続きを送って同じサブエージェントを続けたときもそのID"}`),
		func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				AgentID string `json:"agent_id"`
			}
			if err := decode(raw, &a); err != nil {
				return nil, err
			}
			return host.NextTask(ctx, a.AgentID)
		})

	add("write_output", "タスクの出力データを書く。スキーマに合わなければaccepted: falseと理由を返す。",
		schema(occProp+`,"name":{"type":"string"},"content":{"type":"string"}`, "occurrence", "name", "content"),
		func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct{ Occurrence, Name, Content string }
			if err := decode(raw, &a); err != nil {
				return nil, err
			}
			return host.WriteOutput(ctx, a.Occurrence, a.Name, a.Content)
		})

	add("report_result", "タスクの終わり方（outcome）を報告する。",
		schema(occProp+`,"outcome":{"type":"string"},"feedback":{"type":"string"},"agent_id":{"type":"string"}`, "occurrence", "outcome"),
		func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				Occurrence, Outcome, Feedback string
				AgentID                       string `json:"agent_id"`
			}
			if err := decode(raw, &a); err != nil {
				return nil, err
			}
			return host.ReportResult(ctx, a.Occurrence, a.Outcome, a.Feedback, a.AgentID)
		})

	add("report_concern", "セキュリティ上の懸念を報告する。以後next_taskは人間の判断までブロックする。",
		schema(occProp+`,"text":{"type":"string"}`, "occurrence", "text"),
		func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct{ Occurrence, Text string }
			if err := decode(raw, &a); err != nil {
				return nil, err
			}
			return host.ReportConcern(ctx, a.Occurrence, a.Text)
		})

	add("ask_human", "人間に質問し、答えが来るまで待つ。questionノードのタスクだけが使う。",
		schema(occProp+`,"questions":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"text":{"type":"string"},"options":{"type":"array","items":{"type":"string"}}},"required":["id","text"]}}`, "occurrence", "questions"),
		func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct {
				Occurrence string
				Questions  []Question
			}
			if err := decode(raw, &a); err != nil {
				return nil, err
			}
			return host.AskHuman(ctx, a.Occurrence, a.Questions)
		})

	add("run_privileged_command", "宣言・承認済みの特権コマンドを別のVMで実行する。",
		schema(`"name":{"type":"string"}`, "name"),
		func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a struct{ Name string }
			if err := decode(raw, &a); err != nil {
				return nil, err
			}
			return host.RunPrivilegedCommand(ctx, a.Name)
		})
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}
