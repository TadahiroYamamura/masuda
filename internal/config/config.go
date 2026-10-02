// Package config は対象リポジトリの`.masuda/settings.json`（コミットされる宣言）と
// `.masuda/settings.local.json`（ユーザーごとの承認、gitignore対象）を読む。
//
// settings.jsonは対象リポジトリにコミットされるので無条件には信頼しない。
// 宣言だけでは何も起きず、ローカルの承認と揃ったときだけ効く。承認は宣言のハッシュに
// 結びつけ、宣言が変わったら承認は失効する（DeclHash）。
//
// M1時点では旧設計から写した宣言/承認の形だけを置く。フィールドの再設計
// （egress・secrets・envFiles・checks等）はM6で行う。
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DirName は対象リポジトリのルートに置く設定ディレクトリの名前。
const DirName = ".masuda"

// SettingsFileName はDirName内の宣言ファイルの名前。
const SettingsFileName = "settings.json"

// SettingsPath はrepoRootの宣言ファイルの絶対パスを返す。
func SettingsPath(repoRoot string) string {
	return filepath.Join(repoRoot, DirName, SettingsFileName)
}

// Config は`.masuda/settings.json`の形。どのフィールドも省略できる。
type Config struct {
	Image string `json:"image,omitempty"`
	// ClaudeSettings はClaude Codeへそのまま渡す不透明な値で、masudaは解釈しない。
	ClaudeSettings json.RawMessage `json:"claudeSettings,omitempty"`
	// EgressAllowlist はゲストが届いてよいホストの宣言。ホスト名そのものが識別子で
	// 差し替えられる中身を持たないので、ハッシュには結びつけない。
	EgressAllowlist    []string                         `json:"egressAllowlist,omitempty"`
	PrivilegedCommands map[string]PrivilegedCommandDecl `json:"privilegedCommands,omitempty"`
}

// PrivilegedCommandDecl は特権コマンド1つの宣言。エージェントが渡せるのは名前だけで、
// 実際に動くものは常にこの宣言から来る。
type PrivilegedCommandDecl struct {
	Command        string   `json:"command"`
	Image          string   `json:"image"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
	Outputs        []string `json:"outputs,omitempty"`
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

// ValidateOutputPath は特権コマンドのOutputsの1項目が/workspaceの外を指さないか確かめる。
// 承認時と回収時の両方で同じ判定を使うためここに置く。
func ValidateOutputPath(out string) error {
	if strings.TrimSpace(out) == "" {
		return errors.New(`"outputs" contains an empty path`)
	}
	if filepath.IsAbs(out) {
		return fmt.Errorf(`"outputs" entry %q must be relative to /workspace`, out)
	}
	clean := filepath.Clean(out)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf(`"outputs" entry %q escapes the workspace`, out)
	}
	return nil
}

// Load はrepoRootの宣言ファイルを読む。ファイルが無いのはエラーでなく、ゼロ値を返す。
func Load(repoRoot string) (Config, error) {
	path := SettingsPath(repoRoot)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}
