package gate

// FAILING-FIRST (TDD RED, GG-5): the two wirings residuals §2.9 and §2.10(a) found missing,
// fenced where they can actually be checked -- as REACHABILITY from the lifecycle callback that
// has to reach them.
//
// WHY REACHABILITY AND NOT JUST A CALL. android/gate/boundverbledger_test.go asserts that
// production Kotlin calls each verb, which is the control that catches a verb nobody wired at
// all. It cannot see WHERE the call is: a `subscribeJournal` inside a helper nothing invokes
// satisfies it, and so does one on a path that runs once at construction and never again. For
// these three the LIFECYCLE is the property -- subscribe on resume, withdraw on pause
// (ADR-007 B16) -- so this file walks from `render` and from `release`.
//
// WHY IT CANNOT BE A KOTLIN TEST. The phone core cannot be built on the unit-test JVM: there is
// no libgojni, so `Swarmmobile.newApp` raises an UnsatisfiedLinkError and every Robolectric
// launch of PhoneActivity gets `PhoneStartup.Unavailable`. `renderReady` -- where all of this
// lives -- is unreachable from any test on this machine. That is the same property that let the
// defect ship, so the fence is a source fence and says so.
//
// THE WALK IS THE S17 ONE (s17_pushclient_test.go), not a second weaker one. It already handles
// expression bodies, which is most of this tree, and it was repaired twice this phase. It is
// driven over ONE FILE rather than the whole module: `s17Bodies` concatenates the bodies of
// every same-named function in production Kotlin, and `render` is declared in three surfaces --
// so a module-wide walk would accept a subscribe in SettingsSurface.render as satisfying
// PhoneSurface's. Its documented limit still applies: it cannot cross a property initialiser.
//
// NOTHING HERE CLAIMS PB-E2E-5's DEFERRED SET. These are assertions about source. That a real
// handset delivers an event, that a real Keystore answers a capability probe, that a real
// biometric gates anything -- none of it is claimed.

import (
	"path/filepath"
	"strings"
	"testing"
)

func phoneSurfacePath(t *testing.T) string {
	return filepath.Join(kotlinMainRoot(t),
		filepath.FromSlash("dev/swarm/phone/PhoneSurface.kt"))
}

func phoneRuntimePath(t *testing.T) string {
	return filepath.Join(kotlinMainRoot(t),
		filepath.FromSlash("dev/swarm/phone/PhoneRuntime.kt"))
}

// reachableInFile walks from entry within one file's declarations.
func reachableInFile(t *testing.T, path, entry string, depth int) string {
	t.Helper()
	src := stripKotlinComments(readFileOrFail(t, path, "PB-APP-3"))
	bodies := s17BodiesIn(src)
	if len(bodies) == 0 {
		t.Fatalf("the Kotlin reader found no function bodies in %s at all. Every assertion "+
			"below would then pass or fail for a reason that has nothing to do with the code",
			mustRel(t, path))
	}
	body, ok := s17ReachableIn(bodies, entry, depth)
	if !ok {
		t.Fatalf("%s declares no `%s`. The assertions in this file are about what that callback "+
			"reaches, and a renamed lifecycle hook must fail here rather than take its fences "+
			"with it (defect class (iii))", mustRel(t, path), entry)
	}
	return body
}

// ---------------------------------------------------------------------------
// residuals §2.9: the app could not observe.
// ---------------------------------------------------------------------------

