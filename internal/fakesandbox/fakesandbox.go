// Package fakesandbox は、masuda-sandboxのSandboxService（../masuda-sandbox/proto）を
// VM無しでプロセス内に実装したもの。契約テストと`masuda serve --fake-sandbox`用。
//
// ゲストのrootは`<Dir>/<sandbox-id>/root/`で、ゲストの`/workspace`は`root/workspace`、
// `/home/ubuntu`は`root/home/ubuntu`に写る。Execはホストでそのまま動くので隔離は無い。
// ゲストへ渡すコマンドは、cwdを写像するだけでコマンド文字列中の絶対パスは写像しない。ただしroot
// 指定のExecとRunJob（特権コマンド）はゲストrootへchrootして動かすので、絶対パスもゲストのものになる（exec.go）。
package fakesandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
	"github.com/TadahiroYamamura/masuda/internal/sandboxcontract"
)

// Service はSandboxServiceHandlerの実装。
type Service struct {
	sandboxv1connect.UnimplementedSandboxServiceHandler

	dir string

	mu        sync.Mutex
	sandboxes map[string]*sandbox
	images    []*sandboxv1.Image
}

type sandbox struct {
	info        *sandboxv1.Sandbox
	defaultUser string
	env         map[string]string

	// events はWatchEventsの再送用に全件を持つ。フェイクではHTTPイベントが出ないので
	// 状態変化の数件しか溜まらない。
	events  []*sandboxv1.SandboxEvent
	waiters []chan struct{}
	closed  bool
}

// New はdir（`<DataDir>/fake`）の下にゲストrootを作るフェイクを返す。
func New(dir string) *Service {
	return &Service{dir: dir, sandboxes: map[string]*sandbox{}}
}

// Root はsandbox idのゲストrootのホスト側パス。
func (s *Service) Root(id string) string { return filepath.Join(s.dir, id, "root") }

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (s *Service) get(id string) (*sandbox, error) {
	sb, ok := s.sandboxes[id]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("sandbox %q not found", id))
	}
	return sb, nil
}

// emit は状態変化を記録し、待っているWatchEventsを起こす。s.muを持って呼ぶ。
func (sb *sandbox) emit(state sandboxv1.SandboxState, detail string) {
	sb.info.State = state
	ev := &sandboxv1.SandboxEvent{
		Seq:  uint64(len(sb.events) + 1),
		Time: timestamppb.Now(),
		Event: &sandboxv1.SandboxEvent_StateChanged_{StateChanged: &sandboxv1.SandboxEvent_StateChanged{
			State: state, Detail: detail,
		}},
	}
	sb.events = append(sb.events, ev)
	for _, w := range sb.waiters {
		close(w)
	}
	sb.waiters = nil
}

// placeholder はシークレット名ごとに決定的な偽の値を返す。sandboxをまたいで同じ値に
// なるので、テストがプレースホルダの値を事前に知ることができる。
func placeholder(d *sandboxv1.SecretDecl) string {
	sum := sha256.Sum256([]byte("masuda-fake-placeholder:" + d.Name))
	body := hex.EncodeToString(sum[:])
	prefix := d.PlaceholderPrefix
	if prefix == "" {
		prefix = "masuda-fake-placeholder-"
	}
	n := int(d.PlaceholderLength)
	if n == 0 {
		n = len(prefix) + 32
	}
	for len(prefix)+len(body) < n {
		body += body
	}
	if n > len(prefix) {
		return prefix + body[:n-len(prefix)]
	}
	return prefix
}

func homeOf(user string) string {
	if user == "root" {
		return "/root"
	}
	return "/home/" + user
}

