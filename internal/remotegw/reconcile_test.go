package remotegw

// FAILING-FIRST (TDD RED, GG-5) tests for the GATEWAY half of PB-SYNC-7 (6.6): the
// machine seals a RECONCILE RECORD onto the EXISTING machine->phone outbound stream,
// carrying the three rollback authorities PB-STATE-4 (6.1) names. The carrier is
// deliberate: a phone-INITIATED signed reconcile walks straight into PB-SYNC-5's trap
// (actionClass is a closed fail-closed switch in internal/skeleton/deviceauth.go and
// rec.Capability is pinned at enrollment, never read from the wire), so the record
// rides outbound instead and needs no new signed device action. internal/remote/crypto
// stays FROZEN -- it is an ordinary sealed mailbox plaintext with a kind tag.
//
// THE SEAMS these tests pin (undefined symbols -> compile-fail RED):
//
//	// The authorities the sink cannot know itself. Errors, not zeros: an unreachable
//	// authority must fail closed, because a fabricated 0 raises no phone high-water and
//	// silently re-opens every retained pre-rollback frame.
//	type ReconcileSource interface {
//	    InboundHighWater() (uint64, error)          // (a) PB-GW-1's durable inbound accepted high-water
//	    ReplyCeiling() (uint64, error)              // (b) the command-reply bucket (outbound-reply.seq)
//	    GrantWatermark() (epoch uint32, seq uint64, err error) // (c) the daemon's grant-issuance coordinate
//	}
//	RelayConfig gains: Machine string; Authorities ReconcileSource
//	func (*RelaySink) Reconcile() error              // seal one record onto the shared stream
//	SeqSource gains: Issued() uint64                 // safe watermark (see the Issued test below)
//
// (b)'s journal/terminal half is NOT in the interface: it is the sink's own seq, which
// only the sink knows. (a)'s production implementation reads S2's InboundState --
// Load().Highest[InboundStream{Sender: [8]byte{}, Epoch: cfg.EpochID}], sender-zero
// because every phone->machine seal leaves SenderKeyID unset (phonecore input.go /
// command.go).

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// wireReconcileFrame is the committed sealed plaintext, byte-identical to the shape the
// phone decoder demuxes (pinned independently in internal/phonecore/reconcile_test.go
// and internal/protocol/schema/reconcile_test.go). Same discipline as
// TestRelaySink_ForwardsTerminalSnapshot: the bytes are the contract.
const wireReconcileFrame = `{"kind":"reconcile","machine":"m1","epoch_id":7,"inbound_high_water":42,"journal_ceiling":3,"reply_ceiling":5,"grant_epoch":7,"grant_seq":2,"issued_at":1700000000000}`

// stubReconcileSource is a fixed set of authorities, or a uniform failure.
type stubReconcileSource struct {
	inbound    uint64
	reply      uint64
	grantEpoch uint32
	grantSeq   uint64
	err        error
}

func (s stubReconcileSource) InboundHighWater() (uint64, error) {
	if s.err != nil {
		return 0, s.err
	}
	return s.inbound, nil
}

func (s stubReconcileSource) ReplyCeiling() (uint64, error) {
	if s.err != nil {
		return 0, s.err
	}
	return s.reply, nil
}

func (s stubReconcileSource) GrantWatermark() (uint32, uint64, error) {
	if s.err != nil {
		return 0, 0, s.err
	}
	return s.grantEpoch, s.grantSeq, nil
}

// testAuthorities are the authorities wireReconcileFrame carries.
var testAuthorities = stubReconcileSource{inbound: 42, reply: 5, grantEpoch: 7, grantSeq: 2}

