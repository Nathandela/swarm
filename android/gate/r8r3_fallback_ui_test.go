package gate

// WAVE R8 / ROUND 3 -- THE TWO PHONE-SIDE FINDINGS ROUND 2 LEFT OPEN.
//
// MODERATE 5. `DefaultWatchHorizon` is 60 seconds and `reconcileTerminalWatch`'s renewal is
// issued only from `drawContent` -- its own comment says "the renewal is deliberately driven
// by the redraw rather than by a timer". But `PhoneSurface.render()` has exactly one
// lifecycle caller (`PhoneActivity.onResume`) plus `PhoneEvents.observe { render() }`, and
// there is no `postDelayed`, `ScheduledExecutor` or coroutine tick anywhere in the phone's
// main Kotlin except `PairingSurface`'s own poller. So an IDLE fallback screen on an IDLE
// session has NO GUARANTEED REDRAW inside the horizon: the machine reaps a watch the user is
// looking at, the peek ends from the gateway side so nothing blanks, and the screen keeps a
// dead grid it labels fresh.
//
// The renewal therefore gets a tick of its own -- and the tick is BOUND TO THE LIVE
// FOREGROUND SCREEN exactly as `keepAlive` is (T6-c): it is started only where a fallback is
// on the glass, cancelled in `release()`, and it renews rather than re-watching, so it can
// never acquire a peek the capability gate did not open. That is the distinction T6-c draws
// and this file pins: a timer that outlives the composition is banned; a timer the
// composition owns and cancels IS the composition.
//
// MODERATE 6. `App.TerminalControlBegin`, `TerminalControlEnd`, `TerminalControlKeepalive`
// and `TerminalInput` read as BOUND because `TerminalFallbackBinding`'s wrappers name them --
// while `beginControl`, `keepAlive` and `.type(` have ZERO production callers and the only
// `releaseControl` hit is `PhoneSurface`'s LEASE verb of the same name. That is verbatim the
// hole `TestBoundVerbs_EveryBridgeMethodIsReachableOrLedgered` exists to close one layer up,
// on a class the ledger had never been pointed at. The ledger's dimensions extend to the
// binding, and the four unreached wrappers get honest rows.

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	r8PhoneSurface  = "dev/swarm/phone/PhoneSurface.kt"
	r8PhoneActivity = "dev/swarm/phone/PhoneActivity.kt"
)

// TestR8R3Gate_AnIdleFallbackScreenKeepsItsWatchAcrossAHorizon is MODERATE 5's phone half.
//
// It asserts three things together, because any one alone is satisfiable by something that
// does not work: a tick EXISTS, it is CANCELLED when the screen goes away, and it RENEWS
// rather than re-watching.
func TestR8R3Gate_AnIdleFallbackScreenKeepsItsWatchAcrossAHorizon(t *testing.T) {
	code := kotlinCodeOnly(r8AllProductionKotlin(t)[r8PhoneSurface])
	if code == "" {
		t.Fatalf("%s does not exist", r8PhoneSurface)
	}
	if !regexp.MustCompile(`postDelayed\s*\(`).MatchString(code) {
		t.Errorf("ADR-017 T4-b: %s schedules nothing. The watch renewal rides the redraw, and an "+
			"IDLE fallback screen on an IDLE session has no guaranteed redraw inside the 60s "+
			"horizon -- so the machine reaps a watch the user is looking at, the peek ends from "+
			"the gateway side (no OpError, no blanking frame), and the screen keeps a dead grid "+
			"with no staleness signal, because StreamStale is set by desync events and the "+
			"machine heartbeat keeps arriving.", r8PhoneSurface)
	}
	// The tick must be TORN DOWN. A renewal that outlives the composition is a machine
	// rendering for nobody, which is the whole cost T4-b's horizon exists to stop.
	rel := kotlinMember(t, code, "fun release(")
	if !regexp.MustCompile(`removeCallbacks`).MatchString(rel) &&
		!regexp.MustCompile(`removeCallbacks`).MatchString(kotlinMember(t, code, "private fun reconcileTerminalWatch(")) {
		t.Errorf("ADR-017 T6-c: %s starts a renewal tick and cancels it nowhere reachable from "+
			"release(). A tick that outlives the screen is exactly the background emitter the "+
			"ruling bans: it holds the machine rendering, sealing and appending with no screen "+
			"displaying anything.", r8PhoneSurface)
	}
	// It must RENEW, not re-watch: a renewal never STARTS a watch, so a tick cannot acquire a
	// peek the capability gate did not open.
	tick := r8r3RenewTickBody(t, code)
	if tick == "" {
		t.Fatalf("ADR-017 T4-b: %s has no identifiable renewal tick. The scan looks for a member "+
			"named `scheduleWatchRenewal`, `renewTick` or `watchRenewalTick`.", r8PhoneSurface)
	}
	if strings.Contains(tick, ".watch()") {
		t.Errorf("ADR-017 T4-b: the renewal tick in %s calls `watch()`. A tick that can OPEN a "+
			"watch is a tick that can acquire a peek with no user on the screen; renewal is the "+
			"only thing a clock may do here.", r8PhoneSurface)
	}
	// AND IT MUST BE STARTED where a watch is taken. A tick nothing arms is a member that
	// exists, is unit-testable and never runs -- this phase's standing defect class, in Kotlin.
	if !strings.Contains(kotlinMember(t, code, "private fun reconcileTerminalWatch("),
		"scheduleWatchRenewal()") {
		t.Errorf("ADR-017 T4-b: reconcileTerminalWatch in %s takes a watch and arms no renewal "+
			"tick, so the horizon still depends on a redraw that an idle screen does not "+
			"produce.", r8PhoneSurface)
	}
	// The renewal itself, allowing ONE hop -- the tick may call a named renewer -- because the
	// alternative is a gate that forces the whole body to be inlined into a lambda.
	if !r8r3ReachesRenew(t, code, tick) {
		t.Errorf("ADR-017 T4-b: the renewal tick in %s never reaches `renew()`. A clock that "+
			"schedules nothing the machine can hear is a clock that lets the horizon lapse "+
			"exactly as before.", r8PhoneSurface)
	}
}

