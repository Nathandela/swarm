package phonecore

// FAILING-FIRST (TDD RED, GG-5) tests for the PHONE half of PB-SYNC-7 (6.6): the
// machine->phone reconcile record is demuxed off the SHARED relay mailbox like every
// other frame kind, its three authorities (PB-STATE-4) are obtainable after a
// reconnect, and each one is SUFFICIENT to refuse what a rollback would otherwise
// re-open. A relay that withholds the frame must leave the phone refusing mutating ops
// -- and that refusal must be RECOVERABLE, since a permanent one is the brick PB-SYNC-7
// exists to prevent.
//
// THE SEAMS these tests pin (undefined symbols -> compile-fail RED):
//
//	const kindReconcile = "reconcile"                      // snapshot.go, the router's switch
//	type reconcileFrame struct{ Kind string; schema.ReconcileRecord }
//	func (*MailboxRouter) Reconciled() (schema.ReconcileRecord, bool)
//	func (*MailboxRouter) RequireReconciled() error         // nil once obtained, else ErrUnreconciled
//	var ErrUnreconciled error
//	func (*Sequencer) SeedFrom(n uint64)                    // input.go: monotonic send-seq resume
//
// internal/remote/crypto is FROZEN and untouched: the record rides the EXISTING sealed
// mailbox envelope, SeedHighWater and NewGrantReceiverAt already exist, and the phone
// never initiates a signed reconcile (which would walk into PB-SYNC-5's closed
// actionClass switch).

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// wireZeroProfile is the committed JSON of the zero-value RemoteProfileV1 (no field
// carries omitempty). wantReconcileRecord below leaves Profile unset, so every wire-shape
// const in this file gained this exact suffix (ADR-017 T5).
const wireZeroProfile = `{"version":0,"accepted_actions":null,"accepted_body_versions":null,"interaction_schema_version":0,"terminal_view_version":0,"capability_record_version":0}`

// wireReconcileFrame is the committed sealed plaintext of a reconcile frame: the kind
// discriminator plus the schema.ReconcileRecord fields. It is byte-identical to what
// the gateway seals (internal/remotegw/reconcile_test.go pins the producer side against
// this same literal) and to the record schema itself
// (internal/protocol/schema/reconcile_test.go). These tests decode the LITERAL, not the
// production frame type, so a rename or reorder in Go cannot silently move the wire.
const wireReconcileFrame = `{"kind":"reconcile","machine":"m1","epoch_id":7,"inbound_high_water":42,"journal_ceiling":3,"reply_ceiling":5,"grant_epoch":7,"grant_seq":2,"issued_at":1700000000000,"profile":` + wireZeroProfile + `}`

// wantReconcileRecord is the record those bytes carry.
func wantReconcileRecord() protocol.ReconcileRecord {
	return protocol.ReconcileRecord{
		Machine:          "m1",
		EpochID:          7,
		InboundHighWater: 42,
		JournalCeiling:   3,
		ReplyCeiling:     5,
		GrantEpoch:       7,
		GrantSeq:         2,
		IssuedAt:         1700000000000,
	}
}

// machineSender is the machine's routing key id, stamped by RelaySink on journal,
// terminal AND reconcile frames -- so all three share ONE (sender, epoch) seq bucket.
// Command replies deliberately leave it zero (command_in.go), which is what makes the
// reply bucket separate and gives PB-STATE-4 its per-bucket authorities.
var machineSender = [8]byte{9, 10, 11, 12, 13, 14, 15, 16}

// sealFrameFrom seals one mailbox plaintext at seq for an explicit (sender, epoch)
// bucket. sealFrame (snapshot_test.go) always seals sender-zero; the reconcile record
// rides the machine-sender journal bucket, so the bucket must be selectable here.
// IssuedAt mirrors the real producers, for the reason sealFrame's doc gives: PB-TIME-2's
// bounded-age check is live on this path, and an unstamped fixture is ~56 years old.
func sealFrameFrom(t *testing.T, key crypto.ContentKey, sender [8]byte, epoch uint32, seq uint64, plain []byte) []byte {
	t.Helper()
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version:     crypto.VersionV1,
		EpochID:     epoch,
		Seq:         seq,
		SenderKeyID: sender,
		IssuedAt:    time.Now().UnixMilli(),
	}, plain)
	if err != nil {
		t.Fatalf("seal seq %d: %v", seq, err)
	}
	return env.Marshal()
}

