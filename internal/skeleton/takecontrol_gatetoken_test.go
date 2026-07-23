package skeleton

// FAILING-FIRST (TDD RED) end-to-end tests for slice A5-c — binding a single-use,
// "biometric-attested" gate token into the signed take_control command, and refusing a
// REPLAYED take_control (operation_id single-use). A5-a landed take_control as a signed,
// authorized lease establishment and A5-b reopened input behind it; A5-c ADDS (1) a
// per-invocation gate token bound into the device signature via the content-hash
// mechanism, and (2) operation_id single-use so a captured command cannot be replayed to
// re-establish control.
//
// These run against the REAL assembled daemon over its dedicated remote.sock, with a REAL
// device keystore + registry + phonecore.SignCommand — the same E2E pattern as
// command_e2e_test.go. The content-hash binding is ONLY observable with the real
// authenticator: handleTakeControl must compute content_hash = SHA256(GateToken) and pass
// it into requireRemoteAuthz, so the device's Ed25519 signature — which covers the content
// hash (crypto.Command.Canonical, field 6) — FAILS to verify when a relay swaps the wire
// GateToken for a different value than the one signed over. A protocol-layer stub authzFn
// does not verify signatures, so it cannot exhibit this anti-tamper property; the real
// authenticator here can. This mirrors deviceauth_test.go's TestPolicy_LaunchContentMismatchRejected,
// but end-to-end through handleTakeControl (which is what must actually COMPUTE and pass
// the hash), not the authorizer in isolation.
//
// RED status — undefined-only compile failure: protocol.Control has no GateToken field
// yet (the single new wire field A5-c adds). Every test below constructs
// protocol.Control{... GateToken: ...}, so this file fails to compile until GREEN:
//
//   - adds Control.GateToken (string) in internal/protocol/types.go — the take_control
//     one-shot gate token (intended production, NOT a typo);
//   - makes handleTakeControl (internal/protocol/server.go) REQUIRE it non-empty
//     (present-check) AND pass ContentHash = SHA256(GateToken) into
//     requireRemoteAuthz(c, ActionTakeControl, c.SessionID, sha256(GateToken)) — the SAME
//     ContentHash seam handleLaunch uses for LaunchContentHash;
//   - claims the operation_id single-use through the daemon's existing two-phase
//     idempotency store (internal/idempotency): after authz, a Prepare(operationID,
//     "take_control", session) that returns existed=true refuses the op (a replay
//     establishes NO second lease). See the accompanying RED report for the exact seam
//     (a protocol.OperationClaimer optional interface on the DaemonAPI, mirroring
//     DeviceAuthenticator, backed by a new (*daemon.Daemon).ClaimOperation wrapping
//     d.idem.Prepare).
//
// The security assertions are the point: a swapped or absent gate token, and a replayed
// operation_id, must each establish NO controller lease. An OpLease grant is emitted ONLY
// after s.attach opens an upstream stream, so a non-lease (OpError) reply is the faithful
// end-to-end signal that no attach/lease happened.

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// signedTakeControl signs a take_control command binding content_hash = SHA256(signedToken)
// with the device keystore, via the REAL phonecore signer (the phone's authoring side).
// The returned auth is what the phone would present; the caller separately chooses the
// gate token it puts ON THE WIRE, so a swap/replay test can make the two differ.
func signedTakeControl(t *testing.T, ks crypto.KeyStore, machine, session, opID, signedToken string, exp time.Time) protocol.DeviceCommandAuth {
	t.Helper()
	h := sha256.Sum256([]byte(signedToken))
	cmd, err := phonecore.SignCommand(ks, phonecore.CommandInput{
		Action:      protocol.ActionTakeControl,
		Machine:     machine,
		Session:     session,
		OperationID: opID,
		ExpiresAt:   exp,
		ContentHash: h[:], // 32-byte SHA256(gateToken): the daemon must recompute this from the wire GateToken
	})
	if err != nil {
		t.Fatalf("sign take_control: %v", err)
	}
	return cmd
}

// sendTakeControl writes a take_control frame on rc: the signed command's identity fields
// plus the wire gate token (wireGateToken), which a swap/replay test can make differ from
// what the signature was computed over.
func sendTakeControl(rc *rawRemote, ep, session string, cmd protocol.DeviceCommandAuth, wireGateToken string) {
	exp := cmd.ExpiresAt
	rc.write(protocol.Control{
		Op:          protocol.OpTakeControl,
		EndpointID:  ep,
		SessionID:   session,
		OperationID: cmd.OperationID,
		DeviceID:    cmd.DeviceID,
		DeviceSig:   cmd.Sig,
		ExpiresAt:   &exp,
		GateToken:   wireGateToken,
	})
}

// TestSkeleton_TakeControlValidGateTokenEstablishes is the positive path: a CapFull device
// signs a take_control over content_hash = SHA256(gateToken) and presents the SAME gate
// token on the wire; the daemon recomputes SHA256(gateToken), the signature verifies, and
// a controller lease is established (OpLease with a nonzero generation) over a real running
// session. RED: compile-fail until Control.GateToken exists; once it does, this also pins
// that handleTakeControl computes and passes the content hash rather than ignoring the token.
func TestSkeleton_TakeControlValidGateTokenEstablishes(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull) // pairs a device: also turns the durable kill switch ON
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	session := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	rc := dialRemote(t, rsock, protocol.CapRemoteGateway)
	const gateToken = "gate-oneshot-valid"
	cmd := signedTakeControl(t, ks, sk.api.endpointID, session, "devTC:01JVALID000000000000000", gateToken, time.Now().Add(time.Minute))
	sendTakeControl(rc, rc.endpointID, session, cmd, gateToken)

	got := rc.read(10 * time.Second)
	if got.Op != protocol.OpLease {
		t.Fatalf("valid gate-token take_control = op %q code %q; want an OpLease grant (control session established)", got.Op, got.ErrorCode)
	}
	if got.Generation == 0 {
		t.Fatalf("take_control lease generation = 0; want a nonzero lease generation (lease established)")
	}
}