func (s *Service) CreateSandbox(_ context.Context, req *connect.Request[sandboxv1.CreateSandboxRequest]) (*connect.Response[sandboxv1.Sandbox], error) {
	m := req.Msg
	if !idPattern.MatchString(m.Id) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid sandbox id %q", m.Id))
	}
	user := m.DefaultUser
	if user == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("default_user is required"))
	}
	if !idPattern.MatchString(user) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid default_user %q", user))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sandboxes[m.Id]; ok {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("sandbox %q already exists", m.Id))
	}

	if err := initRoot(s.Root(m.Id), user); err != nil {
		return nil, err
	}

	placeholders := map[string]string{}
	for _, d := range m.Secrets {
		if d.Name == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("secret without a name"))
		}
		placeholders[d.Name] = placeholder(d)
	}
	policy := m.Policy
	if policy == nil {
		policy = &sandboxv1.Policy{}
	}
	if err := checkPolicy(policy, placeholders); err != nil {
		return nil, err
	}
	sb := &sandbox{
		info: &sandboxv1.Sandbox{
			Id:           m.Id,
			BuildId:      m.BuildId,
			CreatedAt:    timestamppb.Now(),
			Placeholders: placeholders,
			Policy:       proto.Clone(policy).(*sandboxv1.Policy),
		},
		defaultUser: user,
		env:         m.Env,
	}
	// 実物は「ゲストがExecを受け付けるようになってから戻る」ので、戻る時点でRUNNINGにする。
	sb.emit(sandboxv1.SandboxState_SANDBOX_STATE_STARTING, "")
	sb.emit(sandboxv1.SandboxState_SANDBOX_STATE_RUNNING, "")
	s.sandboxes[m.Id] = sb
	return connect.NewResponse(proto.Clone(sb.info).(*sandboxv1.Sandbox)), nil
}

// initRoot はゲストrootを空の状態から作る。実物のVMは使い捨てでディスクを持ち越さないので、
// 同じidの前回のrootは消して作り直す。
func initRoot(root, user string) error {
	if err := os.RemoveAll(root); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	for _, d := range []string{"masuda", "tmp", "root", filepath.Join("home", user)} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
	}
	return nil
}

func checkPolicy(p *sandboxv1.Policy, placeholders map[string]string) error {
	for _, name := range p.EnabledSecrets {
		if _, ok := placeholders[name]; !ok {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("secret %q was not declared at creation", name))
		}
	}
	return nil
}

