package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-0rle: the WRITE side of agents-tracker-4zue's
// latch, which nothing in this repository asked for.
//
// WHAT 4zue LEFT HALF-WIRED, and it is the shape this project keeps shipping: a facade a caller
// never reaches. `PhoneSurface.drawPairOnly` now re-reads the machine's verdict on every draw,
// keyed by an operation id it takes from `PhoneRuntime.revokeOperation()`, and
// android/gate/4zue_revokeverdict_test.go fences that read thoroughly. Nothing writes the latch.
// So `revokeOperation()` answers "" for ever, the re-read resolves nothing, and the screen keeps
// the sentence composed in the settle -- which is the very defect 4zue was filed about, still
// live, behind a gate that is green because it only ever looked at the reader.
//
// WHY THE WRITE CANNOT BE FENCED THERE. The id exists for one moment, in one place: the settle of
// the revoke press, where `App.RevokeThisDevice`'s `Op` is in hand. That is
// `SettingsSurface.onReplace`, a different file with a different owner, so the read fence names
// its own subject honestly and this one names the other half.
//
// WHY IT IS A GO GATE AND THE ONLY ONE THERE CAN BE, in the words 4zue's own header uses:
// `PhoneRuntime.phone()` answers Unavailable on every JVM run -- the phone core is a gomobile AAR
// of .so files cross-compiled for Android ABIs -- so `onReplace` returns at its `readyApp()` guard
// under Robolectric and no unit test can reach a settle at all.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. The scan is one named file.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const rleSurfaceFile = "dev/swarm/phone/SettingsSurface.kt"

// rleLatchWrite is the write this fence exists for.
var rleLatchWrite = regexp.MustCompile(`\blatchRevoke\s*\(`)

// rleComposes is the fallback sentence the settle writes, which is the arm the latch belongs in:
// both are about the same answer, and the screen replaces the second with the first's re-read.
var rleComposes = regexp.MustCompile(`\brevokeNoticeFor\s*\(`)

// rleHandsOffTheWindow is the redraw that ENDS this panel: `PhoneSurface.renderReady` re-asks the
// presentation gate and draws the pair-only screen, which is the reader of the latch.
var rleHandsOffTheWindow = regexp.MustCompile(`\bonReplaced\b`)

func rleSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(rleSurfaceFile))
	return kotlinWithoutStringLiterals(kotlinCodeOnly(readFileOrFail(t, path, "agents-tracker-0rle")))
}

// rleFaults reports every way the revoke's answer can fail to be claimable by the screen it lands
// on.
//
// THE FOUR CHECKS ARE SEPARATE BECAUSE THE FOUR FAILURES ARE, and three of them are what a
// half-finished wiring leaves behind: no latch at all, a latch holding something other than the
// id the machine will answer about, a latch written where the verdict is not (so one arm of the
// settle sets it and the other leaves the last revoke's id standing), and a latch written after
// the window has already been handed over and drawn.
//
// @param code the source, comments and string literals already stripped.
func rleFaults(where, code string) []string {
	at := rleLatchWrite.FindStringIndex(code)
	if at == nil {
		return []string{where + ": nothing latches the revoke's operation id, so " +
			"`PhoneRuntime.revokeOperation()` answers the empty string for ever and the pair-only " +
			"screen's re-read resolves nothing. The screen then keeps the sentence composed in the " +
			"settle -- \"your machine has not confirmed it\" -- which is what agents-tracker-4zue " +
			"was filed about and what its own fix cannot fix from the other side"}
	}

	var faults []string
	arm, ok := il7uEnclosingBlock(code, at[0])
	if !ok {
		return append(faults, where+": the latch sits in no block this fence can read")
	}
	if !rleComposes.MatchString(arm) {
		faults = append(faults, where+": the latch is written in an arm that composes no revoke "+
			"notice, so the id and the sentence it is supposed to replace are decided in different "+
			"places. The settle has an arm for a verb that never reached the wire, and an id "+
			"latched there names an operation no machine will ever answer -- while a REAL revoke's "+
			"id, latched on the arm above, is what the screen needs")
	}

	// The id must come from the answer the verb returned. A literal, or the last operation this
	// surface happens to be holding, is a latch that reads as an answer about something else.
	sites := kotlinCallSites(arm, "latchRevoke")
	issued := ""
	if v := kotlinCallSites(arm, "revokeVerdict"); len(v) > 0 {
		if args := s23CallArguments(arm, v[0]); len(args) > 1 {
			issued = args[1]
		}
	}
	if len(sites) > 0 && issued != "" {
		args := s23CallArguments(arm, sites[0])
		if len(args) == 0 || !strings.Contains(args[0], issued) {
			faults = append(faults, where+": the latch is handed something other than the value the "+
				"verdict is resolved from (`"+issued+"`), so the screen re-reads an outcome for an "+
				"operation this revoke did not issue -- which PB-SYNC-2 answers PENDING for, for "+
				"ever, exactly as an unlatched id does")
		}
	}

	// And it must be set before the window that reads it is drawn.
	if hand := rleHandsOffTheWindow.FindAllStringIndex(code, -1); len(hand) > 0 {
		last := hand[len(hand)-1][0]
		if at[0] > last {
			faults = append(faults, where+": the latch is written after the window is handed to "+
				"`onReplaced`, which is the redraw that draws the pair-only screen. That draw reads "+
				"the latch, so a write after it is a write the first draw cannot see")
		}
	}
	return faults
}

