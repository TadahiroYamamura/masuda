// Package snapshot fixes a workflow set for the life of one run (ADR-0070).
// It is taken when the run starts, from the host repository's .masuda/
// and the bundled defaults, and kept in the state daemon's store, which
// lives in a host-only directory the sandbox cannot write.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing/fstest"

	"github.com/TadahiroYamamura/masuda/internal/workflow/check"
	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
	"github.com/TadahiroYamamura/masuda/internal/workflow/engine"
)

const (
	keyManifest = "wf:def-manifest"
	prefixFile  = "wf:def/"
)

type manifest struct {
	Root   string            `json:"root"`
	Hashes map[string]string `json:"hashes"`
	// Origins records where each file came from, for `workflow show` and
	// for telling a user which defaults a run used.
	Origins map[string]def.Origin `json:"origins"`
}

// Exists reports whether a run's definitions have been fixed.
func Exists(store engine.Store) bool {
	_, ok := store.Get(keyManifest)
	return ok
}

// Save loads root from src, checks it, and stores every file it reaches.
// A set that fails the load-time checks is refused: a run never starts on
// definitions the engine would reject.
func Save(store engine.Store, src def.Source, root string, opts check.Options) (*def.Set, error) {
	set, errs := def.Load(src, root)
	if len(errs) == 0 {
		errs = check.Run(set, opts)
	}
	if len(errs) != 0 {
		return nil, joinErrors(errs)
	}
	m := manifest{Root: root, Hashes: map[string]string{}, Origins: set.Origins}
	for ref, b := range set.Raw {
		m.Hashes[ref] = hash(b)
		if err := store.Put(prefixFile+ref, b); err != nil {
			return nil, err
		}
	}
	mb, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return set, store.Put(keyManifest, mb)
}

// Load rebuilds the set from the store, verifying each file against the
// hash recorded when it was saved.
func Load(store engine.Store) (*def.Set, error) {
	mb, ok := store.Get(keyManifest)
	if !ok {
		return nil, fmt.Errorf("no workflow definitions have been fixed for this run")
	}
	var m manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		return nil, err
	}
	fsys := fstest.MapFS{}
	for ref, want := range m.Hashes {
		b, ok := store.Get(prefixFile + ref)
		if !ok {
			return nil, fmt.Errorf("snapshot is missing %s", ref)
		}
		if hash(b) != want {
			return nil, fmt.Errorf("snapshot of %s does not match its recorded hash", ref)
		}
		name := ref + ".yaml"
		if strings.HasPrefix(ref, "agents/") {
			name = ref + ".md"
		}
		fsys[name] = &fstest.MapFile{Data: b}
	}
	set, errs := def.Load(def.Source{Bundled: fsys}, m.Root)
	if len(errs) != 0 {
		return nil, joinErrors(errs)
	}
	set.Origins = m.Origins
	return set, nil
}

func hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func joinErrors(errs []*def.Error) error {
	lines := make([]string, len(errs))
	for i, e := range errs {
		lines[i] = e.Error()
	}
	return fmt.Errorf("workflow definitions are invalid:\n  %s", strings.Join(lines, "\n  "))
}
