package skeleton

// ADVERSARIAL confirmation test for slice A5-d, attack 14 (a forged/altered target). This
// is NOT a new gate: it pins that the take_control device signature BINDS the target
// session, a property already landed by A5-a/c (requireRemoteAuthz verifies the signature
// against the WIRE SessionID, and crypto.Command.Canonical signs Session as field 3). It
// must PASS against current code, closing out the take_control adversarial acceptance bar.
//
// The attack: a device legitimately signs a take_control for one session (sessionA), and a
// hostile relay rewrites ONLY the wire Control.SessionID to a different, real running
// session (sessionB) it wants to hijack — keeping the authentic signature, operation_id,
// and a valid one-shot gate token. handleTakeControl passes the WIRE SessionID into
// requireRemoteAuthz, so the daemon recomputes the canonical tuple with Session = sessionB
// and the device's Ed25519 signature (computed over Session = sessionA) FAILS to verify.
// take_control is refused not_authorized and NO lease is established. A relay cannot
// redirect a valid command to a session the device never authorized.
//
// Runs against the REAL assembled daemon over remote.sock with a REAL device keystore +
// registry + phonecore.SignCommand, reusing signedTakeControl/sendTakeControl from
// takecontrol_gatetoken_test.go (only the target differs from the valid path, so any
// refusal is attributable to the session mismatch alone).

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// TestSkeleton_TakeControlForgedTargetRefused: a CapFull device signs a take_control (with
// a valid gate token) binding Session = sessionA, but the wire carries SessionID = sessionB
// (a real running session). The daemon verifies the signature against the WIRE session, so
// it fails — take_control is refused not_authorized with NO lease. The signature binds the
// target; a swapped SessionID cannot re-aim an authentic command.
func TestSkeleton_TakeControlForgedTargetRefused(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull)
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")

	// sessionB is the REAL running session the relay wants to hijack (put on the wire).
	sessionB := protocol.NamespacedID(sk.api.endpointID, meta.ID)
	// sessionA is a DIFFERENT, well-formed session the device actually signed over.
	sessionA := protocol.NamespacedID(sk.api.endpointID, "sessSIGNED")
	if sessionA == sessionB {
		t.Fatalf("test setup: signed session must differ from the wire target")
	}

	rc := dialRemote(t, rsock, protocol.CapRemoteGateway)
	const gateToken = "gate-oneshot-forgedtarget"
	// Sign a fully-valid take_control for sessionA (valid gate token bound as content hash),
	// then swap ONLY the wire SessionID to sessionB — everything else is authentic.
	cmd := signedTakeControl(t, ks, sk.api.endpointID, sessionA, "devTC:01JFORGE000000000000000", gateToken, time.Now().Add(time.Minute))
	sendTakeControl(rc, rc.endpointID, sessionB, cmd, gateToken)

	got := rc.read(10 * time.Second)
	if got.Op != protocol.OpError || got.ErrorCode != protocol.CodeNotAuthorized {
		t.Fatalf("forged-target take_control = op %q code %q; want error/not_authorized. "+
			"handleTakeControl passes the WIRE SessionID into requireRemoteAuthz and "+
			"crypto.Command.Canonical signs Session, so a signature over sessionA must fail to "+
			"verify when sessionB is on the wire. An OpLease here means the target was NOT bound "+
			"into the signature.", got.Op, got.ErrorCode)
	}
}