// TestRle_TheRevokesOperationIdIsLatchedForTheScreenItLandsOn is the fence.
func TestRle_TheRevokesOperationIdIsLatchedForTheScreenItLandsOn(t *testing.T) {
	code := rleSource(t)

	if !rleComposes.MatchString(code) {
		t.Fatalf("agents-tracker-0rle: nothing in %s composes a revoke notice, so this fence has "+
			"no subject -- the settle this is about has moved or gone. Re-point the gate rather "+
			"than deleting it.", rleSurfaceFile)
	}
	if faults := rleFaults(rleSurfaceFile, code); len(faults) > 0 {
		t.Errorf("agents-tracker-0rle: the revoke's answer is unclaimable by the screen the revoke "+
			"sends the phone to:\n  %s\n\nThe settle runs when `signedCommand` sealed and appended, "+
			"a relay round trip before the machine can have answered, so the sentence composed "+
			"there is the UNCONFIRMED fallback by construction (agents-tracker-4zue). "+
			"`PhoneSurface.drawPairOnly` re-reads the outcome on every draw and needs one thing "+
			"from this file: the id to read it by.", strings.Join(faults, "\n  "))
	}
}

// TestRle_TheLatchScanDiscriminates is the control, in every direction a half-wiring can go.
//
// The direction that fails silently is a scan that matches nothing: it reports the file clean, and
// a green run then says nothing at all. `shipped` is the settle as it stands at the commit this
// test was written on.
func TestRle_TheLatchScanDiscriminates(t *testing.T) {
	const shipped = `class SettingsSurface {
    private fun onReplace(control: View) {
        dispatch.press(
            control,
            SendPlane.COMMAND,
            work = { app.revokeThisDevice() },
            settle = { answer ->
                unpairNotice = answer.fold(
                    onSuccess = { issued -> PairOnlyScreen.revokeNoticeFor(revokeVerdict(app, issued)) },
                    onFailure = { refused -> PairOnlyScreen.revokeUnsentNotice(routed(refused)) },
                )
                onReplaced?.invoke() ?: render()
            },
        )
    }
}`
	if faults := rleFaults("shipped.kt", shipped); len(faults) != 1 {
		t.Fatalf("the scan finds %d faults in a settle that latches nothing at all, so every clean "+
			"run of the assertion above is about nothing:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}

	// What the fix produces.
	fixed := strings.Replace(shipped,
		"onSuccess = { issued -> PairOnlyScreen.revokeNoticeFor(revokeVerdict(app, issued)) },",
		`onSuccess = { issued ->
                        runtime.latchRevoke((issued as? Op)?.operationID.orEmpty())
                        PairOnlyScreen.revokeNoticeFor(revokeVerdict(app, issued))
                    },`, 1)
	if faults := rleFaults("fixed.kt", fixed); len(faults) > 0 {
		t.Errorf("the scan rejects a settle that latches the id it just issued, which is a fence "+
			"nobody can satisfy:\n%s", strings.Join(faults, "\n"))
	}

	// A LATCH HOLDING SOMETHING ELSE. An empty id is what `revokeOperation()` already answers, so
	// this changes nothing while looking exactly like the wiring.
	literal := strings.Replace(fixed, `runtime.latchRevoke((issued as? Op)?.operationID.orEmpty())`,
		`runtime.latchRevoke("")`, 1)
	if faults := rleFaults("literal.kt", kotlinWithoutStringLiterals(literal)); len(faults) == 0 {
		t.Error("the scan passes a latch handed a literal rather than the operation the verb " +
			"returned, which leaves the screen reading exactly what it read unlatched")
	}

	// THE WRONG ARM. The failure arm is the verb that never reached the wire: there is no operation
	// id, so latching there names an operation no machine will answer -- and it does it while the
	// success arm, which has one, latches nothing.
	wrongArm := strings.Replace(shipped,
		"onFailure = { refused -> PairOnlyScreen.revokeUnsentNotice(routed(refused)) },",
		`onFailure = { refused ->
                        runtime.latchRevoke(lastIssued)
                        PairOnlyScreen.revokeUnsentNotice(routed(refused))
                    },`, 1)
	if faults := rleFaults("wrongarm.kt", wrongArm); len(faults) == 0 {
		t.Error("the scan passes a latch written in the arm for a verb that never reached the " +
			"wire, so the id the screen re-reads belongs to no revoke this press issued")
	}

	// AFTER THE WINDOW IS GONE. `onReplaced` re-asks the presentation gate and draws the pair-only
	// screen in the same frame, so a latch written after it is one the first draw cannot see.
	late := strings.Replace(fixed, "onReplaced?.invoke() ?: render()",
		`onReplaced?.invoke() ?: render()
                runtime.latchRevoke(issuedId)`, 1)
	late = strings.Replace(late, "runtime.latchRevoke((issued as? Op)?.operationID.orEmpty())\n                        ", "", 1)
	if faults := rleFaults("late.kt", late); len(faults) == 0 {
		t.Error("the scan passes a latch written after the redraw that reads it, so the draw the " +
			"revoke lands on is the one draw that cannot claim the answer")
	}
}
