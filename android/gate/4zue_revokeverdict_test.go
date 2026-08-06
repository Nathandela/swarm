package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-4zue: "the machine's answer to the revoke is
// asked for at the one moment it cannot have arrived."
//
// WHAT HAPPENS. `SettingsSurface.revokeVerdict` resolves the verdict inside the press SETTLE, and
// a settle runs the moment the work returns -- `signedCommand` seals, appends and returns, with
// the reply a relay round trip away. `CommandVerdict.of` refuses to claim an outcome whose code
// is blank, which is what the durable map holds for an operation nobody has answered yet, so in
// practice that reading is UNANSWERED: `PairOnlyScreen.revokeNoticeFor`'s other two arms -- the
// silence for a confirmed removal and the machine's own words for a refused one -- are dead in
// production, and every revoke this app has issued reads REVOKE_UNCONFIRMED.
//
// "IN PRACTICE" AND NOT "ALWAYS", because the difference is a RACE rather than an invariant and
// this sentence is the kind that gets fenced later. The reply lands through a different goroutine
// entirely -- `mobile/relay.go`'s drain calls accept -> onReply -> resolve, ordered against
// neither the dispatch lane nor the looper -- so a machine that answered before the settle reached
// the main thread would resolve ACCEPTED there. Nothing forbids it; what makes it unreachable in
// the field is that a revoke which SUCCEEDS severs the path its own reply comes back on. The fence
// below therefore pins the re-read, which is right whichever way that race falls, and not the
// timing, which is nobody's promise to keep.
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

// zueCarriesThePurgeFact is the OTHER thing a revoke leaves behind (agents-tracker-jx23): the
// routed reason the key material at rest survived the purge. It is read here for the same reason
// the operation id is -- the panel that learned it is destroyed by the command that produced it.
var zueCarriesThePurgeFact = regexp.MustCompile(`\bpurgeFailure\b`)

