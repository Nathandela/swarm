package gate

// The wiring half of agents-tracker-w6o3: "a revoked phone must not read like a fresh install".
//
// THE DEFECT WAS A DECISION ORDER. `PhoneSurface.renderReady` asks `PairOnlyScreen.presentationOf`
// FIRST and returns before `FacadeBridge.connectionBanner()` is ever read, and `mobile/relay.go`'s
// `transportEndsPairing` folds `repair_required` and a past-grace `revoked` into `paired = false`.
// So the two most carefully worded banners in ConnectionUi.kt are unreachable in production and
// the handset they describe opens on the screen a fresh install opens on -- with, on
// repair_required, one control that leads into a pairing the machine fail-fasts while it still
// holds this device's registration (PB-STATE-10). That is PB-APP-10's forbidden failure loop,
// reached through the remedy.
//
// WHY THIS IS A GO GATE AND NOT A KOTLIN TEST, which is d0b8_unpair_test.go's argument for the
// same surface: `PhoneRuntime.phone()` answers `PhoneStartup.Unavailable` under Robolectric -- the
// phone core is a gomobile AAR of .so files cross-compiled for Android ABIs -- so no JVM test can
// drive a real handset into a terminal transport state and watch which screen it lands on. What a
// test CAN see is the source: whether the draw consults the transport at all, and whether what it
// learns survives the trip to the words. The presentation itself is argued by
// PairOnlyTerminalReasonTest and PairOnlyViewTest, which are the stronger tests for what they
// cover and cannot cover this.
//
// IT DOES NOT LOOSEN d0b8's FENCE AND MUST NOT. That gate reads the lambda passed to
// `presentationOf` and fails if it mentions the connection state, because whether this phone is
// USABLY paired is one fact assembled in Go over the durable unpair AND the transport, and a call
// site that rebuilt it here would be a second opinion to keep in step. The reason is a DIFFERENT
// question -- not "is this phone paired" but "what does the screen it has been sent to say" -- and
// it is asked after the gate has already answered.
//
// ## What this file is, after being rebuilt (agents-tracker-raa9)
//
// IT USED TO BE THREE UNLINKED SUBSTRING CHECKS: that PhoneSurface.kt contains `reasonFor`
// somewhere, that the lambda at that index mentions `connectionState`, and that `drawPairOnly`'s
// body contains `copyFor`. Every one of them passes on a draw that computes the reason correctly
// and then renders `copyFor(PairOnlyReason.FIRST_RUN)` -- the exact defect, with the fix's symbols
// present. There was no control either, so a clean run said nothing about whether the scan could
// still see a fault at all.
//
// WHAT IT PINS NOW IS THE CHAIN, one link at a time: the transport is read, the reason it produces
// is the value handed to the draw, the draw's own parameter is what `copyFor` is asked about, and
// the copy that comes back is what the view is given. Breaking any single link is a fixture below.
// The second scan is over the words themselves -- that repair_required's cause reaches the BODY
// slot, ahead of the control, rather than being announced on the control the user has already
// pressed by the time they read it.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. The scan is two named files.

import (
	"regexp"
	"strings"
	"testing"
)

const w6o3ScreenFile = "dev/swarm/phone/ui/screens/PairOnlyScreen.kt"

// w6o3TransportRead is the transport's own state, which is the ONLY place the cause survives: the
// summary carries `paired` and nothing about why it turned false.
var w6o3TransportRead = regexp.MustCompile(`\bconnectionState\b`)

// w6o3Unregister is the machine-side step that has to come first, spelled as PairOnlyScreen spells
// it. Both halves are named because a screen that says only "revoke" leaves the user without the
// command that finds the device id the other one takes.
var w6o3Unregister = []string{"swarm remote devices", "swarm remote revoke"}

// ---------------------------------------------------------------------------
// The chain: transport -> reason -> draw -> copy -> view.
// ---------------------------------------------------------------------------

// w6o3ArgumentsOfFirstCall returns the arguments of the first CALL to name (never its declaration).
func w6o3ArgumentsOfFirstCall(code, name string) ([]string, bool) {
	for _, open := range kotlinCallSites(code, name) {
		return s23CallArguments(code, open), true
	}
	return nil, false
}

