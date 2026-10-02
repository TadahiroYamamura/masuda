package serve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/privileged"
	"github.com/TadahiroYamamura/masuda/internal/secrets"
)

// configService はConfigServiceの実装。宣言（settings.json）は作業ツリーのものを読み、
// 承認はsettings.local.jsonへ、秘密の値は秘密ストアへ書く。宣言に無いものは承認も値の登録も
// 受け付けない（InvalidArgument）。
type configService struct {
	apiv1connect.UnimplementedConfigServiceHandler
	backend *backend
	// mu はsettings.local.jsonの読み→書きを直列にする。別々の承認が同時に来ても、
	// 片方の書き込みがもう片方を消さないように。
	mu sync.Mutex
}

// load はrepo_rootを検査して、宣言と承認を読む。
func (s *configService) load(ctx context.Context, repoRoot string) (string, config.Settings, config.LocalSettings, error) {
	if repoRoot == "" || !filepath.IsAbs(repoRoot) {
		return "", config.Settings{}, config.LocalSettings{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("repo_root must be an absolute path: %q", repoRoot))
	}
	root, err := repoTop(ctx, repoRoot)
	if err != nil {
		return "", config.Settings{}, config.LocalSettings{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		return "", config.Settings{}, config.LocalSettings{}, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	local, err := config.LoadLocal(root)
	if err != nil {
		return "", config.Settings{}, config.LocalSettings{}, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return root, cfg, local, nil
}

func (s *configService) saveLocal(root string, local config.LocalSettings) error {
	if err := config.SaveLocal(root, local); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// egress
// ---------------------------------------------------------------------------

func egressEntries(cfg config.Settings, local config.LocalSettings) *apiv1.ListEgressResponse {
	out := &apiv1.ListEgressResponse{}
	for _, h := range cfg.Egress {
		out.Entries = append(out.Entries, &apiv1.EgressEntry{Host: h, Approved: contains(local.EgressApproved, h)})
	}
	return out
}

func (s *configService) ListEgress(ctx context.Context, req *connect.Request[apiv1.RepoRequest]) (*connect.Response[apiv1.ListEgressResponse], error) {
	_, cfg, local, err := s.load(ctx, req.Msg.RepoRoot)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(egressEntries(cfg, local)), nil
}

func (s *configService) ApproveEgress(ctx context.Context, req *connect.Request[apiv1.HostRequest]) (*connect.Response[apiv1.ListEgressResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, cfg, local, err := s.load(ctx, req.Msg.RepoRoot)
	if err != nil {
		return nil, err
	}
	h := req.Msg.Host
	if !contains(cfg.Egress, h) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("host %q is not declared in egress of .masuda/settings.json", h))
	}
	if !contains(local.EgressApproved, h) {
		local.EgressApproved = append(local.EgressApproved, h)
		if err := s.saveLocal(root, local); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(egressEntries(cfg, local)), nil
}

// RejectEgress は承認を取り消す。宣言から消えたホストの承認も取り消せる（残った承認は
// 効かないが、settings.local.jsonの掃除のため）。どちらにも無いホストはInvalidArgument。
func (s *configService) RejectEgress(ctx context.Context, req *connect.Request[apiv1.HostRequest]) (*connect.Response[apiv1.ListEgressResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, cfg, local, err := s.load(ctx, req.Msg.RepoRoot)
	if err != nil {
		return nil, err
	}
	h := req.Msg.Host
	if !contains(cfg.Egress, h) && !contains(local.EgressApproved, h) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("host %q is neither declared nor approved", h))
	}
	if contains(local.EgressApproved, h) {
		var kept []string
		for _, x := range local.EgressApproved {
			if x != h {
				kept = append(kept, x)
			}
		}
		local.EgressApproved = kept
		if err := s.saveLocal(root, local); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(egressEntries(cfg, local)), nil
}

// ---------------------------------------------------------------------------
// secrets
// ---------------------------------------------------------------------------

// secretEntries は宣言した秘密の一覧（宣言の順）。approvedはplaintextなら承認の有無、
// placeholderなら承認が要らないのでtrue。Claude APIのトークンは宣言ではないので含めず、
// 値の有無だけをclaude_token_setで返す（Runが使うのと同じく、M4の暫定ファイルも見る）。
func (s *configService) secretEntries(root string, cfg config.Settings, local config.LocalSettings) *apiv1.ListSecretsResponse {
	store := secrets.New(s.backend.dataDir)
	out := &apiv1.ListSecretsResponse{}
	for _, d := range cfg.Secrets {
		mode := d.EffectiveMode()
		plaintext := mode == config.ModePlaintext
		out.Entries = append(out.Entries, &apiv1.SecretEntry{
			Name:             d.Name,
			Hosts:            d.Hosts,
			Mode:             mode,
			ValueSet:         store.Has(root, d.Name),
			ApprovalRequired: plaintext,
			Approved:         !plaintext || contains(local.SecretsApproved, d.Name),
		})
	}
	_, out.ClaudeTokenSet, _ = store.ClaudeToken(root, local.ClaudeTokenName())
	return out
}

// ListSecrets はrepo_rootが空なら、ユーザー単位のClaudeのトークン（既定の名前）の有無だけを返す。
func (s *configService) ListSecrets(ctx context.Context, req *connect.Request[apiv1.RepoRequest]) (*connect.Response[apiv1.ListSecretsResponse], error) {
	if req.Msg.RepoRoot == "" {
		return connect.NewResponse(&apiv1.ListSecretsResponse{ClaudeTokenSet: secrets.New(s.backend.dataDir).Has("", config.ReservedSecret)}), nil
	}
	root, cfg, local, err := s.load(ctx, req.Msg.RepoRoot)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(s.secretEntries(root, cfg, local)), nil
}

// SetSecret は値を秘密ストアへ置く。宣言した名前と、Claude APIのトークンの名前
// （CLAUDE_CODE_OAUTH_TOKENか、settings.local.jsonのclaudeTokenが選んだ名前）だけを受け付ける。
// repo_rootが空ならユーザー単位の置き場所へ置く（Claudeのトークン用。どのリポジトリでも、
// リポジトリごとの登録が無ければこれを使う）。
func (s *configService) SetSecret(ctx context.Context, req *connect.Request[apiv1.SetSecretRequest]) (*connect.Response[apiv1.ListSecretsResponse], error) {
	if req.Msg.RepoRoot == "" {
		return s.setUserSecret(req.Msg.Name, req.Msg.Value)
	}
	root, cfg, local, err := s.load(ctx, req.Msg.RepoRoot)
	if err != nil {
		return nil, err
	}
	name := req.Msg.Name
	_, declared := cfg.Secret(name)
	if !declared && name != config.ReservedSecret && name != local.ClaudeTokenName() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("secret %q is not declared in secrets of .masuda/settings.json", name))
	}
	if req.Msg.Value == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("value is empty"))
	}
	if err := secrets.New(s.backend.dataDir).Set(root, name, req.Msg.Value); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(s.secretEntries(root, cfg, local)), nil
}

