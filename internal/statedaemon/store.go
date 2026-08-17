// Package statedaemon implements the generic key-value store that backs
// masuda's per-workspace RPC daemon (Issue #35): a workspace's masuda-owned
// state (gate markers, plan artifacts, orchestrator bookkeeping -- the full
// /masuda-state surface, see Issue #29's inventory) as key/value pairs,
// persisted to disk and observable via a blocking WaitForChange.
//
// Keys are namespaced strings that mirror today's file layout, e.g.
// "gate:plan" or "artifact:plan/summary.md" -- the namespace before the
// colon is one of gate/artifact/internal (Issue #35's category split) and
// becomes a top-level subdirectory on disk.
//
// This package is transport-agnostic on purpose: no MCP, no vsock, no CLI
// wiring lives here yet (future work under the same issue). One Store
// instance is meant to back one per-workspace daemon process, so there is no
// workspace ID dimension in the API.
package statedaemon

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// socketFileName/curatedSocketFileName are the Unix domain socket files a
// workspace's state daemon listens on, relative to its state directory (see
// internal/workspace) -- the trusted full tool set and the curated,
// Claude-facing tool set (internal/statedaemon/mcpserver.New/NewCurated)
// respectively.
const (
	socketFileName        = "daemon.sock"
	curatedSocketFileName = "daemon-curated.sock"
)

// SocketPath returns the Unix domain socket path a workspace's state
// daemon's trusted (full) tool set listens on, given its state directory.
// Exported so any trusted caller (cmd/masuda's Go code, internal/gate) can
// locate the socket without depending on cmd/masuda (package main,
// unimportable) or duplicating this path convention.
func SocketPath(stateDir string) string {
	return filepath.Join(stateDir, socketFileName)
}

// CuratedSocketPath returns the Unix domain socket path a workspace's state
// daemon's curated, Claude-facing tool set listens on, given its state
// directory. Reachable from inside the sandbox the same way SocketPath is
// (the state directory is bind-mounted at /masuda-state), meant to be
// relayed to Claude's own MCP client over vsock/a local proxy rather than
// dialed directly the way trusted callers dial SocketPath.
func CuratedSocketPath(stateDir string) string {
	return filepath.Join(stateDir, curatedSocketFileName)
}

// Store is a single workspace's key-value state, persisted under dir.
type Store struct {
	dir string

	mu      sync.Mutex
	values  map[string][]byte
	waiters map[string][]chan struct{}
}

// Open loads a Store persisted under dir, creating dir if it doesn't exist
// yet. Every key previously Put is restored from disk.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating store dir: %w", err)
	}
	s := &Store{
		dir:     dir,
		values:  make(map[string][]byte),
		waiters: make(map[string][]chan struct{}),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load walks dir and restores every file that maps onto a namespaced key.
// Files that don't (e.g. left over from a future on-disk format change) are
// skipped rather than failing Open outright.
func (s *Store) load() error {
	return filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.dir, path)
		if err != nil {
			return err
		}
		key, ok := pathToKey(rel)
		if !ok {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s.values[key] = data
		return nil
	})
}

// keyPath maps key to its on-disk path under dir.
func (s *Store) keyPath(key string) (string, error) {
	ns, rest, ok := strings.Cut(key, ":")
	if !ok || ns == "" || rest == "" {
		return "", fmt.Errorf("invalid key %q: want \"<namespace>:<rest>\"", key)
	}
	clean := filepath.Clean(rest)
	if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("invalid key %q: rest must be a relative path", key)
	}
	return filepath.Join(s.dir, ns, clean), nil
}

// pathToKey is keyPath's inverse, used by load(). rel is a path relative to
// the store's dir, as produced by filepath.WalkDir.
func pathToKey(rel string) (string, bool) {
	parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + ":" + parts[1], true
}

// Get returns the current value for key and whether it exists.
func (s *Store) Get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	return v, ok
}

// Put sets key's value, persists it to disk, and wakes any goroutines
// currently blocked in WaitForChange(key).
func (s *Store) Put(key string, value []byte) error {
	path, err := s.keyPath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, value, 0o644); err != nil {
		return err
	}

	s.mu.Lock()
	s.values[key] = value
	s.notify(key)
	s.mu.Unlock()
	return nil
}

// Delete removes key. Deleting a key that doesn't exist is not an error
// (mirrors internal/gate.clearDeviation's idempotent-remove pattern).
func (s *Store) Delete(key string) error {
	path, err := s.keyPath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}

	s.mu.Lock()
	delete(s.values, key)
	s.notify(key)
	s.mu.Unlock()
	return nil
}

// List returns every key currently starting with prefix, sorted.
func (s *Store) List(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.values {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// notify wakes every goroutine currently blocked in WaitForChange(key) and
// clears the waiter list. Callers must hold s.mu.
func (s *Store) notify(key string) {
	for _, ch := range s.waiters[key] {
		close(ch)
	}
	delete(s.waiters, key)
}

// removeWaiter drops ch from key's waiter list, e.g. after its
// WaitForChange call was cancelled before notify() fired. A no-op if notify
// already removed it first (the two can race harmlessly).
func (s *Store) removeWaiter(key string, ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiters := s.waiters[key]
	for i, w := range waiters {
		if w == ch {
			s.waiters[key] = append(waiters[:i], waiters[i+1:]...)
			break
		}
	}
	if len(s.waiters[key]) == 0 {
		delete(s.waiters, key)
	}
}

// WaitForChange blocks until the next Put or Delete call on key (relative to
// when WaitForChange itself was called), then returns the new value (ok is
// false if the key was deleted). This is the RPC-callable equivalent of
// ADR-0017's inotifywait single blocking call: one call per wait, no looping
// inside the caller.
func (s *Store) WaitForChange(ctx context.Context, key string) (value []byte, ok bool, err error) {
	s.mu.Lock()
	ch := make(chan struct{})
	s.waiters[key] = append(s.waiters[key], ch)
	s.mu.Unlock()

	select {
	case <-ch:
	case <-ctx.Done():
		s.removeWaiter(key, ch)
		return nil, false, ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	return v, ok, nil
}
