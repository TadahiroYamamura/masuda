package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"golang.org/x/term"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/internal/config"
)

// repoFlag は設定系のサブコマンドが共通で受ける--repo。
func repoFlag(c *command) *string {
	return c.fs.String("repo", ".", "対象リポジトリ（作業ツリーのトップ）")
}

func absRepo(repo string) (string, error) { return filepath.Abs(repo) }

// subcommand は`masuda <group> <sub> ...`の振り分け。
func subcommand(args []string, usage string, subs map[string]func([]string) error) error {
	if len(args) > 0 {
		if f := subs[args[0]]; f != nil {
			return f(args[1:])
		}
	}
	fmt.Fprintf(os.Stderr, "usage: masuda %s\n", usage)
	return errUsage
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// ---------------------------------------------------------------------------
// egress
// ---------------------------------------------------------------------------

const egressUsage = "egress list|approve <host>|reject <host> [--repo <dir>]"

func runEgress(args []string) error {
	return subcommand(args, egressUsage, map[string]func([]string) error{
		"list":    egressList,
		"approve": func(a []string) error { return egressDecide(a, true) },
		"reject":  func(a []string) error { return egressDecide(a, false) },
	})
}

func printEgress(res *apiv1.ListEgressResponse) {
	t := newTable(os.Stdout)
	fmt.Fprintln(t, "HOST\tAPPROVED")
	for _, e := range res.Entries {
		fmt.Fprintf(t, "%s\t%s\n", e.Host, yesNo(e.Approved))
	}
	t.Flush()
}

func egressList(args []string) error {
	c := newCommand("egress list", "egress list [--repo <dir>]")
	repo := repoFlag(c)
	if _, err := c.parse(args, 0, 0); err != nil {
		return err
	}
	root, err := absRepo(*repo)
	if err != nil {
		return err
	}
	res, err := c.clients().config.ListEgress(context.Background(), connect.NewRequest(&apiv1.RepoRequest{RepoRoot: root}))
	if err != nil {
		return err
	}
	printEgress(res.Msg)
	return nil
}

func egressDecide(args []string, approve bool) error {
	name := "egress reject"
	if approve {
		name = "egress approve"
	}
	c := newCommand(name, name+" <host> [--repo <dir>]")
	repo := repoFlag(c)
	pos, err := c.parse(args, 1, 1)
	if err != nil {
		return err
	}
	root, err := absRepo(*repo)
	if err != nil {
		return err
	}
	req := connect.NewRequest(&apiv1.HostRequest{RepoRoot: root, Host: pos[0]})
	var res *connect.Response[apiv1.ListEgressResponse]
	if approve {
		res, err = c.clients().config.ApproveEgress(context.Background(), req)
	} else {
		res, err = c.clients().config.RejectEgress(context.Background(), req)
	}
	if err != nil {
		return err
	}
	printEgress(res.Msg)
	return nil
}

// ---------------------------------------------------------------------------
// secret
// ---------------------------------------------------------------------------

const secretUsage = "secret list|set|approve|reject <NAME> [--repo <dir>]"

func runSecret(args []string) error {
	return subcommand(args, secretUsage, map[string]func([]string) error{
		"list":    secretList,
		"set":     secretSet,
		"approve": func(a []string) error { return secretDecide(a, true) },
		"reject":  func(a []string) error { return secretDecide(a, false) },
	})
}

// printSecrets は秘密の一覧。plaintextは本物の値がゲストに入るので、承認の有無と並べて目立たせる。
func printSecrets(res *apiv1.ListSecretsResponse) {
	t := newTable(os.Stdout)
	fmt.Fprintln(t, "NAME\tMODE\tHOSTS\tVALUE\tAPPROVED")
	for _, e := range res.Entries {
		mode, approved := e.Mode, "-"
		if e.ApprovalRequired {
			mode = "PLAINTEXT"
			approved = yesNo(e.Approved)
		}
		value := "unset"
		if e.ValueSet {
			value = "set"
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\n", e.Name, mode, orDash(strings.Join(e.Hosts, ",")), value, approved)
	}
	t.Flush()
	token := "unset"
	if res.ClaudeTokenSet {
		token = "set"
	}
	fmt.Printf("Claude token: %s\n", token)
}

func secretDecide(args []string, approve bool) error {
	name := "secret reject"
	if approve {
		name = "secret approve"
	}
	c := newCommand(name, name+" <NAME> [--repo <dir>]")
	repo := repoFlag(c)
	pos, err := c.parse(args, 1, 1)
	if err != nil {
		return err
	}
	root, err := absRepo(*repo)
	if err != nil {
		return err
	}
	req := connect.NewRequest(&apiv1.NameRequest{RepoRoot: root, Name: pos[0]})
	var res *connect.Response[apiv1.ListSecretsResponse]
	if approve {
		res, err = c.clients().config.ApproveSecret(context.Background(), req)
	} else {
		res, err = c.clients().config.RejectSecret(context.Background(), req)
	}
	if err != nil {
		return err
	}
	printSecrets(res.Msg)
	return nil
}

func secretList(args []string) error {
	c := newCommand("secret list", "secret list [--repo <dir>]")
	repo := repoFlag(c)
	if _, err := c.parse(args, 0, 0); err != nil {
		return err
	}
	root, err := absRepo(*repo)
	if err != nil {
		return err
	}
	res, err := c.clients().config.ListSecrets(context.Background(), connect.NewRequest(&apiv1.RepoRequest{RepoRoot: root}))
	if err != nil {
		return err
	}
	printSecrets(res.Msg)
	return nil
}

// secretSet は値を標準入力から読む。引数やフラグで受けないのは、シェルの履歴やpsに残さないため。
// 端末ならエコーせずに1行読み、パイプなら全部読んで末尾の改行1つだけを落とす。
// secretSet は値を置く。Claudeのトークン（CLAUDE_CODE_OAUTH_TOKEN）は、--repoを付けなければ
// ユーザー単位（どのリポジトリでも使う）に置く。--repoを付ければそのリポジトリだけの上書き。
func secretSet(args []string) error {
	c := newCommand("secret set", "secret set <NAME> [--repo <dir>]  (値は標準入力から。CLAUDE_CODE_OAUTH_TOKENは--repo無しならユーザー単位)")
	repo := repoFlag(c)
	pos, err := c.parse(args, 1, 1)
	if err != nil {
		return err
	}
	repoGiven := false
	c.fs.Visit(func(f *flag.Flag) { repoGiven = repoGiven || f.Name == "repo" })
	root := ""
	if repoGiven || pos[0] != config.ReservedSecret {
		if root, err = absRepo(*repo); err != nil {
			return err
		}
	}
	value, err := readSecretValue(pos[0])
	if err != nil {
		return err
	}
	if value == "" {
		return errors.New("empty value")
	}
	res, err := c.clients().config.SetSecret(context.Background(), connect.NewRequest(&apiv1.SetSecretRequest{RepoRoot: root, Name: pos[0], Value: value}))
	if err != nil {
		return err
	}
	if root == "" {
		fmt.Printf("%s: set for this user (used by every repository without its own value)\n", pos[0])
		return nil
	}
	printSecrets(res.Msg)
	return nil
}

func readSecretValue(name string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprintf(os.Stderr, "%sの値: ", name)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	s := strings.TrimSuffix(string(b), "\n")
	return strings.TrimSuffix(s, "\r"), nil
}

// ---------------------------------------------------------------------------
// privileged-command
// ---------------------------------------------------------------------------

const privilegedUsage = "privileged-command list|approve <name> [--repo <dir>]"

func runPrivilegedCommand(args []string) error {
	return subcommand(args, privilegedUsage, map[string]func([]string) error{
		"list":    privilegedList,
		"approve": privilegedApprove,
	})
}

func printPrivileged(res *apiv1.ListPrivilegedCommandsResponse) {
	t := newTable(os.Stdout)
	fmt.Fprintln(t, "NAME\tIMAGE\tAPPROVED\tCOMMAND")
	for _, e := range res.Entries {
		approved := yesNo(e.Approved)
		if e.Stale {
			approved = "stale"
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\n", e.Name, orDash(e.Image), approved, firstLine(e.Command))
	}
	t.Flush()
}

func privilegedList(args []string) error {
	c := newCommand("privileged-command list", "privileged-command list [--repo <dir>]")
	repo := repoFlag(c)
	if _, err := c.parse(args, 0, 0); err != nil {
		return err
	}
	root, err := absRepo(*repo)
	if err != nil {
		return err
	}
	res, err := c.clients().config.ListPrivilegedCommands(context.Background(), connect.NewRequest(&apiv1.RepoRequest{RepoRoot: root}))
	if err != nil {
		return err
	}
	printPrivileged(res.Msg)
	return nil
}

func privilegedApprove(args []string) error {
	c := newCommand("privileged-command approve", "privileged-command approve <name> [--repo <dir>]")
	repo := repoFlag(c)
	pos, err := c.parse(args, 1, 1)
	if err != nil {
		return err
	}
	root, err := absRepo(*repo)
	if err != nil {
		return err
	}
	res, err := c.clients().config.ApprovePrivilegedCommand(context.Background(), connect.NewRequest(&apiv1.NameRequest{RepoRoot: root, Name: pos[0]}))
	if err != nil {
		return err
	}
	printPrivileged(res.Msg)
	return nil
}

// ---------------------------------------------------------------------------
// image
// ---------------------------------------------------------------------------

const imageUsage = "image list|build [<entry>] [--repo <dir>]"

func runImage(args []string) error {
	return subcommand(args, imageUsage, map[string]func([]string) error{
		"list":  imageList,
		"build": imageBuild,
	})
}

func imageList(args []string) error {
	c := newCommand("image list", "image list [--repo <dir>]")
	repo := repoFlag(c)
	if _, err := c.parse(args, 0, 0); err != nil {
		return err
	}
	root, err := absRepo(*repo)
	if err != nil {
		return err
	}
	res, err := c.clients().config.ListImages(context.Background(), connect.NewRequest(&apiv1.RepoRequest{RepoRoot: root}))
	if err != nil {
		return err
	}
	t := newTable(os.Stdout)
	fmt.Fprintln(t, "ENTRY\tBUILT\tBUILD_ID")
	for _, e := range res.Msg.Entries {
		fmt.Fprintf(t, "%s\t%s\t%s\n", e.Entry, yesNo(e.Built), orDash(e.BuildId))
	}
	t.Flush()
	return nil
}

func imageBuild(args []string) error {
	c := newCommand("image build", "image build [<entry>] [--repo <dir>]  (entry省略時はsettings.jsonのimage)")
	repo := repoFlag(c)
	pos, err := c.parse(args, 0, 1)
	if err != nil {
		return err
	}
	root, err := absRepo(*repo)
	if err != nil {
		return err
	}
	entry := ""
	if len(pos) == 1 {
		entry = pos[0]
	}
	if note := imageClaudeCodeNote(root, entry); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	stream, err := c.clients().config.BuildImage(context.Background(), connect.NewRequest(&apiv1.BuildImageRequest{RepoRoot: root, Entry: entry}))
	if err != nil {
		return err
	}
	defer stream.Close()
	var id string
	for stream.Receive() {
		ev := stream.Msg()
		if ev.LogLine != "" {
			fmt.Fprintln(os.Stderr, ev.LogLine)
		}
		if ev.BuildId != "" {
			id = ev.BuildId
		}
	}
	if err := stream.Err(); err != nil {
		return err
	}
	if id == "" {
		return errors.New("the build finished without a build id")
	}
	fmt.Println(id)
	return nil
}
