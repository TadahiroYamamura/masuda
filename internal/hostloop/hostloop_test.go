package hostloop

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpserver"
)

// newTestDaemon starts a fresh Store-backed daemon over a short-lived state
// directory under /tmp (not t.TempDir(), whose longer paths risk exceeding
// AF_UNIX's ~108 byte sun_path limit -- see internal/statedaemon/mcpserver's
// uds_test.go) and returns the state directory this package's functions
// expect (they derive the socket path from it via statedaemon.SocketPath).
func newTestDaemon(t *testing.T) string {
	t.Helper()
	stateDir, err := os.MkdirTemp("", "hl")
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

func TestWriteTaskBriefRoundTrips(t *testing.T) {
	stateDir := newTestDaemon(t)
	if err := WriteTaskBrief(stateDir, "タスクの説明"); err != nil {
		t.Fatalf("WriteTaskBrief() error = %v, want nil", err)
	}

	c, err := dial(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	value, found, err := c.Get(context.Background(), taskBriefKey)
	if err != nil {
		t.Fatal(err)
	}
	if !found || value != "タスクの説明" {
		t.Fatalf("Get(taskBriefKey) = (%q, %v), want (%q, true)", value, found, "タスクの説明")
	}
}

func TestWriteInstructionsRoundTrips(t *testing.T) {
	stateDir := newTestDaemon(t)
	if err := WriteInstructions(stateDir, []byte("事前調査メモ")); err != nil {
		t.Fatalf("WriteInstructions() error = %v, want nil", err)
	}

	c, err := dial(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	value, found, err := c.Get(context.Background(), instructionsKey)
	if err != nil {
		t.Fatal(err)
	}
	if !found || value != "事前調査メモ" {
		t.Fatalf("Get(instructionsKey) = (%q, %v), want (%q, true)", value, found, "事前調査メモ")
	}
}

func TestWriteTDDIntentRoundTrips(t *testing.T) {
	stateDir := newTestDaemon(t)
	if err := WriteTDDIntent(stateDir); err != nil {
		t.Fatalf("WriteTDDIntent() error = %v, want nil", err)
	}

	c, err := dial(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, found, err := c.Get(context.Background(), tddRequestedKey)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("Get(tddRequestedKey) found = false, want true")
	}
}
