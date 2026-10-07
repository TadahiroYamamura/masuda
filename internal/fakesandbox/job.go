package fakesandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/privileged"
)

const (
	defaultJobUser        = "root"
	defaultJobCwd         = "/workspace"
	defaultMaxOutputFile  = 64 << 20
	defaultMaxOutputTotal = 1 << 30
)

// RunJob はsandbox.protoのRunJobを真似る。VMの代わりにゲストrootを作り、inputsを置き、
// setup_shell→shellをExecと同じ仕組みで動かし、outputsをoutputs_host_dirへ回収してから帳簿から外す。
// 通信は無いのでdeniedは出さない。DestroySandboxと同じく、ゲストrootは後から中を見られるよう残す。
func (s *Service) RunJob(ctx context.Context, req *connect.Request[sandboxv1.RunJobRequest], stream *connect.ServerStream[sandboxv1.RunJobEvent]) error {
	m := req.Msg
	if err := checkJob(m); err != nil {
		return err
	}
	user := m.User
	if user == "" {
		user = defaultJobUser
	}
	cwd := m.Cwd
	if cwd == "" {
		cwd = defaultJobCwd
	}
	jobTimeout := time.Duration(m.JobTimeoutMs) * time.Millisecond
	if jobTimeout == 0 {
		jobTimeout = max(2*time.Hour, time.Duration(m.TimeoutMs)*time.Millisecond+30*time.Minute)
	}
	jobCtx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()

	var buf [8]byte
	_, _ = rand.Read(buf[:])
	id := "job-" + hex.EncodeToString(buf[:])
	mu := &sync.Mutex{}
	send := func(ev *sandboxv1.RunJobEvent) error {
		mu.Lock()
		defer mu.Unlock()
		return stream.Send(ev)
	}
	phase := func(name string) error {
		return send(&sandboxv1.RunJobEvent{Event: &sandboxv1.RunJobEvent_Phase_{Phase: &sandboxv1.RunJobEvent_Phase{Name: name, SandboxId: id}}})
	}

	if err := phase("creating"); err != nil {
		return err
	}
	root, err := s.createJobSandbox(id, user, m)
	if err != nil {
		return err
	}
	destroyed := false
	destroy := func() {
		if !destroyed {
			destroyed = true
			_, _ = s.DestroySandbox(context.WithoutCancel(ctx), connect.NewRequest(&sandboxv1.DestroySandboxRequest{Id: id}))
		}
	}
	defer destroy()

	if err := phase("inputs"); err != nil {
		return err
	}
	if err := s.putInputs(root, cwd, m.Inputs); err != nil {
		return err
	}
	if err := mkdirGuest(root, cwd); err != nil {
		return err
	}

	stdout := &eventWriter{mu: mu, send: func(b []byte) error {
		return stream.Send(&sandboxv1.RunJobEvent{Event: &sandboxv1.RunJobEvent_Stdout{Stdout: b}})
	}}
	stderr := &eventWriter{mu: mu, send: func(b []byte) error {
		return stream.Send(&sandboxv1.RunJobEvent{Event: &sandboxv1.RunJobEvent_Stderr{Stderr: b}})
	}}
	run := func(shell string, timeoutMs uint32) (*sandboxv1.ExecEvent_Exited, error) {
		cmd, err := prepareExec(root, user, nil, &sandboxv1.ExecRequest{Shell: shell, User: user, Cwd: cwd, Env: m.Env, TimeoutMs: timeoutMs})
		if err != nil {
			return nil, err
		}
		return cmd.run(jobCtx, stdout, stderr)
	}
	// jobTimedOut はジョブの期限が過ぎたこと。呼び出し元が去った（ctxの取り消し）のとは分ける。
	jobTimedOut := func() bool { return ctx.Err() == nil && errors.Is(jobCtx.Err(), context.DeadlineExceeded) }

	fin := &sandboxv1.RunJobEvent_Finished{}
	setupOK := true
	if m.SetupShell != "" {
		if err := phase("setup"); err != nil {
			return err
		}
		exited, err := run(m.SetupShell, 0)
		switch {
		case jobTimedOut():
			setupOK = false
		case err != nil:
			return err
		default:
			fin.Setup = exited
			setupOK = exited.ExitCode == 0 && exited.Signal == "" && !exited.TimedOut
		}
	}
	if setupOK && !jobTimedOut() {
		if err := phase("running"); err != nil {
			return err
		}
		exited, err := run(m.Shell, m.TimeoutMs)
		switch {
		case jobTimedOut():
		case err != nil:
			return err
		default:
			fin.Exited = exited
		}
	}
	if jobTimedOut() {
		fin.JobTimedOut = true
	} else if len(m.Outputs) > 0 {
		if err := phase("outputs"); err != nil {
			return err
		}
		outputs, outErr, err := collectOutputs(root, cwd, m)
		if err != nil {
			return err
		}
		fin.Outputs, fin.OutputsError = outputs, outErr
	}
	if err := phase("destroying"); err != nil {
		return err
	}
	destroy()
	if ctx.Err() != nil {
		return connect.NewError(connect.CodeCanceled, ctx.Err())
	}
	return send(&sandboxv1.RunJobEvent{Event: &sandboxv1.RunJobEvent_Finished_{Finished: fin}})
}

