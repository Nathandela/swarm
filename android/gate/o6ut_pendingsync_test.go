package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-o6ut(b): "a locally-refused push_prefs leaves
// pendingSync raised forever."
//
// WHAT HAPPENS. `SettingsSurface.onToggled` computes the screen the tap WANTS, issues
// `setPushPreference`, and ends in `draw(next, ...)`. When the facade THROWS, the catch routes the
// refusal and says it -- and the flow still ends in that same `draw(next, ...)`. So the screen
// carries a routed error AND `SettingsScreen.pendingNotice` ("Saved on this phone. It takes effect
// once your machine confirms it.") about a command that was never issued.
//
// AND NOTHING CAN EVER CLEAR THE SECOND. `pendingOp` is assigned from the operation the throw never
// produced, so it is still null, and `settleWithTheMachine` returns early on a null `pendingOp` --
// the one path that lowers `pendingSync` cannot run. The notice stands for the life of the install.
//
// THE SWITCH STAYS WHERE THE FINGER LEFT IT TOO. `SwitchCompat` changes its own position on touch
// and this listener runs afterwards, and `draw`'s equality check cannot put it back: `next` is a
// screen the panel has already been rebuilt for. That is exactly the shape agents-tracker-po3x
// fixed on the two paths it covered, and `restore` is the function it wrote for it.
//
// WHY THIS IS A GO GATE AND THE ONLY ONE THERE CAN BE, in the words android/gate/
// postconfirmreturn_test.go already uses over this same file: reaching the throw needs a
// `SettingsSurface` with a drawn panel and a facade that refuses, and `PhoneRuntime.phone()`
// answers Unavailable on every JVM run (the phone core is a gomobile AAR of .so files
// cross-compiled for Android ABIs), so the panel is never drawn in a unit test and no switch on it
// can be tapped.
//
// WHAT IS FENCED IS THE ARM'S SHAPE, and the shape is what the defect lacked: a refusal that
// reaches no machine has to END the tap -- put the control back and stop -- rather than fall
// through into the tail that draws the wanted screen. The other half of o6ut, (a) "an operation the
// relay never answers", is NOT fenced here: it needs a clock and a retry policy this app does not
// have, and inventing either is a decision for an ADR rather than a bug fix.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. The scan is one named file.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const o6utSurfaceFile = "dev/swarm/phone/SettingsSurface.kt"

// o6utPreferenceWrite is the facade verb whose refusal this fence is about.
var o6utPreferenceWrite = regexp.MustCompile(`\bsetPushPreference\s*\(`)

// o6utCatch is a Kotlin catch clause.
var o6utCatch = regexp.MustCompile(`\bcatch\s*\(`)

// o6utSettleFailure is the refusal arm of a write that crossed to a lane (agents-tracker-h39k).
//
// `SetPushPreference` resolves through `sendContext`, whose `awaitConn` polls for up to five
// seconds, so the write cannot run on the looper -- and a write handed to `VerbDispatch.press`
// does not throw where the tap was: the answer comes back as a `Result` and the refusal is
// `onFailure`. A fence that read only catch clauses would report the whole file clean.
var o6utSettleFailure = regexp.MustCompile(`\bonFailure\b\s*=?\s*\{`)

// o6utRoutesARefusal is a catch that turns the exception into words for the user, which is what
// tells the refusal handler apart from a catch that merely swallows something.
var o6utRoutesARefusal = regexp.MustCompile(`routeFacadeError|ofRefusal`)

// o6utPutsTheControlBack is the surface's own undo for a switch the user has already moved.
var o6utPutsTheControlBack = regexp.MustCompile(`\brestore\s*\(`)

// o6utEndsTheTap is a transfer of control out of the arm. Without one the arm falls into the
// function's tail, which is the whole defect: the tail draws the screen the tap WANTED.
var o6utEndsTheTap = regexp.MustCompile(`\breturn\b|\bthrow\b`)

func o6utSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(o6utSurfaceFile))
	return kotlinWithoutStringLiterals(kotlinCodeOnly(readFileOrFail(t, path, "agents-tracker-o6ut")))
}

// o6utBlockAfter returns the brace-balanced block that opens after from.
//
// It is `notifyBlockAfter` by index rather than by name: a catch clause is found by its keyword and
// there may be several in one body, so the scan cannot start each search from the top of the file.
func o6utBlockAfter(src string, from int) (string, bool) {
	open := strings.IndexByte(src[from:], '{')
	if open < 0 {
		return "", false
	}
	start := from + open
	depth := 0
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1], true
			}
		}
	}
	return "", false
}

// o6utArm is one refusal arm and the shape it arrived in, which decides what is asked of it.
type o6utArm struct {
	// dispatched is true for a `onFailure` arm, false for a catch clause.
	dispatched bool
	block      string
}

