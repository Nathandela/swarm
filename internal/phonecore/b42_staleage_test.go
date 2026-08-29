package phonecore

// FAILING-FIRST (TDD RED, GG-5) for ADR-007 B42's fast-clock finding, which the residual
// recorded as a LOSS OF FUNCTION ("the phone goes deaf") and which is worse than that.
//
// WHAT THE CODE DID. On crypto.ErrStaleAge -- PB-TIME-2's ten-minute bounded-age backstop --
// AcceptCommit committed NOTHING and ACKED THE RELAY. The ack is what tells the relay to
// compact, so the only copy of the frame was destroyed, permanently, at the moment it
// arrived; and because the transport was up, App.ConnectionState kept reading "online". A
// phone whose clock ran fast therefore deleted its entire inbound plane as it was delivered,
// reported itself healthy while doing it, and recovered NOTHING when the clock was corrected.
//
// AND THE RELAY CONTROLS THE TRIGGER. The relay is the declared adversary (ADR-007 D9). It
// does not need a wrong phone clock: withholding delivery for ten minutes and then releasing
// makes every released frame breach the bound, so the phone ack-and-discards the lot. That is
// silent, permanent content destruction PERFORMED BY THE VICTIM and reported as health.
//
// THE RULE THESE TESTS PIN, and it is AcceptCommit's own stated invariant read honestly --
// "no frame is both acked and unapplied":
//
//  1. a frame refused for AGE is NOT acked. Nothing was applied, so the relay must keep the
//     only copy. crypto.ErrStaleSeq keeps its ack and MUST keep it: that frame was already
//     applied, so compaction loses nothing.
//  2. correcting the clock RECOVERS the frame. This is the whole difference between a delay
//     and a deletion, and it is the property the ack destroyed.
//  3. while the inbound plane is being refused, the phone does not read healthy.
//
// THE COST, recorded rather than hidden: an age-refused frame that is never acked is never
// compacted, so the drain re-reads it and the mailbox stalls until the relay's own retention
// cap (§6.0, 7 d) drops it. That is the SAME bounded stall an unopenable frame already causes
// (ADR-007 B42's fourth finding) and it is strictly better than what it replaces -- a stall is
// recoverable and loud, a deletion is neither. Rule 3 is what makes it loud.

import (
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// b42StaleFrame seals a command reply that is one minute past PB-TIME-2's bound as of now.
func b42StaleFrame(t *testing.T, seq uint64, now time.Time) []byte {
	t.Helper()
	env, err := crypto.SealMailbox(testContentKey(), crypto.EnvelopeHeader{
		Version:  crypto.VersionV1,
		EpochID:  7,
		Seq:      seq,
		IssuedAt: now.Add(-InboundMaxAge - time.Minute).UnixMilli(),
	}, marshalReply(t, takeControlReply()))
	if err != nil {
		t.Fatalf("seal the stale frame: %v", err)
	}
	return env.Marshal()
}

// TestB42StaleAge_ARefusedFrameIsNeverAcked is the destruction itself.
//
// The ack is not a bookkeeping detail on this path: it is the delete. The phone holds no copy
// (the transaction committed nothing) and the relay holds the only one, so acking is the
// instruction to destroy the frame -- issued by the phone, about content it never read.
func TestB42StaleAge_ARefusedFrameIsNeverAcked(t *testing.T) {
	st := &memStore{}
	seedPaired(t, st)
	ack := &recordingAcker{}
	r := resumeRouter(t, st, ack)
	now := time.UnixMilli(1_784_000_000_000)

	rcpt, err := r.AcceptCommitAt(b42StaleFrame(t, 1, now), 11, now)
	if !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("AcceptCommitAt on a frame past the %v bound = %v, want crypto.ErrStaleAge; "+
			"the bound is not live and every assertion below is vacuous", InboundMaxAge, err)
	}
	if rcpt.Acked {
		t.Errorf("the receipt reports Acked for a frame the phone refused and never applied")
	}
	if len(ack.acked) != 0 {
		t.Errorf("the relay was acked at cursor(s) %v for a frame nothing committed.\n"+
			"The ack is the DELETE: the phone kept no copy and the relay held the only one, so "+
			"this is the phone instructing the relay to destroy content it never read. A relay "+
			"that withholds delivery for %v and then releases makes the phone do this to its "+
			"whole inbound plane.", ack.acked, InboundMaxAge)
	}
	if got := st.Load().RelayCursor; got != 0 {
		t.Errorf("the durable relay cursor moved to %d over a refused frame; the drain must not "+
			"advance past content it did not take", got)
	}
	if r.Replies().Len() != 0 {
		t.Errorf("the reply cache holds %d entries after a refusal; a fail-closed refusal "+
			"commits no content", r.Replies().Len())
	}
}

