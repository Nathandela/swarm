package skeleton

// FAILING-FIRST integration tests for remote slice A3.3-d: the daemon's coreAPI
// (internal/skeleton) implements protocol.PairingHost with a REAL pairing handshake
// end to end — pair over an in-memory rendezvous, run the mandatory SAS confirm gate
// over the owner-tier wire, and on an affirmative confirm ONLY: enroll.Enroll ->
// coreAPI.devices.Add, minting real device authority. This is the enrollment keystone
// (enroll_e2e_test.go) driven through the pair_start/pair_confirm WIRE against a live
// assembled daemon, instead of a direct Machine.Pair call.
//
// Contrast pairing_bridge_test.go (protocol layer): that slice wired the pair_* ops to
// a FAKE PairingHost (canned SAS, no crypto/enroll). THIS slice proves the REAL host —
// a real Noise handshake, a real SAS, real enrollment — and the security invariant that
// device authority is minted ONLY on an explicit affirmative confirm: a deny or a
// disconnect at the SAS gate enrolls NOTHING (fail closed).
//
// RED is undefined-only: this file does not compile because the intended PRODUCTION
// pairing seam on coreAPI does not exist yet. The GREEN implementer defines EXACTLY
// this seam (match it byte-for-byte, as pairing_bridge_test.go's contract was matched):
//
// SEAM 1 — the injected pairing config (a new type in internal/skeleton; nil until
// wired, mirroring how the assembly wires d.api.devices / d.api.launchPolicy /
// d.api.stateDir). It carries the machine-side pairing identity + enrollment material
// (generated in tests exactly as enroll_e2e_test.go does; in prod from the daemon
// keystore) plus the rendezvous seam. A nil config MUST make BeginPairing fail
// ("pairing not configured on this daemon"):
//
//	type pairingConfig struct {
//	    Static       *crypto.NoiseStatic // machine Noise-static handle (msg2 identity)
//	    RecipientPub []byte              // machine sealed-box recipient X25519 pub (A14)
//	    SignPub      []byte              // machine Ed25519 grant-signing pub (phone pins it)
//	    SignPriv     ed25519.PrivateKey  // machine Ed25519 grant-signing priv (signs the epoch grant)
//	    EpochID      uint32              // the granted epoch id
//	    GrantSeq     uint64              // the epoch grant sequence
//	    EpochKeys    crypto.EpochKeys    // wake/content keys sealed to the paired device
//	    Hostname     string              // MachinePayload.Hostname
//	    RoutingID    []byte              // MachinePayload.MachineRoutingID
//	    RelayAuthPub []byte              // MachinePayload.MachineRelayAuthPub
//
//	    // NewRendezvous returns the machine-side RendezvousTransport for a freshly
//	    // generated rendezvous id. BeginPairing mints the rendezvous id + single-use
//	    // secret + QR, then asks this for the transport it drives the machine leg on.
//	    // In prod a relay adapter (slice A3.3-e); in tests a memRendezvous the test
//	    // ALSO drives the device leg on.
//	    NewRendezvous func(ctx context.Context, id [16]byte) (pairing.RendezvousTransport, error)
//	}
//
//	// coreAPI gains the field, set at assembly (nil => pairing unsupported):
//	//   pairing *pairingConfig
//
// SEAM 2 — coreAPI implements protocol.PairingHost. BeginPairing MUST, synchronously:
// mint a rendezvous id + single-use 32-byte secret + a decodable pairing.EncodeQR
// carrying BOTH (a real phone recovers the secret from the QR — this test decodes it to
// drive the device leg), call cfg.NewRendezvous(id) for the machine-side transport, and
// return the PairView{QR, RendezvousID, ExpiresAt}. It runs pairing.NewMachine(mp).Pair
// in a background goroutine whose pairing.ConfirmFunc calls the passed-in
// confirm(sas[:], deviceName). On an AFFIRMATIVE confirm ONLY: enroll.Enroll(outcome,
// cap, cfg.SignPriv, cfg.EpochID, cfg.GrantSeq, cfg.EpochKeys, now) then
// a.devices.Add(res.Record), then result(PairResult{DeviceID:..., Name, Capability}).
// On decline / disconnect / any error: result(PairResult{Err:...}) and enroll NOTHING
// (no Add). GREEN also adds the conformance pin `var _ protocol.PairingHost = (*coreAPI)(nil)`.
//
// The MachineParams the host builds mirror enroll_e2e_test.go's mp exactly (Static,
// Secret, RendezvousID, LocalConsole:true, Confirm, Payload{Hostname, MachineRoutingID,
// MachineRelayAuthPub, RecipientPub, MachineSignPub, EpochID}); enroll.Enroll's args
// mirror that test's Enroll call.
//
// Reused (same package `skeleton`, so NOT replicated): memRendezvous / rendezvousPair
// (enroll_e2e_test.go), dialRemote / rawRemote (remote_journal_test.go), assemble
// (serve_test.go). Every wait is deadline-bounded; nothing may hang.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// injectPairing wires the coreAPI pairing seam with a test identity generated exactly
// as enroll_e2e_test.go does, plus a memRendezvous provider. It returns a channel that
// receives the DEVICE end of the shared rendezvous the instant BeginPairing asks for a
// transport, so the test can drive the scripted device leg on the SAME memRendezvous
// the daemon's machine leg runs on.
func injectPairing(t *testing.T, sk *Daemon) chan *memRendezvous {
	t.Helper()
	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	signPub, signPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("machine grant-signing key: %v", err)
	}
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}

	deviceEnds := make(chan *memRendezvous, 1)
	sk.api.pairing = &pairingConfig{
		Static:       machineID.NoiseStatic(),
		RecipientPub: machineID.RecipientPublic(),
		SignPub:      signPub,
		SignPriv:     signPriv,
		EpochID:      1,
		GrantSeq:     1,
		EpochKeys:    keys,
		Hostname:     "test-machine.local",
		RoutingID:    []byte("machine-routing-id-0001"),
		RelayAuthPub: make([]byte, 32),
		NewRendezvous: func(context.Context, [16]byte) (pairing.RendezvousTransport, error) {
			mEnd, dEnd := rendezvousPair()
			deviceEnds <- dEnd
			return mEnd, nil
		},
	}
	return deviceEnds
}

