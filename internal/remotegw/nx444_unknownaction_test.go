package remotegw

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nx44.4(d): an action this gateway does not
// know hangs the phone-side op FOREVER.
//
// THE PATH, in one line: CommandBridge.handle calls routeCommand BEFORE consume, routeCommand's
// default arm is forward, and forward's first act is opForAction -- which returns an error for
// an unknown action and for a launch/approve whose body the relay stripped. forward returns that
// error, so nothing is ever sealed back, and the phone's operation stays PENDING for the life of
// the install: mobile's op ledger resolves on a reply and there is no other terminator.
//
// IT IS NOT HYPOTHETICAL. It is the same defect S18 found for device_revoke and IS-LIFE-4 found
// for approve, and it bit again in the 2026-08-09 field test: a phone on a build that seals
// `approve` against a 0.8.0 gateway with no approve arm sends a correctly-signed command that is
// refused one hop short of the daemon, with the card spinning until the app is reinstalled. Both
// earlier fixes added the missing ARM. Neither closed the class, because the class is what
// happens when there is no arm -- and a version-skewed pair of ends is the ordinary way to get
// one.
//
// WHAT THE FIX OWES, and the two halves are separate assertions below:
//
//  1. A REPLY IS SEALED. protocol.OpError carrying the command's own OperationID, so the phone
//     resolves the op with an honest refusal rather than waiting on a frame that will never come.
//  2. THE ITEM STILL FAILS. The error is not swallowed: an unconsumed item is what keeps the
//     mailbox cursor where it is and puts the reason in a poll error an operator can see. This is
//     refusePushPrefs' shape (errors.Join), and it is deliberately kept.

import (
	"context"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// TestNx444_AnUnknownActionIsRefusedWithASealedReply is the defect itself: a command whose
// action this build has no arm for.
func TestNx444_AnUnknownActionIsRefusedWithASealedReply(t *testing.T) {
	key := testContentKey()
	cmd := protocol.DeviceCommandAuth{
		// A verb a NEWER phone seals and this build has no arm for. It is spelled as a
		// plausible future action rather than as noise, because that is the population this
		// covers: `approve` was exactly this string to a 0.8.0 gateway.
		Action:      "session_rename",
		Session:     "m/s1",
		OperationID: "op-skewed-1",
		DeviceID:    "d1",
		Sig:         "device-signature",
	}
	// Sealed as a bare tuple: an action with no arm has no body this build could read anyway,
	// and opForAction refuses it before anything is forwarded.
	mb := &fakeMailbox{inbox: []relay.Item{{Cursor: 1, Envelope: sealedCmd(t, key, 1, cmd)}}}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})

	if _, err := b.PollOnce(context.Background()); err == nil {
		t.Fatalf("PollOnce accepted a command this gateway has no arm for; the item must still " +
			"fail locally so the cursor does not advance over it and an operator sees the reason")
	}
	if len(fwd.ops) != 0 {
		t.Fatalf("forwarded ops = %v, want nothing: an action with no arm must not reach the daemon", fwd.ops)
	}
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies = %d, want exactly 1. Nothing seals a refusal for an action this "+
			"build cannot route, so the phone's operation %q never resolves -- it is not slow, it "+
			"is unanswerable, and no retry, reconnect or relaunch ends it", len(mb.replies), cmd.OperationID)
	}
	_, ctrl := openReplyControl(t, key, mb.replies[0])
	if ctrl.Op != protocol.OpError {
		t.Errorf("refusal reply Op = %q, want %q: a phone told anything else would record a "+
			"success for a command that never reached the daemon", ctrl.Op, protocol.OpError)
	}
	if ctrl.OperationID != cmd.OperationID {
		t.Errorf("refusal reply OperationID = %q, want %q -- the id is the ONLY thing the phone "+
			"can resolve its pending op by", ctrl.OperationID, cmd.OperationID)
	}
	if ctrl.SessionID != cmd.Session {
		t.Errorf("refusal reply SessionID = %q, want the command's own %q", ctrl.SessionID, cmd.Session)
	}
	if !strings.Contains(ctrl.Error, "session_rename") {
		t.Errorf("refusal reply Error = %q, want it to name the action it could not route; a "+
			"refusal that does not say what was refused is a bug report nobody can act on", ctrl.Error)
	}
}

// TestNx444_ABodylessLaunchIsRefusedWithASealedReply is the same hole reached by the other
// route into opForAction's error return: an arm EXISTS, and the body the daemon reads is gone.
//
// A relay cannot forge a sealed frame but the phone can be on a build that seals a shape this
// one does not decode, and the outcome for the user is identical -- a launch that never
// resolves. The arm's own refusal ("missing its launch spec in-envelope") is right and stays;
// what it lacked was a reply.
func TestNx444_ABodylessLaunchIsRefusedWithASealedReply(t *testing.T) {
	key := testContentKey()
	cmd := protocol.DeviceCommandAuth{
		Action:      protocol.ActionLaunch,
		Session:     "m/s2",
		OperationID: "op-launch-nobody",
		DeviceID:    "d1",
		Sig:         "device-signature",
	}
	mb := &fakeMailbox{inbox: []relay.Item{{Cursor: 1, Envelope: sealedCmd(t, key, 1, cmd)}}}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
	})

	if _, err := b.PollOnce(context.Background()); err == nil {
		t.Fatalf("PollOnce accepted a launch with no spec; the refusal must stay loud")
	}
	if len(fwd.ops) != 0 {
		t.Fatalf("forwarded ops = %v, want nothing: a launch with no spec must not reach the daemon", fwd.ops)
	}
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies = %d, want exactly 1: the launch card on the phone resolves on a "+
			"reply and on nothing else", len(mb.replies))
	}
	_, ctrl := openReplyControl(t, key, mb.replies[0])
	if ctrl.Op != protocol.OpError || ctrl.OperationID != cmd.OperationID {
		t.Errorf("refusal reply = {Op:%q OperationID:%q}, want {Op:%q OperationID:%q}",
			ctrl.Op, ctrl.OperationID, protocol.OpError, cmd.OperationID)
	}
}
