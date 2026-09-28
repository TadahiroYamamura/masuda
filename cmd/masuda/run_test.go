package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/workflow/hostenv"
)

func TestParseInputs(t *testing.T) {
	f := filepath.Join(t.TempDir(), "todo.md")
	if err := os.WriteFile(f, []byte("やりたいこと"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := parseInputs([]string{"instructions=@" + f}, []string{"instructions"})
	if err != nil || got["instructions"] != "やりたいこと" {
		t.Fatalf("got %v %v", got, err)
	}
	for _, c := range []struct {
		raw  []string
		want string
	}{
		{nil, "missing --input for: instructions"},
		{[]string{"task=x", "instructions=y"}, "declares no such input"},
		{[]string{"instructions"}, "want key=value"},
		{[]string{"instructions=@/no/such/file"}, "no such file"},
	} {
		if _, err := parseInputs(c.raw, []string{"instructions"}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("parseInputs(%v) = %v, want %q", c.raw, err, c.want)
		}
	}
}

func TestWriteInputsStoresFiles(t *testing.T) {
	dir := t.TempDir()
	paths, err := writeInputs(&hostenv.Env{StateDir: dir, TrustedDir: t.TempDir()}, map[string]string{"instructions": "x"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(paths["instructions"])
	if err != nil || string(b) != "x" || !strings.HasPrefix(paths["instructions"], dir) {
		t.Fatalf("paths = %v, content %q, %v", paths, b, err)
	}
}