// w6o3ParameterName is the name of the first parameter of `fun name(...)`, which is the value the
// body must carry through rather than deciding for itself.
func w6o3ParameterName(code, name string) (string, bool) {
	decl := regexp.MustCompile(`fun\s+` + regexp.QuoteMeta(name) + `\s*\(`).FindStringIndex(code)
	if decl == nil {
		return "", false
	}
	args := s23CallArguments(code, decl[1]-1)
	if len(args) == 0 {
		return "", false
	}
	return strings.TrimSpace(strings.SplitN(args[0], ":", 2)[0]), true
}

// w6o3WiringFaults reports every broken link between the transport and the words on screen.
//
// @param code PhoneSurface.kt, comments and string literals already stripped.
func w6o3WiringFaults(where, code string) []string {
	var faults []string

	drawArgs, ok := w6o3ArgumentsOfFirstCall(code, "drawPairOnly")
	if !ok {
		return []string{where + ": nothing draws the pair-only screen, so this fence has no " +
			"subject. If the draw moved behind a helper this scan cannot see, re-point the gate " +
			"at it rather than deleting it"}
	}
	// LINK 1: the draw is TOLD a reason, and the reason is asked of PairOnlyScreen.
	if len(drawArgs) == 0 || !strings.Contains(drawArgs[0], "reasonFor") {
		faults = append(faults, where+": the pair-only draw is handed `"+strings.Join(drawArgs, ", ")+
			"`, which never asks `PairOnlyScreen.reasonFor`. A phone whose owner ran `swarm remote "+
			"revoke` reads \"Pair this phone\" -- identical to a fresh install -- and a phone whose "+
			"relay-auth key was destroyed is offered a bare pairing CTA that the machine refuses "+
			"while it still holds this device's registration (PB-STATE-10)")
	} else if !w6o3TransportRead.MatchString(drawArgs[0]) {
		// LINK 2: the reader is the TRANSPORT's own state and not a second inference from the
		// durable blob. `revoked` and `repair_required` are reported nowhere else.
		faults = append(faults, where+": the pair-only reason is read from `"+
			strings.TrimSpace(drawArgs[0])+"`. The transport state is where `revoked` and "+
			"`repair_required` are reported -- ConnectionState.of over App.ConnectionState -- and a "+
			"reason derived from anything else on this handset is guessing at a cause the state "+
			"summary does not carry")
	}

	param, ok := w6o3ParameterName(code, "drawPairOnly")
	if !ok {
		return append(faults, where+": `drawPairOnly` declares no parameter this fence can read")
	}
	body := d0b8FunctionBodyOf(code, "drawPairOnly")
	if body == "" {
		return append(faults, where+": `drawPairOnly` has no body this fence can read")
	}
	// LINK 3: the reason the draw was GIVEN is the one the words are asked about. This is the link
	// the first version of this gate did not make: `copyFor(PairOnlyReason.FIRST_RUN)` satisfied a
	// scan for `copyFor` while rendering the first-run screen over every terminal state.
	copyArgs, ok := w6o3ArgumentsOfFirstCall(body, "copyFor")
	switch {
	case !ok:
		faults = append(faults, where+": `drawPairOnly` composes the screen without asking "+
			"`PairOnlyScreen.copyFor`, so whatever the reason turned out to be, the draw puts the "+
			"first-run constants over it")
	case len(copyArgs) == 0 || strings.TrimSpace(copyArgs[0]) != param:
		faults = append(faults, where+": `drawPairOnly` asks for the copy of `"+
			strings.Join(copyArgs, ", ")+"` while it was handed `"+param+"`. A draw that decides "+
			"its own reason is a second opinion about a question the transport already answered, "+
			"and the answer it discards is the only one that knows why the pairing ended")
	}
	// LINK 4: the copy reaches the composition. A copy resolved and dropped is the same screen it
	// was before any of this.
	viewArgs, ok := w6o3ArgumentsOfFirstCall(body, "pairOnlyView")
	if !ok {
		return append(faults, where+": `drawPairOnly` no longer composes `pairOnlyView`")
	}
	drawn := false
	for _, arg := range viewArgs {
		if strings.Contains(arg, "copyFor") {
			drawn = true
		}
	}
	if !drawn {
		faults = append(faults, where+": `pairOnlyView` is composed from `"+
			strings.Join(viewArgs, ", ")+"`, so the copy the reason chose reaches no view. The "+
			"parameter defaults to the first-run screen, which is exactly the screen this issue is "+
			"about")
	}
	return faults
}

