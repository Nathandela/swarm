package remotegw

// FAILING-FIRST (TDD RED, GG-5) for Wave R6 review findings B1 and B6 -- the two gateway
// defects, both of the SAME shape: a reference path nothing exercised.
//
// B1 (BLOCKER, orchestrator-confirmed). Gateway.ForwardCommand assembled the daemon
// protocol.Control by copying Launch, Approve, SessionLaunch and SubjectOperationID -- and
// NEVER rc.ComposerSend. grep confirmed no assignment to Control.ComposerSend existed outside
// tests. So a real phone send arrived at the daemon with a NIL BODY and was refused
// invalid_field, while every test that "covered" composer_send hand-built its own Control and
// bypassed the assembly entirely. This is the R5 PolicyEnv blocker one field over.
//
// B6 (BLOCKER). ADR-014 §1 stated as an ACCEPTED DECISION that interaction_history and
// interaction_detail "are gateway-routed", and r6-chat.md marked M3.1/M3.3 GREEN. There was
// no arm in opForAction, no arm in routeCommand and no action constant for either name: a
// phone-issued read hit opForAction's default and was refused "unsupported command action".
//
// THE FENCE IS REFLECTIVE ON PURPOSE. One hand-written "the composer body arrives" assertion
// would have closed B1 and left the NEXT body to be forgotten in exactly the same way -- which
// is what happened between R5 and R6. TestR6Fix_EveryRemoteCommandBodyReachesTheDaemonControl
// walks protocol.RemoteCommand BY REFLECTION, sets every body field non-nil, runs the REAL
// assembly, and fails naming any field that did not arrive. A new body added without a
// carriage line fails here on the day it is added, not on the day a user taps Send.

import (
	"context"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// r6FixForwarder records the WHOLE forwarded RemoteCommand (r6Forwarder keeps only the auth
// tuple and the composer body, which is exactly the narrowness finding B1 hid behind).
type r6FixForwarder struct {
	ops  []string
	seen []protocol.RemoteCommand
}

func (f *r6FixForwarder) ForwardCommand(op string, rc protocol.RemoteCommand) (protocol.Control, error) {
	f.ops = append(f.ops, op)
	f.seen = append(f.seen, rc)
	return protocol.Control{Op: op, SessionID: rc.Session, OperationID: rc.OperationID}, nil
}

// bodyFieldsOf reports the names of every pointer-to-struct body field on t. Those are
// exactly the fields that carry an op's payload; the scalars (BodyVersion,
// SubjectOperationID, ResyncCursor) are separately asserted by their own ops' tests.
func bodyFieldsOf(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Type.Kind() == reflect.Pointer && f.Type.Elem().Kind() == reflect.Struct && f.IsExported() {
			out = append(out, f.Name)
		}
	}
	return out
}

// notForwarded are the RemoteCommand bodies that DELIBERATELY never reach the daemon, each
// with the reason it does not. The list is hand-kept on purpose: adding a row is a decision a
// reader can see and argue with, whereas a body that quietly never arrives is finding B1.
var notForwarded = map[string]string{
	"PushPrefs": "push_prefs is authorized by the daemon but APPLIED AT THE GATEWAY " +
		"(PB-PUSH-8/PB-PUSH-10, CommandBridge.applyPushPrefs): the preference is gateway-side " +
		"state, so there is no Control.PushPrefs for it to arrive in and routeCommand never " +
		"reaches forward() for this action at all.",
}

// TestR6Fix_EveryRemoteCommandBodyReachesTheDaemonControl is B1's permanent fence, and it is
// deliberately about the CLASS rather than about composer_send.
func TestR6Fix_EveryRemoteCommandBodyReachesTheDaemonControl(t *testing.T) {
	rcType := reflect.TypeOf(protocol.RemoteCommand{})

	bodies := bodyFieldsOf(rcType)
	// Vacuity control: a reflection walk that found nothing would make every assertion
	// below a no-op and this fence would pass over an assembly that copied not one field.
	if len(bodies) < 4 {
		t.Fatalf("the walk found %d body fields on RemoteCommand (%v); it is not reading the type",
			len(bodies), bodies)
	}

	// Build a RemoteCommand with EVERY body field non-nil.
	rc := reflect.New(rcType).Elem()
	for _, name := range bodies {
		f := rc.FieldByName(name)
		f.Set(reflect.New(f.Type().Elem()))
	}
	filled := rc.Interface().(protocol.RemoteCommand)

	got := reflect.ValueOf(forwardControl("endpoint-1", protocol.OpComposerSend, filled))
	for _, name := range bodies {
		if reason, ok := notForwarded[name]; ok {
			// An exemption must state WHY, and the anti-vacuity check below proves the
			// exemption list is not quietly absorbing new fields.
			t.Logf("RemoteCommand.%s is exempt from forwarding: %s", name, reason)
			continue
		}
		cf := got.FieldByName(name)
		if !cf.IsValid() {
			t.Errorf("RemoteCommand.%s has NO field of that name on Control: a body the phone can "+
				"seal and the daemon can never receive. Either carry it across under a documented "+
				"different name and add that name to this fence, or the body is unroutable.", name)
			continue
		}
		if cf.IsNil() {
			t.Errorf("RemoteCommand.%s did NOT reach Control.%s: forwardControl forgot to copy it. "+
				"This is finding B1 exactly -- composer_send was refused invalid_field on every real "+
				"phone send because this one line was missing, and no test noticed because every test "+
				"hand-built its own Control.", name, name)
		}
	}

	// Anti-vacuity on the exemption list itself: an exemption for a field that no longer
	// exists is dead weight that teaches the next reader to add more rows.
	for name := range notForwarded {
		if _, ok := reflect.TypeOf(protocol.RemoteCommand{}).FieldByName(name); !ok {
			t.Errorf("notForwarded exempts %q, which is not a RemoteCommand field any more; "+
				"delete the row", name)
		}
	}
}

