package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/TadahiroYamamura/masuda-engine/engine"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/guest"
	"github.com/TadahiroYamamura/masuda/internal/secrets"
)

// bootPlan は実行1つのsandboxとゲストの組み立て方。定義の写し（settings.json）・利用者の
// 承認（settings.local.json）・秘密ストアから、Run・Resumeの受け付け時に作る。
// 作れない（承認や値が足りない）ならワークスペースを作る前・再開する前に断る。
type bootPlan struct {
	// image はイメージのエントリ。ビルドの文脈は定義の写しの`images/<entry>/`。
	image string
	// diskMiB はVMのルートディスクの最小容量（settings.jsonの`images.<entry>.diskMiB`）。
	diskMiB uint32
	// egress はノードが選んでよいホストの上限（宣言∩承認）。
	egress []string
	// secrets はsandboxへ宣言する秘密（Claude APIのトークンを含む）。値は秘密ストアから読んだもの。
	secrets []*sandboxv1.SecretDecl
	// placeholderNames はノードが有効にできる秘密の名前（トークンを除く）。
	placeholderNames []string
	// plaintext はplaintextモードの秘密の名前→値。sandboxのenvで渡す。
	plaintext map[string]string
	envFiles  []config.EnvFile
	// vars はenvFilesの公開値（settings.local.jsonのvars）。
	vars           map[string]string
	checks         map[string]string
	claudeSettings json.RawMessage
	// stallAfter はsettings.local.jsonのstallAfter（無ければ0で、serve全体の既定に従う）。
	// serveの--stall-afterが指定されていればそちらが勝つ（backend.stallFor）。
	stallAfter time.Duration
}

// claudeTokenPrefix はClaude APIのトークン（OAuth）のプレースホルダの形。クライアントが
// 形式を検査しても通るようにする。
const claudeTokenPrefix = "sk-ant-oat01-"

// planBoot はdefsDir（`.masuda/`の写し）とrepoRootの承認・秘密からbootPlanを作る。
// 足りないものはまとめてFailedPreconditionで返す（1つ直すたびに次が出てくるのを避ける）。
// imageは要求で指定されたエントリ（空ならsettings.jsonのimage）。
func (b *backend) planBoot(defsDir, repoRoot string, set *engine.Set, workflow, image string) (*bootPlan, error) {
	cfg, err := config.LoadDir(defsDir)
	if err != nil {
		// 設定ファイルが読めないのはConfigServiceと同じくFailedPrecondition（契約「エラーコードの約束」）。
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	local, err := config.LoadLocal(repoRoot)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	store := secrets.New(b.dataDir)
	p := &bootPlan{
		image:          image,
		egress:         config.AllowedEgress(cfg, local),
		plaintext:      map[string]string{},
		envFiles:       cfg.EnvFiles,
		vars:           local.Vars,
		checks:         cfg.Checks,
		claudeSettings: cfg.ClaudeSettings,
	}
	if p.image == "" {
		p.image = cfg.ImageEntry()
	}
	p.diskMiB = cfg.DiskMiB(p.image)
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if p.stallAfter, err = local.StallAfterDuration(); err != nil {
		add("%s: %v", config.SettingsLocalPath(repoRoot), err)
	}

	if !config.ValidCheckName(p.image) {
		add("image %q is not a valid entry name", p.image)
	} else {
		if st, err := os.Stat(filepath.Join(defsDir, "images", p.image, "Dockerfile")); err != nil || !st.Mode().IsRegular() {
			add("image %s: .masuda/images/%s/Dockerfile is missing (masuda init writes a template)", p.image, p.image)
		}
	}

	token, ok, err := store.ClaudeToken(repoRoot, local.ClaudeTokenName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// フェイクはAPIへ出ないので、トークンが無くても動かせる（契約テストが値を用意しない）。
	if !ok && !b.fake {
		add("the Claude API token %s has no value (masuda secret set %s)", local.ClaudeTokenName(), local.ClaudeTokenName())
	}
	p.secrets = append(p.secrets, &sandboxv1.SecretDecl{
		Name:              guest.TokenEnv,
		Value:             token,
		Hosts:             []string{claudeAPIHost},
		SubstituteIn:      []sandboxv1.SubstituteIn{sandboxv1.SubstituteIn_SUBSTITUTE_IN_HEADER},
		PlaceholderPrefix: claudeTokenPrefix,
	})

	for _, d := range cfg.Secrets {
		value, ok, err := store.Get(repoRoot, d.Name)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if d.EffectiveMode() == config.ModePlaintext {
			if !contains(local.SecretsApproved, d.Name) {
				add("secret %s is plaintext (its real value goes into the guest) and is not approved: run masuda secret approve %s", d.Name, d.Name)
			}
			if !ok {
				add("secret %s has no value (masuda secret set %s)", d.Name, d.Name)
			}
			p.plaintext[d.Name] = value
			continue
		}
		if !ok {
			add("secret %s has no value (masuda secret set %s)", d.Name, d.Name)
		}
		decl := &sandboxv1.SecretDecl{Name: d.Name, Value: value, Hosts: d.Hosts}
		for _, in := range d.EffectiveIn() {
			if in == config.InBody {
				decl.SubstituteIn = append(decl.SubstituteIn, sandboxv1.SubstituteIn_SUBSTITUTE_IN_BODY)
			} else {
				decl.SubstituteIn = append(decl.SubstituteIn, sandboxv1.SubstituteIn_SUBSTITUTE_IN_HEADER)
			}
		}
		p.secrets = append(p.secrets, decl)
		p.placeholderNames = append(p.placeholderNames, d.Name)
	}

	for _, f := range cfg.EnvFiles {
		for _, v := range f.Vars {
			if _, isSecret := cfg.Secret(v); isSecret {
				continue
			}
			if _, ok := local.Vars[v]; !ok {
				add("envFiles %s: %s is not a declared secret and has no value in vars of .masuda/settings.local.json", f.Path, v)
			}
		}
	}

	missing, err := missingChecks(set, workflow, cfg.Checks)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	for _, name := range missing {
		add("the workflow runs %s/%s but checks.%s is not declared in .masuda/settings.json", guest.ChecksDir, name, name)
	}

	if len(problems) > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(strings.Join(problems, "; ")))
	}
	return p, nil
}

