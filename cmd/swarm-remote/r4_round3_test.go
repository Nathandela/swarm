package main

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 round 3's BLOCKING finding on D4/u37c: the
// round-2 fix opened a SELF-DoS. redrivePendingMachineRevoke runs at EVERY swarm-remote
// start and drives the stored obligation with NO check that the pairing is still
// revoked. push-gateway.json has no writer anywhere in the tree (a hand-provisioned
// scaffold), so a re-pair after a revoke keeps the SAME push address -- and Drive uses
// the obligation's stored address regardless. Sequence: owner revokes -> obligation
// recorded -> gateway unreachable, obligation stays pending -> owner re-pairs a phone
// -> the next process start DELETEs the now-LIVE push address at the gateway
// (internal/pushgw/revoke.go tombstones it), destroying the fresh pairing's wake path
// permanently while swarm-remote reports success.
//
// The round-2 justification ("a completed revoke leaves ZERO paired devices -- exactly
// the state the gate exits on") is a PRECONDITION the code never enforced. Round 3
// enforces it: the redrive drives only while quiescent (zero paired devices), and a
// pending obligation found with a device paired again is RETIRED durably -- never left
// to fire on a later zero-device start against an address that was live in between.
//
// Second (the round-2 NOTE, D4 durability hole): machineRevoke(record=true) returned
// BEFORE Record() when machine_revoke_capability was empty, so on a pre-producer
// provisioning the revoke moment left NO durable obligation at all -- provisioning the
// capability afterwards could never drive the delete that was owed. Round 3 records the
// obligation unconditionally and only skips the drive.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestR4R3_Redrive_RefusesToDeleteALivePushAddress: a pending obligation plus a device
// PAIRED AGAIN. The redrive must present ZERO deletes at the gateway -- the stored
// address is (by provisioning reality: push-gateway.json has no writer in this tree)
// the live pairing's own wake path -- and must RETIRE the obligation durably, so a
// later zero-device start cannot fire it either.
func TestR4R3_Redrive_RefusesToDeleteALivePushAddress(t *testing.T) {
	stateDir := t.TempDir()

	capture := &r4r2Capture{}
	hs := httptest.NewTLSServer(capture.handler())
	defer hs.Close()

	writePushGatewayFile(t, stateDir, pushGatewayFile{
		GatewayURL:              hs.URL,
		SubmitCapability:        "cap-submit-000000000000000000000",
		MachineRevokeCapability: "cap-machine-revoke-00000000000000",
		PushAddress:             "4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e",
	})
	// The residue of an earlier revoke whose delete never resolved...
	r4r2RecordObligation(t, stateDir)
	// ...and a phone paired again since. The quiescence premise of the redrive's
	// placement no longer holds, and the stored address is the LIVE pairing's.
	addPairedDevice(t, stateDir)

	prev := revokeHTTPClient
	revokeHTTPClient = func() *http.Client { return hs.Client() }
	t.Cleanup(func() { revokeHTTPClient = prev })

	redrivePendingMachineRevoke(stateDir)

	if got := capture.count(); got != 0 {
		t.Fatalf("the gateway saw %d DELETE(s) with a device paired again; the redrive just "+
			"destroyed the live pairing's wake path (the tombstone makes it permanent)", got)
	}

	store, err := remotegw.OpenRevokeObligationStore(
		filepath.Join(stateDir, "remote", "revoke-obligation.json"))
	if err != nil {
		t.Fatalf("reopening the obligation store: %v", err)
	}
	if _, ok := store.Pending(); ok {
		t.Fatalf("the obligation is STILL PENDING with a device paired again; the next " +
			"zero-device start (the next revoke) will fire it against whatever address is " +
			"stored -- it must be retired durably, not deferred")
	}
}

// TestR4R3_Redrive_StillDrivesWhileQuiescent: the gate must not break the round-2
// contract -- with ZERO paired devices (the only state a pending obligation should
// exist in) the redrive still drives and resolves the obligation.
func TestR4R3_Redrive_StillDrivesWhileQuiescent(t *testing.T) {
	stateDir := t.TempDir()

	capture := &r4r2Capture{}
	hs := httptest.NewTLSServer(capture.handler())
	defer hs.Close()

	writePushGatewayFile(t, stateDir, pushGatewayFile{
		GatewayURL:              hs.URL,
		SubmitCapability:        "cap-submit-000000000000000000000",
		MachineRevokeCapability: "cap-machine-revoke-00000000000000",
		PushAddress:             "4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e",
	})
	r4r2RecordObligation(t, stateDir)

	prev := revokeHTTPClient
	revokeHTTPClient = func() *http.Client { return hs.Client() }
	t.Cleanup(func() { revokeHTTPClient = prev })

	redrivePendingMachineRevoke(stateDir)

	if got := capture.count(); got != 1 {
		t.Fatalf("the gateway saw %d DELETE(s) from a 0-device state dir, want exactly 1: "+
			"the quiescence gate must not make the durable retry unreachable again", got)
	}
}

// TestR4R3_RevokeMoment_RecordsTheObligationEvenWithoutACapability: a pre-producer
// push-gateway.json (no machine_revoke_capability) still owes the delete. The revoke
// moment must leave a DURABLE PENDING obligation -- only the drive is skipped -- so
// provisioning the capability later can resolve what is owed. Zero network attempts:
// there is no capability to present.
func TestR4R3_RevokeMoment_RecordsTheObligationEvenWithoutACapability(t *testing.T) {
	stateDir := t.TempDir()

	capture := &r4r2Capture{}
	hs := httptest.NewTLSServer(capture.handler())
	defer hs.Close()

	prev := revokeHTTPClient
	revokeHTTPClient = func() *http.Client { return hs.Client() }
	t.Cleanup(func() { revokeHTTPClient = prev })

	var addr remotegw.PushAddress
	for i := range addr {
		addr[i] = 0x4E
	}
	machineRevoke(stateDir, hs.URL, "", addr, true)

	if got := capture.count(); got != 0 {
		t.Fatalf("the gateway saw %d request(s) with no capability to present, want 0", got)
	}
	store, err := remotegw.OpenRevokeObligationStore(
		filepath.Join(stateDir, "remote", "revoke-obligation.json"))
	if err != nil {
		t.Fatalf("reopening the obligation store: %v", err)
	}
	ob, ok := store.Pending()
	if !ok {
		t.Fatal("the revoke moment left NO durable obligation on a pre-producer provisioning; " +
			"provisioning machine_revoke_capability afterwards can never drive the delete " +
			"that was owed -- the durable-retry promise silently does not hold")
	}
	if ob.Address != addr {
		t.Errorf("the recorded obligation holds address %x, want %x", ob.Address, addr)
	}
}
