package hookclient

// F1 (G5): the monotonic sequence source. The old contract read SWARM_HOOK_SEQ
// from the spawn-time environment — a CONSTANT across a session's per-event hook
// invocations — so the engine accepted callback #1 and rejected every later one as
// a replay. The fix is a per-session counter FILE (SWARM_HOOK_SEQ_FILE) that each
// `swarm hook` invocation atomically increments, so every invocation gets a
// distinct, strictly increasing sequence even under concurrent processes.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/engine"
)

// N concurrent increments of one counter file yield N distinct, strictly
// increasing sequences (exactly 1..N) with no collision — the property a naive
// append-then-stat would violate.
func TestNextSequenceMonotonicUnderConcurrency(t *testing.T) {
	seqFile := filepath.Join(t.TempDir(), "hook.seq")
	const n = 64
	got := make([]uint64, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			v, err := nextSequence(seqFile)
			if err != nil {
				t.Errorf("nextSequence: %v", err)
				return
			}
			got[i] = v
		}(i)
	}
	close(start)
	wg.Wait()

	seen := make(map[uint64]bool, n)
	for _, v := range got {
		if v == 0 {
			t.Fatalf("a nextSequence returned 0; sequences start at 1")
		}
		if seen[v] {
			t.Fatalf("collision: sequence %d handed out twice (got=%v)", v, got)
		}
		seen[v] = true
	}
	for i := uint64(1); i <= n; i++ {
		if !seen[i] {
			t.Fatalf("missing sequence %d; the counter must produce exactly 1..%d (got=%v)", i, n, got)
		}
	}
}

// With a counter file injected, successive FromEnv invocations (each a separate
// `swarm hook`) produce strictly increasing sequences, and the engine accepts them
// in order while rejecting a replay of an already-accepted one.
func TestCounterSequencesDriveEngineAcceptance(t *testing.T) {
	seqFile := filepath.Join(t.TempDir(), "hook.seq")
	rec := &localRecorder{}
	e := engine.New(engine.Config{
		Now:                time.Now,
		CPUSampler:         func(int) (float64, error) { return 0, nil },
		StalenessThreshold: time.Minute,
		PollInterval:       time.Second,
		Emit:               rec.emit,
	})
	e.RegisterSession("s1", "tok", os.Getpid(), []adapter.SignalSource{{Kind: "hook"}})

	env := map[string]string{EnvSessionID: "s1", EnvToken: "tok", EnvSequenceFile: seqFile}
	getenv := func(k string) string { return env[k] }

	var seqs []uint64
	for _, turn := range []string{"active", "idle", "active"} {
		cb, err := FromEnv(getenv, "e", map[string]string{"turn": turn})
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		if err := e.HandleCallback(cb); err != nil {
			t.Fatalf("HandleCallback seq=%d: %v (per-event callbacks must not be rejected as replays)", cb.Sequence, err)
		}
		seqs = append(seqs, cb.Sequence)
	}
	if !(seqs[0] < seqs[1] && seqs[1] < seqs[2]) {
		t.Fatalf("sequences not strictly increasing: %v", seqs)
	}

	// A replay of an already-accepted sequence is rejected by the engine.
	replay := engine.Callback{SessionID: "s1", Token: "tok", Sequence: seqs[0], Event: "e", Payload: map[string]string{"turn": "idle"}}
	if err := e.HandleCallback(replay); err == nil {
		t.Fatalf("replayed seq=%d: got nil error, want rejection", seqs[0])
	}
}

// The legacy SWARM_HOOK_SEQ env fallback still composes a callback when no counter
// file is injected, so the frozen FromEnv contract holds; a counter file, when
// present, takes precedence.
func TestSequenceFileTakesPrecedenceOverEnvInt(t *testing.T) {
	seqFile := filepath.Join(t.TempDir(), "hook.seq")
	env := map[string]string{
		EnvToken:        "tok",
		EnvSequence:     "99", // legacy fixed value; must be ignored when the file is present
		EnvSequenceFile: seqFile,
	}
	cb, err := FromEnv(func(k string) string { return env[k] }, "e", nil)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cb.Sequence != 1 {
		t.Fatalf("first counter-file sequence = %d, want 1 (env int 99 must not win)", cb.Sequence)
	}
}
