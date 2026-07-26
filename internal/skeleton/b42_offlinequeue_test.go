package skeleton

// EVIDENCE for ADR-007 B42's PB-NET-4 finding, which is a DECISION and not a wiring task.
//
// PB-NET-4's last clause -- "only high-level idempotent ops may queue, with a stated bound" --
// is counted met over internal/phonecore.OpQueue, whose evidence cites a boundedness test.
// `QueuedOp{}` is constructed nowhere outside tests, `Core.Ops()` has no production caller, and
// `mobile.(*App).resolveSend` requires a live connection before ANY mutating op is authored --
// so nothing can ever enqueue. The question the audit put is whether the queue should be wired
// or the clause withdrawn.
//
// THIS FILE IS THE ANSWER, MADE EXECUTABLE. The clause cannot be satisfied as written, because
// a LATER decision took it away:
//
//	§6.0 "Signed ExpiresAt by op class": ordinary commands now + 1 min (take_control now + 15).
//	internal/skeleton/deviceauth.go: `if now.After(cmd.ExpiresAt) { return "command expired" }`.
//	internal/phonecore/opqueue.go:   "The command is signed ONCE at enqueue time; it is never
//	                                  re-signed or re-keyed on replay."
//
// A queued op is a SIGNED command with a one-minute horizon. Held across an outage and replayed
// on reconnect, it is refused by the daemon -- for every outage longer than the horizon, which
// is every outage an offline queue exists for. The queue can only ever deliver an op that was
// enqueued less than a minute before the reconnect, which is not an offline queue; it is a
// sixty-second retry.
//
// AND IT CANNOT BE FIXED BY RE-SIGNING AT DRAIN TIME. PB-SEC-2 pins the biometric gate as
// PER-USE (`CryptoObject`) for revoke, kill switch, launch and kill -- exactly the actions
// ADR-007 D7 names as the queue's contents -- so re-authoring on reconnect requires the user
// present and consenting again. That is a prompt, not a queue, and it is a different product
// decision needing its own ADR.
//
// WHAT THIS TEST DOES NOT CLAIM. It says nothing about whether an offline queue is desirable.
// It pins that the one described by PB-NET-4 and ADR-007 D7, built out of the signed commands
// this system actually authors, is refused by this system's own daemon. Whichever way the
// amendment goes, this must keep holding or the numbers behind it have moved and the question
// must be re-derived.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// b42RegisteredPhone mints a phone keystore and registers it at full capability, so the only
// thing that can refuse the command below is its own expiry.
func b42RegisteredPhone(t *testing.T) (crypto.KeyStore, *device.Registry) {
	t.Helper()
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	reg, err := device.Open(t.TempDir())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := reg.Add(device.Record{
		DeviceID:       device.DeviceIDFor(ks.CommandSigningPublic()),
		Name:           "phone",
		NoiseStaticPub: make([]byte, 32),
		RelayAuthPub:   make([]byte, 32),
		CommandSignPub: ks.CommandSigningPublic(),
		RecipientPub:   make([]byte, 32),
		Capability:     device.CapFull,
		PairedAt:       time.Unix(1_700_000_000, 0),
		GrantedEpoch:   1,
	}); err != nil {
		t.Fatalf("registry add: %v", err)
	}
	return ks, reg
}

