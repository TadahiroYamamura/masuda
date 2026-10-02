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

// LocalSettings は`.masuda/settings.local.json`の形。
type LocalSettings struct {
	// EgressAllowlist はConfig.EgressAllowlistのうちユーザーが承認したもの。
	// 両方にあるホストだけが許可される。
	EgressAllowlist    []string                             `json:"egressAllowlist,omitempty"`
	PrivilegedCommands map[string]PrivilegedCommandApproval `json:"privilegedCommands,omitempty"`
}

// PrivilegedCommandApproval は宣言された特権コマンド1つへのユーザーの判断。
type PrivilegedCommandApproval struct {
	Approved bool   `json:"approved"`
	DeclHash string `json:"declHash,omitempty"`
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
	if err := json.Unmarshal(data, &s); err != nil {
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
