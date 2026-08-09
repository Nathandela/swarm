package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nx44.4(c): the ONE-SHOT latch that makes a
// SECOND pairing in the same Activity do nothing at all.
//
// WHAT THE LATCH IS FOR AND WHAT IT COSTS. `PairingSurface.renderReady` runs one block when the
// flow reaches PAIRED: `runtime.rebuildAfterPairing()` (the App that ran the pairing was built
// before its relay URL was known) and then `onPaired()`, which `PhoneSurface` assigns `::render`
// so the window swaps at the moment it happens. Both are guarded by a field set true the first
// time and reset by nothing, so they run ONCE PER PROCESS.
//
// A SECOND PAIRING IS NOT AN EDGE CASE, it is the recovery path this product documents. The
// owner revokes the handset (or the machine is replaced); `PhoneSurface` sends the phone to the
// pair-only screen; the user pairs again from that very screen -- same Activity, same latch.
// Neither call runs, so the whole-window redraw never happens, and the two clears that redraw
// performs (`settings.unpairNotice = ""` and `runtime.latchRevoke("")`, PhoneSurface's
// "the revoke's divergence is spent here and nowhere else") never happen either. The handset
// sits on REVOKE_UNCONFIRMED, successfully paired, until the Android process is rebuilt. That is
// the brick PB-STATE-10 is named for, reached through the remedy.
//
// WHY IT IS A GO GATE. `PhoneRuntime.phone()` answers `PhoneStartup.Unavailable` on every JVM
// run -- the phone core is a gomobile AAR of .so files cross-compiled for Android ABIs -- so
// `renderReady` never reaches its PAIRED arm under Robolectric and no unit test can drive a
// pairing to completion, let alone two. What a test CAN see is the source: whether the latch
// guarding that arm is cleared where a new attempt begins.
//
// EITHER FIX SATISFIES THIS FENCE, because either ends the defect: clear the latch on the lane
// that begins a pairing, or stop latching the arm at all. What it refuses is a latch nothing
// clears.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. The scan is one named file.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const nx444PairingSurface = "dev/swarm/phone/PairingSurface.kt"

// nx444Rebuild is the call the latch guards: the App swap a completed pairing owes.
var nx444Rebuild = regexp.MustCompile(`\brebuildAfterPairing\s*\(`)

// nx444BeginsAnAttempt are the two facade verbs that START a pairing. Either one means the
// function it is in is the lane a new attempt arrives on, whichever spelling the payload took
// (ADR-007 B140's two entry points: the scanned payload and the typed code).
var nx444BeginsAnAttempt = regexp.MustCompile(`\bbeginPairing(WithCode)?\s*\(`)

// nx444Negated finds the identifiers a condition reads as `!name`.
var nx444Negated = regexp.MustCompile(`!\s*([A-Za-z_][A-Za-z0-9_]*)\b`)

// nx444LatchDeclaration matches the declaration of a boolean field that starts false -- the
// shape a one-shot latch has, and the one the scan must not mistake for a clear.
func nx444LatchDeclaration(name string) *regexp.Regexp {
	return regexp.MustCompile(`\b(?:var|val)\s+` + regexp.QuoteMeta(name) + `\s*=\s*false\b`)
}