// d0b8FunctionBodyOf is d0b8FunctionBody without the testing.T: this scan runs over fixtures where
// a missing body is an expected answer rather than a broken subject.
func d0b8FunctionBodyOf(code, name string) string {
	body, ok := kotlinFunBody(code, name)
	if !ok {
		return ""
	}
	return body
}

// ---------------------------------------------------------------------------
// The words: repair_required's cause, in the slot that is read before the control.
// ---------------------------------------------------------------------------

var w6o3ConstDecl = regexp.MustCompile(`(?m)^[ \t]*(?:private |internal )?const val (\w+)\s*=`)

var w6o3StringLiteral = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

var w6o3ConstReference = regexp.MustCompile(`\b[A-Z][A-Z0-9_]{2,}\b`)

// w6o3Constants maps each `const val` in the file to the expression it is assigned.
//
// The expression ends at the next declaration or the next blank line, whichever comes first.
// Over-reading would let text from a NEIGHBOURING constant satisfy the assertions below, which is
// a false pass -- the one failure mode a fence must not have.
func w6o3Constants(code string) map[string]string {
	out := map[string]string{}
	decls := w6o3ConstDecl.FindAllStringSubmatchIndex(code, -1)
	for i, m := range decls {
		end := len(code)
		if i+1 < len(decls) {
			end = decls[i+1][0]
		}
		expr := code[m[1]:end]
		if blank := strings.Index(expr, "\n\n"); blank >= 0 {
			expr = expr[:blank]
		}
		out[code[m[2]:m[3]]] = expr
	}
	return out
}

// w6o3Text resolves one Kotlin string expression -- literals and `const val` references, joined --
// to the text a reader would see. Depth is bounded because a constant that referred to itself
// would otherwise hang the suite.
func w6o3Text(expr string, consts map[string]string, depth int) string {
	if depth > 4 {
		return ""
	}
	var out strings.Builder
	for _, lit := range w6o3StringLiteral.FindAllStringSubmatch(expr, -1) {
		out.WriteString(lit[1])
		out.WriteByte(' ')
	}
	for _, ref := range w6o3ConstReference.FindAllString(w6o3StringLiteral.ReplaceAllString(expr, " "), -1) {
		if body, ok := consts[ref]; ok {
			out.WriteString(w6o3Text(body, consts, depth+1))
			out.WriteByte(' ')
		}
	}
	return out.String()
}

// w6o3CopyFaults reports every way repair_required's remedy can fail to be read before it is
// needed.
//
// @param code PairOnlyScreen.kt with its comments stripped and its STRING LITERALS KEPT: the
//  subject here is the words.
func w6o3CopyFaults(where, code string) []string {
	body, ok := kotlinFunBody(code, "copyFor")
	if !ok {
		return []string{where + ": nothing here composes `copyFor`, so this scan has no subject"}
	}
	arm := strings.Index(body, "PairOnlyReason.REPAIR_REQUIRED ->")
	if arm < 0 {
		return []string{where + ": `copyFor` has no REPAIR_REQUIRED arm. The state is terminal and " +
			"cannot clear itself, so a screen with nothing to say in it says nothing forever"}
	}
	args, ok := w6o3ArgumentsOfFirstCall(body[arm:], "PairOnlyCopy")
	if !ok || len(args) != 3 {
		return []string{where + ": the REPAIR_REQUIRED arm does not compose a three-part " +
			"PairOnlyCopy, so this fence cannot tell the body from the control"}
	}
	consts := w6o3Constants(code)
	// The slots are positional and the order is the reading order: title, body, control.
	bodyText, ctaText := w6o3Text(args[1], consts, 0), w6o3Text(args[2], consts, 0)

	var faults []string
	for _, step := range w6o3Unregister {
		if !strings.Contains(bodyText, step) {
			faults = append(faults, where+": repair_required's body never names `"+step+"`. The "+
				"machine still holds this device's registration and `swarm remote pair` is refused "+
				"while it does (PB-STATE-10), so a user who presses the only control on this screen "+
				"walks into a pairing that cannot complete -- the failure loop PB-APP-10 forbids, "+
				"reached through the remedy")
		}
		if strings.Contains(ctaText, step) {
			faults = append(faults, where+": repair_required puts `"+step+"` on the CONTROL. A "+
				"label is read as the thing being pressed, not as a step to carry out first, and "+
				"the sentence explaining the order is what the body slot is for")
		}
	}
	return faults
}

