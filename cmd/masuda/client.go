package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
)

// clients は`masuda serve`の公開APIのクライアント。
type clients struct {
	ws        apiv1connect.WorkspaceServiceClient
	gates     apiv1connect.GateServiceClient
	questions apiv1connect.QuestionServiceClient
	config    apiv1connect.ConfigServiceClient
}

func dial(socket string) *clients {
	httpc := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	const base = "http://masuda"
	return &clients{
		ws:        apiv1connect.NewWorkspaceServiceClient(httpc, base, connect.WithGRPC()),
		gates:     apiv1connect.NewGateServiceClient(httpc, base, connect.WithGRPC()),
		questions: apiv1connect.NewQuestionServiceClient(httpc, base, connect.WithGRPC()),
		config:    apiv1connect.NewConfigServiceClient(httpc, base, connect.WithGRPC()),
	}
}

// command はクライアントのサブコマンド1つ分の引数解析。--socketはどのサブコマンドでも受ける。
type command struct {
	fs     *flag.FlagSet
	socket *string
	usage  string
}

func newCommand(name, usage string) *command {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	c := &command{fs: fs, usage: usage}
	c.socket = fs.String("socket", filepath.Join(runtimeDir(), "masuda.sock"), "masuda serveのUDSのパス")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: masuda %s\n", usage)
		fs.PrintDefaults()
	}
	return c
}

var errUsage = errors.New("usage")

// parse はフラグと位置引数を読む。位置引数の数がnargs（-1なら問わない、maxは上限）に合わなければ使い方を出す。
// フラグを位置引数の後にも書けるよう、位置引数を1つ取るたびに残りを解析し直す。
func (c *command) parse(args []string, min, max int) ([]string, error) {
	var pos []string
	for {
		if err := c.fs.Parse(args); err != nil {
			return nil, err
		}
		rest := c.fs.Args()
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
	if len(pos) < min || (max >= 0 && len(pos) > max) {
		c.fs.Usage()
		return nil, errUsage
	}
	return pos, nil
}

func (c *command) clients() *clients { return dial(*c.socket) }

// multiFlag は繰り返し指定できる文字列フラグ。
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func fmtTime(ts *timestamppb.Timestamp) string {
	if ts == nil || (ts.Seconds == 0 && ts.Nanos == 0) {
		return "-"
	}
	return ts.AsTime().Local().Format("01-02 15:04:05")
}

func shortState(s apiv1.WorkspaceState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "WORKSPACE_STATE_"))
}

func shortActivity(a *apiv1.Activity) string {
	if a == nil {
		return "-"
	}
	k := strings.ToLower(strings.TrimPrefix(a.Kind.String(), "ACTIVITY_KIND_"))
	if a.InputWait != "" {
		k += "(" + a.InputWait + ")"
	}
	return k
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func newTable(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0) }

// ---------------------------------------------------------------------------
// workspaces
// ---------------------------------------------------------------------------

func runRun(args []string) error {
	c := newCommand("run", "run --workflow <path> --branch <name> [--repo <dir>] [--base <ref>] [--image <entry>] [--input name=value|name=@file]...")
	repo := c.fs.String("repo", ".", "対象リポジトリ（作業ツリーのトップ）")
	workflow := c.fs.String("workflow", "", "ワークフロー（例: workflows/develop）")
	branch := c.fs.String("branch", "", "作るブランチ")
	base := c.fs.String("base", "", "分岐元（空ならリポジトリの既定のブランチ）")
	image := c.fs.String("image", "", "イメージのエントリ（空ならsettings.jsonの既定）")
	var inputs multiFlag
	c.fs.Var(&inputs, "input", "入力。name=value、またはname=@file でファイルの中身（繰り返し可）")
	if _, err := c.parse(args, 0, 0); err != nil {
		return err
	}
	if *workflow == "" || *branch == "" {
		c.fs.Usage()
		return errUsage
	}
	root, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	in := map[string][]byte{}
	for _, kv := range inputs {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return fmt.Errorf("--input %q: want name=value or name=@file", kv)
		}
		if path, isFile := strings.CutPrefix(val, "@"); isFile {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			in[name] = b
		} else {
			in[name] = []byte(val)
		}
	}
	res, err := c.clients().ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{
		RepoRoot: root, Workflow: *workflow, Branch: *branch, Base: *base, Image: *image, Inputs: in,
	}))
	if err != nil {
		return err
	}
	fmt.Printf("%s %s\n", res.Msg.Id, shortState(res.Msg.State))
	return nil
}

