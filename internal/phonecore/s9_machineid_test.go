package phonecore

// Slice S9 (PB-NET-1): the machine endpoint id a fresh install never learns.
//
// OpenStore's machineID has always been a load-time FILTER and nothing else -- a blob whose
// `machine` field does not match is discarded wholesale, which is what keeps a re-pair after
// `swarm remote init` working. On a FRESH install there is no blob, so nothing ever set
// State.Machine, and nothing downstream sets it either: the pairing handshake carries no
// endpoint id (pairing.MachinePayload has no such field), so mobile.App.pin cannot supply one.
//
// The first consequence is loud and is fenced one layer up, in the facade's own conformance
// suite: crypto.Command.Canonical refuses an empty Machine, so every mutating verb fails.
//
// THE CONSEQUENCE FENCED HERE IS THE SILENT ONE. persistState writes Machine="" and the very
// next OpenStore compares that against the configured id and takes the "not ours" early
// return -- discarding the pairing, the epoch, the sealed content key, the relay cursor and
// the durable send-seq ceilings. None of that is recoverable: the machine coordinates cost a
// re-pair, and a send-seq ceiling that restarts at 1 under a retained epoch is stale-dropped
// by the gateway for good (PB-STATE-3). On Android a process death is routine, so the first
// one after pairing loses everything.
//
// Nothing caught it because every fixture in this package and in mobile/conformance SEEDS
// State.Machine, which is standing defect class (v) exactly: the whole fixture family seeds
// past the one value production never sets. These two tests are therefore written so that
// nothing may seed it -- the state they save is the state CUSTODY handed them, mutated the
// way pin() mutates it, and never a literal.

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// s9Machine is the endpoint id the install under test is configured for.
const s9Machine = "machine-endpoint-s9"

// s9PinCoordinates mutates st the way mobile.App.pin does: it takes the state custody handed
// out and writes the coordinates the pairing handshake authenticated onto it, plus the two
// replay-guard ceilings a first session moves. It deliberately does NOT touch st.Machine --
// pin() has nothing to set it from, and a helper that "helpfully" stamped it here would be
// the seeding fixture that hid this defect in the first place.
func s9PinCoordinates(st State) State {
	st.MachineStatic = bytes.Repeat([]byte{0xA1}, 32)
	st.MachineSignPub = bytes.Repeat([]byte{0xB2}, 32)
	st.MachineRelayAuthPub = bytes.Repeat([]byte{0xC3}, 32)
	st.OperatorNamespace = "owner"
	st.EpochID = 7
	st.Keys.ContentKey = crypto.ContentKey(bytes.Repeat([]byte{0xD4}, len(crypto.ContentKey{})))
	st.SendSeq = map[uint32]uint64{7: 512}
	st.RelayCursor = 17
	return st
}

