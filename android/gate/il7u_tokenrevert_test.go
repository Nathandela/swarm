package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-b6iu: "push token deletion is never reversed
// when the machine refuses the preference."
//
// WHAT HAPPENED. `SettingsSurface.onToggled` reconciles the FCM token OPTIMISTICALLY -- it calls
// `reconcileTheToken(next)` as soon as `setPushPreference` has been issued, so both categories
// going off deletes the token and either coming back on re-registers it.
//
// The optimism is right and is not what this fence is about. The Go verb clears durable state
// BEFORE it speaks to the relay, `onConnected` reconciles the relay to durable state on every
// authenticated reconnect, and `pendingOp` lives only in memory -- so a token deletion that waited
// for an acknowledgement a process death would lose is a token that is never deleted at all.
//
// WHAT IS MISSING IS THE REVERSAL. agents-tracker-os37 gave the machine's refusal an arm:
// `settleWithTheMachine`'s `PushSync.REFUSED` clears `pendingOp`, says the machine's words and
// calls `SettingsScreen.refused()`, which puts the switches back where the machine has them.
// Nothing puts the TOKEN back. So:
//
//   - both categories off, refused, a category restored to ON -- the token stays DELETED. Push is
//     the sole path to a backgrounded phone (ADR-007 B16), so push is dead until some later launch
//     re-registers, while the screen shows a category enabled. That is os37's own defect -- a
//     screen reporting a setting nobody has -- one layer down, in the transport rather than the
//     switch.
//   - a category turned ON from both-off, refused, both back OFF -- the token stays REGISTERED,
//     which is the provider-visible identifier for a phone that has asked for nothing that
//     PB-PUSH-9's "deletion on revoke/disable" exists to remove.
//
// WHY THIS IS A GO GATE AND THE ONLY ONE THERE CAN BE, in the words android/gate/
// postconfirmreturn_test.go already uses over this same file: driving the arm needs a
// `SettingsSurface` with a drawn panel and a machine that answers, and `PhoneRuntime.phone()`
// answers Unavailable on every JVM run (the phone core is a gomobile AAR of .so files
// cross-compiled for Android ABIs), so the panel is never drawn in a unit test and no switch on it
// can be tapped. What CAN be checked is that the revert and the reconcile are in the same place.
//
// THE RULE IS DERIVED FROM THE SOURCE RATHER THAN TYPED IN. What counts as "reconciling the token"
// is whatever function in this file reaches `PushTokens`, so a rename of the private helper moves
// the fence with it instead of silently emptying it.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. The scan is one named file.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const il7uSurfaceFile = "dev/swarm/phone/SettingsSurface.kt"

