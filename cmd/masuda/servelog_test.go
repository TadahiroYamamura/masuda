package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenServeLog(t *testing.T) {
	var warned []string
	warn := func(format string, a ...any) { warned = append(warned, fmt.Sprintf(format, a...)) }

	t.Run("既定ではデータディレクトリのlogsの下にファイルを作る", func(t *testing.T) {
		dataDir := t.TempDir()
		path, f := openServeLog("", dataDir, warn)
		if f == nil {
			t.Fatal("no log file")
		}
		defer f.Close()
		if path != filepath.Join(dataDir, "logs", "masuda-serve.log") {
			t.Fatalf("path = %q", path)
		}
	})
	t.Run("起動のたびに前のファイルを.1に回し、その前の.1は残さない", func(t *testing.T) {
		dataDir := t.TempDir()
		for _, line := range []string{"first\n", "second\n", "third\n"} {
			_, f := openServeLog("", dataDir, warn)
			if _, err := f.WriteString(line); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
		cur, _ := os.ReadFile(filepath.Join(dataDir, "logs", "masuda-serve.log"))
		prev, _ := os.ReadFile(filepath.Join(dataDir, "logs", "masuda-serve.log.1"))
		if string(cur) != "third\n" || string(prev) != "second\n" {
			t.Fatalf("current = %q, .1 = %q", cur, prev)
		}
	})
	t.Run("-なら標準エラー出力のままにする", func(t *testing.T) {
		dataDir := t.TempDir()
		if path, f := openServeLog("-", dataDir, warn); f != nil || path != "" {
			t.Fatalf("openServeLog(-) = %q, %v", path, f)
		}
		if _, err := os.Stat(filepath.Join(dataDir, "logs")); err == nil {
			t.Fatal("logs directory must not be created")
		}
	})
	t.Run("開けないときは標準エラー出力のままにしてその旨を書く", func(t *testing.T) {
		dataDir := t.TempDir()
		// logsの場所がファイルなので、ディレクトリを作れない
		if err := os.WriteFile(filepath.Join(dataDir, "logs"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		warned = nil
		if path, f := openServeLog("", dataDir, warn); f != nil || path != "" {
			t.Fatalf("openServeLog = %q, %v", path, f)
		}
		if len(warned) != 1 || !strings.Contains(warned[0], "logging to stderr") {
			t.Fatalf("warned = %q", warned)
		}
	})
	t.Run("ディレクトリに書けずファイルを開けないときも標準エラー出力のままにする", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("rootは読み取り専用のディレクトリにも書ける")
		}
		dataDir := t.TempDir()
		logs := filepath.Join(dataDir, "logs")
		if err := os.Mkdir(logs, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(logs, 0o700) })
		warned = nil
		if path, f := openServeLog("", dataDir, warn); f != nil || path != "" {
			t.Fatalf("openServeLog = %q, %v", path, f)
		}
		if len(warned) == 0 || !strings.Contains(warned[len(warned)-1], "cannot open the log file") {
			t.Fatalf("warned = %q", warned)
		}
	})
}

func TestSandboxLogPathFollowsXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	if got := sandboxLogPath(); got != "/data/masuda-sandbox/logs/masuda-sandbox-serve.log" {
		t.Fatalf("sandboxLogPath = %q", got)
	}
}