// TestB42StaleAge_AlreadyAppliedFramesKeepTheirAck is the other side, and it is why the fix
// is not "stop acking refusals".
//
// crypto.ErrStaleSeq means the durable high-water has ALREADY taken this frame. Its content is
// safe on disk, so compacting the relay's copy loses nothing -- and NOT acking it is the
// failure the ack was added for: the phone re-reads the same item forever while the mailbox
// never compacts. The two refusals differ in exactly one fact -- whether the content survived
// -- and that fact is what decides the ack.
func TestB42StaleAge_AlreadyAppliedFramesKeepTheirAck(t *testing.T) {
	st := &memStore{}
	seedPaired(t, st)
	ack := &recordingAcker{}
	r := resumeRouter(t, st, ack)

	raw := sealFrame(t, testContentKey(), 1, marshalReply(t, takeControlReply()))
	if _, err := r.AcceptCommit(raw, 11); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	rcpt, err := r.AcceptCommit(raw, 12)
	if !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("redelivery = %v, want crypto.ErrStaleSeq", err)
	}
	if !rcpt.Acked {
		t.Errorf("an already-applied frame was not acked. Its content is durable, so the ack " +
			"destroys nothing -- and without it the phone re-reads the same item for the whole " +
			"retention window while the relay mailbox never compacts (PB-SYNC-6).")
	}
	if len(ack.acked) != 2 {
		t.Errorf("acked cursors = %v, want both the applied frame and its redelivery", ack.acked)
	}
}

func TestB42StaleAge_AlreadyAppliedOldFrameCompactsAfterCursorRecovery(t *testing.T) {
	st := &memStore{}
	seedPaired(t, st)
	ack := &recordingAcker{}
	r := resumeRouter(t, st, ack)
	issued := time.UnixMilli(1_784_000_000_000)
	env, err := crypto.SealMailbox(testContentKey(), crypto.EnvelopeHeader{
		Version: crypto.VersionV1, EpochID: 7, Seq: 1, IssuedAt: issued.UnixMilli(),
	}, marshalReply(t, takeControlReply()))
	if err != nil {
		t.Fatal(err)
	}
	raw := env.Marshal()
	if _, err := r.AcceptCommitAt(raw, 1, issued); err != nil {
		t.Fatalf("initial delivery: %v", err)
	}

	rcpt, err := r.AcceptCommitAt(raw, 2, issued.Add(InboundMaxAge+time.Minute))
	if !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("old replay = %v, want ErrStaleAge surfaced", err)
	}
	if !rcpt.Acked || len(ack.acked) != 2 || ack.acked[1] != 2 {
		t.Fatalf("durably applied old replay was not compacted: receipt=%+v acks=%v", rcpt, ack.acked)
	}
}

