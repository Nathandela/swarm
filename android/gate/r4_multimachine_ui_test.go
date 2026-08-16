package gate

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 deliverable 3's Android half (bead
// agents-tracker-hggx.5): the add/switch/remove computer UX and the global inbox,
// SCREEN-MODEL FIRST -- "every affordance nameable by the screen model, tested from
// first-run resolver state, Obsidian design system".
//
// WHAT THIS GATE PINS (source-level; the behavioral half is the JVM suite
// android/app/src/test/kotlin/dev/swarm/phone/ui/screens/MachinesScreenTest.kt, which
// is compile-RED against the same missing model):
//
//   - ui/screens/MachinesScreen.kt exists: the machine switcher's PURE screen model, in
//     the module's established shape (PairOnlyScreen, TriageInboxScreen -- logic as a
//     pure function, views take data and copy from it, PB-DS-9).
//   - Every R4 affordance is NAMEABLE BY THE MODEL: add computer, switch computer,
//     forget computer, and the global inbox destination. An affordance a screen draws
//     but the model cannot name is untestable copy-in-the-view.
//   - The FIRST-RUN RESOLVER stays a function: with zero machines the destination is
//     the pair-only screen (PairOnlyReason.FIRST_RUN's world); with one or more it is
//     the machines destination. Named `destinationFor` so the JVM suite can drive it
//     from the empty state without an Activity.
//   - The JVM test file exists beside every other screen model's.
//
// OBSIDIAN IS NOT RE-FENCED HERE: s24_screens_test.go's PB-DS-11/PB-DS-6 sweeps cover
// ALL production Kotlin, so the new screen is inside the existing fence the moment the
// file exists -- adding a second scanner would be a parallel fence that can drift.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func r4ScreensDir(t *testing.T) string {
	return filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone", "ui", "screens")
}

// TestR4_MachinesScreenModelExistsAndNamesEveryAffordance: the model file, its
// affordance names, and its first-run resolver.
func TestR4_MachinesScreenModelExistsAndNamesEveryAffordance(t *testing.T) {
	path := filepath.Join(r4ScreensDir(t), "MachinesScreen.kt")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("R4's machine switcher has no screen model: %v. The module's rule (PB-DS-9, "+
			"PairOnlyScreen/TriageInboxScreen precedent) is a pure model the JVM suite can drive; "+
			"a switcher composed directly in a Surface is untestable copy-in-the-view", err)
	}
	src := string(raw)

	for _, affordance := range []string{
		"ADD_COMPUTER",    // playbook 4.1: "Add computer"; adds beside, never replaces.
		"SWITCH_COMPUTER", // playbook 4.2: the switcher; feeds least-recently-viewed.
		"FORGET_COMPUTER", // playbook 4.9: phone-side forget, distinct from machine-side revoke.
		"GLOBAL_INBOX",    // playbook 4.2: the aggregate inbox destination.
	} {
		if !strings.Contains(src, affordance) {
			t.Errorf("MachinesScreen.kt does not name affordance %s; an affordance the model "+
				"cannot name cannot be asserted on, deep-linked to, or read by a screen reader", affordance)
		}
	}

	if !strings.Contains(src, "destinationFor") {
		t.Errorf("MachinesScreen.kt has no destinationFor resolver; the first-run state (zero " +
			"machines -> pair-only) must be a function the JVM suite drives from the empty state, " +
			"not a branch buried in PhoneSurface's redraw")
	}

	// The rows must be modeled off the four facts of playbook 4.2:198 -- name,
	// reachability, last successful sync, needs-input count -- keyed by machine id.
	for _, field := range []string{"reachability", "lastSync", "needsInput"} {
		if !strings.Contains(src, field) {
			t.Errorf("MachinesScreen.kt's row model carries no %q; playbook 4.2:198 makes it a "+
				"row fact: \"Each row names the machine, reachability, last successful sync, and "+
				"count of sessions needing input\"", field)
		}
	}
}

// TestR4_MachinesScreenModelHasItsJVMSuite: the behavioral tests live where every other
// screen model's do, so the gradle unit lane runs them.
//
// PASSES AT RED TIME, AND THAT IS NOT COVERAGE -- labelled the way s16's B8 restatement
// is: the RED slice itself checks in MachinesScreenTest.kt (compile-RED against the
// missing model), so this existence check is green from day one. It is written as a
// FENCE: the suite cannot be deleted to make the gradle lane green, which is exactly
// the shortcut a compile-RED Kotlin file invites.
func TestR4_MachinesScreenModelHasItsJVMSuite(t *testing.T) {
	path := filepath.Join(appModule(t), "src", "test", "kotlin", "dev", "swarm", "phone",
		"ui", "screens", "MachinesScreenTest.kt")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("MachinesScreen has no JVM suite: %v. The screen-model tests -- first-run "+
			"resolver from the empty state, duplicate (machine_id, session_id) rows not colliding, "+
			"stale rows carrying their last-sync age -- are the R4 UX deliverable's evidence", err)
	}
}