// setUserSecret はユーザー単位の値を置く。宣言はリポジトリにあるので、ここでは名前の形だけを
// 確かめる（claudeTokenでリポジトリごとに別名を選べるので、名前を既定の1つに絞らない）。
// 応答のclaude_token_setは置いた名前に値があるか。entriesは空。
func (s *configService) setUserSecret(name, value string) (*connect.Response[apiv1.ListSecretsResponse], error) {
	if !config.ValidName(name) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid secret name %q", name))
	}
	if value == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("value is empty"))
	}
	store := secrets.New(s.backend.dataDir)
	if err := store.Set("", name, value); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&apiv1.ListSecretsResponse{ClaudeTokenSet: store.Has("", name)}), nil
}

// ApproveSecret はplaintextモードの秘密の承認を記録する。placeholderは承認が要らないので
// InvalidArgument（承認したつもりで何も変わらないことに気づけるように）。
func (s *configService) ApproveSecret(ctx context.Context, req *connect.Request[apiv1.NameRequest]) (*connect.Response[apiv1.ListSecretsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, cfg, local, err := s.load(ctx, req.Msg.RepoRoot)
	if err != nil {
		return nil, err
	}
	name := req.Msg.Name
	d, ok := cfg.Secret(name)
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("secret %q is not declared in secrets of .masuda/settings.json", name))
	}
	if d.EffectiveMode() != config.ModePlaintext {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("secret %q is in %s mode and needs no approval", name, d.EffectiveMode()))
	}
	if !contains(local.SecretsApproved, name) {
		local.SecretsApproved = append(local.SecretsApproved, name)
		if err := s.saveLocal(root, local); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(s.secretEntries(root, cfg, local)), nil
}

