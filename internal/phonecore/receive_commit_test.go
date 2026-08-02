package phonecore

// FAILING-FIRST (TDD RED, GG-5) tests for PB-STATE-7: the phone's RECEIVE path commits
// atomically. Today the high-water advances inside crypto's Accept (envelope.go:254), the
// caches mutate afterwards (snapshot.go:201), and the relay cursor/ack come later still --
// three independent steps with no transaction across them. A crash in between either
// loses a frame forever (the durable high-water stale-drops it on redelivery, so it is
// never applied) or, if the order is reversed, permits a replay.
//
// The rule: {receive high-water, relay cursor, decoded cache mutation, stale flags}
// commit as ONE durable transaction BEFORE the ack, and the ack is idempotent on retry.
// The two invariants, stated so a test can refuse each: NO FRAME IS BOTH ACKED AND
// UNAPPLIED, and NO FRAME IS APPLIED TWICE.
//
// THE SEAM THESE TESTS PIN:
//
//	type Acker interface{ Ack(cursor uint64) error }
//	type Receipt struct{ Gap bool; Acked bool }
//	func (*MailboxRouter) AcceptCommit(raw []byte, cursor uint64) (Receipt, error)
//	func (*MailboxRouter) Stale(Bucket) bool
//
// The Acker is INJECTED because internal/remote/relay is outside the bound dependency
// closure (PB-BIND-0, deps_allowlist.txt): phonecore must not import the relay client.
//
// The payload is a command_reply throughout, deliberately. ReplyCache.Append is the only
// inbound mutation whose DOUBLE application is observable -- SessionCache.Apply and
// SnapshotCache.Apply are both idempotent/latest-wins, so a test built on them would pass
// while the frame was applied twice.