// TestB42PBNET4_AQueuedCommandDoesNotSurviveTheOutageItWouldBeQueuedFor.
//
// The op is a kill: one of the four ADR-007 D7 names as the queue's contents, idempotent by
// operation_id, and signed exactly as mobile.(*App).sealSignedCommand signs it -- at
// phonecore.CommandTTLFor(action), which is §6.0's horizon and not a number invented here.
func TestB42PBNET4_AQueuedCommandDoesNotSurviveTheOutageItWouldBeQueuedFor(t *testing.T) {
	ks, reg := b42RegisteredPhone(t)
	enqueuedAt := time.Unix(1_700_000_100, 0)
	horizon := phonecore.CommandTTLFor(protocol.ActionKill)

	cmd, err := phonecore.SignCommand(ks, phonecore.CommandInput{
		Action:      protocol.ActionKill,
		Machine:     "machine1",
		Session:     "machine1/sess1",
		OperationID: "op-queued-1",
		ExpiresAt:   enqueuedAt.Add(horizon),
	})
	if err != nil {
		t.Fatalf("phone SignCommand: %v", err)
	}

	// NON-VACUITY: the command is well-formed and this device may kill, so nothing but time
	// can refuse it. Without this the assertion below would pass over a broken fixture.
	if err := authorizeCommand(reg, enqueuedAt, cmd); err != nil {
		t.Fatalf("the command was refused at the instant it was authored: %v.\n"+
			"Every assertion below would then be measuring a broken fixture rather than expiry", err)
	}

	// THE OUTAGE. One second past the signed horizon -- the shortest outage for which an
	// offline queue is the answer rather than a retry.
	replayedAt := enqueuedAt.Add(horizon).Add(time.Second)
	err = authorizeCommand(reg, replayedAt, cmd)
	if err == nil {
		t.Fatalf("a command signed at %v and replayed %v later was ACCEPTED.\n"+
			"If that is now true, §6.0's signed-ExpiresAt horizon or the daemon's expiry check "+
			"has moved, and PB-NET-4's offline-queue clause must be re-derived against the new "+
			"numbers rather than inherited.", horizon, replayedAt.Sub(enqueuedAt))
	}
	if got := err.Error(); got != "command expired" {
		t.Fatalf("the replayed command was refused with %q, want \"command expired\"; the "+
			"refusal must be the EXPIRY, or this test is evidence about something else", got)
	}

	t.Logf("PB-NET-4 evidence: an ordinary op's signed horizon is %v (§6.0), and "+
		"internal/phonecore/opqueue.go states the command is signed ONCE at enqueue and never "+
		"re-signed on replay. An offline queue built from these commands therefore delivers "+
		"nothing for any outage longer than %v.", horizon, horizon)
}

// TestB42PBNET4_TakeControlIsNotAnExceptionEither closes the one op class §6.0 gives a longer
// horizon, so the finding cannot be read as "true except for the fifteen-minute one".
//
// take_control's 15 minutes exist so the SIGNATURE is not what ends a typing session -- and it
// is the op for which queuing is most obviously wrong anyway: a lease requested while offline
// and granted a quarter of an hour later is a control session nobody asked for, held over a
// terminal whose state has since changed.
func TestB42PBNET4_TakeControlIsNotAnExceptionEither(t *testing.T) {
	ks, reg := b42RegisteredPhone(t)
	enqueuedAt := time.Unix(1_700_000_100, 0)
	horizon := phonecore.CommandTTLFor(protocol.ActionTakeControl)

	if horizon <= phonecore.CommandTTLFor(protocol.ActionKill) {
		t.Fatalf("take_control's horizon (%v) is not longer than an ordinary command's (%v); "+
			"§6.0's exception has gone and this test no longer measures it",
			horizon, phonecore.CommandTTLFor(protocol.ActionKill))
	}
	cmd, err := phonecore.SignCommand(ks, phonecore.CommandInput{
		Action:      protocol.ActionTakeControl,
		Machine:     "machine1",
		Session:     "machine1/sess1",
		OperationID: "op-queued-2",
		ExpiresAt:   enqueuedAt.Add(horizon),
	})
	if err != nil {
		t.Fatalf("phone SignCommand: %v", err)
	}
	if err := authorizeCommand(reg, enqueuedAt, cmd); err != nil {
		t.Fatalf("the command was refused at the instant it was authored: %v", err)
	}
	if err := authorizeCommand(reg, enqueuedAt.Add(horizon).Add(time.Second), cmd); err == nil {
		t.Fatalf("a take_control signed at %v and replayed one second past it was accepted; "+
			"§6.0's horizon is not being enforced", horizon)
	}
}