// RejectSecret は承認を取り消す。RejectEgressと同じく、宣言から消えた名前の承認も掃除できる。
// 宣言がplaceholderで承認の記録も無い名前はInvalidArgument。
func (s *configService) RejectSecret(ctx context.Context, req *connect.Request[apiv1.NameRequest]) (*connect.Response[apiv1.ListSecretsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, cfg, local, err := s.load(ctx, req.Msg.RepoRoot)
	if err != nil {
		return nil, err
	}
	name := req.Msg.Name
	recorded := contains(local.SecretsApproved, name)
	d, declared := cfg.Secret(name)
	switch {
	case !declared && !recorded:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("secret %q is neither declared nor approved", name))
	case declared && d.EffectiveMode() != config.ModePlaintext && !recorded:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("secret %q is in %s mode and needs no approval", name, d.EffectiveMode()))
	}
	if recorded {
		var kept []string
		for _, x := range local.SecretsApproved {
			if x != name {
				kept = append(kept, x)
			}
		}
		local.SecretsApproved = kept
		if err := s.saveLocal(root, local); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(s.secretEntries(root, cfg, local)), nil
}

// ---------------------------------------------------------------------------
// privileged commands
// ---------------------------------------------------------------------------

func privilegedEntries(cfg config.Settings, local config.LocalSettings) (*apiv1.ListPrivilegedCommandsResponse, error) {
	names := make([]string, 0, len(cfg.PrivilegedCommands))
	for name := range cfg.PrivilegedCommands {
		names = append(names, name)
	}
	sort.Strings(names)
	out := &apiv1.ListPrivilegedCommandsResponse{}
	for _, name := range names {
		d := cfg.PrivilegedCommands[name]
		hash, err := config.DeclHash(d)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		a, recorded := local.PrivilegedCommandsApproved[name]
		out.Entries = append(out.Entries, &apiv1.PrivilegedCommandEntry{
			Name:     name,
			Image:    d.Image,
			Command:  d.Command,
			Approved: recorded && a.DeclHash == hash,
			Stale:    recorded && a.DeclHash != hash,
		})
	}
	return out, nil
}

func (s *configService) ListPrivilegedCommands(ctx context.Context, req *connect.Request[apiv1.RepoRequest]) (*connect.Response[apiv1.ListPrivilegedCommandsResponse], error) {
	_, cfg, local, err := s.load(ctx, req.Msg.RepoRoot)
	if err != nil {
		return nil, err
	}
	out, err := privilegedEntries(cfg, local)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(out), nil
}

