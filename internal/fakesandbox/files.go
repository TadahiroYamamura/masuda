package fakesandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
)

const (
	defaultMaxRead = 64 << 20
	chunkSize      = 64 << 10
)

// hostPath はゲストの絶対パスをroot下のホストパスへ写す。`..`はpath.Cleanで
// root内に畳まれるので、root外を指すことはない。
func hostPath(root, guest string) (string, error) {
	if !strings.HasPrefix(guest, "/") {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("guest path must be absolute: %q", guest))
	}
	return filepath.Join(root, filepath.FromSlash(path.Clean(guest))), nil
}

// symlinkInside はrootからfullまでの途中（full自身は含めない）にsymlinkがあれば
// そのパスを返す。実物はゲストのファイルシステム上で辿らずに扱うが、フェイクでは
// ゲストが置いたsymlinkがホストのroot外を指しうるので、途中も含めて辿らない。
func symlinkInside(root, full string) (string, error) {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	cur := root
	parts := strings.Split(rel, string(filepath.Separator))
	for _, p := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, p)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		} else if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return cur, nil
		}
	}
	return "", nil
}

func (s *Service) ReadFile(_ context.Context, req *connect.Request[sandboxv1.ReadFileRequest], stream *connect.ServerStream[sandboxv1.FileChunk]) error {
	root, _, _, err := s.sandboxFor(req.Msg.Id)
	if err != nil {
		return err
	}
	full, err := hostPath(root, req.Msg.Path)
	if err != nil {
		return err
	}
	notFound := connect.NewError(connect.CodeNotFound, fmt.Errorf("%s: not a regular file", req.Msg.Path))
	if link, err := symlinkInside(root, full); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	} else if link != "" {
		return notFound
	}
	f, err := os.OpenFile(full, os.O_RDONLY|noFollow, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || isLoop(err) {
			return notFound
		}
		return connect.NewError(connect.CodeInternal, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if !fi.Mode().IsRegular() {
		return notFound
	}
	limit := req.Msg.MaxBytes
	if limit == 0 {
		limit = defaultMaxRead
	}
	if uint64(fi.Size()) > limit {
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("%s is %d bytes, over the limit %d", req.Msg.Path, fi.Size(), limit))
	}
	buf := make([]byte, chunkSize)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if serr := stream.Send(&sandboxv1.FileChunk{Data: append([]byte(nil), buf[:n]...)}); serr != nil {
				return serr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
	}
}

func (s *Service) WriteFile(_ context.Context, stream *connect.ClientStream[sandboxv1.WriteFileRequest]) (*connect.Response[sandboxv1.WriteFileResponse], error) {
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("empty WriteFile stream"))
	}
	h := stream.Msg().GetHeader()
	if h == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the first WriteFile message must be the header"))
	}
	root, _, _, err := s.sandboxFor(h.Id)
	if err != nil {
		return nil, err
	}
	full, err := hostPath(root, h.Path)
	if err != nil {
		return nil, err
	}
	if full == root {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cannot write to /"))
	}
	if link, err := symlinkInside(root, full); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	} else if link != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s: a parent directory is a symlink", h.Path))
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	mode := fs.FileMode(h.Mode) & fs.ModePerm
	if mode == 0 {
		mode = 0o644
	}

	// 一時ファイルに書いてからrenameすると、既存のsymlinkは辿られずに置き換わる。
	tmp, err := os.CreateTemp(filepath.Dir(full), ".fakesandbox-write-")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	defer os.Remove(tmp.Name())
	var n uint64
	for stream.Receive() {
		data := stream.Msg().GetData()
		if stream.Msg().GetHeader() != nil {
			tmp.Close()
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("header sent twice"))
		}
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		n += uint64(len(data))
	}
	if err := stream.Err(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := tmp.Close(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&sandboxv1.WriteFileResponse{BytesWritten: n}), nil
}
