package remotegw

// M0.3 (agents-tracker-dwwv.1.3) pins the guarantee agents-tracker-joyi's notes ask for going
// forward: an envelope carrying an action THIS BUILD DOES NOT KNOW must be (a) consumed --
// never re-served to loop forever -- and (b) answered with a sealed refusal reply the phone
// can render, naming the unknown action.
//
// THE LIVE FACT THIS PINS (joyi, 2026-08-09 outage diagnosis): a phone-sealed `approve` against
// the released 0.8.0 gateway fell into command_loop.go's default arm, which errored BEFORE
// b.consume -- no reply was sealed, the op never resolved, and the approval card hung forever.
//
// THIS RUN IS EXPECTED GREEN, NOT RED. The nx44 wave (agents-tracker-nx44.4, released 0.9.0)
// already closed the general class: routeCommand's default arm forwards to opForAction, whose
// own default arm returns "unsupported command action %q"; forward's refuseCommand seals that
// reason as a protocol.OpError reply carrying the command's own OperationID BEFORE returning a
// refusedCommand; and handle's errors.As(err, &refused) branch still calls b.consume for that
// case, advancing the durable cursor past the item. Two existing tests already hold each half
// separately (nx444_unknownaction_test.go's TestNx444_AnUnknownActionIsRefusedWithASealedReply
// for the seal, refusal_consume_test.go's TestF3_ARefusedCommandIsConsumedSoTheMailboxDrains for
// the consume). This test is the M0.3 pin: it ships as the single place that names joyi's exact
// scenario -- an unknown action fed through the loop's Run, not just PollOnce -- and asserts
// both halves of the guarantee together, so a regression in either shows up here even if one of
// the two upstream tests is ever narrowed or removed.
import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

func TestM03_UnknownActionIsConsumedAndSealedNeverLeftPending(t *testing.T) {
	key := testContentKey()
	cmd := protocol.DeviceCommandAuth{
		// An action shape a phone one release ahead can seal that this build has no arm for --
		// exactly what `approve` was to a 0.8.0 gateway (joyi's live fact).
		Action:      "future_verb_this_build_does_not_know",
		Session:     "m/s1",
		OperationID: "op-skew-1",
		DeviceID:    "d1",
		Sig:         "device-signature",
	}
	const itemCursor = uint64(9)
	mb := &fakeMailbox{inbox: []relay.Item{{Cursor: itemCursor, Envelope: sealedCmd(t, key, 1, cmd)}}}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
		// Short so Run's MailboxWait loop (fakeMailbox never blocks) exits promptly once the
		// mailbox is drained and the context below is cancelled.
		WaitTimeout: 50 * time.Millisecond,
	})

	// Feed the loop via Run -- the production drive, not PollOnce directly -- for one cycle,
	// then cancel. fakeMailbox.MailboxWait never blocks, so Run makes progress immediately.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = b.Run(ctx) // returns ctx.Err() on cancel/timeout; the loop's own error path, not this test's

	// (a) CONSUMED: the durable cursor advanced past the unknown-action item. If it had not,
	// the same envelope would still sit ahead of the cursor and every future drain would re-open
	// and re-refuse it forever -- the "loop" joyi's diagnosis names.
	if got := b.Cursor(); got != itemCursor {
		t.Fatalf("cursor after an unknown action = %d, want %d: an unconsumed refusal is re-served "+
			"by every later drain, which is the forever-pending card joyi's diagnosis describes",
			got, itemCursor)
	}
	if len(fwd.ops) != 0 {
		t.Fatalf("forwarded ops = %v, want nothing: an action with no arm must never reach the daemon", fwd.ops)
	}

	// (b) SEALED REFUSAL REPLY THE PHONE CAN RENDER: exactly one reply, naming the unknown
	// action, correlated to the command's own OperationID so the phone's pending op resolves.
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies = %d, want exactly 1: with none sealed the phone's operation %q "+
			"stays pending for the life of the install -- not slow, unanswerable", len(mb.replies), cmd.OperationID)
	}
	_, ctrl := openReplyControl(t, key, mb.replies[0])
	if ctrl.Op != protocol.OpError {
		t.Errorf("refusal reply Op = %q, want %q", ctrl.Op, protocol.OpError)
	}
	if ctrl.OperationID != cmd.OperationID {
		t.Errorf("refusal reply OperationID = %q, want %q -- the id is the only handle the phone "+
			"has on its pending op", ctrl.OperationID, cmd.OperationID)
	}
	if !strings.Contains(ctrl.Error, cmd.Action) {
		t.Errorf("refusal reply Error = %q, want it to name the unknown action %q", ctrl.Error, cmd.Action)
	}
}
