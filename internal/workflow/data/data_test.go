package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validPlan = `{"summary": "方針", "steps": [
  {"description": "s1", "files": [{"path": "a.go", "description": "x"}]},
  {"description": "s2", "files": [{"path": "b.go", "description": "y"}, {"path": "a.go", "description": "z"}]}
], "expected_byproducts": ["**/__pycache__/**", "*.log"]}`

func TestStoreWriteLatestHas(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if _, err := s.Write("0000003", "investigation", []byte("調査")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("0000010", "investigation", []byte("調査2")); err != nil {
		t.Fatal(err)
	}
	p, ok, err := s.Latest("investigation")
	if err != nil || !ok || !strings.Contains(p, "0000010") {
		t.Fatalf("Latest = %q %v %v", p, ok, err)
	}
	if !s.Has("0000003", "investigation") || s.Has("0000003", "report") {
		t.Fatal("Has is wrong")
	}
	if filepath.Ext(s.Path("1", "plan")) != ".json" || filepath.Ext(s.Path("1", "security-notes")) != ".md" {
		t.Fatal("unexpected file names")
	}
}

func TestStoreRejects(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	cases := map[string]string{
		"diff":                  "anything",
		"plan":                  `{"summary": "", "steps": []}`,
		"findings":              `[{"file": "../x", "startLine": 0, "endLine": 0, "severity": "大", "description": ""}]`,
		"selected-perspectives": `{"a": 1}`,
		"commit-message":        "\n\n",
		"report":                "   ",
	}
	for name, content := range cases {
		if _, err := s.Write("1", name, []byte(content)); err == nil {
			t.Errorf("Write(%s) accepted %q", name, content)
		}
	}
}

func TestPlanFilesAndByproducts(t *testing.T) {
	p, err := ParsePlan([]byte(validPlan))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Files(1), ","); got != "b.go,a.go" {
		t.Fatalf("Files(1) = %s", got)
	}
	if got := strings.Join(p.Files(-1), ","); got != "a.go,b.go" {
		t.Fatalf("Files(-1) = %s", got)
	}
	for file, want := range map[string]bool{
		"pkg/__pycache__/m.pyc": true,
		"__pycache__/m.pyc":     true,
		"build.log":             true,
		"dir/build.log":         false,
		"a.go":                  false,
	} {
		if got := p.Byproduct(file); got != want {
			t.Errorf("Byproduct(%s) = %v, want %v", file, got, want)
		}
	}
}

func TestLedgerImportReplacesSameSourceAndListsToFix(t *testing.T) {
	l := Ledger{File: filepath.Join(t.TempDir(), "findings.json")}
	f := func(desc string, autofix bool) Finding {
		return Finding{File: "a.go", StartLine: 1, EndLine: 2, Severity: "中", Description: desc, Autofix: autofix}
	}
	if err := l.Import(Record{Workflow: "w", Occurrence: "0000005", Source: "0000004.0/review"}, []Finding{f("old", true)}); err != nil {
		t.Fatal(err)
	}
	if err := l.Import(Record{Workflow: "w", Occurrence: "0000007", Source: "0000004.0/review"}, []Finding{f("new", true), f("design", false)}); err != nil {
		t.Fatal(err)
	}
	if err := l.Import(Record{Workflow: "w", Occurrence: "0000009", Source: "0000004.1/review"}, []Finding{f("other", true)}); err != nil {
		t.Fatal(err)
	}
	all, _ := l.Load()
	if len(all) != 3 {
		t.Fatalf("records = %d, want 3 (the rewritten source replaced its old finding)", len(all))
	}
	if err := l.SetStatus("0000009-001", StatusResolved); err != nil {
		t.Fatal(err)
	}
	todo, err := l.ToFix("0000006")
	if err != nil {
		t.Fatal(err)
	}
	if len(todo) != 1 || todo[0].Description != "new" {
		t.Fatalf("ToFix = %+v, want only the open auto-fixable one", todo)
	}
}

func TestTheCopyForAgentsDoesNotChangeWhatIsRead(t *testing.T) {
	s := Store{Dir: t.TempDir(), MirrorRoot: t.TempDir(), MirrorRel: "wf"}
	p, err := s.Write("0000004", "plan", []byte(validPlan))
	if err != nil {
		t.Fatal(err)
	}
	copyPath := s.Mirrored(p)
	if copyPath == p || !strings.HasPrefix(copyPath, filepath.Join(s.MirrorRoot, "wf")) {
		t.Fatalf("Mirrored = %q, want a path under %s", copyPath, s.MirrorRoot)
	}
	// The copy is where agents read and write; neither editing it nor
	// adding a later-looking one may change the value the engine reads.
	if err := os.WriteFile(copyPath, []byte(`{"summary":"x","steps":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.MirrorRoot, "wf", "out", "9999999"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.MirrorRoot, "wf", "out", "9999999", "plan.json"), []byte(validPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Latest("plan")
	if err != nil || !ok || got != p {
		t.Fatalf("Latest = %q %v %v, want %s", got, ok, err, p)
	}
}

func TestLedgerKeepsACopyForAgents(t *testing.T) {
	dir := t.TempDir()
	l := Ledger{File: filepath.Join(dir, "trusted", "findings.json"), MirrorRoot: filepath.Join(dir, "shared"), MirrorName: "wf/findings.json"}
	if err := os.MkdirAll(l.MirrorRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := l.Import(Record{Occurrence: "0000002", Source: "f/review"}, []Finding{{File: "a.go", StartLine: 1, EndLine: 1, Severity: "高", Description: "p", Suggestion: "s", Autofix: true}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(l.MirrorPath())
	if err != nil || !strings.Contains(string(b), "a.go") {
		t.Fatalf("copy = %q %v", b, err)
	}
	if err := os.WriteFile(l.MirrorPath(), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	rs, err := l.Load()
	if err != nil || len(rs) != 1 {
		t.Fatalf("Load = %v %v, want the one finding despite the edited copy", rs, err)
	}
}
