package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-d0b8 and agents-tracker-2lz5: the two halves
// of "Replace this computer" that never reached the screen.
//
// d0b8 IS THE FACT. The presentation gate asks whether this handset is shown the app at all, and
// it asked a machine NAME -- a coordinate nothing clears, because `phonecore` filters the durable
// blob on it and every mutating verb signs over it. So a revoked phone answered FULL_APP forever.
// The Go side now carries the fact the gate actually needs ("is this phone usably paired"), and
// what is fenced here is that the Kotlin readers ASK it.
//
// 2lz5 IS THE MOMENT. Even with the right fact, the press did not re-evaluate the gate: the
// unqualified `render()` in the replace settle resolves to `SettingsSurface.render`, which redraws
// the settings panel and nothing else. `PhoneSurface.render` is the whole-window redraw, and it is
// what re-asks the gate. The surface already solved this once for the mirror event --
// `PairingSurface.onPaired` is assigned `::render` so that a pairing SUCCESS swaps the window at
// the moment it happens -- and settings got no equivalent when it took the revoke.
//
// WHY THESE ARE GO GATES AND NOT KOTLIN TESTS. Both are statements about WIRING, and the Kotlin
// suite cannot make either: `PhoneRuntime.phone()` answers `PhoneStartup.Unavailable` under
// Robolectric (the phone core is a native library cross-compiled for Android ABIs), so `onReplace`
// returns before it dispatches and no press can be driven to a settle. What a test CAN see is the
// source: which fact the call site reads, and which redraw the settle reaches. That is the same
// line `pairingentry_test.go` draws for the containment fact underneath the same feature.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. Every read starts at the app module, so it cannot
// descend into `.claude/worktrees/`, which holds other agents' full checkouts.

import (
	"path/filepath"
	"strings"
	"testing"
)

const (
	d0b8PhoneSurface    = "dev/swarm/phone/PhoneSurface.kt"
	d0b8SettingsSurface = "dev/swarm/phone/SettingsSurface.kt"
	d0b8PairingSurface  = "dev/swarm/phone/PairingSurface.kt"
)

// d0b8Code reads one production Kotlin source with its comments removed. The reason is
// `kotlinCodeOnly`'s own: a fence a comment can satisfy is one the next thorough comment turns
// off, and this feature has already shipped a comment asserting the very behaviour that was
// missing (`SettingsSurface`'s "a revoked phone is an unpaired phone, and the gate ... so the next
// draw is the screen an unpaired phone gets").
func d0b8Code(t *testing.T, rel string) string {
	t.Helper()
	return kotlinCodeOnly(readFileOrFail(t,
		filepath.Join(kotlinMainRoot(t), filepath.FromSlash(rel)), "agents-tracker-d0b8"))
}

// d0b8Balanced returns the text of the brace- or paren-delimited group that OPENS at or after
// from, with its delimiters. It is how a call's argument and a function's body are read without
// parsing Kotlin: the interesting question in every case below is what one expression or one body
// mentions, and a line-based match would answer it with whatever happened to be nearby.
//
// It returns false when no group opens, which is a subject that has changed shape rather than a
// clean result -- every caller fails loudly on it.
func d0b8Balanced(code string, from int, open, close byte) (string, bool) {
	start := strings.IndexByte(code[from:], open)
	if start < 0 {
		return "", false
	}
	start += from
	depth := 0
	for i := start; i < len(code); i++ {
		switch code[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return code[start : i+1], true
			}
		}
	}
	return "", false
}

// d0b8FunctionBody returns the body of `fun name(...)`, braces included.
func d0b8FunctionBody(t *testing.T, code, name, file string) string {
	t.Helper()
	decl := "fun " + name + "("
	at := strings.Index(code, decl)
	if at < 0 {
		t.Fatalf("agents-tracker-d0b8: %s no longer declares %s(). This fence's subject is gone, and a "+
			"fence whose subject silently disappeared reports clean forever", file, name)
	}
	// Past the parameter list, so a default argument holding braces is not mistaken for the body.
	params, ok := d0b8Balanced(code, at+len(decl)-1, '(', ')')
	if !ok {
		t.Fatalf("agents-tracker-d0b8: %s's %s() has no closing parameter list", file, name)
	}
	body, ok := d0b8Balanced(code, at+len(decl)-1+len(params), '{', '}')
	if !ok {
		t.Fatalf("agents-tracker-d0b8: %s's %s() has no body this fence can read", file, name)
	}
	return body
}

// ---------------------------------------------------------------------------
// d0b8: the fact the gate turns on.
// ---------------------------------------------------------------------------