import (
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// ---------------------------------------------------------------------------
// Shared S7 helpers.
// ---------------------------------------------------------------------------

// gatewayReceiver is the machine side of the phone -> machine mailbox: the REAL
// crypto.MailboxReceiver the gateway builds, so every phone-side assertion about
// acceptance and about the Gap bit is made at the guard that actually enforces it.
func gatewayReceiver() *crypto.MailboxReceiver { return crypto.NewMailboxReceiver() }

// takeControlAuth is a stand-in signed command tuple. Only its shape matters here; the
// daemon verifies signatures, and these tests never reach the daemon.
func takeControlAuth() protocol.DeviceCommandAuth {
	return protocol.DeviceCommandAuth{
		Action:      protocol.ActionTakeControl,
		Machine:     "m1",
		Session:     "m1/s1",
		OperationID: "op-take-1",
	}
}

// takeControlReply is the daemon's durable outcome for that operation id.
func takeControlReply() protocol.Control {
	return protocol.Control{Op: protocol.OpOK, SessionID: "m1/s1", OperationID: "op-take-1"}
}

// recordingAcker records the cursors it was asked to ack, and can be made to fail --
// "the process died before the relay ack", the retainingRelay idiom of
// remotegw/inbound_crash_matrix_test.go.
type recordingAcker struct {
	acked []uint64
	fail  bool
}

var errAckDied = errors.New("phonecore test: the process died before the relay ack")

func (a *recordingAcker) Ack(cursor uint64) error {
	if a.fail {
		return errAckDied
	}
	a.acked = append(a.acked, cursor)
	return nil
}

// resumeRouter builds a Core over st (with ack) and returns its router. Calling it twice
// over the SAME Store is a restart: the process died, the state file did not.
func resumeRouter(t *testing.T, st Store, ack Acker) *MailboxRouter {
	t.Helper()
	c, err := Resume(Config{State: st, Ack: ack})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	return c.Router()
}

// pairedState is a State already carrying the epoch material the router needs, so the
// tests below start from a paired phone rather than a blank one.
func pairedState() State {
	return State{Machine: "m1", EpochID: 7, Keys: crypto.EpochKeys{ContentKey: testContentKey()}}
}

// seedPaired writes pairedState into st.
func seedPaired(t *testing.T, st Store) {
	t.Helper()
	if err := st.Save(pairedState()); err != nil {
		t.Fatalf("seed paired state: %v", err)
	}
}

// ---------------------------------------------------------------------------

// TestReceiveCommit_AckNeverPrecedesTheCommit is the direct statement of "no frame is
// both acked and unapplied". The durable commit fails, so nothing was applied; the ack
// must therefore never be issued, because acking tells the relay to compact away the only
// copy of a frame the phone has not recorded.
func TestReceiveCommit_AckNeverPrecedesTheCommit(t *testing.T) {
	st := &memStore{}
	seedPaired(t, st)
	failing := &failAfterNStore{inner: st, n: 0}
	ack := &recordingAcker{}
	r := resumeRouter(t, failing, ack)

	raw := sealFrame(t, testContentKey(), 1, marshalReply(t, takeControlReply()))
	rcpt, err := r.AcceptCommit(raw, 11)
	if !errors.Is(err, errStoreDied) {
		t.Fatalf("AcceptCommit with a failing commit = %v; want the persist error surfaced (S2's N3: a silent drop leaves no signal at all)", err)
	}
	if rcpt.Acked {
		t.Fatalf("Receipt reports Acked after the commit failed")
	}
	if len(ack.acked) != 0 {
		t.Fatalf("the relay was acked at cursor(s) %v although nothing was committed; that frame is now unrecoverable and unapplied", ack.acked)
	}
	if r.Replies().Len() != 0 {
		t.Fatalf("reply cache holds %d entries after a failed commit; the cache mutation must be part of the transaction, not before it", r.Replies().Len())
	}
}

// TestReceiveCommit_CrashBeforeTheCommitAppliesExactlyOnceOnRedelivery is boundary 1: the
// process died after authenticating and before anything hit disk. Nothing was acked, so
// the relay re-serves the frame at a FRESH storage cursor (a hostile or merely un-purged
// relay re-appends the identical envelope), and the restarted phone must apply it -- once.
func TestReceiveCommit_CrashBeforeTheCommitAppliesExactlyOnceOnRedelivery(t *testing.T) {
	st := &memStore{}
	seedPaired(t, st)
	raw := sealFrame(t, testContentKey(), 1, marshalReply(t, takeControlReply()))

	r1 := resumeRouter(t, &failAfterNStore{inner: st, n: 0}, &recordingAcker{})
	if _, err := r1.AcceptCommit(raw, 11); !errors.Is(err, errStoreDied) {
		t.Fatalf("run 1 AcceptCommit = %v; want the injected crash", err)
	}

	// RESTART against the surviving state file. The relay re-serves at cursor 21.
	ack2 := &recordingAcker{}
	r2 := resumeRouter(t, st, ack2)
	if _, err := r2.AcceptCommit(raw, 21); err != nil {
		t.Fatalf("redelivered frame after a crash BEFORE the commit = %v; want it applied (a frame that was never recorded must not be stale-dropped and lost forever)", err)
	}
	if n := r2.Replies().Len(); n != 1 {
		t.Fatalf("reply cache holds %d entries; want exactly 1 (applied once, never twice)", n)
	}
	if len(ack2.acked) != 1 || ack2.acked[0] != 21 {
		t.Fatalf("acked cursors = %v; want exactly [21] after the commit landed", ack2.acked)
	}
	if got := st.Load().RelayCursor; got != 21 {
		t.Fatalf("persisted relay cursor = %d; want 21 (the cursor is part of the same transaction)", got)
	}
}

// TestReceiveCommit_CrashBeforeTheAckIsIdempotentAndNeverAppliesTwice is boundary 2 and
// the sharper half: the transaction committed, the ack did not. The relay still holds the
// frame and re-serves it. The restarted phone must REFUSE it at the durable high-water
// (already applied) AND still ack it, or the phone re-reads the same item forever while
// the mailbox never compacts.
func TestReceiveCommit_CrashBeforeTheAckIsIdempotentAndNeverAppliesTwice(t *testing.T) {
	st := &memStore{}
	seedPaired(t, st)
	raw := sealFrame(t, testContentKey(), 1, marshalReply(t, takeControlReply()))

	failingAck := &recordingAcker{fail: true}
	r1 := resumeRouter(t, st, failingAck)
	rcpt, err := r1.AcceptCommit(raw, 11)
	if !errors.Is(err, errAckDied) {
		t.Fatalf("run 1 AcceptCommit = %v; want the ack failure surfaced, not swallowed", err)
	}
	if rcpt.Acked {
		t.Fatalf("Receipt reports Acked although the ack failed")
	}
	if got := st.Load().Receive[replyBucket(7)]; got != 1 {
		t.Fatalf("persisted receive high-water = %d after a committed frame; want 1 -- the commit must land BEFORE the ack, so a failed ack cannot un-apply it", got)
	}

	// RESTART. The transaction committed, so rebind restores its DURABLE outcome into the
	// delivery FIFO before the relay re-serves anything -- that restored entry is the
	// mechanism PB-SYNC-2 needs (an op is resolved by its durable outcome), not an
	// application of the redelivered frame. Pinning it here is what makes the
	// "applied twice" assertion below unambiguous: it measures the DELTA the redelivery
	// causes, which must be zero.
	ack2 := &recordingAcker{}
	r2 := resumeRouter(t, st, ack2)
	restored := r2.Replies().Len()
	if restored != 1 {
		t.Fatalf("reply cache after the restart holds %d replies; want exactly 1 restored from the durable outcome (the commit landed BEFORE the failed ack, so the reply survived the process)", restored)
	}
	rcpt2, err := r2.AcceptCommit(raw, 21)
	if !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("redelivered frame after a crash BEFORE the ack = %v; want crypto.ErrStaleSeq (it was already applied; applying it twice duplicates the reply)", err)
	}
	if !rcpt2.Acked {
		t.Fatalf("an already-applied frame was refused WITHOUT being acked; the phone re-reads it forever and the relay mailbox never compacts")
	}
	if len(ack2.acked) != 1 || ack2.acked[0] != 21 {
		t.Fatalf("acked cursors on the idempotent retry = %v; want [21]", ack2.acked)
	}
	if n := r2.Replies().Len(); n != restored {
		t.Fatalf("the restarted router applied %d further replies from a frame it had already applied; want 0 (cache went %d -> %d)", n-restored, restored, n)
	}
	// The durable half of the same statement: ONE operation, ONE recorded outcome. A second
	// application would either append a duplicate above or re-key the outcome here.
	if got := st.Load().OpOutcomes; len(got) != 1 || got["op-take-1"].OperationID != "op-take-1" {
		t.Fatalf("persisted outcomes = %+v; want exactly one, for op-take-1 (applied once, never twice)", got)
	}
}

