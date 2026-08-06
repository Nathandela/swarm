package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-4zue: "the machine's answer to the revoke is
// asked for at the one moment it cannot have arrived."
//
// WHAT HAPPENS. `SettingsSurface.revokeVerdict` resolves the verdict inside the press SETTLE, and
// a settle runs the moment the work returns -- `signedCommand` seals, appends and returns, with
// the reply a relay round trip away. `CommandVerdict.of` refuses to claim an outcome whose code
// is blank, which is what the durable map holds for an operation nobody has answered yet, so the
// verdict is UNANSWERED on every run. `PairOnlyScreen.revokeNoticeFor`'s other two arms -- the
// silence for a confirmed removal and the machine's own words for a refused one -- are dead in
// production, and every revoke this app has ever issued reads REVOKE_UNCONFIRMED.
//
// THE SURFACE'S OWN COMMENT ARGUES IT IS FORCED: "there is no later draw of this surface to ask
// again from -- the phone is unpaired the moment the purge above finishes". The premise is right
// and the conclusion does not follow. There is no later draw of THAT PANEL, and there are many
// of the screen the phone is sent to: `PhoneSurface.render` runs on every resume and every
// journal event, and `drawPairOnly` is what it draws. The answer arrives there.
//
// SO THE FENCE IS ON THE SCREEN THAT LASTS. `renderKillVerdict` and `renderLeaseVerdict` are the
// same program on the other two verbs -- claim the operation id, re-read the outcome per draw --
// and the pair-only branch returns before `renderVerdicts` is ever reached. What it needs beyond
// them is somewhere to keep the id: the panel that issued the revoke is destroyed by it, so the
// latch outlives the surface (`PhoneRuntime`, which is also where the relay coordinate the facade
// has no field for is kept).
//
// WHY THIS IS A GO GATE AND NOT A KOTLIN TEST: `PhoneRuntime.phone()` answers Unavailable under
// Robolectric -- the phone core is a gomobile AAR of .so files cross-compiled for Android ABIs --
// so no JVM test can reach a draw that has a facade to ask. `CommandVerdictTest` argues the table
// this reads and cannot argue when it is read.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. The scan is one named file.

import (
	"regexp"
	"strings"
	"testing"
)

// zueLatchRead is the claim on the operation id the revoke was issued under. It is READ here and
// written by the panel, so this fence names the read.
var zueLatchRead = regexp.MustCompile(`\brevokeOperation\b`)

// zueAsksTheMachine is the outcome read every verdict on this surface goes through.
var zueAsksTheMachine = regexp.MustCompile(`\blaunchOutcome\s*\(`)

// zueComposes is the screen's own sentence for a verdict, which is where the answered arms live.
var zueComposes = regexp.MustCompile(`\brevokeNoticeFor\s*\(`)

// zueFaults reports every way the pair-only draw can fail to claim the machine's answer.
//
// THE FOUR CHECKS ARE SEPARATE BECAUSE THE FOUR FAILURES ARE, and three of them are states a
// half-finished fix leaves behind: an id nobody claims, an id claimed with nothing asked of the
// machine, a verdict resolved and then composed by nobody, and a re-read the draw never reaches.
//
// @param code the source, comments and string literals already stripped.
func zueFaults(where, code string) []string {
	readers := il7uEnclosing(code, zueLatchRead)
	if len(readers) == 0 {
		return []string{where + ": nothing claims the revoke's operation id, so the pair-only " +
			"screen draws the sentence composed in the settle -- and a settle runs when the " +
			"command was APPENDED, a relay round trip before the machine can have answered. " +
			"Every revoke reads \"your machine has not confirmed it\", including the ones it " +
			"refused with a kill switch"}
	}
	var faults []string
	for _, name := range readers {
		body, ok := kotlinFunBody(code, name)
		if !ok {
			continue
		}
		if !zueAsksTheMachine.MatchString(body) {
			faults = append(faults, where+": `"+name+"` claims the operation id and asks the "+
				"machine nothing. PB-SYNC-2 keys outcomes by id so that a screen can resolve ITS "+
				"own command; holding the id without reading the outcome is the same silence")
		}
		if !zueComposes.MatchString(body) {
			faults = append(faults, where+": `"+name+"` resolves a verdict that reaches no words. "+
				"`PairOnlyScreen.revokeNoticeFor` owns the three sentences -- silence for a "+
				"confirmed removal, the machine's own reason for a refused one, and the "+
				"unconfirmed notice -- and a verdict composed anywhere else is a second copy of "+
				"copy the screen owns (PB-DS-9)")
		}
	}
	drawn := false
	body, ok := kotlinFunBody(code, "drawPairOnly")
	if !ok {
		return append(faults, where+": `drawPairOnly` has no body this fence can read")
	}
	for _, name := range readers {
		if strings.Contains(body, name+"(") {
			drawn = true
		}
	}
	if !drawn {
		faults = append(faults, where+": `drawPairOnly` never reaches the re-read, so the answer is "+
			"claimed on a draw nobody sees. This is the draw the revoke sends the phone to and the "+
			"one every resume and every journal event repeats")
	}
	return faults
}