// newReconcileSink is newTestRelaySink plus the reconcile wiring (machine id +
// authorities) and an explicit seq source, so a test can supply a DURABLE one.
func newReconcileSink(app MailboxAppender, key crypto.ContentKey, src ReconcileSource, seq SeqSource) *RelaySink {
	fixed := time.Unix(1_700_000_000, 0)
	return NewRelaySink(RelayConfig{
		Appender:       app,
		Target:         "phone-routing-id",
		Machine:        "m1",
		EpochID:        7,
		Key:            key,
		RecipientKeyID: [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
		SenderKeyID:    [8]byte{9, 10, 11, 12, 13, 14, 15, 16},
		Now:            func() time.Time { return fixed },
		Seq:            seq,
		Authorities:    src,
	})
}

func reconcileTestKey() crypto.ContentKey {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

// openPlaintext parses and opens one appended envelope, returning the header and plaintext.
func openPlaintext(t *testing.T, key crypto.ContentKey, raw []byte) (crypto.EnvelopeHeader, []byte) {
	t.Helper()
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		t.Fatalf("open envelope: %v", err)
	}
	return env.Header, plain
}

// TestRelaySink_ReconcileSealsThePinnedWireShape: Reconcile seals ONE record in the
// committed shape onto the SHARED journal/terminal stream -- the machine's SenderKeyID
// and the journal seq allocator, not a stream of its own. Riding the shared bucket is
// what makes JournalCeiling self-certifying (see the ceiling test below).
func TestRelaySink_ReconcileSealsThePinnedWireShape(t *testing.T) {
	key := reconcileTestKey()
	app := &fakeAppender{}
	sink := newReconcileSink(app, key, testAuthorities, nil)

	// Two journal records first: the reconcile record must draw seq 3 from the SAME
	// allocator, and publish 3 as the shared bucket's ceiling.
	if err := sink.Event(protocol.JournalRecord{Cursor: 1, SessionID: "s1", Type: "launched"}); err != nil {
		t.Fatalf("journal event 1: %v", err)
	}
	if err := sink.Event(protocol.JournalRecord{Cursor: 2, SessionID: "s2", Type: "launched"}); err != nil {
		t.Fatalf("journal event 2: %v", err)
	}
	if err := sink.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(app.envs) != 3 {
		t.Fatalf("appended %d envelopes; want 3 (two journal records + one reconcile record)", len(app.envs))
	}
	hdr, plain := openPlaintext(t, key, app.envs[2])
	if string(plain) != wireReconcileFrame {
		t.Fatalf("reconcile plaintext =\n  %s\nwant\n  %s", plain, wireReconcileFrame)
	}
	if hdr.EpochID != 7 || hdr.Seq != 3 {
		t.Fatalf("reconcile header = epoch %d seq %d; want epoch 7 seq 3 (the shared journal stream)", hdr.EpochID, hdr.Seq)
	}
	if hdr.SenderKeyID != [8]byte{9, 10, 11, 12, 13, 14, 15, 16} {
		t.Fatalf("reconcile SenderKeyID = %v; want the machine key id (same bucket as journal/terminal)", hdr.SenderKeyID)
	}
	// One clock, no divergence: the record's issued_at is the envelope's issued_at, so a
	// consumer that sees only the plaintext (MailboxResult carries no header) reads the
	// same time PB-TIME-1 would skew-check.
	if hdr.IssuedAt != 1700000000000 {
		t.Fatalf("envelope IssuedAt = %d; want the record's issued_at (1700000000000)", hdr.IssuedAt)
	}
}

// TestRelaySink_ReconcileCeilingIsTheHighestIssuedSeq is the trap PB-SYNC-7's schema has
// to avoid, and the reason JournalCeiling is not simply "the durable ceiling".
//
// durableSeq persists a RESERVATION ceiling one block (seqReserveBlock = 64) ahead of
// what it has issued. Publishing that number would make the phone seed its shared-bucket
// high-water up to 63 seqs ABOVE the last frame actually sent -- and then stale-drop
// every legitimate frame until the gateway caught up. The authority must therefore be
// the highest seq ISSUED, which for a record sealed under the sink's own lock is the
// record's OWN seq. Two properties, asserted together:
//
//	JournalCeiling == the reconcile envelope's seq   (self-certifying, safe to seed)
//	the next frame  == JournalCeiling + 1            (seeding drops no live traffic)
func TestRelaySink_ReconcileCeilingIsTheHighestIssuedSeq(t *testing.T) {
	key := reconcileTestKey()
	app := &fakeAppender{}
	seq, err := OpenSeqSource(filepath.Join(t.TempDir(), "outbound-journal.seq"))
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	sink := newReconcileSink(app, key, testAuthorities, seq)

	if err := sink.Event(protocol.JournalRecord{Cursor: 1, SessionID: "s1", Type: "launched"}); err != nil {
		t.Fatalf("journal event: %v", err)
	}
	if err := sink.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := sink.Event(protocol.JournalRecord{Cursor: 2, SessionID: "s2", Type: "launched"}); err != nil {
		t.Fatalf("journal event after reconcile: %v", err)
	}

	hdr, plain := openPlaintext(t, key, app.envs[1])
	var got struct {
		JournalCeiling uint64 `json:"journal_ceiling"`
	}
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("decode reconcile plaintext: %v", err)
	}
	if got.JournalCeiling != hdr.Seq {
		t.Fatalf("journal_ceiling = %d but the record's own seq is %d; the authority must be the highest ISSUED seq, never the durable reservation ceiling (seeding the phone there stale-drops a whole block of live frames)", got.JournalCeiling, hdr.Seq)
	}
	nextHdr, _ := openPlaintext(t, key, app.envs[2])
	if nextHdr.Seq != got.JournalCeiling+1 {
		t.Fatalf("frame after the reconcile record has seq %d; want ceiling+1 = %d (a phone seeded at the ceiling must still accept it)", nextHdr.Seq, got.JournalCeiling+1)
	}
}

