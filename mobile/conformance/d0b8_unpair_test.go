package conformance_test

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-d0b8, at the seam the Android screens
// actually read: `StateSummary`.
//
// THE GATE IS ONE FIELD AWAY FROM THE PURGE AND WAS NEVER CONNECTED TO IT.
// `PhoneSurface.renderReady` asks `PairOnlyScreen.presentationOf` whether this handset is shown
// the app at all, and the fact it reads comes from `App.StateSummary`. The purge behind
// "Replace this computer" destroys both key tiers and every decrypted cache and leaves that fact
// exactly as it was, so the summary goes on reporting a paired phone -- in the four-tab scaffold,
// with the settings screen still naming the machine, and with the pairing entry point on a screen
// the gate will not show. `internal/phonecore` holds the durable half of this; here it is the
// PRODUCT fact, read the way a screen reads it, across the process death that makes it a brick
// rather than a redraw bug.

import (
	"testing"

	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// TestD0B8_TheSummaryReportsAnUnpairedPhoneAfterARevoke.
//
// WHY `Paired` IS ITS OWN FIELD AND NOT AN EMPTIED `Machine`. The machine endpoint id is a
// coordinate: `phonecore.OpenStore` filters the durable blob on it and every mutating verb signs
// over it, so it has to keep saying what it says. Whether the phone is USABLY paired is a
// different question that no existing field answers -- `Machine` says a pairing once happened,
// `Restored` says the route to the machine was pinned, and neither notices a revoke. Reading a
// name and inferring a state is what put the defect here in the first place, so the fact the gate
// turns on is stated rather than inferred.
func TestD0B8_TheSummaryReportsAnUnpairedPhoneAfterARevoke(t *testing.T) {
	h := newHarness(t)

	before, err := h.App.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary on the paired phone: %v", err)
	}
	if !before.Paired {
		t.Fatal("the seeded phone does not read as paired, so this test cannot tell a revoke from " +
			"the state it started in")
	}

	// The whole of what "Replace this computer" does locally: the command is sent (and may well
	// not arrive -- the situation the control exists for is a phone that cannot reach its
	// machine), and the purge runs regardless, in a `finally`.
	if err := h.App.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	after, err := h.App.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary after the revoke: %v", err)
	}
	if after.Paired {
		t.Error("agents-tracker-d0b8: the phone still reports itself as paired after the revoke that " +
			"destroyed both key tiers. The presentation gate reads this field, so the handset stays in " +
			"the four-tab scaffold -- and the only screen that could pair it again is the one that gate " +
			"is now refusing to show. There is no way back short of clearing the app's data")
	}
	if after.Machine == "" {
		t.Error("agents-tracker-d0b8: the revoke emptied the machine endpoint id. That is the coordinate " +
			"OpenStore's load-time filter tests the durable blob against, and the one every mutating verb " +
			"signs over -- clearing it is S9, reopened by the remedy for a different bug")
	}
}

// TestD0B8_TheUnpairSurvivesTheProcessDeathThatMakesItABrick.
//
// THIS IS THE CASE, not a hardening of it. Android SIGKILLs the app as routine behaviour, and the
// screen that issued the revoke is the last thing the user saw before backgrounding it. A local
// unpair held in memory is one the next launch does not have -- so the handset comes back paired
// to a machine that deregistered it, holding no key of either tier, and the four-tab shell it
// opens on reads a roster nothing will ever fill.
//
// THE RESTART IS A SECOND `NewApp` OVER THE SAME DIRECTORY, which is what a process death is here,
// and it uses the same custody: a different one would be a phone that cannot open its own blob,
// which proves something else.
func TestD0B8_TheUnpairSurvivesTheProcessDeathThatMakesItABrick(t *testing.T) {
	h := newHarness(t)
	if err := h.App.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	restarted, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir: h.Dir, RelayURL: h.RelayURL, MachineID: h.Machine,
	}, h.Custody)
	if err != nil {
		t.Fatalf("the revoked phone could not be reopened after a process death: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })

	sum, err := restarted.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary after the process death: %v", err)
	}
	if sum.Paired {
		t.Error("agents-tracker-d0b8: the next process comes up believing the phone is paired. The " +
			"revoke reached the keys and not the durable record of the registration, so the brick is " +
			"one SIGKILL away from every revoke")
	}
	// Non-vacuity: the blob was READ, not discarded. `OpenStore` loads EMPTY when the stamped
	// machine is not the caller's, and an empty state would answer "not paired" for the wrong
	// reason -- with the epoch, the watermarks and the relay cursor gone with it.
	if sum.EpochID == 0 {
		t.Error("agents-tracker-d0b8: the reopened phone holds no epoch, so its durable blob was " +
			"discarded at load rather than read. That is S9 -- the unpair must be a coordinate the " +
			"state carries, not the absence of the one it is filtered on")
	}
}