// il7uRevert is the model call that puts the switches back where the machine has them, with the
// screen it is called ON captured -- that receiver is the screen the machine REJECTED, and
// reconciling the token against it is the way to satisfy this fence while changing nothing.
var il7uRevert = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*\.\s*refused\s*\(\s*\)`)

// il7uTokenCall is the token lifecycle as this module reaches it: the deletion PB-PUSH-9 asks for
// on disable, and the registration that makes the switch usable twice.
var il7uTokenCall = regexp.MustCompile(`PushTokens\s*\.\s*(?:disable|requestInitialToken)\s*\(`)

func il7uSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(il7uSurfaceFile))
	return kotlinWithoutStringLiterals(kotlinCodeOnly(readFileOrFail(t, path, "agents-tracker-b6iu")))
}

// il7uEnclosing names every function in the source holding a match of re, once each.
func il7uEnclosing(code string, re *regexp.Regexp) []string {
	var out []string
	seen := map[string]bool{}
	for _, at := range re.FindAllStringIndex(code, -1) {
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

// il7uReconcilers names the functions that reach the token lifecycle. Today that is one private
// helper; the set is read off the source so that it survives being renamed or split.
func il7uReconcilers(code string) []string { return il7uEnclosing(code, il7uTokenCall) }

// il7uRevertingFunctions names the functions that put the switches back.
func il7uRevertingFunctions(code string) []string { return il7uEnclosing(code, il7uRevert) }

// il7uReconcileIn reports the first token reconcile one body performs, as the name called and the
// first argument handed to it.
//
// A DIRECT `PushTokens.` CALL COUNTS. If the arm reconciles the token itself rather than through
// the helper, the requirement is met; what it is handed is then a Context and the argument check
// below has nothing to say about it, which is correct -- there is no rejected screen to pass.
func il7uReconcileIn(body string, reconcilers []string) (called, arg string, ok bool) {
	for _, name := range reconcilers {
		for _, open := range kotlinCallSites(body, name) {
			args := s23CallArguments(body, open)
			if len(args) == 0 {
				return name, "", true
			}
			return name, args[0], true
		}
	}
	if at := il7uTokenCall.FindStringIndex(body); at != nil {
		return strings.TrimSpace(body[at[0]:at[1]-1]), "", true
	}
	return "", "", false
}

// il7uFaults reports every place the switches go back and the token does not.
//
// @param code the source, comments and string literals already stripped.
func il7uFaults(where, code string) []string {
	reconcilers := il7uReconcilers(code)
	var faults []string
	for _, name := range il7uRevertingFunctions(code) {
		body, ok := kotlinFunBody(code, name)
		if !ok {
			continue
		}
		called, arg, ok := il7uReconcileIn(body, reconcilers)
		if !ok {
			faults = append(faults, where+": `"+name+"` puts the switches back with `refused()` "+
				"and reconciles no token, so the deletion the tap made optimistically stands over "+
				"a preference the machine rejected")
			continue
		}
		rejected := ""
		if m := il7uRevert.FindStringSubmatch(body); m != nil {
			rejected = m[1]
		}
		if rejected != "" && arg == rejected {
			faults = append(faults, where+": `"+name+"` reconciles the token with `"+called+"("+
				arg+")`, and `"+arg+"` is the screen the machine REJECTED -- the reconcile agrees "+
				"with the optimistic deletion instead of undoing it")
		}
	}
	sort.Strings(faults)
	return faults
}

// TestIl7u_TheTokenGoesBackWithTheSwitches is the fence.
func TestIl7u_TheTokenGoesBackWithTheSwitches(t *testing.T) {
	code := il7uSource(t)

	if len(il7uReconcilers(code)) == 0 {
		t.Fatalf("agents-tracker-b6iu: nothing in %s reaches `PushTokens`, so PB-PUSH-9's "+
			"\"deletion on ... disable\" has no caller on the screen a user disables it from and "+
			"this fence has no subject. If the reconcile moved to another file, re-point this gate "+
			"at it rather than deleting it.", il7uSurfaceFile)
	}
	if len(il7uRevertingFunctions(code)) == 0 {
		t.Fatalf("agents-tracker-b6iu: nothing in %s calls `refused()`, so the machine's refusal "+
			"no longer puts the switches back -- which is agents-tracker-os37 undone, and a bigger "+
			"defect than the one this fence watches.", il7uSurfaceFile)
	}

	if faults := il7uFaults(il7uSurfaceFile, code); len(faults) > 0 {
		t.Errorf("agents-tracker-b6iu: the machine's refusal restores the switches and leaves the "+
			"push token where the optimistic tap left it:\n  %s\n\nThe token is what the wake "+
			"arrives on, so a category shown ON over a deleted token is a phone that cannot be "+
			"woken at all (ADR-007 B16: push is the only background path), and both categories "+
			"shown OFF over a live token is the provider-visible identifier PB-PUSH-9 asks to be "+
			"deleted. Reconcile against the RESTORED screen, which is the preference that is now "+
			"in effect.", strings.Join(faults, "\n  "))
	}
}

