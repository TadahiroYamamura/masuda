package privileged

import (
	"slices"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

func TestResolve(t *testing.T) {
	decl := config.PrivilegedCommandDecl{Command: "make itest", Image: "default"}
	hash, err := config.DeclHash(decl, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Settings{
		Egress: []string{"a.example", "b.example"},
		PrivilegedCommands: map[string]config.PrivilegedCommandDecl{
			"itest": decl,
			"bad":   {Command: "", Image: "default"},
		},
	}
	approved := func(h string) config.LocalSettings {
		return config.LocalSettings{
			EgressApproved:             []string{"b.example", "c.example"},
			PrivilegedCommandsApproved: map[string]config.PrivilegedCommandApproval{"itest": {DeclHash: h}, "bad": {DeclHash: "x"}},
		}
	}
	images := func(entry string) bool { return entry == "default" }

	t.Run("承認と宣言のハッシュが一致すれば宣言・ハッシュと、宣言と承認の積の通信先を返す", func(t *testing.T) {
		r, err := Resolve(cfg, approved(hash), "itest", images)
		if err != nil {
			t.Fatal(err)
		}
		if r.Name != "itest" || r.Decl.Command != "make itest" || r.Hash != hash || !slices.Equal(r.Egress, []string{"b.example"}) {
			t.Fatalf("resolved %+v", r)
		}
	})
	t.Run("コマンドの宣言のegressは承認の積に重ならないものだけを後ろに足す", func(t *testing.T) {
		withEgress := decl
		withEgress.Egress = []string{"b.example", "auth.example"}
		h, err := config.DeclHash(withEgress, "")
		if err != nil {
			t.Fatal(err)
		}
		c := cfg
		c.PrivilegedCommands = map[string]config.PrivilegedCommandDecl{"itest": withEgress}
		r, err := Resolve(c, approved(h), "itest", images)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(r.Egress, []string{"b.example", "auth.example"}) {
			t.Fatalf("egress %v", r.Egress)
		}
	})
	cases := []struct {
		name, cmd string
		local     config.LocalSettings
		want      string
	}{
		{"宣言に無い名前は宣言済みの名前を添えて断る", "nope", approved(hash), `not declared in privilegedCommands of .masuda/settings.json (declared: [bad itest])`},
		{"形の壊れた宣言は承認があっても断る", "bad", approved(hash), "command is empty"},
		{"承認の記録が無ければapproveを促して断る", "itest", config.LocalSettings{}, "is not approved; a human must run `masuda privileged-command approve itest`"},
		{"承認の後に宣言が変わっていれば承認し直しを促して断る", "itest", approved("old"), "changed since it was approved; a human must run `masuda privileged-command approve itest` again"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Resolve(cfg, c.local, c.cmd, images); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v; want %q", err, c.want)
			}
		})
	}
}

func TestResolveは承認の後に準備を宣言したら承認し直しを促して断る(t *testing.T) {
	decl := config.PrivilegedCommandDecl{Command: "make itest", Image: "default"}
	hash, _ := config.DeclHash(decl, "")
	cfg := config.Settings{Prepare: "make generate", PrivilegedCommands: map[string]config.PrivilegedCommandDecl{"itest": decl}}
	local := config.LocalSettings{PrivilegedCommandsApproved: map[string]config.PrivilegedCommandApproval{"itest": {DeclHash: hash}}}
	images := func(entry string) bool { return entry == "default" }
	if _, err := Resolve(cfg, local, "itest", images); err == nil || !strings.Contains(err.Error(), "changed since it was approved") {
		t.Fatalf("err %v", err)
	}
	withPrepare, _ := config.DeclHash(decl, cfg.Prepare)
	local.PrivilegedCommandsApproved["itest"] = config.PrivilegedCommandApproval{DeclHash: withPrepare}
	if r, err := Resolve(cfg, local, "itest", images); err != nil || r.Hash != withPrepare {
		t.Fatalf("resolved %+v, %v", r, err)
	}
}

func TestApprovalOf(t *testing.T) {
	decl := config.PrivilegedCommandDecl{Command: "make itest", Image: "default"}
	hash, _ := config.DeclHash(decl, "")
	cases := []struct {
		name  string
		local config.LocalSettings
		want  Approval
	}{
		{"記録が無ければNotApproved", config.LocalSettings{}, NotApproved},
		{"今のハッシュの記録ならApproved", config.LocalSettings{PrivilegedCommandsApproved: map[string]config.PrivilegedCommandApproval{"itest": {DeclHash: hash}}}, Approved},
		{"違うハッシュの記録ならStale", config.LocalSettings{PrivilegedCommandsApproved: map[string]config.PrivilegedCommandApproval{"itest": {DeclHash: "old"}}}, Stale},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, got, err := ApprovalOf("itest", decl, "", c.local)
			if err != nil || got != c.want || h != hash {
				t.Fatalf("ApprovalOf = %q, %v, %v; want %v", h, got, err, c.want)
			}
		})
	}
}
