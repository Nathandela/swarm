package protocol

// FAILING-FIRST (TDD RED, GG-5) for Wave R6's REAL composer_send handler -- Mirror M2.4,
// IS-LIFE-5 (interaction-schema.md §8), ADR-009 decisions (5) and (8), playbook §8.1 step 3.
// Bead: agents-tracker-hggx.7. Undefined symbols -> compile-fail RED is expected and valid
// per this wave's HARD RULES and the r1/r5 precedent in this same directory.
//
// THE CONTRACT these tests freeze:
//
//   - schema.ComposerSendReq (aliased here as ComposerSendReq): the IS-LIFE-5 body,
//     `(session, expected_turn, text)` under wire names `session` / `expected_turn` /
//     `text`, riding Control.ComposerSend under wire name `composer_send`. ADR-009 (8)
//     books exactly one new protocol.md row for this op and its field -- the GREEN slice
//     that adds the field MUST add the protocol.md rows in the same commit or the GG-7
//     drift fence (protocolmd_test.go) fails the build. NOTE FOR THAT AUTHOR: a Meaning
//     cell must say "wire name", never the literal phrase the bidi test parses as a
//     header row.
//   - schema.ComposerSendContentHash(req): the canonical digest binding the body into the
//     device signature, mirroring SessionLaunchContentHash exactly -- without it a
//     compromised gateway could re-point a valid signature at different text or at a
//     different turn (the R1 skeleton recorded this gap as "closes when each op's real
//     body (and its ContentHash) lands"; this is that landing).
//   - CodeStaleTurn = "stale_turn": the D10-taxonomy refusal for a send whose
//     expected_turn no longer names the session's current turn. Its OWN code, distinct
//     from stale_approval (different subject, different remedy: re-read the transcript,
//     not re-list cards).
//   - ComposerSender: the optional DaemonAPI seam handleComposerSend dispatches to,
//     mirroring InteractionApprover -- (ErrorCode, error) beside the call so D10 codes
//     surface verbatim.
//   - Handler ordering: structural body checks and requireRemoteAuthz FIRST (same choke
//     point as kill/launch/approve, content hash bound), body-version gate, THEN the
//     seam. A backend without the seam refuses loudly rather than replying OK to a send
//     nothing delivered.
//
// SUPERSESSION, PRE-RECORDED (the r5 precedent, r1_refusalops_test.go:96-104): the GREEN
// slice for this file IS the real handler for composer_send. r1Ops()'s composer_send row
// (wantCode CodeNotImplemented) is retargeted in that slice to what the REAL handler
// answers the same bodyless frame -- CodeInvalidField, refused AFTER the same authz +
// body-version gates -- exactly as R5 retargeted session_launch and operation_status.
// Every choke-point ordering assertion there is inherited unchanged. No other assertion
// in r1_refusalops_test.go is touched.

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// errStaleTurnForTest is the seam-side error a stale-turn refusal rides beside its code.
var errStaleTurnForTest = errors.New("expected_turn 01JOLDTURN is not the session's current turn")

// r6ComposerCall records one send the backend saw.
type r6ComposerCall struct {
	machine     string
	operationID string
	req         ComposerSendReq
}

// r6ComposerBackend is a stubDaemon that ALSO implements ComposerSender, recording every
// call and answering a configurable (code, error).
type r6ComposerBackend struct {
	*stubDaemon
	mu       sync.Mutex
	sends    []r6ComposerCall
	sendCode ErrorCode
	sendErr  error
}

func newR6ComposerBackend() *r6ComposerBackend {
	return &r6ComposerBackend{stubDaemon: newStubDaemon()}
}

func (b *r6ComposerBackend) ComposerSend(machine, operationID string, req ComposerSendReq) (ErrorCode, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sends = append(b.sends, r6ComposerCall{machine: machine, operationID: operationID, req: req})
	return b.sendCode, b.sendErr
}

func (b *r6ComposerBackend) sendCalls() []r6ComposerCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]r6ComposerCall(nil), b.sends...)
}

// The seam is the one handleComposerSend dispatches on. Pinned at compile time.
var _ ComposerSender = (*r6ComposerBackend)(nil)

// r6ComposerFrame builds one authorized-shaped composer_send frame around body.
func r6ComposerFrame(rep Control, opID string, body *ComposerSendReq) Control {
	exp := time.Now().Add(time.Minute)
	c := Control{
		Op: OpComposerSend, EndpointID: rep.EndpointID,
		OperationID: opID, DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
		BodyVersion:  schema.CurrentProfileVersion,
		ComposerSend: body,
	}
	if body != nil {
		c.SessionID = body.Session
	}
	return c
}

