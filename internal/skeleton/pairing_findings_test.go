package skeleton

// FAILING-FIRST (TDD RED, GG-5) tests for the three Phase-A audit-committee pairing
// findings the daemon side owns (ADR-007 amendment 2026-07-24, decisions C5/C6/C7):
//
//   C7 - BeginPairing must GUARD a nil rendezvous seam (relay not configured) and
//        return a clean error instead of panicking on the nil NewRendezvous call.
//   C6 - single-device v1: BeginPairing must REFUSE when a device is already paired,
//        fail-fast (no rendezvous minted, no handshake spawned), Count unchanged.
//   C5 - (daemon half) BeginPairing must PERSIST res.Grant addressable by device id,
//        so the separate gateway process can deliver it over the relay mailbox.
//
// Reused from sibling test files (same package): assemble (serve_test.go), injectPairing
// / runDeviceLeg / awaitControl / recvDeviceEnd (pairing_integration_test.go), dialRemote
// (remote_journal_test.go).

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// validDeviceRecord builds a well-formed registry record (passes validateRecord) so a
// test can pre-seed the registry without running a real pairing.
func validDeviceRecord(t *testing.T) device.Record {
	t.Helper()
	cmdPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	rnd := func(n int) []byte {
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			t.Fatalf("rand: %v", err)
		}
		return b
	}
	return device.Record{
		DeviceID:       device.DeviceIDFor(cmdPub),
		Name:           "already-paired",
		NoiseStaticPub: rnd(32),
		RelayAuthPub:   rnd(32),
		CommandSignPub: cmdPub,
		RecipientPub:   rnd(32),
		RoutingID:      rnd(16),
		Capability:     device.CapFull,
		PairedAt:       time.Now(),
		GrantedEpoch:   1,
	}
}

// TestBeginPairing_NilRendezvousFailsCleanly (C7): a pairing config whose NewRendezvous
// is nil (relay not configured) must yield a clean error, NOT a nil-func panic.
func TestBeginPairing_NilRendezvousFailsCleanly(t *testing.T) {
	sk := assemble(t)
	// Provision pairing WITHOUT a rendezvous seam (relay.json absent -> NewRendezvous nil).
	sk.api.pairing = &pairingConfig{NewRendezvous: nil}

	confirm := func([]string, string) (bool, error) { return true, nil }
	// If the guard is missing, the unconditional cfg.NewRendezvous(ctx, id) call panics
	// (this test would crash) instead of returning -- that IS the C7 bug.
	_, err := sk.api.BeginPairing(context.Background(),
		protocol.PairStartReq{Capability: "full"}, confirm, func(protocol.PairResult) {})
	if err == nil {
		t.Fatal("BeginPairing with a nil NewRendezvous returned nil error; want a clean 'relay not configured' refusal")
	}
}

// TestBeginPairing_RefusesSecondDevice (C6): with one device already registered,
// BeginPairing must refuse fail-fast -- no rendezvous minted (NewRendezvous NOT called),
// Count stays 1.
func TestBeginPairing_RefusesSecondDevice(t *testing.T) {
	sk := assemble(t)
	deviceEnds := injectPairing(t, sk)

	if err := sk.api.devices.Add(validDeviceRecord(t)); err != nil {
		t.Fatalf("seed one paired device: %v", err)
	}
	if got := sk.api.devices.Count(); got != 1 {
		t.Fatalf("precondition: Count = %d, want 1", got)
	}

	confirm := func([]string, string) (bool, error) { return true, nil }
	_, err := sk.api.BeginPairing(context.Background(),
		protocol.PairStartReq{Capability: "full"}, confirm, func(protocol.PairResult) {})
	if err == nil {
		t.Fatal("BeginPairing was accepted while a device is already paired; want a single-device refusal")
	}

	// Fail-fast: the rendezvous seam must not have been asked for (no handshake spawned).
	select {
	case <-deviceEnds:
		t.Fatal("BeginPairing minted a rendezvous despite the single-device refusal (not fail-fast)")
	case <-time.After(200 * time.Millisecond):
	}
	if got := sk.api.devices.Count(); got != 1 {
		t.Fatalf("Count = %d after a refused second pairing; want 1 (unchanged)", got)
	}
}

// TestPairing_PersistsSealedGrant (C5 daemon half): after a successful wire pairing, the
// sealed grant is persisted addressable by the new device id and round-trips -- it opens
// under the device keystore + the pinned machine sign pub to the epoch keys the config
// granted (proving it equals enroll.Enroll's res.Grant, which is sealed non-deterministically
// so a byte-compare is impossible).
func TestPairing_PersistsSealedGrant(t *testing.T) {
	sk := assemble(t)
	deviceEnds := injectPairing(t, sk)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	rc := dialRemote(t, sk.SocketPath(), protocol.CapPairing)
	rc.write(protocol.Control{Op: protocol.OpPairStart, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Capability: "full"}})

	reply := awaitControl(t, rc, protocol.OpPairStart)
	if reply.Pairing == nil || reply.Pairing.QR == "" {
		t.Fatalf("pair_start reply missing QR: %+v", reply.Pairing)
	}
	qp, err := pairing.DecodeQR(reply.Pairing.QR)
	if err != nil {
		t.Fatalf("pair_start QR undecodable: %v", err)
	}

	dEnd := recvDeviceEnd(t, deviceEnds)
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("device keystore: %v", err)
	}
	devDone := runDeviceLeg(ctx, ks, dEnd, qp)

	pending := awaitControl(t, rc, protocol.OpPairPending)
	if pending.Pairing == nil || len(pending.Pairing.SAS) != 6 {
		t.Fatalf("pair_pending missing the 6-word SAS gate: %+v", pending.Pairing)
	}
	rc.write(protocol.Control{Op: protocol.OpPairConfirm, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Allow: true}})

	res := awaitControl(t, rc, protocol.OpPairResult)
	if res.Pairing == nil || res.Pairing.DeviceID == "" {
		t.Fatalf("pair_result = %+v; want success carrying the new DeviceID", res.Pairing)
	}
	select {
	case r := <-devDone:
		if r.err != nil {
			t.Fatalf("device leg failed: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("device leg never completed")
	}

	// The sealed grant is persisted next to the registry, keyed by the device id.
	registryDir := filepath.Join(sk.api.stateDir, "devices")
	g, err := grant.Load(registryDir, res.Pairing.DeviceID)
	if err != nil {
		t.Fatalf("load persisted grant: %v", err)
	}
	if g == nil {
		t.Fatal("no sealed grant persisted for the paired device (C5 daemon half missing)")
	}

	// Round-trip: it verifies + opens under the device keystore and the machine sign pub
	// the config granted, recovering exactly the config's epoch/seq/keys.
	epochID, seq, keys, err := crypto.OpenEpochGrant(ks, sk.api.pairing.SignPub, g)
	if err != nil {
		t.Fatalf("persisted grant does not open under the device keystore + pinned machine key: %v", err)
	}
	if epochID != sk.api.pairing.EpochID || seq != sk.api.pairing.GrantSeq {
		t.Fatalf("grant coords = (epoch %d, seq %d); want (%d, %d)", epochID, seq, sk.api.pairing.EpochID, sk.api.pairing.GrantSeq)
	}
	if keys != sk.api.pairing.EpochKeys {
		t.Fatal("grant delivered different epoch keys than the config granted")
	}
}
