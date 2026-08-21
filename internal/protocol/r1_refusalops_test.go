package protocol

// FAILING-FIRST (TDD RED, GG-5) for the Wave R1 "refusal-ops" slice (playbook §6.3, ADR-017
// T5, ADR-007 B144): the daemon-side dispatch for the new R1 semantic-op vocabulary --
// session_launch, composer_send, operation_status, turn_interrupt, terminal_control_begin,
// terminal_control_end -- landing as REFUSAL-ONLY named actions ahead of their real business
// logic (playbook R1 deliverable: "Add session_launch, composer_send, operation_status,
// turn_interrupt, terminal-view/control, and push-pairing shapes with refusal-only daemon
// handlers first").
//
// THE CONTRACT these tests freeze (undefined symbols -> compile-fail RED is expected and
// valid per this wave's HARD RULES):
//
//   - Six new Action constants (ActionSessionLaunch .. ActionTerminalControlEnd), each MAPPED
//     in internal/skeleton's actionClass switch (see internal/skeleton's companion RED file)
//     rather than left fail-closed -- a name this build KNOWS but has not yet built a real
//     handler for is a different fact than a name it has never heard of, and the two must stay
//     distinguishable to a phone one release ahead of its daemon (nx444/M0.3's whole point).
//   - Six new Op constants (OpSessionLaunch .. OpTerminalControlEnd), wired into
//     handleControl's switch behind a SHARED refusal-only handler that:
//     1. calls requireRemoteAuthz FIRST (the SAME choke point kill/delete/launch/approve
//     already ride -- device signature + capability, before any op-specific reply);
//     2. only on success, checks the body version the phone bound this op to; and
//     3. only once both hold, answers the sealed, stable op_not_implemented refusal --
//     because the real handler does not exist yet.
//   - Control.BodyVersion int: the per-op body-version tag playbook §6.3 requires ("Every
//     durable semantic mutation binds the selected profile ... signs a canonical hash of its
//     full body"), paired with RemoteProfileV1's existing accepted_body_versions. For the R1
//     companion set there is exactly one accepted value, schema.CurrentProfileVersion
//     (profile.go's own doc: "the REST OF THE COMPANION R1 SET SHARE" it) -- CONSUMED here
//     rather than adding a second, unused source of truth beside it. Zero (unset) is refused
//     identically to a wrong version: there is no body version 0.
//   - CodeNotImplemented ErrorCode = "op_not_implemented": a SEALED, STABLE code distinct
//     from CodeNotAuthorized (authz failure), CodeInvalidField (structural/version failure),
//     and the bare "unknown op" default arm's un-coded refusal (which stays generic and
//     UNCHANGED -- see internal/remotegw's companion file for the extended unknown-action
//     coverage on the gateway's own generic-refusal arm).
//
// WHAT THIS FILE DELIBERATELY DOES NOT DO:
//
//   - It gives none of the six a real body (composer_send's text, session_launch's preset_id,
//     ...) or binds one into the signature via ContentHash. Those shapes are IS-LIFE-5's own
//     amendment obligation (ADR-017 T5) and land with each op's GREEN slice; this skeleton
//     carries only the one field every one of the six needs to refuse a version mismatch
//     honestly. BodyVersion is therefore NOT YET content-hash bound -- a compromised gateway
//     could alter it in flight -- and that gap is inherited by, not introduced by, this slice:
//     it closes when each op's real body (and its ContentHash) lands.
//   - "push-pairing shapes" (the playbook's seventh named item) is OUT OF SCOPE here. ADR-015
//     P7 places the push-binding transfer (push_address, submit_capability,
//     machine_revoke_capability, wake_key) INSIDE the pairing exchange transcript -- before a
//     device is registered and before any command-signing key exists to sign a RemoteCommand
//     with at all. It is architecturally the same family as pair_start/pair_confirm (owner-
//     tier, PairFailure-shaped), not kill/launch/approve, and forcing it through THIS slice's
//     actionClass/requireRemoteAuthz mechanism would be wrong, not merely early. It is left
//     for a follow-up RED slice scoped to ADR-015's own pairing-transcript wire shape.
//   - terminal_input and terminal_control_keepalive are DELIBERATELY EXCLUDED from this
//     vocabulary (ADR-017 T6: "not individually signed ... ride only the E2EE frame's
//     authenticated sender/sequence and that device-bound confirmed generation", the SAME
//     exception the existing lease input frame already takes). internal/remotegw and
//     internal/skeleton's companion RED files pin that they stay unmapped.

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// r1Op names one new semantic op under test: its wire Op string, its signed Action string,
// whether it binds an existing session instance (composer_send / turn_interrupt /
// terminal_control_begin / terminal_control_end) or none (session_launch creates one;
// operation_status names none), and the code an AUTHORIZED, correctly-versioned frame of
// this shape now answers.
//
// SUPERSESSION (Wave R5, pre-recorded in docs/verification/r5-red/go-red.txt §3): the R5
// GREEN slice IS the real handler for session_launch and operation_status, exactly as this
// file's header anticipated. For those two ops -- and only those -- wantCode is retargeted
// away from op_not_implemented to what the REAL handler answers this same frame (which
// carries no session_launch body and no subject_operation_id): the structural
// invalid_field refusal, refused AFTER the same authz + body-version gates. Every
// choke-point ordering assertion (authz first, tuple count, sentinel pinning, the
// missing-fields and body-version gates) is inherited unchanged and still asserted below.
type r1Op struct {
	name     string
	op       string
	action   string
	session  bool
	wantCode ErrorCode
}