// checkJob はVMを作る前に分かる誤りをInvalidArgumentで返す。
func checkJob(m *sandboxv1.RunJobRequest) error {
	invalid := func(format string, a ...any) error {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, a...))
	}
	if m.Shell == "" {
		return invalid("shell is required")
	}
	if m.User != "" && !idPattern.MatchString(m.User) {
		return invalid("invalid user %q", m.User)
	}
	if m.Cwd != "" && !path.IsAbs(m.Cwd) {
		return invalid("cwd must be absolute: %q", m.Cwd)
	}
	if len(m.Outputs) > 0 && !filepath.IsAbs(m.OutputsHostDir) {
		return invalid("outputs_host_dir must be an absolute host path with outputs")
	}
	for _, p := range m.Outputs {
		if err := privileged.ValidatePattern(p); err != nil {
			return invalid("outputs: %v", err)
		}
	}
	for _, in := range m.Inputs {
		switch src := in.Source.(type) {
		case *sandboxv1.RunJobRequest_Input_HostFile:
			if !filepath.IsAbs(src.HostFile.HostPath) || !path.IsAbs(src.HostFile.GuestPath) {
				return invalid("host_file: host_path and guest_path must be absolute")
			}
		case *sandboxv1.RunJobRequest_Input_FromSandbox:
			f := src.FromSandbox
			if !path.IsAbs(f.Root) || (f.DestRoot != "" && !path.IsAbs(f.DestRoot)) {
				return invalid("from_sandbox: root and dest_root must be absolute")
			}
			for _, p := range f.Patterns {
				if err := privileged.ValidatePattern(p); err != nil {
					return invalid("from_sandbox: %v", err)
				}
			}
		default:
			return invalid("an input without a source")
		}
	}
	return nil
}

// createJobSandbox はジョブのVMに当たるsandboxを帳簿に載せ、ゲストrootを作る。
func (s *Service) createJobSandbox(id, user string, m *sandboxv1.RunJobRequest) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root := s.Root(id)
	if err := initRoot(root, user); err != nil {
		return "", err
	}
	sb := &sandbox{
		info: &sandboxv1.Sandbox{
			Id:        id,
			BuildId:   m.BuildId,
			CreatedAt: timestamppb.Now(),
			Policy:    &sandboxv1.Policy{AllowedHosts: append([]string(nil), m.AllowedHosts...)},
		},
		defaultUser: user,
	}
	sb.emit(sandboxv1.SandboxState_SANDBOX_STATE_STARTING, "")
	sb.emit(sandboxv1.SandboxState_SANDBOX_STATE_RUNNING, "")
	s.sandboxes[id] = sb
	return root, nil
}

