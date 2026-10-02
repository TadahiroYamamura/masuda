package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestNodeStatus(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want checkStatus
	}{
		{"v22.19.0", checkOK},
		{"v24.16.1", checkOK},
		{"v22.18.9", checkFail},
		{"v20.11.0", checkFail},
		{"v24.17.0", checkWarn},
		{"v25.0.0", checkWarn},
		{"nightly", checkWarn},
	} {
		if got, _, _ := nodeStatus(tc.out); got != tc.want {
			t.Errorf("nodeStatus(%q) = %v, want %v", tc.out, got, tc.want)
		}
	}
}

// 警告だけなら可、NGが1つでもあれば不可。直し方はNG・警告の項目にだけ出る。
func TestReportChecks(t *testing.T) {
	var buf bytes.Buffer
	if reportChecks(&buf, []checkResult{{name: "a", detail: "x"}, {name: "b", status: checkWarn, detail: "y", fix: "do b"}}) {
		t.Fatal("warnings alone should not fail")
	}
	if !strings.Contains(buf.String(), "do b") {
		t.Fatalf("fix for a warning should be shown:\n%s", buf.String())
	}
	buf.Reset()
	if !reportChecks(&buf, []checkResult{{name: "a", status: checkFail, detail: "missing", fix: "install a"}}) {
		t.Fatal("a failure should fail")
	}
	if !strings.Contains(buf.String(), "[NG  ] a: missing") || !strings.Contains(buf.String(), "install a") {
		t.Fatalf("output:\n%s", buf.String())
	}
}
