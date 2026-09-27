package snapshot

import (
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/workflow/check"
	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
	"github.com/TadahiroYamamura/masuda/internal/workflow/defaults"
	"github.com/TadahiroYamamura/masuda/internal/workflow/engine"
)

func TestSaveLoadRoundTripAndTamperDetection(t *testing.T) {
	store := engine.NewMemStore()
	saved, err := Save(store, def.Source{Bundled: defaults.FS()}, "workflows/develop", check.Options{})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Workflows) != len(saved.Workflows) || len(loaded.Agents) != len(saved.Agents) {
		t.Fatalf("loaded %d/%d, saved %d/%d", len(loaded.Workflows), len(loaded.Agents), len(saved.Workflows), len(saved.Agents))
	}
	_ = store.Put(fileKey("workflows/develop"), []byte("version: 1\nstart: x\nnodes:\n  x:\n    type: discard\n    next: end\n"))
	if _, err := Load(store); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered snapshot: err = %v", err)
	}
}

func TestSaveRefusesInvalidDefinitions(t *testing.T) {
	if _, err := Save(engine.NewMemStore(), def.Source{Bundled: defaults.FS()}, "workflows/missing", check.Options{}); err == nil {
		t.Fatal("Save accepted a missing workflow")
	}
}

func TestSaveOnTheDaemonStoreWhenARefIsAlsoADirectory(t *testing.T) {
	// workflows/review and workflows/review/default are both refs. The
	// daemon's store keeps one file per key, so the two must not map to a
	// file and a directory of the same name.
	store, err := statedaemon.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Save(store, def.Source{Bundled: defaults.FS()}, "workflows/review", check.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(store); err != nil {
		t.Fatal(err)
	}
}
