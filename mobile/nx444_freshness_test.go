package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nx44.4(b): a re-pair re-arms every
// epoch-scoped coordinate in App.pin EXCEPT the freshness clock, so the new registration
// inherits the old one's answer to "when did I last hear from my machine?".
//
// WHY IT MATTERS, in the terms PB-APP-11 is written in. State.LastHeardAt is the newest
// AAD-covered IssuedAt this phone has ACCEPTED, it is DURABLE, and Core.MachineSilentAt reads
// nothing else. So a handset that pairs to a machine it has never heard a frame from starts
// life inside the 5-minute freshness budget, on a coordinate contributed by the PREVIOUS
// registration -- and every restored cache is presented as live on the strength of it. That is
// the exact presentation the requirement exists to forbid, reached without the relay having to
// withhold anything.
//
// AND THE LOAD-TIME FILTER DOES NOT COVER IT. phonecore's discard of a blob stamped with
// another machine is guarded by a non-empty machine id, which Android never passes
// (PhoneRuntime builds its Config without one), so on the platform this ships to nothing else
// clears the coordinate at all.
//
// pin already clears Disowned and, on an epoch change, Keys. This is the third coordinate that
// belongs to the registration rather than to the device, and it is cleared UNCONDITIONALLY:
// re-pairing to the SAME machine in the SAME epoch is still a phone that has heard nothing
// since, and a clock that only reset on a change of machine would be a clock the one case the
// filter cannot see never resets.

import (
	"crypto/ed25519"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// freshnessApp is an App over an in-memory core: enough to run pin and read the state back,
// with no relay, no store and no transport generation to re-arm.
func freshnessApp(t *testing.T) *App {
	t.Helper()
	core, err := phonecore.Resume(phonecore.Config{})
	if err != nil {
		t.Fatalf("phonecore.Resume: %v", err)
	}
	a := &App{core: core, events: newDispatcher(), needs: map[string]string{}}
	t.Cleanup(a.events.close)
	return a
}

// pairedOutcome is a completed handshake's outcome for machine, in the epoch given.
func pairedOutcome(machine string, epoch uint32) *pairing.DeviceOutcome {
	pub, _, _ := ed25519.GenerateKey(nil)
	return &pairing.DeviceOutcome{
		MachineStatic: []byte(machine + "-static"),
		Machine: pairing.MachinePayload{
			Hostname:            machine,
			OperatorNamespace:   "owner",
			MachineEndpointID:   machine,
			MachineRelayAuthPub: pub,
			MachineSignPub:      pub,
			EpochID:             epoch,
		},
	}
}

// TestNx444_PairingResetsTheFreshnessClock is the defect. The phone has heard from its old
// machine; it is then paired to another one, and must not claim to have heard from THAT one.
func TestNx444_PairingResetsTheFreshnessClock(t *testing.T) {
	a := freshnessApp(t)

	// What a live registration leaves behind: a freshness coordinate inside the budget.
	const heard = int64(1_754_000_000_000)
	if err := a.core.Mutate(func(st *phonecore.State) {
		st.Machine = "old-machine"
		st.LastHeardAt = heard
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	if err := a.pin(pairedOutcome("new-machine", 7)); err != nil {
		t.Fatalf("pin: %v", err)
	}

	st := a.core.State()
	if st.Machine != "new-machine" {
		t.Fatalf("pin did not pin the new machine (Machine = %q); the fixture is measuring nothing", st.Machine)
	}
	if st.LastHeardAt != 0 {
		t.Errorf("State.LastHeardAt = %d after pairing, want 0. The phone has never heard a frame "+
			"from the machine it has just paired to, and PB-APP-11's budget is measured against "+
			"nothing else -- so every restored cache is presented as LIVE on a coordinate the "+
			"PREVIOUS registration contributed. Core.LastHeard() answers %v.", st.LastHeardAt, a.core.LastHeard())
	}
	// The reset is the coordinate's alone: it must not take the pairing with it.
	if st.Disowned {
		t.Errorf("State.Disowned = true after pairing; the pairing is what clears it")
	}
}

// TestNx444_TheFreshnessResetIsUnconditional: a re-pair to the SAME machine in the SAME epoch
// is the case no filter anywhere can see, and it is a phone that has heard nothing since
// whatever silence sent the owner back to the pairing screen.
func TestNx444_TheFreshnessResetIsUnconditional(t *testing.T) {
	a := freshnessApp(t)
	if err := a.core.Mutate(func(st *phonecore.State) {
		st.Machine = "same-machine"
		st.EpochID = 3
		st.LastHeardAt = 1_754_000_000_000
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	if err := a.pin(pairedOutcome("same-machine", 3)); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if got := a.core.State().LastHeardAt; got != 0 {
		t.Errorf("State.LastHeardAt = %d after re-pairing the same machine in the same epoch, "+
			"want 0. A reset conditioned on a change of machine or epoch would never fire for "+
			"the one population that reaches this screen most often", got)
	}
}
