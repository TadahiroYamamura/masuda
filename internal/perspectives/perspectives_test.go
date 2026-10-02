package perspectives

import (
	"io/fs"
	"strings"
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

// テスト漏れの観点は途中レビュー（ステップ単位）で選ばれてはならない。計画はテストを後の
// ステップに置くことが多く、1ステップの差分だけでは漏れかどうかを判断できないため。
func TestMissingTestsPerspectivesHaveNoTrigger(t *testing.T) {
	for _, name := range []string{"missing-tests-new-code.md", "missing-tests-guard-clauses.md"} {
		b, err := fs.ReadFile(Builtin(), name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "\ntrigger:") {
			t.Errorf("%s has a trigger; it would be picked for interim (per-step) reviews", name)
		}
	}
}
