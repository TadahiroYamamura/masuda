package mcpclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpserver"
)

// connect starts a fresh ServeUDS-backed daemon and dials it, returning a
// connected Client. Uses a short /tmp path rather than t.TempDir() to stay
// under AF_UNIX's ~108 byte sun_path limit (see mcpserver/uds_test.go).
func connect(t *testing.T) *Client {
	t.Helper()
	sockDir, err := os.MkdirTemp("", "sdmc")
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

	client, err := Dial(context.Background(), socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestPutGetRoundTrip(t *testing.T) {
	c := connect(t)
	ctx := context.Background()

	if err := c.Put(ctx, "gate:plan", `{"status":"approved"}`); err != nil {
		t.Fatal(err)
	}
	value, found, err := c.Get(ctx, "gate:plan")
	if err != nil {
		t.Fatal(err)
	}
	if !found || value != `{"status":"approved"}` {
		t.Fatalf("Get() = (%q, %v), want (%q, true)", value, found, `{"status":"approved"}`)
	}
}

func TestGetMissingKey(t *testing.T) {
	c := connect(t)
	_, found, err := c.Get(context.Background(), "gate:plan")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("Get() found = true for a key never Put, want false")
	}
}

func TestPutInvalidKeyReturnsError(t *testing.T) {
	c := connect(t)
	if err := c.Put(context.Background(), "no-namespace", "v"); err == nil {
		t.Fatal("Put() error = nil for an invalid key, want error")
	}
}

func TestDelete(t *testing.T) {
	c := connect(t)
	ctx := context.Background()
	if err := c.Put(ctx, "gate:plan", "v"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "gate:plan"); err != nil {
		t.Fatal(err)
	}
	_, found, err := c.Get(ctx, "gate:plan")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("Get() found = true after Delete, want false")
	}
	// Deleting an already-missing key must not error.
	if err := c.Delete(ctx, "gate:plan"); err != nil {
		t.Fatalf("Delete() of missing key error = %v, want nil", err)
	}
}

func TestList(t *testing.T) {
	c := connect(t)
	ctx := context.Background()
	for _, key := range []string{"gate:plan", "gate:review", "artifact:plan/summary.md"} {
		if err := c.Put(ctx, key, "v"); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := c.List(ctx, "gate:")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gate:plan", "gate:review"}
	if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
		t.Fatalf("List() = %v, want %v", keys, want)
	}
}

func TestApply(t *testing.T) {
	c := connect(t)
	ctx := context.Background()

	if err := c.Put(ctx, "gate:plan", "rejected"); err != nil {
		t.Fatal(err)
	}
	consume := []statedaemon.Op{
		{Kind: statedaemon.OpCheck, Key: "gate:plan", Value: []byte("rejected")},
		{Kind: statedaemon.OpPut, Key: "internal:plan-redo-pending", Value: []byte("try again")},
		{Kind: statedaemon.OpDelete, Key: "gate:plan"},
	}

	applied, err := c.Apply(ctx, consume)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !applied {
		t.Fatal("Apply() applied = false, want true")
	}
	if _, found, err := c.Get(ctx, "gate:plan"); err != nil || found {
		t.Errorf("Get(gate:plan) = (found=%v, err=%v), want found=false", found, err)
	}
	if v, found, err := c.Get(ctx, "internal:plan-redo-pending"); err != nil || !found || v != "try again" {
		t.Errorf("Get(internal:plan-redo-pending) = (%q, %v, %v), want (%q, true, nil)", v, found, err, "try again")
	}

	applied, err = c.Apply(ctx, consume)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if applied {
		t.Error("Apply() applied = true on a replay, want false")
	}
}
