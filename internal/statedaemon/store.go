// Package statedaemon implements the generic key-value store that backs
// masuda's per-workspace RPC daemon (Issue #35): a workspace's masuda-owned
// state (gate markers, plan artifacts, orchestrator bookkeeping -- the full
// /masuda-state surface, see Issue #29's inventory) as key/value pairs,
// persisted to disk and observable via a blocking WaitForPresence.
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
	"bytes"
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

// keyOf validates key's shape without needing a Store, for the ops
// validation Apply does before it takes the lock.
func keyOf(key string) (string, error) {
	ns, rest, ok := strings.Cut(key, ":")
	if !ok || ns == "" || rest == "" {
		return "", fmt.Errorf("invalid key %q: want \"<namespace>:<rest>\"", key)
	}
	clean := filepath.Clean(rest)
	if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("invalid key %q: rest must be a relative path", key)
	}
	return filepath.Join(ns, clean), nil
}

// keyPath maps key to its on-disk path under dir.
func (s *Store) keyPath(key string) (string, error) {
	rel, err := keyOf(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.dir, rel), nil
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
// waiting on key so they can re-evaluate what they are waiting for.
func (s *Store) Put(key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putLocked(key, value)
}

// Delete removes key. Deleting a key that doesn't exist is not an error
// (mirrors internal/gate.clearDeviation's idempotent-remove pattern).
func (s *Store) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteLocked(key)
}

// putLocked/deleteLocked carry the actual write so Apply can perform several
// of them without ever dropping s.mu. Callers must hold s.mu.
func (s *Store) putLocked(key string, value []byte) error {
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
	s.values[key] = value
	s.notify(key)
	return nil
}

func (s *Store) deleteLocked(key string) error {
	path, err := s.keyPath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(s.values, key)
	s.notify(key)
	return nil
}

// OpKind is what one Op in an Apply does. The values match the wire form the
// state_apply MCP tool takes, so the server side needs no translation table.
type OpKind string

const (
	OpCheck  OpKind = "check"
	OpPut    OpKind = "put"
	OpDelete OpKind = "delete"
)

// Op is one step of an Apply.
type Op struct {
	Kind OpKind
	Key  string
	// Value is OpPut's new value and OpCheck's expected current value; for
	// OpCheck a nil Value means "this key must not exist". OpDelete ignores
	// it.
	Value []byte
}

// Apply evaluates every OpCheck and then, only if all of them hold, performs
// every OpPut and OpDelete -- all of it under a single hold of s.mu, so no
// other caller can interleave a write between the check and the writes it
// guards. A failed check changes nothing and reports applied=false rather
// than an error: losing the race is an ordinary outcome for the caller
// (re-read and decide again), not a malfunction.
//
// This is what lets a consumer take a decision off a key and record what it
// did with it as one indivisible step. Doing that as separate Get/Put/Delete
// calls leaves two holes that were both reachable in practice: a decision
// arriving between the read and the delete is destroyed unseen, and a crash
// between the delete and the follow-up write loses the decision entirely
// (Issue #42's analysis, Issue #44).
//
// Puts land before deletes, deliberately. The delete of a marker key is the
// consumer's acknowledgement that it has taken responsibility for a
// decision, so it has to come after the record that discharges it: this
// ordering is what makes a crash mid-Apply fail towards redelivering the
// decision rather than dropping it. That is also the honest bound on this
// method -- it is atomic against other callers of this Store, not against
// the process dying, since each op is its own file write. Closing that
// remaining window would take a write-ahead journal.
func (s *Store) Apply(ops []Op) (applied bool, err error) {
	if err := validateOps(ops); err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, op := range ops {
		if op.Kind != OpCheck {
			continue
		}
		current, exists := s.values[op.Key]
		if op.Value == nil {
			if exists {
				return false, nil
			}
			continue
		}
		if !exists || !bytes.Equal(current, op.Value) {
			return false, nil
		}
	}

	for _, op := range ops {
		if op.Kind == OpPut {
			if err := s.putLocked(op.Key, op.Value); err != nil {
				return false, err
			}
		}
	}
	for _, op := range ops {
		if op.Kind == OpDelete {
			if err := s.deleteLocked(op.Key); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

// validateOps rejects an Apply that could not be given one unambiguous
// meaning. Two ops writing the same key is the interesting case: the caller
// clearly meant something, but nothing here can tell what, and picking an
// order silently would be worse than refusing. An OpCheck sharing a key with
// a write is the normal shape (guard the key you are about to consume) and
// stays allowed.
func validateOps(ops []Op) error {
	written := make(map[string]bool, len(ops))
	for _, op := range ops {
		switch op.Kind {
		case OpCheck:
			if _, err := keyOf(op.Key); err != nil {
				return err
			}
			continue
		case OpPut, OpDelete:
		default:
			return fmt.Errorf("invalid op kind %q for key %q", op.Kind, op.Key)
		}
		if _, err := keyOf(op.Key); err != nil {
			return err
		}
		if written[op.Key] {
			return fmt.Errorf("key %q is written twice in one Apply", op.Key)
		}
		written[op.Key] = true
	}
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

// notify wakes every goroutine waiting on key so it can look at the store
// again, and clears the waiter list. Callers must hold s.mu.
//
// Delete notifies too, even though no waiter can be satisfied by a key going
// away: the contract is "key changed, look again" rather than "your wait is
// over", which keeps the waiter loop correct for any condition instead of
// only for the presence one WaitForPresence happens to ask about today.
func (s *Store) notify(key string) {
	for _, ch := range s.waiters[key] {
		close(ch)
	}
	delete(s.waiters, key)
}

// removeWaiter drops ch from key's waiter list, e.g. after its
// WaitForPresence call was cancelled before notify() fired. A no-op if
// notify already removed it first (the two can race harmlessly).
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

// WaitForPresence blocks until key exists, then returns its value. A key
// that already exists returns immediately.
//
// This waits on a condition, not on an event, which is the one thing
// ADR-0017's inotifywait could not do: inotify's only observable is the
// event, so "block until something changes" was the only shape available,
// and ADR-0040/0042 carried that shape over to the RPC even though a Store
// can answer "what is it right now". Waiting on an event silently lost every
// change that landed before the caller's wait registered, and deadlocked
// outright on a gate that was already resolved when the call arrived --
// neither of which any timeout can rescue, since the notification is gone
// rather than late (Issue #42).
//
// The condition form is idempotent, which is what makes it survive the
// things this daemon has no supervisor for: retrying after a dropped
// connection, or re-establishing the wait after the daemon restarted, asks
// the same question and gets the same answer.
func (s *Store) WaitForPresence(ctx context.Context, key string) ([]byte, error) {
	for {
		s.mu.Lock()
		if v, ok := s.values[key]; ok {
			s.mu.Unlock()
			return v, nil
		}
		ch := make(chan struct{})
		s.waiters[key] = append(s.waiters[key], ch)
		s.mu.Unlock()

		select {
		case <-ch:
		case <-ctx.Done():
			s.removeWaiter(key, ch)
			return nil, ctx.Err()
		}
	}
}