// devLegResult is the scripted device leg's terminal outcome.
type devLegResult struct {
	outcome *pairing.DeviceOutcome
	err     error
}

// runDeviceLeg scripts the phone side of the handshake on the shared rendezvous, using
// the secret + rendezvous id recovered from the pair_start QR (as a real phone would).
// It mirrors enroll_e2e_test.go's DeviceParams so the enrolled record is well-formed.
func runDeviceLeg(ctx context.Context, ks crypto.KeyStore, dEnd pairing.RendezvousTransport, qp pairing.QRPayload) chan devLegResult {
	dp := pairing.DeviceParams{
		Static:       ks.NoiseStatic(),
		Secret:       qp.PairingSecret,
		RendezvousID: qp.RendezvousID,
		Payload: pairing.DevicePayload{
			DeviceName:           "Test iPhone",
			DeviceRoutingID:      []byte("device-routing-id-0001"),
			DeviceRelayAuthPub:   ks.RelayAuthPublic(),
			RecipientPub:         ks.RecipientPublic(),
			DeviceCommandSignPub: ks.CommandSigningPublic(),
		},
	}
	ch := make(chan devLegResult, 1)
	go func() {
		do, err := pairing.RunDevice(ctx, dp, dEnd)
		ch <- devLegResult{outcome: do, err: err}
	}()
	return ch
}

// awaitControl reads control frames until one carries op, within a bounded frame budget
// so a missing push fails fast instead of hanging.
func awaitControl(t *testing.T, rc *rawRemote, op string) protocol.Control {
	t.Helper()
	for i := 0; i < 8; i++ {
		c, err := rc.readTry(5 * time.Second)
		if err != nil {
			t.Fatalf("waiting for %q: %v", op, err)
		}
		if c.Op == op {
			return c
		}
	}
	t.Fatalf("did not receive %q within the frame budget", op)
	return protocol.Control{}
}