func mkdirGuest(root, guestDir string) error {
	dir, err := hostPath(root, guestDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	return nil
}

// putInputs はinputsを順にゲストrootへ置く。
func (s *Service) putInputs(root, cwd string, inputs []*sandboxv1.RunJobRequest_Input) error {
	for _, in := range inputs {
		switch src := in.Source.(type) {
		case *sandboxv1.RunJobRequest_Input_HostFile:
			h := src.HostFile
			b, err := os.ReadFile(h.HostPath)
			if err != nil {
				return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("host_file %s: %w", h.HostPath, err))
			}
			mode := fs.FileMode(h.Mode) & fs.ModePerm
			if mode == 0 {
				mode = 0o644
			}
			if err := writeGuestFile(root, h.GuestPath, b, mode); err != nil {
				return err
			}
		case *sandboxv1.RunJobRequest_Input_FromSandbox:
			if err := s.copyFromSandbox(root, cwd, src.FromSandbox); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFromSandbox は別のsandboxのroot以下でpatternsに当たる通常ファイルを、許可ビットを保って
// ジョブのdest_root以下の同じ相対パスへ写す。`.git`の中へは入らず、シンボリックリンクは辿らず写さない。
func (s *Service) copyFromSandbox(root, cwd string, f *sandboxv1.RunJobRequest_FromSandbox) error {
	srcRoot, _, _, err := s.sandboxFor(f.Id)
	if err != nil {
		return err
	}
	dest := f.DestRoot
	if dest == "" {
		dest = cwd
	}
	files, err := matchFiles(srcRoot, f.Root, f.Patterns)
	if err != nil {
		return err
	}
	for _, m := range files {
		b, err := readRegular(m.host)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("from_sandbox %s: %w", m.rel, err))
		}
		if err := writeGuestFile(root, path.Join(dest, m.rel), b, m.mode); err != nil {
			return err
		}
	}
	return nil
}

type matchedFile struct {
	rel  string // dirからの相対パス（`/`区切り）
	host string
	mode fs.FileMode
	size int64
}

// matchFiles はゲストのdir以下で、patternsのどれかに当たる通常ファイルをパスの順に返す。
func matchFiles(root, dir string, patterns []string) ([]matchedFile, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	base, err := hostPath(root, dir)
	if err != nil {
		return nil, err
	}
	if link, err := symlinkInside(root, base); err != nil || link != "" {
		return nil, err
	}
	var out []matchedFile
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == base {
			return fs.SkipAll
		} else if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" && p != base {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, pat := range patterns {
			if privileged.Match(pat, rel) {
				fi, err := d.Info()
				if err != nil {
					return err
				}
				out = append(out, matchedFile{rel: rel, host: p, mode: fi.Mode().Perm(), size: fi.Size()})
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

func writeGuestFile(root, guestPath string, b []byte, mode fs.FileMode) error {
	full, err := hostPath(root, guestPath)
	if err != nil {
		return err
	}
	if link, err := symlinkInside(root, full); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	} else if link != "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s: a parent directory is a symlink", guestPath))
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if err := os.WriteFile(full, b, mode); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	// WriteFileの許可ビットはumaskで削られるので、写し元の値に揃え直す。
	if err := os.Chmod(full, mode); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	return nil
}

// collectOutputs はcwd以下でoutputsに当たるファイルをoutputs_host_dirへ写す。当たらなかった
// パターン、読めなかったファイル、上限を超えたファイルはoutputs_errorにまとめ、読めたものは回収する。
// 戻りのerrorはホスト側に書けなかったときだけ。
func collectOutputs(root, cwd string, m *sandboxv1.RunJobRequest) ([]string, string, error) {
	files, err := matchFiles(root, cwd, m.Outputs)
	if err != nil {
		return nil, "", err
	}
	maxFile, maxTotal := m.MaxOutputFileBytes, m.MaxOutputTotalBytes
	if maxFile == 0 {
		maxFile = defaultMaxOutputFile
	}
	if maxTotal == 0 {
		maxTotal = defaultMaxOutputTotal
	}
	var problems []string
	for _, pat := range m.Outputs {
		hit := false
		for _, f := range files {
			if privileged.Match(pat, f.rel) {
				hit = true
				break
			}
		}
		if !hit {
			problems = append(problems, fmt.Sprintf("no file matched %q", pat))
		}
	}
	got := []string{}
	var total uint64
	for _, f := range files {
		if uint64(f.size) > maxFile {
			problems = append(problems, fmt.Sprintf("%s is %d bytes, over the per-file limit %d", f.rel, f.size, maxFile))
			continue
		}
		if total+uint64(f.size) > maxTotal {
			problems = append(problems, fmt.Sprintf("%s: over the total limit %d", f.rel, maxTotal))
			continue
		}
		b, err := readRegular(f.host)
		if err != nil {
			problems = append(problems, fmt.Sprintf("reading %s: %v", f.rel, err))
			continue
		}
		total += uint64(len(b))
		dst := filepath.Join(m.OutputsHostDir, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, "", connect.NewError(connect.CodeInternal, err)
		}
		if err := os.WriteFile(dst, b, f.mode&0o700); err != nil {
			return nil, "", connect.NewError(connect.CodeInternal, err)
		}
		if err := os.Chmod(dst, f.mode&0o700); err != nil {
			return nil, "", connect.NewError(connect.CodeInternal, err)
		}
		got = append(got, f.rel)
	}
	return got, strings.Join(problems, "; "), nil
}

// readRegular はシンボリックリンクを辿らずに通常ファイルを読む（歩いた後にジョブが差し替えていても）。
func readRegular(p string) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return io.ReadAll(f)
}

// DeleteImage はフェイクの帳簿からイメージを消す。無いbuild_idは何もしない。
func (s *Service) DeleteImage(_ context.Context, req *connect.Request[sandboxv1.DeleteImageRequest]) (*connect.Response[sandboxv1.DeleteImageResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.images[:0]
	for _, img := range s.images {
		if img.BuildId != req.Msg.BuildId {
			kept = append(kept, img)
		}
	}
	s.images = kept
	return connect.NewResponse(&sandboxv1.DeleteImageResponse{}), nil
}
