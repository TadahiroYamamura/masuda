package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowCommandsWorkWithoutServe(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	// 届かないソケットを渡す。serveを叩く作りなら、ここで接続に失敗する
	noServe := []string{"--repo", repo, "--socket", filepath.Join(t.TempDir(), "absent.sock")}

	t.Run("serveが無くても同梱のワークフローの検査が通る", func(t *testing.T) {
		if err := workflowCheck(append([]string{"workflows/smoke"}, noServe...)); err != nil {
			t.Fatalf("workflow check: %v", err)
		}
	})
	t.Run("serveが無くても一覧が出る", func(t *testing.T) {
		if err := workflowList(noServe); err != nil {
			t.Fatalf("workflow list: %v", err)
		}
	})
	t.Run("serveが無くても壊れた定義は問題として返す", func(t *testing.T) {
		dir := filepath.Join(repo, ".masuda", "workflows")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("version: 1\nstart: missing\nnodes: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := workflowCheck(append([]string{"workflows/broken"}, noServe...))
		if err == nil || !strings.Contains(err.Error(), "problem(s)") {
			t.Fatalf("workflow check of a broken definition = %v", err)
		}
	})
}