// nx444Clear matches an ASSIGNMENT of false to name.
func nx444Clear(name string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*=\s*false\b`)
}

// nx444GuardOf returns the condition text of the nearest `if (` preceding at -- the guard on the
// block that call sits in.
func nx444GuardOf(code string, at int) (string, bool) {
	open := strings.LastIndex(code[:at], "if (")
	if open < 0 {
		return "", false
	}
	end := strings.Index(code[open:at], "{")
	if end < 0 {
		return code[open:at], true
	}
	return code[open : open+end], true
}

// nx444Faults reports every way the post-pairing rebuild can be reachable exactly once.
//
// @param code the source, comments already stripped.
func nx444Faults(where, code string) []string {
	rebuild := nx444Rebuild.FindStringIndex(code)
	if rebuild == nil {
		return []string{where + ": nothing calls rebuildAfterPairing(), so this fence has no " +
			"subject -- the post-pairing rebuild has moved or gone. Re-point the gate rather than " +
			"deleting it"}
	}
	guard, ok := nx444GuardOf(code, rebuild[0])
	if !ok {
		return nil // the rebuild is unconditional: there is no latch to clear.
	}

	var latches []string
	for _, m := range nx444Negated.FindAllStringSubmatch(guard, -1) {
		if nx444LatchDeclaration(m[1]).MatchString(code) {
			latches = append(latches, m[1])
		}
	}
	if len(latches) == 0 {
		return nil // guarded, but by nothing this process latches: every PAIRED draw can run it.
	}

	begin := nx444BeginsAnAttempt.FindStringIndex(code)
	if begin == nil {
		return []string{where + ": no function in this file begins a pairing, so this fence cannot " +
			"tell the lane a new attempt arrives on. Re-point the gate rather than deleting it"}
	}
	lane, ok := kotlinEnclosingFunction(code, begin[0])
	if !ok {
		return []string{where + ": the call that begins a pairing is not inside a function this " +
			"fence can name"}
	}

	var faults []string
	for _, latch := range latches {
		cleared := false
		for _, m := range nx444Clear(latch).FindAllStringIndex(code, -1) {
			// The declaration is `var latch = false` and is not a clear: it runs once, when the
			// screen is constructed, which is the process lifetime the defect is measured against.
			if decl := nx444LatchDeclaration(latch).FindStringIndex(code[:m[1]]); decl != nil && decl[1] == m[1] {
				continue
			}
			if fn, ok := kotlinEnclosingFunction(code, m[0]); ok && fn == lane {
				cleared = true
				break
			}
		}
		if !cleared {
			faults = append(faults, where+": `"+latch+"` guards the post-pairing rebuild and is "+
				"cleared nowhere in `"+lane+"`, the function a new pairing attempt begins in. It is "+
				"therefore a ONE-SHOT for the life of the Activity: the second pairing -- which is "+
				"the documented recovery from a revoke -- calls neither rebuildAfterPairing() nor "+
				"onPaired(), so PhoneSurface never re-renders and never spends the revoke's "+
				"divergence. The phone is paired and still showing REVOKE_UNCONFIRMED")
		}
	}
	return faults
}

// TestNx444_ThePostPairingRebuildIsReachableByASecondPairing is the fence.
func TestNx444_ThePostPairingRebuildIsReachableByASecondPairing(t *testing.T) {
	code := kotlinCodeOnly(readFileOrFail(t,
		filepath.Join(kotlinMainRoot(t), filepath.FromSlash(nx444PairingSurface)), "agents-tracker-nx44.4"))
	if faults := nx444Faults(nx444PairingSurface, code); len(faults) > 0 {
		t.Errorf("agents-tracker-nx44.4: a pairing that completes on a screen this process has "+
			"already paired once from does nothing:\n  %s\n\nPhoneSurface assigns "+
			"`pairing.onPaired = ::render` precisely so the window swaps at the moment a pairing "+
			"succeeds, and that redraw is the only thing that clears `settings.unpairNotice` and "+
			"the latched revoke operation. Behind a one-shot latch the recovery works exactly "+
			"once per process.", strings.Join(faults, "\n  "))
	}
}

// TestNx444_TheLatchScanDiscriminates is the control, in every direction the fix can go and every
// direction a half-fix can. A scan that matched nothing would report the file clean for ever,
// which is the failure this repository has shipped before.
func TestNx444_TheLatchScanDiscriminates(t *testing.T) {
	const shipped = `class PairingSurface {
    private var rebuilt = false

    fun acceptScannedPayload(payload: String) {
        val started = if (carriesRelay(payload)) app.beginPairing(payload)
        else app.beginPairingWithCode(payload, relayForTypedCode())
        handle = started
        render()
    }

    private fun renderReady(startup: PhoneStartup.Ready) {
        if (current == PairingStep.PAIRED && !rebuilt) {
            rebuilt = true
            runtime.rebuildAfterPairing()
            onPaired()
        }
    }
}`
	if faults := nx444Faults("shipped.kt", shipped); len(faults) != 1 {
		t.Fatalf("the scan finds %d faults in the latch as it shipped, so every clean run of the "+
			"assertion above is about nothing:\n%s", len(faults), strings.Join(faults, "\n"))
	}

	// FIX ONE: the latch is cleared where a new attempt begins.
	reset := strings.Replace(shipped, "        handle = started",
		"        rebuilt = false\n        handle = started", 1)
	if faults := nx444Faults("reset.kt", reset); len(faults) > 0 {
		t.Errorf("the scan rejects a latch cleared on the lane that begins a pairing, which is a "+
			"fence nobody can satisfy:\n%s", strings.Join(faults, "\n"))
	}

	// FIX TWO: no latch at all. Every draw that reads PAIRED may run the rebuild, which is the
	// idempotent path -- rebuildAfterPairing drops a phone the next phone() rebuilds anyway.
	unlatched := strings.Replace(
		strings.Replace(shipped, " && !rebuilt", "", 1),
		"            rebuilt = true\n", "", 1)
	if faults := nx444Faults("unlatched.kt", unlatched); len(faults) > 0 {
		t.Errorf("the scan rejects an unguarded rebuild, which is the other fix the defect "+
			"allows:\n%s", strings.Join(faults, "\n"))
	}

	// THE CLEAR HAS TO BE ON THE LANE. A reset written in the render path is not a reset a new
	// attempt performs -- renderReady runs on every poll, so a clear there would re-run the
	// rebuild on every draw of a paired screen, which is the opposite defect.
	elsewhere := strings.Replace(shipped,
		"    private fun renderReady(startup: PhoneStartup.Ready) {",
		"    private fun renderReady(startup: PhoneStartup.Ready) {\n        rebuilt = false", 1)
	if faults := nx444Faults("elsewhere.kt", elsewhere); len(faults) != 1 {
		t.Errorf("the scan reports %d faults on a latch cleared outside the lane a pairing begins "+
			"on:\n%s", len(faults), strings.Join(faults, "\n"))
	}

	// A DECLARATION IS NOT A CLEAR. `private var rebuilt = false` runs once, when the screen is
	// constructed -- which is the process lifetime the whole defect is measured against.
	declOnly := strings.Replace(shipped, "    private var rebuilt = false",
		"    private var rebuilt = false // reset per process", 1)
	if faults := nx444Faults("declonly.kt", declOnly); len(faults) != 1 {
		t.Errorf("the scan reads the field's own declaration as a clear, so a latch nothing resets "+
			"passes: %d faults\n%s", len(faults), strings.Join(faults, "\n"))
	}

	// AND THE SUBJECT MUST BE THERE. A fence whose subject silently disappeared reports clean.
	gone := strings.Replace(shipped, "runtime.rebuildAfterPairing()", "runtime.somethingElse()", 1)
	if faults := nx444Faults("gone.kt", gone); len(faults) != 1 {
		t.Errorf("the scan says nothing about a file that no longer performs the post-pairing "+
			"rebuild: %d faults\n%s", len(faults), strings.Join(faults, "\n"))
	}
}