// ---------------------------------------------------------------------------
// The fences.
// ---------------------------------------------------------------------------

// TestW6O3_TheTerminalTransportStatesReachTheScreenTheySendThePhoneTo.
func TestW6O3_TheTerminalTransportStatesReachTheScreenTheySendThePhoneTo(t *testing.T) {
	code := kotlinWithoutStringLiterals(d0b8Code(t, d0b8PhoneSurface))
	if faults := w6o3WiringFaults(d0b8PhoneSurface, code); len(faults) > 0 {
		t.Errorf("agents-tracker-w6o3: the pair-only screen is drawn without the reason the "+
			"pairing ended:\n  %s\n\nThree endings share this screen -- a fresh install, an owner "+
			"who removed the device, and a destroyed relay-auth key -- and only one of them is "+
			"fixed by the control it offers.", strings.Join(faults, "\n  "))
	}
}

// TestW6O3_TheRemedyIsStatedBeforeTheControlThatNeedsIt.
func TestW6O3_TheRemedyIsStatedBeforeTheControlThatNeedsIt(t *testing.T) {
	if faults := w6o3CopyFaults(w6o3ScreenFile, d0b8Code(t, w6o3ScreenFile)); len(faults) > 0 {
		t.Errorf("agents-tracker-w6o3: the repair_required screen offers a bare pairing "+
			"control:\n  %s", strings.Join(faults, "\n  "))
	}
}

