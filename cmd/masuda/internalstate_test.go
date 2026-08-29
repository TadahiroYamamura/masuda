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

// runStateStdin is runState with a body on stdin, for `apply` -- whose ops
// are JSON, which the subcommand deliberately takes on stdin rather than as
// an argument.
func runStateStdin(t *testing.T, socketPath, stdin string, args ...string) string {
	t.Helper()
	cmd := newInternalStateCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append(args, "--socket", socketPath))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("masuda internal state %v: %v (output: %s)", args, err, out.String())
	}
	return strings.TrimSpace(out.String())
}

func TestInternalStateApply(t *testing.T) {
	socketPath := startTestDaemon(t)
	runState(t, socketPath, "put", "gate:plan", "approved")

	const ops = `[{"op":"check","key":"gate:plan","value":"approved"},
	              {"op":"put","key":"internal:plan-approved","value":"approved"},
	              {"op":"delete","key":"gate:plan"}]`

	if out := runStateStdin(t, socketPath, ops, "apply"); !strings.Contains(out, `"applied":true`) {
		t.Fatalf("apply output = %q, want applied=true", out)
	}
	if out := runState(t, socketPath, "get", "gate:plan"); !strings.Contains(out, `"found":false`) {
		t.Errorf("get gate:plan after apply = %q, want found=false", out)
	}
	if out := runState(t, socketPath, "get", "internal:plan-approved"); !strings.Contains(out, "approved") {
		t.Errorf("get internal:plan-approved after apply = %q, want the consumed decision", out)
	}
	if out := runStateStdin(t, socketPath, ops, "apply"); !strings.Contains(out, `"applied":false`) {
		t.Errorf("replayed apply output = %q, want applied=false", out)
	}
}

// TestInternalStateApplyCheckWithoutValueMeansAbsent pins down that the
// absent-vs-empty-string distinction survives the JSON round trip through
// stdin, which is the whole reason the wire form uses an optional field
// rather than an empty string.
func TestInternalStateApplyCheckWithoutValueMeansAbsent(t *testing.T) {
	socketPath := startTestDaemon(t)

	const ops = `[{"op":"check","key":"gate:plan"},{"op":"put","key":"gate:plan","value":"v"}]`
	if out := runStateStdin(t, socketPath, ops, "apply"); !strings.Contains(out, `"applied":true`) {
		t.Fatalf("apply output = %q, want applied=true while gate:plan is absent", out)
	}
	if out := runStateStdin(t, socketPath, ops, "apply"); !strings.Contains(out, `"applied":false`) {
		t.Errorf("apply output = %q, want applied=false once gate:plan exists", out)
	}
}