// TestSkeleton_TakeControlSwappedGateTokenRefused is the anti-tamper property: the device
// signs over SHA256(tokenA), but a (hostile) relay swaps the wire GateToken to tokenB. The
// daemon recomputes SHA256(tokenB) and verifies the device signature over the tuple that
// now includes the WRONG content hash — verification FAILS — so take_control is refused
// not_authorized and NO lease is established. A gateway/relay that swaps the one-shot gate
// token breaks the signature; it cannot bind a different token to a valid command.
func TestSkeleton_TakeControlSwappedGateTokenRefused(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull)
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	session := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	rc := dialRemote(t, rsock, protocol.CapRemoteGateway)
	const tokenA = "gate-oneshot-authentic"
	const tokenB = "gate-oneshot-swapped-by-relay"
	// Signed over SHA256(tokenA); the wire carries tokenB.
	cmd := signedTakeControl(t, ks, sk.api.endpointID, session, "devTC:01JSWAP0000000000000000", tokenA, time.Now().Add(time.Minute))
	sendTakeControl(rc, rc.endpointID, session, cmd, tokenB)

	got := rc.read(10 * time.Second)
	if got.Op != protocol.OpError || got.ErrorCode != protocol.CodeNotAuthorized {
		t.Fatalf("swapped-gate-token take_control = op %q code %q; want error/not_authorized "+
			"(the daemon must recompute SHA256(wire GateToken) as the content hash, so a signature over "+
			"SHA256(tokenA) fails to verify when tokenB is on the wire). An OpLease here means the token was "+
			"NOT bound into the signature.", got.Op, got.ErrorCode)
	}
}

// TestSkeleton_TakeControlMissingGateTokenRefused pins the present-check: an EMPTY gate
// token is refused, with NO lease. It is adversarial on purpose — the phone even signs over
// SHA256("") so a daemon that ONLY checked the content hash (binding it without a
// present-check) would wrongly ACCEPT an empty token. handleTakeControl must REQUIRE a
// non-empty GateToken: an absent one-shot token can never gate a control session.
func TestSkeleton_TakeControlMissingGateTokenRefused(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull)
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	session := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	rc := dialRemote(t, rsock, protocol.CapRemoteGateway)
	// Sign over SHA256("") and send an empty wire GateToken: only a present-check refuses this.
	cmd := signedTakeControl(t, ks, sk.api.endpointID, session, "devTC:01JMISS0000000000000000", "", time.Now().Add(time.Minute))
	sendTakeControl(rc, rc.endpointID, session, cmd, "")

	got := rc.read(10 * time.Second)
	if got.Op != protocol.OpError {
		t.Fatalf("missing-gate-token take_control = op %q code %q; want a refusal with NO lease. "+
			"handleTakeControl must REQUIRE a non-empty GateToken (present-check), not merely bind its hash "+
			"(SHA256(\"\") is a valid 32-byte hash, so a hash-only implementation would wrongly establish).", got.Op, got.ErrorCode)
	}
}

// TestSkeleton_TakeControlReplayedOperationIDRefused pins operation_id single-use: one
// fully-valid, device-signed take_control (a captured command) is replayed VERBATIM on a
// second connection, exactly as an untrusted relay could. Only the FIRST may establish a
// lease; the replay (same operation_id) must be refused so exactly ONE control session is
// ever opened. An OpLease is emitted only after s.attach opens an upstream stream, so the
// second reply being a non-lease is the daemon-side "one attach, not two" signal.
func TestSkeleton_TakeControlReplayedOperationIDRefused(t *testing.T) {
	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull)
	meta := launchFake(t, sk, "print HELLO\nidle 60s\n")
	session := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	const gateToken = "gate-oneshot-replayed"
	// A single captured command, replayed byte-for-byte (same operation_id, signature, token).
	cmd := signedTakeControl(t, ks, sk.api.endpointID, session, "devTC:01JREPLAY00000000000000", gateToken, time.Now().Add(time.Minute))

	// First connection: establishes the control session (the one and only attach).
	rc1 := dialRemote(t, rsock, protocol.CapRemoteGateway)
	sendTakeControl(rc1, rc1.endpointID, session, cmd, gateToken)
	got1 := rc1.read(10 * time.Second)
	if got1.Op != protocol.OpLease || got1.Generation == 0 {
		t.Fatalf("first take_control = op %q code %q gen %d; want an OpLease grant (first establishes)", got1.Op, got1.ErrorCode, got1.Generation)
	}

	// Second connection replays the IDENTICAL signed command. Single-use (idempotency
	// Prepare existed -> refuse) must reject it: a replay establishes NO second lease and,
	// on the remote tier, cannot supersede the first controller.
	rc2 := dialRemote(t, rsock, protocol.CapRemoteGateway)
	sendTakeControl(rc2, rc2.endpointID, session, cmd, gateToken)
	got2 := rc2.read(10 * time.Second)
	if got2.Op == protocol.OpLease {
		t.Fatalf("replayed take_control (same operation_id) was granted a SECOND lease (gen %d); want a refusal. "+
			"operation_id must be single-use via the daemon idempotency store (Prepare returns existed -> refuse), "+
			"so only ONE control session is ever established (one attach, not two).", got2.Generation)
	}
	if got2.Op != protocol.OpError {
		t.Fatalf("replayed take_control = op %q code %q; want error (refused replay), not a lease grant", got2.Op, got2.ErrorCode)
	}
}
