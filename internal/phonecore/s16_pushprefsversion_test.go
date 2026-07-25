package phonecore

// Slice S16 -- the durable half of PB-APP-7 / PB-PUSH-10.
//
// WHY THIS TEST EXISTS SEPARATELY FROM THE PINNED FIXTURES. PushPreference.Version is a new
// DURABLE field, and the mechanism that normally catches one -- a top-level json tag missing
// from the pinned literal for the current schema version -- cannot see it: the counter lives
// inside the existing push_preference object, so the tag set is unchanged. Nor can a fixture
// carry a non-zero counter, because every pinned literal from v4 on must restore the SAME
// fullState(), and giving fullState() a non-zero version would break the v4 and v5 blobs that
// predate the field.
//
// So the round trip is asserted directly. The defect it guards is invisible from every screen:
// the machine refuses any push_prefs whose Version does not STRICTLY exceed the stored one
// (remotegw.filePushPrefs.SavePrefs, because the relay may replay a frame from before the user
// turned pushes off), so a counter that came back as zero after the process death Android hands
// out routinely means every toggle from that moment on is refused forever, while the settings
// screen goes on showing the value the user chose.

import (
	"path/filepath"
	"testing"
)

func TestStateStore_PushPreferenceVersionSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	kek := &s14aSealer{kek: stateV4FixtureKEK}

	store, err := OpenStore(path, "m1", kek, kek)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	st := store.Load()
	st.PushPreference = PushPreference{Alerts: true, Mentions: false, Version: 42}
	if err := store.Save(st); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A SECOND OpenStore over the same path, which is what a relaunch does.
	reopened, err := OpenStore(path, "m1", kek, kek)
	if err != nil {
		t.Fatalf("OpenStore after the restart: %v", err)
	}
	got := reopened.Load().PushPreference
	if got.Version != 42 {
		t.Errorf("PB-PUSH-10: the push-preference version came back as %d after a restart, want 42. "+
			"The machine refuses any update that does not strictly advance, so a counter that "+
			"restarts is a settings screen the machine has stopped listening to -- with no symptom "+
			"on either side", got.Version)
	}
	if got.Alerts != true || got.Mentions != false {
		t.Errorf("the toggles came back as %+v, want {Alerts:true Mentions:false}", got)
	}
}
