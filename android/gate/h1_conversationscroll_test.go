// The conversation opens where the conversation IS, and coming back lands where the reader left.
//
// WHY A SOURCE SCAN AND NOT A BEHAVIOURAL TEST. The behaviour has one -- `ConversationScrollTest`
// drives a real measure/layout under Robolectric and asserts the resulting `scrollY`. What that
// test cannot reach is the WIRING: `PhoneRuntime.phone()` answers Unavailable on every JVM run, so
// no session row exists, no drill-down can be opened, and `drawScaffold`'s remember-and-restore
// path is out of reach from an `ActivityScenario`. That is the same bound
// `PhoneSurfaceConversationHostTest` and `TestPBAPP3_TheSessionDetailIsReachedFromTheApp` already
// record, and a source scan is this repository's existing answer to it.
//
// WHAT IT IS FENCING, and it is not hypothetical. Before Wave H the conversation opened at its
// OLDEST message and never recovered: nothing scrolled on the full-rebuild path, and
// stick-to-bottom then self-disarmed because a reader who has never been at the bottom does not
// count as having left it. Live output accumulated below the fold for the life of the session.
// **That was the owner's original complaint about this product, and a wave of six agents rebuilt
// the screen around it without touching it** -- because no test in the suite asserted a scroll
// position, so 1,570 green tests said nothing about it.
//
// The mechanism has three parts and each can be removed on its own without breaking a compile,
// which is exactly what makes it worth a fence:
//
//  1. the scaffold ANCHORS -- a one-shot layout listener, because a ScrollView cannot scroll
//     before it has laid out and `post {}` is a race dressed as a fix;
//  2. the surface REMEMBERS the outgoing offset, because `contentHost` is a FrameLayout and has
//     no scroll position of its own to keep;
//  3. the surface HANDS IT BACK on the conversation branch.
//
// Delete (3) and the app still builds, still passes every Robolectric test that constructs a
// scaffold directly, and quietly opens every returning reader at the top again.
package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func h1Source(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "android", "app", "src", "main", "kotlin", "dev", "swarm", "phone", rel)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("H.1: cannot read %s: %v", rel, err)
	}
	return string(b)
}

// TestH1_TheConversationScaffoldAnchorsItsScroll fences part (1).
func TestH1_TheConversationScaffoldAnchorsItsScroll(t *testing.T) {
	src := kotlinCodeOnly(h1Source(t, "ui/screens/PhoneScaffoldView.kt"))

	if !strings.Contains(src, "anchorConversation") {
		t.Error("H.1: `conversationScaffoldView` no longer anchors its scroll. The transcript is " +
			"oldest-at-top, so a scaffold that opens at scrollY=0 opens the reader on the session's " +
			"FIRST messages -- and stick-to-bottom will not rescue them, because it refuses to " +
			"follow an agent for a reader who is not already at the bottom")
	}
	if !strings.Contains(src, "addOnLayoutChangeListener") {
		t.Error("H.1: the anchor no longer waits for a layout pass. A ScrollView clamps scrollTo " +
			"against its own height and its child's, and both are zero until a measure/layout pass " +
			"has run, so a scroll issued at construction is discarded silently. `post {}` is not a " +
			"substitute: it lands after whichever traversal the queue happens to run first, which " +
			"is right on a warm hierarchy and wrong on a cold start")
	}
	// THE TRAP THIS CLAUSE EXISTS FOR, and it is subtle enough that the lane that wrote the fix
	// wrote the wrong version first. Arming on "the first layout with any height" looks correct
	// and is not: this scroll sets `isFillViewport` (load-bearing for the tab bar), which
	// re-measures an EMPTY content host to EXACTLY the viewport. It never reports zero, so the
	// anchor is spent on the empty pass and a transcript arriving one frame later sits at its
	// oldest message -- the original defect, moved one frame to the right, under a green suite.
	if !strings.Contains(src, "isFillViewport") && !strings.Contains(src, "scroll.height") {
		t.Error("H.1: the anchor no longer compares the content against the viewport before " +
			"spending itself. `isFillViewport` measures an empty content host to exactly the " +
			"viewport height, so an anchor armed on `height > 0` fires on the empty pass and the " +
			"real transcript lands unscrolled one frame later")
	}
}

