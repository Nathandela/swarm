package remotegw

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-2pnu F3: a SEALED refusal that never
// consumes the item it answered.
//
// THE DEFECT, in one line: refuseCommand seals the reply AND returns the reason, so
// CommandBridge.handle returns before b.consume -- the inbound cursor never moves past the
// poison item, the relay is never acked for it, and every later poll re-serves the same
// envelope. Within one process the re-serve is refused at OpenMailboxFrame (the in-memory
// seq high-water already accepted it), so what an operator sees is a poll that fails forever
// with nothing left to fix; across a restart the persisted high-water was never raised
// either, so the frame opens again and a SECOND OpError is sealed for an operation the phone
// resolved on the first.
//
// A COMMAND THIS BUILD CANNOT ROUTE WILL NOT ROUTE ON RETRY, which is the whole argument for
// consuming it. The refusal is final by construction: opForAction has no arm for the action,
// or the body the arm needs is not in the envelope, and neither fact can change while the
// binary does not. That is the opposite of a forward error (the daemon was down) or a seal
// error (the relay refused the append), both of which stay unconsumed so a retry can still
// deliver -- and both of which this file leaves alone.
//
// A phone one release ahead of its gateway is the ordinary way to reach this, and the first
// unknown command wedges the mailbox for the life of the install.

import (
	"context"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// downForwarder is the daemon being unreachable, which is the one refusal shape that a
// retry can still resolve. It is local to this file because it exists only as the negative
// control below.
type downForwarder struct{}

func (downForwarder) ForwardCommand(_ string, _ protocol.RemoteCommand) (protocol.Control, error) {
	return protocol.Control{}, errors.New("dial daemon: connection refused")
}

// TestF3_ARefusedCommandIsConsumedSoTheMailboxDrains polls TWICE against one poison command.
// The first poll must seal exactly one refusal; the second must find nothing left to do,
// which is only true if the first advanced the cursor past the item it answered.
func TestF3_ARefusedCommandIsConsumedSoTheMailboxDrains(t *testing.T) {
	key := testContentKey()
	cmd := protocol.DeviceCommandAuth{
		// The population this covers: `approve` was exactly this shape to a 0.8.0 gateway.
		Action:      "session_rename",
		Session:     "m/s1",
		OperationID: "op-poison-1",
		DeviceID:    "d1",
		Sig:         "device-signature",
	}
	const cursor = uint64(7)
	mb := &fakeMailbox{inbox: []relay.Item{{Cursor: cursor, Envelope: sealedCmd(t, key, 1, cmd)}}}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   &fakeForwarder{},
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})

	if _, err := b.PollOnce(context.Background()); err == nil {
		t.Fatal("PollOnce hid the refusal: the reason must still reach an operator's poll error")
	}
	if got := b.Cursor(); got != cursor {
		t.Fatalf("cursor after the refusal = %d, want %d. The reply was sealed and the item was "+
			"NOT consumed, so the relay re-serves this envelope on every drain for the life of "+
			"the install -- a command this build has no arm for cannot route on a retry, so the "+
			"backlog behind it is stuck on an answer that already went out", got, cursor)
	}

	// The second drain. Nothing is left past the cursor, so it is quiet -- and quiet is the
	// assertion: a poll that keeps failing on an answered command is a permanent error an
	// operator can neither read nor act on.
	processed, err := b.PollOnce(context.Background())
	if err != nil || processed != 0 {
		t.Fatalf("second PollOnce = (%d, %v), want (0, nil): the refused item is still in the "+
			"gateway's way", processed, err)
	}
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies after two polls = %d, want exactly 1: the phone resolved this "+
			"operation on the first refusal and every later copy is noise on its mailbox",
			len(mb.replies))
	}
}

// TestF3_ARefusedPushPrefsIsConsumedSoTheMailboxDrains is the same shape on the other
// refusal seam. push_prefs with no body in-envelope is refused by refusePushPrefs, which
// seals and returns exactly as refuseCommand does -- so the item wedges identically.
func TestF3_ARefusedPushPrefsIsConsumedSoTheMailboxDrains(t *testing.T) {
	key := testContentKey()
	const cursor = uint64(1)
	prefs := &stubPrefs{prefs: PushPrefs{Version: 4, NeedsInput: true, Finished: true}}
	b, mb := prefsBridge(t, key, prefs, &fakeForwarder{}, sealedPrefsCmd(t, key, 1, "op-nobody", nil))

	if _, err := b.PollOnce(context.Background()); err == nil {
		t.Fatal("a body-less push_prefs must stay loud: a stripped body defaults to ENABLED")
	}
	if got := b.Cursor(); got != cursor {
		t.Fatalf("cursor after the push_prefs refusal = %d, want %d: the phone was told and the "+
			"item was left in the mailbox anyway", got, cursor)
	}

	processed, err := b.PollOnce(context.Background())
	if err != nil || processed != 0 {
		t.Fatalf("second PollOnce = (%d, %v), want (0, nil)", processed, err)
	}
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies after two polls = %d, want exactly 1", len(mb.replies))
	}
}

// TestF3_AForwardFailureIsStillRetried is the NEGATIVE CONTROL, and it is what keeps the fix
// from being "consume everything that failed". A daemon that was down is a transient fact:
// the item must stay unconsumed so the next drain forwards it again.
func TestF3_AForwardFailureIsStillRetried(t *testing.T) {
	key := testContentKey()
	cmd := protocol.DeviceCommandAuth{
		Action:      protocol.ActionKill,
		Session:     "m/s1",
		OperationID: "op-daemon-down",
		DeviceID:    "d1",
		Sig:         "device-signature",
	}
	mb := &fakeMailbox{inbox: []relay.Item{{Cursor: 3, Envelope: sealedCmd(t, key, 1, cmd)}}}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   downForwarder{},
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})

	if _, err := b.PollOnce(context.Background()); err == nil {
		t.Fatal("a forward failure must be loud")
	}
	if got := b.Cursor(); got != 0 {
		t.Fatalf("cursor after a forward failure = %d, want 0: the daemon being down is not a "+
			"verdict about the command, and consuming here would drop a kill its owner issued", got)
	}
}