// TestReceiveCommit_CacheAndStaleFlagsAreInTheSameTransaction pins the two remaining
// members of the transaction PB-STATE-7 enumerates. A commit that persisted only the
// high-water and cursor would pass the tests above and still lose the decoded frame and
// the stale flag on a crash -- which is how a phone ends up believing a stream is fresh
// while the content that would have proved otherwise is gone.
func TestReceiveCommit_CacheAndStaleFlagsAreInTheSameTransaction(t *testing.T) {
	st := &memStore{}
	seedPaired(t, st)
	key := testContentKey()

	r1 := resumeRouter(t, st, &recordingAcker{})
	if _, err := r1.AcceptCommit(sealFrame(t, key, 1, marshalReply(t, takeControlReply())), 11); err != nil {
		t.Fatalf("accept reply: %v", err)
	}
	if _, err := r1.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, 1, marshalSnapshot(t, "m1/s1", []string{"$ ls"}, 80, 24)), 12); err != nil {
		t.Fatalf("accept snapshot: %v", err)
	}

	// RESTART. Everything the transaction covered must still be there.
	r2 := resumeRouter(t, st, &recordingAcker{})
	if n := r2.Replies().Len(); n != 1 {
		t.Fatalf("reply cache after restart holds %d entries; want 1 (the decoded cache mutation is part of the commit)", n)
	}
	snap, ok := r2.Snapshots().Get("m1/s1")
	if !ok || len(snap.Lines) != 1 || snap.Lines[0] != "$ ls" {
		t.Fatalf("snapshot cache after restart = %+v (ok=%v); want the committed grid", snap, ok)
	}
	// Both buckets advanced, independently. A scalar high-water would have let the reply
	// bucket's seq 1 stale-drop the journal bucket's seq 1.
	if got := st.Load().Receive[replyBucket(7)]; got != 1 {
		t.Errorf("persisted reply-bucket high-water = %d; want 1", got)
	}
	if got := st.Load().Receive[journalBucket(7)]; got != 1 {
		t.Errorf("persisted journal-bucket high-water = %d; want 1 (the two buckets have independent seq spaces)", got)
	}
}

// TestReceiveCommit_RetainedFrameIsRefusedAfterARestart is the §4.3 mirror direction in
// its plainest form, and the reason this defect is a SECURITY regression and not only a
// liveness one: MailboxReceiver.highest is in-memory, so a process death resets the
// phone's replay high-water to zero, `seen == false` skips the staleness test entirely
// (envelope.go:254-256), and every frame the relay retained is re-accepted.
func TestReceiveCommit_RetainedFrameIsRefusedAfterARestart(t *testing.T) {
	st := &memStore{}
	seedPaired(t, st)
	key := testContentKey()
	retained := sealFrame(t, key, 3, marshalReply(t, takeControlReply()))

	r1 := resumeRouter(t, st, &recordingAcker{})
	if _, err := r1.AcceptCommit(retained, 11); err != nil {
		t.Fatalf("accept the legitimate reply: %v", err)
	}

	// RESTART. The relay never honoured the ack and serves the same envelope again.
	r2 := resumeRouter(t, st, &recordingAcker{})
	if _, err := r2.AcceptCommit(retained, 21); !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("retained frame after restart = %v; want crypto.ErrStaleSeq (the phone's replay high-water must survive the process, or a retaining relay redelivers freely)", err)
	}
	if n := r2.Replies().Len(); n != 0 {
		t.Fatalf("the retained frame was applied again after the restart (%d replies)", n)
	}
	// Live traffic is unaffected: high-water + 1 is still accepted.
	if _, err := r2.AcceptCommit(sealFrame(t, key, 4, marshalReply(t, takeControlReply())), 22); err != nil {
		t.Fatalf("the next legitimate frame = %v; want accepted (a durable guard must not become a wall)", err)
	}
}
