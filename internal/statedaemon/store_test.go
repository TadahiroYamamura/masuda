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

func TestWaitForPresenceBlocksUntilPut(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var gotValue []byte
	go func() {
		gotValue, _ = s.WaitForPresence(context.Background(), "gate:plan")
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("WaitForPresence returned before any Put")
	case <-time.After(100 * time.Millisecond):
	}

	if err := s.Put("gate:plan", []byte(`{"status":"approved"}`)); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForPresence did not return after Put")
	}

	if string(gotValue) != `{"status":"approved"}` {
		t.Fatalf("WaitForPresence() = %q, want %q", gotValue, `{"status":"approved"}`)
	}
}

// TestWaitForPresenceReturnsImmediatelyWhenAlreadyPresent covers the case the
// event-shaped predecessor deadlocked on (Issue #42): a gate resolved before
// the wait arrives. That happens for real in two ways -- a human approving
// inside the seconds it takes the AI session to issue the tool call, and a
// resumed loop re-establishing its wait on a marker that was written but
// never consumed before the previous run died.
func TestWaitForPresenceReturnsImmediatelyWhenAlreadyPresent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("gate:plan", []byte("v")); err != nil {
		t.Fatal(err)
	}

	// A context that is already past its deadline: if WaitForPresence blocks
	// at all it fails here, rather than passing on a race the test machine
	// happened to win.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	value, err := s.WaitForPresence(ctx, "gate:plan")
	if err != nil {
		t.Fatalf("WaitForPresence() error = %v, want nil for an already-present key", err)
	}
	if string(value) != "v" {
		t.Fatalf("WaitForPresence() = %q, want %q", value, "v")
	}
}

func TestWaitForPresenceFanOutToAllWaiters(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	const waiters = 3
	done := make(chan []byte, waiters)
	for range waiters {
		go func() {
			v, _ := s.WaitForPresence(context.Background(), "gate:review")
			done <- v
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
		case v := <-done:
			if string(v) != "v" {
				t.Errorf("WaitForPresence() = %q, want %q", v, "v")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d/%d waiters woke up", i, waiters)
		}
	}
}

// TestWaitForPresenceKeepsWaitingAfterDelete pins down that notify() means
// "look again" rather than "your wait is over": a Delete wakes the waiter,
// which must find the condition still unmet and go back to waiting instead of
// returning an absent key as if it were an answer.
func TestWaitForPresenceKeepsWaitingAfterDelete(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("gate:triage", []byte("stale")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("gate:triage"); err != nil {
		t.Fatal(err)
	}

	done := make(chan []byte, 1)
	go func() {
		v, _ := s.WaitForPresence(context.Background(), "gate:triage")
		done <- v
	}()
	time.Sleep(50 * time.Millisecond)

	// A Delete on a key that is already absent still broadcasts, which is
	// exactly the wake-up the waiter must not mistake for a result.
	if err := s.Delete("gate:triage"); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-done:
		t.Fatalf("WaitForPresence returned %q after a Delete, want it still waiting", v)
	case <-time.After(100 * time.Millisecond):
	}

	if err := s.Put("gate:triage", []byte("fresh")); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-done:
		if string(v) != "fresh" {
			t.Errorf("WaitForPresence() = %q, want %q", v, "fresh")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForPresence did not return after the key reappeared")
	}
}

func TestWaitForPresenceReturnsOnContextCancel(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := s.WaitForPresence(ctx, "gate:plan"); err == nil {
		t.Fatal("WaitForPresence() error = nil, want context deadline error")
	}

	// The cancelled waiter must not still be registered -- a later Put on
	// the same key should not panic or hang trying to notify it.
	if err := s.Put("gate:plan", []byte("v")); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRunsEveryOpWhenChecksHold(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("gate:plan", []byte(`{"status":"rejected"}`)); err != nil {
		t.Fatal(err)
	}

	applied, err := s.Apply([]Op{
		{Kind: OpCheck, Key: "gate:plan", Value: []byte(`{"status":"rejected"}`)},
		{Kind: OpPut, Key: "internal:plan-redo-pending", Value: []byte("fix the plan")},
		{Kind: OpDelete, Key: "gate:plan"},
	})
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if !applied {
		t.Fatal("Apply() applied = false, want true")
	}
	if _, ok := s.Get("gate:plan"); ok {
		t.Error("gate:plan still present after an Apply that deleted it")
	}
	if v, ok := s.Get("internal:plan-redo-pending"); !ok || string(v) != "fix the plan" {
		t.Errorf("internal:plan-redo-pending = (%q, %v), want (%q, true)", v, ok, "fix the plan")
	}
}

// TestApplyChangesNothingWhenACheckFails is the race the gate consumers exist
// to survive: a second decision landing between the read and the writes that
// consume the first one. The apply must lose cleanly and leave the newer
// decision intact rather than deleting it unseen.
func TestApplyChangesNothingWhenACheckFails(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("gate:plan", []byte(`{"status":"rejected"}`)); err != nil {
		t.Fatal(err)
	}

	applied, err := s.Apply([]Op{
		{Kind: OpCheck, Key: "gate:plan", Value: []byte(`{"status":"approved"}`)},
		{Kind: OpPut, Key: "internal:plan-redo-pending", Value: []byte("fix the plan")},
		{Kind: OpDelete, Key: "gate:plan"},
	})
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if applied {
		t.Fatal("Apply() applied = true, want false for a stale check")
	}
	if v, ok := s.Get("gate:plan"); !ok || string(v) != `{"status":"rejected"}` {
		t.Errorf("gate:plan = (%q, %v), want the newer decision left untouched", v, ok)
	}
	if _, ok := s.Get("internal:plan-redo-pending"); ok {
		t.Error("internal:plan-redo-pending was written even though the Apply did not apply")
	}
}

func TestApplyCheckWithNilValueMeansAbsent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	applied, err := s.Apply([]Op{
		{Kind: OpCheck, Key: "gate:plan"},
		{Kind: OpPut, Key: "gate:plan", Value: []byte("v")},
	})
	if err != nil || !applied {
		t.Fatalf("Apply() = (%v, %v), want (true, nil) while gate:plan is absent", applied, err)
	}

	applied, err = s.Apply([]Op{
		{Kind: OpCheck, Key: "gate:plan"},
		{Kind: OpPut, Key: "gate:review", Value: []byte("v")},
	})
	if err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}
	if applied {
		t.Fatal("Apply() applied = true, want false: gate:plan exists now")
	}
	if _, ok := s.Get("gate:review"); ok {
		t.Error("gate:review was written even though the Apply did not apply")
	}
}

