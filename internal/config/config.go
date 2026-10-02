// Package config は対象リポジトリの`.masuda/settings.json`（コミットされる宣言）と
// `.masuda/settings.local.json`（ユーザーごとの承認、gitignore対象）を読む。
//
// settings.jsonは対象リポジトリにコミットされるので無条件には信頼しない。
// 宣言だけでは何も起きず、ローカルの承認と揃ったときだけ効く（egress・plaintextの秘密・
// 特権コマンド）。特権コマンドの承認は宣言のハッシュに結びつけ、宣言が変わったら失効する（DeclHash）。
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// DirName は対象リポジトリのルートに置く設定ディレクトリの名前。
const DirName = ".masuda"

// SettingsFileName はDirName内の宣言ファイルの名前。
const SettingsFileName = "settings.json"

// DefaultImage はimageを省略したときのイメージのエントリ（`.masuda/images/<entry>/`）。
const DefaultImage = "default"

// SettingsPath はrepoRootの宣言ファイルの絶対パスを返す。
func SettingsPath(repoRoot string) string {
	return filepath.Join(repoRoot, DirName, SettingsFileName)
}

// 秘密の置換モード。
const (
	ModePlaceholder = "placeholder"
	ModePlaintext   = "plaintext"
)

// 秘密の置換場所。
const (
	InHeader = "header"
	InBody   = "body"
)

// Settings は`.masuda/settings.json`の形。どのフィールドも省略できる。
type Settings struct {
	// Image はゲストのイメージのエントリ（`.masuda/images/<Image>/Dockerfile`）。既定はDefaultImage。
	Image string `json:"image,omitempty"`
	// Egress はゲストが届いてよいホストの宣言。承認（LocalSettings.EgressApproved）との積が上限になる。
	Egress  []string     `json:"egress,omitempty"`
	Secrets []SecretDecl `json:"secrets,omitempty"`
	// EnvFiles はゲストの作業ツリーに生成するdotenv形式のファイル。
	EnvFiles           []EnvFile                        `json:"envFiles,omitempty"`
	PrivilegedCommands map[string]PrivilegedCommandDecl `json:"privilegedCommands,omitempty"`
	// Checks はチェック名→シェルコマンド。ゲストの`/masuda/checks/<名前>`に実行可能スクリプトとして置く。
	Checks map[string]string `json:"checks,omitempty"`
	// ClaudeSettings はゲストの`~/.claude/settings.json`へ合成するオブジェクト（フックはmasudaが優先）。
	ClaudeSettings json.RawMessage `json:"claudeSettings,omitempty"`
	// Images はイメージのエントリ（`.masuda/images/<entry>/`）ごとのVMの設定。書かなかった
	// エントリは既定値で動く。
	Images map[string]ImageDecl `json:"images,omitempty"`
}

// DefaultDiskMiB はImageDecl.DiskMiBを省略したときのVMのルートディスクの最小容量。
// sandboxの既定（イメージの中身に数百MiBを足すだけ）では、Goのビルドキャッシュや
// テストの生成物で`No space left on device`になった（M8）ため、masuda側で大きめに決める。
const DefaultDiskMiB = 4096

// ImageDecl はイメージのエントリ1つのVMの設定。
type ImageDecl struct {
	// DiskMiB はVMの書き込めるルートディスクの最小容量（MiB）。0なら既定（DefaultDiskMiB）。
	DiskMiB uint32 `json:"diskMiB,omitempty"`
}

// SecretDecl は秘密1つの宣言。値は宣言に書かず、秘密ストア（internal/secrets）に置く。
type SecretDecl struct {
	Name string `json:"name"`
	// Hosts は本物の値を送ってよいホスト（placeholderモードの置換先）。
	Hosts []string `json:"hosts,omitempty"`
	// Mode はplaceholder（既定）かplaintext。plaintextは本物の値をゲストの環境変数に置くので、
	// ローカルの承認（LocalSettings.SecretsApproved）が要る。
	Mode string `json:"mode,omitempty"`
	// In はプレースホルダを置換する場所。既定はheaderだけ。
	In []string `json:"in,omitempty"`
}

