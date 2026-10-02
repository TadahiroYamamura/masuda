package perspectives

import (
	"io/fs"
	"testing"
)

func TestBuiltinHasFourteenPerspectives(t *testing.T) {
	entries, err := fs.ReadDir(Builtin(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 14 {
		t.Fatalf("builtin perspectives = %d, want 14", len(entries))
	}
}