func (s *Service) GetSandbox(_ context.Context, req *connect.Request[sandboxv1.GetSandboxRequest]) (*connect.Response[sandboxv1.Sandbox], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sb, err := s.get(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(proto.Clone(sb.info).(*sandboxv1.Sandbox)), nil
}

func (s *Service) ListSandboxes(context.Context, *connect.Request[sandboxv1.ListSandboxesRequest]) (*connect.Response[sandboxv1.ListSandboxesResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &sandboxv1.ListSandboxesResponse{}
	for _, sb := range s.sandboxes {
		out.Sandboxes = append(out.Sandboxes, proto.Clone(sb.info).(*sandboxv1.Sandbox))
	}
	sort.Slice(out.Sandboxes, func(i, j int) bool { return out.Sandboxes[i].Id < out.Sandboxes[j].Id })
	return connect.NewResponse(out), nil
}

// DestroySandbox は帳簿から外す。ゲストrootは消さずに残す（テストや調査で後から
// 中身を見られるように）。同じidで作り直すときにCreateSandboxが消す。
func (s *Service) DestroySandbox(_ context.Context, req *connect.Request[sandboxv1.DestroySandboxRequest]) (*connect.Response[sandboxv1.DestroySandboxResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sb, ok := s.sandboxes[req.Msg.Id]; ok {
		sb.emit(sandboxv1.SandboxState_SANDBOX_STATE_STOPPED, "destroyed")
		sb.closed = true
		delete(s.sandboxes, req.Msg.Id)
	}
	return connect.NewResponse(&sandboxv1.DestroySandboxResponse{}), nil
}

func (s *Service) SetPolicy(_ context.Context, req *connect.Request[sandboxv1.SetPolicyRequest]) (*connect.Response[sandboxv1.SetPolicyResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sb, err := s.get(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	p := req.Msg.Policy
	if p == nil {
		p = &sandboxv1.Policy{}
	}
	if err := checkPolicy(p, sb.info.Placeholders); err != nil {
		return nil, err
	}
	sb.info.Policy = proto.Clone(p).(*sandboxv1.Policy)
	return connect.NewResponse(&sandboxv1.SetPolicyResponse{}), nil
}

func (s *Service) BuildImage(_ context.Context, req *connect.Request[sandboxv1.BuildImageRequest], stream *connect.ServerStream[sandboxv1.BuildImageEvent]) error {
	if req.Msg.ContextDir == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("context_dir is required"))
	}
	sum := sha256.Sum256([]byte(req.Msg.ContextDir + "\x00" + req.Msg.Name + "\x00" + time.Now().String()))
	img := &sandboxv1.Image{
		BuildId:   "fake-" + hex.EncodeToString(sum[:8]),
		Name:      req.Msg.Name,
		Arch:      req.Msg.Arch,
		CreatedAt: timestamppb.Now(),
		OciDigest: "sha256:" + hex.EncodeToString(sum[:]),
	}
	s.mu.Lock()
	s.images = append(s.images, img)
	s.mu.Unlock()
	if err := stream.Send(&sandboxv1.BuildImageEvent{Event: &sandboxv1.BuildImageEvent_LogLine{LogLine: "fake sandbox: no build performed"}}); err != nil {
		return err
	}
	return stream.Send(&sandboxv1.BuildImageEvent{Event: &sandboxv1.BuildImageEvent_Built{Built: img}})
}

func (s *Service) ListImages(context.Context, *connect.Request[sandboxv1.ListImagesRequest]) (*connect.Response[sandboxv1.ListImagesResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &sandboxv1.ListImagesResponse{}
	for _, img := range s.images {
		out.Images = append(out.Images, proto.Clone(img).(*sandboxv1.Image))
	}
	return connect.NewResponse(out), nil
}

func (s *Service) DisableSsh(_ context.Context, req *connect.Request[sandboxv1.DisableSshRequest]) (*connect.Response[sandboxv1.DisableSshResponse], error) {
	return connect.NewResponse(&sandboxv1.DisableSshResponse{}), nil
}

// WatchEvents はafter_seqより後の記録済みイベントを送ってから、新しいものを待って送る。
// sandboxが破棄されたら残りを送って終わる。
func (s *Service) WatchEvents(ctx context.Context, req *connect.Request[sandboxv1.WatchEventsRequest], stream *connect.ServerStream[sandboxv1.SandboxEvent]) error {
	s.mu.Lock()
	sb, err := s.get(req.Msg.Id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	next := uint64(len(sb.events))
	if req.Msg.AfterSeq > 0 {
		next = min(req.Msg.AfterSeq, next)
	}
	s.mu.Unlock()

	for {
		s.mu.Lock()
		pending := append([]*sandboxv1.SandboxEvent(nil), sb.events[next:]...)
		closed := sb.closed
		var wait chan struct{}
		if len(pending) == 0 && !closed {
			wait = make(chan struct{})
			sb.waiters = append(sb.waiters, wait)
		}
		s.mu.Unlock()

		for _, ev := range pending {
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
		next += uint64(len(pending))
		if closed {
			return nil
		}
		if wait != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wait:
			}
		}
	}
}

// sandboxFor はExec・ファイル操作の対象sandboxと既定ユーザー・環境を返す。
func (s *Service) sandboxFor(id string) (root, defaultUser string, env map[string]string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sb, err := s.get(id)
	if err != nil {
		return "", "", nil, err
	}
	return s.Root(id), sb.defaultUser, sb.env, nil
}

// GetServerInfo は実物と同じ形で返す。contract_sha256はmasudaの生成元のものなので、
// フェイクとの契約は常に合う。
func (s *Service) GetServerInfo(context.Context, *connect.Request[sandboxv1.GetServerInfoRequest]) (*connect.Response[sandboxv1.ServerInfo], error) {
	return connect.NewResponse(&sandboxv1.ServerInfo{
		Version:        "fake",
		Contract:       "masuda.sandbox.v1",
		ContractSha256: sandboxcontract.SHA256,
		Platform:       runtime.GOOS + "/" + runtime.GOARCH,
	}), nil
}