// TestSeqSource_IssuedIsASafeWatermarkAcrossRestart pins the contract that makes
// ReplyCeiling implementable without the same trap: Issued() is >= every seq already
// handed out and < every seq that will ever be handed out, ACROSS A RESTART. It is
// asserted as that two-sided property rather than against seqReserveBlock, so the block
// size stays a tuning knob.
func TestSeqSource_IssuedIsASafeWatermarkAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbound-reply.seq")
	s, err := OpenSeqSource(path)
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	var last uint64
	for i := 0; i < 3; i++ {
		if last, err = s.Next(); err != nil {
			t.Fatalf("Next: %v", err)
		}
	}
	if got := s.Issued(); got != last {
		t.Fatalf("Issued() = %d after issuing up to %d; want the highest issued seq", got, last)
	}

	// Restart over the same durable file: the watermark may jump (the unused tail of the
	// reserved block is burned) but must never fall below an already-issued seq, and the
	// next seq must land strictly above it.
	s2, err := OpenSeqSource(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	w := s2.Issued()
	if w < last {
		t.Fatalf("Issued() = %d after restart; want >= %d (a watermark below an issued seq re-opens replay)", w, last)
	}
	next, err := s2.Next()
	if err != nil {
		t.Fatalf("Next after restart: %v", err)
	}
	if next <= w {
		t.Fatalf("first post-restart seq %d <= published watermark %d; a phone seeded at the watermark would stale-drop it", next, w)
	}
}