// TestS9_AFreshInstallsFirstSaveSurvivesTheNextProcessStart is consequence B.
//
// Both cases end in one of OpenStore's two EARLY returns -- the file is absent, or it belongs
// to another machine -- which are exactly the two places the state is left holding whatever it
// was constructed with. If only one of them stamps the machine id, the other produces a phone
// that pairs, cannot author a single mutating command, and loses the pairing on the next
// launch: the "loads EMPTY, re-pair self-heals" path self-heals into the same brick.
func TestS9_AFreshInstallsFirstSaveSurvivesTheNextProcessStart(t *testing.T) {
	for _, tc := range []struct {
		name string
		// seed writes the blob this install starts from, before it opens the directory.
		seed func(t *testing.T, path string, wake, content Sealer)
	}{
		{
			name: "first run: no blob at all",
			seed: func(*testing.T, string, Sealer, Sealer) {},
		},
		{
			// `swarm remote init` regenerates the machine identity, so the phone re-pairs over
			// a directory still holding the OLD machine's blob. OpenStore discards it by
			// design; what must not survive that discard is an empty machine id.
			name: "re-pair after the machine identity was regenerated",
			seed: func(t *testing.T, path string, wake, content Sealer) {
				t.Helper()
				old, err := OpenStore(path, "the-machine-that-was", wake, content)
				if err != nil {
					t.Fatalf("seeding the previous machine's blob: %v", err)
				}
				if err := old.Save(s9PinCoordinates(old.Load())); err != nil {
					t.Fatalf("seeding the previous machine's blob: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), StateFileName)
			// ONE pair of tier KEKs for the whole test: they model the Android Keystore, which
			// outlives the process. A second s14aNewSealer would be a second KEK, and the
			// content key would then fail to open for a reason that has nothing to do with
			// this defect.
			wake, content := s14aNewSealer(t), s14aNewSealer(t)
			tc.seed(t, path, wake, content)

			store, err := OpenStore(path, s9Machine, wake, content)
			if err != nil {
				t.Fatalf("OpenStore on the install under test: %v", err)
			}
			if err := store.Save(s9PinCoordinates(store.Load())); err != nil {
				t.Fatalf("saving what the pairing pinned: %v", err)
			}

			// PROCESS DEATH. Android SIGKILLs the app; the next launch is a new OpenStore over
			// the same directory, with the same configured machine id and the same tier KEKs.
			// Nothing else is different, which is what makes a loss here silent.
			again, err := OpenStore(path, s9Machine, wake, content)
			if err != nil {
				t.Fatalf("OpenStore on the next process start: %v", err)
			}
			got := again.Load()

			if got.Machine != s9Machine {
				t.Errorf("after a restart the durable state names machine %q, want %q", got.Machine, s9Machine)
			}
			if got.EpochID != 7 {
				t.Fatalf("the durable blob did not survive the first process death: epoch is %d, "+
					"want 7. The blob was written with State.Machine=%q, so this OpenStore compared "+
					"it against %q and discarded the whole state -- pairing, epoch, content key, "+
					"relay cursor and send-seq ceilings. The phone is unpaired and nothing said so.",
					got.EpochID, "", s9Machine)
			}
			if !bytes.Equal(got.MachineRelayAuthPub, bytes.Repeat([]byte{0xC3}, 32)) {
				t.Errorf("the machine's relay-auth pub did not survive the restart (%x); it is the "+
					"only coordinate that says how to REACH the machine, so losing it costs a re-pair",
					got.MachineRelayAuthPub)
			}
			if !bytes.Equal(got.MachineStatic, bytes.Repeat([]byte{0xA1}, 32)) ||
				!bytes.Equal(got.MachineSignPub, bytes.Repeat([]byte{0xB2}, 32)) {
				t.Errorf("the pinned machine identity did not survive the restart: static=%x sign=%x",
					got.MachineStatic, got.MachineSignPub)
			}
			if want := crypto.ContentKey(bytes.Repeat([]byte{0xD4}, len(crypto.ContentKey{}))); got.Keys.ContentKey != want {
				t.Errorf("the sealed content key did not survive the restart; the phone can open "+
					"nothing the machine sends under epoch %d", got.EpochID)
			}
			if got.SendSeq[7] != 512 {
				t.Errorf("the durable send-seq ceiling for epoch 7 is %d after the restart, want 512. "+
					"A ceiling that restarts below the gateway's high-water is stale-dropped for good "+
					"(PB-STATE-3), so this is the coordinate whose loss is unrecoverable", got.SendSeq[7])
			}
			if got.RelayCursor != 17 {
				t.Errorf("the relay read cursor is %d after the restart, want 17; the phone re-reads "+
					"every frame the relay still retains", got.RelayCursor)
			}
		})
	}
}

// TestS9_OpenStoreStampsTheMachineItWasOpenedFor pins the mechanism the test above depends on,
// including the two cases that must NOT change.
func TestS9_OpenStoreStampsTheMachineItWasOpenedFor(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)

	// (a) First run: no blob. The state describes the machine this install is configured for,
	// so the first Save is not self-discarding.
	fresh, err := OpenStore(filepath.Join(dir, "absent.json"), "m1", wake, content)
	if err != nil {
		t.Fatalf("OpenStore on a missing file: %v", err)
	}
	if got := fresh.Load(); got.Machine != "m1" {
		t.Errorf("a first-run store names machine %q, want %q", got.Machine, "m1")
	}

	// (b) Another machine's blob still loads EMPTY -- that is what keeps a re-pair working --
	// but empty means "no coordinates", never "no machine".
	foreign := filepath.Join(dir, "foreign.json")
	seed, err := OpenStore(foreign, "m9", wake, content)
	if err != nil {
		t.Fatalf("OpenStore to seed a foreign blob: %v", err)
	}
	if err := seed.Save(s9PinCoordinates(seed.Load())); err != nil {
		t.Fatalf("seeding a foreign blob: %v", err)
	}
	reopened, err := OpenStore(foreign, "m1", wake, content)
	if err != nil {
		t.Fatalf("OpenStore for a different machine: %v", err)
	}
	got := reopened.Load()
	if got.Machine != "m1" {
		t.Errorf("a store opened over another machine's blob names machine %q, want %q", got.Machine, "m1")
	}
	if got.EpochID != 0 || len(got.SendSeq) != 0 || len(got.MachineRelayAuthPub) != 0 {
		t.Errorf("another machine's coordinates leaked into this install: %+v; its epoch-1 "+
			"high-water would stale-drop a freshly paired phone", got)
	}

	// (c) An EMPTY machineID is an unpaired caller with no expectation: it adopts whatever the
	// blob describes and must not invent an identity of its own.
	anon, err := OpenStore(filepath.Join(dir, "anon.json"), "", wake, content)
	if err != nil {
		t.Fatalf("OpenStore with no machine id: %v", err)
	}
	if got := anon.Load(); got.Machine != "" {
		t.Errorf("a store opened with no machine id named %q; a caller with no expectation "+
			"adopts the blob rather than stamping one", got.Machine)
	}

	// (d) The purely IN-MEMORY store (empty path) is the same contract. It has no blob to
	// filter, but a caller reading State.Machine off it must not see an empty one either.
	mem, err := OpenStore("", "m1", nil, nil)
	if err != nil {
		t.Fatalf("OpenStore with no path: %v", err)
	}
	if got := mem.Load(); got.Machine != "m1" {
		t.Errorf("an in-memory store names machine %q, want %q", got.Machine, "m1")
	}
}
