package swarmmobile

// WAVE R8 / ROUND 2 -- T8-b HAS A VERB, AND THE VERB IS REACHED (round-2 MAJOR 6).
//
// ADR-017 amendment T8-b: "Backgrounding is a severance trigger in its own right,
// independent of transport." Round 1 landed both mechanisms -- `TerminalControlState.
// Background` and `LeaseState.Background` -- and NEITHER HAD A PRODUCTION CALLER. Neither
// appeared on the facade at all, so no Kotlin could reach one, and `PhoneActivity.onPause`
// documented in so many words that it "reaches no facade verb". Backgrounding therefore
// still severed only BY CONSEQUENCE of the disconnect it forces, which is the precise answer
// T8-b was written to replace.
//
// The composite worst case the review named is what makes this a security fence rather than
// a completeness one: a BACKGROUNDED app -- or malware holding the generation -- keeps
// sending keepalives and retains raw-input authority over the owner's live terminal, with no
// screen displaying the persistent banner T6 requires. The daemon-side half of that is fixed
// separately (the signed horizon no longer moves, and the server sweeps an idle generation);
// this is the phone's own half of the same rule.

import (
	"os"
	"strings"
	"testing"
)

// readRepoFile reads a file outside this package -- the Android surface -- as text. The
// wiring claim spans two languages, so the fence has to as well.
func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// stripKotlinComments blanks line and block comments so a fence measures CODE. Without it a
// commented-out call still matches, and "the app severs on backgrounding" would be satisfied
// by a line that does nothing -- which is exactly what a mutation probe does.
func stripKotlinComments(src string) string {
	var b strings.Builder
	for {
		i := strings.Index(src, "//")
		j := strings.Index(src, "/*")
		switch {
		case i < 0 && j < 0:
			b.WriteString(src)
			return b.String()
		case j < 0 || (i >= 0 && i < j):
			b.WriteString(src[:i])
			nl := strings.IndexByte(src[i:], '\n')
			if nl < 0 {
				return b.String()
			}
			src = src[i+nl:]
		default:
			b.WriteString(src[:j])
			end := strings.Index(src[j:], "*/")
			if end < 0 {
				return b.String()
			}
			src = src[j+end+2:]
		}
	}
}

// kotlinMemberBody returns the source of one Kotlin member, from its declaration to the
// first line that is exactly four spaces and a closing brace -- ktlint guarantees that shape
// for a class member, and the Go funcBody helper's "\nfunc " delimiter finds nothing in
// Kotlin and would silently return the whole rest of the file.
func kotlinMemberBody(t *testing.T, src, decl string) string {
	t.Helper()
	i := strings.Index(src, decl)
	if i < 0 {
		t.Fatalf("the Kotlin source declares no %s", decl)
	}
	rest := src[i:]
	j := strings.Index(rest, "\n    }\n")
	if j < 0 {
		t.Fatalf("could not find the end of %s", decl)
	}
	return rest[:j]
}

// TestR8Background_TheFacadeExposesADirectSeveranceVerb is the surface half. The verb must
// exist on `App`, or the Android lifecycle has nothing to call.
func TestR8Background_TheFacadeExposesADirectSeveranceVerb(t *testing.T) {
	golden := readMobileSource(t, "testdata/exported_surface.golden")
	if !strings.Contains(golden, "App.EnterBackground") {
		t.Fatalf("the pinned exported surface carries no backgrounding verb. Both Background " +
			"methods exist in phonecore and neither is reachable from Kotlin, so T8-b is a ruling " +
			"the app cannot obey.")
	}
}

