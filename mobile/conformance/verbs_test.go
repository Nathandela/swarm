package conformance_test

// FAILING-FIRST (TDD RED, GG-5) guards for the three ways a facade method can look like
// it worked and not have: it can send the WRONG verb (release-control sealing a delete),
// it can send a verb no hop can resolve and hold the op open forever, or it can leave a
// gate open across an epoch change. None of these is visible to a caller -- every one of
// them returns a nil error -- so each needs an assertion about what actually crossed the
// wire, not about what the call returned.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"

	swarmmobile "github.com/Nathandela/swarm/mobile"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// TestS8_ReleaseControlEndsTheLeaseAndNeverDeletesTheSession.
//
// screen_coverage.tsv row take_control.release names the verb: take_control_end. The
// gateway routes it on the lease plane (command_loop.go routeCommand, protocol.
// OpTakeControlEnd -> Leases.End) and never forwards it to the daemon. ActionDelete is
// forwarded (opForAction -> protocol.OpDelete -> handleDelete -> Daemon.Delete), and
// internal/skeleton/deviceauth.go classes delete as ActionControl -- the tier a phone
// that can take control at all already holds. So a release-control button sealing a
// delete is fully authorized and DESTROYS the agent session the user was merely stepping
// away from, appending a tombstone.
//
// The negative half is the point: it is not enough that take_control_end arrives, no
// delete may EVER be sealed by this path.
func TestS8_ReleaseControlEndsTheLeaseAndNeverDeletesTheSession(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})

	if _, err := h.App.TakeControl(testSession); err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	h.AwaitCommand(protocol.ActionTakeControl)

	if _, err := h.App.ReleaseControl(testSession); err != nil {
		t.Fatalf("ReleaseControl: %v", err)
	}
	got := h.AwaitCommand(protocol.OpTakeControlEnd)
	if got.Session != testSession {
		t.Errorf("take_control_end targeted %q, want %q -- the lease teardown carries its OWN "+
			"target session, so the gateway never routes it by mutable focus state", got.Session, testSession)
	}
	if h.sawCommand(protocol.ActionDelete) {
		t.Fatalf("release-control sealed a %q command. It is forwarded to the daemon's Delete and a "+
			"control-tier phone -- the only kind that can take control at all -- is authorized for it, "+
			"so tapping release DESTROYS the session", protocol.ActionDelete)
	}
}

// TestS8_SurfacesWithNoWireVerbFailVisiblyAndLeakNoPendingOps.
//
// ONE element has no device-reachable wire verb (screen_coverage.tsv says so): device_revoke
// IS in the signed action set and the daemon serves it, but remotegw's opForAction refuses it
// one hop short of the daemon, and a refused action seals no reply.
//
// TWO CASES LEFT THIS TEST WITH SLICE S16 AND THE ASSERTIONS DID NOT MOVE. interrupt and
// push_preference were both verb-less when this was written and are now wired: an interrupt is
// a keystroke on the live input plane (PB-APP-3, gated on a confirmed lease and therefore no
// longer a refusal at all), and SetPushPreference seals the signed push_prefs command S12
// shipped. Their behaviour is asserted far more strongly by the S16 conformance suite
// (TestPBAPP3_* and TestPBAPP7_*); keeping them here would assert they are still BROKEN.
// device_revoke stays, because its gap is real and unchanged.
//
// The alternatives to a refusal are worse, which is why the shape is pinned:
//
//   - an op handed to issue() that no reply can ever resolve raises PendingOpCount for
//     the life of the process, which makes every REAL pending op invisible;
//   - a device_revoke that is sealed and appended burns a durable send-seq and returns
//     nil, so the phone's declared panic action (PB-SEC-7) is a silent no-op dressed up
//     as success.
func TestS8_SurfacesWithNoWireVerbFailVisiblyAndLeakNoPendingOps(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})

	before, err := h.App.PendingOpCount()
	if err != nil {
		t.Fatalf("PendingOpCount: %v", err)
	}

	cases := []struct {
		name string
		call func() (*swarmmobile.Op, error)
	}{
		{"RevokeThisDevice", func() (*swarmmobile.Op, error) { return h.App.RevokeThisDevice() }},
	}
	for _, c := range cases {
		op, err := c.call()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if op == nil || op.OperationID == "" {
			t.Fatalf("%s returned no operation id, so its verdict is unclaimable", c.name)
		}
		out, err := h.App.Outcome(op.OperationID)
		if err != nil {
			t.Fatalf("%s: Outcome: %v", c.name, err)
		}
		if !out.Resolved {
			t.Errorf("%s returned an op no wire verb can ever resolve, and it stays UNRESOLVED. "+
				"The screen shows a button that hangs forever instead of a legible refusal; "+
				"Interrupt is the model", c.name)
		}
		if out.Message == "" {
			t.Errorf("%s resolved with no message; the refusal must say WHY, or the app can only "+
				"report a bare failure", c.name)
		}
	}

	if h.sawCommand(protocol.ActionDeviceRevoke) {
		t.Errorf("RevokeThisDevice sealed and appended a %q the gateway refuses (opForAction has no "+
			"mapping for it), burning a durable send-seq on a command that can never be delivered",
			protocol.ActionDeviceRevoke)
	}

	after, err := h.App.PendingOpCount()
	if err != nil {
		t.Fatalf("PendingOpCount: %v", err)
	}
	if after != before {
		t.Errorf("PendingOpCount went %d -> %d over three calls whose ops no reply can ever "+
			"resolve. A counter that only rises makes every real in-flight op invisible", before, after)
	}
}

