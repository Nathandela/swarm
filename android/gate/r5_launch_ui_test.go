package gate

// FAILING-FIRST (TDD RED, GG-5) for Wave R5 deliverable 4's Android half (bead
// agents-tracker-hggx.6): the phone-side launch UX -- preset selection and the
// confirmation sheet -- SCREEN-MODEL FIRST, in the module's established shape
// (PairOnlyScreen / TriageInboxScreen / MachinesScreen precedent: logic as a pure
// function, views take data and copy from it, PB-DS-9).
//
// WHAT THIS GATE PINS (source-level; the behavioral half is the JVM suite
// LaunchPresetScreenTest.kt beside every other screen model's, compile-RED against
// the same missing model):
//
//   - ui/screens/LaunchPresetScreen.kt exists: the launch flow's PURE screen model.
//     The existing LaunchPanel.kt (Phase B S24's free-form agent+cwd form) is NOT this
//     and is not edited by the RED slice: R5's phone never supplies an arbitrary
//     filesystem path, so the preset flow is a new model and the free-form form's
//     retirement from the remote surface is the GREEN slice's explicit decision.
//   - Every R5 affordance is NAMEABLE BY THE MODEL: NEW_SESSION (playbook 4.3: only
//     on a selected, online, full-tier, kill-switch-on machine), SELECT_PRESET,
//     CONFIRM_LAUNCH, CANCEL_LAUNCH.
//   - Every STABLE REFUSAL is a nameable state, not interpolated copy: STALE_PRESET,
//     UNKNOWN_PRESET, KILL_SWITCH, NOT_AUTHORIZED, OFFLINE -- plus the delivery
//     states PENDING and OUTCOME_UNKNOWN (ADR-017 T9: launch uses the composer's
//     delivery vocabulary; outcome_unknown is rendered honestly, never as success or
//     failure). Visible success AND visible refusal on every verb is the D3 lesson.
//   - The availability RESOLVER stays a function, `launchAvailabilityFor`, driving
//     the affordance from (online, tier, killSwitch, presetCount) -- including the
//     first-run/empty states (zero presets -> the setup hint naming `swarm remote
//     presets`; read_only/read_approve tier -> a named reason, not a missing button).
//   - The CONFIRMATION model carries the five facts playbook 4.3:219-220 puts on the
//     sheet -- machineName, provider, workspacePath, worktreeBehavior,
//     hasInitialPrompt -- so the JVM suite can assert a confirm never renders fewer.
//
// OBSIDIAN IS NOT RE-FENCED HERE: s24_screens_test.go's PB-DS-11/PB-DS-6 sweeps cover
// all production Kotlin, so the new screen is inside the existing fence the moment
// the file exists.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func r5ScreensDir(t *testing.T) string {
	return filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone", "ui", "screens")
}

// TestR5_LaunchPresetScreenModelExistsAndNamesEveryAffordance: the model file, its
// affordances, its stable refusal/delivery states, its availability resolver, and the
// confirmation sheet's five facts.
func TestR5_LaunchPresetScreenModelExistsAndNamesEveryAffordance(t *testing.T) {
	path := filepath.Join(r5ScreensDir(t), "LaunchPresetScreen.kt")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("R5's launch flow has no screen model: %v. The module's rule (PB-DS-9, "+
			"MachinesScreen precedent) is a pure model the JVM suite can drive; a launch flow "+
			"composed directly in a Surface is untestable copy-in-the-view", err)
	}
	src := string(raw)

	for _, affordance := range []string{
		"NEW_SESSION",    // playbook 4.3: the entry affordance, gated by machine/tier/kill switch
		"SELECT_PRESET",  // the phone selects; it never composes
		"CONFIRM_LAUNCH", // the explicit phone confirm D8 retains
		"CANCEL_LAUNCH",  // backing out of the sheet is an affordance, not a gesture
	} {
		if !strings.Contains(src, affordance) {
			t.Errorf("LaunchPresetScreen.kt does not name affordance %s; an affordance the model "+
				"cannot name cannot be asserted on, deep-linked to, or read by a screen reader", affordance)
		}
	}

	for _, state := range []string{
		"STALE_PRESET",    // the machine re-authored between list and confirm; remedy: re-pick
		"UNKNOWN_PRESET",  // the machine never authored this id; remedy: re-list
		"KILL_SWITCH",     // remote control is off machine-side
		"NOT_AUTHORIZED",  // wrong key, revoked pairing, or read_only/read_approve tier
		"OFFLINE",         // target unreachable: refused phone-side before anything is composed
		"PENDING",         // in flight, unresolved
		"OUTCOME_UNKNOWN", // died mid-flight and the machine could not prove the outcome
	} {
		if !strings.Contains(src, state) {
			t.Errorf("LaunchPresetScreen.kt does not name state %s; every stable refusal code and "+
				"delivery state must be a nameable model state with its own copy -- interpolating "+
				"wire strings into one notice is how refusals become invisible (D3)", state)
		}
	}

	if !strings.Contains(src, "launchAvailabilityFor") {
		t.Errorf("LaunchPresetScreen.kt has no launchAvailabilityFor resolver; whether NEW_SESSION " +
			"is offered (online + full tier + kill switch on + presets exist) must be a function " +
			"the JVM suite drives through every denial, not a branch buried in a Surface redraw")
	}

	for _, field := range []string{"machineName", "provider", "workspacePath", "worktreeBehavior", "hasInitialPrompt"} {
		if !strings.Contains(src, field) {
			t.Errorf("LaunchPresetScreen.kt's confirmation model carries no %q; playbook 4.3 puts "+
				"it on the sheet: \"The confirmation sheet shows machine, provider, resolved "+
				"workspace display path, worktree behavior, and initial-prompt presence\"", field)
		}
	}
}

// TestR5_LaunchPresetScreenModelHasItsJVMSuite: the behavioral tests live where every
// other screen model's do, so the gradle unit lane runs them.
//
// PASSES AT RED TIME, AND THAT IS NOT COVERAGE (the s16/R4 labelling): the RED slice
// itself checks in LaunchPresetScreenTest.kt, compile-RED against the missing model.
// This existence check is a FENCE: the suite cannot be deleted to make the gradle
// lane green.
func TestR5_LaunchPresetScreenModelHasItsJVMSuite(t *testing.T) {
	path := filepath.Join(appModule(t), "src", "test", "kotlin", "dev", "swarm", "phone",
		"ui", "screens", "LaunchPresetScreenTest.kt")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("LaunchPresetScreen has no JVM suite: %v. The screen-model tests -- the "+
			"availability matrix through every denial, the five confirmation facts, the "+
			"stale_preset and outcome_unknown notices -- are the R5 UX deliverable's evidence", err)
	}
}
