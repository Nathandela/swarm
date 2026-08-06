package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-jx23: the SettingsSurface half of
// agents-tracker-r3os.
//
// WHAT r3os LANDED AND WHAT IT COULD NOT. `PhoneRuntime.purgeKeys` answers `RoutedError?` now --
// null when the purge finished at rest, the routed reason when the sealed containers survived --
// because `App.PurgeKeys` documents that an error means the key material AT REST survived (a full
// disk, a read-only data directory) while the memory half happened regardless. Both Go layers keep
// that promise deliberately. android/gate/r3os_purgehonesty_test.go fences the signature and the
// body; what it cannot fence is the caller, which ran the purge as a statement inside
// `onReplace`'s `finally` and dropped the answer on the floor. A contract that is expressible and
// not honoured is the shape this project keeps shipping.
//
// AND THE REVOKE IS THE ONE PRESS WHERE IT MATTERS MOST. The purge is in a `finally` precisely
// because the situation the control exists for is one where the phone may not reach its machine at
// all (ADR-007 B133 decision 3), so both key tiers go whether or not the command landed. A handset
// that could NOT destroy them is then an unpaired-looking phone still holding the material its
// owner has just disowned, and the screen it lands on is the only place that can say so.
//
// BOTH ARMS OF THE SETTLE CARRY IT, and that is the half a smaller fence would miss. The two facts
// are independent: a revoke that never reached the wire (offline, a facade refusal) can happen at
// the same time as a purge that could not finish, and `PairOnlyScreen` takes the purge failure on
// BOTH composers for that reason. An arm that drops it is the same silence jx23 is about, one
// branch over.
//
// WHY IT IS A GO GATE AND THE ONLY ONE THERE CAN BE: `PhoneRuntime.phone()` answers Unavailable on
// every JVM run -- the phone core is a gomobile AAR of .so files cross-compiled for Android ABIs --
// so `onReplace` returns at its `readyApp()` guard under Robolectric and no unit test can reach a
// settle at all. `PairOnlyPurgeNoticeTest` drives the sentence; nothing but this can say the
// surface passes the fact into it.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. The scan is one named file.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const jxSurfaceFile = "dev/swarm/phone/SettingsSurface.kt"

// jxComposers are the screen's two notices for a revoke, both of which take the purge fact.
var jxComposers = []string{"revokeNoticeFor", "revokeUnsentNotice"}

// jxPurgeSource is the runtime's answer to the purge, in either spelling the surface can reach it
// by: the value `purgeKeys` returned, or the latch it wrote for the screen that redraws later.
//
// A LITERAL IS NOT A SOURCE, and that is the point of matching the source rather than the
// parameter. `purgeFailure = ""` satisfies any check that merely asks whether the argument is
// present, and says exactly what saying nothing said.
var jxPurgeSource = regexp.MustCompile(`\bpurge(?:Keys|Failure)\s*\(`)

func jxSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(jxSurfaceFile))
	return kotlinWithoutStringLiterals(kotlinCodeOnly(readFileOrFail(t, path, "agents-tracker-jx23")))
}

// jxFaults reports every revoke notice composed without the fact the purge answered.
//
// @param code the source, comments and string literals already stripped.
func jxFaults(where, code string) []string {
	var faults []string
	for _, composer := range jxComposers {
		for _, open := range kotlinCallSites(code, composer) {
			args := s23CallArguments(code, open)
			carried := false
			for _, arg := range args {
				if jxPurgeSource.MatchString(arg) {
					carried = true
				}
			}
			if carried {
				continue
			}
			faults = append(faults, where+": `"+composer+"` is composed without what the purge "+
				"answered, so a handset that could not destroy its key material at rest says "+
				"nothing about it on the one screen that can. `PhoneRuntime.purgeKeys` returns the "+
				"routed reason and latches it (agents-tracker-r3os); a literal in this argument is "+
				"the silence it replaced")
		}
	}
	sort.Strings(faults)
	return faults
}

