package main

// Failing-first tests for the gateway binary's config assembler (slice G1):
// resolveGatewayParams is a PURE function that reads the provisioned state
// (machine identity, relay.json, the paired-device registry) and returns
// everything remotegw.Service needs EXCEPT the dialed relay Mailbox -- that
// dial happens in slice G2, not here. resolveGatewayParams and gatewayParams
// do not exist yet; this file is intentionally RED (compile-fail) until GREEN
// adds them.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// writeMachineIdentity provisions <stateDir>/remote/machine.key exactly as
// `swarm remote init` does (cmd/swarm/remote.go runRemoteInit).
func writeMachineIdentity(t *testing.T, stateDir string) *machineid.Identity {
	t.Helper()
	id, err := machineid.Generate("test-host")
	if err != nil {
		t.Fatalf("machineid.Generate: %v", err)
	}
	remoteDir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("mkdir remote dir: %v", err)
	}
	if err := id.Save(filepath.Join(remoteDir, "machine.key")); err != nil {
		t.Fatalf("id.Save: %v", err)
	}
	return id
}

// writeRelayURL provisions <stateDir>/remote/relay.json exactly as
// `swarm remote init --relay-url` does (cmd/swarm/remote.go runRemoteInit),
// matching the shape internal/skeleton/pairing_config.go's loadRelayURL reads.
func writeRelayURL(t *testing.T, stateDir, url string) {
	t.Helper()
	remoteDir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("mkdir remote dir: %v", err)
	}
	b, err := json.Marshal(map[string]string{"relay_url": url})
	if err != nil {
		t.Fatalf("marshal relay.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remoteDir, "relay.json"), b, 0o600); err != nil {
		t.Fatalf("write relay.json: %v", err)
	}
}

// randBytes returns n cryptographically random bytes, failing the test on error.
func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return b
}

// addPairedDevice registers exactly one paired device in the registry at
// <stateDir>/devices (internal/remote/device.Open's dir, per
// internal/skeleton/serve.go's device.Open(filepath.Join(cfg.StateDir, "devices"))).
// It returns the persisted Record so the test can pin the resolver's output
// against it.
func addPairedDevice(t *testing.T, stateDir string) device.Record {
	t.Helper()
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatalf("device.Open: %v", err)
	}
	cmdPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	// A CANONICAL, well-behaved phone: its self-reported RoutingID is exactly the raw
	// bytes of relay.RoutingID(its relay-auth pub) -- the same derivation machineid uses
	// for the machine's own routing id. So hex(RoutingID) == relay.RoutingID(RelayAuthPub),
	// and PhoneTarget resolves identically whether derived from the pub or the stored bytes.
	relayAuthPub := randBytes(t, 32)
	routingID, err := hex.DecodeString(relay.RoutingID(relayAuthPub))
	if err != nil {
		t.Fatalf("decode canonical routing id: %v", err)
	}
	rec := device.Record{
		DeviceID:       device.DeviceIDFor(cmdPub),
		Name:           "phone",
		NoiseStaticPub: randBytes(t, 32),
		RelayAuthPub:   relayAuthPub,
		CommandSignPub: cmdPub,
		RecipientPub:   randBytes(t, 32),
		RoutingID:      routingID,
		Capability:     device.CapFull,
		PairedAt:       time.Now(),
		GrantedEpoch:   1,
	}
	if err := reg.Add(rec); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}
	return rec
}

// TestResolveGatewayParams_Populated pins the full contract: every field the
// resolver returns must trace back to the provisioned state on disk.
func TestResolveGatewayParams_Populated(t *testing.T) {
	stateDir := t.TempDir()
	id := writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	rec := addPairedDevice(t, stateDir)

	const daemonSocket = "/tmp/does-not-need-to-exist/remote.sock"
	got, err := resolveGatewayParams(stateDir, daemonSocket)
	if err != nil {
		t.Fatalf("resolveGatewayParams: %v", err)
	}

	if got.DaemonSocket != daemonSocket {
		t.Errorf("DaemonSocket = %q, want %q", got.DaemonSocket, daemonSocket)
	}
	if got.RelayURL != "ws://127.0.0.1:9999" {
		t.Errorf("RelayURL = %q, want %q", got.RelayURL, "ws://127.0.0.1:9999")
	}

	// PhoneTarget is the relay routing STRING the relay keys the phone's mailbox by:
	// relay.RoutingID(the device's relay-auth pub) -- the SAME deriver the relay
	// (client.go: RoutingID(auth.RelayAuthPub)) and machineid use. For this canonical
	// fixture that equals hex(rec.RoutingID), so the happy-path value is unchanged; the
	// derivation itself (from the pub, not the self-reported routing bytes) is pinned by
	// TestResolveGatewayParams_PhoneTargetDerivedFromRelayAuth.
	wantTarget := hex.EncodeToString(rec.RoutingID)
	if got.PhoneTarget != wantTarget {
		t.Errorf("PhoneTarget = %q, want %q", got.PhoneTarget, wantTarget)
	}

	if got.Key != id.EpochKeys().ContentKey {
		t.Errorf("Key = %x, want %x", got.Key, id.EpochKeys().ContentKey)
	}
	if got.EpochID != id.EpochID() {
		t.Errorf("EpochID = %d, want %d", got.EpochID, id.EpochID())
	}

	wantRecipientKeyID := crypto.KeyID(rec.RecipientPub)
	if got.RecipientKeyID != wantRecipientKeyID {
		t.Errorf("RecipientKeyID = %x, want %x", got.RecipientKeyID, wantRecipientKeyID)
	}
	wantSenderKeyID := crypto.KeyID(id.RecipientPublic())
	if got.SenderKeyID != wantSenderKeyID {
		t.Errorf("SenderKeyID = %x, want %x", got.SenderKeyID, wantSenderKeyID)
	}

	// RelayAuth must be usable: its Sign closure must produce a signature that
	// verifies under its own RelayAuthPub, and that pub must be the machine
	// identity's relay-auth public key (relay.ClientAuth's shape, per
	// machineid.go's own doc comment on RelayAuthSign).
	if !ed25519PubEqual(got.RelayAuth.RelayAuthPub, id.RelayAuthPublic()) {
		t.Errorf("RelayAuth.RelayAuthPub = %x, want %x", got.RelayAuth.RelayAuthPub, id.RelayAuthPublic())
	}
	challenge := []byte("resolver-test-challenge")
	sig := got.RelayAuth.Sign(challenge)
	if !ed25519.Verify(got.RelayAuth.RelayAuthPub, challenge, sig) {
		t.Errorf("RelayAuth.Sign produced a signature that does not verify under RelayAuth.RelayAuthPub")
	}
}

