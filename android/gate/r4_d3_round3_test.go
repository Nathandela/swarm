package gate

// FAILING-FIRST (TDD RED, GG-5) for wave R4's D3 round-3 fix pack (bead agents-tracker-0ox9):
// round 3's review findings against the composed switcher, each one a structural fact about
// which function reaches which, fenced the way r4_d3_round2_test.go fences the same surface and
// reusing its readers rather than re-declaring them.
//
// THE FINDINGS, in the review's own severity order:
//
//	SILENT SUCCESS   switchComputer settled with machineVerb's default no-op: on success nothing
//	                 was said and nothing was marked, and drawMachines' equality guard
//	                 early-returned because the panel was byte-identical -- the panel CANNOT
//	                 change from the facade, because registrymanager only flips Connected when
//	                 the roster exceeds the cap and MachineInfo carries no current-machine fact.
//	                 A successful switch was indistinguishable from a dead button. Round 2 routed
//	                 REFUSALS through say(...) and left SUCCESS mute on the primary control.
//	UNCOMPLETABLE    Add computer presents two raw text boxes and calls AddMachine directly. The
//	ADD, UNDISCLOSED row it creates awaits that machine's OWN pairing ceremony (bead
//	                 agents-tracker-ak2s, out of this slice by ruling), and SelectMachine does
//	                 not re-target the live relay session (mobile/machines.go:19-21). Neither
//	                 limit was anywhere on screen. The fix here is HONESTY, not a ceremony.
//	UNCONFIRMED,     addComputer runs app.stop() around the add: suspendInput abandons buffered
//	UNFENCED ADD     keystrokes as undelivered, severs every input lease and drops the link --
//	                 strictly more destructive than Forget, which asks. And VerbDispatch.enqueue
//	                 has no double-tap fence, so a rapid double tap ran the whole sequence twice.
//	UNCLAIMED        MachinesPanelView.kt and GlobalInboxView.kt were never added to
//	SCREENS          s24ScreenComponents, so they are covered only by the weak "calls at least
//	                 one kit factory" fence and can silently drop navHeaderDrill/notice/session
//	                 composition.
//
// WHY GO GATES AND NOT KOTLIN TESTS for the surface half: mrq5's recorded line, verbatim --
// PhoneRuntime.phone() answers Unavailable on every JVM run, so no Robolectric press reaches
// these functions. The COPY is MachinesPanelRound3Test's, the rendering is
// MachinesPanelViewRound3Test's, and the dispatch fence is VerbDispatchRound3Test's; this file
// asserts only what none of them can see -- which function reaches which.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT: the two verification records are read by name.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// BLOCKING 1: a successful switch marks the row AND speaks.
// ---------------------------------------------------------------------------

func TestR4D3R3_ASuccessfulSwitchIsNotSilent(t *testing.T) {
	surface := r4d3Code(t, r4d3SurfaceFile)
	block := r4d3r2Block(t, surface, "switchComputer")

	if !strings.Contains(block, "settle") {
		t.Errorf("agents-tracker-0ox9 round 3: switchComputer still hands machineVerb its "+
			"DEFAULT no-op settle, so a switch that succeeded changes nothing at all: %q",
			strings.TrimSpace(block))
	}
	if !strings.Contains(block, "say(") {
		t.Errorf("agents-tracker-0ox9 round 3: switchComputer's success path does not say(...). " +
			"Round 2 routed every REFUSAL through say and left SUCCESS mute on the switcher's " +
			"primary control -- the user taps a row and the app answers with silence, which is " +
			"the silent-no-op shape hard rule 5 forbids arriving through the success door")
	}
	if !strings.Contains(block, "switchedTo") {
		t.Errorf("agents-tracker-0ox9 round 3: switchComputer does not spend the model's " +
			"recorded switchedTo(...) confirmation; a sentence typed at this call site is the " +
			"drift PB-DS-9 forbids, and this one carries mobile/machines.go:19-21's limit")
	}
}

func TestR4D3R3_TheSelectedMachineReachesThePanelAndTheRow(t *testing.T) {
	// The mark is SURFACE-side by necessity: MachineInfo has no current-machine field and the
	// roster's Connected flag only moves when the roster exceeds the cap, so the panel could
	// not differ between two switches and drawMachines' equality guard early-returned forever.
	surface := r4d3Code(t, r4d3SurfaceFile)
	panel := r4d3r2Block(t, surface, "machinesPanel")
	if !strings.Contains(panel, "selected") {
		t.Errorf("agents-tracker-0ox9 round 3: machinesPanel builds MachinesPanelScreen.of(...) "+
			"without the selection, so every panel it produces is byte-identical across a "+
			"switch and drawMachines' guard early-returns: %q", strings.TrimSpace(panel))
	}
	if !strings.Contains(r4d3r2Block(t, surface, "switchComputer"), "selected") {
		t.Errorf("agents-tracker-0ox9 round 3: switchComputer records no selection, so nothing " +
			"the draw reads ever changes and the mark can never appear")
	}

	view := r4d3Code(t, r4d3ViewFile)
	if !strings.Contains(view, "selectedMachineId") {
		t.Errorf("agents-tracker-0ox9 round 3: %s never reads panel.selectedMachineId, so the "+
			"selection reaches the model and stops one layer short of the row a user looks at "+
			"(the model-is-beautiful-and-nothing-renders-it defect, PB-DS-6's recorded NOT MET)",
			r4d3ViewFile)
	}
}

