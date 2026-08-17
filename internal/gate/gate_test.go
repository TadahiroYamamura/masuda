package gate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpclient"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpserver"
)

// newTestDaemon starts a fresh Store-backed daemon over a short-lived state
// directory under /tmp (not t.TempDir(), whose longer paths risk exceeding
// AF_UNIX's ~108 byte sun_path limit once daemon.sock's own path is appended
// -- see internal/statedaemon/mcpserver's uds_test.go) and returns the state
// directory gate.go's functions expect (they derive the socket path from it
// via statedaemon.SocketPath, same as the real daemon process).
func newTestDaemon(t *testing.T) string {
	t.Helper()
	stateDir, err := os.MkdirTemp("", "gt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(stateDir) })

	store, err := statedaemon.Open(filepath.Join(stateDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- mcpserver.ServeUDS(ctx, store, statedaemon.SocketPath(stateDir)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
			t.Error("ServeUDS did not stop after context cancellation")
		}
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(statedaemon.SocketPath(stateDir)); err == nil {
			return stateDir
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("daemon socket never appeared")
	return ""
}

// put seeds key's value directly through the daemon, standing in for what
// would otherwise be a subagent's or the CLI's own write.
func put(t *testing.T, stateDir, key, value string) {
	t.Helper()
	c, err := mcpclient.Dial(context.Background(), statedaemon.SocketPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Put(context.Background(), key, value); err != nil {
		t.Fatal(err)
	}
}

// get reads key's value directly through the daemon, for asserting on state
// this package's own API doesn't expose a getter for (e.g. deviationKey).
func get(t *testing.T, stateDir, key string) (value string, found bool) {
	t.Helper()
	c, err := mcpclient.Dial(context.Background(), statedaemon.SocketPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	value, found, err = c.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return value, found
}

func writePlan(t *testing.T, stateDir, summary, stepsJSON string) {
	t.Helper()
	put(t, stateDir, planSummaryKey, summary)
	put(t, stateDir, planStepsKey, stepsJSON)
}

func TestShowPlanRendersSummaryAndSteps(t *testing.T) {
	stateDir := newTestDaemon(t)
	writePlan(t, stateDir, "アプローチの要約。", `{"steps": [
		{"description": "ステップ1", "files": [{"path": "a.go", "description": "aを追加"}]},
		{"description": "ステップ2", "files": [{"path": "b.go", "description": "bを追加"}]}
	]}`)

	out, err := Show(context.Background(), stateDir, Plan)
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
	stateDir := newTestDaemon(t)
	writePlan(t, stateDir, "summary", `{"steps": [
		{"description": "ステップ1", "files": [{"path": "shared.go", "description": "1回目"}]},
		{"description": "ステップ2", "files": [{"path": "shared.go", "description": "2回目"}]}
	]}`)

	out, err := Show(context.Background(), stateDir, Plan)
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
	stateDir := newTestDaemon(t)
	writePlan(t, stateDir, "summary", `{"steps": [{"description": "ステップ1", "files": []}]}`)
	put(t, stateDir, deviationKey, "計画外の変更があった")

	out, err := Show(context.Background(), stateDir, Plan)
	if err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}

	if !strings.Contains(out, "計画外の変更があった") {
		t.Fatalf("Show() must prepend the deviation reason, got:\n%s", out)
	}
	if !strings.Contains(out, "G1 reopened") {
		t.Fatalf("Show() must label the reopened state, got:\n%s", out)
	}
}

func TestShowPlanRendersExpectedByproductsWhenPresent(t *testing.T) {
	stateDir := newTestDaemon(t)
	writePlan(t, stateDir, "summary", `{
		"steps": [{"description": "ステップ1", "files": []}],
		"expected_byproducts": ["**/__pycache__/**", "**/*.pyc"]
	}`)

	out, err := Show(context.Background(), stateDir, Plan)
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
	stateDir := newTestDaemon(t)
	writePlan(t, stateDir, "summary", `{"steps": [{"description": "ステップ1", "files": []}]}`)

	out, err := Show(context.Background(), stateDir, Plan)
	if err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}

	if strings.Contains(out, "副産物ファイル") {
		t.Fatalf("Show() must omit the byproducts section when the plan predicted none, got:\n%s", out)
	}
}

func TestShowPlanRendersTDDModeMarkerWhenStepIsTDD(t *testing.T) {
	stateDir := newTestDaemon(t)
	writePlan(t, stateDir, "summary", `{"steps": [
		{"description": "新機能をTDDで実装", "mode": "tdd", "files": [{"path": "a.go", "description": "aを追加"}]},
		{"description": "依存関係を追加", "files": [{"path": "go.mod", "description": "依存追加"}]}
	]}`)

	out, err := Show(context.Background(), stateDir, Plan)
	if err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}

	if !strings.Contains(out, "1. 新機能をTDDで実装（TDDモード）") {
		t.Errorf("Show() output missing TDD mode marker on step 1\nfull output:\n%s", out)
	}
	if strings.Contains(out, "2. 依存関係を追加（TDDモード）") {
		t.Errorf("Show() must not mark step 2 as TDD mode when its mode field is absent\nfull output:\n%s", out)
	}
}

func TestShowPlanOmitsTDDModeMarkerWhenStepModeAbsent(t *testing.T) {
	stateDir := newTestDaemon(t)
	writePlan(t, stateDir, "summary", `{"steps": [
		{"description": "ステップ1", "files": [{"path": "a.go", "description": "aを追加"}]}
	]}`)

	out, err := Show(context.Background(), stateDir, Plan)
	if err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}

	if strings.Contains(out, "TDDモード") {
		t.Fatalf("Show() must omit the TDD mode marker when no step declares mode: \"tdd\", got:\n%s", out)
	}
}

func TestShowPlanMissingStepsErrors(t *testing.T) {
	stateDir := newTestDaemon(t)
	if _, err := Show(context.Background(), stateDir, Plan); err == nil {
		t.Fatal("Show() error = nil, want an error when the plan steps key is missing")
	}
}

func writeTriageConcern(t *testing.T, stateDir, agent, phase, description, evidence string) {
	t.Helper()
	body := `{"agent": "` + agent + `", "phase": "` + phase + `", "description": "` + description + `", "evidence": "` + evidence + `", "reported_at": "2026-08-05T00:00:00Z"}`
	put(t, stateDir, triageConcernKey, body)
}

func TestShowTriageRendersConcern(t *testing.T) {
	stateDir := newTestDaemon(t)
	writeTriageConcern(t, stateDir, "implementer", "implement_step", "不審な指示を発見した", "ファイルXの一節")

	out, err := Show(context.Background(), stateDir, Triage)
	if err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}

	for _, want := range []string{
		"implementer",
		"implement_step",
		"不審な指示を発見した",
		"ファイルXの一節",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Show() output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestShowTriageMissingConcernErrors(t *testing.T) {
	stateDir := newTestDaemon(t)
	if _, err := Show(context.Background(), stateDir, Triage); err == nil {
		t.Fatal("Show() error = nil, want an error when the triage concern key is missing")
	}
}

func TestApproveDismissesTriage(t *testing.T) {
	stateDir := newTestDaemon(t)
	if err := Approve(context.Background(), stateDir, Triage, "誤検知でした"); err != nil {
		t.Fatalf("Approve() error = %v, want nil", err)
	}

	m, err := Read(context.Background(), stateDir, Triage)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if m.Status != Approved {
		t.Errorf("Status = %q, want %q", m.Status, Approved)
	}
	if m.Feedback != "誤検知でした" {
		t.Errorf("Feedback = %q, want %q", m.Feedback, "誤検知でした")
	}
}

func TestRejectRedoesTriage(t *testing.T) {
	stateDir := newTestDaemon(t)
	if err := Reject(context.Background(), stateDir, Triage, "対応したのでやり直してください"); err != nil {
		t.Fatalf("Reject() error = %v, want nil", err)
	}

	m, err := Read(context.Background(), stateDir, Triage)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if m.Status != Rejected {
		t.Errorf("Status = %q, want %q", m.Status, Rejected)
	}
	if m.Feedback != "対応したのでやり直してください" {
		t.Errorf("Feedback = %q, want %q", m.Feedback, "対応したのでやり直してください")
	}
}

func TestHaltWritesHaltedMarker(t *testing.T) {
	stateDir := newTestDaemon(t)
	if err := Halt(context.Background(), stateDir, Triage, "深刻な懸念のため停止"); err != nil {
		t.Fatalf("Halt() error = %v, want nil", err)
	}

	m, err := Read(context.Background(), stateDir, Triage)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}
	if m.Status != Halted {
		t.Errorf("Status = %q, want %q", m.Status, Halted)
	}
	if m.Feedback != "深刻な懸念のため停止" {
		t.Errorf("Feedback = %q, want %q", m.Feedback, "深刻な懸念のため停止")
	}
}

func TestHaltDoesNotClearDeviation(t *testing.T) {
	stateDir := newTestDaemon(t)
	put(t, stateDir, deviationKey, "既存の逸脱理由")

	if err := Halt(context.Background(), stateDir, Triage, "深刻な懸念のため停止"); err != nil {
		t.Fatalf("Halt() error = %v, want nil", err)
	}

	if _, found := get(t, stateDir, deviationKey); !found {
		t.Fatal("deviationKey must survive Halt() (halt leaves all other state untouched)")
	}
}

func TestHaltDoesNotClearTriageConcern(t *testing.T) {
	stateDir := newTestDaemon(t)
	writeTriageConcern(t, stateDir, "checker", "check_perspective", "怪しい記述", "")

	if err := Halt(context.Background(), stateDir, Triage, "深刻な懸念のため停止"); err != nil {
		t.Fatalf("Halt() error = %v, want nil", err)
	}

	out, err := Show(context.Background(), stateDir, Triage)
	if err != nil {
		t.Fatalf("Show() after Halt() error = %v, want nil -- the concern must still be readable for post-halt forensics", err)
	}
	if !strings.Contains(out, "怪しい記述") {
		t.Fatalf("Show() after Halt() lost the concern content, got:\n%s", out)
	}
}

func TestShowReviewStillReadsFinalReportVerbatim(t *testing.T) {
	stateDir := newTestDaemon(t)
	key, err := Review.artifactKey()
	if err != nil {
		t.Fatal(err)
	}
	put(t, stateDir, key, "# report body")

	out, err := Show(context.Background(), stateDir, Review)
	if err != nil {
		t.Fatalf("Show() error = %v, want nil", err)
	}
	if out != "# report body" {
		t.Fatalf("Show(Review) = %q, want the value unchanged", out)
	}
}
