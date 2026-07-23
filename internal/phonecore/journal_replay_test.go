// FAILING-FIRST (TDD RED, GG-5) tests for the phone-side journal-receive REPLAY /
// REORDER / GAP guard (review finding HIGH-3, R-PHC.5 + R-JRN.6). An UNTRUSTED relay
// stores the sealed journal envelopes and can replay, reorder, or drop them at will.
// Today the phone opens each envelope with a bare AEAD (OpenJournalEnvelope) that tracks
// no per-(sender,epoch) sequence, and SessionCache.Apply mutates Group/Present/Delete
// state UNCONDITIONALLY -- so a replayed OLDER envelope re-mutates phone state and a
// DROPPED envelope produces a silent gap with no resync signal.
//
// These tests seal REAL envelopes through the gateway RelaySink (the same content key
// the phone holds) so the security property is proven end to end on the receive path,
// not against hand-faked ciphertext. The frozen crypto layer already provides the
// primitive (crypto.MailboxReceiver: per-(sender_key_id, epoch_id) seq monotonicity,
// crypto.ErrStaleSeq on replay/reorder, MailboxResult.Gap on a skip, and SeedHighWater
// for resume); this pins a minimal phonecore seam that runs the receive path through it.
//
// DESIRED API these tests pin as the implementer's contract (undefined today -> the
// package test build fails; that is the RED for a new API):
//
//	// JournalReceiver is the phone's replay/reorder/gap-protected journal receive path.
//	// It wraps a crypto.MailboxReceiver plus the epoch content key: Accept parses one
//	// sealed envelope, authenticates + seq-guards it, and decodes the journal record.
//	type JournalReceiver struct { /* wraps crypto.NewMailboxReceiver() + crypto.ContentKey */ }
//	func NewJournalReceiver(key crypto.ContentKey) *JournalReceiver
//	// Accept opens and seq-guards one sealed envelope. A replayed/reordered seq returns
//	// crypto.ErrStaleSeq and a zero record (the caller must NOT apply it). A valid but
//	// SKIPPED seq returns gap=true alongside the decoded record, so the phone
//	// journal_read-resyncs (R-JRN.6) instead of applying it as if contiguous.
//	func (*JournalReceiver) Accept(raw []byte) (rec protocol.JournalRecord, gap bool, err error)
//	// SeedHighWater seeds the resume high-water mark for a (sender, epoch) stream to a
//	// journal_read snapshot cursor N, so an envelope at seq <= N is rejected on resume.
//	func (*JournalReceiver) SeedHighWater(sender [8]byte, epoch uint32, seq uint64)
//
//	// SessionCache.Apply gains a monotonic-cursor guard AND reports whether it mutated:
//	//   func (*SessionCache) Apply(rec protocol.JournalRecord) (applied bool)
//	// A record whose Cursor is STRICTLY LESS than the highest applied cursor is stale
//	// (replay/reorder) and must NOT mutate Group/Present/Delete state (applied=false).
//	// An EQUAL cursor is NOT stale: a roster snapshot shares one read cursor across all
//	// its sessions, so equal-cursor records still apply.

package phonecore

import (
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
	"github.com/Nathandela/swarm/internal/status"
)

// contentKey builds a deterministic 32-byte epoch content key for a test.
func contentKey(seed byte) crypto.ContentKey {
	var k crypto.ContentKey
	for i := range k {
		k[i] = seed + byte(i)
	}
	return k
}

// sealJournalStream seals recs under key through a real gateway RelaySink and returns
// the marshalled envelopes (envelope Seq is the sink's strictly-increasing counter, so
// recs[i] carries Seq i+1). This is the machine -> E2EE -> relay half of the path; the
// phone's JournalReceiver is the receive half under test.
func sealJournalStream(t *testing.T, key crypto.ContentKey, epoch uint32, sender [8]byte, recs []protocol.JournalRecord) [][]byte {
	t.Helper()
	box := &memMailbox{}
	sink := remotegw.NewRelaySink(remotegw.RelayConfig{
		Appender: box, Target: "phone", EpochID: epoch, Key: key, SenderKeyID: sender,
	})
	for _, rec := range recs {
		sink.Event(rec)
	}
	if err := sink.Err(); err != nil {
		t.Fatalf("relay sink error sealing stream: %v", err)
	}
	return box.envs
}

