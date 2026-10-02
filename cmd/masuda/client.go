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
	workflows apiv1connect.WorkflowServiceClient
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
		workflows: apiv1connect.NewWorkflowServiceClient(httpc, base, connect.WithGRPC()),
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
	c := newCommand("run", "run <workflow> --branch <name> [--repo <dir>] [--base <ref>] [--image <entry>] [--input name=value|name=@file]...")
	repo := c.fs.String("repo", ".", "対象リポジトリ（作業ツリーのトップ）")
	workflow := c.fs.String("workflow", "", "ワークフロー（例: workflows/develop）。位置引数でも渡せる")
	branch := c.fs.String("branch", "", "作るブランチ。publishしないワークフローなら既存のブランチも指定できる")
	base := c.fs.String("base", "", "分岐元（空なら今チェックアウトしているブランチ、既存のブランチを指定したときはリポジトリの既定のブランチ）")
	image := c.fs.String("image", "", "イメージのエントリ（空ならsettings.jsonの既定）")
	var inputs multiFlag
	c.fs.Var(&inputs, "input", "入力。name=value、またはname=@file でファイルの中身（繰り返し可）")
	pos, err := c.parse(args, 0, 1)
	if err != nil {
		return err
	}
	if len(pos) == 1 {
		if *workflow != "" && *workflow != pos[0] {
			return fmt.Errorf("workflow given twice: %q and --workflow %q", pos[0], *workflow)
		}
		*workflow = pos[0]
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
	c := newCommand("list", "list [--repo <dir>] [--all]")
	repo := c.fs.String("repo", "", "このリポジトリのワークスペースだけを出す（空なら全部）")
	all := c.fs.Bool("all", false, "終わった（done）・止めた（stopped）ワークスペースも出す")
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
	fmt.Fprintln(tw, "ID\tBRANCH\tSTATE\tACTIVITY\tPOSITION\tOPEN")
	now := time.Now()
	for _, w := range res.Msg.Workspaces {
		if !*all && (w.State == apiv1.WorkspaceState_WORKSPACE_STATE_DONE || w.State == apiv1.WorkspaceState_WORKSPACE_STATE_STOPPED) {
			continue
		}
		fmt.Fprintln(tw, listRow(w, now))
	}
	return tw.Flush()
}

// listRow はワークスペース1つを一覧の1行（タブ区切り）にする。活動は種類と最終活動からの経過、
// 位置は止まった理由・結果があればそちらを、OPENは開いているゲートと質問を出す。
func listRow(w *apiv1.Workspace, now time.Time) string {
	pos := w.Position
	switch {
	case w.Reason != "":
		pos = w.Reason
	case w.Outcome != "":
		pos = "outcome " + w.Outcome
	}
	act := shortActivity(w.Activity)
	if w.Activity != nil && w.Activity.LastActivity != nil && w.Activity.LastActivity.IsValid() {
		act += " " + since(now.Sub(w.Activity.LastActivity.AsTime())) + " ago"
	}
	var open []string
	for _, g := range w.OpenGates {
		open = append(open, "gate:"+g)
	}
	for _, q := range w.OpenQuestions {
		open = append(open, "question:"+q)
	}
	return strings.Join([]string{w.Id, w.Branch, shortState(w.State), act, orDash(firstLine(pos)), orDash(strings.Join(open, ","))}, "\t")
}

// since は経過時間を一覧で読みやすい粒度（秒・分・時間・日のうち1つ）にする。
func since(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
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
		if ev.WorkspaceId == "" {
			// serve全体のこと（ディスク使用量の警告等）。出現もノードも無い。
			return fmt.Sprintf("%s %s %s", head, x.Kind, x.Detail)
		}
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

const gateUsage = "gate list [<id>] | gate show <id> <occurrence> | gate approve <id> <occurrence> [--hash <h>] [--file <path>]... [--comment <text>] | gate reject <id> <occurrence> [--comment <text>] | gate dismiss|halt|redo <id> <occurrence> [--comment <text>]"

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
	case "dismiss", "halt", "redo":
		return gateTriage(args[0], args[1:])
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
	fmt.Print(formatGate(res.Msg))
	return nil
}