// TestZUE_ThePairOnlyDrawClaimsTheMachinesAnswerToTheRevoke is the fence.
func TestZUE_ThePairOnlyDrawClaimsTheMachinesAnswerToTheRevoke(t *testing.T) {
	if faults := zueFaults(d0b8PhoneSurface, kotlinWithoutStringLiterals(d0b8Code(t, d0b8PhoneSurface))); len(faults) > 0 {
		t.Errorf("agents-tracker-4zue: the phone tells its owner that the removal is unconfirmed "+
			"whatever the machine said:\n  %s\n\nA revoke the machine REFUSED leaves this handset "+
			"purged while the registration stands, and `swarm remote pair` is refused until that "+
			"is cleared (PB-STATE-10) -- the one sentence that would explain the pairing about to "+
			"fail.", strings.Join(faults, "\n  "))
	}
}

// TestZUE_TheVerdictScanDiscriminates is the control, in every direction a half-fix can go.
func TestZUE_TheVerdictScanDiscriminates(t *testing.T) {
	// `shipped` is the pair-only draw as it stood at the commit this test was written on: the
	// sentence is a value the panel wrote once, and nothing on this side asks again.
	const shipped = `class PhoneSurface {
    private fun drawPairOnly(reason: PairOnlyReason) {
        val revoked = settings.unpairNotice
        val next = Triple(pairingStarted, revoked, reason)
        if (pairOnlyDrawn == next && host.childCount > 0) return
        host.addView(pairOnlyView(notice = revoked, copy = PairOnlyScreen.copyFor(reason)))
    }
}`
	if faults := zueFaults("shipped.kt", shipped); len(faults) != 1 {
		t.Fatalf("the scan finds %d faults in a draw that claims no operation id at all, so every "+
			"clean run of the assertion above is about nothing:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}

	// A latch that is read and asked nothing.
	const claimedAndUnasked = `class PhoneSurface {
    private fun revokeNotice(app: App): String {
        val issued = runtime.revokeOperation()
        return settings.unpairNotice
    }

    private fun drawPairOnly(reason: PairOnlyReason, app: App) {
        val revoked = revokeNotice(app)
        host.addView(pairOnlyView(notice = revoked, copy = PairOnlyScreen.copyFor(reason)))
    }
}`
	if faults := zueFaults("claimedandunasked.kt", claimedAndUnasked); len(faults) != 2 {
		t.Errorf("the scan does not report an id claimed with nothing asked of the machine and no "+
			"sentence composed from the answer:\n%s", strings.Join(faults, "\n"))
	}

	// The answer is read and the screen draws the settle's sentence over it anyway.
	const askedAndUncomposed = `class PhoneSurface {
    private fun revokeNotice(app: App): String {
        val issued = runtime.revokeOperation()
        if (issued.isEmpty()) return settings.unpairNotice
        CommandVerdict.of(FacadeBridge(app).launchOutcome(issued), issued, CommandVerdict.ACCEPTED_OK)
        return settings.unpairNotice
    }

    private fun drawPairOnly(reason: PairOnlyReason, app: App) {
        val revoked = revokeNotice(app)
        host.addView(pairOnlyView(notice = revoked, copy = PairOnlyScreen.copyFor(reason)))
    }
}`
	if faults := zueFaults("askedanduncomposed.kt", askedAndUncomposed); len(faults) != 1 {
		t.Errorf("the scan passes a verdict that is resolved and never turned into the screen's "+
			"words:\n%s", strings.Join(faults, "\n"))
	}

	// The whole re-read, on a draw that does not call it.
	const readButNotDrawn = `class PhoneSurface {
    private fun revokeNotice(app: App): String {
        val issued = runtime.revokeOperation()
        if (issued.isEmpty()) return settings.unpairNotice
        val verdict = CommandVerdict.of(FacadeBridge(app).launchOutcome(issued), issued, CommandVerdict.ACCEPTED_OK)
        return PairOnlyScreen.revokeNoticeFor(verdict)
    }

    private fun drawPairOnly(reason: PairOnlyReason) {
        val revoked = settings.unpairNotice
        host.addView(pairOnlyView(notice = revoked, copy = PairOnlyScreen.copyFor(reason)))
    }
}`
	if faults := zueFaults("readbutnotdrawn.kt", readButNotDrawn); len(faults) != 1 {
		t.Errorf("the scan passes a re-read the draw never reaches, which is the fix written and "+
			"not wired:\n%s", strings.Join(faults, "\n"))
	}

	// What the fix produces.
	const fixed = `class PhoneSurface {
    private fun revokeNotice(app: App): String {
        val issued = runtime.revokeOperation()
        if (issued.isEmpty()) return settings.unpairNotice
        val verdict = try {
            CommandVerdict.of(FacadeBridge(app).launchOutcome(issued), issued, CommandVerdict.ACCEPTED_OK)
        } catch (unreadable: Exception) {
            return settings.unpairNotice
        }
        if (!verdict.answered) return settings.unpairNotice
        return PairOnlyScreen.revokeNoticeFor(verdict)
    }

    private fun drawPairOnly(reason: PairOnlyReason, app: App) {
        val revoked = revokeNotice(app)
        host.addView(pairOnlyView(notice = revoked, copy = PairOnlyScreen.copyFor(reason)))
    }
}`
	if faults := zueFaults("fixed.kt", fixed); len(faults) > 0 {
		t.Errorf("the scan rejects a draw that re-asks the machine and composes what it said, "+
			"which is a fence nobody can satisfy:\n%s", strings.Join(faults, "\n"))
	}
}