// TestPhoneCore_ReplayedOlderEnvelopeRejectedAndDoesNotMutateCache (finding HIGH-3):
// deliver seq 1,2,3, then have the untrusted relay REPLAY the seq-2 envelope. The
// receiver must refuse the replay (crypto.ErrStaleSeq) so the phone never re-applies it
// and the cache is NOT rolled back to the stale group the replay would have re-set.
func TestPhoneCore_ReplayedOlderEnvelopeRejectedAndDoesNotMutateCache(t *testing.T) {
	key := contentKey(0x30)
	sender := crypto.KeyID([]byte("machine-A"))
	envs := sealJournalStream(t, key, 5, sender, []protocol.JournalRecord{
		{Cursor: 11, SessionID: "m/s2", Type: "group_transition", Group: status.Group("working")},     // seq 1
		{Cursor: 12, SessionID: "m/s2", Type: "group_transition", Group: status.Group("needs_input")}, // seq 2
		{Cursor: 13, SessionID: "m/s2", Type: "group_transition", Group: status.Group("idle")},         // seq 3
	})

	recv := NewJournalReceiver(key)
	cache := NewSessionCache()
	// The phone loop: open + seq-guard each envelope, apply only what the guard admits.
	for i, raw := range envs {
		rec, gap, err := recv.Accept(raw)
		if err != nil {
			t.Fatalf("env %d accept: %v", i, err)
		}
		if gap {
			t.Fatalf("env %d surfaced a gap on contiguous seq %d", i, i+1)
		}
		cache.Apply(rec)
	}
	if cs, _ := cache.Get("m/s2"); cs.Group != status.Group("idle") {
		t.Fatalf("after seq 1..3 m/s2 group = %q; want idle (set by seq 3)", cs.Group)
	}

	// Relay replays the seq-2 envelope (would re-set m/s2 to needs_input if applied).
	if _, _, err := recv.Accept(envs[1]); !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("replayed seq 2 err = %v; want crypto.ErrStaleSeq (refused, never applied)", err)
	}
	if cs, _ := cache.Get("m/s2"); cs.Group != status.Group("idle") {
		t.Fatalf("replay mutated the cache: m/s2 group = %q; want idle (unchanged)", cs.Group)
	}
}

// TestSessionCache_OutOfOrderCursorNotApplied (finding HIGH-3, defense in depth): even a
// record that reaches SessionCache.Apply out of order must not roll back state -- a
// Cursor strictly below the highest applied cursor is stale and must mutate NOTHING
// (Group, Present, or Delete). An EQUAL cursor is a roster-snapshot sibling and DOES
// apply (else the snapshot half of R-JRN.4 would drop sessions).
func TestSessionCache_OutOfOrderCursorNotApplied(t *testing.T) {
	cache := NewSessionCache()

	// Establish m/s2 present + idle at cursor 12.
	if applied := cache.Apply(protocol.JournalRecord{Cursor: 12, SessionID: "m/s2", Type: "group_transition", Group: status.Group("idle")}); !applied {
		t.Fatal("record at cursor 12 not applied to a fresh cache")
	}

	// Equal cursor is NOT stale: a second session sharing the read cursor still applies.
	if applied := cache.Apply(protocol.JournalRecord{Cursor: 12, SessionID: "m/s3", Type: "roster", Group: status.Group("working")}); !applied {
		t.Error("equal-cursor (12) roster sibling refused; roster snapshots share one cursor and must apply")
	}
	if _, ok := cache.Get("m/s3"); !ok {
		t.Error("equal-cursor roster record not applied; want present")
	}

	// Stale-cursor group_transition (11 < 12) must NOT mutate Group.
	if applied := cache.Apply(protocol.JournalRecord{Cursor: 11, SessionID: "m/s2", Type: "group_transition", Group: status.Group("working")}); applied {
		t.Error("group_transition at stale cursor 11 was applied; want refused (applied=false)")
	}
	if cs, _ := cache.Get("m/s2"); cs.Group != status.Group("idle") {
		t.Errorf("stale cursor mutated Group: got %q, want idle", cs.Group)
	}

	// Stale-cursor delete (11 < 12) must NOT remove the session (Present kept).
	if applied := cache.Apply(protocol.JournalRecord{Cursor: 11, SessionID: "m/s2", Type: "deleted"}); applied {
		t.Error("delete at stale cursor 11 was applied; want refused")
	}
	if _, ok := cache.Get("m/s2"); !ok {
		t.Error("stale-cursor delete removed m/s2; want kept")
	}

	// Stale-cursor new session (11 < 12) must NOT be created (Present not set).
	if applied := cache.Apply(protocol.JournalRecord{Cursor: 11, SessionID: "m/s9", Type: "launched"}); applied {
		t.Error("launched at stale cursor 11 was applied; want refused")
	}
	if _, ok := cache.Get("m/s9"); ok {
		t.Error("stale-cursor record created m/s9; want absent")
	}
}

