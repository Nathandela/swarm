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
// WHY THE WRITE CANNOT BE FENCED THERE. The id belongs to one press, in one file:
// `SettingsSurface.onReplace`, where `App.RevokeThisDevice`'s `Op` is in hand. That is a different
// file with a different owner, so the read fence names its own subject honestly and this one names
// the other half.
//
// AND WHERE IN THAT PRESS IS THE WHOLE OF IT NOW (agents-tracker-xeex). This fence's first version
// pinned the write to the SETTLE's success arm, which is where it was first wired and is exactly
// the wrong place: `VerbDispatch.press` ends in `if (attached) settle(answer)`, and
// `PhoneSurface.release` detaches on every pause -- while `work` runs on the lane whatever is
// attached. So a revoke whose round trip outlives the user's attention loses its settle, and with
// it the latch AND the fallback sentence, while the purge in the same press's `finally` has
// already destroyed both key tiers. The phone comes back unpaired, purged, and drawing the
// pair-only screen with nothing on it at all -- not even "your machine has not confirmed it".
//
// It is likelier on this press than on any other in the app: the revoke severs the connection its
// own reply would come back on, `sendContext` can wait five seconds before the append, and people
// put the phone down after confirming a destructive dialog.
//
// SO THE REQUIREMENT IS THE LANE, NOT THE ARM. The latch is written where the answer cannot be
// dropped, which is inside the `work` the press hands to a lane. That also retires this fence's
// old ordering check -- "the latch precedes the redraw that reads it" -- because `work` completes
// before any settle can run, so the ordering is structural rather than something to assert.
//
// WHY IT IS A GO GATE AND THE ONLY ONE THERE CAN BE, in the words 4zue's own header uses:
// `PhoneRuntime.phone()` answers Unavailable on every JVM run -- the phone core is a gomobile AAR
// of .so files cross-compiled for Android ABIs -- so `onReplace` returns at its `readyApp()` guard
// under Robolectric and no unit test can reach a press at all.
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

// rleComposes is the fallback sentence the settle writes. It is what a DROPPED settle takes with
// it, and the reason the latch may not keep it company.
var rleComposes = regexp.MustCompile(`\brevokeNoticeFor\s*\(`)

// rleRevoke is the verb whose answer the latch names.
var rleRevoke = regexp.MustCompile(`\brevokeThisDevice\s*\(`)

// rleFromTheAnswer is the operation id read off the `Op` the verb returned. A literal, or an id
// this surface happens to be holding from somewhere else, is a latch that names another command.
var rleFromTheAnswer = regexp.MustCompile(`\boperationID\b`)

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

	// THE LANE, NOT THE ARM (agents-tracker-xeex). `work` is the only part of a press that runs
	// whatever is attached; everything in the settle is conditional on a screen still being there
	// to receive it, and the revoke is the press most likely to outlive one.
	lane := s25Span{}
	found := false
	for _, span := range s25Bodies(code, "work = {", '{', '}') {
		if rleRevoke.MatchString(code[span.start:span.end]) {
			lane, found = span, true
			break
		}
	}
	if !found {
		return append(faults, where+": no `work` lambda in this file issues the revoke, so this "+
			"fence cannot tell the lane from the settle. Re-point it rather than deleting it")
	}
	if at[0] < lane.start || at[0] >= lane.end {
		faults = append(faults, where+": the latch is written outside the `work` the revoke is "+
			"dispatched as -- in the settle, which `VerbDispatch.press` runs only `if (attached)` "+
			"and `PhoneSurface.release` detaches on every pause. The purge in the same press's "+
			"`finally` has already destroyed both key tiers by then, so a dropped settle leaves a "+
			"phone that has unpaired and purged itself drawing the pair-only screen with no id to "+
			"claim an answer by and no sentence on it at all")
	}

	// The id must come from the answer the verb returned, and not from a literal or from whatever
	// this surface happens to be holding: an outcome read by the wrong id answers PENDING for ever,
	// exactly as an unlatched one does.
	if sites := kotlinCallSites(code, "latchRevoke"); len(sites) > 0 {
		args := s23CallArguments(code, sites[0])
		if len(args) == 0 || !rleFromTheAnswer.MatchString(args[0]) {
			faults = append(faults, where+": the latch is handed something that is not the "+
				"`operationID` of the `Op` the verb returned, so the pair-only screen re-reads an "+
				"outcome for an operation this revoke did not issue")
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

	// THE FIRST WIRING, which this fence itself pinned until agents-tracker-xeex: the latch is
	// written, correctly derived, and in the one part of the press that a pause throws away.
	inTheSettle := strings.Replace(shipped,
		"onSuccess = { issued -> PairOnlyScreen.revokeNoticeFor(revokeVerdict(app, issued)) },",
		`onSuccess = { issued ->
                        runtime.latchRevoke((issued as? Op)?.operationID.orEmpty())
                        PairOnlyScreen.revokeNoticeFor(revokeVerdict(app, issued))
                    },`, 1)
	if faults := rleFaults("inthesettle.kt", inTheSettle); len(faults) != 1 {
		t.Errorf("the scan reports %d faults on a latch written in the settle, which is dropped "+
			"whenever the dispatch is detached -- every pause -- while the purge in the same "+
			"press has already destroyed both key tiers:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}

	// What the fix produces: the latch rides the lane, where the answer is in hand and nothing is
	// conditional on a screen still being there.
	fixed := strings.Replace(shipped, "work = { app.revokeThisDevice() },",
		"work = { app.revokeThisDevice().also { runtime.latchRevoke(it?.operationID.orEmpty()) } },", 1)
	if faults := rleFaults("fixed.kt", fixed); len(faults) > 0 {
		t.Errorf("the scan rejects a latch written on the lane that issues the revoke, which is a "+
			"fence nobody can satisfy:\n%s", strings.Join(faults, "\n"))
	}

	// A LATCH HOLDING SOMETHING ELSE. An empty id is what `revokeOperation()` already answers, so
	// this changes nothing while looking exactly like the wiring.
	literal := strings.Replace(fixed, `runtime.latchRevoke(it?.operationID.orEmpty())`,
		`runtime.latchRevoke("")`, 1)
	if faults := rleFaults("literal.kt", kotlinWithoutStringLiterals(literal)); len(faults) == 0 {
		t.Error("the scan passes a latch handed a literal rather than the operation the verb " +
			"returned, which leaves the screen reading exactly what it read unlatched")
	}

	// AND THE LANE HAS TO BE THE REVOKE'S. This surface dispatches other work -- the preference
	// write, the token reconcile -- and a latch on one of those names an operation the pair-only
	// screen will never be able to resolve.
	otherLane := strings.Replace(shipped, "work = { app.revokeThisDevice() },",
		`work = { app.setPushPreference(pref).also { runtime.latchRevoke(it?.operationID.orEmpty()) } },`, 1)
	if faults := rleFaults("otherlane.kt", otherLane); len(faults) == 0 {
		t.Error("the scan passes a latch written on a lane that issues no revoke, so the id the " +
			"pair-only screen reads belongs to another command entirely")
	}
}