// ApprovePrivilegedCommand は今の宣言のハッシュで承認を記録する。宣言が変われば一覧でstaleになり、
// 承認し直すまで使えない。形の壊れた宣言（空のコマンド、無いイメージ、/workspaceの外を指す
// inputs・outputs）は承認させない。
func (s *configService) ApprovePrivilegedCommand(ctx context.Context, req *connect.Request[apiv1.NameRequest]) (*connect.Response[apiv1.ListPrivilegedCommandsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, cfg, local, err := s.load(ctx, req.Msg.RepoRoot)
	if err != nil {
		return nil, err
	}
	d, ok := cfg.PrivilegedCommands[req.Msg.Name]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("privileged command %q is not declared in .masuda/settings.json", req.Msg.Name))
	}
	if err := privileged.Validate(d, imageExistsIn(filepath.Join(root, config.DirName))); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("privileged command %q: %w", req.Msg.Name, err))
	}
	hash, err := config.DeclHash(d)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if local.PrivilegedCommandsApproved == nil {
		local.PrivilegedCommandsApproved = map[string]config.PrivilegedCommandApproval{}
	}
	local.PrivilegedCommandsApproved[req.Msg.Name] = config.PrivilegedCommandApproval{DeclHash: hash}
	if err := s.saveLocal(root, local); err != nil {
		return nil, err
	}
	out, err := privilegedEntries(cfg, local)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(out), nil
}

// ---------------------------------------------------------------------------
// images
// ---------------------------------------------------------------------------

// imageEntries は`.masuda/images/`の下でDockerfileを持つディレクトリ（エントリ）の名前を返す。
func imageEntries(root string) ([]string, error) {
	dirs, err := os.ReadDir(filepath.Join(root, config.DirName, "images"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range dirs {
		if !d.IsDir() || !config.ValidCheckName(d.Name()) {
			continue
		}
		if st, err := os.Stat(filepath.Join(root, config.DirName, "images", d.Name(), "Dockerfile")); err == nil && st.Mode().IsRegular() {
			out = append(out, d.Name())
		}
	}
	return out, nil
}

// ListImages はエントリごとに最後にビルドしたbuild_idを返す。builtは、そのbuild_idの資産が
// 今もsandbox serviceにあること。
func (s *configService) ListImages(ctx context.Context, req *connect.Request[apiv1.RepoRequest]) (*connect.Response[apiv1.ListImagesResponse], error) {
	root, _, _, err := s.load(ctx, req.Msg.RepoRoot)
	if err != nil {
		return nil, err
	}
	entries, err := imageEntries(root)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	res, err := s.backend.sandbox.ListImages(ctx, connect.NewRequest(&sandboxv1.ListImagesRequest{}))
	if err != nil {
		return nil, fmt.Errorf("listing the sandbox service's images: %w", err)
	}
	have := map[string]bool{}
	for _, img := range res.Msg.Images {
		have[img.BuildId] = true
	}
	out := &apiv1.ListImagesResponse{}
	for _, e := range entries {
		entry := &apiv1.ImageEntry{Entry: e}
		if rec, ok := s.backend.loadImageRecord(root, e); ok {
			entry.BuildId = rec.BuildID
			entry.Built = have[rec.BuildID]
		}
		out.Entries = append(out.Entries, entry)
	}
	return connect.NewResponse(out), nil
}

// BuildImage は作業ツリーの`.masuda/images/<entry>/`をビルドし、ログを流して最後にbuild_idを返す。
// entryが空ならsettings.jsonのimage。
func (s *configService) BuildImage(ctx context.Context, req *connect.Request[apiv1.BuildImageRequest], stream *connect.ServerStream[apiv1.BuildImageEvent]) error {
	root, cfg, _, err := s.load(ctx, req.Msg.RepoRoot)
	if err != nil {
		return err
	}
	entry := req.Msg.Entry
	if entry == "" {
		entry = cfg.ImageEntry()
	}
	if !config.ValidCheckName(entry) {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid image entry %q", entry))
	}
	dir := filepath.Join(root, config.DirName, "images", entry)
	if st, err := os.Stat(filepath.Join(dir, "Dockerfile")); err != nil || !st.Mode().IsRegular() {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf(".masuda/images/%s/Dockerfile does not exist", entry))
	}
	id, err := s.backend.buildImage(ctx, root, entry, dir, func(line string) error {
		return stream.Send(&apiv1.BuildImageEvent{LogLine: line})
	})
	if err != nil {
		return err
	}
	return stream.Send(&apiv1.BuildImageEvent{BuildId: id})
}
