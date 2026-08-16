package remotegw

// FAILING-FIRST (TDD RED, GG-5) for the Wave R1 "refusal-ops" slice's GATEWAY half (playbook
// §6.3, ADR-017 T5, ADR-007 B144). Companion to internal/protocol/r1_refusalops_test.go
// (which pins the daemon-side refusal) and internal/skeleton's device-auth companion; this
// file pins the gateway's own half of the same contract:
//
//   - opForAction gains an arm for each of the six new actions (session_launch,
//     composer_send, operation_status, turn_interrupt, terminal_control_begin,
//     terminal_control_end): they are FORWARDED to the daemon (Op == Action, mirroring
//     kill/delete/approve/push_prefs), never gateway-locally refused -- the refusal is the
//     DAEMON's to make, because only the daemon holds the device registry requireRemoteAuthz
//     authorizes against, and the gateway is a blind conduit (CommandForwarder's own doc).
//   - the BodyVersion the phone sealed rides the RemoteCommand the gateway forwards,
//     UNCHANGED.
//   - terminal_input / terminal_control_keepalive are DELIBERATELY EXCLUDED (ADR-017 T6: not
//     individually signed, riding only the E2EE frame's authenticated sender/sequence and a
//     device-bound confirmed generation, exactly like the existing lease input frame). As
//     Action strings on a SIGNED command they stay UNMAPPED in opForAction and fall to the
//     SAME generic "unsupported command action" refusal nx444_unknownaction_test.go and
//     m03_unknown_action_test.go already pin for any unknown action -- this file EXTENDS that
//     coverage with these two specific, easy-to-confuse-with-real names (rather than a
//     synthetic placeholder), so the exclusion reads as deliberate, not an oversight.
//   - PB-GW-3 (command_loop.go's persist-ordering discipline): all six ride the SAME
//     "forward before persist" class kill/delete/launch already ride, so a crash between the
//     daemon's answer and the checkpoint persist re-forwards the retained frame exactly once
//     more. For a stateless refusal that costs nothing extra: the daemon answers the SAME
//     op_not_implemented refusal both times, and the duplicate window still closes on the
//     next restart -- mirroring TestCrashMatrix_MutationDuplicateBoundedToOneRedelivery
//     (inbound_crash_matrix_test.go) for kill.
//
// "push-pairing shapes" (the playbook's seventh named item) is OUT OF SCOPE here for the
// reason internal/protocol's companion file records at length: ADR-015 P7 places that
// exchange inside the pairing transcript, before any device-signing key exists, not on this
// signed-command path at all.