// r8r3ReachesRenew answers whether the tick calls `renew()` directly or through one named
// member of the same file that does.
func r8r3ReachesRenew(t *testing.T, code, tick string) bool {
	t.Helper()
	if strings.Contains(tick, "renew()") {
		return true
	}
	for _, m := range regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*\(`).FindAllStringSubmatch(tick, -1) {
		decl := "private fun " + m[1] + "("
		if !strings.Contains(code, decl) {
			continue
		}
		if strings.Contains(kotlinMember(t, code, decl), "renew()") {
			return true
		}
	}
	return false
}

// r8r3RenewTickBody returns the body of the member that carries the renewal tick.
func r8r3RenewTickBody(t *testing.T, code string) string {
	t.Helper()
	for _, decl := range []string{
		"private fun scheduleWatchRenewal(",
		"private fun renewTick(",
		"private fun watchRenewalTick(",
	} {
		if strings.Contains(code, decl) {
			return kotlinMember(t, code, decl)
		}
	}
	return ""
}

// TestR8R3Gate_TheRenewalTickIsNotAKeepaliveEmitter draws the line the tick must not cross.
// T6-c bans a background emitter for `terminal_control_keepalive` specifically because a
// generation held open with no screen displaying it defeats the persistent banner and the
// leave-screen trigger together. A watch grants NO INPUT AUTHORITY (T4), so a watch renewal
// on a clock is a different thing -- and this asserts the two never merge.
func TestR8R3Gate_TheRenewalTickIsNotAKeepaliveEmitter(t *testing.T) {
	code := kotlinCodeOnly(r8AllProductionKotlin(t)[r8PhoneSurface])
	tick := r8r3RenewTickBody(t, code)
	if tick == "" {
		t.Fatalf("no renewal tick in %s", r8PhoneSurface)
	}
	for _, banned := range []string{"keepAlive(", "beginControl(", ".type(", "terminalInput("} {
		if strings.Contains(tick, banned) {
			t.Errorf("ADR-017 T6-c: the watch renewal tick in %s emits `%s`. A watch grants no "+
				"input authority and its clock must not acquire any: the keepalive is bound to "+
				"the live foreground composition that owns terminal_input, and a timer is "+
				"precisely what that ruling excludes.", r8PhoneSurface, banned)
		}
	}
}

// TestR8R3Gate_TheFallbackBindingsVerbsAreReachedOrLedgered is MODERATE 6.
//
// The ledger's second dimension exists because "a bridge method nothing calls makes its verbs
// read as CALLED while the app can no more reach them than before". `TerminalFallbackBinding`
// is a second such adapter -- the ONE place the fallback screen meets the facade -- and the
// ledger had never been pointed at it. Four of its wrappers have no caller, and between them
// they carry all four newly exported control verbs.
func TestR8R3Gate_TheFallbackBindingsVerbsAreReachedOrLedgered(t *testing.T) {
	declared := r8r3BindingMethods(t)
	if len(declared) < 6 {
		t.Fatalf("the scan found %d public methods on TerminalFallbackBinding, want at least 6. "+
			"It has stopped reading %s, and a reachability question over nothing passes "+
			"vacuously", len(declared), r8FallbackScreen)
	}
	files := r8AllProductionKotlin(t)
	var elsewhere strings.Builder
	for name, src := range files {
		if name == r8FallbackScreen {
			continue
		}
		elsewhere.WriteString(kotlinCodeOnly(src))
		elsewhere.WriteString("\n")
	}
	ledger := ledgerIndex(readUnboundLedger(t))
	var faults []string
	for _, m := range declared {
		if r8r3BindingCall(elsewhere.String(), m) {
			continue
		}
		if _, excused := ledger["TerminalFallbackBinding."+m]; excused {
			continue
		}
		faults = append(faults, m)
	}
	sort.Strings(faults)
	for _, m := range faults {
		t.Errorf("TerminalFallbackBinding.%s is reached from no production Kotlin outside %s, and "+
			"has no `TerminalFallbackBinding.%s` row in android/unbound-verbs.tsv.\n"+
			"This class is the one adapter between the fallback screen and the facade, so a "+
			"wrapper nothing reaches makes the App verb it names read as BOUND while the app "+
			"cannot get to it -- which is the standing defect class the ledger exists for, on a "+
			"receiver it had never been pointed at.", m, r8FallbackScreen, m)
	}
}

// TestR8R3Gate_TheLedgerCannotExcuseAReachedBindingMethod is the rot check for the new
// dimension: once a wrapper acquires a caller, its row must go, or the file grows forever.
func TestR8R3Gate_TheLedgerCannotExcuseAReachedBindingMethod(t *testing.T) {
	declared := r8r3BindingMethods(t)
	files := r8AllProductionKotlin(t)
	var elsewhere strings.Builder
	for name, src := range files {
		if name == r8FallbackScreen {
			continue
		}
		elsewhere.WriteString(kotlinCodeOnly(src))
		elsewhere.WriteString("\n")
	}
	declaredSet := map[string]bool{}
	for _, m := range declared {
		declaredSet[m] = true
	}
	for _, r := range readUnboundLedger(t) {
		name, ok := strings.CutPrefix(r.Symbol, "TerminalFallbackBinding.")
		if !ok {
			continue
		}
		if !declaredSet[name] {
			t.Errorf("android/unbound-verbs.tsv:%d excuses %s, which %s no longer declares",
				r.Line, r.Symbol, r8FallbackScreen)
			continue
		}
		if r8r3BindingCall(elsewhere.String(), name) {
			t.Errorf("android/unbound-verbs.tsv:%d excuses %s as deliberately unbound, and "+
				"production Kotlin now reaches it. The row's REASON is prose that stops being "+
				"true when the wiring lands, and a stale reason in an exemption file is worse "+
				"than a missing one.", r.Line, r.Symbol)
		}
	}
}

// r8r3FacadeReceivers are the spellings production Kotlin uses for the BOUND FACADE handle.
// A call on one of them is a call to `swarmmobile.App`, never to the binding.
//
// THE COLLISION IS MEASURED, NOT HYPOTHETICAL, and it is the whole reason this dimension
// cannot reuse `namesKotlinCall`. `App.ReleaseControl` is the LEASE verb, PhoneSurface calls
// `app.releaseControl(target)` on it, and `TerminalFallbackBinding.releaseControl` happens to
// share the name -- so a bare name match reports the binding's wrapper as reached while
// nothing in the app can get to it. That is precisely the "reads as bound, cannot be reached"
// shape the ledger exists to catch, and a check that fell for it would be excusing the defect
// it was written to find.
var r8r3FacadeReceivers = []string{"app", "live", "core", "facade"}

// r8r3BindingCall reports whether src calls name on something that is NOT the bound facade.
func r8r3BindingCall(src, name string) bool {
	re := regexp.MustCompile(`(?:^|[^A-Za-z0-9_.])([A-Za-z0-9_]*)\s*\??\.\s*` +
		regexp.QuoteMeta(name) + `\s*\(`)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		facade := false
		for _, recv := range r8r3FacadeReceivers {
			if m[1] == recv {
				facade = true
				break
			}
		}
		if !facade {
			return true
		}
	}
	// A receiver-less call (`renew()` inside a class that IS the binding) cannot occur outside
	// the declaring file, so only the dotted form is considered above.
	return false
}

// r8r3BindingMethods is every public method on TerminalFallbackBinding, read from the screen's
// own source. It is derived rather than listed for boundAppVerbs' reason: a list would have to
// be edited by the same person adding the wrapper.
func r8r3BindingMethods(t *testing.T) []string {
	t.Helper()
	src, ok := r8AllProductionKotlin(t)[r8FallbackScreen]
	if !ok {
		t.Fatalf("%s does not exist", r8FallbackScreen)
	}
	code := kotlinCodeOnly(src)
	i := strings.Index(code, "class TerminalFallbackBinding")
	if i < 0 {
		t.Fatalf("%s declares no TerminalFallbackBinding", r8FallbackScreen)
	}
	body := code[i:]
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^\s{4}fun\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`).
		FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}