// TestIl7u_TheRevertScanDiscriminates is the control, in both directions.
//
// The direction that fails silently is a scan that matches nothing: it reports the file clean, and
// a green run then says nothing at all. `shipped` is the arm as it stands at the commit this test
// was written on.
func TestIl7u_TheRevertScanDiscriminates(t *testing.T) {
	const surface = `class SettingsSurface {
    private fun onToggled(toggle: PushToggle, value: Boolean) {
        val op = app.setPushPreference(pref)
        pendingOp = op.operationID
        reconcileTheToken(next)
    }

    private fun reconcileTheToken(next: SettingsScreen) {
        if (!next.alerts && !next.mentions) {
            PushTokens.disable(activity)
        } else {
            PushTokens.requestInitialToken(activity)
        }
    }
}`

	const shipped = `class SettingsSurface {
    private fun settleWithTheMachine(bridge: FacadeBridge, held: SettingsScreen): SettingsScreen {
        val id = pendingOp ?: return held
        return when (SettingsScreen.syncAnswer(bridge.launchOutcome(id), id)) {
            PushSync.PENDING -> held
            PushSync.ACCEPTED -> {
                pendingOp = null
                held.acknowledged()
            }

            PushSync.REFUSED -> {
                pendingOp = null
                say(PressFeedback.ofRefusal(SettingsScreen.refusalNotice(answer)))
                held.refused()
            }
        }
    }
}` + surface

	if faults := il7uFaults("shipped.kt", shipped); len(faults) != 1 {
		t.Fatalf("the scan finds %d faults in the arm that plainly has one, so every clean run of "+
			"the assertion is about nothing:\n%s", len(faults), strings.Join(faults, "\n"))
	}

	// The no-op fix: a reconcile that is handed the very screen the machine rejected. It satisfies
	// a bare "does this arm reconcile the token" check and changes nothing at all, which is the way
	// this fence would most plausibly be defeated.
	const agreeing = `class SettingsSurface {
    private fun settleWithTheMachine(bridge: FacadeBridge, held: SettingsScreen): SettingsScreen {
        return when (SettingsScreen.syncAnswer(bridge.launchOutcome(id), id)) {
            PushSync.REFUSED -> {
                pendingOp = null
                reconcileTheToken(held)
                held.refused()
            }
        }
    }
}` + surface

	if faults := il7uFaults("agreeing.kt", agreeing); len(faults) != 1 {
		t.Errorf("the scan passes a reconcile against the REJECTED screen, which restores nothing "+
			"while looking exactly like the fix:\n%s", strings.Join(faults, "\n"))
	}

	// What the fix produces: the restored screen is named, and the token is reconciled against it.
	const fixed = `class SettingsSurface {
    private fun settleWithTheMachine(bridge: FacadeBridge, held: SettingsScreen): SettingsScreen {
        return when (SettingsScreen.syncAnswer(bridge.launchOutcome(id), id)) {
            PushSync.PENDING -> held
            PushSync.ACCEPTED -> {
                pendingOp = null
                held.acknowledged()
            }

            PushSync.REFUSED -> {
                pendingOp = null
                say(PressFeedback.ofRefusal(SettingsScreen.refusalNotice(answer)))
                val restored = held.refused()
                reconcileTheToken(restored)
                restored
            }
        }
    }
}` + surface

	if faults := il7uFaults("fixed.kt", fixed); len(faults) > 0 {
		t.Errorf("the scan rejects an arm that puts the token back with the switches, which is a "+
			"fence nobody can satisfy:\n%s", strings.Join(faults, "\n"))
	}

	// And the comment case: this fix's own notes quote the defective arm. A scan that read comments
	// would fail on the explanation of the thing it checks.
	const commented = `class SettingsSurface {
    // The arm used to end in ` + "`held.refused()`" + ` with no reconcile, so the token stayed
    // deleted over a category the refusal had put back ON.
    private fun settleWithTheMachine(bridge: FacadeBridge, held: SettingsScreen): SettingsScreen {
        val restored = held.refused()
        reconcileTheToken(restored)
        return restored
    }
}` + surface

	if faults := il7uFaults("commented.kt", kotlinCodeOnly(commented)); len(faults) > 0 {
		t.Errorf("a comment describing the defect reads as the defect, so the fence fails on its "+
			"own documentation:\n%s", strings.Join(faults, "\n"))
	}
}