// TestJx23_TheRevokeSaysWhatThePurgeCouldNotFinish is the fence.
func TestJx23_TheRevokeSaysWhatThePurgeCouldNotFinish(t *testing.T) {
	code := jxSource(t)

	if !jxPurgeSource.MatchString(code) {
		t.Fatalf("agents-tracker-jx23: nothing in %s reaches the purge, so the revoke no longer "+
			"destroys this handset's key tiers -- which is ADR-007 B133 decision 3 undone, and a "+
			"bigger defect than the one this fence watches.", jxSurfaceFile)
	}
	composed := 0
	for _, composer := range jxComposers {
		composed += len(kotlinCallSites(code, composer))
	}
	if composed < len(jxComposers) {
		t.Fatalf("agents-tracker-jx23: %s composes %d revoke notices and there are two arms to a "+
			"settle -- the machine's answer and a verb that never reached the wire. A scan that "+
			"finds fewer has lost its subject; re-point the gate rather than deleting it.",
			jxSurfaceFile, composed)
	}

	if faults := jxFaults(jxSurfaceFile, code); len(faults) > 0 {
		t.Errorf("agents-tracker-jx23: the revoke reports the machine and not the handset:\n  %s\n\n"+
			"`App.PurgeKeys` returns an error to say the key material AT REST survived, and the "+
			"purge runs in a `finally` so it happens whether or not the command landed. A phone "+
			"that could not destroy it looks unpaired and is still holding the material its owner "+
			"just disowned, with the sentence on no screen in the product.",
			strings.Join(faults, "\n  "))
	}
}

// TestJx23_ThePurgeReportScanDiscriminates is the control, in both directions.
func TestJx23_ThePurgeReportScanDiscriminates(t *testing.T) {
	// `shipped` is the settle as it stood at the commit this test was written on: the purge is a
	// statement and both notices are composed from the machine's answer alone.
	const shipped = `class SettingsSurface {
    private fun onReplace(control: View) {
        dispatch.press(
            control,
            SendPlane.COMMAND,
            work = {
                try {
                    app.revokeThisDevice()
                } finally {
                    runtime.purgeKeys()
                }
            },
            settle = { answer ->
                unpairNotice = answer.fold(
                    onSuccess = { issued -> PairOnlyScreen.revokeNoticeFor(revokeVerdict(app, issued)) },
                    onFailure = { refused -> PairOnlyScreen.revokeUnsentNotice(routed(refused)) },
                )
            },
        )
    }
}`
	if faults := jxFaults("shipped.kt", shipped); len(faults) != 2 {
		t.Fatalf("the scan finds %d faults in a settle whose two notices both drop the purge "+
			"answer, so every clean run of the assertion above is about nothing:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}

	// What the fix produces.
	fixed := strings.Replace(shipped,
		"onSuccess = { issued -> PairOnlyScreen.revokeNoticeFor(revokeVerdict(app, issued)) },",
		"onSuccess = { issued -> PairOnlyScreen.revokeNoticeFor(revokeVerdict(app, issued), purgeFailure = runtime.purgeFailure()) },", 1)
	fixed = strings.Replace(fixed,
		"onFailure = { refused -> PairOnlyScreen.revokeUnsentNotice(routed(refused)) },",
		"onFailure = { refused -> PairOnlyScreen.revokeUnsentNotice(routed(refused), purgeFailure = runtime.purgeFailure()) },", 1)
	if faults := jxFaults("fixed.kt", fixed); len(faults) > 0 {
		t.Errorf("the scan rejects a settle that carries the purge answer into both notices, which "+
			"is a fence nobody can satisfy:\n%s", strings.Join(faults, "\n"))
	}

	// ONE ARM ONLY. The two facts are independent -- a revoke that never reached the wire can
	// happen at the same time as a purge that could not finish -- so a fix applied to the arm the
	// issue was written about leaves the other saying nothing.
	oneArm := strings.Replace(shipped,
		"onSuccess = { issued -> PairOnlyScreen.revokeNoticeFor(revokeVerdict(app, issued)) },",
		"onSuccess = { issued -> PairOnlyScreen.revokeNoticeFor(revokeVerdict(app, issued), purgeFailure = runtime.purgeFailure()) },", 1)
	if faults := jxFaults("onearm.kt", oneArm); len(faults) != 1 {
		t.Errorf("the scan reports %d faults on a settle that tells the purge failure to one arm "+
			"of two, so the offline revoke keeps the silence:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}

	// THE NO-OP FIX: the parameter is there and says nothing. It is what a check for the
	// parameter's presence would accept, and it changes nothing at all.
	literal := strings.Replace(fixed, "purgeFailure = runtime.purgeFailure()", `purgeFailure = ""`, 2)
	if faults := jxFaults("literal.kt", kotlinWithoutStringLiterals(literal)); len(faults) != 2 {
		t.Errorf("the scan reports %d faults on notices handed a literal purge failure, which is "+
			"the silence this fence exists to end wearing the fix's shape:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}
}
