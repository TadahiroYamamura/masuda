package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	staging   apiv1connect.StagingServiceClient
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
		staging:   apiv1connect.NewStagingServiceClient(httpc, base, connect.WithGRPC()),
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
// 位置は止まった理由・結果（done以外で終わったならその理由も）があればそちらを、OPENは開いているゲートと質問を出す。
func listRow(w *apiv1.Workspace, now time.Time) string {
	pos := w.Position
	switch {
	case w.Outcome != "" && w.Reason != "":
		pos = "outcome " + w.Outcome + ": " + firstLine(w.Reason)
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
	case *apiv1.WorkspaceEvent_Notice:
		// serve全体のこと（ディスク使用量の警告等）。workspace_idは空。
		return fmt.Sprintf("%s notice %s %s", head, e.Notice.Kind, e.Notice.Detail)
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

const gateUsage = "gate list [<id>] | gate show <id> <occurrence> | gate approve <id> <occurrence> [--hash <h>] [--file <path>]... [--comment <text>] | gate reject <id> <occurrence> [--comment <text>] | gate comment <id> <occurrence> <path>:<line> <text> | gate dismiss|halt|redo <id> <occurrence> [--comment <text>]"

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
	case "comment":
		return gateComment(args[1:])
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
	cl := c.clients()
	ctx := context.Background()
	res, err := cl.gates.Get(ctx, connect.NewRequest(&apiv1.GetGateRequest{WorkspaceId: pos[0], Occurrence: pos[1]}))
	if err != nil {
		return err
	}
	var comments []*apiv1.Comment
	if res.Msg.StagingCommit != "" {
		cs, err := cl.staging.ListComments(ctx, connect.NewRequest(&apiv1.ListCommentsRequest{WorkspaceId: pos[0], Commit: res.Msg.StagingCommit}))
		if err != nil {
			return err
		}
		comments = cs.Msg.Comments
	}
	fmt.Print(formatGate(res.Msg, comments))
	return nil
}

// gateComment は差分のゲートの承認対象のコミット（staging_commit）の行に人間のコメントを付ける。
// 付けたコメントは、そのゲートを却下したときに差し戻し先のエージェントへ本文とともに届く。
func gateComment(args []string) error {
	const usage = "gate comment <id> <occurrence> <path>:<line> <text>"
	c := newCommand("gate comment", usage)
	pos, err := c.parse(args, 4, -1)
	if err != nil {
		return err
	}
	path, line, err := parseLocation(pos[2])
	if err != nil {
		return err
	}
	// 引用符で囲まずに書いた本文も1つのコメントとして受ける
	body := strings.Join(pos[3:], " ")
	cl := c.clients()
	ctx := context.Background()
	g, err := cl.gates.Get(ctx, connect.NewRequest(&apiv1.GetGateRequest{WorkspaceId: pos[0], Occurrence: pos[1]}))
	if err != nil {
		return err
	}
	if g.Msg.StagingCommit == "" {
		return fmt.Errorf("このゲートは差分を対象にしていない（gate %s, target %s）", g.Msg.Gate, orDash(g.Msg.Target))
	}
	res, err := cl.staging.AddComment(ctx, connect.NewRequest(&apiv1.AddCommentRequest{
		WorkspaceId: pos[0], Commit: g.Msg.StagingCommit, Path: path, Line: line, Body: body,
	}))
	if err != nil {
		return err
	}
	fmt.Printf("comment %s on %s:%d (commit %s)\n", res.Msg.Id, path, line, res.Msg.Commit)
	return nil
}

// parseLocation は`<path>:<line>`を読む。パスに`:`が入っていてもよいよう、最後の`:`で分ける。
func parseLocation(s string) (string, uint32, error) {
	i := strings.LastIndex(s, ":")
	if i <= 0 {
		return "", 0, fmt.Errorf("場所は<path>:<line>で書く: %q", s)
	}
	n, err := strconv.ParseUint(s[i+1:], 10, 32)
	if err != nil || n == 0 {
		return "", 0, fmt.Errorf("行番号は1以上の整数で書く: %q", s)
	}
	return s[:i], uint32(n), nil
}

// triageOutcomes はtriageゲートの判断と、その意味（engineの扱い）。
var triageOutcomes = []struct{ outcome, meaning string }{
	{"dismiss", "懸念を退けて続ける"},
	{"halt", "実行を止める"},
	{"redo", "懸念の出た出現を差し戻して入り直す"},
}

// diffTargets はゲートの承認対象のうち差分であるもの。どちらも中身はunified diffだが、承認して
// 確定するものが違う（diffはpublishされるコミット済みの内容、step-diffはこれからcommitされる内容）
// ので、取り違えないよう見出しで区別する。
var diffTargets = map[string]struct{ meaning, heading string }{
	"diff":      {"publishされる内容: 分岐元..ブランチ先頭のコミット済みの差分", "changes to be published (committed, base..branch head):"},
	"step-diff": {"これからcommitされる内容: ブランチ先頭..作業ツリーの未コミットの差分", "changes this step will commit (uncommitted, branch head..work tree):"},
}

// formatGate はゲートを人間が読む形にする。中身（subject）はゲートの種類で読み方が違うので、
// triageは懸念の本文、deviationは計画の外で変わったファイルの一覧、target: planは節に分けた計画として
// 見出しを付けて出し、最後にそのゲートで打てる判断のコマンドを添える。commentsはstaging_commitに
// 付いたコメントで、そのうち人間のもの（却下でエージェントへ届くもの）を差分の後に出す。
func formatGate(g *apiv1.Gate, comments []*apiv1.Comment) string {
	var b strings.Builder
	target := orDash(g.Target)
	dt, isDiff := diffTargets[g.Target]
	if isDiff {
		target += "（" + dt.meaning + "）"
	}
	fmt.Fprintf(&b, "gate:        %s\noccurrence:  %s\ntarget:      %s\ntarget_hash: %s\nopened:      %s\n",
		g.Gate, g.Occurrence, target, g.TargetHash, fmtTime(g.OpenedAt))
	if g.StagingCommit != "" {
		note := ""
		if g.Target == "step-diff" {
			note = "（作業ツリーのスナップショット。親がブランチ先頭）"
		}
		fmt.Fprintf(&b, "commit:      %s%s\n", g.StagingCommit, note)
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
		if isDiff {
			b.WriteString("\n" + dt.heading + "\n")
			if subject == "" {
				b.WriteString("  (no changes)\n")
			}
		}
		if plan, ok := formatPlan(g.Target, subject); ok {
			b.WriteString("\n" + plan)
		} else if subject != "" {
			b.WriteString("\n" + subject + "\n")
		}
		if g.StagingCommit != "" {
			b.WriteString(formatHumanComments(comments))
		}
		if g.Decision == nil {
			fmt.Fprintf(&b, "\napprove: masuda gate approve %s --hash %s [--comment <text>]\nreject:  masuda gate reject %s [--comment <text>]\n", ref, g.TargetHash, ref)
			if g.StagingCommit != "" {
				fmt.Fprintf(&b, "comment: masuda gate comment %s <path>:<line> <text>\n", ref)
			}
		}
	}
	return b.String()
}

// formatHumanComments は人間のコメントを`<path>:<line>: <body>`で並べる。無ければ見出しごと省く。
func formatHumanComments(comments []*apiv1.Comment) string {
	var b strings.Builder
	for _, c := range comments {
		if c.Author != "human" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("\ncomments (sent to the agent on reject):\n")
		}
		loc := c.Path
		switch {
		case loc == "":
			loc = "(commit)"
		case c.Line > 0:
			loc = fmt.Sprintf("%s:%d", c.Path, c.Line)
		}
		fmt.Fprintf(&b, "  %s: %s\n", loc, strings.ReplaceAll(strings.TrimSpace(c.Body), "\n", "\n    "))
	}
	return b.String()
}