// zueFallsBackForASilentPanel is the join that keeps a dropped settle from drawing nothing
// (agents-tracker-xeex): the panel's sentence where there is one, and the sentence composed from
// the latches where the panel never got to speak.
//
// It names the SHAPE, `unpairNotice.ifEmpty { ... }`, because that is the only spelling this
// surface uses for "prefer what was said, fall back to what can be derived". A different one has
// to re-aim this clause rather than delete it.
var zueFallsBackForASilentPanel = regexp.MustCompile(`unpairNotice\s*\.\s*ifEmpty`)

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
		// THE RECOMPOSITION MUST NOT DROP THE OTHER FACT (agents-tracker-jx23). This function
		// REPLACES the sentence the panel composed, so a purge failure the panel joined on and
		// this one does not pass through vanishes from the screen at the moment the machine
		// answers -- which is the moment the user is reading it.
		if !zueCarriesThePurgeFact.MatchString(body) {
			faults = append(faults, where+": `"+name+"` recomposes the notice without the purge "+
				"failure. `App.PurgeKeys` reports that the key material AT REST survived, and this "+
				"draw is what overwrites the panel's sentence -- so the fact would be on screen "+
				"until the machine replied and gone from the moment it did")
		}
		// AND THE PANEL MAY NEVER HAVE SPOKEN AT ALL (agents-tracker-xeex). `VerbDispatch.press`
		// ends in `if (attached) settle(answer)` and `release()` detaches on every pause, so a
		// revoke whose round trip outlives the user's attention loses the sentence the settle
		// would have written -- while the purge in the same press's `finally` has already
		// destroyed both key tiers. Returning the panel's empty string then draws the screen a
		// FRESH INSTALL gets on a handset that has just unpaired and purged itself.
		if !zueFallsBackForASilentPanel.MatchString(body) {
			faults = append(faults, where+": `"+name+"` hands back the panel's sentence with no "+
				"fallback. A settle that was dropped wrote none, and this draw is the only thing "+
				"left that can say anything -- both latches survive a dropped settle, so the "+
				"unconfirmed sentence and the purge failure are both still derivable here")
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
	if faults := zueFaults("claimedandunasked.kt", claimedAndUnasked); len(faults) != 4 {
		t.Errorf("the scan does not report an id claimed with nothing asked of the machine, no "+
			"sentence composed from the answer, no purge fact carried and no fallback for a "+
			"panel that never spoke:\n%s", strings.Join(faults, "\n"))
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
	if faults := zueFaults("askedanduncomposed.kt", askedAndUncomposed); len(faults) != 3 {
		t.Errorf("the scan passes a verdict that is resolved and never turned into the screen's "+
			"words (nor carries the purge fact those words would have taken, nor falls back when "+
			"the panel wrote nothing):\n%s", strings.Join(faults, "\n"))
	}

	// The whole re-read, on a draw that does not call it.
	const readButNotDrawn = `class PhoneSurface {
    private fun revokeNotice(app: App): String {
        val issued = runtime.revokeOperation()
        if (issued.isEmpty()) return settings.unpairNotice
        val verdict = CommandVerdict.of(FacadeBridge(app).launchOutcome(issued), issued, CommandVerdict.ACCEPTED_OK)
        val composed = PairOnlyScreen.revokeNoticeFor(verdict, purgeFailure = runtime.purgeFailure())
        return if (verdict.answered) composed else settings.unpairNotice.ifEmpty { composed }
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

	// THE PURGE FACT DROPPED BY THE RECOMPOSITION (agents-tracker-jx23): everything else wired,
	// and the sentence the panel joined on is overwritten the moment the machine answers.
	const purgeDropped = `class PhoneSurface {
    private fun revokeNotice(app: App): String {
        val issued = runtime.revokeOperation()
        if (issued.isEmpty()) return settings.unpairNotice
        val verdict = CommandVerdict.of(FacadeBridge(app).launchOutcome(issued), issued, CommandVerdict.ACCEPTED_OK)
        val composed = PairOnlyScreen.revokeNoticeFor(verdict)
        return if (verdict.answered) composed else settings.unpairNotice.ifEmpty { composed }
    }

    private fun drawPairOnly(reason: PairOnlyReason, app: App) {
        val revoked = revokeNotice(app)
        host.addView(pairOnlyView(notice = revoked, copy = PairOnlyScreen.copyFor(reason)))
    }
}`
	if faults := zueFaults("purgedropped.kt", purgeDropped); len(faults) != 1 {
		t.Errorf("the scan passes a recomposition that silently drops the purge failure, which is "+
			"a warning that disappears exactly when the user is reading it:\n%s",
			strings.Join(faults, "\n"))
	}

	// THE PANEL'S SENTENCE TAKEN AS THE ONLY ONE (agents-tracker-xeex): everything else wired, and
	// a dropped settle draws the fresh-install screen on a phone that has purged itself.
	const panelSilent = `class PhoneSurface {
    private fun revokeNotice(app: App): String {
        val issued = runtime.revokeOperation()
        if (issued.isEmpty()) return settings.unpairNotice
        val verdict = try {
            CommandVerdict.of(FacadeBridge(app).launchOutcome(issued), issued, CommandVerdict.ACCEPTED_OK)
        } catch (unreadable: Exception) {
            return settings.unpairNotice
        }
        if (!verdict.answered) return settings.unpairNotice
        return PairOnlyScreen.revokeNoticeFor(verdict, purgeFailure = runtime.purgeFailure())
    }

    private fun drawPairOnly(reason: PairOnlyReason, app: App) {
        val revoked = revokeNotice(app)
        host.addView(pairOnlyView(notice = revoked, copy = PairOnlyScreen.copyFor(reason)))
    }
}`
	if faults := zueFaults("panelsilent.kt", panelSilent); len(faults) != 1 {
		t.Errorf("the scan finds %d faults where the draw hands back a sentence the dropped settle "+
			"never wrote, on a handset that has already destroyed both key tiers:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}

	// What the fix produces.
	const fixed = `class PhoneSurface {
    private fun revokeNotice(app: App): String {
        val issued = runtime.revokeOperation()
        if (issued.isEmpty()) return settings.unpairNotice
        val verdict = try {
            CommandVerdict.of(FacadeBridge(app).launchOutcome(issued), issued, CommandVerdict.ACCEPTED_OK)
        } catch (unreadable: Exception) {
            CommandVerdict.UNANSWERED
        }
        val composed = PairOnlyScreen.revokeNoticeFor(verdict, purgeFailure = runtime.purgeFailure())
        return if (verdict.answered) composed else settings.unpairNotice.ifEmpty { composed }
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