func runResume(args []string) error {
	c := newCommand("resume", "resume <id>")
	pos, err := c.parse(args, 1, 1)
	if err != nil {
		return err
	}
	res, err := c.clients().ws.Resume(context.Background(), connect.NewRequest(&apiv1.ResumeRequest{Id: pos[0]}))
	if err != nil {
		return err
	}
	fmt.Printf("%s %s\n", res.Msg.Id, shortState(res.Msg.State))
	return nil
}

func runStop(args []string) error {
	c := newCommand("stop", "stop <id>")
	pos, err := c.parse(args, 1, 1)
	if err != nil {
		return err
	}
	res, err := c.clients().ws.Stop(context.Background(), connect.NewRequest(&apiv1.StopRequest{Id: pos[0]}))
	if err != nil {
		return err
	}
	fmt.Printf("%s %s\n", res.Msg.Id, shortState(res.Msg.State))
	return nil
}

func runRemove(args []string) error {
	c := newCommand("remove", "remove <id> [--force]")
	force := c.fs.Bool("force", false, "動いていても止めて消す")
	pos, err := c.parse(args, 1, 1)
	if err != nil {
		return err
	}
	if _, err := c.clients().ws.Remove(context.Background(), connect.NewRequest(&apiv1.RemoveRequest{Id: pos[0], Force: *force})); err != nil {
		return err
	}
	fmt.Printf("%s removed\n", pos[0])
	return nil
}

func runList(args []string) error {
	c := newCommand("list", "list [--repo <dir>]")
	repo := c.fs.String("repo", "", "このリポジトリのワークスペースだけを出す（空なら全部）")
	if _, err := c.parse(args, 0, 0); err != nil {
		return err
	}
	root := ""
	if *repo != "" {
		abs, err := filepath.Abs(*repo)
		if err != nil {
			return err
		}
		root = abs
	}
	res, err := c.clients().ws.List(context.Background(), connect.NewRequest(&apiv1.ListWorkspacesRequest{RepoRoot: root}))
	if err != nil {
		return err
	}
	tw := newTable(os.Stdout)
	fmt.Fprintln(tw, "ID\tSTATE\tACTIVITY\tBRANCH\tWORKFLOW\tPOSITION\tLAST ACTIVITY")
	for _, w := range res.Msg.Workspaces {
		pos := w.Position
		switch {
		case w.Reason != "":
			pos = w.Reason
		case w.Outcome != "":
			pos = "outcome " + w.Outcome
		}
		var last *timestamppb.Timestamp
		if w.Activity != nil {
			last = w.Activity.LastActivity
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", w.Id, shortState(w.State), shortActivity(w.Activity),
			w.Branch, w.Workflow, orDash(firstLine(pos)), fmtTime(last))
	}
	return tw.Flush()
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func runWatch(args []string) error {
	c := newCommand("watch", "watch [<id>] [--after <seq>]")
	after := c.fs.Uint64("after", 0, "このseqより後のイベントから再送する（0なら今の状態と新しいものだけ）")
	pos, err := c.parse(args, 0, 1)
	if err != nil {
		return err
	}
	id := ""
	if len(pos) == 1 {
		id = pos[0]
	}
	st, err := c.clients().ws.Watch(context.Background(), connect.NewRequest(&apiv1.WatchRequest{Id: id, AfterSeq: *after}))
	if err != nil {
		return err
	}
	defer st.Close()
	for st.Receive() {
		fmt.Println(formatEvent(st.Msg()))
	}
	return st.Err()
}

func formatEvent(ev *apiv1.WorkspaceEvent) string {
	head := fmt.Sprintf("%d %s %s", ev.Seq, ev.Time.AsTime().Local().Format(time.TimeOnly), ev.WorkspaceId)
	switch e := ev.Event.(type) {
	case *apiv1.WorkspaceEvent_Status:
		w := e.Status
		s := fmt.Sprintf("%s status %s %s", head, shortState(w.State), shortActivity(w.Activity))
		if w.Position != "" {
			s += " " + w.Position
		}
		if w.Reason != "" {
			s += " reason=" + firstLine(w.Reason)
		}
		if w.Outcome != "" {
			s += " outcome=" + w.Outcome
		}
		return s
	case *apiv1.WorkspaceEvent_Engine:
		x := e.Engine
		s := fmt.Sprintf("%s engine %s occ=%s %s/%s", head, x.Kind, orDash(x.Occurrence), x.Workflow, x.Node)
		if x.Outcome != "" {
			s += " outcome=" + x.Outcome
		}
		if x.Detail != "" {
			s += " " + firstLine(x.Detail)
		}
		return s
	case *apiv1.WorkspaceEvent_GuestHook:
		return fmt.Sprintf("%s hook %s %s", head, e.GuestHook.Hook, e.GuestHook.Detail)
	case *apiv1.WorkspaceEvent_Http:
		h := e.Http
		if h.Denied {
			return fmt.Sprintf("%s http denied %s", head, h.Host)
		}
		if h.Status == 0 {
			return fmt.Sprintf("%s http %s %s%s ...", head, h.Method, h.Host, h.Path)
		}
		return fmt.Sprintf("%s http %s %s%s %d %dms", head, h.Method, h.Host, h.Path, h.Status, h.DurationMs)
	}
	return head
}

// ---------------------------------------------------------------------------
// gates
// ---------------------------------------------------------------------------

const gateUsage = "gate list [<id>] | gate show <id> <occurrence> | gate approve <id> <occurrence> [--hash <h>] [--file <path>]... [--comment <text>] | gate reject <id> <occurrence> [--comment <text>]"

func runGate(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: masuda %s\n", gateUsage)
		return errUsage
	}
	switch args[0] {
	case "list":
		return gateList(args[1:])
	case "show":
		return gateShow(args[1:])
	case "approve":
		return gateDecide(args[1:], true)
	case "reject":
		return gateDecide(args[1:], false)
	}
	fmt.Fprintf(os.Stderr, "usage: masuda %s\n", gateUsage)
	return errUsage
}