// TestH2_TheSurfaceRemembersAndRestoresTheConversationScroll fences parts (2) and (3).
func TestH2_TheSurfaceRemembersAndRestoresTheConversationScroll(t *testing.T) {
	src := kotlinCodeOnly(h1Source(t, "PhoneSurface.kt"))

	if !strings.Contains(src, "conversationScrollY") {
		t.Fatal("H.2: the surface no longer remembers the conversation's scroll offset. " +
			"`contentHost` is a FrameLayout and has no scroll position to keep, so the offset has " +
			"to be carried as a number across a rebuild -- and `drawScaffold` rebuilds on every " +
			"ScaffoldKey change, which includes opening an R8 output screen or an R9 diff")
	}
	if !strings.Contains(src, "scrollY = conversationScrollY") {
		t.Error("H.2: the remembered offset is never handed back to the scaffold. This is the " +
			"clause that can be deleted without breaking a compile or any view-level test: the " +
			"offset is still captured, still stored, and every returning reader still lands at " +
			"the top of the transcript")
	}
	if !strings.Contains(src, "as? ScrollView") {
		t.Error("H.2: the offset is no longer read off the outgoing scroll. It cannot be read " +
			"from `contentHost`, which is the FrameLayout a comment here once claimed kept it")
	}
}

// TestH2_TheSurfaceDoesNotClaimContentHostKeepsTheScroll is the fence over the CLAIM rather than
// the code, and it is here because the claim was false and load-bearing.
//
// `PhoneSurface` asserted that `contentHost` kept its child throughout "so coming back lands the
// reader where they left rather than at the top of the transcript". `contentHost` is a
// `FrameLayout`; the scroll offset lived on the `ScrollView` that had just been discarded. A
// comment that justifies a design with something the platform does not do is the failure class
// that cost this repository a P0 in the pairing-entry wave, where four comments asserted downstream
// effects that no code produced.
func TestH2_TheSurfaceDoesNotClaimContentHostKeepsTheScroll(t *testing.T) {
	src := h1Source(t, "PhoneSurface.kt")
	for _, claim := range []string{
		"with their scroll intact",
		"keeps its child throughout, so coming back lands the reader where they left",
	} {
		if !strings.Contains(src, claim) {
			continue
		}
		// The sentence may survive ONLY as a quoted correction of itself.
		idx := strings.Index(src, claim)
		// CASE-INSENSITIVE, and the reason is that this check failed on its first run against a
		// correction that was properly written: the marker read "THIS COMMENT USED TO CLAIM ...
		// AND IT DID NOT", and a lowercase-only match for "claimed" did not see it. A fence whose
		// wording assumption is narrower than the house style reports the code wrong when the
		// code is right, which spends a reader's trust in the fence rather than in the file.
		window := strings.ToLower(src[max0(idx-800):idx])
		corrected := false
		for _, marker := range []string{"used to claim", "correct", "was false", "did not", "claimed"} {
			if strings.Contains(window, marker) {
				corrected = true
				break
			}
		}
		if !corrected {
			t.Errorf("H.2: PhoneSurface.kt still asserts %q as a live claim. A FrameLayout has no "+
				"scroll position; if the sentence is kept it must be kept as a quoted correction "+
				"of itself, so the next reader meets the ruling instead of the mistake", claim)
		}
	}
}

func max0(i int) int {
	if i < 0 {
		return 0
	}
	return i
}

// TestH1_TheFenceCannotBeSatisfiedByAComment is the negative control for the three fences above,
// and it guards the one way they could pass while the mechanism was gone.
//
// EVERY ASSERTION HERE IS A SUBSTRING MATCH OVER SOURCE TEXT, so the interesting failure is not
// "somebody deleted the clause" -- that trivially fails -- but "the clause survives only in the
// prose that explains it". These files carry long argued KDoc that quotes the very identifiers
// being checked, precisely because the argument for a mechanism sits directly above it. A fence
// that read the comments would go on passing after the code beneath them was removed.
//
// This is not hypothetical: the copy gate built one wave earlier had exactly this defect, matching
// tabled sentences against whole files including KDoc, and four of its bindings were passing on
// comments alone (agents-tracker-3jop). `kotlinCodeOnly` is the answer both fences use; this
// asserts it actually answers.
//
// The perturbation is done IN MEMORY on a copy of the source text, never on the file: several
// agents compile these paths concurrently, and a source state that exists in no commit costs a
// verification pass and then a reconciliation (recorded 2026-08-03).
func TestH1_TheFenceCannotBeSatisfiedByAComment(t *testing.T) {
	const clause = "scrollY = conversationScrollY"

	commentOnly := "// the surface hands it back with `" + clause + "` on the conversation branch\n" +
		"private val nothing = 1\n"
	if strings.Contains(kotlinCodeOnly(commentOnly), clause) {
		t.Fatalf("H.2: the fence reads comments, so %q satisfies it from the KDoc that explains "+
			"it. Every clause these tests assert is quoted in the prose beside it, so the fences "+
			"would pass unchanged after the mechanism was deleted", clause)
	}

	realCode := "val scaffold = conversationScaffoldView(\n    " + clause + ",\n)\n"
	if !strings.Contains(kotlinCodeOnly(realCode), clause) {
		t.Fatalf("H.2: the comment stripper removed %q from real code, so the fences cannot pass "+
			"even when the mechanism is present -- a gate that always fails is uninstalled within "+
			"a week", clause)
	}
}