// triageOutcomes はtriageゲートの判断と、その意味（engineの扱い）。
var triageOutcomes = []struct{ outcome, meaning string }{
	{"dismiss", "懸念を退けて続ける"},
	{"halt", "実行を止める"},
	{"redo", "懸念の出た出現を差し戻して入り直す"},
}

// formatGate はゲートを人間が読む形にする。中身（subject）はゲートの種類で読み方が違うので、
// triageは懸念の本文、deviationは計画の外で変わったファイルの一覧として見出しを付けて出し、
// 最後にそのゲートで打てる判断のコマンドを添える。
func formatGate(g *apiv1.Gate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "gate:        %s\noccurrence:  %s\ntarget:      %s\ntarget_hash: %s\nopened:      %s\n",
		g.Gate, g.Occurrence, orDash(g.Target), g.TargetHash, fmtTime(g.OpenedAt))
	if g.StagingCommit != "" {
		fmt.Fprintf(&b, "commit:      %s\n", g.StagingCommit)
	}
	if d := g.Decision; d != nil {
		if d.Outcome == "superseded" {
			fmt.Fprintf(&b, "decision:    superseded（triageで無効。入り直した出現が新しいゲートを開く）\n")
		} else {
			fmt.Fprintf(&b, "decision:    %s %s\n", d.Outcome, d.Comment)
		}
		if len(d.ApprovedFiles) > 0 {
			fmt.Fprintf(&b, "approved:    %s\n", strings.Join(d.ApprovedFiles, ", "))
		}
	}
	subject := strings.TrimRight(string(g.Subject), "\n")
	ref := g.WorkspaceId + " " + g.Occurrence
	switch g.Gate {
	case "triage":
		b.WriteString("\nconcern (the agent reported this about its own task):\n")
		for _, line := range strings.Split(subject, "\n") {
			b.WriteString("  " + line + "\n")
		}
		if g.Decision == nil {
			b.WriteString("\ndecide with one of:\n")
			for _, o := range triageOutcomes {
				fmt.Fprintf(&b, "  masuda gate %-7s %s [--comment <text>]   # %s\n", o.outcome, ref, o.meaning)
			}
		}
	case "deviation":
		b.WriteString("\nfiles changed outside the plan:\n")
		for _, f := range strings.Split(subject, "\n") {
			if f != "" {
				b.WriteString("  - " + f + "\n")
			}
		}
		if g.Decision == nil {
			fmt.Fprintf(&b, "\nadd files to the plan:   masuda gate approve %s --hash %s --file <path>...\n", ref, g.TargetHash)
			b.WriteString("  (files not listed stay uncommitted in the guest's work tree)\n")
			fmt.Fprintf(&b, "send back to the agent:  masuda gate reject %s [--comment <text>]\n", ref)
		}
	default:
		if subject != "" {
			b.WriteString("\n" + subject + "\n")
		}
		if g.Decision == nil {
			fmt.Fprintf(&b, "\napprove: masuda gate approve %s --hash %s [--comment <text>]\nreject:  masuda gate reject %s [--comment <text>]\n", ref, g.TargetHash, ref)
		}
	}
	return b.String()
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

// gateTriage はtriageゲートへの判断（dismiss・halt・redo）を送る。triageの判断は内容の承認では
// ないのでtarget_hashは要らない。
func gateTriage(outcome string, args []string) error {
	c := newCommand("gate "+outcome, "gate "+outcome+" <id> <occurrence> [--comment <text>]")
	comment := c.fs.String("comment", "", "判断に添えるコメント")
	pos, err := c.parse(args, 2, 2)
	if err != nil {
		return err
	}
	res, err := c.clients().gates.Decide(context.Background(), connect.NewRequest(&apiv1.DecideRequest{
		WorkspaceId: pos[0], Occurrence: pos[1], Decision: &apiv1.Decision{Outcome: outcome, Comment: *comment},
	}))
	if err != nil {
		return err
	}
	fmt.Printf("%s %s %s\n", res.Msg.Gate, res.Msg.Occurrence, outcome)
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
