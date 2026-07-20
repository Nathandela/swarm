package skeleton

// Failing-first (TDD RED) real-crypto tests for R-POL.9b: the daemon-side device
// authenticator that verifies a remote command's Ed25519 signature against the pinned
// registry key AND enforces the R-POL.6 capability tier AND rejects an expired command.
// Unlike the protocol-layer choke-point tests (fake authenticator), these use a REAL
// crypto.KeyStore and a REAL device.Registry, so a forged/replayed/expired signature or
// a capability escalation is genuinely rejected by the cryptography, not a stub.
//
// The contract these pin (green only once authorizeCommand + coreAPI.AuthorizeCommand
// exist):
//   - a full-capability device with a valid, unexpired signature is authorized;
//   - a forged signature (wrong key / tampered bytes) is rejected;
//   - a signature captured for one operation_id, replayed under another, is rejected
//     (the tuple binds operation_id and expiry);
//   - an expired command is rejected even with a valid signature;
//   - a read+approve device may not launch/kill; a read-only device may not approve
//     (capability comes from the registry, NOT the wire -- a compromised gateway cannot
//     escalate a device by editing a capability field, because there is none);
//   - an unknown device and a nil registry both fail closed.

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// authFixture builds a registry holding one device (with the given capability) whose
// command-signing key is a real KeyStore, and returns the registry, the keystore, and
// the device id. A second keystore is returned for forgery tests.
func authFixture(t *testing.T, cap device.Capability) (*device.Registry, crypto.KeyStore, crypto.KeyStore, string) {
	t.Helper()
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileKeyStore: %v", err)
	}
	other, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileKeyStore(other): %v", err)
	}
	reg, err := device.Open(t.TempDir())
	if err != nil {
		t.Fatalf("device.Open: %v", err)
	}
	id := device.DeviceIDFor(ks.CommandSigningPublic())
	rec := device.Record{
		DeviceID:       id,
		Name:           "phone",
		NoiseStaticPub: make([]byte, 32),
		RelayAuthPub:   make([]byte, 32),
		CommandSignPub: ks.CommandSigningPublic(),
		RecipientPub:   make([]byte, 32),
		RoutingID:      []byte{1, 2, 3, 4},
		Capability:     cap,
		PairedAt:       time.Unix(1_700_000_000, 0),
		GrantedEpoch:   1,
	}
	if err := reg.Add(rec); err != nil {
		t.Fatalf("registry Add: %v", err)
	}
	return reg, ks, other, id
}

// signWith builds a DeviceCommandAuth for (action, session, opID, expiry) signed by ks.
func signWith(t *testing.T, ks crypto.KeyStore, deviceID, action, machine, session, opID string, exp time.Time) protocol.DeviceCommandAuth {
	t.Helper()
	msg, err := crypto.Command{
		Action:      action,
		Machine:     machine,
		Session:     session,
		OperationID: opID,
		ExpiresAt:   exp.Unix(),
	}.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	sig := ks.SignCommand(msg)
	return protocol.DeviceCommandAuth{
		DeviceID:    deviceID,
		Action:      action,
		Machine:     machine,
		Session:     session,
		OperationID: opID,
		ExpiresAt:   exp,
		Sig:         base64.StdEncoding.EncodeToString(sig),
	}
}

func TestPolicy_ValidCommandAccepted(t *testing.T) {
	reg, ks, _, id := authFixture(t, device.CapFull)
	now := time.Unix(1_700_000_100, 0)
	cmd := signWith(t, ks, id, protocol.ActionKill, "machine1", "machine1/sess1", "op-1", now.Add(time.Minute))
	if err := authorizeCommand(reg, now, cmd); err != nil {
		t.Fatalf("valid full-capability kill rejected: %v", err)
	}
}