// TestRelaySink_ReconcileBootstrapsEveryOutboundRun: JournalSink.Snapshot is called
// exactly once per (re)connection (the interface contract, gateway.go), which makes it
// the first-connect AND post-reconnect bootstrap point PB-SYNC-7 asks for -- the phone
// gets the authorities before any journal frame it might have to reconcile against, on
// every run, with no new lifecycle hook. (Post-ROTATION bootstrap falls out of the same
// point: a rotated epoch key means a fresh sink and a fresh run.)
func TestRelaySink_ReconcileBootstrapsEveryOutboundRun(t *testing.T) {
	key := reconcileTestKey()
	app := &fakeAppender{}
	sink := newReconcileSink(app, key, testAuthorities, nil)

	roster := []protocol.JournalRecord{{Cursor: 5, SessionID: "s1", Type: "roster"}}
	if err := sink.Snapshot(roster, 5); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(app.envs) != 2 {
		t.Fatalf("first run appended %d envelopes; want 2 (reconcile record then the roster record)", len(app.envs))
	}
	if _, plain := openPlaintext(t, key, app.envs[0]); !isReconcileFrame(plain) {
		t.Fatalf("first frame of the run = %s; want the reconcile record", plain)
	}

	// A reconnect re-snapshots: the phone must get a FRESH record, not one stale set of
	// authorities from process start.
	if err := sink.Snapshot(roster, 6); err != nil {
		t.Fatalf("Snapshot after reconnect: %v", err)
	}
	if len(app.envs) != 4 {
		t.Fatalf("after the reconnect %d envelopes; want 4 (a second reconcile record + roster)", len(app.envs))
	}
	if _, plain := openPlaintext(t, key, app.envs[2]); !isReconcileFrame(plain) {
		t.Fatalf("first frame of the second run = %s; want a fresh reconcile record", plain)
	}
}

// isReconcileFrame reports whether a sealed plaintext carries the reconcile kind.
func isReconcileFrame(plain []byte) bool {
	var disc struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(plain, &disc); err != nil {
		return false
	}
	return disc.Kind == "reconcile"
}

// TestRelaySink_ReconcileFailsClosedWhenAnAuthorityIsUnreachable is the adversarial pin
// (9 rule 5). A record with a fabricated zero authority is WORSE than no record: the
// phone would adopt 0 as truth, raise nothing, and keep accepting every retained
// pre-rollback frame -- a silent rollback re-opening dressed as a successful reconcile.
// So an unreachable authority seals NOTHING, appends NOTHING, and surfaces the error;
// the phone then stays unreconciled and fails closed for mutating ops (recoverably --
// phonecore's TestReconcileWithheld_RefusesMutatingOpsButRecovers).
//
// The bootstrap inherits it: Snapshot appends no journal frames either, so the phone is
// never fed a stream it has no authority to reconcile against.
//
// NOTE for the implementer: a nil Authorities is the unit-test wiring only (the existing
// RelaySink tests construct sinks that way and pin that Snapshot still forwards); it
// must never be reachable in production. That wiring assertion belongs to the slice that
// lands PB-GW-1's InboundState (S2) and PB-GW-8's outbox (S2b), which is where the real
// authorities come from.
func TestRelaySink_ReconcileFailsClosedWhenAnAuthorityIsUnreachable(t *testing.T) {
	key := reconcileTestKey()
	app := &fakeAppender{}
	unreachable := errors.New("inbound state unreadable")
	sink := newReconcileSink(app, key, stubReconcileSource{err: unreachable}, nil)

	if err := sink.Reconcile(); err == nil {
		t.Fatalf("Reconcile with an unreachable authority = nil; want an error (never fabricate a zero authority)")
	}
	if len(app.envs) != 0 {
		t.Fatalf("appended %d envelopes on a failed reconcile; want 0 (no partial record on the wire)", len(app.envs))
	}
	if sink.Err() == nil {
		t.Fatalf("Err() = nil after a failed reconcile; the fault must be surfaced")
	}

	if err := sink.Snapshot([]protocol.JournalRecord{{Cursor: 5, SessionID: "s1", Type: "roster"}}, 5); err == nil {
		t.Fatalf("Snapshot with an unreachable authority = nil; want the bootstrap failure to propagate")
	}
	if len(app.envs) != 0 {
		t.Fatalf("appended %d envelopes; want 0 (no stream the phone cannot reconcile against)", len(app.envs))
	}

	// A sink with NO authority source may not silently synthesize an all-zero record.
	bare := newReconcileSink(app, key, nil, nil)
	if err := bare.Reconcile(); err == nil {
		t.Fatalf("Reconcile with no authority source = nil; want an error")
	}
	if len(app.envs) != 0 {
		t.Fatalf("appended %d envelopes with no authority source; want 0", len(app.envs))
	}
}
