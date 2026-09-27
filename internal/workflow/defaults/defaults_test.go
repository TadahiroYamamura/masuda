package defaults

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/workflow/check"
	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
)

// Every bundled workflow must load and pass the load-time checks on its
// own, so each can also be run standalone (ADR-0077).
func TestBundledWorkflowsPassChecks(t *testing.T) {
	var paths []string
	err := fs.WalkDir(FS(), "workflows", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".yaml") {
			paths = append(paths, strings.TrimSuffix(p, ".yaml"))
		}
		return err
	})
	if err != nil || len(paths) == 0 {
		t.Fatalf("walk: %v, %d files", err, len(paths))
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			set, errs := def.Load(def.Source{Bundled: FS()}, p)
			errs = append(errs, check.Run(set, check.Options{CheckNames: map[string]bool{"test": true}})...)
			if len(errs) != 0 {
				t.Fatalf("errors: %v", errs)
			}
		})
	}
}

func TestBundledRequiresPlan(t *testing.T) {
	cases := map[string]bool{
		"workflows/develop":              false,
		"workflows/review":               false,
		"workflows/implement/build-step": true,
		"workflows/fix-finding":          true,
	}
	for p, want := range cases {
		set, errs := def.Load(def.Source{Bundled: FS()}, p)
		if len(errs) != 0 {
			t.Fatalf("%s: %v", p, errs)
		}
		if got := check.RequiresPlan(set); got != want {
			t.Errorf("RequiresPlan(%s) = %v, want %v", p, got, want)
		}
	}
}
