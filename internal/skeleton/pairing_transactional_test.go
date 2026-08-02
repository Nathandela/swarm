package skeleton

// FAILING-FIRST (TDD RED, GG-5) tests for two re-audit findings the daemon side owns:
//
//   C2 - enrollment must be TRANSACTIONAL: grant.Save runs AFTER devices.Add, so a
//        grant-write failure used to leave an enrolled device (Count=1) that reports
//        "pairing failed" yet BLOCKS re-pairing -- recoverable only by revoke. On a
//        grant.Save error BeginPairing must ROLL BACK the device so a failed pairing
//        leaves NOTHING enrolled and a clean retry works.
//   C4 - revoke must clean the orphaned grant sidecar: RevokeDevice used to call only
//        devices.Remove, leaking grant.Path(registryDir, deviceID) on every
//        revoke-then-repair. RevokeDevice must delete the sidecar after a successful Remove.
//
// Reused (same package): assemble (serve_test.go), injectPairing / runDeviceLeg /
// awaitControl / recvDeviceEnd (pairing_integration_test.go), validDeviceRecord
// (pairing_findings_test.go), dialRemote (remote_journal_test.go).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// TestPairing_GrantSaveFailureRollsBackDevice (C2): when grant.Save fails after the device
// was enrolled, the enrollment must be rolled back -- the registry Count returns to 0 and
// the pairing reports failure -- so a clean retry (no revoke needed) works.
func TestPairing_GrantSaveFailureRollsBackDevice(t *testing.T) {
	sk := assemble(t)
	deviceEnds := injectPairing(t, sk)

	// Force grant.Save to fail: pre-create <stateDir>/devices/grants as a FILE, so the
	// grant.Save os.MkdirAll(<...>/grants) can never create the sidecar directory.
	grantsPath := filepath.Join(sk.api.stateDir, "devices", "grants")
	if err := os.WriteFile(grantsPath, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("pre-create grants file: %v", err)
	}

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

	// The handshake completed (the grant persist, not the pairing, is what fails), so the
	// device leg resolves cleanly.
	select {
	case r := <-devDone:
		if r.err != nil {
			t.Fatalf("device leg failed: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("device leg never completed")
	}

	// The result is a FAILURE: the grant could not be persisted, so no device identity is
	// carried (fail closed).
	res := awaitControl(t, rc, protocol.OpPairResult)
	if res.Pairing != nil && res.Pairing.DeviceID != "" {
		t.Fatalf("pair_result carried DeviceID %q despite a grant-save failure; want failure", res.Pairing.DeviceID)
	}

	// C2: the failed grant write must leave NOTHING enrolled -- a clean retry works without
	// a revoke. Poll briefly to outlast the async enroll->Add->Save->rollback sequence.
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := sk.api.devices.Count()
		if got == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("registry Count = %d after a grant-save failure; want 0 (rolled back, not left blocking re-pairing)", got)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestRevokeDevice_DeletesGrantSidecar (C4): revoking a device must remove its persisted
// grant sidecar, so a revoke-then-repair does not leak the file.
func TestRevokeDevice_DeletesGrantSidecar(t *testing.T) {
	sk := assemble(t)

	rec := validDeviceRecord(t)
	if err := sk.api.devices.Add(rec); err != nil {
		t.Fatalf("seed paired device: %v", err)
	}
	registryDir := sk.api.registryDir()
	if err := grant.Save(registryDir, rec.DeviceID, &crypto.EpochGrant{}); err != nil {
		t.Fatalf("persist grant sidecar: %v", err)
	}
	sidecar := grant.Path(registryDir, rec.DeviceID)
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("precondition: sidecar not written: %v", err)
	}

	removed, err := sk.api.RevokeDevice(rec.DeviceID)
	if err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	if !removed {
		t.Fatalf("RevokeDevice reported no device removed; want true")
	}

	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("grant sidecar still present after revoke (stat err = %v); want gone (C4 leak)", err)
	}
}
