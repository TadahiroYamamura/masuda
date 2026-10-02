package serve

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/secrets"
)

func TestApproveAndRejectSecret(t *testing.T) {
	dataDir := t.TempDir()
	s := &configService{backend: &backend{dataDir: dataDir}}
	repo := newSmokeRepo(t)
	writeRepoFile(t, repo, ".masuda/settings.json", `{
		"secrets": [{"name": "LINEAR_API_KEY", "hosts": ["api.linear.app"]}, {"name": "LEGACY", "mode": "plaintext"}]
	}`)
	ctx := context.Background()
	req := func(name string) *connect.Request[apiv1.NameRequest] {
		return connect.NewRequest(&apiv1.NameRequest{RepoRoot: repo, Name: name})
	}
	entry := func(res *apiv1.ListSecretsResponse, name string) *apiv1.SecretEntry {
		for _, e := range res.Entries {
			if e.Name == name {
				return e
			}
		}
		t.Fatalf("%s not listed: %v", name, res.Entries)
		return nil
	}

	list, err := s.ListSecrets(ctx, connect.NewRequest(&apiv1.RepoRequest{RepoRoot: repo}))
	if err != nil {
		t.Fatal(err)
	}
	if e := entry(list.Msg, "LEGACY"); !e.ApprovalRequired || e.Approved {
		t.Fatalf("plaintext before approval: %+v", e)
	}
	if e := entry(list.Msg, "LINEAR_API_KEY"); e.ApprovalRequired || !e.Approved {
		t.Fatalf("placeholder: %+v", e)
	}
	if list.Msg.ClaudeTokenSet {
		t.Fatal("claude token reported as set before it was stored")
	}

	for _, name := range []string{"LINEAR_API_KEY", "NOPE"} {
		if _, err := s.ApproveSecret(ctx, req(name)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("approve %s: %v", name, err)
		}
		if _, err := s.RejectSecret(ctx, req(name)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("reject %s: %v", name, err)
		}
	}

	res, err := s.ApproveSecret(ctx, req("LEGACY"))
	if err != nil {
		t.Fatal(err)
	}
	if !entry(res.Msg, "LEGACY").Approved {
		t.Fatal("LEGACY not approved")
	}
	local, _ := config.LoadLocal(repo)
	if len(local.SecretsApproved) != 1 || local.SecretsApproved[0] != "LEGACY" {
		t.Fatalf("settings.local.json: %+v", local)
	}

	res, err = s.RejectSecret(ctx, req("LEGACY"))
	if err != nil {
		t.Fatal(err)
	}
	if entry(res.Msg, "LEGACY").Approved {
		t.Fatal("LEGACY still approved")
	}

	if err := secrets.New(dataDir).Set(repo, config.ReservedSecret, "tok"); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListSecrets(ctx, connect.NewRequest(&apiv1.RepoRequest{RepoRoot: repo}))
	if !list.Msg.ClaudeTokenSet || len(list.Msg.Entries) != 2 {
		t.Fatalf("after storing the token: %+v", list.Msg)
	}
}

// repo_rootを空にしたSetSecretはユーザー単位の置き場所へ置き、どのリポジトリでもトークンとして見える。
func TestSetSecretUserScope(t *testing.T) {
	dataDir := t.TempDir()
	s := &configService{backend: &backend{dataDir: dataDir}}
	repo := newSmokeRepo(t)
	ctx := context.Background()
	res, err := s.SetSecret(ctx, connect.NewRequest(&apiv1.SetSecretRequest{Name: config.ReservedSecret, Value: "tok"}))
	if err != nil || !res.Msg.ClaudeTokenSet {
		t.Fatalf("user-scope SetSecret: %v %v", err, res)
	}
	if !secrets.New(dataDir).Has("", config.ReservedSecret) {
		t.Fatal("not stored in the user scope")
	}
	list, err := s.ListSecrets(ctx, connect.NewRequest(&apiv1.RepoRequest{RepoRoot: repo}))
	if err != nil || !list.Msg.ClaudeTokenSet {
		t.Fatalf("a repository sees the user token: %v %v", err, list)
	}
	user, err := s.ListSecrets(ctx, connect.NewRequest(&apiv1.RepoRequest{}))
	if err != nil || !user.Msg.ClaudeTokenSet || len(user.Msg.Entries) != 0 {
		t.Fatalf("user-scope ListSecrets: %v %v", err, user)
	}
	if _, err := s.SetSecret(ctx, connect.NewRequest(&apiv1.SetSecretRequest{Name: "../x", Value: "v"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad name: %v", err)
	}
}