// TestWiring_TheScreenComingToTheFrontStartsObserving.
//
// `SetEventListener`, `SubscribeJournal` and `TerminalWatch` appeared ZERO times in ALL Kotlin
// -- main, test and androidTest alike. So no listener was installed, journal delivery never
// started, and the machine was never asked to send terminal frames. `FacadeBridge.terminalPeek`
// reads `App.Peek`, which reads `Router().Snapshots()` -- a LOCAL cache that only a watched
// session fills -- so the peek was permanently empty and failed looking exactly like a quiet
// machine. PB-APP-3/4/5 were non-functional in the shipping app.
func TestWiring_TheScreenComingToTheFrontStartsObserving(t *testing.T) {
	body := reachableInFile(t, phoneSurfacePath(t), "render", 4)

	for _, want := range []struct{ verb, why string }{
		{"SetEventListener", "no listener is installed, so no event the core emits can reach " +
			"a screen and the roster never updates after the first draw"},
		{"SubscribeJournal", "journal delivery never starts: mobile/relay.go's onJournal " +
			"returns early unless `subscribed` is set, so every record is folded into durable " +
			"state and none is announced"},
		// `TerminalWatch` STOOD HERE AND IS DELETED WITH THE WELL, not merely dropped from a
		// list. It was required because a peek over an unwatched session is empty forever and
		// fails looking exactly like a quiet machine. ADR-009-structured-chat-interaction (2)
		// answers that a different way: "no phone surface issues a watch", because there is no
		// grid left to fill -- and (7) makes it load-bearing rather than tidy, since the
		// machine-to-phone budget is <= 8 appends/s across journal AND terminal combined for one
		// target and the transcript now inherits the whole of it. The INVERSION is asserted
		// below rather than the requirement merely removed: a check that only stopped asking
		// would pass again the day somebody re-added the call.
	} {
		if !s17NamesVerb(body, want.verb) {
			t.Errorf("PB-APP-3/4/5: nothing reachable from PhoneSurface.render calls "+
				"App.%s.\n%s\n"+
				"The verb existing is not the property: it existed, was unit-tested, and was "+
				"traced in mobile/screen_coverage.tsv while the app could not reach it.\n"+
				"reachable from render:\n%s", want.verb, want.why, s17Indent(body))
		}
	}
}

// TestWiring_TheScreenLeavingWithdrawsWhatItAskedFor.
//
// ADR-007 B16: backgrounding DISCONNECTS, and the phone is reached by a push wake instead. Both
// requests below are work the MACHINE is doing on this phone's behalf -- per-session terminal
// rendering, and journal delivery into a queue nothing drains -- so a screen that subscribes
// and never withdraws leaks them for every session the user ever opened. `TerminalUnwatch`'s
// own doc says so in as many words.
func TestWiring_TheScreenLeavingWithdrawsWhatItAskedFor(t *testing.T) {
	body := reachableInFile(t, phoneSurfacePath(t), "release", 3)

	// **This is a sanctioned change to a passing gate, and a fence mandated it.** The
	// withdrawal used to be an inline `live.unsubscribeJournal()` this walk saw directly;
	// committee round 3 moved the whole background chunk onto VerbDispatch's command lane
	// (android/gate/s25r3_releasepath_test.go -- App.Stop beside it joins the drain's
	// five-second close, which may not run on the looper). The property is unchanged and is
	// asserted link by link across the seam: release reaches the lane's background verb; the
	// lane's background verb withdraws journal delivery before it stops; the binding's
	// withdrawal is the facade call itself.
	if !strings.Contains(body, ".background(") {
		t.Errorf("PB-RUN-3/ADR-007 B16: nothing reachable from PhoneSurface.release calls "+
			"LifecycleLane.background, so the screen leaving withdraws nothing.\n"+
			"reachable from release:\n%s", s17Indent(body))
	}
	lane := stripKotlinComments(readFileOrFail(t,
		filepath.Join(kotlinMainRoot(t), filepath.FromSlash("dev/swarm/phone/LifecycleLane.kt")),
		"PB-RUN-3"))
	bg := kotlinMember(t, lane, "fun background(")
	if !strings.Contains(bg, ".unsubscribeJournal(") {
		t.Errorf("PB-RUN-3/ADR-007 B16: LifecycleLane.background does not withdraw journal "+
			"delivery: a backgrounded screen goes on being fed events it will never render, "+
			"into the bounded drop-oldest queue mobile/events.go documents.\n%s", s17Indent(bg))
	}
	// **Committee round 4 (Opus F5), measured by mutation**: deleting `handle.stop()` from
	// LifecycleLane.background left EVERY Go gate green, because the order check below only
	// fires when a stop exists (`s >= 0`). The stop is load-bearing: App.Stop joins the relay
	// drain's five-second graceful close (LifecycleHandle.stop's own doc), so a
	// disconnecting screen that never stops leaves the link alive behind a phone the user
	// has left. Presence is required FIRST; only then does order mean anything.
	if !strings.Contains(bg, ".stop(") {
		t.Errorf("PB-RUN-3/ADR-007 B16: LifecycleLane.background never stops the link: a "+
			"disconnecting screen that withdraws its subscription but keeps the socket leaves "+
			"the relay session connected with no screen behind it.\n%s", s17Indent(bg))
	}
	if u, s := strings.Index(bg, ".unsubscribeJournal("), strings.Index(bg, ".stop("); s >= 0 && u > s {
		t.Errorf("PB-RUN-3/ADR-007 B16: LifecycleLane.background stops the link BEFORE "+
			"withdrawing journal delivery; the withdrawal must go while there is still a "+
			"socket to withdraw it over.\n%s", s17Indent(bg))
	}
	binding := stripKotlinComments(readFileOrFail(t,
		filepath.Join(kotlinMainRoot(t), filepath.FromSlash("dev/swarm/phone/AppLifecycle.kt")),
		"PB-RUN-3"))
	member := kotlinMember(t, binding, "fun unsubscribeJournal(")
	// The DOTTED shape, not s17NamesVerb: the member string begins with the wrapper's own
	// `fun unsubscribeJournal(` declaration, which satisfies the undotted match -- the
	// declaration-satisfies-the-grep defect r8's rule 4 names, measured here by mutation
	// (a hollowed wrapper kept this gate green until the dot).
	if !strings.Contains(member, ".unsubscribeJournal(") {
		t.Errorf("PB-RUN-3/ADR-007 B16: AppLifecycle.unsubscribeJournal does not call "+
			"App.UnsubscribeJournal, so the chain above ends in a wrapper that withdraws "+
			"nothing.\n%s", s17Indent(member))
	}
	// `TerminalUnwatch` STOOD HERE, and it goes with its other half: a phone that opens no
	// watch leaks none. See the note in the test above, and the inversion below.
}