// gatePlan はengine同梱の計画スキーマ（plan）のうち表示に使う項目。既存ワークスペースの記録には
// goal・title・tests・alternatives・risksを持たない旧スキーマの計画も残っているので、どの項目も
// 欠けてよい前提で読み、欠けた項目は行ごと省く。
type gatePlan struct {
	Goal    string `json:"goal"`
	Summary string `json:"summary"`
	Steps   []struct {
		Number      int      `json:"number"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Tests       []string `json:"tests"`
		Files       []string `json:"files"`
	} `json:"steps"`
	Alternatives []struct {
		Option string `json:"option"`
		Reason string `json:"reason"`
	} `json:"alternatives"`
	Risks              []string `json:"risks"`
	ExpectedByproducts []string `json:"expected_byproducts"`
	Checks             []struct {
		ID       string `json:"id"`
		Question string `json:"question"`
		Answer   string `json:"answer"`
		Status   string `json:"status"`
	} `json:"checks"`
}

// formatPlan はtarget: planのゲートの中身（計画のJSON）を、goal・summary・steps・checks・
// alternatives・risks・expected byproductsの節に分けて字下げして出す。JSONとして解けない、またはstepsが無い
// ときはok=falseを返し、呼び出し側は中身をそのまま出す（スキーマ外の計画でも内容を隠さないため）。
func formatPlan(target, subject string) (string, bool) {
	if target != "plan" {
		return "", false
	}
	var p gatePlan
	if err := json.Unmarshal([]byte(subject), &p); err != nil || len(p.Steps) == 0 {
		return "", false
	}
	var b strings.Builder
	indent := func(text, pad string) {
		for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			b.WriteString(pad + line + "\n")
		}
	}
	if p.Goal != "" {
		b.WriteString("goal: " + p.Goal + "\n\n")
	}
	if p.Summary != "" {
		b.WriteString("summary:\n")
		indent(p.Summary, "  ")
		b.WriteString("\n")
	}
	b.WriteString("steps:\n")
	for _, st := range p.Steps {
		head := fmt.Sprintf("  %d.", st.Number)
		if st.Title != "" {
			head += " " + st.Title
		}
		b.WriteString(head + "\n")
		if st.Description != "" {
			indent(st.Description, "     ")
		}
		if len(st.Tests) > 0 {
			b.WriteString("     tests:\n")
			for _, t := range st.Tests {
				b.WriteString("       - " + t + "\n")
			}
		}
		if len(st.Files) > 0 {
			b.WriteString("     files: " + strings.Join(st.Files, ", ") + "\n")
		}
	}
	if len(p.Checks) > 0 {
		b.WriteString("\nchecks (questions raised about the plan, with the planner's answers):\n")
		for _, c := range p.Checks {
			b.WriteString("  " + c.ID + " [" + c.Status + "] " + c.Question + "\n")
			if strings.TrimSpace(c.Answer) != "" {
				indent(c.Answer, "      ")
			}
		}
	}
	if len(p.Alternatives) > 0 {
		b.WriteString("\nalternatives (considered, not taken):\n")
		for _, a := range p.Alternatives {
			b.WriteString("  - " + a.Option + ": " + a.Reason + "\n")
		}
	}
	if len(p.Risks) > 0 {
		b.WriteString("\nrisks:\n")
		for _, r := range p.Risks {
			b.WriteString("  - " + r + "\n")
		}
	}
	if len(p.ExpectedByproducts) > 0 {
		b.WriteString("\nexpected byproducts: " + strings.Join(p.ExpectedByproducts, ", ") + "\n")
	}
	return b.String(), true
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
		fmt.Print(formatQuestion(q))
	}
	return nil
}

// formatQuestion は開いている質問1つを出す。1回のask_humanに複数の問いが入り（developの
// plan-interviewerは計画の問いを`SPEC-1`等のidで一度に聞く）、本文は問いと理由の複数行に
// なるので、2行目以降も字下げし、すべての問いに答えるコマンドの形を最後に添える
// （answerは聞かれた問いすべての答えを求める）。
func formatQuestion(q *apiv1.OpenQuestion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s (opened %s)\n", q.WorkspaceId, q.Occurrence, fmtTime(q.OpenedAt))
	hint := "answer: masuda question answer " + q.WorkspaceId + " " + q.Occurrence
	for _, it := range q.Items {
		lines := strings.Split(strings.TrimRight(it.Text, "\n"), "\n")
		fmt.Fprintf(&b, "  %s: %s\n", it.Id, lines[0])
		for _, l := range lines[1:] {
			b.WriteString("    " + l + "\n")
		}
		if len(it.Options) > 0 {
			fmt.Fprintf(&b, "    options: %s\n", strings.Join(it.Options, " | "))
		}
		// `<answer>`をシェルのリダイレクトと取られないよう、引用した形で示す。
		hint += " '" + strings.ReplaceAll(it.Id, "'", `'\''`) + "=<answer>'"
	}
	b.WriteString(hint + "\n")
	return b.String()
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
