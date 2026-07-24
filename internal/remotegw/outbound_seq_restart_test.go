// FAILING-FIRST (TDD RED, GG-5) test for committee finding C2b: the machine->phone
// OUTBOUND sequence counters must SURVIVE a gateway (swarm-remote) restart.
//
// The bug: RelaySink.seq (journal + terminal snapshots) and CommandBridge.replySeq
// (command replies) were in-memory and reset to 0 on restart. EpochID and the content
// key are durable, so the (sender,epoch) content-key stream is REUSED after restart --
// but the phone's per-(sender,epoch) high-water is ALSO durable. crypto.MailboxReceiver
// .Accept rejects any seq <= that high-water as ErrStaleSeq with no resync, so a
// restarted gateway that resumes at seq 1 has EVERY journal/terminal/reply frame silently
// dropped by the phone until the counter climbs back past the old high-water -- freezing a
// real phone after a supervised restart (contradicting A2's "resume from the last durable
// cursor").
//
// The fix (undefined symbols here -> compile-fail RED): a durable outbound seq high-water
// (remotegw.SeqSource + remotegw.OpenSeqSource) seeded into RelaySink and CommandBridge so
// the FIRST frame after restart carries a seq STRICTLY GREATER than any the phone accepted.
package remotegw

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// TestGateway_OutboundSeqSurvivesRestart drives the JOURNAL/TERMINAL stream (RelaySink,
// SenderKeyID = the machine key id): seal frames, let a phone-side MailboxReceiver accept
// them (tracking a durable high-water), then reconstruct a FRESH RelaySink from the SAME
// persisted seq state (simulating a restart) and assert its next frame's seq is > the last
// the phone saw AND is accepted (not ErrStaleSeq).
func TestGateway_OutboundSeqSurvivesRestart(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	sender := [8]byte{9, 10, 11, 12, 13, 14, 15, 16}
	const epoch = 7
	seqPath := filepath.Join(t.TempDir(), "outbound-journal.seq")

	newSink := func() (*RelaySink, *fakeAppender) {
		seq, err := OpenSeqSource(seqPath)
		if err != nil {
			t.Fatalf("open durable seq: %v", err)
		}
		app := &fakeAppender{}
		return NewRelaySink(RelayConfig{
			Appender:    app,
			Target:      "phone",
			EpochID:     epoch,
			Key:         key,
			SenderKeyID: sender,
			Seq:         seq,
			Now:         func() time.Time { return time.Unix(1_700_000_000, 0) },
		}), app
	}

	// Before restart: seal several frames; the phone accepts each, advancing its durable
	// per-(sender,epoch) high-water to the last seq it saw.
	sink, app := newSink()
	for i := 0; i < 3; i++ {
		if err := sink.Event(protocol.JournalRecord{Cursor: uint64(i + 1), SessionID: "s", Type: "launched"}); err != nil {
			t.Fatalf("pre-restart seal %d: %v", i, err)
		}
	}
	phone := crypto.NewMailboxReceiver()
	var lastSeq uint64
	for i, raw := range app.envs {
		env, err := crypto.ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("pre-restart env %d parse: %v", i, err)
		}
		if _, err := phone.Accept(key, env); err != nil {
			t.Fatalf("phone rejected pre-restart frame %d (seq %d): %v", i, env.Header.Seq, err)
		}
		lastSeq = env.Header.Seq
	}

	// Restart: a FRESH sink from the SAME persisted seq state. A reset in-memory counter
	// would restart at 1 <= lastSeq; the durable high-water must resume STRICTLY above it,
	// covering any unflushed tail (frames issued in the current reservation block that were
	// never individually persisted).
	sink2, app2 := newSink()
	if err := sink2.Event(protocol.JournalRecord{Cursor: 99, SessionID: "s", Type: "launched"}); err != nil {
		t.Fatalf("post-restart seal: %v", err)
	}
	env2, err := crypto.ParseEnvelope(app2.envs[0])
	if err != nil {
		t.Fatalf("post-restart env parse: %v", err)
	}
	if env2.Header.Seq <= lastSeq {
		t.Fatalf("post-restart seq %d not > last accepted %d: a restarted gateway reused a seq the phone already saw (C2b freeze)", env2.Header.Seq, lastSeq)
	}
	if _, err := phone.Accept(key, env2); err != nil {
		t.Fatalf("phone STALE-DROPPED the post-restart frame (seq %d, last %d): %v -- the gateway restart froze the phone (C2b)", env2.Header.Seq, lastSeq, err)
	}
}

// TestGateway_ReplySeqSurvivesRestart drives the COMMAND-REPLY stream (CommandBridge,
// SenderKeyID stays zero via SealControlReply): the reply seq must be durable too, so a
// restarted gateway never re-emits a reply seq the phone already accepted.
func TestGateway_ReplySeqSurvivesRestart(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}
	const epoch = 1
	seqPath := filepath.Join(t.TempDir(), "outbound-reply.seq")

	newBridge := func(mb *fakeMailbox) *CommandBridge {
		seq, err := OpenSeqSource(seqPath)
		if err != nil {
			t.Fatalf("open reply seq: %v", err)
		}
		return NewCommandBridge(CommandBridgeConfig{
			Mailbox:     mb,
			Forwarder:   &fakeForwarder{},
			Key:         key,
			EpochID:     epoch,
			ReplyTarget: "phone",
			ReplySeq:    seq,
		})
	}

	mb1 := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: sealedCmd(t, key, 1, protocol.DeviceCommandAuth{Action: protocol.ActionKill, Session: "m/s1", OperationID: "op-1", DeviceID: "d", Sig: "s"})},
		{Cursor: 2, Envelope: sealedCmd(t, key, 2, protocol.DeviceCommandAuth{Action: protocol.ActionKill, Session: "m/s2", OperationID: "op-2", DeviceID: "d", Sig: "s"})},
	}}
	b1 := newBridge(mb1)
	if _, err := b1.PollOnce(context.Background()); err != nil {
		t.Fatalf("pre-restart poll: %v", err)
	}

	// The reply stream is keyed on (sender=0, epoch); the phone tracks its high-water there.
	phone := crypto.NewMailboxReceiver()
	var lastSeq uint64
	for i, raw := range mb1.replies {
		env, err := crypto.ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("pre-restart reply %d parse: %v", i, err)
		}
		if _, err := phone.Accept(key, env); err != nil {
			t.Fatalf("phone rejected pre-restart reply %d (seq %d): %v", i, env.Header.Seq, err)
		}
		lastSeq = env.Header.Seq
	}

	// Restart: a fresh bridge from the SAME persisted reply-seq state.
	mb2 := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: sealedCmd(t, key, 5, protocol.DeviceCommandAuth{Action: protocol.ActionKill, Session: "m/s3", OperationID: "op-3", DeviceID: "d", Sig: "s"})},
	}}
	b2 := newBridge(mb2)
	if _, err := b2.PollOnce(context.Background()); err != nil {
		t.Fatalf("post-restart poll: %v", err)
	}
	env2, err := crypto.ParseEnvelope(mb2.replies[0])
	if err != nil {
		t.Fatalf("post-restart reply parse: %v", err)
	}
	if env2.Header.Seq <= lastSeq {
		t.Fatalf("post-restart reply seq %d not > last accepted %d: the restarted gateway reused a reply seq the phone already saw (C2b)", env2.Header.Seq, lastSeq)
	}
	if _, err := phone.Accept(key, env2); err != nil {
		t.Fatalf("phone STALE-DROPPED the post-restart reply (seq %d, last %d): %v (C2b)", env2.Header.Seq, lastSeq, err)
	}
}
