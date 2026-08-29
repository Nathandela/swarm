package remotegw

// The PRODUCTION wiring half of PB-SYNC-7. RelaySink.Reconcile has been unit-tested against
// a stub ReconcileSource since S1b, but a Service that never hands the sink an authority
// source publishes NO reconcile record at all -- and a phone that fails closed on
// RequireReconciled (PB-STATE-4) then refuses every mutating op FOREVER, with nothing in the
// tree failing. The phone-side S7 tests deliver the record by hand, so they cannot catch it;
// this asserts the RECORD the assembled runtime actually puts on the wire, field by field,
// not merely that some field is non-nil.

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestService_PublishesTheReconcileRecordFromItsOwnDurableState builds a Service exactly as
// production does and drives the sink's bootstrap point (Snapshot runs once per journal
// (re)connection), then opens the appended envelope and checks every authority against the
// runtime state it must be read from.
func TestService_PublishesTheReconcileRecordFromItsOwnDurableState(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 3)
	}
	const epoch = uint32(7)

	// (a) the durable INBOUND high-water for the sender-zero phone -> machine stream.
	inbound, err := OpenInboundState("", "")
	if err != nil {
		t.Fatalf("OpenInboundState: %v", err)
	}
	if err := inbound.Save(InboundCheckpoint{
		Cursor:  9,
		Highest: map[InboundStream]uint64{{Sender: [8]byte{}, Epoch: epoch}: 42},
	}); err != nil {
		t.Fatalf("seed inbound checkpoint: %v", err)
	}
	// (b) the command-reply bucket's issued watermark.
	replySeq, err := OpenSeqSource("")
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := replySeq.Next(); err != nil {
			t.Fatalf("reply seq: %v", err)
		}
	}

	mb := &scriptedMailbox{}
	svc := NewService(ServiceConfig{
		DaemonSocket:   "/nonexistent/remote.sock",
		Relay:          mb,
		PhoneTarget:    "phone-routing-id",
		Machine:        "m1",
		Key:            key,
		EpochID:        epoch,
		RecipientKeyID: [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
		SenderKeyID:    [8]byte{9, 10, 11, 12, 13, 14, 15, 16},
		ReplySeq:       replySeq,
		Inbound:        inbound,
		GrantSeq:       2,
	})

	// The journal bootstrap: one Snapshot per (re)connection, which must lead with the record.
	if err := svc.sink.Snapshot(nil, 0); err != nil {
		t.Fatalf("Snapshot: %v (a Service with no authority source fails closed here -- and a Service that simply never publishes leaves the phone bricked)", err)
	}
	if len(mb.appends) != 2 {
		t.Fatalf("the assembled runtime appended %d envelopes; want reconcile then empty roster reseed", len(mb.appends))
	}

	env, err := crypto.ParseEnvelope(mb.appends[0])
	if err != nil {
		t.Fatalf("parse appended envelope: %v", err)
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		t.Fatalf("open appended envelope: %v", err)
	}
	var got struct {
		Kind string `json:"kind"`
		protocol.ReconcileRecord
	}
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("decode appended plaintext: %v", err)
	}

	want := protocol.ReconcileRecord{
		Machine:          "m1",
		EpochID:          epoch,
		InboundHighWater: 42,             // the durable inbound checkpoint, sender-zero
		JournalCeiling:   env.Header.Seq, // self-certifying: the record's OWN seq
		ReplyCeiling:     5,              // the reply SeqSource's issued watermark
		GrantEpoch:       epoch,
		GrantSeq:         2,
		IssuedAt:         env.Header.IssuedAt, // one clock: the envelope's own issued_at
	}
	if got.Kind != kindReconcile {
		t.Fatalf("first frame of the run has kind %q; want %q", got.Kind, kindReconcile)
	}
	// ReconcileRecord now carries a Profile field with a slice and a map, so it is not
	// comparable with == -- reflect.DeepEqual is the correct (and only) equality check.
	if !reflect.DeepEqual(got.ReconcileRecord, want) {
		t.Fatalf("published reconcile record =\n  %+v\nwant\n  %+v\n(every authority must be read from the runtime's own durable state, never fabricated)",
			got.ReconcileRecord, want)
	}
	if got.Machine == "" {
		t.Fatalf("the record names no machine; the phone refuses an authority it cannot attribute, which is the same permanent refusal as no record at all")
	}
}

// TestService_ReconcileRecordNamesTheEndpointIDTheDaemonAssigned closes the PRODUCTION half
// of the attribution: which string the record must carry.
//
// A phone REFUSES a reconcile record whose machine is not the one it paired with
// (phonecore.Core.Reconcile), and the machine id a phone holds is the DAEMON'S ENDPOINT ID
// -- the same id every session id it sees is namespaced with (Gateway.namespaceRecord) and
// the id it signs into every command tuple (phonecore.CommandInput.Machine,
// phonesim.Config.Machine). Only the daemon knows it: it is derived from the daemon's state
// dir and arrives in the hello reply, so no value cmd/swarm-remote can assemble from
// machineid.Identity (hostname, routing id) is that string. Stamping any of them publishes a
// record the phone refuses -- the same permanent fail-closed refusal of mutating ops as
// publishing nothing, but wearing a plausible value that hides it.
//
// So this drives the REAL journal path against a daemon that assigns a known endpoint id and
// asserts the record on the wire carries it, with ServiceConfig.Machine deliberately unset.
func TestService_ReconcileRecordNamesTheEndpointIDTheDaemonAssigned(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 11)
	}
	const (
		epoch    = uint32(4)
		endpoint = "ep-d1ce4f00"
	)

	sock, ln := journalSocket(t)
	gotRead := make(chan protocol.Control, 1)
	go serveFakeJournalDaemon(t, ln, endpoint, protocol.Control{Cursor: 7}, gotRead)

	mb := &scriptedMailbox{}
	svc := NewService(ServiceConfig{
		DaemonSocket: sock,
		Relay:        mb,
		PhoneTarget:  "phone-routing-id",
		// Machine is deliberately NOT set: production cannot know it at assembly time.
		Key:      key,
		EpochID:  epoch,
		GrantSeq: 3,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = svc.Gateway().RunJournal(ctx) // returns when the one-shot fake daemon hangs up
	select {
	case <-gotRead:
	case <-time.After(time.Second):
		t.Fatal("the gateway never issued a journal_read; the fake daemon was not driven at all")
	}

	if len(mb.appends) == 0 {
		t.Fatal("the journal run appended nothing; the reconcile record must lead every run")
	}
	env, err := crypto.ParseEnvelope(mb.appends[0])
	if err != nil {
		t.Fatalf("parse appended envelope: %v", err)
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		t.Fatalf("open appended envelope: %v", err)
	}
	var got struct {
		Kind string `json:"kind"`
		protocol.ReconcileRecord
	}
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("decode appended plaintext: %v", err)
	}
	if got.Kind != kindReconcile {
		t.Fatalf("first frame of the run has kind %q; want %q", got.Kind, kindReconcile)
	}
	if got.Machine != endpoint {
		t.Fatalf("published reconcile record names machine %q; want %q -- the endpoint id the daemon assigned at hello, which is the id the phone pairs against. Any other value is refused by phonecore.Core.Reconcile and mutating ops stay fail-closed FOREVER",
			got.Machine, endpoint)
	}
	if got.GrantEpoch != epoch || got.GrantSeq != 3 {
		t.Errorf("published grant watermark = (%d,%d); want (%d,3)", got.GrantEpoch, got.GrantSeq, epoch)
	}
}
