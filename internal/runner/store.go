package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/TadahiroYamamura/masuda-engine/engine"
)

// FileStore はengine.Storeをワークスペースの1ファイル（`records/engine.json`）で実装する。
// 1ワークスペースの記録は高々数万件の小さな値なので、全体をメモリに持ち、変更のたびに
// 一時ファイルからrenameで丸ごと書き直す。キーごとのファイルにしなかったのは、Applyの
// 原子性（checkが通ったときだけ全部書く）を1回のrenameで保てるため。
type FileStore struct {
	mu   sync.Mutex
	path string
	m    map[string][]byte
}

// OpenFileStore はpathの記録を読み込む。無ければ空で始める。
func OpenFileStore(path string) (*FileStore, error) {
	s := &FileStore{path: path, m: map[string][]byte{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	} else if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.m); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FileStore) Get(key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	return append([]byte(nil), v...), ok, nil
}

func (s *FileStore) Put(key string, value []byte) error {
	return s.apply([]engine.Op{{Kind: engine.OpPut, Key: key, Value: value}})
}

func (s *FileStore) Delete(key string) error {
	return s.apply([]engine.Op{{Kind: engine.OpDelete, Key: key}})
}

func (s *FileStore) List(prefix string) ([]engine.KV, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []engine.KV
	for k, v := range s.m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, engine.KV{Key: k, Value: append([]byte(nil), v...)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *FileStore) Apply(ops []engine.Op) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, op := range ops {
		if op.Kind != engine.OpCheck {
			continue
		}
		cur, ok := s.m[op.Key]
		if op.Value == nil {
			if ok {
				return false, nil
			}
		} else if !ok || string(cur) != string(op.Value) {
			return false, nil
		}
	}
	return true, s.applyLocked(ops)
}

func (s *FileStore) apply(ops []engine.Op) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(ops)
}

// applyLocked は書き込みを新しいmapに作ってからファイルへ書き、成功したら差し替える。
// 書き込みに失敗したときにメモリとファイルが食い違わないようにするため。
func (s *FileStore) applyLocked(ops []engine.Op) error {
	next := make(map[string][]byte, len(s.m)+len(ops))
	for k, v := range s.m {
		next[k] = v
	}
	changed := false
	for _, op := range ops {
		switch op.Kind {
		case engine.OpPut:
			next[op.Key] = append([]byte(nil), op.Value...)
			changed = true
		case engine.OpDelete:
			delete(next, op.Key)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.path, b); err != nil {
		return err
	}
	s.m = next
	return nil
}

func writeFileAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