func r1Ops() []r1Op {
	return []r1Op{
		// Implemented by Wave R5: the frame below (bodyless, subject-less) is refused
		// invalid_field by the REAL handler once authorized and version-checked.
		{"session_launch", OpSessionLaunch, ActionSessionLaunch, false, CodeInvalidField},
		// SUPERSESSION EXECUTED (Wave R6, pre-recorded in docs/verification/r6-red/
		// chat-red.txt and in r6_composersend_test.go's header): this row read
		//   {"composer_send", OpComposerSend, ActionComposerSend, true, CodeNotImplemented}
		// while composer_send had only the refusal-only handler. The REAL handler
		// (remote_chat.go handleComposerSend) refuses this same bodyless frame as
		// STRUCTURAL -- CodeInvalidField, after the same authz + body-version gates --
		// exactly as R5 retargeted session_launch. Every choke-point ordering assertion
		// below is inherited unchanged.
		{"composer_send", OpComposerSend, ActionComposerSend, true, CodeInvalidField},
		{"operation_status", OpOperationStatus, ActionOperationStatus, false, CodeInvalidField},
		// SUPERSESSION EXECUTED TWICE, and the second one changes the VALUE.
		//
		// R6 (first): this row read
		//   {"turn_interrupt", OpTurnInterrupt, ActionTurnInterrupt, true, CodeNotImplemented}
		// for the refusal-only handler, and the R6 slice kept the value on the reasoning
		// that "turn_interrupt's frame below IS well-formed (the op has no body), so the
		// retarget is the SEAM'S answer for this stub backend".
		//
		// R6 FIX-PACK (second, finding B7; recorded in docs/verification/r6-red/
		// chat-red.txt): the premise "the op has no body" is gone. A probe showed a Stop
		// rendered against turnA typing the cancel sequence into turnB -- in playbook §8.1,
		// the turn the OWNER just started at the terminal, whose half-typed line the cancel
		// key clears. turn_interrupt now carries (session, expected_turn), so the BODYLESS
		// frame this table sends is no longer well-formed and is refused STRUCTURALLY,
		// before the seam -- CodeInvalidField, after the same authz + body-version gates,
		// which is exactly what composer_send's row two lines up asserts and why. Every
		// choke-point ordering assertion below is inherited unchanged; the seam-absent
		// CodeNotImplemented answer this row used to measure is still measured, by
		// r6_turninterrupt_test.go's
		// TestR6TurnInterrupt_ABackendWithoutTheSeamRefusesRatherThanPretending, which
		// sends a WELL-FORMED frame and therefore reaches the seam.
		{"turn_interrupt", OpTurnInterrupt, ActionTurnInterrupt, true, CodeInvalidField},
		// SUPERSESSION EXECUTED (Wave R8, pre-recorded in docs/verification/r8-red/
		// control-red.txt and in r8_terminalcontrol_test.go's
		// TestR8Control_BeginIsNoLongerARefusalStub): these two rows read
		//   {"terminal_control_begin", ..., CodeNotImplemented}
		//   {"terminal_control_end",   ..., CodeNotImplemented}
		// while ADR-017 T6's ops had only the refusal-only handler. The REAL handlers
		// (remote_terminal.go) refuse this same BODYLESS, generation-less frame after the
		// same authz + body-version gates, exactly as R5 retargeted session_launch and R6
		// retargeted composer_send and turn_interrupt:
		//   - begin is refused STRUCTURALLY (no terminal_control_begin body at all);
		//   - end is refused STALE (this connection holds no generation to end) -- an OK
		//     there would tell a phone its control was released when there was nothing to
		//     release, which is what would make the banner's disappearance a lie.
		// Every choke-point ordering assertion below is inherited unchanged.
		{"terminal_control_begin", OpTerminalControlBegin, ActionTerminalControlBegin, true, CodeInvalidField},
		{"terminal_control_end", OpTerminalControlEnd, ActionTerminalControlEnd, true, CodeStaleGeneration},
	}
}

