package protocol

// FAILING-FIRST (TDD RED, GG-5) for Wave R6's REAL turn_interrupt handler -- Mirror M2.4
// ("Stop becomes a signed interrupt op"), the complete-chat table's Interrupt row ("a
// semantic interruption reaches the current turn and is observed"), ADR-009 (1)/(5).
// Bead: agents-tracker-hggx.7.
//
// THE CONTRACT these tests freeze:
//
//   - TurnInterrupter: the optional DaemonAPI seam handleTurnInterrupt dispatches to,
//     mirroring InteractionApprover/ComposerSender -- (ErrorCode, error) beside the call.
//
//     SUPERSESSION, EXECUTED (Wave R6 review fix-pack, finding B7; recorded in
//     docs/verification/r6-red/chat-red.txt). This bullet read: "The op carries NO new
//     body: the session it names is the signed tuple's own subject (Control.SessionID),
//     so the existing envelope conventions cover it whole and no new crypto appears."
//     The last clause still holds and the first two do not. A probe showed a Stop
//     rendered against turnA typing the cancel sequence into turnB, because the op had
//     no turn coordinate at all -- session is the AUTHORIZATION subject, the turn is the
//     OPERATIONAL one. The op now carries TurnInterruptReq(session, expected_turn),
//     bound through the tuple's EXISTING content slot via TurnInterruptContentHash, so
//     HARD RULE 7 is still honored: no new crypto anywhere, composer_send's own
//     arrangement reused verbatim. Every assertion below is otherwise unchanged.
//   - CodeInterruptUnsupported = "interrupt_unsupported": the honest ADR-017-shaped
//     refusal for a session whose adapter proves no semantic interrupt seam. Distinct
//     from not_authorized (the caller is fine; the capability is absent) and from
//     invalid_field (the frame is well-formed). "This provider version has no safe
//     remote interrupt" is a nameable state, never a hang and never a guessed keystroke.
//   - Ordering: requireRemoteAuthz FIRST, then the session-shape gate, then the seam.
//
// SUPERSESSION, PRE-RECORDED: r1_refusalops_test.go's turn_interrupt row (wantCode
// CodeNotImplemented) is retargeted by this op's GREEN slice to what the REAL handler
// answers that same session-bound, bodyless frame -- for turn_interrupt the frame IS
// well-formed (the op has no body), so the row's retarget is the seam's own answer for
// stubbed backends, exactly as R5 retargeted session_launch. The choke-point ordering
// assertions there are inherited unchanged.

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type r6InterruptCall struct {
	machine      string
	operationID  string
	session      string
	expectedTurn string
}

type r6InterruptBackend struct {
	*stubDaemon
	mu      sync.Mutex
	calls   []r6InterruptCall
	intCode ErrorCode
	intErr  error
}

func newR6InterruptBackend() *r6InterruptBackend {
	return &r6InterruptBackend{stubDaemon: newStubDaemon()}
}

func (b *r6InterruptBackend) InterruptTurn(machine, operationID string, req TurnInterruptReq) (ErrorCode, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, r6InterruptCall{
		machine: machine, operationID: operationID, session: req.Session, expectedTurn: req.ExpectedTurn,
	})
	return b.intCode, b.intErr
}

func (b *r6InterruptBackend) interruptCalls() []r6InterruptCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]r6InterruptCall(nil), b.calls...)
}

var _ TurnInterrupter = (*r6InterruptBackend)(nil)

// r6InterruptFrame builds a WELL-FORMED interrupt frame. Since fix-pack B7 that includes
// the body naming the turn: a bodyless frame is now refused, and its refusal is pinned by
// r6fix_chatgates_test.go rather than by making every test here drive a malformed frame.
func r6InterruptFrame(rep Control, opID, session string) Control {
	return r6InterruptFrameTurn(rep, opID, session, "01JTURN")
}

func r6InterruptFrameTurn(rep Control, opID, session, turn string) Control {
	exp := time.Now().Add(time.Minute)
	c := Control{
		Op: OpTurnInterrupt, EndpointID: rep.EndpointID, SessionID: session,
		OperationID: opID, DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
		BodyVersion: schema.CurrentProfileVersion,
	}
	if session != "" {
		c.TurnInterrupt = &TurnInterruptReq{Session: session, ExpectedTurn: turn}
	}
	return c
}

// TestR6TurnInterrupt_InterruptUnsupportedIsItsOwnSealedCode pins the code VALUE the
// phone's per-verb refusal state names.
func TestR6TurnInterrupt_InterruptUnsupportedIsItsOwnSealedCode(t *testing.T) {
	if got := string(CodeInterruptUnsupported); got != "interrupt_unsupported" {
		t.Fatalf("CodeInterruptUnsupported = %q, want the sealed wire value \"interrupt_unsupported\"", got)
	}
}

