package phonecore

// FAILING-FIRST (TDD RED, GG-5) test for A7 input Slice 1: the phone's take_control
// authoring path. take_control is the signed command that establishes a control lease
// (A5), so its authoring must mirror launch exactly: the device signs the canonical
// command tuple with Action=take_control and ContentHash=SHA256(gateToken) — the SAME
// content-hash seam the daemon (handleTakeControl) recomputes from the WIRE gate token,
// so a relay that swaps the one-shot token breaks the signature — and the sealed mailbox
// envelope carries the gate token + requested TTL through the untrusted relay so the
// gateway (OpenRemoteCommand) can reconstruct the take_control Control frame.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestSignTakeControl_BindsGateTokenAndExpiry proves both halves of the authoring path:
// SignTakeControl binds the gate token into the device signature via
// ContentHash=SHA256(gateToken) with Action=take_control and the carried ExpiresAt (the
// signed tuple verifies against the pinned command-signing key), and
// SealTakeControlEnvelope carries the gate token + TTL through the mailbox so the
// gateway's OpenRemoteCommand recovers them.
func TestSignTakeControl_BindsGateTokenAndExpiry(t *testing.T) {
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}

	const gateToken = "gate-oneshot-abc"
	const ttlSeconds = 45
	exp := time.Unix(1_700_000_100, 0).Add(time.Minute)

	cmd, err := SignTakeControl(ks, TakeControlInput{
		Machine:     "machine1",
		Session:     "machine1/sess1",
		OperationID: "op-tc-1",
		ExpiresAt:   exp,
		GateToken:   gateToken,
	})
	if err != nil {
		t.Fatalf("SignTakeControl: %v", err)
	}

	// Action is the canonical take_control string.
	if cmd.Action != protocol.ActionTakeControl {
		t.Fatalf("Action = %q; want %q", cmd.Action, protocol.ActionTakeControl)
	}
	// ContentHash binds the gate token exactly: SHA256(gateToken).
	wantHash := sha256.Sum256([]byte(gateToken))
	if !bytes.Equal(cmd.ContentHash, wantHash[:]) {
		t.Fatalf("ContentHash = %x; want SHA256(gateToken) %x", cmd.ContentHash, wantHash)
	}
	// The requested expiry is carried unchanged.
	if !cmd.ExpiresAt.Equal(exp) {
		t.Fatalf("ExpiresAt = %v; want %v", cmd.ExpiresAt, exp)
	}

	// The signed tuple verifies against the device's pinned command-signing key: the
	// daemon reconstructs the SAME canonical input from the recovered fields.
	msg, err := crypto.Command{
		Action:      cmd.Action,
		Machine:     cmd.Machine,
		Session:     cmd.Session,
		OperationID: cmd.OperationID,
		ExpiresAt:   cmd.ExpiresAt.Unix(),
		ContentHash: cmd.ContentHash,
	}.Canonical()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	sig, err := base64.StdEncoding.DecodeString(cmd.Sig)
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if err := crypto.VerifyCommandSig(ks.CommandSigningPublic(), msg, sig); err != nil {
		t.Fatalf("take_control signature does not verify against the pinned command-signing key: %v", err)
	}

	// The gateway (machine side) opens the sealed envelope and recovers the take_control
	// action, gate token, and requested TTL.
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 3)
	}
	raw, err := SealTakeControlEnvelope(key, 1, 1, cmd, gateToken, ttlSeconds)
	if err != nil {
		t.Fatalf("SealTakeControlEnvelope: %v", err)
	}
	rc, err := remotegw.OpenRemoteCommand(key, raw)
	if err != nil {
		t.Fatalf("OpenRemoteCommand: %v", err)
	}
	if rc.Action != protocol.ActionTakeControl {
		t.Fatalf("recovered Action = %q; want %q", rc.Action, protocol.ActionTakeControl)
	}
	if rc.OperationID != cmd.OperationID {
		t.Fatalf("recovered OperationID = %q; want %q", rc.OperationID, cmd.OperationID)
	}
	if rc.GateToken != gateToken {
		t.Fatalf("recovered GateToken = %q; want %q (the gate token must ride the mailbox to the gateway)", rc.GateToken, gateToken)
	}
	if rc.TTLSeconds != ttlSeconds {
		t.Fatalf("recovered TTLSeconds = %d; want %d (the requested control-session TTL must ride the mailbox)", rc.TTLSeconds, ttlSeconds)
	}
}
