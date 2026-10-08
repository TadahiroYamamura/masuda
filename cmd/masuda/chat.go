package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
)

// runChat はワークスペースのゲストのtmuxセッション（メインセッションのclaude）へsshでアタッチする。
// serveが返したssh_argvをそのままexecする（このプロセスをsshに置き換える）。端末の扱いを
// sshに任せ、デタッチ（C-b d）やsshの終了でそのままシェルへ戻れるようにするため。
func runChat(args []string) error {
	c := newCommand("chat", "chat <id>")
	pos, err := c.parse(args, 1, 1)
	if err != nil {
		return err
	}
	res, err := c.clients().ws.AttachInfo(context.Background(), connect.NewRequest(&apiv1.AttachInfoRequest{Id: pos[0]}))
	if connect.CodeOf(err) == connect.CodeUnimplemented {
		return fmt.Errorf("this sandbox does not provide ssh (for example, masuda serve runs with --fake-sandbox): %w", err)
	} else if err != nil {
		return err
	}
	argv := res.Msg.SshArgv
	if len(argv) == 0 {
		return errors.New("masuda serve returned no ssh command")
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("%s not found; install it to attach: %w", argv[0], err)
	}
	return syscall.Exec(path, argv, os.Environ())
}
