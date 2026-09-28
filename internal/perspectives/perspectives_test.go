package perspectives

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"enable true", "---\nname: \"a\"\nenable: true\n---\nbody\n", true},
		{"enable false", "---\nname: \"a\"\nenable: false\n---\nbody\n", false},
		{"enable absent", "---\nname: \"a\"\n---\nbody\n", true},
		{"no frontmatter", "body only\n", true},
		{"frontmatter only", "---\nenable: false\n---", false},
	} {
		got, err := Enabled([]byte(tc.src))
		if err != nil || got != tc.want {
			t.Errorf("%s: Enabled() = %v, %v, want %v", tc.name, got, err, tc.want)
		}
	}
	for _, src := range []string{"---\nenable: false\n", "---\nenable: [\n---\n"} {
		if _, err := Enabled([]byte(src)); err == nil {
			t.Errorf("Enabled(%q) succeeded, want an error", src)
		}
	}
}

func TestBuiltinPerspectivesAreEnabled(t *testing.T) {
	entries, err := os.ReadDir("builtin")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join("builtin", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := Enabled(b); err != nil || !ok {
			t.Errorf("%s: Enabled() = %v, %v, want true", e.Name(), ok, err)
		}
	}
}