// TestW6O3_TheWiringScanDiscriminates is the control, on every link in the chain.
//
// Each fixture is the fix with ONE link cut, and every one of them passes the substring gate this
// file replaced -- which is what that gate's clean runs were worth.
func TestW6O3_TheWiringScanDiscriminates(t *testing.T) {
	const fixed = `class PhoneSurface {
    private fun renderReady(startup: PhoneStartup.Ready) {
        if (PairOnlyScreen.presentationOf { startup.app.stateSummary().paired } == Presentation.PAIR_ONLY) {
            drawPairOnly(
                PairOnlyScreen.reasonFor { ConnectionState.of(startup.app.connectionState()) },
                startup.app,
            )
            return
        }
    }

    private fun drawPairOnly(reason: PairOnlyReason, app: App) {
        val revoked = revokeNotice(app)
        host.addView(
            pairOnlyView(
                context = activity,
                notice = revoked,
                copy = PairOnlyScreen.copyFor(reason),
            ),
        )
    }
}`
	if faults := w6o3WiringFaults("fixed.kt", fixed); len(faults) > 0 {
		t.Fatalf("the scan rejects the shape this issue was fixed with, which is a fence nobody "+
			"can satisfy:\n%s", strings.Join(faults, "\n"))
	}

	// LINK 1 CUT: the draw is told a constant. The transport is never asked.
	blind := strings.Replace(fixed,
		"PairOnlyScreen.reasonFor { ConnectionState.of(startup.app.connectionState()) }",
		"PairOnlyReason.FIRST_RUN", 1)
	if faults := w6o3WiringFaults("blind.kt", blind); len(faults) != 1 {
		t.Errorf("the scan finds %d faults in a draw that never asks why the pairing ended:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}

	// LINK 2 CUT: a reason inferred from the durable blob instead of the transport, which is the
	// second opinion d0b8 argues against and cannot answer this question at all.
	guessed := strings.Replace(fixed,
		"ConnectionState.of(startup.app.connectionState())",
		"ConnectionState.of(startup.app.stateSummary().machine)", 1)
	if faults := w6o3WiringFaults("guessed.kt", guessed); len(faults) != 1 {
		t.Errorf("the scan finds %d faults in a reason read from anything but the transport:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}

	// LINK 3 CUT: the defect itself, with every symbol the old gate looked for still present.
	unlinked := strings.Replace(fixed, "copyFor(reason)", "copyFor(PairOnlyReason.FIRST_RUN)", 1)
	if faults := w6o3WiringFaults("unlinked.kt", unlinked); len(faults) != 1 {
		t.Errorf("the scan finds %d faults in a draw that computes the reason and then renders the "+
			"first-run copy over it -- the exact shape this issue is about, and the shape the "+
			"substring gate reported clean:\n%s", len(faults), strings.Join(faults, "\n"))
	}

	// LINK 4 CUT: the copy is resolved and the view is given the default.
	dropped := strings.Replace(fixed, "copy = PairOnlyScreen.copyFor(reason),", "", 1)
	if faults := w6o3WiringFaults("dropped.kt", dropped); len(faults) != 2 {
		t.Errorf("the scan finds %d faults where the copy reaches no view (the resolve is gone "+
			"with it, so both links report):\n%s", len(faults), strings.Join(faults, "\n"))
	}
}

// TestW6O3_TheCopyScanDiscriminates is the control on the words.
func TestW6O3_TheCopyScanDiscriminates(t *testing.T) {
	const shape = `object PairOnlyScreen {
    const val BODY = "Sessions, machines and activity all come from the machine this phone is paired with."

    const val CTA = "Pair a computer"

    private const val UNREGISTER_FIRST = "run ` + "`swarm remote devices`" + ` on your machine to find " +
        "this device and ` + "`swarm remote revoke <device-id>`" + ` to unregister it"

    private const val REPAIR_REQUIRED_CAUSE = "This phone's key was destroyed and cannot be " +
        "recovered. Your machine still has this device registered, so " + UNREGISTER_FIRST +
        " before pairing this phone again."

    fun copyFor(reason: PairOnlyReason): PairOnlyCopy = when (reason) {
        PairOnlyReason.FIRST_RUN -> PairOnlyCopy(TITLE, BODY, CTA)

        PairOnlyReason.REPAIR_REQUIRED -> PairOnlyCopy(
            TITLE_REPAIR_REQUIRED,
            REPAIR_REQUIRED_CAUSE + " " + BODY,
            CTA,
        )
    }
}`
	if faults := w6o3CopyFaults("shape.kt", shape); len(faults) > 0 {
		t.Fatalf("the scan rejects a screen that states the machine-side step in its body and then "+
			"offers the control:\n%s", strings.Join(faults, "\n"))
	}

	// THE BARE CONTROL PB-APP-10 FORBIDS: a terminal state with the first-run sentence under it.
	bare := strings.Replace(shape, "REPAIR_REQUIRED_CAUSE + \" \" + BODY,", "BODY,", 1)
	if faults := w6o3CopyFaults("bare.kt", bare); len(faults) != 2 {
		t.Errorf("the scan finds %d faults where repair_required offers a pairing control with no "+
			"statement of the step the machine has to take first:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}

	// THE STEP ANNOUNCED ON THE CONTROL, which is the same words in the one slot that is read
	// after the press rather than before it.
	onControl := strings.Replace(shape,
		"REPAIR_REQUIRED_CAUSE + \" \" + BODY,\n            CTA,",
		"BODY,\n            CTA + \" -- \" + UNREGISTER_FIRST,", 1)
	if faults := w6o3CopyFaults("oncontrol.kt", onControl); len(faults) != 4 {
		t.Errorf("the scan finds %d faults where the remedy is a label on the button rather than "+
			"the sentence above it (two steps, missing from the body and present on the "+
			"control):\n%s", len(faults), strings.Join(faults, "\n"))
	}
}