// TestS8_RepairIntoANewEpochReArmsTheFailClosedGates.
//
// pin() writes the new epoch id and nothing else: it leaves the live App's reconciled
// flag set and leaves the PREVIOUS epoch's content key in durable state. So after a
// re-pair the phone keeps permitting mutating ops against rollback authorities adopted
// for an epoch that no longer exists, and keeps sealing frames under the old key while
// labelling them with the new epoch. It fails closed on the next process start (NewApp
// compares ReconciledEpoch against EpochID) -- which is exactly why the live window is
// invisible: on Android that process can live for hours.
//
// Both halves are the same root cause -- epoch-scoped state surviving an epoch change --
// so both are asserted here.
func TestS8_RepairIntoANewEpochReArmsTheFailClosedGates(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})
	if _, err := h.App.Kill(testSession); err != nil {
		t.Fatalf("a mutating op before the re-pair: %v", err)
	}

	newEpoch := h.EpochID + 1
	p, err := h.App.BeginPairing(h.pairMachineAtEpoch(t, newEpoch))
	if err != nil {
		t.Fatalf("BeginPairing: %v", err)
	}
	// PB-PAIR-6 (S16): the destination is confirmed before anything is joined.
	s16PassOriginGate(t, p)
	eventually(t, "the phone never derived a SAS", func() bool {
		s, err := p.SAS()
		return err == nil && s != ""
	})
	if err := p.Confirm(); err != nil {
		t.Fatalf("Pairing.Confirm: %v", err)
	}
	eventually(t, "the re-pair never reached its terminal paired state", func() bool {
		st, err := p.State()
		return err == nil && st == "paired"
	})

	sum, err := h.App.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary: %v", err)
	}
	if sum.EpochID != int64(newEpoch) {
		t.Fatalf("the re-pair did not adopt the new epoch: EpochID = %d, want %d", sum.EpochID, newEpoch)
	}
	if sum.Reconciled {
		t.Errorf("the phone still reports Reconciled after re-pairing into epoch %d. The adopted "+
			"authorities belong to epoch %d and cannot bound anything in this one, yet every mutating "+
			"op is permitted until the process happens to die (PB-SYNC-7)", newEpoch, h.EpochID)
	}
	if _, err := h.App.Kill(testSession); err == nil {
		t.Errorf("a mutating op is still permitted after a re-pair into a new epoch; the fail-closed " +
			"refusal must be re-armed until the machine republishes its authorities")
	}
	if err := h.App.SendInput(testSession, []byte("x")); err == nil {
		t.Errorf("the phone still seals input after a re-pair: durable state kept epoch %d's content "+
			"key while advancing EpochID to %d, so every frame is labelled with an epoch whose key it "+
			"was not sealed under", h.EpochID, newEpoch)
	}
}

// ---- helpers -----------------------------------------------------------------

// sawCommand reports whether a command with the given action has EVER reached the
// machine. It is the negative half of AwaitCommand: proving a verb arrived says nothing
// about the destructive one that may have arrived with it.
func (h *harness) sawCommand(action string) bool {
	h.t.Helper()
	h.Drain()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.Commands {
		if c.Action == action {
			return true
		}
	}
	return false
}

// pairMachineAtEpoch runs an auto-confirming machine-side responder that publishes a
// DIFFERENT epoch id and returns the QR the phone should scan. It is deliberately not
// runMachinePairing: that helper pairs into the harness's CURRENT epoch, and an epoch
// that never changes cannot show whether epoch-scoped state is re-armed. The machine's
// real relay-auth pub is published so the re-paired phone still points at this harness's
// machine and a send that should fail fails for the RIGHT reason.
func (h *harness) pairMachineAtEpoch(t *testing.T, epoch uint32) string {
	t.Helper()

	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	var secret [32]byte
	var rid [16]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := rand.Read(rid[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}

	m := pairing.NewMachine(pairing.MachineParams{
		Static:       machineID.NoiseStatic(),
		Secret:       secret,
		RendezvousID: rid,
		LocalConsole: true,
		Confirm:      func(context.Context, [6]string, string) (bool, error) { return true, nil },
		Payload: pairing.MachinePayload{
			Hostname:            "repair.local",
			MachineRoutingID:    []byte(relay.RoutingID(h.machineRelayAuthPub)),
			MachineRelayAuthPub: h.machineRelayAuthPub,
			RecipientPub:        machineID.RecipientPublic(),
			MachineSignPub:      h.machineSignPub,
			EpochID:             epoch,
		},
	})

	conn, err := relay.DialRaw(h.ctx, h.RelayURL)
	if err != nil {
		t.Fatalf("machine DialRaw: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() { _, _ = m.Pair(h.ctx, &relayRendezvous{conn: conn, label: hex.EncodeToString(rid[:])}) }()

	qr, err := pairing.EncodeQR(pairing.QRPayload{
		RelayURL:      h.RelayURL,
		RendezvousID:  rid,
		PairingSecret: secret,
	})
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}
	return qr
}
