package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SettingsLocalFileName はDirName内のユーザーごとの承認ファイルの名前。
const SettingsLocalFileName = "settings.local.json"

// SettingsLocalPath はrepoRootの承認ファイルの絶対パスを返す。
func SettingsLocalPath(repoRoot string) string {
	return filepath.Join(repoRoot, DirName, SettingsLocalFileName)
}

// LocalSettings は`.masuda/settings.local.json`の形。利用者ごとの承認と、リポジトリに
// コミットしない値を置く。秘密の値はここに置かない（internal/secrets）。
type LocalSettings struct {
	// EgressApproved はSettings.Egressのうち利用者が承認したもの。両方にあるホストだけが許可される。
	EgressApproved []string `json:"egressApproved,omitempty"`
	// SecretsApproved はplaintextモードの秘密のうち、本物の値をゲストへ置いてよいと承認した名前。
	SecretsApproved []string `json:"secretsApproved,omitempty"`
	// PrivilegedCommandsApproved は特権コマンドの承認。承認した時点の宣言のハッシュを持つ。
	PrivilegedCommandsApproved map[string]PrivilegedCommandApproval `json:"privilegedCommandsApproved,omitempty"`
	// ClaudeToken はClaude APIのトークンとして使う秘密ストアの名前。既定はCLAUDE_CODE_OAUTH_TOKEN。
	// アカウントを使い分けるとき、別名で登録したトークンを選ぶ。
	ClaudeToken string `json:"claudeToken,omitempty"`
	// Vars はenvFilesの公開値（秘密として宣言していない変数）の値。
	Vars map[string]string `json:"vars,omitempty"`
}

// PrivilegedCommandApproval は宣言された特権コマンド1つへの承認。
type PrivilegedCommandApproval struct {
	DeclHash string `json:"declHash"`
}

// ClaudeTokenName はClaudeTokenの既定を埋めた値を返す。
func (l LocalSettings) ClaudeTokenName() string {
	if l.ClaudeToken == "" {
		return ReservedSecret
	}
	return l.ClaudeToken
}

// LoadLocal はrepoRootの承認ファイルを読む。無ければ「まだ何も承認していない」ゼロ値。
func LoadLocal(repoRoot string) (LocalSettings, error) {
	path := SettingsLocalPath(repoRoot)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return LocalSettings{}, nil
	}
	if err != nil {
		return LocalSettings{}, err
	}
	var s LocalSettings
	if err := decodeStrict(data, &s); err != nil {
		return LocalSettings{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return s, nil
}

// SaveLocal は承認ファイルを0600で原子的に書く。同じディレクトリの一時ファイルから
// renameするので、途中で落ちても切り詰められたファイルは残らず、2つの端末から
// 同時に承認しても内容が混ざらない。
func SaveLocal(repoRoot string, settings LocalSettings) error {
	dir := filepath.Join(repoRoot, DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+SettingsLocalFileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, SettingsLocalPath(repoRoot)); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
