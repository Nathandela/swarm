package conformance_test

// FAILING-FIRST (TDD RED, GG-5) test for the PHONE half of slice S19's first production hole.
//
// THE HOLE. A handset has no configured machine id -- PhoneRuntime.construct passes "" -- and
// until now nothing else supplied one either: pairing.MachinePayload carried no endpoint id, so
// mobile.App.pin had nothing to write. Every mutating verb signs a tuple carrying it and
// crypto.Command.Canonical REFUSES an empty Machine, so a phone that completed a real pairing
// could author nothing: launch, take_control, kill and the revoke panic button all fail before
// a byte is sealed.
//
// WHY NO SHIPPED TEST COULD HAVE CAUGHT IT, which is the finding rather than an aside. Every
// fixture in this suite hands the phone the machine id it is about to assert:
// s10FreshInstall and TestPBNET1_AFreshInstallsPairingSurvivesTheNextProcessStart both open
// the App with `MachineID: testMachineID`, and the conformance harness additionally seeds
// State.Machine into the store. Those tests then verify the id survives a restart -- which it
// does, because the CONFIG supplied it on every open. The value they check is the value they
// provided, so the production question ("where does a handset get this?") was never asked.
//
// THIS TEST OPENS WITH MachineID: "" AND NEVER SEEDS THE STORE. That single difference is what
// makes it a statement about production: the only path left for the id is the pairing.

import (
	"testing"

	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// TestS19_ARealPairingTeachesTheHandsetWhichMachineItPairedWith.
//
// It asserts across a RESTART rather than only in memory. The id has to be DURABLE: Android
// kills the process routinely, and an id held only by the live App would leave every verb
// refused after the first process death, with the phone still showing itself as paired.
func TestS19_ARealPairingTeachesTheHandsetWhichMachineItPairedWith(t *testing.T) {
	ctx, relayURL, dir, _ := s10FreshInstall(t)

	// A HANDSET's constructor: no machine id, because there is nowhere for the Android app to
	// have learned one before the pairing that teaches it. Deliberately not s10FreshInstall's
	// opener, which passes testMachineID -- that parameter is the fixture this hole hid behind.
	custody := newTestCustody(t)
	open := func() *swarmmobile.App {
		t.Helper()
		app, err := swarmmobile.NewApp(&swarmmobile.Config{
			StateDir: dir, RelayURL: relayURL, MachineID: "",
		}, custody)
		if err != nil {
			t.Fatalf("swarmmobile.NewApp: %v", err)
		}
		t.Cleanup(func() { _ = app.Close() })
		if err := app.Start(); err != nil {
			t.Fatalf("App.Start: %v", err)
		}
		return app
	}

	app := open()

	// NON-VACUITY: nothing may know the machine before the handshake runs. Without this, a
	// build that stamped the id at load time from any source at all would satisfy the
	// assertion below with the pairing deleted.
	sum, err := app.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary on a fresh install: %v", err)
	}
	if sum.Machine != "" {
		t.Fatalf("an unpaired handset already names machine %q; the assertion below would then "+
			"hold for a phone that never paired", sum.Machine)
	}

	m := newS10Machine(t, ctx, relayURL)
	runPairing(t, app, m)

	if sum, err = app.StateSummary(); err != nil {
		t.Fatalf("StateSummary after the pairing: %v", err)
	}
	if sum.Machine != s10MachineEndpointID {
		t.Fatalf("after a completed pairing the phone names machine %q, want %q. The endpoint id "+
			"is signed into every mutating command and crypto.Command.Canonical refuses an empty "+
			"one, so this phone is paired and mute: launch, kill, take_control and the revoke "+
			"panic button all fail before a byte is sealed", sum.Machine, s10MachineEndpointID)
	}

	// The restart. The state directory outlives the App, so this is a statement about disk.
	if err := app.Close(); err != nil {
		t.Fatalf("App.Close before the restart: %v", err)
	}
	restarted := open()
	if sum, err = restarted.StateSummary(); err != nil {
		t.Fatalf("StateSummary after the restart: %v", err)
	}
	if sum.Machine != s10MachineEndpointID {
		t.Fatalf("after a process death the phone names machine %q, want %q. It reached durable "+
			"state or it did not; a handset that must re-pair after every Android process kill "+
			"to name its machine is the same brick one restart later", sum.Machine, s10MachineEndpointID)
	}
}