// TestR6Fix_TheComposerBodyReachesTheDaemonThroughTheRealAssembly is B1's named case, kept
// beside the class fence because a blocker deserves a test that says its own name.
func TestR6Fix_TheComposerBodyReachesTheDaemonThroughTheRealAssembly(t *testing.T) {
	body := &protocol.ComposerSendReq{Session: "m/s1", ExpectedTurn: "01JTURN", Text: "run the linter"}
	ctrl := forwardControl("endpoint-1", protocol.OpComposerSend, protocol.RemoteCommand{
		DeviceCommandAuth: protocol.DeviceCommandAuth{
			Action: protocol.ActionComposerSend, Session: "m/s1", OperationID: "op-1",
			DeviceID: "d1", Sig: "device-signature",
		},
		ComposerSend: body,
		BodyVersion:  schema.CurrentProfileVersion,
	})
	if ctrl.ComposerSend == nil {
		t.Fatalf("the assembled Control carries NO composer body; the daemon refuses it invalid_field " +
			"and the user's message is never typed")
	}
	if *ctrl.ComposerSend != *body {
		t.Errorf("Control.ComposerSend = %+v, want %+v verbatim: any alteration here breaks the "+
			"phone's ComposerSendContentHash binding daemon-side", *ctrl.ComposerSend, *body)
	}
	if ctrl.BodyVersion != schema.CurrentProfileVersion {
		t.Errorf("BodyVersion = %d, want %d carried unchanged", ctrl.BodyVersion, schema.CurrentProfileVersion)
	}
}

// TestR6Fix_TheInterruptBodyReachesTheDaemonThroughTheRealAssembly is B7's carriage half:
// turn_interrupt stopped being bodyless, so it acquired a body that can be dropped.
func TestR6Fix_TheInterruptBodyReachesTheDaemonThroughTheRealAssembly(t *testing.T) {
	body := &protocol.TurnInterruptReq{Session: "m/s1", ExpectedTurn: "01JTURN"}
	ctrl := forwardControl("endpoint-1", protocol.OpTurnInterrupt, protocol.RemoteCommand{
		DeviceCommandAuth: protocol.DeviceCommandAuth{
			Action: protocol.ActionTurnInterrupt, Session: "m/s1", OperationID: "op-2",
		},
		TurnInterrupt: body,
	})
	if ctrl.TurnInterrupt == nil || *ctrl.TurnInterrupt != *body {
		t.Fatalf("Control.TurnInterrupt = %v, want %+v verbatim: a Stop whose turn is dropped in "+
			"assembly is refused invalid_field daemon-side, and the user's Stop does nothing",
			ctrl.TurnInterrupt, *body)
	}
}

// TestR6Fix_TheM3ReadsAreRoutedAndNotRefusedAsUnknownActions is B6's fence: ADR-014's
// "gateway-routed" must be true of the code, not only of the ADR.
func TestR6Fix_TheM3ReadsAreRoutedAndNotRefusedAsUnknownActions(t *testing.T) {
	for _, tc := range []struct {
		action, op string
		rc         protocol.RemoteCommand
	}{
		{protocol.ActionInteractionHistory, protocol.OpInteractionHistory, protocol.RemoteCommand{
			DeviceCommandAuth: protocol.DeviceCommandAuth{Action: protocol.ActionInteractionHistory},
			History:           &protocol.InteractionHistoryReq{Session: "m/s1", BeforeItem: "01JB", Limit: 20},
		}},
		{protocol.ActionInteractionDetail, protocol.OpInteractionDetail, protocol.RemoteCommand{
			DeviceCommandAuth: protocol.DeviceCommandAuth{Action: protocol.ActionInteractionDetail},
			Detail:            &protocol.InteractionDetailReq{Session: "m/s1", ItemID: "01JB"},
		}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			op, err := opForAction(tc.rc)
			if err != nil {
				t.Fatalf("opForAction(%s) = %v; ADR-014 §1 states as an ACCEPTED DECISION that this "+
					"read is gateway-routed, so a refusal here means the ADR documents a route that "+
					"does not exist and M3 is unreachable from a phone", tc.action, err)
			}
			if op != tc.op {
				t.Fatalf("opForAction(%s) = %q, want %q", tc.action, op, tc.op)
			}
		})
	}
}