// TestWiring_NoScreenIssuesATerminalWatch is the AMENDMENT the two tests above record, stated as
// its own assertion so the deletion is a rule rather than an omission.
//
// **This is a sanctioned change to a passing gate, and the design mandated it.**
// `docs/adr/ADR-009-structured-chat-interaction.md` (2): "no phone surface issues a watch ...
// `TerminalSnapshot` and `terminal_watch` stay on the wire unchanged -- no protocol change,
// nothing deleted -- but no phone surface issues a watch, and the machine->phone append budget in
// (7) is spent by the journal alone". The verbs stay exported and ledgered in
// android/unbound-verbs.tsv; what may not come back is this app asking for frames it no longer
// draws.
func TestWiring_NoScreenIssuesATerminalWatch(t *testing.T) {
	kotlin := kotlinCodeOnly(appKotlinSource(t))

	for _, verb := range []string{"terminalWatch", "terminalUnwatch"} {
		if strings.Contains(kotlin, "."+verb+"(") {
			t.Errorf("ADR-009 (2): production Kotlin calls App.%s.\n"+
				"With no grid on any screen, a watch spends the machine->phone append budget on "+
				"frames nothing draws -- and PB-SYNC-1 stales journal AND terminal together on a "+
				"shared-bucket gap, so the cost lands on the transcript. The verb is deliberately "+
				"still exported: (2) narrows the renderer's ROLE and withdraws nothing from the "+
				"wire. Re-adding the call needs an amendment to that ADR, not a commit.",
				strings.ToUpper(verb[:1])+verb[1:])
		}
	}
}

