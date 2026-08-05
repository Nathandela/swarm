package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-po3x: "post-confirmation silent returns when the
// runtime degrades between draw and tap."
//
// WHAT HAPPENED. Two guards in `SettingsSurface` returned SILENTLY after the user had already
// acted, both spelled the same way:
//
//	val app = (runtime.phone() as? PhoneStartup.Ready)?.app ?: return
//
//   - `onReplace` runs AFTER the destructive `Replace this computer` dialog has been confirmed. A
//     phone core that failed to build between the draw and that confirmation ended the flow with
//     nothing said and nothing done -- on the one screen where a user has just been asked whether
//     they are sure.
//   - `onToggled` runs after the switch has already moved: `SwitchCompat` changes its own position
//     on touch and the listener runs afterwards. So the control was left in the dragged position,
//     the preference was never persisted, and the screen said nothing.
//
// The window is narrow and it is real: `PhoneRuntime` builds the core LAZILY and FAILABLY, and a
// refusal is not cached -- so the answer to `phone()` genuinely changes between one call and the
// next, which is the whole reason `PhoneActivity.onResume` retries it.
//
// WHY THIS IS A GO GATE AND THE ONLY ONE THERE CAN BE. Driving those two lines needs a
// `SettingsSurface` with a drawn panel, and `PhoneRuntime.phone()` answers Unavailable on every JVM
// run (the phone core is a gomobile AAR of .so files cross-compiled for Android ABIs), so the panel
// is never drawn in a unit test and no control on it can be pressed. That argument is recorded in
// full in android/gate/pbapp6_pbinput2_surface_test.go and `SettingsSurfaceReplaceTest`'s header.
// What CAN be checked is the shape of the branch, and the defect had a very particular shape: the
// Unavailable case was not handled, it was elided.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. The scan is one named file.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const po3xSurfaceFile = "dev/swarm/phone/SettingsSurface.kt"

// po3xRuntimeRead is a read of the runtime's startup answer.
var po3xRuntimeRead = regexp.MustCompile(`runtime\.phone\s*\(`)

// po3xElidedBranch is the defect's own spelling: the Ready case cast out of the sealed type and the
// other case dropped by an elvis.
//
// IT MATCHES THE ELISION AND NOT THE HANDLING. `as? PhoneStartup.Ready` followed by `?: return` says
// "if it is not Ready, stop" without ever naming what it IS -- which is the sealed type's other
// case, the one carrying the routed error this app already knows how to show.
var po3xElidedBranch = regexp.MustCompile(`as\?\s*PhoneStartup\.Ready[^\n]*\?:\s*return`)

