package remotegw

// S10's MACHINE leg of the journal repair (PB-SYNC-2 / PB-SYNC-3 / PB-SYNC-8). The phone's
// half is fenced in internal/phonecore and mobile/conformance; those two prove the phone
// SENDS a journal_resync and that it APPLIES a reseed frame. Neither can see the hop in
// between, and a repair channel whose middle link is missing is a resync that reaches the
// machine, is answered with nothing, and leaves the flag set forever -- with every test on
// either side still green. That is standing class (v), so it gets its own fence here.
//
// Two things are pinned:
//
//	the DISPATCH -- journal_resync reaches the resyncer, carrying the phone's cursor and
//	                without ever touching the daemon's device authenticator (PB-SYNC-5);
//	the WIRE     -- RelaySink.Reseed marshals the exact bytes phonecore decodes. remotegw
//	                cannot import phonecore, so the literal is restated, which is the
//	                discipline the reconcile and terminal-snapshot frames already use: a
//	                Go-side rename on either end moves one literal and fails here.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/status"
)

// fakeResyncer records the resync requests the bridge dispatched.
type fakeResyncer struct {
	mu    sync.Mutex
	froms []uint64
}

func (f *fakeResyncer) Resync(_ context.Context, from uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.froms = append(f.froms, from)
	return nil
}

// TestS10_TheBridgeRoutesJournalResyncToTheResyncer proves the dispatch exists and carries
// the cursor. The kill alongside it is the NON-VACUITY control: it proves this bridge really
// does forward the actions it is meant to, so "journal_resync was not forwarded" is a
// routing decision rather than a bridge that forwards nothing.
func TestS10_TheBridgeRoutesJournalResyncToTheResyncer(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 11)
	}
	resync := protocol.RemoteCommand{
		DeviceCommandAuth: protocol.DeviceCommandAuth{Action: protocol.ActionJournalResync, Machine: "m1"},
		ResyncCursor:      42,
	}
	mb := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: sealRemoteCmd(t, key, 1, resync)},
		{Cursor: 2, Envelope: sealedCmd(t, key, 2, protocol.DeviceCommandAuth{
			Action: protocol.ActionKill, Session: "m1/s1", OperationID: "op-kill", DeviceID: "d1", Sig: "sig-k"})},
	}}
	fwd := &fakeForwarder{}
	rs := &fakeResyncer{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb, Forwarder: fwd, Resync: rs, Key: key, EpochID: 1, ReplyTarget: "phone",
	})

	if _, err := b.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.froms) != 1 {
		t.Fatalf("the resyncer was called %d time(s), want 1. PB-SYNC-2 makes an atomic "+
			"roster+events snapshot THE journal repair channel; a bridge that does not route the "+
			"request leaves the phone asking a machine that never answers, and the stale flag "+
			"clears only on the repair (PB-SYNC-3) -- so it never clears", len(rs.froms))
	}
	if rs.froms[0] != 42 {
		t.Errorf("the resync was dispatched from cursor %d, want the phone's own 42. Without it "+
			"the machine must read from 0 and re-send every event it has ever journalled to "+
			"repair one hole -- into the same 600-per-tumbling-minute mailbox the repair itself "+
			"has to arrive on", rs.froms[0])
	}
	// PB-SYNC-5's decision, as a fence: the resync is UNSIGNED and is NOT forwarded to the
	// daemon's device authenticator. The gateway performs the journal_read and holds no
	// device signing key, so a forwarded resync could never be authorized at all.
	if len(fwd.ops) != 1 || fwd.ops[0] != protocol.OpKill {
		t.Errorf("forwarded ops = %v, want [kill] only. journal_resync must not reach the daemon's "+
			"device authenticator: it is an unsigned read the gateway serves itself, exactly like "+
			"terminal_watch", fwd.ops)
	}
}

// TestS10_ABridgeWithNoResyncerDoesNotWedgeTheLoop: a bridge assembled without the seam
// (every unit-test fake, and any embedder that wires no gateway) must drop the request, not
// fail the whole poll -- a poll error stops the page and the phone's own inbound repair
// rides that same loop.
func TestS10_ABridgeWithNoResyncerDoesNotWedgeTheLoop(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 13)
	}
	mb := &fakeMailbox{inbox: []relay.Item{{Cursor: 1, Envelope: sealRemoteCmd(t, key, 1,
		protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{Action: protocol.ActionJournalResync}})}}}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb, Forwarder: &fakeForwarder{}, Key: key, EpochID: 1, ReplyTarget: "phone",
	})

	n, err := b.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce with no resyncer = %v, want the item consumed and dropped", err)
	}
	if n != 1 {
		t.Errorf("processed %d items, want 1", n)
	}
}