// TestR6Fix_AStrippedM3ReadBodyIsRefusedNeverForwardedBodyless inherits launch's, approve's,
// session_launch's and composer_send's rule for the two reads: a body lost in transit must
// not reach the daemon as a read naming no item.
func TestR6Fix_AStrippedM3ReadBodyIsRefusedNeverForwardedBodyless(t *testing.T) {
	for _, action := range []string{protocol.ActionInteractionHistory, protocol.ActionInteractionDetail} {
		t.Run(action, func(t *testing.T) {
			_, err := opForAction(protocol.RemoteCommand{
				DeviceCommandAuth: protocol.DeviceCommandAuth{Action: action},
			})
			if err == nil {
				t.Fatalf("opForAction forwarded a bodyless %s; a read naming no item would surface "+
					"to the user as some other refusal for a frame that merely lost its payload", action)
			}
		})
	}
}

// TestR6Fix_AStrippedInterruptBodyIsRefusedNeverForwardedBodyless is B7's gateway half.
//
// SUPERSESSION, EXECUTED: r6_composerroute_test.go's
// TestR6ComposerRoute_TurnInterruptStaysBodylessAndForwards pinned the OPPOSITE -- that a
// bodyless interrupt forwards -- on the premise that "the signed tuple's session IS its whole
// subject". Review finding B7 disproved that premise by probe (a Stop rendered against turnA
// typed the cancel sequence into turnB), and the orchestrator ruled expected_turn is bound.
// That test is retargeted in place with its own supersession note; this is its replacement
// assertion, and NOTHING is weakened: the interrupt still forwards, it simply must carry the
// turn it names.
func TestR6Fix_AStrippedInterruptBodyIsRefusedNeverForwardedBodyless(t *testing.T) {
	if _, err := opForAction(protocol.RemoteCommand{
		DeviceCommandAuth: protocol.DeviceCommandAuth{Action: protocol.ActionTurnInterrupt},
	}); err == nil {
		t.Fatal("opForAction forwarded a bodyless turn_interrupt; a Stop naming no turn is exactly " +
			"the frame finding B7 exists to make unspeakable -- it lands the cancel key in whichever " +
			"turn is current on arrival, including one the owner just started at the terminal")
	}
}

// TestR6Fix_TheM3ReadsForwardEndToEndThroughTheBridge drives the whole gateway leg: a sealed,
// UNSIGNED interaction_history in the mailbox is opened, routed, forwarded to the daemon as
// OpInteractionHistory with its body intact, and the daemon's page is sealed back to the
// phone. Nothing here is hand-built past the mailbox frame.
func TestR6Fix_TheM3ReadsForwardEndToEndThroughTheBridge(t *testing.T) {
	key := testContentKey()
	fwd := &r6FixForwarder{}
	cmd := protocol.RemoteCommand{
		DeviceCommandAuth: protocol.DeviceCommandAuth{
			Action: protocol.ActionInteractionHistory, Session: "m/s1", OperationID: "op-hist-1",
		},
		History: &protocol.InteractionHistoryReq{Session: "m/s1", BeforeItem: "01JB", Limit: 20},
	}
	b, mb := r6Bridge(t, key, fwd, sealAt(t, key, 1, 1, cmd))
	if _, err := b.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v -- the M3 read must forward, not be refused locally", err)
	}
	if len(fwd.ops) != 1 || fwd.ops[0] != protocol.OpInteractionHistory {
		t.Fatalf("forwarded ops = %v, want exactly [%q]", fwd.ops, protocol.OpInteractionHistory)
	}
	if len(fwd.seen) != 1 || fwd.seen[0].History == nil {
		t.Fatalf("the read reached the daemon with no body: %+v", fwd.seen)
	}
	if *fwd.seen[0].History != *cmd.History {
		t.Errorf("forwarded read body = %+v, want %+v verbatim (blind conduit)",
			*fwd.seen[0].History, *cmd.History)
	}
	if len(mb.replies) != 1 {
		t.Fatalf("sealed replies = %d, want 1: an unanswered read leaves 'load earlier' spinning "+
			"forever", len(mb.replies))
	}
}