// EffectiveMode はModeの既定を埋めた値を返す。
func (d SecretDecl) EffectiveMode() string {
	if d.Mode == "" {
		return ModePlaceholder
	}
	return d.Mode
}

// EffectiveIn はInの既定を埋めた値を返す。
func (d SecretDecl) EffectiveIn() []string {
	if len(d.In) == 0 {
		return []string{InHeader}
	}
	return d.In
}

// EnvFile はゲストの作業ツリーに生成するファイル1つ。Varsのうち秘密として宣言した名前は
// プレースホルダ（plaintextなら本物の値）、それ以外は公開値（LocalSettings.Vars）を書く。
type EnvFile struct {
	Path string   `json:"path"`
	Vars []string `json:"vars"`
}

// PrivilegedCommandDecl は特権コマンド1つの宣言。エージェントが渡せるのは名前だけで、
// 実際に動くものは常にこの宣言から来る。宣言の検証と実行はinternal/privileged。
type PrivilegedCommandDecl struct {
	Command        string   `json:"command"`
	Image          string   `json:"image"`
	Inputs         []string `json:"inputs,omitempty"`
	Outputs        []string `json:"outputs,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
}

// PinnedDecl は承認をハッシュで結びつける宣言の型。anyでなく閉じた型集合にしているのは、
// たまたまJSONにできる無関係な値をDeclHashが黙って受け付けないようにするため。
type PinnedDecl interface {
	PrivilegedCommandDecl
}

// DeclHash は宣言の正準JSONのsha256を返す。承認と一緒に記録し、一致しなければ
// 未承認と同じに扱う。名前だけで承認すると、承認済みの宣言の中身をコミットで
// 差し替えられてしまう。
func DeclHash[T PinnedDecl](decl T) (string, error) {
	data, err := json.Marshal(decl)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Load はrepoRootの宣言ファイルを読む。ファイルが無いのはエラーでなく、ゼロ値を返す。
func Load(repoRoot string) (Settings, error) {
	return LoadDir(filepath.Join(repoRoot, DirName))
}

// LoadDir は`.masuda/`に当たるディレクトリdir（実行ごとの写しでもよい）から宣言を読んで検査する。
func LoadDir(dir string) (Settings, error) {
	p := filepath.Join(dir, SettingsFileName)
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	if err := decodeStrict(data, &s); err != nil {
		return Settings{}, fmt.Errorf("parsing %s: %w", p, err)
	}
	if err := s.Validate(); err != nil {
		return Settings{}, fmt.Errorf("%s: %w", p, err)
	}
	return s, nil
}

// decodeStrict は知らないフィールドを拒否して読む。旧設計の`egressAllowlist`のような
// 綴りの違いを黙って無視すると、宣言したつもりの設定が効かないことに気づけないため。
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

var (
	// envNameRe は秘密・envFilesの変数名。ゲストで環境変数名になる。
	envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// checkNameRe はチェック名。`/masuda/checks/<名前>`のファイル名になる。
	checkNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// hostRe はegress・秘密の送り先のホスト。先頭の`*.`だけワイルドカードを許す。
	hostRe = regexp.MustCompile(`^(\*\.)?[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)
)

// ReservedSecret はClaude APIのトークンの名前。masudaが常に宣言するので、settings.jsonでは宣言できない。
const ReservedSecret = "CLAUDE_CODE_OAUTH_TOKEN"

// ValidName は秘密・変数の名前として使えるかを返す。
func ValidName(name string) bool { return envNameRe.MatchString(name) }

// ValidCheckName はチェック名として使えるかを返す。
func ValidCheckName(name string) bool { return checkNameRe.MatchString(name) }