// TestD0B8_ThePresentationGateAsksWhetherThePhoneIsPairedAndNotWhatItsMachineIsCalled.
//
// The machine endpoint id is written once, at pairing success, and cleared by nothing: not by the
// revoke, not by the purge that destroys both key tiers, and not by the deregistration on the
// machine. It is the wrong fact for this decision and it was the only one available. The right one
// is now on the summary; this is the call site reading it.
func TestD0B8_ThePresentationGateAsksWhetherThePhoneIsPairedAndNotWhatItsMachineIsCalled(t *testing.T) {
	code := d0b8Code(t, d0b8PhoneSurface)
	at := strings.Index(code, "presentationOf")
	if at < 0 {
		t.Fatal("agents-tracker-d0b8: PhoneSurface.kt no longer calls PairOnlyScreen.presentationOf. " +
			"That call is the decision about whether an unpaired handset is shown the app at all " +
			"(agents-tracker-64rf); this fence cannot check a gate that is not there")
	}
	reader, ok := d0b8Balanced(code, at, '{', '}')
	if !ok {
		t.Fatal("agents-tracker-d0b8: the presentationOf call site passes no lambda this fence can read")
	}
	// ONE FACT AND NOT A COMPOSITION. `paired` is computed in Go over the durable unpair AND the
	// terminal transport states, because there is more than one way for a registration to end and
	// only one of them runs on this handset. A call site that rebuilt the answer here -- reading
	// the machine name, or the connection state, or both -- would be a second gate to keep in
	// agreement with the first, which is how the machine name came to be the fact in the first
	// place.
	if strings.Contains(reader, "machine") || strings.Contains(reader, "connection") {
		t.Errorf("agents-tracker-d0b8: the presentation gate composes its own answer from %s. The fact "+
			"is App.StateSummary.paired and it is assembled where the coordinates live; a screen that "+
			"rebuilds it holds a second opinion that has to be kept in step with the first",
			strings.TrimSpace(reader))
	}
	if !strings.Contains(reader, "paired") {
		t.Errorf("agents-tracker-d0b8: the presentation gate reads %s. Whether this phone is USABLY "+
			"paired is the question it decides, and after a revoke the honest answer is no -- while "+
			"every other fact on the summary still describes the registration that ended. A gate that "+
			"reads the machine's NAME answers FULL_APP for a handset whose keys are destroyed, whose "+
			"machine deregistered it, and whose only way out is the screen this gate is refusing to "+
			"show", strings.TrimSpace(reader))
	}
}

// TestD0B8_TheRePairingFlowAgreesWithTheGate is the half that makes the fix a way OUT rather than
// a nicer dead end.
//
// `PairingSurface` renders step PAIRED -- "you are already paired", no scan -- whenever it reads a
// pin, because a completed pairing clears the attempt record and the pin is what says a pairing
// happened at all. Left reading the machine name, a revoked phone sent to the pairing screen by
// the fixed gate arrives at a panel that tells it it is already paired. The gate would be right
// and the handset would still be unpairable.
func TestD0B8_TheRePairingFlowAgreesWithTheGate(t *testing.T) {
	code := d0b8Code(t, d0b8PairingSurface)
	body := d0b8FunctionBody(t, code, "isPinned", "PairingSurface.kt")
	if !strings.Contains(body, "paired") {
		t.Errorf("agents-tracker-d0b8: PairingSurface.isPinned reads %s. It decides whether the pairing "+
			"panel offers the SCAN flow or reports that this phone is already paired, so reading a "+
			"coordinate the revoke does not clear sends the revoked handset from the pair-only screen "+
			"to a panel that refuses to pair it", strings.TrimSpace(body))
	}
}

// ---------------------------------------------------------------------------
// 2lz5: the moment the gate is re-asked.
// ---------------------------------------------------------------------------

// TestPB2LZ5_TheReplaceSettleReachesTheWholeWindowAndNotOnlyTheSettingsPanel.
//
// THE ASYMMETRY IS THE EVIDENCE. `PhoneSurface` wires `pairing.onPaired = ::render` with a comment
// saying why the whole window must be redrawn at the moment a pairing succeeds -- an unpaired
// phone is shown one screen, so the success has to swap the window then rather than the next time
// the user happens to leave and come back. A revoke is the same event in the other direction and
// got no such callback: the settle's unqualified `render()` binds to `SettingsSurface.render`, so
// the panel redraws inside a window whose gate was never re-asked.
//
// WHAT IT FENCES IS THE CALLBACK AND NOT A CALL INTO `PhoneSurface`. A panel reaching up into the
// surface that hosts it would be the same coupling `SettingsSurface` takes its dispatch as a
// parameter to avoid; the surface owns the wiring, and this checks both ends of it.
func TestPB2LZ5_TheReplaceSettleReachesTheWholeWindowAndNotOnlyTheSettingsPanel(t *testing.T) {
	settings := d0b8Code(t, d0b8SettingsSurface)
	body := d0b8FunctionBody(t, settings, "onReplace", "SettingsSurface.kt")
	if !strings.Contains(body, "onReplaced") {
		t.Errorf("agents-tracker-2lz5: the replace settle is %s. `render()` here is "+
			"`SettingsSurface.render` -- the settings panel and nothing above it -- so the presentation "+
			"gate is not re-evaluated by the press that changes its answer. The screen stays on the "+
			"four-tab scaffold until the next onResume, and journal events stop arriving precisely "+
			"because the revoke severed the link", strings.TrimSpace(body))
	}

	surface := d0b8Code(t, d0b8PhoneSurface)
	if !strings.Contains(surface, "onReplaced") {
		t.Error("agents-tracker-2lz5: PhoneSurface wires nothing to the settings surface's replace " +
			"callback, so the callback exists and fires into nobody. `pairing.onPaired = ::render` is " +
			"the same wiring for the mirror event, and it is one line above where the settings surface " +
			"is built")
	}
	if strings.Contains(surface, "onReplaced") && !strings.Contains(surface, "onReplaced = ::render") {
		t.Error("agents-tracker-2lz5: PhoneSurface wires the replace callback to something other than " +
			"its own `render`. The whole-window redraw is what re-asks the gate; anything narrower " +
			"leaves the revoked phone in the shell it just left")
	}
}