// o6utRefusalArms returns every block in one function body that routes a refusal, in either
// shape: the catch clause a synchronous write throws into, and the `onFailure` arm a dispatched
// one settles into.
func o6utRefusalArms(body string) []o6utArm {
	var out []o6utArm
	for _, at := range o6utCatch.FindAllStringIndex(body, -1) {
		block, ok := o6utBlockAfter(body, at[1])
		if !ok || !o6utRoutesARefusal.MatchString(block) {
			continue
		}
		out = append(out, o6utArm{block: block})
	}
	for _, at := range o6utSettleFailure.FindAllStringIndex(body, -1) {
		// The match ENDS on the arm's own `{`, so the block starts there rather than at whatever
		// brace comes next -- which for `onFailure = { refused -> ... }` is the same character,
		// and for a lambda with a nested block would otherwise be the inner one.
		block, ok := o6utBlockAfter(body, at[1]-1)
		if !ok || !o6utRoutesARefusal.MatchString(block) {
			continue
		}
		out = append(out, o6utArm{dispatched: true, block: block})
	}
	return out
}

// o6utArgumentOf is the argument at index of the first call to name inside the arm, or "" where
// the arm makes no such call.
//
// IT IS HOW THE DISPATCHED ARM IS JUDGED. The synchronous arm is checked structurally -- does it
// transfer control out before the tail that draws the wanted screen -- and a lambda has no tail
// to fall into, so the equivalent question is which screen it draws. `restore(toggle, current)`
// names the screen the control goes back to and `draw(current, ...)` names the screen the panel
// goes back to, and the requirement is that they are the SAME one. Comparing the two arguments
// rather than looking for a particular identifier keeps the fence independent of what the
// surface calls its local.
func o6utArgumentOf(arm, name string, index int) string {
	for _, open := range kotlinCallSites(arm, name) {
		args := s23CallArguments(arm, open)
		if len(args) > index {
			return args[index]
		}
	}
	return ""
}

// o6utFaults reports every refusal arm that leaves the tap half-done.
//
// THE TWO CHECKS ARE SEPARATE BECAUSE THE TWO FAILURES ARE. An arm that returns without restoring
// leaves the control in the dragged position over a preference nothing recorded (po3x's shape); an
// arm that restores and falls through still draws the wanted screen, whose `pendingSync` is raised
// and whose `pendingOp` is null.
//
// @param code the source, comments and string literals already stripped.
func o6utFaults(where, code string) []string {
	var faults []string
	for _, name := range il7uEnclosing(code, o6utPreferenceWrite) {
		body, ok := kotlinFunBody(code, name)
		if !ok {
			continue
		}
		for _, arm := range o6utRefusalArms(body) {
			if !o6utPutsTheControlBack.MatchString(arm.block) {
				faults = append(faults, where+": `"+name+"` routes the refusal and leaves the "+
					"switch where the finger dragged it, showing a preference nothing recorded")
			}
			if !arm.dispatched {
				if !o6utEndsTheTap.MatchString(arm.block) {
					faults = append(faults, where+": `"+name+"` routes the refusal and falls through "+
						"into its own tail, which draws the screen the tap WANTED -- so the panel "+
						"carries \"Saved on this phone, waiting for your machine\" about a command "+
						"that was never issued, with `pendingOp` null so nothing can ever clear it")
				}
				continue
			}
			// THE DISPATCHED ARM HAS NO TAIL TO FALL INTO, and the optimistic draw has already
			// happened by the time it runs: the wanted screen went on the panel before the verb
			// left, which is what raises `pendingSync` while the machine is being asked. So the
			// arm has to put the PANEL back as well as the switch, and to the same screen.
			restored := o6utArgumentOf(arm.block, "restore", 1)
			drawn := o6utArgumentOf(arm.block, "draw", 0)
			if drawn == "" {
				faults = append(faults, where+": `"+name+"` routes the refusal and redraws "+
					"nothing, so the panel keeps the screen the tap WANTED -- \"Saved on this "+
					"phone, waiting for your machine\" about a command that was never issued, "+
					"with `pendingOp` null so nothing can ever clear it")
			} else if restored != "" && drawn != restored {
				faults = append(faults, where+": `"+name+"` puts the switch back to `"+restored+
					"` and draws `"+drawn+"` -- the screen the tap WANTED, whose pendingSync is "+
					"raised over a command that was never issued, with `pendingOp` null so "+
					"nothing can ever clear it")
			}
		}
	}
	return faults
}