// TestResolveGatewayParams_PhoneTargetDerivedFromRelayAuth (re-audit finding C5) pins that
// PhoneTarget is DERIVED from the paired device's relay-auth pub via relay.RoutingID -- the
// same function the relay keys the phone's mailbox by -- NOT the phone's self-reported,
// unverifiable Record.RoutingID. A phone that supplied a non-canonical routing id must not be
// able to make the gateway misroute (or silently drop) its bootstrap grant.
func TestResolveGatewayParams_PhoneTargetDerivedFromRelayAuth(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")

	// A NON-canonical phone: its self-reported RoutingID is independent random bytes, NOT
	// the raw HKDF of its relay-auth pub. hex(RoutingID) therefore differs from the routing
	// id the relay actually keys its mailbox by.
	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		t.Fatalf("device.Open: %v", err)
	}
	cmdPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	rec := device.Record{
		DeviceID:       device.DeviceIDFor(cmdPub),
		Name:           "rogue-phone",
		NoiseStaticPub: randBytes(t, 32),
		RelayAuthPub:   randBytes(t, 32),
		CommandSignPub: cmdPub,
		RecipientPub:   randBytes(t, 32),
		RoutingID:      randBytes(t, 16), // NON-canonical: unrelated to RelayAuthPub
		Capability:     device.CapFull,
		PairedAt:       time.Now(),
		GrantedEpoch:   1,
	}
	if err := reg.Add(rec); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}

	got, err := resolveGatewayParams(stateDir, "/tmp/remote.sock")
	if err != nil {
		t.Fatalf("resolveGatewayParams: %v", err)
	}

	want := relay.RoutingID(rec.RelayAuthPub)
	if got.PhoneTarget != want {
		t.Errorf("PhoneTarget = %q, want %q (relay.RoutingID of the device relay-auth pub)", got.PhoneTarget, want)
	}
	// And it must NOT be the self-reported routing id -- that is the exact misroute C5 fixes.
	if got.PhoneTarget == hex.EncodeToString(rec.RoutingID) {
		t.Errorf("PhoneTarget = %q equals the phone's self-reported hex(RoutingID); want the relay-derived id", got.PhoneTarget)
	}
}

func ed25519PubEqual(a, b ed25519.PublicKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestResolveGatewayParams_MissingIdentityFailsClosed: no machine.key at all
// (not even the remote/ dir) must fail closed, never return a zero-value params.
func TestResolveGatewayParams_MissingIdentityFailsClosed(t *testing.T) {
	stateDir := t.TempDir()
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	addPairedDevice(t, stateDir)

	if _, err := resolveGatewayParams(stateDir, "/tmp/remote.sock"); err == nil {
		t.Fatal("resolveGatewayParams: want error for missing machine identity, got nil")
	}
}

// TestResolveGatewayParams_NoPairedDeviceFailsClosed: identity and relay.json
// are both provisioned, but the device registry has zero paired devices.
func TestResolveGatewayParams_NoPairedDeviceFailsClosed(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	writeRelayURL(t, stateDir, "ws://127.0.0.1:9999")
	// No device registered: the registry dir is intentionally left absent, so
	// resolveGatewayParams itself must open/create it and observe zero devices.

	if _, err := resolveGatewayParams(stateDir, "/tmp/remote.sock"); err == nil {
		t.Fatal("resolveGatewayParams: want error for zero paired devices, got nil")
	}
}

// TestResolveGatewayParams_MissingRelayURLFailsClosed: identity and a paired
// device are both provisioned, but relay.json was never written (`swarm remote
// init` ran without --relay-url).
func TestResolveGatewayParams_MissingRelayURLFailsClosed(t *testing.T) {
	stateDir := t.TempDir()
	writeMachineIdentity(t, stateDir)
	addPairedDevice(t, stateDir)
	// No relay.json written.

	if _, err := resolveGatewayParams(stateDir, "/tmp/remote.sock"); err == nil {
		t.Fatal("resolveGatewayParams: want error for missing relay.json, got nil")
	}
}