// TestPhoneCore_DroppedEnvelopeGapDetectedAndSignalsResync (finding HIGH-3, R-JRN.6):
// the relay DROPS seq 2 and delivers seq 3. The skip must be surfaced as a gap so the
// phone journal_read-resyncs, instead of applying seq 3 as if it were contiguous.
func TestPhoneCore_DroppedEnvelopeGapDetectedAndSignalsResync(t *testing.T) {
	key := contentKey(0x50)
	sender := crypto.KeyID([]byte("machine-A"))
	envs := sealJournalStream(t, key, 5, sender, []protocol.JournalRecord{
		{Cursor: 11, SessionID: "m/s1", Type: "launched"}, // seq 1
		{Cursor: 12, SessionID: "m/s2", Type: "launched"}, // seq 2 (DROPPED by relay)
		{Cursor: 13, SessionID: "m/s3", Type: "launched"}, // seq 3
	})

	recv := NewJournalReceiver(key)

	// seq 1: contiguous, no gap.
	if _, gap, err := recv.Accept(envs[0]); err != nil || gap {
		t.Fatalf("seq 1 accept: err=%v gap=%v; want ok and no gap", err, gap)
	}
	// seq 3 after a dropped seq 2: the gap must be surfaced (record still decoded so the
	// caller can act, but gap=true tells it to resync rather than trust contiguity).
	rec, gap, err := recv.Accept(envs[2])
	if err != nil {
		t.Fatalf("seq 3 accept: %v", err)
	}
	if !gap {
		t.Fatal("dropped seq 2 not surfaced: gap=false on seq 3 delivered after seq 1")
	}
	if rec.SessionID != "m/s3" {
		t.Fatalf("seq 3 record = %+v; want the m/s3 record decoded alongside the gap signal", rec)
	}
}

// TestPhoneCore_ResumeSeedRejectsAlreadySeenEnvelope (finding HIGH-3, F4/R-JRN.6): after
// a journal_read snapshot carries the stream through seq 5, the receiver is seeded at
// high-water 5. On resume the relay may replay seq <= 5; those are already seen and must
// be rejected. Only seq 6 (the next contiguous) is accepted.
func TestPhoneCore_ResumeSeedRejectsAlreadySeenEnvelope(t *testing.T) {
	key := contentKey(0x70)
	sender := crypto.KeyID([]byte("machine-A"))
	const epoch uint32 = 5
	envs := sealJournalStream(t, key, epoch, sender, []protocol.JournalRecord{
		{Cursor: 11, SessionID: "m/s1", Type: "launched"}, // seq 1
		{Cursor: 12, SessionID: "m/s2", Type: "launched"}, // seq 2
		{Cursor: 13, SessionID: "m/s3", Type: "launched"}, // seq 3
		{Cursor: 14, SessionID: "m/s4", Type: "launched"}, // seq 4
		{Cursor: 15, SessionID: "m/s5", Type: "launched"}, // seq 5
		{Cursor: 16, SessionID: "m/s6", Type: "launched"}, // seq 6
	})

	recv := NewJournalReceiver(key)
	recv.SeedHighWater(sender, epoch, 5)

	if _, _, err := recv.Accept(envs[2]); !errors.Is(err, crypto.ErrStaleSeq) { // seq 3 < 5
		t.Fatalf("resume re-accepted seq 3 (err=%v); want crypto.ErrStaleSeq", err)
	}
	if _, _, err := recv.Accept(envs[4]); !errors.Is(err, crypto.ErrStaleSeq) { // seq 5 == seeded high-water
		t.Fatalf("resume re-accepted the seeded seq 5 (err=%v); want crypto.ErrStaleSeq", err)
	}
	if _, gap, err := recv.Accept(envs[5]); err != nil || gap { // seq 6 == 5+1
		t.Fatalf("seq 6 after resume seed: err=%v gap=%v; want ok and no gap", err, gap)
	}
}