// TestR6ComposerSend_WireShapeRoundTripsUnderItsOwnKeys pins the exact wire names
// IS-LIFE-5 and ADR-009 (8) commit to: `composer_send` on Control, and `session` /
// `expected_turn` / `text` inside the body. These are the names the booked protocol.md
// row documents and the phone binds its signature against; a drifted key here is a
// signature that verifies over different bytes than the daemon reads.
func TestR6ComposerSend_WireShapeRoundTripsUnderItsOwnKeys(t *testing.T) {
	c := Control{Op: OpComposerSend, ComposerSend: &ComposerSendReq{
		Session: "ep/sess1", ExpectedTurn: "01JTURN", Text: "ship it",
	}}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"composer_send"`, `"session":"ep/sess1"`, `"expected_turn":"01JTURN"`, `"text":"ship it"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("serialized composer_send Control %s lacks %s", raw, key)
		}
	}
	var back Control
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ComposerSend == nil || *back.ComposerSend != *c.ComposerSend {
		t.Fatalf("round trip = %+v, want %+v", back.ComposerSend, c.ComposerSend)
	}
	// A Control that carries no composer body must serialize byte-identically to the
	// pre-R6 shape (schema.Control's own rule: every field omitempty).
	if raw, err := json.Marshal(Control{Op: OpKill}); err != nil || strings.Contains(string(raw), "composer_send") {
		t.Errorf("a composer-less Control leaks the new key: %s (err %v)", raw, err)
	}
}

// TestR6ComposerSend_StaleTurnIsItsOwnSealedCode pins the code VALUE. The phone renders a
// distinct, gentle state from it (M2.4: "the stale_turn refusal surfaced gently"), so the
// string is wire contract, not an implementation detail.
func TestR6ComposerSend_StaleTurnIsItsOwnSealedCode(t *testing.T) {
	if got := string(CodeStaleTurn); got != "stale_turn" {
		t.Fatalf("CodeStaleTurn = %q, want the sealed wire value \"stale_turn\"", got)
	}
	if CodeStaleTurn == CodeStaleApproval {
		t.Fatal("stale_turn and stale_approval collapsed into one code; they answer different questions with different remedies")
	}
}

// TestR6ComposerSend_AuthorizedSendReachesTheSeamAndBindsTheBodyHash is the success path:
// authz runs at the same choke point as every mutating op, the tuple's content slot binds
// ComposerSendContentHash(body) so a gateway cannot alter text or expected_turn under a
// valid signature, the seam sees the exact body, and the reply is OK for the session.
func TestR6ComposerSend_AuthorizedSendReachesTheSeamAndBindsTheBodyHash(t *testing.T) {
	b := newR6ComposerBackend()
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	body := &ComposerSendReq{Session: rep.EndpointID + "/sess1", ExpectedTurn: "01JTURNA", Text: "run the tests"}
	rc.writeControl(r6ComposerFrame(rep, "devA:01JSEND", body))
	got := rc.readControl()
	if got.Op == OpError {
		t.Fatalf("authorized composer_send refused: code %q %q", got.ErrorCode, got.Error)
	}
	if got.OperationID != "devA:01JSEND" {
		t.Errorf("reply operation_id = %q, want devA:01JSEND (replies are claimed by op id)", got.OperationID)
	}

	tuples := b.authorizedTuples()
	if len(tuples) != 1 {
		t.Fatalf("authenticator saw %d tuples, want 1", len(tuples))
	}
	if tuples[0].Action != ActionComposerSend {
		t.Errorf("authz tuple action = %q, want %q", tuples[0].Action, ActionComposerSend)
	}
	if tuples[0].Session != body.Session {
		t.Errorf("authz subject = %q, want the body session %q", tuples[0].Session, body.Session)
	}
	if want := schema.ComposerSendContentHash(body); !bytes.Equal(tuples[0].ContentHash, want) {
		t.Errorf("authz content hash = %x, want ComposerSendContentHash(body) %x: without the "+
			"binding a gateway could re-point a valid signature at different text or a different turn",
			tuples[0].ContentHash, want)
	}

	calls := b.sendCalls()
	if len(calls) != 1 {
		t.Fatalf("seam saw %d sends, want 1", len(calls))
	}
	if calls[0].req != *body {
		t.Errorf("seam req = %+v, want the wire body %+v", calls[0].req, *body)
	}
	if calls[0].operationID != "devA:01JSEND" {
		t.Errorf("seam operation id = %q, want devA:01JSEND", calls[0].operationID)
	}
}

// TestR6ComposerSend_BodylessFrameIsRefusedInvalidFieldAndNothingIsForwarded: the frame
// r1_refusalops_test.go sends (authorized, correctly versioned, NO body) is now refused
// by the REAL handler as structural, after authz -- the pre-recorded supersession above.
func TestR6ComposerSend_BodylessFrameIsRefusedInvalidFieldAndNothingIsForwarded(t *testing.T) {
	b := newR6ComposerBackend()
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	f := r6ComposerFrame(rep, "devA:01JNOBODY", nil)
	f.SessionID = rep.EndpointID + "/sess1"
	rc.writeControl(f)
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("bodyless composer_send = op %q code %q, want error/invalid_field", got.Op, got.ErrorCode)
	}
	if n := len(b.sendCalls()); n != 0 {
		t.Errorf("seam saw %d sends for a bodyless frame, want 0", n)
	}
}