func TestPolicy_ForgedDeviceSignatureRejected(t *testing.T) {
	reg, _, forger, id := authFixture(t, device.CapFull)
	now := time.Unix(1_700_000_100, 0)
	// Signed by a DIFFERENT keystore than the one pinned for id.
	cmd := signWith(t, forger, id, protocol.ActionKill, "machine1", "machine1/sess1", "op-1", now.Add(time.Minute))
	if err := authorizeCommand(reg, now, cmd); err == nil {
		t.Fatalf("forged signature (wrong key) was accepted; want rejection")
	}

	// Tampered signature bytes are also rejected.
	reg2, ks, _, id2 := authFixture(t, device.CapFull)
	good := signWith(t, ks, id2, protocol.ActionKill, "machine1", "machine1/sess1", "op-1", now.Add(time.Minute))
	raw, _ := base64.StdEncoding.DecodeString(good.Sig)
	raw[0] ^= 0xFF
	good.Sig = base64.StdEncoding.EncodeToString(raw)
	if err := authorizeCommand(reg2, now, good); err == nil {
		t.Fatalf("tampered signature was accepted; want rejection")
	}
}

func TestPolicy_ReplayedSignatureRejected(t *testing.T) {
	reg, ks, _, id := authFixture(t, device.CapFull)
	now := time.Unix(1_700_000_100, 0)
	cmd := signWith(t, ks, id, protocol.ActionKill, "machine1", "machine1/sess1", "op-1", now.Add(time.Minute))
	// Replay the SAME signature under a different operation_id: the tuple no longer
	// matches what was signed, so verification must fail.
	cmd.OperationID = "op-2"
	if err := authorizeCommand(reg, now, cmd); err == nil {
		t.Fatalf("signature replayed under a new operation_id was accepted; want rejection")
	}
}

func TestPolicy_ExpiredCommandRejected(t *testing.T) {
	reg, ks, _, id := authFixture(t, device.CapFull)
	signedExp := time.Unix(1_700_000_100, 0)
	cmd := signWith(t, ks, id, protocol.ActionKill, "machine1", "machine1/sess1", "op-1", signedExp)
	// now is AFTER the (validly-signed) expiry.
	now := signedExp.Add(time.Second)
	if err := authorizeCommand(reg, now, cmd); err == nil {
		t.Fatalf("expired command was accepted; want rejection")
	}
}

func TestPolicy_ReadApproveDeviceCannotLaunch(t *testing.T) {
	reg, ks, _, id := authFixture(t, device.CapReadApprove)
	now := time.Unix(1_700_000_100, 0)
	// A validly-signed launch from a read+approve device must still be refused on
	// capability grounds (the signature is genuine; the tier is insufficient).
	launch := signWith(t, ks, id, protocol.ActionLaunch, "machine1", protocol.LaunchSessionSentinel, "op-1", now.Add(time.Minute))
	if err := authorizeCommand(reg, now, launch); err == nil {
		t.Fatalf("read+approve device was allowed to launch; want rejection")
	}
	kill := signWith(t, ks, id, protocol.ActionKill, "machine1", "machine1/sess1", "op-2", now.Add(time.Minute))
	if err := authorizeCommand(reg, now, kill); err == nil {
		t.Fatalf("read+approve device was allowed to kill; want rejection")
	}
	// But it MAY approve (valid sig + sufficient tier).
	approve := signWith(t, ks, id, protocol.ActionApprove, "machine1", "machine1/sess1", "op-3", now.Add(time.Minute))
	if err := authorizeCommand(reg, now, approve); err != nil {
		t.Fatalf("read+approve device was refused approve: %v", err)
	}
}

func TestPolicy_ReadOnlyDeviceCannotApprove(t *testing.T) {
	reg, ks, _, id := authFixture(t, device.CapReadOnly)
	now := time.Unix(1_700_000_100, 0)
	approve := signWith(t, ks, id, protocol.ActionApprove, "machine1", "machine1/sess1", "op-1", now.Add(time.Minute))
	if err := authorizeCommand(reg, now, approve); err == nil {
		t.Fatalf("read-only device was allowed to approve; want rejection")
	}
}

func TestPolicy_UnknownDeviceAndNilRegistryFailClosed(t *testing.T) {
	reg, ks, _, _ := authFixture(t, device.CapFull)
	now := time.Unix(1_700_000_100, 0)
	// A command whose device id is not in the registry.
	cmd := signWith(t, ks, "not-registered", protocol.ActionKill, "machine1", "machine1/sess1", "op-1", now.Add(time.Minute))
	if err := authorizeCommand(reg, now, cmd); err == nil {
		t.Fatalf("unknown device was authorized; want fail-closed rejection")
	}
	// A nil registry authorizes nothing.
	if err := authorizeCommand(nil, now, cmd); err == nil {
		t.Fatalf("nil registry authorized a command; want fail-closed rejection")
	}
}
