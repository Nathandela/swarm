package daemon

// FAILING-FIRST tests for C4: the daemon must bound its idempotency log. Today it
// opens the store with idempotency.Open (Options{}: no TTL, no cap) and never calls
// Compact, so every remote kill/delete/take_control/launch appends a permanent
// fsync'd record replayed IN FULL on every restart -> slow disk exhaustion +
// O(all-ops-ever) restart replay.
//
// R6 safety (recorded in the A5 review): a record must NOT be GC'd before its
// command's ExpiresAt, or the replay hole reopens. A device-signed ExpiresAt is
// capped server-side at now+1h (F5), so no accepted command can be valid past ~1h.
// The store GCs on UpdatedAt, so a 24h TTL (>> the 1h validity cap) can never drop
// a record whose command is still replayable. These tests assert the WIRING:
// bounded options + a Compact that runs, WITHOUT weakening the within-TTL replay
// property (a still-valid record survives and keeps its cached outcome).

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/idempotency"
)

// TestDaemon_IdempotencyStoreCompacts seeds the durable log with one stale record
// (UpdatedAt far beyond any sane TTL) and one within-TTL record BEFORE the daemon
// opens, then asserts Open compacts: the stale record is GC'd while the fresh one
// survives WITH its cached outcome intact (replay must still short-circuit). RED
// today because the daemon opens an unbounded store and never Compacts, so the
// stale record survives.
func TestDaemon_IdempotencyStoreCompacts(t *testing.T) {
	cfg := daemonConfig(t)
	idemDir := filepath.Join(cfg.StateDir, "idempotency")

	// Seed the store's durable log via its own public API (a faithful log, not a
	// hand-rolled file) with a mutable clock: the stale record is aged far past any
	// sane TTL, the fresh record is stamped now.
	clock := time.Now().Add(-72 * time.Hour)
	seed, err := idempotency.OpenWithOptions(idemDir, idempotency.Options{Clock: func() time.Time { return clock }})
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if _, _, err := seed.Prepare("devA:STALE", "kill", "s1"); err != nil {
		t.Fatalf("seed stale Prepare: %v", err)
	}
	if err := seed.Complete("devA:STALE", []byte(`{"cached":"stale"}`)); err != nil {
		t.Fatalf("seed stale Complete: %v", err)
	}
	clock = time.Now() // fresh record: well within any sane TTL
	if _, _, err := seed.Prepare("devA:FRESH", "kill", "s2"); err != nil {
		t.Fatalf("seed fresh Prepare: %v", err)
	}
	if err := seed.Complete("devA:FRESH", []byte(`{"cached":"fresh"}`)); err != nil {
		t.Fatalf("seed fresh Complete: %v", err)
	}

	// Open the real daemon: it must open the store with a bounded TTL (>> the 1h
	// command-validity cap) and Compact so the startup replay is bounded.
	d := openDaemon(t, cfg)

	if _, ok := d.idem.Get("devA:STALE"); ok {
		t.Fatalf("stale idempotency record survived daemon Open: the store is unbounded (no TTL, Compact never called) -> the log grows without limit (C4)")
	}
	rec, ok := d.idem.Get("devA:FRESH")
	if !ok {
		t.Fatalf("within-TTL idempotency record was dropped by Compact: a still-valid command's replay guard was GC'd (replay hole reopened, R6 violated)")
	}
	if string(rec.Outcome) != `{"cached":"fresh"}` {
		t.Fatalf("within-TTL record lost its cached outcome after Compact: a replay would RE-EXECUTE instead of returning the cached result; got %q", rec.Outcome)
	}
}

// TestDaemon_IdempotencyCompactionStopsOnClose proves the scheduled compaction
// goroutine is tied to the daemon lifecycle: it is registered on d.wg and returns
// on d.stopCh, so Close (which d.wg.Wait()s) drains it rather than leaking it. If
// the goroutine were unscheduled or ignored stopCh, Close would hang here.
func TestDaemon_IdempotencyCompactionStopsOnClose(t *testing.T) {
	d, err := Open(daemonConfig(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung: the idempotency compaction goroutine did not stop on d.stopCh (goroutine leak)")
	}
}