// missingChecks はworkflowから届くexecノードが`/masuda/checks/<名前>`で呼ぶチェックのうち、
// checksに宣言されていない名前を返す。engineの検査（Set.Check）はチェックの宣言を知らないので
// ここで確かめる。実行してから「ファイルが無い」で失敗させると、エージェントへの差し戻しになって
// 宣言の不足が人間に届かないため。
func missingChecks(set *engine.Set, workflow string, checks map[string]string) ([]string, error) {
	refs, err := set.Reachable(workflow)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, ref := range refs {
		wf := set.Workflows[ref]
		if wf == nil {
			continue
		}
		for _, n := range wf.Nodes {
			if n.Type != engine.NodeExec || len(n.Command) == 0 {
				continue
			}
			name, ok := strings.CutPrefix(n.Command[0], guest.ChecksDir+"/")
			if !ok || seen[name] {
				continue
			}
			seen[name] = true
			if _, declared := checks[name]; !declared {
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// guestEnvFiles はplanのenvFilesを、作ったsandboxのプレースホルダで値を埋めたゲスト向けの形にする。
func (p *bootPlan) guestEnvFiles(placeholders map[string]string) []guest.EnvFile {
	var out []guest.EnvFile
	for _, f := range p.envFiles {
		gf := guest.EnvFile{Path: f.Path}
		for _, v := range f.Vars {
			value, ok := placeholders[v]
			if !ok {
				value, ok = p.plaintext[v]
			}
			if !ok {
				value = p.vars[v]
			}
			gf.Vars = append(gf.Vars, guest.EnvVar{Name: v, Value: value})
		}
		out = append(out, gf)
	}
	return out
}

// guestEnv はゲストのコマンド（メインセッションとexecノード）へ渡す環境変数。宣言した秘密の
// プレースホルダ（Claude APIのトークンを除く）と、guest.BaseEnv（PATH・XDGのディレクトリ）。
func (p *bootPlan) guestEnv(placeholders map[string]string) map[string]string {
	env := guest.BaseEnv()
	for _, name := range p.placeholderNames {
		if v, ok := placeholders[name]; ok {
			env[name] = v
		}
	}
	return env
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