// recvDeviceEnd waits (bounded) for BeginPairing to request a rendezvous, proving the
// host actually created one via the injected provider.
func recvDeviceEnd(t *testing.T, deviceEnds chan *memRendezvous) *memRendezvous {
	t.Helper()
	select {
	case d := <-deviceEnds:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("BeginPairing never requested a rendezvous (NewRendezvous was not called)")
		return nil
	}
}

// TestPairing_WireFlowEnrollsDevice (happy path): a real Noise pairing driven through
// the owner-tier pair_start/pair_confirm wire against a live coreAPI PairingHost. An
// affirmative confirm at the SAS gate enrolls the device — a real registry record minted
// by enroll.Enroll -> devices.Add — and flips the remote-control kill switch on.
func TestPairing_WireFlowEnrollsDevice(t *testing.T) {
	sk := assemble(t)
	deviceEnds := injectPairing(t, sk)

	// No device paired yet: the kill switch is off (fail-closed default).
	if got := sk.api.devices.List(); len(got) != 0 {
		t.Fatalf("registry not empty before pairing: %d devices", len(got))
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	rc := dialRemote(t, sk.SocketPath(), protocol.CapPairing)
	rc.write(protocol.Control{Op: protocol.OpPairStart, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Capability: "full"}})

	// The synchronous pair_start reply carries the QR + rendezvous id.
	reply := awaitControl(t, rc, protocol.OpPairStart)
	if reply.Pairing == nil || reply.Pairing.QR == "" || reply.Pairing.RendezvousID == "" {
		t.Fatalf("pair_start reply missing QR/RendezvousID: %+v", reply.Pairing)
	}
	// A real phone recovers the single-use secret + rendezvous id from the QR; decode it
	// to drive the scripted device leg on the SAME rendezvous the daemon minted.
	qp, err := pairing.DecodeQR(reply.Pairing.QR)
	if err != nil {
		t.Fatalf("pair_start QR is not a decodable pairing QR (must carry the secret + rendezvous id): %v", err)
	}

	dEnd := recvDeviceEnd(t, deviceEnds)
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("device keystore: %v", err)
	}
	devDone := runDeviceLeg(ctx, ks, dEnd, qp)

	// With the handshake complete, the host pushes the SAS gate: a 6-word SAS + the
	// device's self-reported name.
	pending := awaitControl(t, rc, protocol.OpPairPending)
	if pending.Pairing == nil || len(pending.Pairing.SAS) != 6 {
		t.Fatalf("pair_pending missing the 6-word SAS gate: %+v", pending.Pairing)
	}
	if pending.Pairing.DeviceName != "Test iPhone" {
		t.Fatalf("pair_pending DeviceName = %q; want %q", pending.Pairing.DeviceName, "Test iPhone")
	}

	// Approve at the SAS gate: this — and ONLY this — mints the device authority.
	rc.write(protocol.Control{Op: protocol.OpPairConfirm, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Allow: true}})

	res := awaitControl(t, rc, protocol.OpPairResult)
	if res.Pairing == nil || res.Pairing.DeviceID == "" {
		t.Fatalf("pair_result = %+v; want success carrying the new DeviceID", res.Pairing)
	}

	// The device leg pinned the machine (the handshake truly completed end to end).
	select {
	case r := <-devDone:
		if r.err != nil {
			t.Fatalf("device leg failed on the happy path: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("device leg never completed on the happy path")
	}

	// The registry now holds exactly the enrolled device, bound to the device's pinned
	// command-signing key (device.DeviceIDFor), at the requested capability tier.
	recs := sk.api.devices.List()
	if len(recs) != 1 {
		t.Fatalf("registry has %d devices after pairing; want exactly 1 enrolled device", len(recs))
	}
	wantID := device.DeviceIDFor(ks.CommandSigningPublic())
	if recs[0].DeviceID != wantID {
		t.Fatalf("enrolled DeviceID = %q; want %q (bound to the pinned command-signing key)", recs[0].DeviceID, wantID)
	}
	if res.Pairing.DeviceID != wantID {
		t.Fatalf("pair_result DeviceID = %q; want %q (must match the enrolled record)", res.Pairing.DeviceID, wantID)
	}
	if recs[0].Name != "Test iPhone" {
		t.Fatalf("enrolled Name = %q; want %q", recs[0].Name, "Test iPhone")
	}
	if recs[0].Capability != device.CapFull {
		t.Fatalf("enrolled Capability = %v; want CapFull", recs[0].Capability)
	}
	// Pairing a device flips the derived remote-control master switch on.
	if !sk.api.RemoteControlEnabled() {
		t.Fatal("RemoteControlEnabled() is false after a device was enrolled; want true")
	}
}

// TestPairing_DenyEnrollsNothing: a declining pair_confirm (Allow=false) at the SAS gate
// drives the host to a FAILURE pair_result and enrolls NOTHING — the registry stays
// empty. This is the anti-MITM invariant: no device authority without an explicit allow.
func TestPairing_DenyEnrollsNothing(t *testing.T) {
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

	// DECLINE at the SAS gate.
	rc.write(protocol.Control{Op: protocol.OpPairConfirm, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Allow: false}})

	// The result is a failure: no device identity is carried.
	res := awaitControl(t, rc, protocol.OpPairResult)
	if res.Pairing != nil && res.Pairing.DeviceID != "" {
		t.Fatalf("pair_result carried DeviceID %q on a DECLINED pairing; want failure (no device)", res.Pairing.DeviceID)
	}

	// The device leg saw the machine's authenticated decline (nothing pinned there either).
	select {
	case r := <-devDone:
		if !errors.Is(r.err, pairing.ErrPairingDeclined) {
			t.Fatalf("device leg err = %v; want ErrPairingDeclined on a declined pairing", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("device leg never resolved after a decline")
	}

	// The security invariant: a declined pairing enrolls NOTHING.
	if got := sk.api.devices.List(); len(got) != 0 {
		t.Fatalf("registry has %d devices after a DECLINED pairing; want 0 (deny enrolls nothing)", len(got))
	}
	if sk.api.RemoteControlEnabled() {
		t.Fatal("RemoteControlEnabled() is true after a declined pairing; want false")
	}
}

// TestPairing_DisconnectBeforeConfirmEnrollsNothing: dropping the connection at the SAS
// gate — before any pair_confirm — MUST fail the pairing CLOSED. The connection-derived
// pairing ctx is cancelled, the in-flight confirm returns a non-nil error (a decline),
// the machine leg terminates WITHOUT enrolling, and the registry stays empty. Bounded
// throughout; a hang here would mean the fail-closed ctx-cancel wiring is missing.
func TestPairing_DisconnectBeforeConfirmEnrollsNothing(t *testing.T) {
	sk := assemble(t)
	deviceEnds := injectPairing(t, sk)

	// The device leg's ctx is test-owned and cancelled in cleanup, so a dropped machine
	// leg never leaves the scripted phone blocked on the rendezvous.
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
	_ = runDeviceLeg(ctx, ks, dEnd, qp)

	// Reaching pair_pending proves the machine leg is now BLOCKING on the SAS gate, so
	// closing the connection next exercises the ctx-cancel fail-closed path deterministically.
	pending := awaitControl(t, rc, protocol.OpPairPending)
	if pending.Op != protocol.OpPairPending {
		t.Fatalf("did not reach the SAS gate before disconnect; got op %q", pending.Op)
	}

	// Drop the connection WITHOUT sending pair_confirm.
	_ = rc.conn.Close()

	// Fail closed: the machine leg terminates without enrolling. The registry must NEVER
	// gain a device — verified across a window that comfortably outlasts a successful
	// confirm's enroll->Add, so a late erroneous Add would be caught.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := sk.api.devices.List(); len(got) != 0 {
			t.Fatalf("registry gained %d devices after a DISCONNECT at the SAS gate; want 0 (fail closed: no Add)", len(got))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if sk.api.RemoteControlEnabled() {
		t.Fatal("RemoteControlEnabled() is true after a disconnect-before-confirm; want false")
	}
}
