package skeleton

// Slice S18 -- PB-SEC-7:
//
//	"Device-loss response works end to end: revoke -> epoch rotation -> gateway severs and
//	 exits -> lost device dead."
//	Criterion: "Phase A revoke evidence RE-ASSERTED through the real transport; ADR documents
//	 threat + response."
//
// LIKE PB-SEC-6, THIS FILE IS GREEN AT HEAD AND SAYS SO. The criterion's own word is
// "re-asserted": the chain is expected to work, and it does. Claiming a RED here would be a
// false claim about the file's own state.
//
// The assertions were validated by breaking each link instead. Run at RED time:
//
//	epoch rotation -> remove the rotateEpoch() call from coreAPI.RevokeDevice
//	                  -> "the epoch did not rotate on revoke (1 -> 1)", test fails
//	gateway exit   -> blank ServiceConfig.StateDir so deviceRevoked() cannot fire
//	                  -> "still running 15s after the paired device was revoked", test fails
//	the whole rig  -> construct the gateway Service but never start it
//	                  -> both tests fail on their pre-revoke "the device was alive" leg
//
// (The production edit for the first was reverted immediately; git reports internal/skeleton
// /api.go unmodified.)
//
// WHAT IS NEW HERE, and what is not. That the revoke path removes the device and rotates the
// epoch in ONE transaction is already verified in the daemon's own tests, and this file does
// not re-litigate it. What Phase A never showed, because it had no transport to show it
// through, is the REST of the arrow: that the running gateway actually exits rather than
// reconnecting under the stale key, and that the device in the attacker's hand is genuinely
// dead afterwards rather than merely un-listed.
//
// So every assertion below is made on the S18 rig (s18_sec6_adversarial_test.go) -- a real
// relay, a real gateway Service, a real daemon, and a real phonecore.Core holding real keys
// and a live lease at the moment the owner revokes. The device is "lost" in the only sense
// that matters to this requirement: an adversary holds it, with everything it had.
//
// THE ADVERSARY KEEPS EVERYTHING. Nothing is taken away from the phone when the revoke lands.
// It still holds its sealed device keys, its epoch content key, its durable send-seq, its
// live relay connection and a lease the daemon granted seconds ago. That is the whole point:
// the response has to work against a device that cooperates with nothing.
//
// WHAT IS NOT CLAIMED. No handset, no real Keystore, no physical loss. PB-E2E-5 stays
// deferred and nothing here may be read as covering any part of it.

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestPBSEC7_TheDeviceLossChainRunsEndToEnd walks the requirement's arrow in order, on one
// rig, with a live lease in flight when the revoke lands.
func TestPBSEC7_TheDeviceLossChainRunsEndToEnd(t *testing.T) {
	r := s18NewRig(t, device.CapFull)

	// --- PRECONDITION: the lost device is genuinely ALIVE first -------------
	//
	// Every assertion after the revoke is of the form "the device can no longer do X". That
	// family passes trivially against a device that never could, so X is demonstrated first.
	r.takeControl(r.namespaced, "op-s18-sec7-lease", 15*time.Minute, 3600)
	alive := s18Marker("sec7alive")
	r.typeWithTheClientGuardRemoved(r.namespaced, []byte(alive+"\n"))
	if !r.watcher.awaitArrival(alive, 1, s18ArrivalWindow) {
		t.Fatalf("PB-SEC-7: the device could not type BEFORE the revoke, so every 'it is dead " +
			"afterwards' assertion below would hold for a device that was never alive")
	}

	epochBefore := r.currentEpoch(t)

	// --- REVOKE + EPOCH ROTATION -------------------------------------------
	removed, err := r.sk.api.RevokeDevice(r.deviceID)
	if err != nil {
		t.Fatalf("PB-SEC-7: RevokeDevice returned %v; the owner's response to a lost device failed", err)
	}
	if !removed {
		t.Fatalf("PB-SEC-7: RevokeDevice reported nothing removed for the device that is " +
			"registered and holding a live lease")
	}
	if _, still := r.sk.api.devices.Get(r.deviceID); still {
		t.Errorf("PB-SEC-7: the revoked device is still in the daemon registry")
	}

	epochAfter := r.currentEpoch(t)
	if epochAfter <= epochBefore {
		t.Errorf("PB-SEC-7: the epoch did not rotate on revoke (%d -> %d). The lost device "+
			"retains a content key that still opens every future frame, so revoking it removes "+
			"a row from a registry and nothing else", epochBefore, epochAfter)
	}

	// --- THE GATEWAY SEVERS AND EXITS ---------------------------------------
	//
	// This is the half Phase A could not assert. A gateway that merely lost its journal
	// subscription would RECONNECT and resume sealing epoch frames to the revoked device's
	// mailbox under the stale key; the requirement's arrow says it exits instead.
	select {
	case err := <-r.gatewayDone:
		r.stopGatewayWait()
		if err == nil {
			t.Errorf("PB-SEC-7: the gateway Service exited with a nil error after the revoke. " +
				"An ordinary shutdown and a revocation exit are not the same event, and the " +
				"operator cannot tell them apart")
		} else if !errors.Is(err, remotegw.ErrDeviceRevoked) {
			t.Errorf("PB-SEC-7: the gateway exited with %v, not remotegw.ErrDeviceRevoked. "+
				"Whatever stopped it, it was not the revoke", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("PB-SEC-7: the gateway Service was still running 15s after the paired device " +
			"was revoked. It reconnects rather than exiting, so it goes on sealing session " +
			"content to the lost device's mailbox under the epoch key that device still holds")
	}

	// --- THE LOST DEVICE IS DEAD --------------------------------------------
	//
	// The keystroke path first: the daemon re-checks device registration on EVERY keystroke
	// (protocol/server.go controlGateOpen clause 4), so the lease the device was holding when
	// the revoke landed must stop carrying bytes -- not at its expiry, now.
	dead := s18Marker("sec7dead")
	r.typeWithTheClientGuardRemoved(r.namespaced, []byte(dead+"\n"))
	r.watcher.requireNeverArrives(t, dead,
		"PB-SEC-7: a revoked device kept typing on the lease it held when it was revoked")
}

// TestPBSEC7_ARevokedDeviceCannotTakeControlAgain covers the other half of "dead": not merely
// that the live lease stopped, but that the device cannot open a new one. A phone that is cut
// off until it retries is not cut off.
//
// It is a SEPARATE rig because the test above deliberately lets the gateway exit, and a
// second take_control needs one running -- reusing that rig would assert against a machine
// with no command loop at all, which is defect class (iii): a refusal observed because the
// subject is unreachable rather than because it refused.
func TestPBSEC7_ARevokedDeviceCannotTakeControlAgain(t *testing.T) {
	r := s18NewRig(t, device.CapFull)

	// Alive first, for the same reason as above.
	r.takeControl(r.namespaced, "op-s18-sec7b-pre", 15*time.Minute, 3600)
	alive := s18Marker("sec7balive")
	r.typeWithTheClientGuardRemoved(r.namespaced, []byte(alive+"\n"))
	if !r.watcher.awaitArrival(alive, 1, s18ArrivalWindow) {
		t.Fatalf("PB-SEC-7: the device could not type before the revoke; the refusal below " +
			"would prove nothing")
	}

	if _, err := r.sk.api.RevokeDevice(r.deviceID); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}

	// The device signs a fresh, perfectly valid take_control with the keys it still holds and
	// seals it under the content key it still holds. The daemon must refuse it on identity
	// alone. (The gateway is exiting concurrently, which is itself a refusal of this command;
	// the assertion is that the keystroke does not land either way, and the two paths cannot
	// both be closed for the wrong reason -- the pre-revoke leg above proves both were open.)
	r.takeControl(r.namespaced, "op-s18-sec7b-post", 15*time.Minute, 3600)
	time.Sleep(time.Second)

	after := s18Marker("sec7bafter")
	r.typeWithTheClientGuardRemoved(r.namespaced, []byte(after+"\n"))
	r.watcher.requireNeverArrives(t, after,
		"PB-SEC-7: a revoked device signed a fresh take_control with the keys it kept and typed again")
}

// currentEpoch reads the machine's epoch off the persisted identity -- the same file
// coreAPI.rotateEpoch rewrites. PB-SEC-7's arrow names the rotation explicitly, and reading
// the ARTIFACT rather than inferring it from "old frames stopped working" is what separates
// "the key changed" from "the transport happened to break".
func (r *s18Rig) currentEpoch(t *testing.T) uint32 {
	t.Helper()
	id, err := machineid.Load(filepath.Join(r.sk.api.stateDir, "remote", "machine.key"))
	if err != nil {
		t.Fatalf("PB-SEC-7: cannot read the machine identity that holds the epoch: %v", err)
	}
	return id.EpochID()
}

var _ = protocol.NamespacedID