// TestReconcileFrame_WireShape pins that the production frame type marshals to the
// committed bytes -- the same discipline TestSnapshotFrame_WireShape applies to
// terminal snapshots.
func TestReconcileFrame_WireShape(t *testing.T) {
	got, err := json.Marshal(reconcileFrame{Kind: kindReconcile, ReconcileRecord: wantReconcileRecord()})
	if err != nil {
		t.Fatalf("marshal reconcile frame: %v", err)
	}
	if string(got) != wireReconcileFrame {
		t.Fatalf("reconcile frame wire shape =\n  %s\nwant\n  %s", got, wireReconcileFrame)
	}
}

// TestMailboxDemux_ReconcileRoutedNotJournaled: a reconcile frame sealed onto the
// SHARED mailbox is demuxed to the router's reconcile slot with every authority intact,
// and never applied to the session, snapshot or reply caches. Against today's code the
// kind hits Accept's default arm and errors out (unrecognised kind), so the record is
// simply unreachable -- which is PB-SYNC-7 in one line.
func TestMailboxDemux_ReconcileRoutedNotJournaled(t *testing.T) {
	key := testContentKey()
	router := NewMailboxRouter(key)

	if _, err := router.Accept(sealFrameFrom(t, key, machineSender, 7, 3, []byte(wireReconcileFrame))); err != nil {
		t.Fatalf("accept reconcile frame: %v", err)
	}

	got, ok := router.Reconciled()
	if !ok {
		t.Fatalf("Reconciled() reports no record; the router dropped the reconcile frame")
	}
	// ReconcileRecord now carries a Profile field with a slice and a map, so it is not
	// comparable with == -- reflect.DeepEqual is the correct (and only) equality check.
	if !reflect.DeepEqual(got, wantReconcileRecord()) {
		t.Fatalf("reconciled record = %+v; want %+v (every authority verbatim)", got, wantReconcileRecord())
	}
	if n := len(router.Sessions().List()); n != 0 {
		t.Fatalf("session cache has %d entries; want 0 (a reconcile record is not a journal record)", n)
	}
	if n := router.Snapshots().Len(); n != 0 {
		t.Fatalf("snapshot cache has %d entries; want 0", n)
	}
	if n := router.Replies().Len(); n != 0 {
		t.Fatalf("reply cache has %d entries; want 0", n)
	}
}

// TestMailboxDemux_MalformedReconcileFailsClosed: adding the reconcile arm must not
// soften the router. A frame that claims the reconcile kind but carries an undecodable
// body is a LOUD error and leaves NO record behind (never half-applied), and an
// unrecognised kind still errors rather than being silently dropped or swallowed as
// journal (the C8 regression).
func TestMailboxDemux_MalformedReconcileFailsClosed(t *testing.T) {
	key := testContentKey()
	router := NewMailboxRouter(key)

	bad := []byte(`{"kind":"reconcile","epoch_id":"not-a-number"}`)
	if _, err := router.Accept(sealFrameFrom(t, key, machineSender, 7, 1, bad)); err == nil {
		t.Fatalf("accept malformed reconcile = nil error; want fail-closed")
	}
	if _, ok := router.Reconciled(); ok {
		t.Fatalf("Reconciled() reports a record after a malformed frame; a partial authority must never be adopted")
	}
	if err := router.RequireReconciled(); !errors.Is(err, ErrUnreconciled) {
		t.Fatalf("RequireReconciled() = %v; want ErrUnreconciled (a malformed frame does not reconcile)", err)
	}
	if _, err := router.Accept(sealFrameFrom(t, key, machineSender, 7, 2, []byte(`{"kind":"who_knows"}`))); err == nil {
		t.Fatalf("accept unknown kind = nil error; want the default arm to stay loud")
	}
}

