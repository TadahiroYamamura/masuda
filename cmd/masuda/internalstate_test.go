package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpserver"
)

// startTestDaemon starts a fresh Store-backed daemon over a short-lived UDS
// socket (under /tmp directly -- see uds_test.go's precedent on sun_path
// length) and returns its socket path.
func startTestDaemon(t *testing.T) string {
	t.Helper()
	sockDir, err := os.MkdirTemp("", "sdis")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	socketPath := filepath.Join(sockDir, "daemon.sock")

	store, err := statedaemon.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- mcpserver.ServeUDS(ctx, store, socketPath) }()
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
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return socketPath
}

// runState invokes `masuda internal state <args...> --socket socketPath` in
// process and returns its trimmed stdout.
func runState(t *testing.T, socketPath string, args ...string) string {
	t.Helper()
	cmd := newInternalStateCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append(args, "--socket", socketPath))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("masuda internal state %v: %v (output: %s)", args, err, out.String())
	}
	return strings.TrimSpace(out.String())
}

func TestInternalStatePutGetRoundTrip(t *testing.T) {
	socketPath := startTestDaemon(t)

	runState(t, socketPath, "put", "gate:plan", `{"status":"approved"}`)

	out := runState(t, socketPath, "get", "gate:plan")
	var got struct {
		Value string `json:"value"`
		Found bool   `json:"found"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if !got.Found || got.Value != `{"status":"approved"}` {
		t.Fatalf("get output = %+v, want found=true value=%q", got, `{"status":"approved"}`)
	}
}

func TestInternalStateGetMissingKey(t *testing.T) {
	socketPath := startTestDaemon(t)
	out := runState(t, socketPath, "get", "gate:plan")
	if !strings.Contains(out, `"found":false`) {
		t.Fatalf("get output = %q, want found=false", out)
	}
}

func TestInternalStateDelete(t *testing.T) {
	socketPath := startTestDaemon(t)
	runState(t, socketPath, "put", "gate:plan", "v")
	runState(t, socketPath, "delete", "gate:plan")
	out := runState(t, socketPath, "get", "gate:plan")
	if !strings.Contains(out, `"found":false`) {
		t.Fatalf("get output after delete = %q, want found=false", out)
	}
	// Deleting again must not error (cobra Execute would have failed the test via runState).
	runState(t, socketPath, "delete", "gate:plan")
}

func TestInternalStateList(t *testing.T) {
	socketPath := startTestDaemon(t)
	runState(t, socketPath, "put", "gate:plan", "v")
	runState(t, socketPath, "put", "gate:review", "v")
	runState(t, socketPath, "put", "artifact:plan/summary.md", "v")

	out := runState(t, socketPath, "list", "gate:")
	var got struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	want := []string{"gate:plan", "gate:review"}
	if len(got.Keys) != len(want) || got.Keys[0] != want[0] || got.Keys[1] != want[1] {
		t.Fatalf("list output = %v, want %v", got.Keys, want)
	}
}

func TestInternalStateWait(t *testing.T) {
	socketPath := startTestDaemon(t)

	done := make(chan string, 1)
	go func() { done <- runState(t, socketPath, "wait", "gate:plan") }()

	select {
	case <-done:
		t.Fatal("wait returned before any put")
	case <-time.After(100 * time.Millisecond):
	}

	runState(t, socketPath, "put", "gate:plan", "approved")

	select {
	case out := <-done:
		if !strings.Contains(out, `"found":true`) || !strings.Contains(out, "approved") {
			t.Fatalf("wait output = %q, want found=true value containing %q", out, "approved")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not return after put")
	}
}

func TestInternalStateUsesMasudaStateDirEnvVar(t *testing.T) {
	socketPath := startTestDaemon(t)
	t.Setenv("MASUDA_STATE_DIR", filepath.Dir(socketPath))

	cmd := newInternalStateCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"put", "gate:plan", "v"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("put without --socket, via MASUDA_STATE_DIR: %v (output: %s)", err, out.String())
	}
}