// Validate は宣言の形を検査する。値（秘密ストア）や承認は見ない。
func (s Settings) Validate() error {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if s.Image != "" && !ValidCheckName(s.Image) {
		add("image %q is not a valid entry name", s.Image)
	}
	for _, h := range s.Egress {
		if !hostRe.MatchString(h) {
			add("egress %q is not a host name", h)
		}
	}
	seen := map[string]bool{}
	for _, d := range s.Secrets {
		switch {
		case !ValidName(d.Name):
			add("secret name %q must match %s", d.Name, envNameRe)
		case d.Name == ReservedSecret:
			add("secret %s is reserved for the Claude API token (masuda secret set %s)", d.Name, d.Name)
		case seen[d.Name]:
			add("secret %s is declared twice", d.Name)
		}
		seen[d.Name] = true
		switch d.EffectiveMode() {
		case ModePlaceholder:
			if len(d.Hosts) == 0 {
				add("secret %s: placeholder mode needs hosts", d.Name)
			}
		case ModePlaintext:
		default:
			add("secret %s: mode %q must be %s or %s", d.Name, d.Mode, ModePlaceholder, ModePlaintext)
		}
		for _, h := range d.Hosts {
			if !hostRe.MatchString(h) {
				add("secret %s: host %q is not a host name", d.Name, h)
			}
		}
		for _, in := range d.In {
			if in != InHeader && in != InBody {
				add("secret %s: in %q must be %s or %s", d.Name, in, InHeader, InBody)
			}
		}
	}
	paths := map[string]bool{}
	for _, f := range s.EnvFiles {
		if err := validateEnvPath(f.Path); err != nil {
			add("envFiles: %v", err)
		} else if paths[path.Clean(f.Path)] {
			add("envFiles: %s is declared twice", f.Path)
		}
		paths[path.Clean(f.Path)] = true
		for _, v := range f.Vars {
			if !ValidName(v) {
				add("envFiles %s: var %q must match %s", f.Path, v, envNameRe)
			}
		}
	}
	for name := range s.Checks {
		if !ValidCheckName(name) {
			add("checks: name %q must match %s", name, checkNameRe)
		}
	}
	for name := range s.PrivilegedCommands {
		if !ValidCheckName(name) {
			add("privilegedCommands: name %q must match %s", name, checkNameRe)
		}
	}
	for name := range s.Images {
		if !ValidCheckName(name) {
			add("images: entry %q must match %s", name, checkNameRe)
		}
	}
	if len(s.ClaudeSettings) > 0 {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(s.ClaudeSettings, &obj); err != nil || obj == nil {
			add("claudeSettings must be a JSON object")
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// validateEnvPath はenvFilesのpathが/workspaceの中の相対パスであることを確かめる。
// `.git/`の下は書かせない（ゲストのgitの設定やフックを差し替えられるため）。
func validateEnvPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return errors.New("empty path")
	}
	if path.IsAbs(p) || strings.Contains(p, `\`) {
		return fmt.Errorf("path %q must be relative to /workspace", p)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path %q escapes the workspace", p)
	}
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return fmt.Errorf("path %q is inside .git", p)
	}
	return nil
}

// ImageEntry はイメージのエントリ（省略時はDefaultImage）を返す。
func (s Settings) ImageEntry() string {
	if s.Image == "" {
		return DefaultImage
	}
	return s.Image
}

// DiskMiB はイメージのエントリentryで作るVMのルートディスクの最小容量（MiB）を返す。
func (s Settings) DiskMiB(entry string) uint32 {
	if d := s.Images[entry].DiskMiB; d > 0 {
		return d
	}
	return DefaultDiskMiB
}

// Secret はnameの宣言を返す。
func (s Settings) Secret(name string) (SecretDecl, bool) {
	for _, d := range s.Secrets {
		if d.Name == name {
			return d, true
		}
	}
	return SecretDecl{}, false
}

// AllowedEgress は宣言と承認の積（宣言の順）を返す。ゲストが届くホストの上限になる。
func AllowedEgress(s Settings, l LocalSettings) []string {
	var out []string
	for _, h := range s.Egress {
		if contains(l.EgressApproved, h) && !contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// HostAllowed はhostがallowedのどれかに当たるかを返す。`*.example.com`は`a.example.com`に
// 当たる（`example.com`自身には当たらない）。ノードが宣言に無いワイルドカードを選ぶことはできない。
func HostAllowed(allowed []string, host string) bool {
	for _, a := range allowed {
		if a == host {
			return true
		}
		if suffix, ok := strings.CutPrefix(a, "*"); ok && strings.HasPrefix(suffix, ".") &&
			!strings.HasPrefix(host, "*") && strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
