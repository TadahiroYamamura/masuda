package guest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
)

const writeChunk = 64 << 10

// WriteFile はdataをゲストのpathへ書く。mode 0は0644。
func WriteFile(ctx context.Context, c sandboxv1connect.SandboxServiceClient, id, path string, data io.Reader, mode uint32) error {
	st := c.WriteFile(ctx)
	if err := st.Send(&sandboxv1.WriteFileRequest{Msg: &sandboxv1.WriteFileRequest_Header_{Header: &sandboxv1.WriteFileRequest_Header{
		Id: id, Path: path, Mode: mode,
	}}}); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("write %s: %w", path, err)
	}
	buf := make([]byte, writeChunk)
	for {
		n, rerr := data.Read(buf)
		if n > 0 {
			// io.EOFはサーバーが先にストリームを閉じたことを表す。本当の理由は
			// CloseAndReceiveが返すので、ここでは送るのをやめるだけにする。
			if err := st.Send(&sandboxv1.WriteFileRequest{Msg: &sandboxv1.WriteFileRequest_Data{Data: append([]byte(nil), buf[:n]...)}}); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		} else if rerr != nil {
			_, _ = st.CloseAndReceive()
			return fmt.Errorf("write %s: %w", path, rerr)
		}
	}
	if _, err := st.CloseAndReceive(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// WriteBytes はWriteFileのバイト列版。
func WriteBytes(ctx context.Context, c sandboxv1connect.SandboxServiceClient, id, path string, data []byte, mode uint32) error {
	return WriteFile(ctx, c, id, path, bytes.NewReader(data), mode)
}

// ExecResult は1回のExecの結果。
type ExecResult struct {
	ExitCode int32
	Signal   string
	TimedOut bool
	Stdout   []byte
	Stderr   []byte
}

// Exec はゲストでコマンドを動かし、出力をすべて集めて返す。終了コードが0以外でも
// エラーにはしない（呼び出し側がExitCodeを見る）。
func Exec(ctx context.Context, c sandboxv1connect.SandboxServiceClient, req *sandboxv1.ExecRequest) (ExecResult, error) {
	st, err := c.Exec(ctx, connect.NewRequest(req))
	if err != nil {
		return ExecResult{}, err
	}
	defer st.Close()
	var res ExecResult
	exited := false
	for st.Receive() {
		switch ev := st.Msg().Event.(type) {
		case *sandboxv1.ExecEvent_Stdout:
			res.Stdout = append(res.Stdout, ev.Stdout...)
		case *sandboxv1.ExecEvent_Stderr:
			res.Stderr = append(res.Stderr, ev.Stderr...)
		case *sandboxv1.ExecEvent_Exited_:
			res.ExitCode = ev.Exited.ExitCode
			res.Signal = ev.Exited.Signal
			res.TimedOut = ev.Exited.TimedOut
			exited = true
		}
	}
	if err := st.Err(); err != nil {
		return res, err
	}
	if !exited {
		return res, errors.New("exec stream ended without an Exited event")
	}
	return res, nil
}

// Shell はshellコマンドを動かし、0以外で終わったらstderrを添えてエラーにする。
func Shell(ctx context.Context, c sandboxv1connect.SandboxServiceClient, id, cwd, script string) (ExecResult, error) {
	res, err := Exec(ctx, c, &sandboxv1.ExecRequest{Id: id, Shell: script, Cwd: cwd})
	if err != nil {
		return res, err
	}
	if res.ExitCode != 0 || res.Signal != "" || res.TimedOut {
		return res, fmt.Errorf("guest command %q failed (exit %d %s): %s", script, res.ExitCode, res.Signal, bytes.TrimSpace(res.Stderr))
	}
	return res, nil
}
