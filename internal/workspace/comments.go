package workspace

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Comment はstagingのcommit+file+lineに付く注記。エージェントの指摘と人間のメモが同じ形を共有する。
type Comment struct {
	ID       string    `json:"id"`
	Commit   string    `json:"commit"` // 解決済みのコミットハッシュ
	Path     string    `json:"path,omitempty"`
	Line     uint32    `json:"line,omitempty"`
	Author   string    `json:"author"` // "human"、またはエージェントの役割名
	Body     string    `json:"body"`
	Severity string    `json:"severity,omitempty"`
	Time     time.Time `json:"time"`
	// FindingID はfindingsから取り込んだコメントの元の指摘のid。同じゲートを開き直したときに
	// 二重に取り込まないために持つ（公開APIには出さない）。
	FindingID string `json:"findingId,omitempty"`
}

// commentsMu はcomments.jsonlへの追記を直列化する。ワークスペースをまたいで1つで足りる程度の頻度。
var commentsMu sync.Mutex

func (w *Workspace) commentsPath() string { return filepath.Join(w.RecordsDir(), "comments.jsonl") }

// AddComment はコメントを追記し、IDと時刻を埋めたものを返す。
func (w *Workspace) AddComment(c Comment) (Comment, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Comment{}, err
	}
	c.ID = hex.EncodeToString(b[:])
	if c.Time.IsZero() {
		c.Time = time.Now().UTC()
	}
	line, err := json.Marshal(c)
	if err != nil {
		return Comment{}, err
	}
	commentsMu.Lock()
	defer commentsMu.Unlock()
	f, err := os.OpenFile(w.commentsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return Comment{}, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return Comment{}, err
	}
	return c, f.Close()
}

// Comments はcommitに付いたコメントを追記順に返す。commitが空ならすべて。
func (w *Workspace) Comments(commit string) ([]Comment, error) {
	commentsMu.Lock()
	defer commentsMu.Unlock()
	f, err := os.Open(w.commentsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Comment
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var c Comment
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			// 書き込み途中で落ちた末尾行などは読み飛ばす
			continue
		}
		if commit == "" || c.Commit == commit {
			out = append(out, c)
		}
	}
	return out, sc.Err()
}