// TestR1RefusalOps_AuthorizedCommandGetsStableNotImplementedRefusal is the positive case for
// all six: a fully authorized, correctly body-versioned command reaches the refusal-only
// handler and gets its OWN stable code, having actually run through device authorization.
func TestR1RefusalOps_AuthorizedCommandGetsStableNotImplementedRefusal(t *testing.T) {
	for _, o := range r1Ops() {
		t.Run(o.name, func(t *testing.T) {
			stub := newStubDaemon()
			sock := serveRemote(t, stub)
			rc := rawDial(t, sock)
			rep := rc.hello(Version, []string{CapRemoteGateway})
			var sid string
			if o.session {
				sid = rep.EndpointID + "/sess1"
			}
			exp := time.Now().Add(time.Minute)
			rc.writeControl(Control{
				Op: o.op, EndpointID: rep.EndpointID, SessionID: sid,
				OperationID: "devA:01J" + o.name, DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
				BodyVersion: schema.CurrentProfileVersion,
			})
			got := rc.readControl()
			if got.Op != OpError || got.ErrorCode != o.wantCode {
				t.Fatalf("%s = op %q code %q, want error/%s: a name this build recognises must "+
					"answer its OWN stable code -- never a silent hang, and never the generic "+
					"unknown-op refusal", o.name, got.Op, got.ErrorCode, o.wantCode)
			}
			if !strings.Contains(got.Error, o.name) {
				t.Errorf("%s refusal text = %q, want it to name the op", o.name, got.Error)
			}
			tuples := stub.authorizedTuples()
			if len(tuples) != 1 {
				t.Fatalf("%s: authenticator saw %d tuples, want 1: the refusal-only handler must "+
					"still run through requireRemoteAuthz like every other mutating op", o.name, len(tuples))
			}
			if tuples[0].Action != o.action {
				t.Errorf("%s: authenticator tuple Action = %q, want %q", o.name, tuples[0].Action, o.action)
			}
		})
	}
}

// TestR1RefusalOps_ForgedSignatureNeverReachesTheRefusalOnlyHandler is the negative control
// HARD RULES calls out by name: device-signature validation must run BEFORE any refusal
// dispatch, so a forged frame is stopped at the SAME choke point every mutating op uses and
// never reaches the op_not_implemented handler behind it.
func TestR1RefusalOps_ForgedSignatureNeverReachesTheRefusalOnlyHandler(t *testing.T) {
	for _, o := range r1Ops() {
		t.Run(o.name, func(t *testing.T) {
			stub := newStubDaemon()
			stub.authzFn = func(DeviceCommandAuth) error { return errForged }
			sock := serveRemote(t, stub)
			rc := rawDial(t, sock)
			rep := rc.hello(Version, []string{CapRemoteGateway})
			var sid string
			if o.session {
				sid = rep.EndpointID + "/sess1"
			}
			exp := time.Now().Add(time.Minute)
			rc.writeControl(Control{
				Op: o.op, EndpointID: rep.EndpointID, SessionID: sid,
				OperationID: "devA:01Jforged" + o.name, DeviceID: "devA", DeviceSig: "forged", ExpiresAt: &exp,
				BodyVersion: schema.CurrentProfileVersion,
			})
			got := rc.readControl()
			if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
				t.Fatalf("%s forged signature = op %q code %q, want error/not_authorized: a forged "+
					"frame reaching op_not_implemented would mean this build ANSWERED a signature it "+
					"never verified", o.name, got.Op, got.ErrorCode)
			}
		})
	}
}

// TestR1RefusalOps_MissingDeviceFieldsIsInvalidField: the existing structural gate
// (requireRemoteAuthz's device_id/device_sig/expires_at check) applies unchanged -- reused,
// not reinvented, for the new ops.
func TestR1RefusalOps_MissingDeviceFieldsIsInvalidField(t *testing.T) {
	for _, o := range r1Ops() {
		t.Run(o.name, func(t *testing.T) {
			stub := newStubDaemon()
			sock := serveRemote(t, stub)
			rc := rawDial(t, sock)
			rep := rc.hello(Version, []string{CapRemoteGateway})
			rc.writeControl(Control{Op: o.op, EndpointID: rep.EndpointID, OperationID: "devA:01Jnodev" + o.name})
			got := rc.readControl()
			if got.Op != OpError || got.ErrorCode != CodeInvalidField {
				t.Fatalf("%s missing device fields = op %q code %q, want error/invalid_field", o.name, got.Op, got.ErrorCode)
			}
		})
	}
}

