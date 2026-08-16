package gate

// FAILING-FIRST (TDD RED, GG-5) for wave R4's D3 round-2 fix pack (bead agents-tracker-0ox9):
// the review findings against the composed switcher, each one a structural fact about which
// function reaches which, fenced the way this package already fences the same surface
// (mrq5_replaceconfirm_test.go, o6_predictiveback_test.go, r4_d3_reachability_test.go -- whose
// package-level readers this file deliberately reuses rather than re-declaring).
//
// THE FOUR FINDINGS, in the review's own severity order:
//
//	ADD NEVER SUCCEEDS   mobile/machines.go refuses AddMachine while `a.sess != nil`, and
//	                     `a.sess` is non-nil from App.Start to Stop -- i.e. on every
//	                     foregrounded phone. The refusal is ErrClassInvalidRequest, which
//	                     ErrorRouter renders as "please report it" (Remedy.REPORT_BUG): the
//	                     panel's primary CTA was a permanent bug-report toast, and the only
//	                     MM6 migration trigger was unreachable. The surface must SATISFY the
//	                     facade's stated precondition -- stop the drain, add, start again --
//	                     rather than forward a refusal routed to "report a bug".
//	ONE-TAP FORGET       ForgetMachine destroys a pairing's registry row, namespace, keys and
//	                     caches, and it hung un-confirmed on a denyChip in a row's trailing
//	                     slot -- the exact shape mrq5's own header names as the defect, while
//	                     `kill` (ONE session) has asked since S24. The mechanism exists
//	                     (PhoneSurface.confirmThenPress's AlertDialog) and was not spent.
//	BACK NOT ARMED       onDrillDownChanged had exactly one write site, the `detail` setter;
//	                     openMachines/openGlobalInbox set their flags without touching it, so
//	                     the system back gesture popped the Activity out from under both new
//	                     drill-downs (O6.3's requirement unmet for them).
//	SILENT NO-OP         machinesPanel's catch and drawGlobalInbox's catch wrote the routed
//	                     refusal to `outcome` -- a child of unrecomposedControls, hosted at the
//	                     bottom of the Inbox tab, which the user standing on Settings cannot
//	                     see -- and the PAIR_ONLY branch composed nothing and said nothing.
//	                     Hard rule: a refusal is a state, never a crash and never a silent
//	                     no-op; `say(...)` is what makes the toast fire over the screen the
//	                     user is actually on.
//
// WHY GO GATES AND NOT KOTLIN TESTS: mrq5's recorded line, verbatim -- `PhoneRuntime.phone()`
// answers Unavailable on every JVM run, so no Robolectric press can be driven as far as these
// functions. What a test CAN see is the source: which function the listener reaches, which
// function reaches the verb, and which state arms the gesture. The COPY is deliberately not
// fenced here (PB-DS-9): what FORGET_CONFIRM and PAIR_FIRST say is MachinesPanelScreen's, and
// MachinesPanelRound2Test asserts it on a plain JVM where the words are values.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT.

import (
	"regexp"
	"strings"
	"testing"
)

// r4d3r2Block is one named function's brace-balanced body out of PhoneSurface.kt, code-only,
// located by `fun name(` -- the paren is load-bearing: "fun addComputer" is a prefix of
// "fun addComputerSlot", and the shorter marker would hand this file the wrong function.
func r4d3r2Block(t *testing.T, src, name string) string {
	t.Helper()
	block := r4d3BlockAfter(src, "fun "+name+"(")
	if block == "" {
		t.Fatalf("agents-tracker-0ox9 round 2: PhoneSurface.kt declares no `fun %s(`; the "+
			"fence's subject moved and the fence must move with it", name)
	}
	return block
}

// ---------------------------------------------------------------------------
// Finding 1: Add computer must be able to succeed on a real handset.
// ---------------------------------------------------------------------------