func gateList(args []string) error {
	c := newCommand("gate list", "gate list [<id>]")
	pos, err := c.parse(args, 0, 1)
	if err != nil {
		return err
	}
	id := ""
	if len(pos) == 1 {
		id = pos[0]
	}
	res, err := c.clients().gates.ListOpen(context.Background(), connect.NewRequest(&apiv1.ListOpenGatesRequest{WorkspaceId: id}))
	if err != nil {
		return err
	}
	tw := newTable(os.Stdout)
	fmt.Fprintln(tw, "WORKSPACE\tOCCURRENCE\tGATE\tTARGET\tOPENED")
	for _, g := range res.Msg.Gates {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", g.WorkspaceId, g.Occurrence, g.Gate, orDash(g.Target), fmtTime(g.OpenedAt))
	}
	return tw.Flush()
}

func gateShow(args []string) error {
	c := newCommand("gate show", "gate show <id> <occurrence>")
	pos, err := c.parse(args, 2, 2)
	if err != nil {
		return err
	}
	res, err := c.clients().gates.Get(context.Background(), connect.NewRequest(&apiv1.GetGateRequest{WorkspaceId: pos[0], Occurrence: pos[1]}))
	if err != nil {
		return err
	}
	g := res.Msg
	fmt.Printf("gate:        %s\noccurrence:  %s\ntarget:      %s\ntarget_hash: %s\nopened:      %s\n",
		g.Gate, g.Occurrence, orDash(g.Target), g.TargetHash, fmtTime(g.OpenedAt))
	if g.StagingCommit != "" {
		fmt.Printf("commit:      %s\n", g.StagingCommit)
	}
	if d := g.Decision; d != nil {
		fmt.Printf("decision:    %s %s\n", d.Outcome, d.Comment)
	}
	if len(g.Subject) > 0 {
		fmt.Printf("\n%s", g.Subject)
		if !strings.HasSuffix(string(g.Subject), "\n") {
			fmt.Println()
		}
	}
	return nil
}

