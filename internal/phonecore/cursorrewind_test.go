package phonecore

// The relay MINTS State.RelayCursor -- relay.Item.Cursor is "the relay's own monotonic
// storage cursor (UNTRUSTED ordering)" -- and the phone adopts it as the durable point its
// next read resumes from. fileStore.mergeGuards raises it monotonically, grouped with the
// replay guards, so nothing that goes through Save can ever lower it. That is right for an
// ordinary writer arriving with a stale snapshot, and it is exactly what made a poisoned
// cursor permanent: one item forwarded past every real cursor and the phone never reads
// anything again, across restarts, with the state directory the only way out.
//
// RewindRelayCursor is the one act that may lower it. These tests pin both halves: the
// rewind reaches disk, and the merge rule it defeats is still there for everyone else.

import (
	"sync"
	"testing"
)

// TestRewindRelayCursor_ReachesDiskThroughTheMonotonicMerge is the seam, on the REAL
// fileStore -- a memStore has no merge rule to defeat and would prove nothing.
func TestRewindRelayCursor_ReachesDiskThroughTheMonotonicMerge(t *testing.T) {
	dir, wake, content, _ := s14aR2Sealed(t)
	core := s14aR2Resume(t, dir, wake, content)

	const poisoned = uint64(1) << 63
	st := core.State()
	st.RelayCursor = poisoned
	if err := core.Save(st); err != nil {
		t.Fatalf("premise Save: %v", err)
	}
	if got := core.State().RelayCursor; got != poisoned {
		t.Fatalf("premise: RelayCursor = %d, want %d", got, poisoned)
	}

	if err := core.RewindRelayCursor(); err != nil {
		t.Fatalf("RewindRelayCursor: %v", err)
	}
	if got := core.State().RelayCursor; got != 0 {
		t.Fatalf("after RewindRelayCursor the in-memory cursor is %d, want 0", got)
	}

	// DURABLE, or the next process death restores the poison. On Android that is routine.
	reopened := s14aR2Resume(t, dir, wake, content)
	if got := reopened.State().RelayCursor; got != 0 {
		t.Fatalf("the rewind did not reach disk: a reopened core resumes at %d, want 0. "+
			"fileStore.mergeGuards raises RelayCursor monotonically, so a rewind that goes "+
			"through an ordinary Save is silently undone by custody", got)
	}
}

// TestRewindRelayCursor_DoesNotWeakenTheMergeRuleForOrdinaryWrites is the other half: the
// monotonic merge exists because a writer holding a stale snapshot must not rewind the read
// position behind a concurrent one, and adding an explicit rewind must not turn that off.
func TestRewindRelayCursor_DoesNotWeakenTheMergeRuleForOrdinaryWrites(t *testing.T) {
	dir, wake, content, _ := s14aR2Sealed(t)
	core := s14aR2Resume(t, dir, wake, content)

	stale := core.State() // the snapshot a slow writer is holding

	ahead := core.State()
	ahead.RelayCursor = 500
	if err := core.Save(ahead); err != nil {
		t.Fatalf("Save(500): %v", err)
	}

	stale.RelayCursor = 7 // the slow writer lands, behind
	if err := core.Save(stale); err != nil {
		t.Fatalf("Save(stale): %v", err)
	}
	if got := core.State().RelayCursor; got != 500 {
		t.Fatalf("an ordinary Save rewound the read cursor to %d: custody must still refuse "+
			"every lowering that is not an explicit RewindRelayCursor", got)
	}
}

func TestRewindRelayCursor_StaleConcurrentWriterCannotRestoreRetiredGeneration(t *testing.T) {
	dir, wake, content, _ := s14aR2Sealed(t)
	core := s14aR2Resume(t, dir, wake, content)

	seed := core.State()
	seed.RelayCursor = 53
	seed.RelayIncarnation = "AAAAAAAAAAAAAAAAAAAAAA"
	if err := core.Save(seed); err != nil {
		t.Fatalf("seed continuity: %v", err)
	}
	stale := core.State() // captured by a writer before recovery begins

	writerReady := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	var once sync.Once
	go func() {
		once.Do(func() { close(writerReady) })
		<-releaseWriter
		writerDone <- core.Save(stale)
	}()
	<-writerReady
	if err := core.RewindRelayCursor(); err != nil {
		t.Fatalf("RewindRelayCursor: %v", err)
	}
	if err := core.SetRelayIncarnation("AQAAAAAAAAAAAAAAAAAAAA"); err != nil {
		t.Fatalf("SetRelayIncarnation: %v", err)
	}
	close(releaseWriter) // the pre-recovery writer finishes last
	if err := <-writerDone; err != nil {
		t.Fatalf("stale concurrent Save: %v", err)
	}

	got := core.State()
	if got.RelayCursor != 0 || got.RelayIncarnation != "AQAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("stale writer restored retired continuity: cursor=%d incarnation=%q", got.RelayCursor, got.RelayIncarnation)
	}
	reopened := s14aR2Resume(t, dir, wake, content).State()
	if reopened.RelayCursor != 0 || reopened.RelayIncarnation != "AQAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("stale writer reached disk: cursor=%d incarnation=%q", reopened.RelayCursor, reopened.RelayIncarnation)
	}
}