// TestO6ut_ARefusalThatReachedNoMachineEndsTheTap is the fence.
func TestO6ut_ARefusalThatReachedNoMachineEndsTheTap(t *testing.T) {
	code := o6utSource(t)

	writers := il7uEnclosing(code, o6utPreferenceWrite)
	if len(writers) == 0 {
		t.Fatalf("agents-tracker-o6ut: nothing in %s calls `setPushPreference`, so this fence has "+
			"no subject -- the two switches reach no machine at all. If the write moved behind a "+
			"helper this scan cannot see, re-point this gate at it rather than deleting it.",
			o6utSurfaceFile)
	}
	armed := 0
	for _, name := range writers {
		if body, ok := kotlinFunBody(code, name); ok {
			armed += len(o6utRefusalArms(body))
		}
	}
	if armed == 0 {
		t.Fatalf("agents-tracker-o6ut: `%s` writes the preference and no arm turns a refusal into "+
			"words, so a facade that throws is swallowed entirely -- which is agents-tracker-os37 "+
			"undone, and a bigger defect than the one this fence watches.",
			strings.Join(writers, "`, `"))
	}

	if faults := o6utFaults(o6utSurfaceFile, code); len(faults) > 0 {
		t.Errorf("agents-tracker-o6ut: a push preference the facade REFUSED is drawn as one the "+
			"machine is still thinking about:\n  %s\n\nThe command was never issued, so there is "+
			"no operation to wait for and no answer that can arrive. The screen to draw is the one "+
			"the tap started from.", strings.Join(faults, "\n  "))
	}
}

