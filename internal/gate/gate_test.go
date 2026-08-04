package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePlan(t *testing.T, stateDir, summary, stepsJSON string) {
	t.Helper()
	dir := filepath.Join(stateDir, planDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(summary), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "steps.json"), []byte(stepsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestShowPlanRendersSummaryAndSteps(t *testing.T) {
	stateDir := t.TempDir()
	writePlan(t, stateDir, "アプローチの要約。", `{"steps": [
		{"description": "ステップ1", "files": [{"path": "a.go", "description": "aを追加"}]},
		{"description": "ステップ2", "files": [{"path": "b.go", "description": "bを追加"}]}
	]}`)

	out, err := Show(stateDir, Plan)
	if err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}

	for _, want := range []string{
		"アプローチの要約。",
		"## 変更するファイル一覧",
		"`a.go`: aを追加",
		"`b.go`: bを追加",
		"## 実装のステップ分解",
		"1. ステップ1",
		"2. ステップ2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Show() output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestShowPlanDedupsFilesSharedAcrossSteps(t *testing.T) {
	stateDir := t.TempDir()
	writePlan(t, stateDir, "summary", `{"steps": [
		{"description": "ステップ1", "files": [{"path": "shared.go", "description": "1回目"}]},
		{"description": "ステップ2", "files": [{"path": "shared.go", "description": "2回目"}]}
	]}`)

	out, err := Show(stateDir, Plan)
	if err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}

	// Dedup only applies to the "変更するファイル一覧" section (a union
	// across steps) -- the per-step breakdown below it legitimately repeats
	// shared.go once per step that touches it, so isolate that section
	// before counting.
	fileListSection := out[strings.Index(out, "## 変更するファイル一覧"):strings.Index(out, "## 実装のステップ分解")]
	if n := strings.Count(fileListSection, "`shared.go`"); n != 1 {
		t.Fatalf("expected shared.go to appear once in 変更するファイル一覧 (deduped), got %d occurrences\n%s", n, fileListSection)
	}
}

func TestShowPlanPrependsDeviationWhenG1Reopened(t *testing.T) {
	stateDir := t.TempDir()
	writePlan(t, stateDir, "summary", `{"steps": [{"description": "ステップ1", "files": []}]}`)
	if err := os.WriteFile(filepath.Join(stateDir, "DEVIATION.md"), []byte("計画外の変更があった"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Show(stateDir, Plan)
	if err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}

	if !strings.Contains(out, "計画外の変更があった") {
		t.Fatalf("Show() must prepend DEVIATION.md's reason, got:\n%s", out)
	}
	if !strings.Contains(out, "G1 reopened") {
		t.Fatalf("Show() must label the reopened state, got:\n%s", out)
	}
}

func TestShowPlanRendersExpectedByproductsWhenPresent(t *testing.T) {
	stateDir := t.TempDir()
	writePlan(t, stateDir, "summary", `{
		"steps": [{"description": "ステップ1", "files": []}],
		"expected_byproducts": ["**/__pycache__/**", "**/*.pyc"]
	}`)

	out, err := Show(stateDir, Plan)
	if err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}

	for _, want := range []string{
		"## 生成される可能性のある副産物ファイル",
		"`**/__pycache__/**`",
		"`**/*.pyc`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Show() output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestShowPlanOmitsExpectedByproductsSectionWhenAbsent(t *testing.T) {
	stateDir := t.TempDir()
	writePlan(t, stateDir, "summary", `{"steps": [{"description": "ステップ1", "files": []}]}`)

	out, err := Show(stateDir, Plan)
	if err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}

	if strings.Contains(out, "副産物ファイル") {
		t.Fatalf("Show() must omit the byproducts section when the plan predicted none, got:\n%s", out)
	}
}

func TestShowPlanMissingStepsErrors(t *testing.T) {
	stateDir := t.TempDir()
	if _, err := Show(stateDir, Plan); err == nil {
		t.Fatal("Show() error = nil, want an error when plan/steps.json is missing")
	}
}

func TestShowReviewStillReadsFinalReportVerbatim(t *testing.T) {
	stateDir := t.TempDir()
	dir := filepath.Join(stateDir, "review_results")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "final_report.md"), []byte("# report body"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Show(stateDir, Review)
	if err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}
	if out != "# report body" {
		t.Fatalf("Show(Review) = %q, want the file's raw content unchanged", out)
	}
}