// TestR6TurnInterrupt_AuthorizedInterruptReachesTheSeamAndRepliesOK is the success path:
// authz at the shared choke point, the seam sees the signed session, the reply is OK --
// the phone's Stop shows a visible SUCCESS, not a fire-and-forget shrug (this is what
// retires the raw-\x03 lease-plane Interrupt: an input frame is dropped silently, a
// signed op resolves).
func TestR6TurnInterrupt_AuthorizedInterruptReachesTheSeamAndRepliesOK(t *testing.T) {
	b := newR6InterruptBackend()
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(r6InterruptFrame(rep, "devA:01JINT", sid))
	got := rc.readControl()
	if got.Op == OpError {
		t.Fatalf("authorized turn_interrupt refused: code %q %q", got.ErrorCode, got.Error)
	}
	if got.OperationID != "devA:01JINT" {
		t.Errorf("reply operation_id = %q, want devA:01JINT", got.OperationID)
	}

	tuples := b.authorizedTuples()
	if len(tuples) != 1 {
		t.Fatalf("authenticator saw %d tuples, want 1", len(tuples))
	}
	if tuples[0].Action != ActionTurnInterrupt {
		t.Errorf("authz tuple action = %q, want %q", tuples[0].Action, ActionTurnInterrupt)
	}
	if tuples[0].Session != sid {
		t.Errorf("authz subject = %q, want %q", tuples[0].Session, sid)
	}

	calls := b.interruptCalls()
	if len(calls) != 1 {
		t.Fatalf("seam saw %d interrupts, want 1", len(calls))
	}
	if calls[0].session != sid {
		t.Errorf("seam session = %q, want %q", calls[0].session, sid)
	}
	if calls[0].operationID != "devA:01JINT" {
		t.Errorf("seam operation id = %q, want devA:01JINT", calls[0].operationID)
	}
	// Fix-pack B7: the turn the phone RENDERED the Stop against reaches the seam, which is
	// the whole point -- the daemon can only refuse a stale Stop if it is told which turn
	// was on screen when the button was drawn.
	if calls[0].expectedTurn != "01JTURN" {
		t.Errorf("seam expected_turn = %q, want 01JTURN", calls[0].expectedTurn)
	}
}

// TestR6TurnInterrupt_ASessionlessFrameIsRefusedBeforeTheSeam: an interrupt names a
// session or it names nothing this op can reach.
func TestR6TurnInterrupt_ASessionlessFrameIsRefusedBeforeTheSeam(t *testing.T) {
	b := newR6InterruptBackend()
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r6InterruptFrame(rep, "devA:01JNOSESS", ""))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("sessionless turn_interrupt = op %q code %q, want error/invalid_field", got.Op, got.ErrorCode)
	}
	if n := len(b.interruptCalls()); n != 0 {
		t.Errorf("seam saw %d interrupts for a sessionless frame, want 0", n)
	}
}

// TestR6TurnInterrupt_ForgedSignatureNeverReachesTheSeam: same choke point, same rule.
func TestR6TurnInterrupt_ForgedSignatureNeverReachesTheSeam(t *testing.T) {
	b := newR6InterruptBackend()
	b.authzFn = func(DeviceCommandAuth) error { return errForged }
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r6InterruptFrame(rep, "devA:01JFORGEDINT", rep.EndpointID+"/sess1"))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
		t.Fatalf("forged turn_interrupt = op %q code %q, want error/not_authorized", got.Op, got.ErrorCode)
	}
	if n := len(b.interruptCalls()); n != 0 {
		t.Errorf("seam saw %d interrupts from a forged frame, want 0", n)
	}
}

// TestR6TurnInterrupt_ASeamRefusalSurfacesItsCodeVerbatim: the daemon's honest
// capability refusal (interrupt_unsupported) crosses back as its own code, so the phone
// can render the ADR-017-shaped state instead of a generic failure.
func TestR6TurnInterrupt_ASeamRefusalSurfacesItsCodeVerbatim(t *testing.T) {
	b := newR6InterruptBackend()
	b.intCode = CodeInterruptUnsupported
	b.intErr = errors.New("claude at this recorded version proves no semantic interrupt seam")
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r6InterruptFrame(rep, "devA:01JUNSUP", rep.EndpointID+"/sess1"))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInterruptUnsupported {
		t.Fatalf("unsupported interrupt = op %q code %q, want error/interrupt_unsupported", got.Op, got.ErrorCode)
	}
	if got.Error == "" {
		t.Error("interrupt_unsupported refusal carries no text; the phone has nothing to render")
	}
}

// TestR6TurnInterrupt_ABackendWithoutTheSeamRefusesRatherThanPretending: OK here is a
// Stop button that "worked" while the agent kept running -- the exact defect the signed
// op exists to end.
func TestR6TurnInterrupt_ABackendWithoutTheSeamRefusesRatherThanPretending(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r6InterruptFrame(rep, "devA:01JNOSEAMINT", rep.EndpointID+"/sess1"))
	got := rc.readControl()
	if got.Op != OpError {
		t.Fatalf("seamless backend answered op %q, want an error", got.Op)
	}
}