// gateDecide は承認・却下を送る。承認の--hashを省くと、今開いているゲートのtarget_hashを使う
// （`gate show`で見た内容がその後に変わっていれば、それを承認してしまう）。確実にするなら
// `gate show`が出したハッシュを--hashで渡す。
func gateDecide(args []string, approve bool) error {
	name, usage := "gate reject", "gate reject <id> <occurrence> [--comment <text>]"
	if approve {
		name, usage = "gate approve", "gate approve <id> <occurrence> [--hash <h>] [--file <path>]... [--comment <text>]"
	}
	c := newCommand(name, usage)
	comment := c.fs.String("comment", "", "判断に添えるコメント")
	var hash *string
	var files multiFlag
	if approve {
		hash = c.fs.String("hash", "", "承認する内容のtarget_hash（省略時は今のゲートのもの）")
		c.fs.Var(&files, "file", "deviationゲートで計画に加えるファイル（繰り返し可。並べないファイルは加えない）")
	}
	pos, err := c.parse(args, 2, 2)
	if err != nil {
		return err
	}
	cl := c.clients()
	ctx := context.Background()
	d := &apiv1.Decision{Outcome: "rejected", Comment: *comment}
	if approve {
		d.Outcome = "approved"
		d.ApprovedFiles = files
		d.TargetHash = *hash
		if d.TargetHash == "" {
			g, err := cl.gates.Get(ctx, connect.NewRequest(&apiv1.GetGateRequest{WorkspaceId: pos[0], Occurrence: pos[1]}))
			if err != nil {
				return err
			}
			d.TargetHash = g.Msg.TargetHash
		}
	}
	res, err := cl.gates.Decide(ctx, connect.NewRequest(&apiv1.DecideRequest{WorkspaceId: pos[0], Occurrence: pos[1], Decision: d}))
	if err != nil {
		return err
	}
	fmt.Printf("%s %s %s\n", res.Msg.Gate, res.Msg.Occurrence, d.Outcome)
	return nil
}

// ---------------------------------------------------------------------------
// questions
// ---------------------------------------------------------------------------

const questionUsage = "question list [<id>] | question answer <id> <occurrence> <question-id>=<answer>..."

func runQuestion(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: masuda %s\n", questionUsage)
		return errUsage
	}
	switch args[0] {
	case "list":
		return questionList(args[1:])
	case "answer":
		return questionAnswer(args[1:])
	}
	fmt.Fprintf(os.Stderr, "usage: masuda %s\n", questionUsage)
	return errUsage
}

func questionList(args []string) error {
	c := newCommand("question list", "question list [<id>]")
	pos, err := c.parse(args, 0, 1)
	if err != nil {
		return err
	}
	id := ""
	if len(pos) == 1 {
		id = pos[0]
	}
	res, err := c.clients().questions.ListOpen(context.Background(), connect.NewRequest(&apiv1.ListOpenQuestionsRequest{WorkspaceId: id}))
	if err != nil {
		return err
	}
	for _, q := range res.Msg.Questions {
		fmt.Printf("%s %s (opened %s)\n", q.WorkspaceId, q.Occurrence, fmtTime(q.OpenedAt))
		for _, it := range q.Items {
			fmt.Printf("  %s: %s\n", it.Id, it.Text)
			if len(it.Options) > 0 {
				fmt.Printf("    options: %s\n", strings.Join(it.Options, " | "))
			}
		}
	}
	return nil
}

func questionAnswer(args []string) error {
	c := newCommand("question answer", "question answer <id> <occurrence> <question-id>=<answer>...")
	pos, err := c.parse(args, 3, -1)
	if err != nil {
		return err
	}
	answers := map[string]string{}
	for _, kv := range pos[2:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return fmt.Errorf("answer %q: want <question-id>=<answer>", kv)
		}
		answers[k] = v
	}
	if _, err := c.clients().questions.Answer(context.Background(), connect.NewRequest(&apiv1.AnswerRequest{
		WorkspaceId: pos[0], Occurrence: pos[1], Answers: answers,
	})); err != nil {
		return err
	}
	fmt.Printf("%s %s answered\n", pos[0], pos[1])
	return nil
}
