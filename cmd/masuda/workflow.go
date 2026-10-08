package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/serve"
)

const workflowUsage = "workflow list [--repo <dir>] | workflow show <workflow> [--repo <dir>] | workflow check [<workflow>] [--repo <dir>]"

// workflowのサブコマンドは、他のクライアントのコマンドと違いmasuda serveを叩かず、同じ処理を
// プロセスの中で呼ぶ（serveの状態を使わないので結果は同じ。定義の検査だけのためにserveを起動しなくて済む）。
// --socketは他のコマンドと揃えて受け付けるが、使わない（initと同じ）。
func runWorkflow(args []string) error {
	return subcommand(args, workflowUsage, map[string]func([]string) error{
		"list":  workflowList,
		"show":  workflowShow,
		"check": workflowCheck,
	})
}

// workflowRepoFlag は--repoを足す。省略時は今いる作業ツリーのトップ、作業ツリーの外なら同梱の定義だけを見る。
func workflowRepoFlag(c *command) func() (string, error) {
	repo := c.fs.String("repo", "", "対象リポジトリ（省略時は今いる作業ツリー。その外なら同梱の定義だけ）")
	return func() (string, error) {
		if *repo != "" {
			return filepath.Abs(*repo)
		}
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return "", nil
		}
		return strings.TrimSpace(string(out)), nil
	}
}

func workflowList(args []string) error {
	c := newCommand("workflow list", "workflow list [--repo <dir>] [--all]")
	repo := workflowRepoFlag(c)
	all := c.fs.Bool("all", false, "also show workflows that are not started by users (user_invocable: false)")
	if _, err := c.parse(args, 0, 0); err != nil {
		return err
	}
	root, err := repo()
	if err != nil {
		return err
	}
	res, err := serve.NewWorkflowService().List(context.Background(), connect.NewRequest(&apiv1.RepoRequest{RepoRoot: root}))
	if err != nil {
		return err
	}
	return printWorkflows(os.Stdout, res.Msg.Workflows, *all)
}

// printWorkflows は一覧の表を書く。allでなければ利用者が始めるもの（user_invocable）だけを出す。
func printWorkflows(w io.Writer, entries []*apiv1.WorkflowEntry, all bool) error {
	tw := newTable(w)
	if !all {
		fmt.Fprintln(tw, "WORKFLOW\tORIGIN\tINPUTS")
		for _, e := range entries {
			if e.UserInvocable {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Path, e.Origin, orDash(strings.Join(e.Inputs, ",")))
			}
		}
		return tw.Flush()
	}
	fmt.Fprintln(tw, "WORKFLOW\tORIGIN\tINPUTS\tUSER_INVOCABLE")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Path, e.Origin, orDash(strings.Join(e.Inputs, ",")), yesNo(e.UserInvocable))
	}
	return tw.Flush()
}

func workflowShow(args []string) error {
	c := newCommand("workflow show", "workflow show <workflow> [--repo <dir>]")
	repo := workflowRepoFlag(c)
	pos, err := c.parse(args, 1, 1)
	if err != nil {
		return err
	}
	root, err := repo()
	if err != nil {
		return err
	}
	res, err := serve.NewWorkflowService().Show(context.Background(), connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: root, Workflow: pos[0]}))
	if err != nil {
		return err
	}
	fmt.Print(res.Msg.Mermaid)
	if !strings.HasSuffix(res.Msg.Mermaid, "\n") {
		fmt.Println()
	}
	return nil
}

// workflowCheck は問題を1行ずつ出し、1つでもあれば終了コード1にする（CIや手元の確認で使えるように）。
func workflowCheck(args []string) error {
	c := newCommand("workflow check", "workflow check [<workflow>] [--repo <dir>]")
	repo := workflowRepoFlag(c)
	pos, err := c.parse(args, 0, 1)
	if err != nil {
		return err
	}
	root, err := repo()
	if err != nil {
		return err
	}
	wf := ""
	if len(pos) == 1 {
		wf = pos[0]
	}
	res, err := serve.NewWorkflowService().Check(context.Background(), connect.NewRequest(&apiv1.ShowWorkflowRequest{RepoRoot: root, Workflow: wf}))
	if err != nil {
		return err
	}
	for _, p := range res.Msg.Problems {
		fmt.Println(formatProblem(p))
	}
	if n := len(res.Msg.Problems); n > 0 {
		return fmt.Errorf("%d problem(s)", n)
	}
	fmt.Println("ok")
	return nil
}

func formatProblem(p *apiv1.Problem) string {
	loc := p.Path
	if p.Node != "" {
		loc += " (node " + p.Node + ")"
	}
	if loc == "" {
		return p.Message
	}
	return loc + ": " + p.Message
}