import (
	"context"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// recordingForwarder captures every forwarded RemoteCommand IN FULL (unlike fakeForwarder,
// which only records its embedded DeviceCommandAuth) and its op string, answering with a
// configurable canned reply -- standing in for the daemon's own op_not_implemented refusal,
// since these tests exercise the gateway's forwarding decision, not the daemon.
type recordingForwarder struct {
	ops   []string
	seen  []protocol.RemoteCommand
	reply protocol.Control
	err   error
}

func (f *recordingForwarder) ForwardCommand(op string, rc protocol.RemoteCommand) (protocol.Control, error) {
	f.ops = append(f.ops, op)
	f.seen = append(f.seen, rc)
	if f.err != nil {
		return protocol.Control{}, f.err
	}
	reply := f.reply
	reply.SessionID = rc.Session
	reply.OperationID = rc.OperationID
	return reply, nil
}

// r1GatewayOp is the gateway-side half of internal/protocol's r1Op table: the action the
// phone signs and the daemon op it must forward to.
type r1GatewayOp struct{ action, op string }

func r1GatewayOps() []r1GatewayOp {
	return []r1GatewayOp{
		{protocol.ActionSessionLaunch, protocol.OpSessionLaunch},
		{protocol.ActionComposerSend, protocol.OpComposerSend},
		{protocol.ActionOperationStatus, protocol.OpOperationStatus},
		{protocol.ActionTurnInterrupt, protocol.OpTurnInterrupt},
		{protocol.ActionTerminalControlBegin, protocol.OpTerminalControlBegin},
		{protocol.ActionTerminalControlEnd, protocol.OpTerminalControlEnd},
	}
}

func TestR1RefusalOps_ForwardedToDaemonNeverGatewayLocallyRefused(t *testing.T) {
	for _, o := range r1GatewayOps() {
		t.Run(o.action, func(t *testing.T) {
			key := testContentKey()
			cmd := protocol.RemoteCommand{
				DeviceCommandAuth: protocol.DeviceCommandAuth{
					Action: o.action, Session: "m/s1", OperationID: "op-" + o.action,
					DeviceID: "d1", Sig: "device-signature",
				},
				BodyVersion: schema.CurrentProfileVersion,
			}
			if o.action == protocol.ActionSessionLaunch {
				// SUPERSEDED BY WAVE R5 for this one row (pre-recorded in
				// docs/verification/r5-red/go-red.txt §3): session_launch now carries a
				// real preset body, and the gateway inherits launch's and approve's
				// stripped-body rule (r5_launchroute_test.go pins the refusal). The
				// forward-not-refuse assertion below is unchanged -- it now holds for a
				// WELL-FORMED session_launch, exactly as it does for launch and approve.
				cmd.SessionLaunch = &schema.SessionLaunchReq{PresetID: "preset-api", PresetRevision: "rev-1"}
			}
			mb := &fakeMailbox{inbox: []relay.Item{{Cursor: 1, Envelope: sealAt(t, key, 1, 1, cmd)}}}
			fwd := &recordingForwarder{reply: protocol.Control{
				Op: protocol.OpError, ErrorCode: protocol.CodeNotImplemented, Error: o.action + ": not implemented yet",
			}}
			b := NewCommandBridge(CommandBridgeConfig{
				Mailbox: mb, Forwarder: fwd, Key: key, EpochID: 1, ReplyTarget: "phone-routing-id",
			})

			if _, err := b.PollOnce(context.Background()); err != nil {
				t.Fatalf("PollOnce: %v -- a mapped action must forward, not refuse locally", err)
			}
			if len(fwd.ops) != 1 || fwd.ops[0] != o.op {
				t.Fatalf("forwarded ops = %v, want exactly [%q]: opForAction must map %q to the "+
					"daemon op of the same name, mirroring kill/delete/approve", fwd.ops, o.op, o.action)
			}
			if fwd.seen[0].BodyVersion != schema.CurrentProfileVersion {
				t.Errorf("forwarded BodyVersion = %d, want %d: the gateway is a blind conduit and "+
					"must not alter the body version the phone bound this op to",
					fwd.seen[0].BodyVersion, schema.CurrentProfileVersion)
			}
			if len(mb.replies) != 1 {
				t.Fatalf("sealed replies = %d, want 1", len(mb.replies))
			}
		})
	}
}

// TestR1RefusalOps_TerminalInputAndKeepaliveStayUnmappedGenericRefusal extends
// nx444/M0.3's generic unknown-action coverage with the two names ADR-017 T6 forbids ever
// signing: they must fall to the SAME "unsupported command action" refusal any unrecognised
// action gets, never to op_not_implemented (which is reserved for a name this build DOES
// recognise).
func TestR1RefusalOps_TerminalInputAndKeepaliveStayUnmappedGenericRefusal(t *testing.T) {
	for _, action := range []string{"terminal_input", "terminal_control_keepalive"} {
		t.Run(action, func(t *testing.T) {
			key := testContentKey()
			cmd := protocol.DeviceCommandAuth{
				Action: action, Session: "m/s1", OperationID: "op-" + action, DeviceID: "d1", Sig: "device-signature",
			}
			mb := &fakeMailbox{inbox: []relay.Item{{Cursor: 1, Envelope: sealedCmd(t, key, 1, cmd)}}}
			fwd := &fakeForwarder{}
			b := NewCommandBridge(CommandBridgeConfig{
				Mailbox: mb, Forwarder: fwd, Key: key, EpochID: 1, ReplyTarget: "phone-routing-id",
			})

			if _, err := b.PollOnce(context.Background()); err == nil {
				t.Fatalf("%s: PollOnce accepted an action this vocabulary deliberately never signs; "+
					"it must stay refused like any other action with no arm", action)
			}
			if len(fwd.ops) != 0 {
				t.Fatalf("%s: forwarded ops = %v, want none -- %s rides the E2EE frame's own "+
					"sender/sequence and a confirmed control generation (ADR-017 T6), never a signed "+
					"action, so it must never reach the daemon's device authenticator", action, fwd.ops, action)
			}
			if len(mb.replies) != 1 {
				t.Fatalf("%s: sealed replies = %d, want exactly 1", action, len(mb.replies))
			}
			_, ctrl := openReplyControl(t, key, mb.replies[0])
			if ctrl.Op != protocol.OpError {
				t.Errorf("%s: refusal Op = %q, want %q", action, ctrl.Op, protocol.OpError)
			}
			if ctrl.ErrorCode == protocol.CodeNotImplemented {
				t.Errorf("%s: refusal carried op_not_implemented -- that code is for a NAME this "+
					"build recognises and does not yet serve; %s must never become a recognised "+
					"action at all, and must keep the generic unmapped-action refusal", action, action)
			}
			if !strings.Contains(ctrl.Error, action) {
				t.Errorf("%s: refusal text = %q, want it to name the action", action, ctrl.Error)
			}
		})
	}
}

// TestR1RefusalOps_CrashShapedRedeliveryGetsTheSameRefusal pins PB-GW-3 for the new
// refusal-only class: a crash between the daemon's answer and the checkpoint persist
// re-forwards the retained frame exactly once more, and -- because the daemon's
// op_not_implemented answer is a pure function of an unchanging input -- the SECOND answer is
// the SAME refusal as the first, not a different one and not a replay rejection. Mirrors
// TestCrashMatrix_MutationDuplicateBoundedToOneRedelivery (inbound_crash_matrix_test.go).
//
// RETARGETED BY WAVE R5 (pre-recorded in docs/verification/r5-red/go-red.txt §3): the
// driving action moved from session_launch -- whose real handler landed, so its answer is
// no longer the stateless op_not_implemented this test's premise names -- to
// composer_send, which remains refusal-only. Every assertion is unchanged; the premise
// ("a stateless refusal redelivers identically") is simply kept true of the op chosen.
func TestR1RefusalOps_CrashShapedRedeliveryGetsTheSameRefusal(t *testing.T) {
	key := inboundKey(97)
	const epoch uint32 = 3
	st := &memInboundState{failSave: errGatewayCrashed}
	rl := &retainingRelay{}
	cmd := protocol.RemoteCommand{
		DeviceCommandAuth: protocol.DeviceCommandAuth{
			Action: protocol.ActionComposerSend, OperationID: "op-launch-crash", DeviceID: "d1", Sig: "device-signature",
		},
		BodyVersion: schema.CurrentProfileVersion,
	}
	env := sealAt(t, key, epoch, 1, cmd)
	rl.add(1, env)

	poll := func(cursor uint64) *recordingForwarder {
		if cursor != 0 {
			rl.add(cursor, env)
		}
		fwd := &recordingForwarder{reply: protocol.Control{
			Op: protocol.OpError, ErrorCode: protocol.CodeNotImplemented,
			Error: protocol.ActionComposerSend + ": not implemented yet",
		}}
		b := NewCommandBridge(CommandBridgeConfig{
			Mailbox: rl, Forwarder: fwd, Key: key, EpochID: epoch, ReplyTarget: "phone", Inbound: st,
		})
		_, err := b.PollOnce(context.Background())
		t.Logf("poll err = %v", err)
		return fwd
	}

	f1 := poll(0)
	if len(f1.ops) != 1 {
		t.Fatalf("run 1 forwarded %d, want 1", len(f1.ops))
	}

	st.mu.Lock()
	st.failSave = nil
	st.mu.Unlock()
	f2 := poll(5)
	if len(f2.ops) != 1 {
		t.Fatalf("run 2 forwarded %d, want exactly 1 re-forward: a refusal must not be LOST by a "+
			"crash between the daemon's answer and the persist, exactly like any other "+
			"mutation-class op", len(f2.ops))
	}
	if f2.seen[0].OperationID != f1.seen[0].OperationID {
		t.Fatalf("re-forwarded operation_id = %q, want the original %q", f2.seen[0].OperationID, f1.seen[0].OperationID)
	}
	if len(rl.replies) != 2 {
		t.Fatalf("sealed replies across both runs = %d, want 2 (one per forward)", len(rl.replies))
	}
	_, first := openReplyControl(t, key, rl.replies[0])
	_, second := openReplyControl(t, key, rl.replies[1])
	if first.ErrorCode != second.ErrorCode || first.Error != second.Error {
		t.Fatalf("redelivered refusal differs from the first: (%q,%q) vs (%q,%q) -- a crash-shaped "+
			"redelivery of a stateless refusal must answer identically, not drift",
			first.ErrorCode, first.Error, second.ErrorCode, second.Error)
	}
	if first.ErrorCode != protocol.CodeNotImplemented {
		t.Fatalf("refusal ErrorCode = %q, want %q", first.ErrorCode, protocol.CodeNotImplemented)
	}

	// Run 3: the window is closed.
	f3 := poll(9)
	if len(f3.ops) != 0 {
		t.Fatalf("run 3 forwarded %d more time(s), want 0: once the consumption is durable the "+
			"retained frame must be refused at the guard, not re-forwarded forever", len(f3.ops))
	}
}