// po3xFunctionsThatRead names every function in the source that reads `runtime.phone()`.
func po3xFunctionsThatRead(code string) []string {
	var out []string
	seen := map[string]bool{}
	for _, at := range po3xRuntimeRead.FindAllStringIndex(code, -1) {
		name, ok := kotlinEnclosingFunction(code, at[0])
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// po3xSilentFaults reports every function that reads the runtime and drops the Unavailable case.
//
// THE RULE IS: whoever asks must answer for both cases. `PhoneStartup` is sealed with exactly two,
// and the Unavailable one carries `RoutedError` -- the sentence PB-APP-9 assigns to this condition,
// which `PhoneSurface.press` already puts on its outcome line. A function that reads the runtime
// without naming that case has either dropped it or delegated it, and delegation is visible: the
// delegate reads the runtime itself and is checked by the same rule.
//
// @param code the source, comments and string literals already stripped.
func po3xSilentFaults(where, code string) []string {
	var faults []string
	for _, name := range po3xFunctionsThatRead(code) {
		body, ok := kotlinFunBody(code, name)
		if !ok {
			continue
		}
		if po3xElidedBranch.MatchString(body) {
			faults = append(faults, where+": `"+name+"` drops the runtime's Unavailable answer with "+
				"`as? PhoneStartup.Ready ... ?: return` -- a user who has already acted is told "+
				"nothing and the control is left as they left it")
			continue
		}
		if !strings.Contains(body, "PhoneStartup.Unavailable") {
			faults = append(faults, where+": `"+name+"` reads `runtime.phone()` and never names "+
				"`PhoneStartup.Unavailable`, so one of the sealed type's two answers has no arm")
		}
	}
	sort.Strings(faults)
	return faults
}

// TestPo3x_EveryRuntimeReadOnTheSettingsSurfaceAnswersForBothCases is the fence.
func TestPo3x_EveryRuntimeReadOnTheSettingsSurfaceAnswersForBothCases(t *testing.T) {
	code := notifySource(t, filepath.Join(kotlinMainRoot(t), filepath.FromSlash(po3xSurfaceFile)))

	readers := po3xFunctionsThatRead(code)
	if len(readers) == 0 {
		t.Fatalf("agents-tracker-po3x: nothing in %s reads `runtime.phone()`, so this fence has no "+
			"subject. The surface cannot reach the facade without asking the runtime for it; if the "+
			"read moved behind a helper this scan cannot see, re-point this gate at it rather than "+
			"deleting it.", po3xSurfaceFile)
	}

	if faults := po3xSilentFaults(po3xSurfaceFile, code); len(faults) > 0 {
		t.Errorf("agents-tracker-po3x: a control's press ends in silence when the runtime "+
			"degrades:\n  %s\n\nBoth of these run AFTER the user acted -- one after a destructive "+
			"confirmation, one after the switch has already moved under their finger. "+
			"`PhoneStartup.Unavailable` carries the routed error PB-APP-9 wrote for exactly this "+
			"condition, and `PhoneSurface.press` already shows it; there is nothing to invent, only "+
			"an arm to write.", strings.Join(faults, "\n  "))
	}
}

// TestPo3x_TheSilentReturnScanDiscriminates is the control, in both directions.
//
// The direction that fails silently is a scan that matches nothing: it reports the file clean and a
// green run then says nothing at all. The defective sources below are the two lines as they shipped.
func TestPo3x_TheSilentReturnScanDiscriminates(t *testing.T) {
	const shipped = `class SettingsSurface {
    private fun onReplace(control: View) {
        val app = (runtime.phone() as? PhoneStartup.Ready)?.app ?: return
        dispatch.press(control, SendPlane.COMMAND, work = { app.revokeThisDevice() })
    }

    private fun onToggled(toggle: PushToggle, value: Boolean) {
        val app = (runtime.phone() as? PhoneStartup.Ready)?.app ?: return
        app.setPushPreference(value)
    }
}`
	faults := po3xSilentFaults("shipped.kt", shipped)
	if len(faults) != 2 {
		t.Fatalf("the scan finds %d silent returns in a file that plainly has two, so every clean "+
			"run of the assertion is about nothing:\n%s", len(faults), strings.Join(faults, "\n"))
	}

	// A read with no arm at all -- neither the elision nor the handling -- is the other way to be
	// silent, and it must not pass either.
	const bare = `class SettingsSurface {
    private fun onToggled() {
        val startup = runtime.phone()
        if (startup is PhoneStartup.Ready) startup.app.setPushPreference(value)
    }
}`
	if got := po3xSilentFaults("bare.kt", bare); len(got) == 0 {
		t.Error("the scan passes a read whose other case is simply not there, which is the same " +
			"silence written differently")
	}

	// What the fix produces: one function reads the runtime, answers for both cases, and the
	// callers take its answer. A fence that rejected this would be one nobody could satisfy.
	const fixed = `class SettingsSurface {
    private fun readyApp(): App? = when (val startup = runtime.phone()) {
        is PhoneStartup.Ready -> startup.app
        is PhoneStartup.Unavailable -> {
            say(PressFeedback.ofRefusal(startup.error.message))
            null
        }
    }

    private fun onReplace(control: View) {
        val app = readyApp() ?: return
        dispatch.press(control, SendPlane.COMMAND, work = { app.revokeThisDevice() })
    }

    private fun onToggled(toggle: PushToggle, value: Boolean) {
        val app = readyApp() ?: return restore(toggle, current)
        app.setPushPreference(value)
    }
}`
	if got := po3xSilentFaults("fixed.kt", fixed); len(got) > 0 {
		t.Errorf("the scan rejects a surface that answers for both cases in one place:\n%s",
			strings.Join(got, "\n"))
	}

	// And the comment case: this fix's own note quotes the defective line verbatim. A scan that read
	// comments would fail on the explanation of the thing it checks.
	const commented = `// This read ` + "`(runtime.phone() as? PhoneStartup.Ready)?.app ?: return`" + `, so a
// press after the runtime degraded said nothing at all.
private fun onReplace(control: View) {
    val app = readyApp() ?: return
}`
	if po3xElidedBranch.MatchString(kotlinCodeOnly(commented)) {
		t.Error("a comment explaining the defect reads as the defect, so the fence fails on its " +
			"own documentation")
	}
}
