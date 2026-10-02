// Package workspace は、ワークスペースのホスト側ディレクトリ
// （`<DataDir>/workspaces/<id>/{staging.git,data,records,exports}`）と、その一覧・削除を扱う。
// stagingの中身はinternal/staging、サンドボックスとエンジンはserveが受け持つ。
package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// ErrNotFound はそのIDのワークスペースが無いことを表す。
var ErrNotFound = errors.New("workspace not found")

// State はワークスペースの状態。公開APIのWorkspaceStateと1対1に対応する。
type State string

const (
	StateStarting        State = "starting"
	StateRunning         State = "running"
	StateWaitingGate     State = "waiting-gate"
	StateWaitingQuestion State = "waiting-question"
	StateStopped         State = "stopped"
	StateDone            State = "done"
	StateBlocked         State = "blocked"
)

// Meta はワークスペースについて永続化するもの（`<id>/workspace.json`）。
type Meta struct {
	ID         string `json:"id"`
	RepoRoot   string `json:"repoRoot"`
	Branch     string `json:"branch"`
	Base       string `json:"base"`       // 分岐元の名前（"main"等）
	BaseCommit string `json:"baseCommit"` // refs/masuda/baseが指すコミット
	Workflow   string `json:"workflow"`
	Image      string `json:"image,omitempty"`
	State      State  `json:"state"`
	Outcome    string `json:"outcome,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Position はengineの現在位置の人間向けの表記（"agent planner (occ 0042)"）。
	Position  string    `json:"position,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Workspace は1つのワークスペースのディレクトリとMeta。
type Workspace struct {
	Meta
	Dir string
}

func (w *Workspace) StagingDir() string { return filepath.Join(w.Dir, "staging.git") }
func (w *Workspace) DataDir() string    { return filepath.Join(w.Dir, "data") }
func (w *Workspace) RecordsDir() string { return filepath.Join(w.Dir, "records") }
func (w *Workspace) ExportsDir() string { return filepath.Join(w.Dir, "exports") }

// DefinitionsDir は実行開始時に写した対象リポジトリの`.masuda/`。再開やserveの再起動の後も、
// 実行中に作業ツリーの定義が書き換わっていても、始めたときと同じ定義でengineを組み直すため。
func (w *Workspace) DefinitionsDir() string { return filepath.Join(w.RecordsDir(), "definitions") }

func (w *Workspace) metaPath() string { return filepath.Join(w.Dir, "workspace.json") }

// Save はMetaを書き戻す。途中で落ちても壊れたJSONが残らないよう、一時ファイルからrenameする。
func (w *Workspace) Save() error {
	b, err := json.MarshalIndent(w.Meta, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(w.Dir, ".workspace.json-")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), w.metaPath())
}

// Store は`<DataDir>/workspaces`以下のワークスペースの集まり。
type Store struct {
	root string
}

// NewStore はdataDir（serveのDataDir）の下のワークスペース置き場を返す。
func NewStore(dataDir string) *Store {
	return &Store{root: filepath.Join(dataDir, "workspaces")}
}

// idPattern はNewIDが作る形。APIから来たIDをパスに使う前にこれで検査し、
// `../`等でワークスペース置き場の外を指せないようにする。
var idPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// ValidID はidがワークスペースIDの形をしているかを返す。
func ValidID(id string) bool { return idPattern.MatchString(id) }

// NewID は新しいワークスペースIDを作る。CLIで打ちやすい短さと、1台のホストで
// 衝突しない程度の長さ（48bit）の兼ね合いで12桁の16進にしている。
func NewID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Create は新しいIDでディレクトリを作り、data・records・exportsを用意してMetaを保存する。
// staging.gitは作らない（internal/staging.Createが作る）。
func (s *Store) Create(m Meta) (*Workspace, error) {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, err
	}
	var w *Workspace
	for range 5 {
		id, err := NewID()
		if err != nil {
			return nil, err
		}
		dir := filepath.Join(s.root, id)
		// MkdirAllでなくMkdirにして、既存のIDとの衝突を検出する。
		if err := os.Mkdir(dir, 0o700); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		m.ID = id
		w = &Workspace{Meta: m, Dir: dir}
		break
	}
	if w == nil {
		return nil, errors.New("could not allocate a workspace id")
	}
	for _, d := range []string{w.DataDir(), w.RecordsDir(), w.ExportsDir()} {
		if err := os.Mkdir(d, 0o700); err != nil {
			os.RemoveAll(w.Dir)
			return nil, err
		}
	}
	now := time.Now().UTC()
	if w.CreatedAt.IsZero() {
		w.CreatedAt = now
	}
	w.UpdatedAt = now
	if err := w.Save(); err != nil {
		os.RemoveAll(w.Dir)
		return nil, err
	}
	return w, nil
}

// Get はidのワークスペースを返す。無ければErrNotFound。
func (s *Store) Get(id string) (*Workspace, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("%q: %w", id, ErrNotFound)
	}
	w := &Workspace{Dir: filepath.Join(s.root, id)}
	b, err := os.ReadFile(w.metaPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%q: %w", id, ErrNotFound)
	} else if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &w.Meta); err != nil {
		return nil, fmt.Errorf("workspace %s: %w", id, err)
	}
	w.ID = id
	return w, nil
}

// List はすべてのワークスペースを作成順に返す。repoRootが空でなければそのリポジトリの分だけ。
// Metaの読めないディレクトリ（作成途中・壊れたもの）は飛ばす。
func (s *Store) List(repoRoot string) ([]*Workspace, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if repoRoot != "" {
		repoRoot = filepath.Clean(repoRoot)
	}
	var out []*Workspace
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		w, err := s.Get(e.Name())
		if err != nil {
			continue
		}
		if repoRoot != "" && filepath.Clean(w.RepoRoot) != repoRoot {
			continue
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Remove はワークスペースのディレクトリをまるごと消す。Metaが壊れていても消せるよう、
// ディレクトリの有無だけを見る。サンドボックスの停止は呼び出し側の責任。
func (s *Store) Remove(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("%q: %w", id, ErrNotFound)
	}
	dir := filepath.Join(s.root, id)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%q: %w", id, ErrNotFound)
	}
	return os.RemoveAll(dir)
}

// RemoveKeepExports はワークスペースのディレクトリのうち`exports/`以外を消す。
// workspace.jsonが無くなるのでGet・Listからは見えなくなり、exportsだけが
// `<DataDir>/workspaces/<id>/exports/`に残る。
func (s *Store) RemoveKeepExports(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("%q: %w", id, ErrNotFound)
	}
	dir := filepath.Join(s.root, id)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%q: %w", id, ErrNotFound)
	} else if err != nil {
		return err
	}
	// workspace.jsonを最初に消し、途中で失敗しても一覧に半端な状態で現れないようにする。
	if err := os.Remove(filepath.Join(dir, "workspace.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if e.Name() == "exports" || e.Name() == "workspace.json" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