// TestReconcileWithheld_RefusesMutatingOpsButRecovers is the relay-withholding pin, in
// BOTH directions PB-SYNC-7 demands.
//
// Fail closed: with no reconcile record, mutating ops are refused -- the phone cannot
// know whether its persisted send-seq, receive high-waters or grant watermark were
// rolled back, so authoring a command is unsafe.
//
// NOT bricked: observation keeps working while unreconciled (journal and terminal are
// reads, and a phone that shows nothing is indistinguishable from a dead one), and the
// refusal CLEARS the moment the withheld frame finally lands -- at a later seq, after
// other traffic. A latched refusal, or one that needs a re-pair, is the permanent brick
// PB-STATE-10 forbids.
func TestReconcileWithheld_RefusesMutatingOpsButRecovers(t *testing.T) {
	key := testContentKey()
	router := NewMailboxRouter(key)

	// (a) Fresh router, relay withholding the reconcile frame: mutating ops refused.
	if err := router.RequireReconciled(); !errors.Is(err, ErrUnreconciled) {
		t.Fatalf("RequireReconciled() on a fresh router = %v; want ErrUnreconciled (fail closed)", err)
	}

	// (b) Observation is unaffected: journal and terminal frames still apply.
	rec := protocol.JournalRecord{Cursor: 5, SessionID: "m/s1", Type: "launched"}
	plain, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal journal record: %v", err)
	}
	if _, err := router.Accept(sealFrameFrom(t, key, machineSender, 7, 1, plain)); err != nil {
		t.Fatalf("accept journal record while unreconciled: %v", err)
	}
	if _, err := router.Accept(sealFrameFrom(t, key, machineSender, 7, 2, marshalSnapshot(t, "m/s1", []string{"a"}, 80, 24))); err != nil {
		t.Fatalf("accept snapshot while unreconciled: %v", err)
	}
	if n := len(router.Sessions().List()); n != 1 {
		t.Fatalf("session cache has %d entries while unreconciled; want 1 (reads must not be blocked)", n)
	}
	if err := router.RequireReconciled(); !errors.Is(err, ErrUnreconciled) {
		t.Fatalf("RequireReconciled() after unrelated traffic = %v; want ErrUnreconciled", err)
	}

	// (c) The withheld frame finally arrives: the refusal clears. Recoverable, not a brick.
	if _, err := router.Accept(sealFrameFrom(t, key, machineSender, 7, 3, []byte(wireReconcileFrame))); err != nil {
		t.Fatalf("accept the late reconcile frame: %v", err)
	}
	if err := router.RequireReconciled(); err != nil {
		t.Fatalf("RequireReconciled() after the record landed = %v; want nil (fail-closed must be recoverable)", err)
	}
}

// TestReconcileAuthority_SendSeqResumesAboveTheGatewayHighWater covers PB-STATE-4(a).
// A rolled-back phone would restart its send-seq low, and every command it seals would
// be stale-dropped by the gateway's durable inbound high-water (PB-GW-1) -- a permanent
// refusal with no local symptom. InboundHighWater is the authority that resumes it, and
// the resume must be MONOTONIC so a stale record can never lower the counter (reserved-
// but-unused seq blocks, PB-STATE-3, are absorbed the same way).
func TestReconcileAuthority_SendSeqResumesAboveTheGatewayHighWater(t *testing.T) {
	rec := wantReconcileRecord()

	var seq Sequencer
	seq.SeedFrom(rec.InboundHighWater)
	if got := seq.Next(); got != rec.InboundHighWater+1 {
		t.Fatalf("first seq after seeding from the authority = %d; want %d (strictly above the gateway high-water)", got, rec.InboundHighWater+1)
	}
	// A stale/lower authority must never rewind an already-advanced sequencer.
	seq.SeedFrom(10)
	if got := seq.Next(); got != rec.InboundHighWater+2 {
		t.Fatalf("seq after a LOWER seed = %d; want %d (SeedFrom is monotonic)", got, rec.InboundHighWater+2)
	}
}