func TestR4D3R2_AddComputerSatisfiesTheMigrationPreconditionItself(t *testing.T) {
	// mobile/machines.go's precondition is real and stays: the MM6 migration must not race a
	// live drain. What was wrong is WHO was asked to satisfy it -- the user, via a refusal
	// routed to "report a bug". The surface owns the drain (App.Stop/App.Start are idempotent
	// and safe by their own KDoc), so the verb must be stop -> add -> start, off the looper,
	// with the restart in a finally so a refused add does not leave the phone disconnected.
	surface := r4d3Code(t, r4d3SurfaceFile)
	block := r4d3r2Block(t, surface, "addComputer")

	stop := strings.Index(block, ".stop()")
	add := strings.Index(block, ".addMachine(")
	start := strings.Index(block, ".start()")
	if stop < 0 || add < 0 || start < 0 {
		t.Fatalf("agents-tracker-0ox9 round 2: addComputer's verb does not stop the drain "+
			"around the add (stop@%d add@%d start@%d): AddMachine is refused whenever a.sess "+
			"!= nil, which is every foregrounded phone, so this CTA can never succeed and the "+
			"refusal it earns is routed to Remedy.REPORT_BUG -- the app blaming itself for a "+
			"precondition only this surface can satisfy", stop, add, start)
	}
	if stop >= add || add >= start {
		t.Errorf("agents-tracker-0ox9 round 2: addComputer's verb is not stop -> add -> start "+
			"(stop@%d add@%d start@%d); the drain must be stopped BEFORE the migration runs and "+
			"resumed after it either way", stop, add, start)
	}

	// Negative control: the same reader over a fixture with the calls reversed must refuse.
	control := "fun addComputer() { machineVerb { app -> app.addMachine(id, name); app.stop(); app.start() } }"
	cStop := strings.Index(control, ".stop()")
	cAdd := strings.Index(control, ".addMachine(")
	if cStop < cAdd {
		t.Fatalf("negative control broken: the reversed fixture reads as ordered")
	}
}

// ---------------------------------------------------------------------------
// Finding 2: Forget asks before it destroys.
// ---------------------------------------------------------------------------

func TestR4D3R2_ForgetAsksTheModelsQuestionBeforeTheVerb(t *testing.T) {
	// `kill` -- ONE session -- has asked since S24; forgetting a pairing destroys its keys,
	// namespace and caches with no undo, from a denyChip in a row's trailing slot. The dialog
	// is the surface's (confirmThenPress's own ruling: a confirmation is a second window, never
	// a row in the composition), and the QUESTION is the model's recorded copy -- a question
	// typed at this call site is the 64rf drift again.
	surface := r4d3Code(t, r4d3SurfaceFile)
	block := r4d3r2Block(t, surface, "forgetComputer")

	asked := strings.Index(block, "setPositiveButton")
	verb := strings.Index(block, ".forgetMachine(")
	if asked < 0 {
		t.Fatalf("agents-tracker-0ox9 round 2: forgetComputer shows no confirmation "+
			"(no setPositiveButton in its body); the app's most destructive per-pairing action "+
			"is one un-confirmed tap on the smaller target, which is mrq5's defect shape "+
			"verbatim -- and the AlertDialog mechanism already exists one function over: %q",
			strings.TrimSpace(block))
	}
	if verb >= 0 && verb < asked {
		t.Errorf("agents-tracker-0ox9 round 2: forgetComputer reaches .forgetMachine( at %d "+
			"BEFORE the confirmation at %d; a dialog shown after the destruction is a "+
			"decoration", verb, asked)
	}
	if !strings.Contains(block, "FORGET_CONFIRM") {
		t.Errorf("agents-tracker-0ox9 round 2: forgetComputer's question is not the model's " +
			"recorded FORGET_CONFIRM; copy typed at a call site is the drift that made the " +
			"pairing panel unfindable (PB-DS-9, agents-tracker-64rf)")
	}

	// And the copy is declared exactly once, on the screen model, where the JVM suite freezes
	// its words.
	decl := regexp.MustCompile(`(?:const\s+)?val\s+FORGET_CONFIRM`)
	if spends := r4d3CallersOf(r4d3ProductionCode(t), "FORGET_CONFIRM", decl); len(spends) == 0 {
		t.Errorf("agents-tracker-0ox9 round 2: no production file outside its declaration " +
			"spends FORGET_CONFIRM")
	}
}

// ---------------------------------------------------------------------------
// Finding 3: the system back gesture is armed for both new drill-downs.
// ---------------------------------------------------------------------------