// TestWiring_TheListenerOutlivesNoScreen.
//
// `SetEventListener` has no un-set that does not cross a null through JNI, and `PhoneRuntime`
// caches the built App across Activity instances -- so a listener that captured the Activity
// would keep it reachable for the life of the process, one per rotation. The shape that avoids
// it is a process-lived listener holding a replaceable sink, which is what `PhoneEvents` is;
// this asserts the pause path clears the SINK, since clearing the listener is what cannot be
// done.
func TestWiring_TheListenerOutlivesNoScreen(t *testing.T) {
	src := stripKotlinComments(appKotlinSource(t))
	if len(kotlinImplementsSupertype(src, "EventListener")) == 0 {
		t.Fatalf("PB-APP-3: no production Kotlin implements swarmmobile.EventListener, so " +
			"App.SetEventListener has nothing to be given")
	}
	body := reachableInFile(t, phoneSurfacePath(t), "release", 3)
	if !strings.Contains(body, "stopObserving(") {
		t.Errorf("PB-RUN-3: PhoneSurface.release does not clear the event sink.\n"+
			"The listener itself cannot be un-installed, so this is the only thing between a "+
			"paused Activity and a redraw against views it no longer owns -- and between the "+
			"phone core and a permanent reference to a dead Activity.\n"+
			"reachable from release:\n%s", s17Indent(body))
	}
}

// ---------------------------------------------------------------------------
// residuals §2.10(a): the custody capability gate was never invoked.
// ---------------------------------------------------------------------------

// TestWiring_TheCapabilityGateIsAsked.
//
// `CustodyPlanner.forDevice` had NO production caller: `PhoneRuntime.construct()` went straight
// to `KeystoreCustodyBootstrap` without building a capability map, so PB-KEY-8's defined
// refusal was a fully-tested pure function nothing invoked, and
// `KeyCustodyException.PlatformCapabilityMissing` was declared, routed by `routeStartupFailure`
// and never thrown. The shipped app refused no handset over any capability -- which also made
// physical-handset runbook step 2c inert.
func TestWiring_TheCapabilityGateIsAsked(t *testing.T) {
	body := reachableInFile(t, phoneRuntimePath(t), "attach", 3)

	if !strings.Contains(body, "forDevice(") {
		t.Errorf("PB-KEY-8: nothing reachable from PhoneRuntime.attach calls "+
			"CustodyPlanner.forDevice.\nThe planner decides the requirement's \"defined refusal "+
			"when the handset lacks the required algorithm or auth capability\", and a decision "+
			"nobody asks for is not a gate.\nreachable from attach:\n%s", s17Indent(body))
	}
	if !strings.Contains(body, "PlatformCapabilityMissing(") {
		t.Errorf("PB-KEY-8: nothing reachable from PhoneRuntime.attach throws " +
			"KeyCustodyException.PlatformCapabilityMissing. A CustodyPlan.Refused that is " +
			"computed and then dropped is the same as no gate, and worse: the type, its " +
			"recovery mapping and its DEVICE_UNSUPPORTED routing all exist and read as live.")
	}
}

// TestWiring_TheAnomalyHasAReader is the non-fatal half, and it is the half that is easy to
// skip: an anomaly is BY DESIGN not a refusal, so nothing breaks if it goes nowhere. A record
// computed on every launch and read by nobody is the same defect class one layer along.
func TestWiring_TheAnomalyHasAReader(t *testing.T) {
	src := stripKotlinComments(appKotlinSource(t))
	if !strings.Contains(src, "CapabilityNotice") {
		t.Fatalf("PB-KEY-8: production Kotlin has nothing that turns a CapabilityAnomaly into " +
			"something a person reads")
	}
	body := reachableInFile(t, phoneSurfacePath(t), "render", 4)
	if !strings.Contains(body, "CapabilityNotice") {
		t.Errorf("PB-KEY-8: nothing reachable from PhoneSurface.render renders the capability "+
			"anomalies.\nThey are recorded on CustodyPlan.Provisioned precisely BECAUSE they are "+
			"not worth refusing a phone over (residuals §2.8) -- which makes a reader the only "+
			"thing that distinguishes recording them from discarding them.\n"+
			"reachable from render:\n%s", s17Indent(body))
	}
}