// ---------------------------------------------------------------------------
// BLOCKING 2: the add form states, on screen, what it cannot finish.
// ---------------------------------------------------------------------------

func TestR4D3R3_TheAddFormsLimitsAreComposed(t *testing.T) {
	decl := regexp.MustCompile(`(?:const\s+)?val\s+ADD_LIMITS`)
	spends := r4d3CallersOf(r4d3ProductionCode(t), "ADD_LIMITS", decl)
	if len(spends) == 0 {
		t.Errorf("agents-tracker-0ox9 round 3: no production Kotlin outside its declaring file " +
			"spends ADD_LIMITS. The composition presents two raw text boxes and calls AddMachine: " +
			"nothing says where a machine id comes from, that the added computer still needs its " +
			"own pairing ceremony (bead agents-tracker-ak2s), or that switching does not move the " +
			"live relay session. A form a user cannot complete, that does not say so, is the " +
			"overclaim this repository's evidence system exists to prevent")
	}
}

func TestR4D3R3_TheD3RecordDisclosesWhatAUserCannotComplete(t *testing.T) {
	// Round 2 DELETED the D3 honesty paragraph from r4-multimachine.md and replaced it with
	// "ADD_COMPUTER, SWITCH_COMPUTER, FORGET_COMPUTER and GLOBAL_INBOX are all reachable by a
	// user", repeating it in r4-d3-ui.md. Deleting a disclosure to claim completeness is the
	// defect class the evidence system exists to prevent, so the restoration is fenced.
	for _, rel := range []string{
		"docs/verification/r4-multimachine.md",
		"docs/verification/r4-d3-ui.md",
	} {
		path := filepath.Join(repoRoot(t), filepath.FromSlash(rel))
		// WHITESPACE-NORMALISED, and it is not tidiness: these records are hard-wrapped, so the
		// overclaim survives as "reachable by a\nuser" and a raw Contains would report clean over
		// the sentence it exists to refuse -- a fence a line break turns off.
		doc := readFileOrFail(t, path, "agents-tracker-0ox9 round 3")
		flat := strings.Join(strings.Fields(doc), " ")
		if strings.Contains(flat, "are all reachable by a user") {
			t.Errorf("%s still claims the four R4 affordances are ALL reachable by a user. "+
				"ADD_COMPUTER cannot be COMPLETED in this slice: the row it creates awaits that "+
				"machine's own pairing ceremony, and switching does not re-target the live relay "+
				"session", rel)
		}
		if !strings.Contains(doc, "agents-tracker-ak2s") {
			t.Errorf("%s does not cite bead agents-tracker-ak2s, which owns the pairing ceremony "+
				"this slice deliberately does not wire; a scope limit with no owner named is a "+
				"limit nobody is accountable for", rel)
		}
		if !strings.Contains(doc, "pairing ceremony") {
			t.Errorf("%s does not state the pairing-ceremony limit; the D3 section must say "+
				"exactly what is and is not user-completable, which is the disclosure round 2 "+
				"deleted", rel)
		}
	}
}

// ---------------------------------------------------------------------------
// BLOCKING 3: Add confirms, and a second Add while one runs is refused.
// ---------------------------------------------------------------------------

func TestR4D3R3_AddAsksBeforeItSeversEveryLease(t *testing.T) {
	// app.stop() -> suspendInput -> coalesce.Abandon (buffered keystrokes resolved undelivered)
	// + Leases().SeverAll + a real disconnect. Forget, which destroys ONE pairing, has asked
	// since round 2; the strictly larger blast radius asked nothing.
	block := r4d3r2Block(t, r4d3Code(t, r4d3SurfaceFile), "addComputer")

	asked := strings.Index(block, "setPositiveButton")
	stop := strings.Index(block, ".stop()")
	if asked < 0 {
		t.Fatalf("agents-tracker-0ox9 round 3: addComputer shows no confirmation (no "+
			"setPositiveButton in its body). It disconnects the phone and destroys every "+
			"keystroke typed and not yet sent, with no question -- while the LESS destructive "+
			"Forget asks: %q", strings.TrimSpace(block))
	}
	if stop >= 0 && stop < asked {
		t.Errorf("agents-tracker-0ox9 round 3: addComputer stops the drain at %d BEFORE the "+
			"confirmation at %d; a dialog shown after the severance is a decoration", stop, asked)
	}
	if !strings.Contains(block, "ADD_CONFIRM") {
		t.Errorf("agents-tracker-0ox9 round 3: addComputer's question is not the model's " +
			"recorded ADD_CONFIRM (PB-DS-9, agents-tracker-64rf)")
	}
}