// TestR1RefusalOps_WrongBodyVersionNamesTheAcceptedOne consumes schema.CurrentProfileVersion:
// a wrong body version is refused BY NAME, not merely "wrong version", and is distinguishable
// from op_not_implemented -- the two refusals are answers to different questions.
func TestR1RefusalOps_WrongBodyVersionNamesTheAcceptedOne(t *testing.T) {
	for _, o := range r1Ops() {
		t.Run(o.name, func(t *testing.T) {
			stub := newStubDaemon()
			sock := serveRemote(t, stub)
			rc := rawDial(t, sock)
			rep := rc.hello(Version, []string{CapRemoteGateway})
			var sid string
			if o.session {
				sid = rep.EndpointID + "/sess1"
			}
			exp := time.Now().Add(time.Minute)
			rc.writeControl(Control{
				Op: o.op, EndpointID: rep.EndpointID, SessionID: sid,
				OperationID: "devA:01Jbadver" + o.name, DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
				BodyVersion: schema.CurrentProfileVersion + 1,
			})
			got := rc.readControl()
			if got.Op != OpError || got.ErrorCode != CodeInvalidField {
				t.Fatalf("%s wrong body_version = op %q code %q, want error/invalid_field", o.name, got.Op, got.ErrorCode)
			}
			want := strconv.Itoa(schema.CurrentProfileVersion)
			if !strings.Contains(got.Error, want) {
				t.Errorf("%s wrong-version refusal = %q, want it to name the accepted version %q "+
					"from this machine's own RemoteProfileV1 (schema.CurrentProfileVersion)", o.name, got.Error, want)
			}
		})
	}
}

// TestR1RefusalOps_AbsentBodyVersionIsAlsoRefused: the zero value is not a free pass. There
// is no body version 0, so an old/non-compliant caller that never bound one is refused
// exactly like a wrong one, never treated as implicitly "version 1".
func TestR1RefusalOps_AbsentBodyVersionIsAlsoRefused(t *testing.T) {
	o := r1Ops()[0] // session_launch
	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	exp := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: o.op, EndpointID: rep.EndpointID,
		OperationID: "devA:01Jnover", DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
		// BodyVersion left at the zero value deliberately.
	})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("%s absent body_version = op %q code %q, want error/invalid_field", o.name, got.Op, got.ErrorCode)
	}
}

// TestR1RefusalOps_SessionlessOpsPinTheOperationSentinelRegardlessOfWireSessionID covers the
// REAL production wire shape for session_launch/operation_status -- gateway.ForwardCommand
// copies rc.Session verbatim into Control.SessionID, and crypto.Command.Canonical refuses an
// empty Session, so a real gateway-forwarded session-less op always arrives with
// SessionID == OperationSessionSentinel ("@op"), never "". It also proves the sentinel is
// PINNED unconditionally, mirroring handleLaunch's unconditional LaunchSessionSentinel: an
// attacker-shaped wire SessionID naming some OTHER session must not steer the authz subject
// away from the sentinel, because a future per-session capability lookup keyed on that
// subject must never be reachable from a session-less op.
func TestR1RefusalOps_SessionlessOpsPinTheOperationSentinelRegardlessOfWireSessionID(t *testing.T) {
	for _, o := range r1Ops() {
		if o.session {
			continue // only session_launch / operation_status name no session instance
		}
		for _, wireSessionID := range []string{OperationSessionSentinel, "some-other/victim-session"} {
			t.Run(o.name+"/wire="+wireSessionID, func(t *testing.T) {
				stub := newStubDaemon()
				sock := serveRemote(t, stub)
				rc := rawDial(t, sock)
				rep := rc.hello(Version, []string{CapRemoteGateway})
				exp := time.Now().Add(time.Minute)
				rc.writeControl(Control{
					Op: o.op, EndpointID: rep.EndpointID, SessionID: wireSessionID,
					OperationID: "devA:01Jpin" + o.name + wireSessionID, DeviceID: "devA", DeviceSig: "sig", ExpiresAt: &exp,
					BodyVersion: schema.CurrentProfileVersion,
				})
				got := rc.readControl()
				if got.Op != OpError || got.ErrorCode != o.wantCode {
					t.Fatalf("%s wire=%q = op %q code %q, want error/%s", o.name, wireSessionID, got.Op, got.ErrorCode, o.wantCode)
				}
				tuples := stub.authorizedTuples()
				if len(tuples) != 1 {
					t.Fatalf("%s: authenticator saw %d tuples, want 1", o.name, len(tuples))
				}
				if tuples[0].Session != OperationSessionSentinel {
					t.Errorf("%s wire=%q: authz subject Session = %q, want %q -- a session-less op "+
						"must always authorize against OperationSessionSentinel, never a wire-supplied "+
						"value, whatever the wire SessionID carries", o.name, wireSessionID, tuples[0].Session, OperationSessionSentinel)
				}
			})
		}
	}
}