func TestR4D3R2_TheBackGestureIsArmedForTheNewDrillDowns(t *testing.T) {
	// O6.3: predictive back honoured on drill-downs. Both new screens compose navHeaderDrill,
	// so they ARE drill-downs -- and onDrillDownChanged had exactly one write site, the
	// `detail` setter. The arming predicate must be the union of every drill sub-state, pushed
	// from one function so the three writers cannot drift.
	surface := r4d3Code(t, r4d3SurfaceFile)

	push := r4d3r2Block(t, surface, "pushDrillDown")
	for _, state := range []string{"detail", "machinesOpen", "globalInboxOpen"} {
		if !strings.Contains(push, state) {
			t.Errorf("agents-tracker-0ox9 round 2: pushDrillDown's predicate does not read %q; "+
				"a drill sub-state the predicate ignores is a screen the back gesture exits the "+
				"app from -- and because the flag survives, re-entering lands the user back in "+
				"the screen the gesture failed to leave", state)
		}
	}
	if !strings.Contains(push, "onDrillDownChanged") {
		t.Errorf("agents-tracker-0ox9 round 2: pushDrillDown does not push onDrillDownChanged; " +
			"the Activity is told, never polled (the property's own KDoc)")
	}
	for _, opener := range []string{"openMachines", "closeMachines", "openGlobalInbox", "closeGlobalInbox"} {
		if !strings.Contains(r4d3r2Block(t, surface, opener), "pushDrillDown(") {
			t.Errorf("agents-tracker-0ox9 round 2: %s moves a drill sub-state without calling "+
				"pushDrillDown(); the gesture stays disarmed (or stays armed) across it", opener)
		}
	}

	// The commit half: the gesture must pop the drill-down the user is standing in, not
	// unconditionally the session detail.
	pop := r4d3r2Block(t, surface, "closeDrillDown")
	for _, closer := range []string{"closeGlobalInbox", "closeMachines", "closeSessionDetail"} {
		if !strings.Contains(pop, closer) {
			t.Errorf("agents-tracker-0ox9 round 2: closeDrillDown does not reach %s; whichever "+
				"drill-down is open is the one the gesture leaves", closer)
		}
	}
	activity := r4d3Code(t, "dev/swarm/phone/PhoneActivity.kt")
	if !strings.Contains(activity, "closeDrillDown") {
		t.Errorf("agents-tracker-0ox9 round 2: PhoneActivity's back callback does not reach " +
			"surface.closeDrillDown(); handleOnBackPressed still pops only the session detail, " +
			"so a committed gesture on the switcher closes the Activity instead")
	}
}

// ---------------------------------------------------------------------------
// Finding 4: a roster-read refusal -- and the PAIR_ONLY answer -- is a visible state.
// ---------------------------------------------------------------------------

func TestR4D3R2_ARosterRefusalIsNeverASilentNoOp(t *testing.T) {
	// `outcome` is a child of unrecomposedControls, hosted at the bottom of the Inbox tab
	// (PhoneSurface's own record) -- a user standing on Settings cannot see a word written
	// there. `say(...)` is the seam that writes the line AND fires row 1's toast over whatever
	// screen is up; every refusal on the switcher path must go through it.
	surface := r4d3Code(t, r4d3SurfaceFile)
	for _, fn := range []string{"machinesPanel", "drawGlobalInbox"} {
		if !strings.Contains(r4d3r2Block(t, surface, fn), "say(") {
			t.Errorf("agents-tracker-0ox9 round 2: %s's refusal path does not say(...) -- the "+
				"user taps, the screen does not change, and nothing anywhere says why: the "+
				"exact silent-no-op shape hard rule 5 forbids, on the path the rule does not "+
				"name", fn)
		}
	}

	// The resolver's PAIR_ONLY answer is an answer, not an absence: the recorded sentence must
	// be spent by a production caller outside its declaring file.
	decl := regexp.MustCompile(`(?:const\s+)?val\s+PAIR_FIRST`)
	if spends := r4d3CallersOf(r4d3ProductionCode(t), "PAIR_FIRST", decl); len(spends) == 0 {
		t.Errorf("agents-tracker-0ox9 round 2: no production Kotlin spends PAIR_FIRST; with " +
			"zero rows the entry bounces back to Settings composing nothing and saying " +
			"nothing, which is the same silent no-op with the resolver's name on it")
	}
}

// ---------------------------------------------------------------------------
// Finding 5 (major): the draw passes the clock, so the row's last-sync age renders.
// ---------------------------------------------------------------------------

func TestR4D3R2_TheMachinesDrawSpendsTheClockSeam(t *testing.T) {
	// Playbook 4.2:198 gives a row FOUR facts and only three reached the screen. The age is
	// the model's to compute (MachinesPanelRound2Test freezes the words) and the surface's to
	// clock: the same System.currentTimeMillis() this file's neighbour already passes into
	// SyncStatus.of one screen over. A view that reads its own clock is untestable; a draw
	// that passes none renders no age at all.
	block := r4d3r2Block(t, r4d3Code(t, r4d3SurfaceFile), "drawMachines")
	if !strings.Contains(block, "currentTimeMillis") {
		t.Errorf("agents-tracker-0ox9 round 2: drawMachines passes no clock into the panel " +
			"view, so MachineRowModel.lastSyncUnixMs -- carried by every row since the model " +
			"landed, and its KDoc's own promise -- never reaches the screen and a parked row " +
			"shows no last-sync age (ADR-018 MM3, playbook 4.2:200-202)")
	}
}
