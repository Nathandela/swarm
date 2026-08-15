package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the Wave R1 "refusal-ops" slice's authorization half
// (playbook §6.3, ADR-017 T5, ADR-007 B144). Companion to deviceauth_test.go, extending its
// REAL-CRYPTO coverage (a genuine device.Registry + crypto.KeyStore, no fakes) to the six new
// semantic-op actions internal/protocol/r1_refusalops_test.go and internal/remotegw's
// companion file pin at the layers above.
//
// THE DECISION THIS FILE PINS, deliberately, per this wave's HARD RULE ("new actions MUST be
// mapped or refused closed -- decide deliberately which"): all six are MAPPED, not left
// fail-closed. A name this build has never heard of stays unmapped and gets the generic
// unknown-action refusal (nx444/M0.3, extended in internal/remotegw for this exact wave); a
// name this build KNOWS but has not yet built a real handler for is mapped and reaches its
// own stable op_not_implemented refusal one layer up -- which is only reachable once
// actionClass says yes. Five are device.ActionControl (session_launch, composer_send,
// turn_interrupt, terminal_control_begin, terminal_control_end -- each starts, steers, or
// ends something). operation_status is device.ActionRead, on ActionPushPrefs's own
// precedent (deviceauth.go's comment on that case: it "cannot start, stop or type into
// anything"; a control-class mapping would leave a read-only paired phone unable to poll the
// status of its own pending operation).
//
// terminal_input and terminal_control_keepalive are the negative space, pinned here at the
// deepest layer: ADR-017 T6 rules they ride only the E2EE frame's authenticated
// sender/sequence and a device-bound confirmed generation, NEVER a per-frame signature, so
// they must NEVER be added to actionClass -- not even provisionally. This file proves the
// exclusion holds against the REAL cryptography: a perfectly valid signature from a
// full-capability device is still refused, because actionClass has no case for either name to
// reach.
//
// "push-pairing shapes" is OUT OF SCOPE here for the reason internal/protocol's companion
// file records at length: ADR-015 P7 places that exchange inside the pairing transcript,
// before any device-signing key exists, not on actionClass's signed-command path at all.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// r1Action is one new action under test: its canonical string, the session slot its signed
// tuple binds (crypto.Command.Canonical refuses an empty Session -- frozen crypto, so every
// signed action needs SOME non-empty value; session-instance ops use a real session id and
// the two session-less ops use protocol.OperationSessionSentinel, LaunchSessionSentinel's
// sibling), and the capability class this slice deliberately maps it to.
type r1Action struct {
	action  string
	session string
	class   device.Action
}

func r1Actions() []r1Action {
	const boundSession = "machine1/sess1"
	return []r1Action{
		{protocol.ActionSessionLaunch, protocol.OperationSessionSentinel, device.ActionControl},
		{protocol.ActionComposerSend, boundSession, device.ActionControl},
		{protocol.ActionOperationStatus, protocol.OperationSessionSentinel, device.ActionRead},
		{protocol.ActionTurnInterrupt, boundSession, device.ActionControl},
		{protocol.ActionTerminalControlBegin, boundSession, device.ActionControl},
		{protocol.ActionTerminalControlEnd, boundSession, device.ActionControl},
	}
}

// TestPolicy_R1ActionsAcceptedForFullCapabilityDevice: every new action is MAPPED (fail-open
// through actionClass) and, with a valid signature and a full-capability device, authorized --
// reachable, even though its daemon handler answers op_not_implemented one layer up.
func TestPolicy_R1ActionsAcceptedForFullCapabilityDevice(t *testing.T) {
	reg, ks, _, id := authFixture(t, device.CapFull)
	now := time.Unix(1_700_000_100, 0)
	for _, a := range r1Actions() {
		cmd := signWith(t, ks, id, a.action, "machine1", a.session, "op-r1-"+a.action, now.Add(time.Minute))
		if err := authorizeCommand(reg, now, cmd); err != nil {
			t.Errorf("%s: valid full-capability command rejected: %v", a.action, err)
		}
	}
}

// TestPolicy_R1ControlActionsRefusedForReadOnlyDevice: the five control-class actions are
// refused for a read-only device (capability comes from the registry, never the wire).
func TestPolicy_R1ControlActionsRefusedForReadOnlyDevice(t *testing.T) {
	reg, ks, _, id := authFixture(t, device.CapReadOnly)
	now := time.Unix(1_700_000_100, 0)
	for _, a := range r1Actions() {
		if a.class != device.ActionControl {
			continue
		}
		cmd := signWith(t, ks, id, a.action, "machine1", a.session, "op-ro-"+a.action, now.Add(time.Minute))
		if err := authorizeCommand(reg, now, cmd); err == nil {
			t.Errorf("%s: read-only device was authorized; want rejection (control-class action, "+
				"insufficient capability)", a.action)
		}
	}
}

// TestPolicy_OperationStatusIsReadClassLikePushPrefs: unlike the five control-class actions,
// a read-only device MAY authorize operation_status.
func TestPolicy_OperationStatusIsReadClassLikePushPrefs(t *testing.T) {
	reg, ks, _, id := authFixture(t, device.CapReadOnly)
	now := time.Unix(1_700_000_100, 0)
	cmd := signWith(t, ks, id, protocol.ActionOperationStatus, "machine1", protocol.OperationSessionSentinel, "op-status-1", now.Add(time.Minute))
	if err := authorizeCommand(reg, now, cmd); err != nil {
		t.Fatalf("read-only device refused operation_status: %v -- it cannot start, stop or type "+
			"into anything, and a control-class mapping would leave a read-only paired phone unable "+
			"to poll the status of its own pending operation (ActionPushPrefs's precedent)", err)
	}
}

// TestPolicy_R1ActionsForgedSignatureRejected extends the real-crypto forged-signature
// coverage (TestPolicy_ForgedDeviceSignatureRejected) to the new actions: a mapped action is
// authenticated exactly like every existing one, before any refusal it might otherwise
// dispatch to.
func TestPolicy_R1ActionsForgedSignatureRejected(t *testing.T) {
	reg, _, forger, id := authFixture(t, device.CapFull)
	now := time.Unix(1_700_000_100, 0)
	for _, a := range r1Actions() {
		cmd := signWith(t, forger, id, a.action, "machine1", a.session, "op-forged-"+a.action, now.Add(time.Minute))
		if err := authorizeCommand(reg, now, cmd); err == nil {
			t.Errorf("%s: forged signature (wrong key) was accepted; want rejection", a.action)
		}
	}
}

// TestPolicy_TerminalInputAndKeepaliveNeverMappedEvenWithValidSignature: the exclusion holds
// against the real cryptography, not merely against a fake authenticator -- a genuinely valid
// signature from a full-capability device is still refused, because actionClass has no case
// for either name.
func TestPolicy_TerminalInputAndKeepaliveNeverMappedEvenWithValidSignature(t *testing.T) {
	reg, ks, _, id := authFixture(t, device.CapFull)
	now := time.Unix(1_700_000_100, 0)
	for _, action := range []string{"terminal_input", "terminal_control_keepalive"} {
		cmd := signWith(t, ks, id, action, "machine1", "machine1/sess1", "op-ti-"+action, now.Add(time.Minute))
		if err := authorizeCommand(reg, now, cmd); err == nil {
			t.Errorf("%s: accepted by a full-capability device's VALID signature; want rejection -- "+
				"ADR-017 T6 forbids ever mapping this name in actionClass, so even a genuine "+
				"signature must fail closed on the unmapped-action arm, not reach a handler", action)
		}
	}
}