// TestReconcileAuthority_ReceiveHighWatersRefuseRetainedFrames covers PB-STATE-4(b),
// PER BUCKET.
//
// Shared journal/terminal bucket: JournalCeiling is the highest seq the gateway has
// ISSUED on that bucket, which is the reconcile frame's OWN seq -- so merely accepting
// the frame reseeds the bucket, and the retained pre-rollback frames the relay still
// holds are refused as ErrStaleSeq while the next legitimate frame is accepted. This is
// exactly why the ceiling may not be the durable RESERVATION ceiling (pinned gateway-
// side by TestRelaySink_ReconcileCeilingIsTheHighestIssuedSeq): a reservation ceiling
// would sit up to a full block above the last issued seq and stale-drop every
// legitimate frame in between.
//
// Command-reply bucket: sender-zero is a DIFFERENT (sender, epoch) bucket, untouched by
// the frame's own arrival, so ReplyCeiling must be seeded explicitly -- the separate
// authority PB-STATE-4 insists on.
func TestReconcileAuthority_ReceiveHighWatersRefuseRetainedFrames(t *testing.T) {
	key := testContentKey()
	router := NewMailboxRouter(key)

	// The rolled-back phone accepts the reconcile frame at seq 3 == JournalCeiling.
	if _, err := router.Accept(sealFrameFrom(t, key, machineSender, 7, 3, []byte(wireReconcileFrame))); err != nil {
		t.Fatalf("accept reconcile frame: %v", err)
	}
	rec, ok := router.Reconciled()
	if !ok {
		t.Fatalf("Reconciled() reports no record")
	}
	if rec.JournalCeiling != 3 {
		t.Fatalf("JournalCeiling = %d; want 3 (the frame's own seq: the highest issued on the shared bucket)", rec.JournalCeiling)
	}

	// A retained pre-rollback journal frame at seq 2 is refused.
	stale := sealFrameFrom(t, key, machineSender, 7, 2, marshalSnapshot(t, "m/s1", []string{"retained"}, 80, 24))
	if _, err := router.Accept(stale); !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("retained frame at seq 2 = %v; want ErrStaleSeq (the ceiling must refuse it)", err)
	}
	// The next legitimate frame (ceiling+1) is still accepted: the ceiling must not
	// stale-drop live traffic.
	fresh := sealFrameFrom(t, key, machineSender, 7, rec.JournalCeiling+1, marshalSnapshot(t, "m/s1", []string{"live"}, 80, 24))
	if _, err := router.Accept(fresh); err != nil {
		t.Fatalf("frame at ceiling+1 = %v; want accepted (the authority must not drop live traffic)", err)
	}

	// The command-reply bucket is separate (sender-zero) and needs its own seeding.
	router.SeedHighWater([8]byte{}, rec.EpochID, rec.ReplyCeiling)
	retainedReply := sealFrame(t, key, rec.ReplyCeiling, marshalReply(t, protocol.Control{Op: protocol.OpOK, OperationID: "op-old"}))
	if _, err := router.Accept(retainedReply); !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("retained reply at seq %d = %v; want ErrStaleSeq", rec.ReplyCeiling, err)
	}
	liveReply := sealFrame(t, key, rec.ReplyCeiling+1, marshalReply(t, protocol.Control{Op: protocol.OpOK, OperationID: "op-new"}))
	if _, err := router.Accept(liveReply); err != nil {
		t.Fatalf("reply at ReplyCeiling+1 = %v; want accepted", err)
	}
	if router.Replies().Len() != 1 {
		t.Fatalf("reply cache has %d entries; want exactly the live one", router.Replies().Len())
	}
}

// TestReconcileAuthority_GrantWatermarkRefusesAnOlderGrant covers PB-STATE-4(c) and its
// acceptance criterion verbatim: "an older correctly-signed grant [is] refused after a
// rollback". crypto.NewGrantReceiverAt already takes exactly (epoch, grant_seq) -- what
// was missing is any way for the phone to LEARN that pair, which is what GrantEpoch and
// GrantSeq supply.
func TestReconcileAuthority_GrantWatermarkRefusesAnOlderGrant(t *testing.T) {
	rec := wantReconcileRecord()

	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	machinePub, machinePriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("machine key: %v", err)
	}
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}

	// The relay retains a correctly-signed grant AT the watermark; after a rollback a
	// phone with no authority would accept it as its first grant.
	retained, err := crypto.SealEpochGrant(machinePriv, ks.RecipientPublic(), rec.GrantEpoch, rec.GrantSeq, keys)
	if err != nil {
		t.Fatalf("seal retained grant: %v", err)
	}
	gr := crypto.NewGrantReceiverAt(rec.GrantEpoch, rec.GrantSeq)
	if _, _, _, err := gr.Accept(ks, machinePub, retained); err == nil {
		t.Fatalf("replayed grant at the watermark was ACCEPTED; want refused")
	}

	// The next legitimate grant still lands, so the watermark is an anchor, not a wall.
	next, err := crypto.SealEpochGrant(machinePriv, ks.RecipientPublic(), rec.GrantEpoch, rec.GrantSeq+1, keys)
	if err != nil {
		t.Fatalf("seal next grant: %v", err)
	}
	if _, _, _, err := gr.Accept(ks, machinePub, next); err != nil {
		t.Fatalf("grant above the watermark = %v; want accepted", err)
	}
}
