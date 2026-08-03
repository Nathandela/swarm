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
//
// THERE IS MORE THAN ONE WAY TO END A REGISTRATION, and the first version of this file covered
// one of them. The acceptance criterion is that NO path leaves the phone unable to reach the
// pairing screen, so the paths are enumerated here and each has a case below:
//
//	1. THE PHONE'S OWN Replace this computer. App.PurgeKeys runs whether or not the command
//	   reached the machine -- the situation the control exists for is a phone that cannot reach
//	   it -- and records the unpair durably. Covered by the first two cases.
//	2. THE OWNER'S `swarm remote revoke <device-id>`, run on the Mac. This is the documented,
//	   supported way to remove a device and the one the machine-side runbook names, and NOTHING
//	   on the phone purges for it: the phone learns only when the relay refuses its next
//	   handshake with relay.ErrRevoked. Covered by TestD0B8_AnOwnerSideRevokeAlsoUnpairsThePhone,
//	   which drives a REAL DeviceRevoke through the real relay.
//	3. THE HANDSET'S OWN relay-auth key destroyed (PB-KEY-6). Nothing on the device recovers it
//	   and the remedy is the same -- pair again. Covered by
//	   TestD0B8_ADestroyedRelayAuthKeyAlsoUnpairsThePhone.
//
// Paths 2 and 3 are why the durable flag alone is not the fix. They are also why the transport
// reading is NOT durable: see App.StateSummary for the trust argument, and
// TestD0B8_AnOwnerSideRevokeAlsoUnpairsThePhone's own comment for what a re-pair must still be
// able to undo. PB-STATE-10's window -- the one reading of a terminal state that must NOT unpair
// the phone -- is asserted next door in ../d0b8_transport_test.go, at the decision itself: it is
// a race this harness cannot lose on demand, and a total function of two values.

import (
	swarmmobile "github.com/Nathandela/swarm/mobile"
	"testing"
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

// TestD0B8_AnOwnerSideRevokeAlsoUnpairsThePhone is path 2, and it is the one a real owner takes.
//
// `swarm remote revoke <device-id>` on the Mac is the documented way to remove a device, and it
// is the ONLY mitigation ADR-007 B133 leaves for a lost handset. Nothing on the phone runs for
// it: App.PurgeKeys has exactly one production caller and it is the Settings press. So the
// durable unpair flag stays false, every pinned coordinate stays exactly where it was, and the
// phone learns what happened from one place only -- the relay refusing its next handshake.
//
// The revoke here is REAL: relay.Client.DeviceRevoke against the real relay.Server the harness
// runs, issued over the machine's own connection, which is the same call `swarm remote revoke`
// makes. Nothing is simulated on the phone's side of the wire.
func TestD0B8_AnOwnerSideRevokeAlsoUnpairsThePhone(t *testing.T) {
	h := newHarness(t)

	before, err := h.App.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary on the paired phone: %v", err)
	}
	if !before.Paired {
		t.Fatal("the seeded phone does not read as paired, so this test cannot tell a revoke from " +
			"the state it started in")
	}

	if err := h.machineRelay.DeviceRevoke(h.ctx, h.phoneTarget); err != nil {
		t.Fatalf("the owner's revoke, issued from the machine: %v", err)
	}
	// A backgrounding and a return, which is what ADR-007 B16 makes the ordinary shape of a
	// phone's connection anyway: the next dial is the moment the relay can tell it.
	if err := h.App.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := h.App.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	eventually(t, "the phone never observed the revoke on its next handshake", func() bool {
		state, err := h.App.ConnectionState()
		return err == nil && state == "revoked"
	})

	sum, err := h.App.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary after the owner's revoke: %v", err)
	}
	if sum.Paired {
		t.Error("agents-tracker-d0b8: the phone still reports itself as paired after the OWNER revoked " +
			"it from the machine. Nothing on this handset purges for that path, so a fix that only " +
			"watches the local Replace press covers the half of the population that never happens: " +
			"the shipped banner already tells this user to pair again, and the gate is what decides " +
			"whether they can")
	}
	// THE RE-PAIR MUST STILL BE POSSIBLE, which is why this reading is not written down. The
	// phone has purged nothing and disowned nothing -- it has been REFUSED, by a party this
	// design does not trust for anything else -- so every coordinate a pairing pinned is still
	// here and a relay that answers differently tomorrow is answered differently.
	if sum.Machine == "" || sum.EpochID == 0 {
		t.Error("agents-tracker-d0b8: the transport reading destroyed durable coordinates. A relay's " +
			"refusal is not the owner acting, and PB-STATE-10 records that a terminal revoked verdict " +
			"can be made stale by a pairing -- one written to disk could not be")
	}
}

// TestD0B8_ADestroyedRelayAuthKeyAlsoUnpairsThePhone is path 3 (PB-KEY-6).
//
// The relay-auth key is what signs the handshake, and a destroyed Keystore entry is not something
// any authentication brings back -- ADR-007 B133 removed the prompt that used to be offered for
// it. So the state is terminal, its shipped banner says "Pair this device again", and until now
// the app had no way to carry that out. It shares the gate with the revoke for that reason and
// not because the two causes are the same: they are kept apart everywhere else.
func TestD0B8_ADestroyedRelayAuthKeyAlsoUnpairsThePhone(t *testing.T) {
	h := newHarness(t)

	// The WAKE tier is what SignRelayAuth unseals, so this is the tier a destroyed key shows up
	// in first -- one dial, one unwrap, one refusal.
	h.Custody.Refuse("wake", swarmmobile.KeyCustodyKeyInvalidated)
	if err := h.App.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := h.App.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	eventually(t, "the phone never reached repair_required", func() bool {
		state, err := h.App.ConnectionState()
		return err == nil && state == "repair_required"
	})

	sum, err := h.App.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary after the key was destroyed: %v", err)
	}
	if sum.Paired {
		t.Error("agents-tracker-d0b8: a phone whose relay-auth key is destroyed still reports itself " +
			"as paired. Its own error banner says to pair this device again, and the gate is what " +
			"decides whether the screen that does so can be reached -- so the product is instructing " +
			"a recovery it has removed")
	}
}
