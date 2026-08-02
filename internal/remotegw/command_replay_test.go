// FAILING-FIRST (TDD RED, GG-5) tests pinning slice GW-M2: the command-IN path
// (CommandBridge.handle -> OpenRemoteCommand -> crypto.OpenMailbox) has NO
// cryptographic replay/reorder protection today. It opens each inbound envelope
// with a BARE AEAD open (command_in.go: ParseEnvelope -> OpenMailbox) -- no
// crypto.MailboxReceiver, no per-(sender,epoch) seq tracking. Dedup relies only
// on it.Cursor, the RELAY's own untrusted storage cursor (ADR-007 D7: "its own
// untrusted storage cursor, distinct from the authenticated seq the device
// trusts"). Consequence: a buggy/malicious relay that re-delivers the IDENTICAL
// sealed envelope at a NEW storage cursor is forwarded to the daemon again -- a
// replay -- and a relay that reorders delivery lets a stale (lower-seq) command
// land after a newer one has already been forwarded.
//
// The intended fix (NOT implemented by these tests) mirrors the already-shipped
// phone-side receive path (internal/phonecore/journal.go JournalReceiver.Accept):
// wrap a crypto.MailboxReceiver keyed by the phone's sender/epoch so a
// replayed/reordered envelope is rejected (crypto.ErrStaleSeq) and NOT forwarded.
//
// These tests must fail on the ASSERTION (forwarded more than once / stale
// command forwarded), not on a missing symbol -- they use only the existing
// CommandBridge/CommandBridgeConfig/PollOnce contract and the fakeMailbox /
// fakeForwarder / sealedCmd test helpers already defined in
// command_loop_test.go.
package remotegw

import (
	"context"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// TestCommandBridge_ReplayedCommandEnvelopeRejectedNotForwarded: the relay
// re-delivers the IDENTICAL sealed command envelope at two different storage
// cursors (it.Cursor 1 and 2 -- the relay's own untrusted bookkeeping, ADR-007
// D7). A crypto.MailboxReceiver keyed by the phone's sender/epoch would refuse
// the second delivery as a replay (crypto.ErrStaleSeq) because its authenticated
// seq (5) was already seen; today the bare AEAD open in OpenRemoteCommand does
// not track seq at all, so it opens and forwards both.
func TestCommandBridge_ReplayedCommandEnvelopeRejectedNotForwarded(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}
	raw := sealedCmd(t, key, 5, protocol.DeviceCommandAuth{
		Action: protocol.ActionKill, Session: "m/s1", OperationID: "op-1", DeviceID: "d1", Sig: "s1",
	})
	// The SAME sealed envelope, replayed by the relay at a later storage cursor.
	mb := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: raw},
		{Cursor: 2, Envelope: raw},
	}}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})

	n, err := b.PollOnce(context.Background())
	t.Logf("PollOnce processed=%d err=%v", n, err)

	// The forwarder must see the replayed command exactly ONCE. Today OpenRemoteCommand
	// is a bare crypto.OpenMailbox call with no crypto.MailboxReceiver seq guard, so the
	// identical envelope at relay cursor 2 opens and forwards again (len==2).
	if len(fwd.seen) != 1 {
		t.Fatalf("forwarder saw the replayed command %d times, want exactly 1: the command-IN path has no "+
			"crypto.MailboxReceiver replay defense, so a relay re-delivering the SAME sealed envelope at a new "+
			"storage cursor (it.Cursor) is opened and forwarded again instead of being rejected with crypto.ErrStaleSeq",
			len(fwd.seen))
	}
}

// TestCommandBridge_ReorderedCommandEnvelopeRejected: two DISTINCT commands sealed
// for the same sender/epoch at seq 1 and seq 2, but the relay delivers seq 2 FIRST
// (it.Cursor 1) and the stale seq 1 SECOND (it.Cursor 2). A crypto.MailboxReceiver
// tracks the authenticated seq, not the relay's delivery/storage order, so the
// lower seq arriving after the higher one is stale and must be rejected
// (crypto.ErrStaleSeq) -- mirroring phonecore's monotonic-seq rejection
// (TestPhoneCore_ReplayedOlderEnvelopeRejectedAndDoesNotMutateCache). Today
// nothing tracks seq on this path, so both commands are opened and forwarded
// regardless of delivery order.
func TestCommandBridge_ReorderedCommandEnvelopeRejected(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 13)
	}
	envSeq2 := sealedCmd(t, key, 2, protocol.DeviceCommandAuth{
		Action: protocol.ActionKill, Session: "m/s2", OperationID: "op-seq2", DeviceID: "d1", Sig: "s2",
	})
	envSeq1 := sealedCmd(t, key, 1, protocol.DeviceCommandAuth{
		Action: protocol.ActionKill, Session: "m/s1", OperationID: "op-seq1", DeviceID: "d1", Sig: "s1",
	})
	mb := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: envSeq2}, // relay delivers the higher seq first
		{Cursor: 2, Envelope: envSeq1}, // stale seq arrives second
	}}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})

	n, err := b.PollOnce(context.Background())
	t.Logf("PollOnce processed=%d err=%v", n, err)

	// The stale seq-1 command must never reach the forwarder once seq-2 has been
	// accepted for this sender/epoch.
	for _, cmd := range fwd.seen {
		if cmd.OperationID == "op-seq1" {
			t.Fatalf("the stale seq-1 command (op-seq1) was forwarded after seq-2 was already accepted for the " +
				"same sender/epoch; want it rejected as stale (crypto.ErrStaleSeq) -- the command-IN path has no " +
				"seq guard, so a reordered lower-seq envelope is forwarded instead of refused")
		}
	}
	if len(fwd.seen) != 1 || fwd.seen[0].OperationID != "op-seq2" {
		t.Fatalf("forwarded commands = %+v, want exactly [op-seq2] (seq-2 forwarded once, stale reordered seq-1 rejected)", fwd.seen)
	}
}
