package statedaemon

import (
	"context"
	"testing"
	"time"
)

func TestPutGetRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("gate:plan", []byte(`{"status":"pending"}`)); err != nil {
		t.Fatal(err)
	}
	v, ok := s.Get("gate:plan")
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if string(v) != `{"status":"pending"}` {
		t.Fatalf("Get() = %q, want %q", v, `{"status":"pending"}`)
	}
}

func TestGetMissingKeyReturnsFalse(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("gate:plan"); ok {
		t.Fatal("Get() ok = true for a key never Put, want false")
	}
}

func TestPutRejectsInvalidKey(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "no-namespace", "gate:", ":rest", "gate:../escape"} {
		if err := s.Put(key, []byte("x")); err == nil {
			t.Errorf("Put(%q) error = nil, want error", key)
		}
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("gate:plan", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("gate:plan"); err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}
	if _, ok := s.Get("gate:plan"); ok {
		t.Fatal("Get() ok = true after Delete, want false")
	}
	// Deleting again (already gone) must not error.
	if err := s.Delete("gate:plan"); err != nil {
		t.Fatalf("Delete() of missing key error = %v, want nil", err)
	}
}

func TestList(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"gate:plan", "gate:review", "artifact:plan/summary.md"} {
		if err := s.Put(key, []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	got := s.List("gate:")
	want := []string{"gate:plan", "gate:review"}
	if len(got) != len(want) {
		t.Fatalf("List(\"gate:\") = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List(\"gate:\") = %v, want %v", got, want)
		}
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Put("gate:plan", []byte(`{"status":"approved"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s1.Put("artifact:plan/summary.md", []byte("summary text")); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := s2.Get("gate:plan")
	if !ok || string(v) != `{"status":"approved"}` {
		t.Fatalf("Get(\"gate:plan\") after reopen = (%q, %v), want (%q, true)", v, ok, `{"status":"approved"}`)
	}
	v, ok = s2.Get("artifact:plan/summary.md")
	if !ok || string(v) != "summary text" {
		t.Fatalf("Get(\"artifact:plan/summary.md\") after reopen = (%q, %v), want (%q, true)", v, ok, "summary text")
	}
}

func TestWaitForChangeBlocksUntilPut(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var gotValue []byte
	var gotOK bool
	go func() {
		gotValue, gotOK, _ = s.WaitForChange(context.Background(), "gate:plan")
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("WaitForChange returned before any Put")
	case <-time.After(100 * time.Millisecond):
	}

	if err := s.Put("gate:plan", []byte(`{"status":"approved"}`)); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForChange did not return after Put")
	}

	if !gotOK || string(gotValue) != `{"status":"approved"}` {
		t.Fatalf("WaitForChange() = (%q, %v), want (%q, true)", gotValue, gotOK, `{"status":"approved"}`)
	}
}

func TestWaitForChangeFanOutToAllWaiters(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	const waiters = 3
	done := make(chan bool, waiters)
	for range waiters {
		go func() {
			_, ok, _ := s.WaitForChange(context.Background(), "gate:review")
			done <- ok
		}()
	}
	// Give every goroutine a chance to register before Put fires, so the
	// notify() broadcast has all of them in its waiter list at once.
	time.Sleep(50 * time.Millisecond)

	if err := s.Put("gate:review", []byte("v")); err != nil {
		t.Fatal(err)
	}

	for i := range waiters {
		select {
		case ok := <-done:
			if !ok {
				t.Error("WaitForChange() ok = false, want true")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d/%d waiters woke up", i, waiters)
		}
	}
}

func TestWaitForChangeReportsDelete(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("gate:triage", []byte("v")); err != nil {
		t.Fatal(err)
	}

	done := make(chan bool, 1)
	go func() {
		_, ok, _ := s.WaitForChange(context.Background(), "gate:triage")
		done <- ok
	}()
	time.Sleep(50 * time.Millisecond)

	if err := s.Delete("gate:triage"); err != nil {
		t.Fatal(err)
	}

	select {
	case ok := <-done:
		if ok {
			t.Error("WaitForChange() ok = true after Delete, want false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForChange did not return after Delete")
	}
}

func TestWaitForChangeReturnsOnContextCancel(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, err = s.WaitForChange(ctx, "gate:plan")
	if err == nil {
		t.Fatal("WaitForChange() error = nil, want context deadline error")
	}

	// The cancelled waiter must not still be registered -- a later Put on
	// the same key should not panic or hang trying to notify it.
	if err := s.Put("gate:plan", []byte("v")); err != nil {
		t.Fatal(err)
	}
}