// wireJournalReseed is the committed sealed plaintext of a journal-reseed frame, restated
// from internal/phonecore's identical literal. Every roster record carries Cursor 0, which
// is what the machine really emits (internal/daemon/journal.go leaves it deliberately
// unset) and the whole reason PB-SYNC-8 exists.
const wireJournalReseed = `{"kind":"journal_reseed","roster":[{"cursor":0,"session_id":"m1/s-alpha","type":"roster","group":"working"},{"cursor":0,"session_id":"m1/s-beta","type":"roster"}],"events":[{"cursor":50,"session_id":"m1/s-beta","type":"group_transition","group":"needs_input"}],"cursor":50}`

// TestS10_TheReseedFrameMatchesThePhonesWire pins the two ends to one byte string. remotegw
// cannot import phonecore, so a drift on either side is otherwise invisible until a real
// handset silently fails to repair.
func TestS10_TheReseedFrameMatchesThePhonesWire(t *testing.T) {
	got, err := json.Marshal(reseedFrame{Kind: kindJournalReseed, JournalReseed: protocol.JournalReseed{
		Roster: []protocol.JournalRecord{
			{SessionID: "m1/s-alpha", Type: "roster", Group: status.GroupWorking},
			{SessionID: "m1/s-beta", Type: "roster"},
		},
		Events: []protocol.JournalRecord{
			{Cursor: 50, SessionID: "m1/s-beta", Type: "group_transition", Group: status.GroupNeedsInput},
		},
		Cursor: 50,
	}})
	if err != nil {
		t.Fatalf("marshal reseed frame: %v", err)
	}
	if string(got) != wireJournalReseed {
		t.Fatalf("reseed frame wire shape =\n  %s\nwant\n  %s", got, wireJournalReseed)
	}
}

// TestS10_TheSinkSealsTheReseedOnTheSharedStream proves the frame really reaches the phone's
// mailbox, on the SAME (sender, epoch) bucket as the journal -- which is what makes its own
// arrival seq the watermark PB-SYNC-3 requires the repair to be committed with.
func TestS10_TheSinkSealsTheReseedOnTheSharedStream(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 17)
	}
	ap := &fakeAppender{}
	sink := NewRelaySink(RelayConfig{
		Appender: ap, Target: "phone", Machine: "m1", EpochID: 1, Key: key,
		SenderKeyID: [8]byte{9, 9, 9, 9, 9, 9, 9, 9},
	})
	// One journal record first, so the reseed's seq is provably drawn from the SAME counter.
	if err := sink.Event(protocol.JournalRecord{Cursor: 1, SessionID: "m1/s-alpha", Type: "launched"}); err != nil {
		t.Fatalf("sink.Event: %v", err)
	}
	if err := sink.Reseed(protocol.JournalReseed{
		Roster: []protocol.JournalRecord{{SessionID: "m1/s-alpha", Type: "roster"}},
		Cursor: 7,
	}); err != nil {
		t.Fatalf("sink.Reseed: %v", err)
	}

	if len(ap.envs) != 2 {
		t.Fatalf("the sink appended %d envelope(s), want 2 (the event and the reseed)", len(ap.envs))
	}
	env, err := crypto.ParseEnvelope(ap.envs[1])
	if err != nil {
		t.Fatalf("parse the reseed envelope: %v", err)
	}
	if env.Header.Seq != 2 {
		t.Errorf("the reseed rode seq %d, want 2. It must draw from the SAME per-sink counter as "+
			"the journal: its own arrival seq is the transport watermark the repair is committed "+
			"with, and a private counter would put it at a seq the phone has already consumed",
			env.Header.Seq)
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		t.Fatalf("open the reseed envelope: %v", err)
	}
	var disc struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(plain, &disc); err != nil {
		t.Fatalf("decode the reseed plaintext: %v", err)
	}
	if disc.Kind != kindJournalReseed {
		t.Errorf("the sealed plaintext is kind %q, want %q -- the phone demuxes on this "+
			"discriminator and fails closed on anything it does not recognise", disc.Kind, kindJournalReseed)
	}
}
