package gate

// FAILING-FIRST (TDD RED, GG-5) for owner ruling R1: take-control leaves the product.
// Plan: docs/specifications/chat-surface-plan.md §2. Bead: agents-tracker-tbpm.7.
//
// WHY A FENCE AND NOT ONLY A DELETION. The control was drawn on `!leaseHeld`, which is the
// RESTING STATE OF EVERY SESSION -- so it was not an edge case a reader might reintroduce by
// accident, it was the default screen. And it bought nothing: `composer_send` is lease-free
// at every layer, so on the chat route the button un-greyed a field for a verb that never
// needed it; on the terminal-fallback route it was never drawn and would have unlocked
// nothing (`TerminalInputSink` has no production implementation); and on the status-card
// route it succeeded and changed nothing visible.
//
// PB-INPUT-2 IS AMENDED, NOT IGNORED (docs/specifications/remote-phaseB-requirements.md,
// 2026-08-26). Its rule still binds any future RAW INPUT plane, where the lease really is
// the daemon's gate. This app has no raw input plane -- `App.SendInput`, `Paste` and `Resize`
// have had zero Kotlin callers since Wave R6 -- and the requirement's intent, that a user can
// tell whether what they type will reach their machine, is carried by the composer's own shut
// state instead. That is a stronger answer than a lease flag, because a held lease never
// implied a session could receive a message and the four shut reasons do.

import (
	"path/filepath"
	"strings"
	"testing"
)

// r1ExemptFile is the ONE screen R1 does not reach, and naming it is cheaper than a
// narrower rule.
//
// The terminal fallback has a control lease OF ITS OWN -- ADR-017 T8's per-session,
// per-device, horizon-bounded `terminal_control` generations -- which is a different
// mechanism from the chat path's `take_control` and is governed by a different ruling. Its
// entering-control slice is PARKED, not shipped: `beginControl`, `type` and `keepAlive` are
// all in android/unbound-verbs.tsv with no production caller, and the screen says so at its
// own site ("IT IS READ-ONLY IN THIS ROUND"). So its Release label names a verb this app
// does not yet issue, and deleting it here would be deleting half of somebody else's slice.
const r1ExemptFile = "TerminalFallbackScreen.kt"

// r1ChatPathKotlin is production Kotlin with comments stripped, minus the exempt screen.
func r1ChatPathKotlin(t *testing.T) string {
	t.Helper()
	root := filepath.Join(appModule(t), "src", "main")
	var b strings.Builder
	for _, f := range kotlinFiles(t, root) {
		if filepath.Base(f) == r1ExemptFile {
			continue
		}
		b.WriteString(readFileOrFail(t, f, "R1"))
		b.WriteString("\n")
	}
	return kotlinCodeOnly(b.String())
}

// r1BannedControlPhrases are the words the deleted ceremony spoke. Each one is a promise the
// product can no longer keep, and the reason differs per phrase:
//
//	"Take control"                 -- there is nothing to take; the composer is already live.
//	"Take control to stop this"    -- turn_interrupt takes no lease either, so the precondition
//	                                  was fake and the Stop it gated was real.
//	"Release control"              -- nothing is held.
//	"take control to type"         -- the sentence the owner photographed, drawn on a screen
//	                                  that also said typing was impossible.
var r1BannedControlPhrases = []string{
	"Take control",
	"Release control",
	"take control to type",
}

// TestR1_NoProductionKotlinOffersTakeControl is the fence. It reads production Kotlin with
// comments stripped, so the reasoning ABOUT the deletion may still be written down beside the
// code -- which is the point of keeping the history legible -- while the strings themselves
// cannot come back.
func TestR1_NoProductionKotlinOffersTakeControl(t *testing.T) {
	kotlin := r1ChatPathKotlin(t)
	for _, phrase := range r1BannedControlPhrases {
		if strings.Contains(kotlin, phrase) {
			t.Errorf("production Kotlin still says %q.\n"+
				"R1 removes take-control from the product. composer_send is lease-free at every "+
				"layer, so this control un-greys a field for a verb that never needed it -- and "+
				"the screen it sat on drew it on !leaseHeld, which is the resting state of every "+
				"session. It was the default screen, not an edge case.", phrase)
		}
	}
}

// TestR1_TheLeaseIsNotAThingAScreenReads pins the model side. `showsTakeControl` and
// `showsRelease` were the two sides of one fact -- which control to draw -- and with neither
// control drawn the fact has no subject. A property left behind and read by nothing is how
// this codebase has repeatedly grown a second, quieter copy of a decision.
func TestR1_TheLeaseIsNotAThingAScreenReads(t *testing.T) {
	kotlin := r1ChatPathKotlin(t)
	for _, prop := range []string{"showsTakeControl", "showsRelease"} {
		if strings.Contains(kotlin, prop) {
			t.Errorf("production Kotlin still reads %q. With no control to draw, the property "+
				"is a fact about a screen that no longer exists", prop)
		}
	}
}

// TestR1_TheComposerStillSaysWhetherItCanSend is the other half of the amendment, and it is
// what stops this being a deletion that loses something. PB-INPUT-2's "visibly confirmed" is
// now carried by the composer's own state: live when the link is up and the session has a
// sink, and otherwise naming which of four reasons it is not.
func TestR1_TheComposerStillSaysWhetherItCanSend(t *testing.T) {
	kotlin := r1ChatPathKotlin(t)
	if !strings.Contains(kotlin, "composerShut") && !strings.Contains(kotlin, "composerAvailability") {
		t.Error("no production Kotlin reads the composer's shut state.\n" +
			"PB-INPUT-2's intent -- a user can tell whether what they type will reach their " +
			"machine -- moved here when the lease left the UX. If nothing reads it, the " +
			"amendment traded a visible confirmation for nothing at all.")
	}
	if !strings.Contains(kotlin, "keyboardEnabled") {
		t.Error("no production Kotlin reads SessionLease.keyboardEnabled. Its lease clause is " +
			"gone, but the link clause is not ceremony: input is live-only and never queued, " +
			"so a composer over a dropped link invites words guaranteed to be dropped.")
	}
}
