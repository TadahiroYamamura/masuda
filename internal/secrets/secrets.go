// Package secrets は秘密の値をホストに置く（`<DataDir>/secrets/<repo-hash>/<NAME>`、0600）。
// リポジトリに依らないユーザー単位の値（Claudeのトークン）は`<DataDir>/secrets/_user/<NAME>`に置く。
//
// 値は対象リポジトリの外、利用者ごとのデータディレクトリに置くので、チームメイトごとに
// 別の値を持て、リポジトリへ漏れない。値を書く口と、sandboxへ渡すために読む口だけがあり、
// 公開APIから値を読み戻す口は作らない。
package secrets

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

// legacyTokenFile はM4の暫定のClaude APIトークンの置き場所（DataDirの直下）。
// 秘密ストアに無いときだけ読む。
const legacyTokenFile = "claude-oauth-token"

// Store はDataDirの下の秘密ストア。
type Store struct{ dataDir string }

// New はdataDirの秘密ストアを返す。
func New(dataDir string) *Store { return &Store{dataDir: dataDir} }

// RepoHash はrepoRoot（絶対パス）のsha256の先頭16桁。ディレクトリ名をリポジトリのパスから
// 決めるのは、リポジトリに識別子を書き込まずに済ませるため（移動すると値は引き継がれない）。
func RepoHash(repoRoot string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(repoRoot)))
	return hex.EncodeToString(sum[:])[:16]
}

// UserScope はユーザー単位の秘密を置くディレクトリの名前。RepoHashは16桁の16進数なので衝突しない。
const UserScope = "_user"

// Dir はrepoRootの秘密を置くディレクトリ。repoRootが空ならユーザー単位の置き場所。
func (s *Store) Dir(repoRoot string) string {
	if repoRoot == "" {
		return filepath.Join(s.dataDir, "secrets", UserScope)
	}
	return filepath.Join(s.dataDir, "secrets", RepoHash(repoRoot))
}

func (s *Store) path(repoRoot, name string) (string, error) {
	if !config.ValidName(name) {
		return "", fmt.Errorf("invalid secret name %q", name)
	}
	return filepath.Join(s.Dir(repoRoot), name), nil
}

// Set はnameの値を0600で原子的に書く。ディレクトリは0700。
func (s *Store) Set(repoRoot, name, value string) error {
	p, err := s.path(repoRoot, name)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// 既にあるディレクトリが緩い権限で作られていても締め直す。
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(value); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, p)
}

// Get はnameの値を返す。無ければok=false。
func (s *Store) Get(repoRoot, name string) (value string, ok bool, err error) {
	p, err := s.path(repoRoot, name)
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

// Has はnameの値が置かれているかを返す。
func (s *Store) Has(repoRoot, name string) bool {
	_, ok, err := s.Get(repoRoot, name)
	return ok && err == nil
}

// ClaudeToken はClaude APIのトークンを返す。name（settings.local.jsonのclaudeToken、既定は
// CLAUDE_CODE_OAUTH_TOKEN）を、リポジトリの置き場所、ユーザー単位の置き場所の順に読み、
// どちらにも無ければM4の暫定ファイルを読む。トークンはアカウントに付くものでリポジトリごとに
// 登録し直す理由が無いので、ユーザー単位を既定の置き場所にし、リポジトリごとの登録は上書きに使う。
// 前後の空白・改行は落とす（`echo ... | masuda secret set`で入る改行がヘッダーを壊すため）。
func (s *Store) ClaudeToken(repoRoot, name string) (string, bool, error) {
	v, ok, err := s.Get(repoRoot, name)
	if err != nil {
		return "", false, err
	}
	if !ok && repoRoot != "" {
		if v, ok, err = s.Get("", name); err != nil {
			return "", false, err
		}
	}
	if !ok {
		b, err := os.ReadFile(filepath.Join(s.dataDir, legacyTokenFile))
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		v = string(b)
	}
	v = strings.TrimSpace(v)
	return v, v != "", nil
}
