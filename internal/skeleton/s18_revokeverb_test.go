package skeleton

// Slice S18 -- the gateway arm that makes PB-SEC-7's PHONE-SIDE panic action work.
//
// PB-SEC-7 is "device-loss response works end to end: revoke -> epoch rotation -> gateway
// severs and exits -> lost device dead", and s18_sec7_deviceloss_test.go asserts that whole
// chain -- starting from coreAPI.RevokeDevice, i.e. from the OWNER acting at their machine.
// This file asserts the other way in, which nothing covered: the revoke the PHONE issues.
//
// THE DEFECT THIS FILE EXISTS FOR. ActionDeviceRevoke is in the signed action set,
// skeleton/deviceauth.go classes it, and protocol/server.go handleDeviceRevoke serves it --
// but remotegw's opForAction had no arm for it, so a correctly-signed device_revoke fell
// through to the default and was refused "unsupported command action" one hop short of the
// daemon. A refused action seals NO reply, so the op could never resolve either: the phone's
// declared panic button did nothing and said nothing. mobile.RevokeThisDevice worked around it
// by sealing nothing at all and recording a durable local refusal, and
// mobile/screen_coverage.tsv recorded the gap against this requirement.
//
// The mapping also has to carry its TARGET. handleDeviceRevoke reads Control.TargetDeviceID
// both as its authorization subject and as the device to remove; the phone signs that id in
// the SESSION position of the command tuple, because that tuple has no separate device field.
// Gateway.ForwardCommand copies it across. An arm without the copy forwards a revoke naming no
// device at all, and an arm that copied the WRONG field would revoke the signer regardless of
// what it signed for -- so the second test below names a different device on purpose.
//
// WHY THE FIRST TEST DOES NOT WAIT FOR A REPLY, and it is not an omission. A successful
// self-revoke DESTROYS THE PATH ITS OWN REPLY WOULD COME BACK ON: the daemon removes the
// device and rotates the epoch in one transaction, severs the live lease, and the gateway
// exits; the relay-side registration and mailbox go with it. Asserting a reply here would be
// asserting a race. What is asserted instead is the effect on the machine, which is the whole
// of what the owner of a lost handset needs, plus the gateway exit that PB-SEC-7's arrow ends
// with. The reply PATH is asserted by the second test, where nothing is severed.
//
// VALIDATED BY BREAKING EACH LINK, at implementation time; both edits reverted immediately:
//
//	remove the opForAction arm     -> "the device is STILL REGISTERED 15s after", test 1 fails
//	remove the TargetDeviceID copy -> the daemon refuses (no target), the device survives, and
//	                                  test 1 fails on the same assertion
//
// WHAT IS NOT CLAIMED. This is s18_sec6_adversarial_test.go's rig: a real relay, a real
// gateway Service, a real daemon and a real phonecore.Core holding real keys. It is not a
// handset. PB-E2E-5 stays deferred and nothing here touches it.