func TestApplyRejectsAmbiguousOrInvalidOps(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, ops := range map[string][]Op{
		"same key written twice": {
			{Kind: OpPut, Key: "gate:plan", Value: []byte("a")},
			{Kind: OpDelete, Key: "gate:plan"},
		},
		"invalid key": {
			{Kind: OpPut, Key: "no-namespace", Value: []byte("a")},
		},
		"invalid key in a check": {
			{Kind: OpCheck, Key: "gate:../escape", Value: []byte("a")},
		},
		"unknown op kind": {
			{Kind: OpKind("frobnicate"), Key: "gate:plan"},
		},
	} {
		if _, err := s.Apply(ops); err == nil {
			t.Errorf("Apply(%s) error = nil, want error", name)
		}
	}
	// Guarding the very key an op writes is the normal shape, not ambiguity.
	if _, err := s.Apply([]Op{
		{Kind: OpCheck, Key: "gate:plan"},
		{Kind: OpPut, Key: "gate:plan", Value: []byte("a")},
	}); err != nil {
		t.Errorf("Apply(check+put on one key) error = %v, want nil", err)
	}
}

func TestApplyWakesWaiters(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan []byte, 1)
	go func() {
		v, _ := s.WaitForPresence(context.Background(), "gate:review")
		done <- v
	}()
	time.Sleep(50 * time.Millisecond)

	if _, err := s.Apply([]Op{{Kind: OpPut, Key: "gate:review", Value: []byte("v")}}); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-done:
		if string(v) != "v" {
			t.Errorf("WaitForPresence() = %q, want %q", v, "v")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a Put made through Apply did not wake a waiter")
	}
}

func TestApplyPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Put("gate:plan", []byte("decision")); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Apply([]Op{
		{Kind: OpPut, Key: "internal:plan-approved", Value: []byte("decision")},
		{Kind: OpDelete, Key: "gate:plan"},
	}); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Get("gate:plan"); ok {
		t.Error("gate:plan came back after reopen, want it deleted on disk too")
	}
	if v, ok := s2.Get("internal:plan-approved"); !ok || string(v) != "decision" {
		t.Errorf("internal:plan-approved after reopen = (%q, %v), want (%q, true)", v, ok, "decision")
	}
}