// TestR6ComposerSend_BodySessionMustMatchTheSignedSession is handleApprove's collision
// rule applied here: the gateway is the documented owner-uid residual, so the two session
// coordinates free to differ would let it point a signature authorized for one session's
// composer at another session's PTY.
func TestR6ComposerSend_BodySessionMustMatchTheSignedSession(t *testing.T) {
	b := newR6ComposerBackend()
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	body := &ComposerSendReq{Session: rep.EndpointID + "/sessB", Text: "hi"}
	f := r6ComposerFrame(rep, "devA:01JMISMATCH", body)
	f.SessionID = rep.EndpointID + "/sessA" // the signed coordinate names a different session
	rc.writeControl(f)
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("session mismatch = op %q code %q, want error/invalid_field", got.Op, got.ErrorCode)
	}
	if n := len(b.sendCalls()); n != 0 {
		t.Errorf("seam saw %d sends for a mismatched frame, want 0", n)
	}
}

// TestR6ComposerSend_EmptyTextIsRefusedInvalidField: there is nothing to inject, so
// forwarding it would spend the shim-wide input transaction on a no-op frame. Refused
// structurally, before the seam.
func TestR6ComposerSend_EmptyTextIsRefusedInvalidField(t *testing.T) {
	b := newR6ComposerBackend()
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r6ComposerFrame(rep, "devA:01JEMPTY", &ComposerSendReq{Session: rep.EndpointID + "/sess1"}))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("empty text = op %q code %q, want error/invalid_field", got.Op, got.ErrorCode)
	}
	if n := len(b.sendCalls()); n != 0 {
		t.Errorf("seam saw %d sends for an empty text, want 0", n)
	}
}

// TestR6ComposerSend_TextPastTheInputPathBoundIsRefusedNotTruncated reuses send_input's
// own ceiling (maxSendInputText): the composer rides the same PTY write path, and a
// silent truncation would submit a DIFFERENT message than the one the user signed.
func TestR6ComposerSend_TextPastTheInputPathBoundIsRefusedNotTruncated(t *testing.T) {
	b := newR6ComposerBackend()
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	long := strings.Repeat("a", maxSendInputText+1)
	rc.writeControl(r6ComposerFrame(rep, "devA:01JLONG", &ComposerSendReq{Session: rep.EndpointID + "/sess1", Text: long}))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("oversized text = op %q code %q, want error/invalid_field", got.Op, got.ErrorCode)
	}
	if n := len(b.sendCalls()); n != 0 {
		t.Errorf("seam saw %d sends for an oversized text, want 0: a clipped send submits a "+
			"different message than the one the signature covered", n)
	}
}

// TestR6ComposerSend_ForgedSignatureNeverReachesTheSeam: device-signature validation runs
// BEFORE the seam, at the same choke point every mutating op uses.
func TestR6ComposerSend_ForgedSignatureNeverReachesTheSeam(t *testing.T) {
	b := newR6ComposerBackend()
	b.authzFn = func(DeviceCommandAuth) error { return errForged }
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r6ComposerFrame(rep, "devA:01JFORGED", &ComposerSendReq{Session: rep.EndpointID + "/sess1", Text: "x"}))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
		t.Fatalf("forged composer_send = op %q code %q, want error/not_authorized", got.Op, got.ErrorCode)
	}
	if n := len(b.sendCalls()); n != 0 {
		t.Errorf("seam saw %d sends from a forged frame, want 0", n)
	}
}

// TestR6ComposerSend_ADaemonStaleTurnSurfacesVerbatim is the render-vs-tap race's wire
// half (IS-LIFE-5: "a tap that lands after the turn moved on is refused, never
// misapplied"): the seam's CodeStaleTurn crosses back to the phone as its own code, with
// text a human can be shown.
func TestR6ComposerSend_ADaemonStaleTurnSurfacesVerbatim(t *testing.T) {
	b := newR6ComposerBackend()
	b.sendCode = CodeStaleTurn
	b.sendErr = errStaleTurnForTest
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r6ComposerFrame(rep, "devA:01JSTALE", &ComposerSendReq{
		Session: rep.EndpointID + "/sess1", ExpectedTurn: "01JOLDTURN", Text: "too late",
	}))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeStaleTurn {
		t.Fatalf("stale send = op %q code %q, want error/stale_turn", got.Op, got.ErrorCode)
	}
	if got.Error == "" {
		t.Error("stale_turn refusal carries no text; the phone has nothing to render gently")
	}
}

// TestR6ComposerSend_ABackendWithoutTheSeamRefusesRatherThanPretending mirrors
// handleApprove's rule: replying OK to a send nothing injected shows the user a sent
// message the agent never received.
func TestR6ComposerSend_ABackendWithoutTheSeamRefusesRatherThanPretending(t *testing.T) {
	stub := newStubDaemon() // implements no ComposerSender
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	body := &ComposerSendReq{Session: rep.EndpointID + "/sess1", Text: "hello"}
	rc.writeControl(r6ComposerFrame(rep, "devA:01JNOSEAM", body))
	got := rc.readControl()
	if got.Op != OpError {
		t.Fatalf("seamless backend answered op %q, want an error: OK here is a sent message "+
			"the agent never received", got.Op)
	}
}