// TestR8Background_TheVerbSeversBothPlanesAndNeitherByConsequence is the CONTENT half, and it
// is written over the body so that "it exists" and "it does the two things" are separate
// assertions. Both planes are named because they are deliberately separate (OPEN-C4): a
// backgrounding that ended the control LEASE and left a raw-input GENERATION live would leave
// the one surface where raw bytes travel still armed with no screen displaying it.
func TestR8Background_TheVerbSeversBothPlanesAndNeitherByConsequence(t *testing.T) {
	body := funcBody(readMobileSource(t, "app.go"), "func (a *App) EnterBackground(")
	if body == "" {
		t.Fatalf("mobile/app.go declares no App.EnterBackground")
	}
	for _, want := range []struct{ sym, why string }{
		{"Leases().Background(", "the control lease plane (PB-INPUT-2's own trigger list)"},
		{"TerminalControl().Background(", "the terminal control generation, which is a SEPARATE plane"},
		{"coalesce.Abandon(", "the held bytes, dropped rather than flushed (T6-f)"},
	} {
		if !strings.Contains(body, want.sym) {
			t.Errorf("App.EnterBackground does not call %s -- %s. T8-b is a DIRECT trigger, so every "+
				"authority the app holds goes at once and none of them waits for a transport event.",
				want.sym, want.why)
		}
	}
	// IT MUST NOT REQUIRE A LIVE CONNECTION. `ready()` refuses a stopped app, and a severance
	// verb that refuses on the way out is a severance verb that does not run on the one path
	// it exists for.
	if strings.Contains(body, "a.ready()") {
		t.Errorf("App.EnterBackground gates on a.ready(). The whole point of this verb is that it " +
			"runs while the app is leaving; a stopped or unstarted app has authority to withdraw " +
			"just the same, and refusing here re-creates the by-consequence answer T8-b replaces.")
	}
}

// TestR8Background_TheAndroidLifecycleReachesIt is the wiring half, and it is the one the
// review's finding was actually about: the mechanism existed, the surface did not, and the
// lifecycle called neither.
func TestR8Background_TheAndroidLifecycleReachesIt(t *testing.T) {
	// **This is a sanctioned change to a passing gate, and a fence mandated it** (committee
	// round 3, the onPause finding). The severance used to be an inline
	// `live.enterBackground()` this test read straight out of release(); that whole chunk now
	// rides VerbDispatch's command lane, because the `live.stop()` BESIDE it joins the relay
	// drain's five-second graceful close and may not run on the main looper
	// (android/gate/s25r3_releasepath_test.go). The two properties this test owns are
	// unchanged and are asserted link by link across the seam:
	//
	//  1. REACH: onPause -> release() -> LifecycleLane.background -> the handle's
	//     enterBackground -> App.EnterBackground.
	//  2. NOT BEHIND THE POLICY: the connectivity policy feeds only the lane's `disconnect`
	//     flag; inside the lane's work the severance runs BEFORE the disconnect guard, so a
	//     build that kept the socket open in the background still severs. (The behavioural
	//     half -- a sever-only background still calls enterBackground -- is
	//     LifecycleLaneTest's `a_sever_only_background_neither_unsubscribes_nor_stops...`.)
	surface := readRepoFile(t, "../android/app/src/main/kotlin/dev/swarm/phone/PhoneSurface.kt")
	release := stripKotlinComments(kotlinMemberBody(t, surface, "fun release() {"))
	if !strings.Contains(release, ".background(") {
		t.Fatalf("PhoneSurface.release() -- the function PhoneActivity.onPause calls -- does not " +
			"reach LifecycleLane.background, so nothing on the backgrounding path severs. A call " +
			"somewhere else in the file is not the backgrounding path.")
	}
	lane := readRepoFile(t, "../android/app/src/main/kotlin/dev/swarm/phone/LifecycleLane.kt")
	background := stripKotlinComments(kotlinMemberBody(t, lane, "fun background("))
	severAt := strings.Index(background, ".enterBackground(")
	if severAt < 0 {
		t.Fatalf("LifecycleLane.background never calls enterBackground(), so the lane release() " +
			"rides withdraws nothing.")
	}
	// The disconnect guard is the policy's only reach into this function: a severance INSIDE
	// its block (or after it) is the by-consequence answer wearing the new verb's name. The
	// BRACED guard, deliberately: the single-statement `if (disconnect) started = null` above
	// the enqueue clears eager state and can contain no call.
	if guardAt := strings.Index(background, "if (disconnect) {"); guardAt >= 0 && severAt > guardAt {
		t.Fatalf("LifecycleLane.background severs only under the disconnect guard. T8-b is " +
			"'independent of transport': gating the severance on a connectivity decision is exactly " +
			"the by-consequence answer, restated.")
	}
	binding := readRepoFile(t, "../android/app/src/main/kotlin/dev/swarm/phone/AppLifecycle.kt")
	member := stripKotlinComments(kotlinMemberBody(t, binding, "fun enterBackground() {"))
	if !strings.Contains(member, ".enterBackground(") {
		t.Fatalf("AppLifecycle.enterBackground does not call App.EnterBackground, so the chain " +
			"above ends in a wrapper that severs nothing. (Dotted on purpose: the member's own " +
			"declaration must not satisfy this.)")
	}
}
