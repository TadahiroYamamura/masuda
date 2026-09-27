package snapshot

import (
	"strings"
	"testing"

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
	_ = store.Put(prefixFile+"workflows/develop", []byte("version: 1\nstart: x\nnodes:\n  x:\n    type: discard\n    next: end\n"))
	if _, err := Load(store); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered snapshot: err = %v", err)
	}
}

func TestSaveRefusesInvalidDefinitions(t *testing.T) {
	if _, err := Save(engine.NewMemStore(), def.Source{Bundled: defaults.FS()}, "workflows/missing", check.Options{}); err == nil {
		t.Fatal("Save accepted a missing workflow")
	}
}