// TestO6ut_TheRefusalArmScanDiscriminates is the control, in both directions.
//
// `shipped` is `onToggled` as it stands at the commit this test was written on, INCLUDING the
// `?: return restore(toggle, current)` that agents-tracker-po3x put on the line above: that guard
// is outside the arm and must not be read as the arm answering for itself.
func TestO6ut_TheRefusalArmScanDiscriminates(t *testing.T) {
	const shipped = `class SettingsSurface {
    private fun onToggled(toggle: PushToggle, value: Boolean) {
        val app = readyApp() ?: return restore(toggle, current)
        try {
            val op = app.setPushPreference(pref)
            pendingOp = op.operationID
            reconcileTheToken(next)
            say(PressFeedback.ofSuccess(null))
        } catch (refused: Exception) {
            say(PressFeedback.ofRefusal(FacadeBridge(app).routeFacadeError(refused.message.orEmpty()).message))
        }
        draw(next, machineOf(app))
    }
}`
	if faults := o6utFaults("shipped.kt", shipped); len(faults) != 2 {
		t.Fatalf("the scan finds %d faults in an arm that neither ends the tap nor puts the switch "+
			"back, so every clean run of the assertion is about nothing:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}

	// Half a fix, in each direction. Both are states this arm could plausibly be left in.
	const saysAndReturns = `class SettingsSurface {
    private fun onToggled(toggle: PushToggle, value: Boolean) {
        val op = try {
            app.setPushPreference(pref)
        } catch (refused: Exception) {
            say(PressFeedback.ofRefusal(FacadeBridge(app).routeFacadeError(refused.message.orEmpty()).message))
            return
        }
        draw(next, machineOf(app))
    }
}`
	if faults := o6utFaults("saysandreturns.kt", saysAndReturns); len(faults) != 1 {
		t.Errorf("the scan does not report a switch left in the dragged position over a preference "+
			"nothing recorded, which is agents-tracker-po3x's own shape:\n%s",
			strings.Join(faults, "\n"))
	}

	const saysAndRestores = `class SettingsSurface {
    private fun onToggled(toggle: PushToggle, value: Boolean) {
        try {
            app.setPushPreference(pref)
        } catch (refused: Exception) {
            say(PressFeedback.ofRefusal(FacadeBridge(app).routeFacadeError(refused.message.orEmpty()).message))
            restore(toggle, current)
        }
        draw(next, machineOf(app))
    }
}`
	if faults := o6utFaults("saysandrestores.kt", saysAndRestores); len(faults) != 1 {
		t.Errorf("the scan passes an arm that puts the switch back and then falls through into the "+
			"draw that raises the pending notice anyway:\n%s", strings.Join(faults, "\n"))
	}

	// What the fix produces: the arm says what happened, puts the control back, and ends there.
	const fixed = `class SettingsSurface {
    private fun onToggled(toggle: PushToggle, value: Boolean) {
        val app = readyApp() ?: return restore(toggle, current)
        val op = try {
            app.setPushPreference(pref)
        } catch (refused: Exception) {
            say(PressFeedback.ofRefusal(FacadeBridge(app).routeFacadeError(refused.message.orEmpty()).message))
            return restore(toggle, current)
        }
        pendingOp = op.operationID
        reconcileTheToken(next)
        say(PressFeedback.ofSuccess(null))
        draw(next, machineOf(app))
    }
}`
	if faults := o6utFaults("fixed.kt", fixed); len(faults) > 0 {
		t.Errorf("the scan rejects an arm that ends the tap it refused, which is a fence nobody "+
			"can satisfy:\n%s", strings.Join(faults, "\n"))
	}

	// ---- the arm after the write crossed to a lane (agents-tracker-h39k) ----
	//
	// `SetPushPreference` is a signed command: it resolves through `sendContext`, whose `awaitConn`
	// polls for up to five seconds, so the write cannot run on the looper and the refusal cannot
	// arrive as a catch. `VerbDispatch.press` hands the answer back as a `Result` on the looper,
	// and the refusal arm is `onFailure`.
	//
	// THE REQUIREMENT DID NOT CHANGE, ONLY WHERE IT LANDS. The wanted screen is now drawn BEFORE
	// the verb leaves -- that is what raises `pendingSync` while the machine is being asked -- so
	// an arm that says the refusal and stops leaves exactly the notice this fence was written
	// about, with `pendingOp` null and nothing that can ever clear it. The arm has to put the
	// control back and draw the screen it put it back TO.

	const dispatched = `class SettingsSurface {
    private fun onToggled(toggle: PushToggle, value: Boolean) {
        draw(next, machineOf(app))
        dispatch.press(
            host,
            SendPlane.COMMAND,
            work = { app.setPushPreference(pref) },
            settle = { answer ->
                answer.fold(
                    onSuccess = { issued -> pendingOp = issued.operationID },
                    onFailure = { refused ->
                        say(PressFeedback.ofRefusal(FacadeBridge(app).routeFacadeError(refused.message.orEmpty()).message))
                        restore(toggle, current)
                        draw(current, machineOf(app))
                    },
                )
            },
        )
    }
}`
	if faults := o6utFaults("dispatched.kt", dispatched); len(faults) > 0 {
		t.Errorf("the scan rejects the arm a dispatched write produces -- it says what happened, "+
			"puts the switch back and draws the screen the tap started from -- which is a fence "+
			"nobody can satisfy:\n%s", strings.Join(faults, "\n"))
	}

	// The optimistic draw is what makes this one a defect rather than a style: `next` is the screen
	// whose `pendingSync` is raised, and `pendingOp` is null because the command never issued.
	drawsTheWantedScreen := strings.Replace(dispatched, "draw(current, machineOf(app))",
		"draw(next, machineOf(app))", 1)
	if faults := o6utFaults("drawswanted.kt", drawsTheWantedScreen); len(faults) != 1 {
		t.Errorf("the scan finds %d faults in an arm that puts the switch back and then draws the "+
			"screen the tap WANTED, whose pendingSync is raised over a command that was never "+
			"issued:\n%s", len(faults), strings.Join(faults, "\n"))
	}

	saysAndDraws := strings.Replace(dispatched, "restore(toggle, current)\n                        ", "", 1)
	if faults := o6utFaults("saysanddraws.kt", saysAndDraws); len(faults) != 1 {
		t.Errorf("the scan finds %d faults in an arm that redraws and leaves the switch where the "+
			"finger dragged it:\n%s", len(faults), strings.Join(faults, "\n"))
	}

	saysOnly := strings.Replace(saysAndDraws, "draw(current, machineOf(app))\n                    ", "", 1)
	if faults := o6utFaults("saysonly.kt", saysOnly); len(faults) != 2 {
		t.Errorf("the scan finds %d faults in an arm that says the refusal and does nothing else, "+
			"which leaves the dragged switch AND the eternal pending notice:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}

	// The success arm is not a refusal arm and must not be read as one: it neither restores nor
	// redraws, and demanding either of it would make this fence unsatisfiable.
	if faults := o6utFaults("success.kt", strings.Replace(dispatched,
		"pendingOp = issued.operationID", "pendingOp = issued.operationID; say(PressFeedback.ofSuccess(null))", 1)); len(faults) > 0 {
		t.Errorf("the scan reads the SUCCESS arm of a dispatched press as a refusal arm:\n%s",
			strings.Join(faults, "\n"))
	}

	// A catch that swallows without routing is not this fence's subject and must not be reported by
	// it -- `PushTokens.register` has one, and os37 is the issue that owns "a refusal said nothing".
	const swallowing = `class SettingsSurface {
    private fun onToggled(toggle: PushToggle, value: Boolean) {
        try {
            app.setPushPreference(pref)
        } catch (ignored: Exception) {
            return
        }
        draw(next, machineOf(app))
    }
}`
	if faults := o6utFaults("swallowing.kt", swallowing); len(faults) > 0 {
		t.Errorf("the scan claims a catch that says nothing at all, which is a different issue's "+
			"subject and would make this fence's message wrong:\n%s", strings.Join(faults, "\n"))
	}
}