func TestR4D3R3_ASecondAddWhileOneIsRunningIsRefusedOutLoud(t *testing.T) {
	surface := r4d3Code(t, r4d3SurfaceFile)
	block := r4d3r2Block(t, surface, "addComputer")

	if !strings.Contains(block, "key") {
		t.Errorf("agents-tracker-0ox9 round 3: addComputer hands its verb to the lane with no "+
			"single-flight key. VerbDispatch.press fences a double tap on a CONTROL, and the "+
			"machines controls are rebuilt per draw so machineVerb uses enqueue -- which is "+
			"deliberately unfenced: a rapid double tap runs stop/add/start twice: %q",
			strings.TrimSpace(block))
	}
	if !strings.Contains(block, "ADD_IN_FLIGHT") {
		t.Errorf("agents-tracker-0ox9 round 3: addComputer does not say ADD_IN_FLIGHT when the " +
			"lane refuses a second add; a tap dropped in silence is the silent-no-op shape on " +
			"the app's most destructive control")
	}

	dispatch := r4d3Code(t, "dev/swarm/phone/VerbDispatch.kt")
	enqueue := r4d3BlockAfter(dispatch, "fun <T> enqueue(")
	if enqueue == "" {
		t.Fatalf("agents-tracker-0ox9 round 3: VerbDispatch declares no `fun <T> enqueue(`; this " +
			"fence's subject moved and the fence must move with it")
	}
	if !r4d3r3EnqueueForwardsKey(dispatch) {
		t.Errorf("agents-tracker-0ox9 round 3: VerbDispatch.enqueue does not forward its opt-in key, so there is no "+
			"single-flight fence for work with no control to key on. The fence must be OPT-IN: "+
			"unkeyed work stays undroppable for the push-token reconciliation "+
			"(agents-tracker-b6iu): %q", strings.TrimSpace(enqueue))
	}
}

// r4d3r3EnqueueForwardsKey checks the implementation body, not the declaration parameter: merely
// accepting a key and then delegating null is the exact silent regression this fence owns.
func r4d3r3EnqueueForwardsKey(dispatch string) bool {
	return strings.Contains(r4d3BlockAfter(dispatch, "fun <T> enqueue("), "key = key")
}

func TestR4D3R3_TheEnqueueKeyFenceCheckDiscriminates(t *testing.T) {
	withFence := `fun <T> enqueue(key: Any?): Boolean {
        return enqueueOnMain(key = key)
    }`
	if !r4d3r3EnqueueForwardsKey(withFence) {
		t.Fatal("the key-fence reader rejected enqueue forwarding its caller's opt-in key")
	}

	withoutFence := `fun <T> enqueue(key: Any?): Boolean {
        return enqueueOnMain(key = null)
    }`
	if r4d3r3EnqueueForwardsKey(withoutFence) {
		t.Fatal("the key-fence reader passed enqueue after its single-flight key was discarded")
	}
}

// ---------------------------------------------------------------------------
// MINOR: no composed screen is fenced by the weak claim alone.
// ---------------------------------------------------------------------------

func TestR4D3R3_EveryComposedScreenIsClaimedByTheCompositionTable(t *testing.T) {
	// s24ScreenComponents is the per-screen claim, and a screen ABSENT from it gets an empty
	// requirement: TestPBDS6_EveryRecomposedScreenIsBuiltOutOfTheKit then checks only that the
	// file calls at least one kit factory, which a screen that quietly dropped its nav header,
	// its notice line or its row composition still passes. That is how MachinesPanelView.kt and
	// GlobalInboxView.kt arrived unfenced. The table has to be exhaustive over the screens that
	// build views, or "the kit is the only way a screen is built" is a claim about a subset
	// nobody records.
	var unclaimed []string
	for name, src := range s24ScreenSources(t) {
		if !s24BuildsViews.MatchString(kotlinCodeOnly(src)) {
			continue
		}
		if _, ok := s24ScreenComponents[name]; !ok {
			unclaimed = append(unclaimed, name)
		}
	}
	sort.Strings(unclaimed)
	if len(unclaimed) > 0 {
		t.Errorf("agents-tracker-0ox9 round 3: %d composed screens have no entry in "+
			"s24ScreenComponents, so nothing is asked of their composition and passing is "+
			"indistinguishable from being unchecked:\n%s",
			len(unclaimed), strings.Join(unclaimed, "\n"))
	}
}