import (
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestS18_APhoneSealedDeviceRevokeRemovesTheDeviceAndStopsTheGateway.
func TestS18_APhoneSealedDeviceRevokeRemovesTheDeviceAndStopsTheGateway(t *testing.T) {
	r := s18NewRig(t, device.CapFull)

	// PRECONDITION. Every assertion below is "the device is gone", which holds trivially for
	// a device that was never there.
	if _, ok := r.sk.api.devices.Get(r.deviceID); !ok {
		t.Fatalf("PB-SEC-7: the rig's device is not registered, so a revoke could not remove it")
	}

	r.sealDeviceRevoke(r.deviceID, "op-s18-selfrevoke")

	// The registry is read IN PROCESS rather than by polling the relay: the phone's mailbox is
	// exactly what a successful revoke tears down, so watching it would be watching the wrong
	// thing at the wrong end.
	if !r.awaitDeviceGone(r.deviceID, 15*time.Second) {
		t.Fatalf("PB-SEC-7: the device is STILL REGISTERED 15s after its own correctly-signed " +
			"device_revoke was sealed and delivered. The phone's panic action is the owner's " +
			"response to a handset they no longer hold, and it did nothing: the command was " +
			"opened by the gateway and dropped one hop short of the daemon")
	}

	// PB-SEC-7's arrow ends "gateway severs and exits", and reaching that from a PHONE-issued
	// revoke is the new part. s18_sec7_deviceloss_test.go asserts the same exit from the
	// owner's side; this is the assertion that the phone's own command drives the same chain.
	select {
	case err := <-r.gatewayDone:
		r.stopGatewayWait()
		if !errors.Is(err, remotegw.ErrDeviceRevoked) {
			t.Errorf("PB-SEC-7: the gateway exited with %v, not remotegw.ErrDeviceRevoked. "+
				"Whatever stopped it, it was not the revoke", err)
		}
	case <-time.After(15 * time.Second):
		t.Errorf("PB-SEC-7: the gateway Service was still running 15s after the phone revoked " +
			"itself, so it goes on sealing session content to the revoked device's mailbox " +
			"under the epoch key that device still holds")
	}
}

// TestS18_ARevokeCarriesTheSIGNEDTargetAndNotTheSigner is the leg that stops the test above
// passing for the wrong reason, and it is aimed squarely at the gateway's TargetDeviceID copy.
//
// The daemon takes its target from Control.TargetDeviceID, and the gateway is what fills that
// field in. If it filled it from the connection's device id instead of from the signed tuple,
// every revoke would remove the SIGNER -- which for the self-revoke above is the same device,
// so that test would pass either way and the substitution would ship.
//
// Here the phone signs a revoke naming a device that is not registered. The signature is
// perfectly valid and binds that target, so nothing about the crypto refuses it; what must
// happen is that the machine acts on the NAMED device (an idempotent no-op, since it holds no
// such record) and leaves the signer alone. The reply path is intact in this case precisely
// because nothing was severed, so the answer is asserted too: it is the half the missing arm
// destroyed independently of the revoke itself.
func TestS18_ARevokeCarriesTheSIGNEDTargetAndNotTheSigner(t *testing.T) {
	r := s18NewRig(t, device.CapFull)

	const operationID = "op-s18-foreignrevoke"
	r.sealDeviceRevoke("device-that-was-never-registered", operationID)

	reply, ok := r.awaitSealedReply(operationID, 5*time.Second)
	if !ok {
		t.Fatalf("PB-SEC-7: no sealed reply for the phone's device_revoke within 5s. The gateway "+
			"refuses an action it cannot map WITHOUT sealing anything, so the panic button "+
			"resolves never -- which on screen is indistinguishable from a command still on its "+
			"way (operation %s)", operationID)
	}
	// OK is the daemon's deliberate answer for a revoke that removed nothing: handleDeviceRevoke
	// replies OK for a clean call "whether it removed a device or was an idempotent no-op", so a
	// retry after a dropped reply is harmless. What would be wrong is an ERROR, which would tell
	// the phone's retry policy the panic action had failed when the machine had simply already
	// done it.
	if reply.Op == protocol.OpError {
		t.Errorf("PB-SEC-7: the machine answered an error (%q) for a device_revoke naming a device "+
			"it does not hold. A revoke is idempotent by design, so a retry after a dropped reply "+
			"must not look like a failure", reply.Error)
	}

	if _, ok := r.sk.api.devices.Get(r.deviceID); !ok {
		t.Errorf("PB-SEC-7: a device_revoke naming ANOTHER device removed the SIGNING device. The " +
			"target rides the session position of the signed tuple and the gateway must carry " +
			"that value into Control.TargetDeviceID, not substitute the connection's own device " +
			"id -- a substitution the self-revoke test cannot see, because there the two are equal")
	}
}

// sealDeviceRevoke signs, seals and appends one device_revoke exactly as the facade does: the
// target device id sits in the SESSION position of the signed tuple, which is where
// Gateway.ForwardCommand reads it from before moving it to Control.TargetDeviceID.
func (r *s18Rig) sealDeviceRevoke(targetDeviceID, operationID string) {
	r.t.Helper()
	cmd, err := phonecore.SignCommand(r.core.KeyStore(), phonecore.CommandInput{
		Action:      protocol.ActionDeviceRevoke,
		Machine:     r.machine,
		Session:     targetDeviceID,
		OperationID: operationID,
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err != nil {
		r.t.Fatalf("sign device_revoke: %v", err)
	}
	seq, err := r.core.Seq().NextCommand()
	if err != nil {
		r.t.Fatalf("allocate command seq: %v", err)
	}
	env, err := phonecore.SealCommandEnvelope(r.keys.ContentKey, r.epochID, seq, cmd)
	if err != nil {
		r.t.Fatalf("seal device_revoke: %v", err)
	}
	if _, err := r.phoneRelay.MailboxAppend(r.ctx, r.machineTgt, env); err != nil {
		r.t.Fatalf("append device_revoke: %v", err)
	}
}

// awaitDeviceGone polls the daemon's own registry. It is deliberately not a relay read: the
// revoke tears the phone's mailbox down, so the transport is the one place the answer cannot
// reliably be observed.
func (r *s18Rig) awaitDeviceGone(deviceID string, within time.Duration) bool {
	r.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, ok := r.sk.api.devices.Get(deviceID); !ok {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