// TestB42StaleAge_CorrectingTheClockRecoversTheFrame is the property the ack destroyed, and
// the one the residual's "loss of function" wording missed entirely.
//
// A phone whose clock runs fast reads every freshly-sealed machine frame as older than the
// bound. The frame is FINE; the phone's reading of it is not. So the refusal must be
// reversible: with the clock corrected, the very same envelope is accepted and its content
// lands. Under the old behaviour the relay had already been told to delete it, and correcting
// the clock recovered nothing.
func TestB42StaleAge_CorrectingTheClockRecoversTheFrame(t *testing.T) {
	st := &memStore{}
	seedPaired(t, st)
	ack := &recordingAcker{}
	r := resumeRouter(t, st, ack)

	machineSealedAt := time.UnixMilli(1_784_000_000_000)
	raw := b42StaleFrame(t, 1, machineSealedAt.Add(InboundMaxAge+time.Minute))

	// The phone's clock is 11 minutes fast, so the frame reads as past the bound.
	fast := machineSealedAt.Add(InboundMaxAge + time.Minute)
	if _, err := r.AcceptCommitAt(raw, 11, fast); !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("with a fast clock the frame was not refused (err = %v)", err)
	}

	// The user corrects the clock. The relay still holds the frame BECAUSE IT WAS NOT ACKED,
	// so the next drain re-serves it -- at whatever storage cursor the relay chooses.
	rcpt, err := r.AcceptCommitAt(raw, 12, machineSealedAt)
	if err != nil {
		t.Fatalf("with the clock corrected the same envelope was refused with %v.\n"+
			"A bounded-age refusal is a verdict about the PHONE's reading of the frame, not "+
			"about the frame, so it must be reversible -- otherwise a wrong clock destroys "+
			"content that was always intact", err)
	}
	if !rcpt.Acked {
		t.Errorf("the recovered frame was not acked; it is applied and durable now, so the " +
			"relay's copy must be released")
	}
	if r.Replies().Len() != 1 {
		t.Errorf("the reply cache holds %d entries after recovery, want 1 -- the content the "+
			"fast clock deferred must actually arrive", r.Replies().Len())
	}
}

// TestB42StaleAge_TheInboundPlaneIsNotReportedHealthy is the second half of the finding: the
// destruction was reported as health.
//
// The transport is up, so nothing in the connection state machine had anything to say -- and
// "online" is what a screen renders as "Connected to your machine." while every frame the
// machine sends is being thrown away. The condition is LIVE rather than durable: it describes
// the drain that is happening now, and it must clear the moment a frame is accepted, or one
// straggler pins the phone in an unhealthy state forever (PB-STATE-10 forbids a latch).
func TestB42StaleAge_TheInboundPlaneIsNotReportedHealthy(t *testing.T) {
	st := &memStore{}
	seedPaired(t, st)
	c, err := Resume(Config{State: st, Ack: &recordingAcker{}})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	r := c.Router()
	now := time.UnixMilli(1_784_000_000_000)

	if r.InboundAgeRefused() {
		t.Fatalf("a router that has seen nothing reports its inbound plane as refused")
	}
	if _, err := r.AcceptCommitAt(b42StaleFrame(t, 1, now), 11, now); !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("AcceptCommitAt = %v, want crypto.ErrStaleAge", err)
	}
	if !r.InboundAgeRefused() {
		t.Errorf("the router reports a healthy inbound plane while discarding every frame that " +
			"reaches it.\nThat is the half of this defect that makes it silent: the transport is " +
			"up, so the connection state machine has nothing to say, and the user is told the " +
			"machine is connected while its output is being deleted on arrival.")
	}

	// A frame that is ACCEPTED says the plane works, so the condition clears with it.
	if _, err := r.AcceptCommit(sealFrame(t, testContentKey(), 1, marshalReply(t, takeControlReply())), 12); err != nil {
		t.Fatalf("a fresh frame after the refusal: %v", err)
	}
	if r.InboundAgeRefused() {
		t.Errorf("the condition survived a frame the phone accepted. It describes the CURRENT " +
			"drain, and a latch would leave a phone reporting itself broken forever over one " +
			"straggler -- which is the brick PB-STATE-10 forbids")
	}
}
