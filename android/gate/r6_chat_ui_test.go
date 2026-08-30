package gate

// FAILING-FIRST (TDD RED, GG-5) for Wave R6's Mirror M2 Kotlin surface -- the go-lane
// runtime RED companion of the compile-RED JVM suites (the r5_launch_ui_test.go
// precedent). Bead: agents-tracker-hggx.7.
//
// What it fences, per M-row:
//
//   - M2.1: ui/kit/Markdown.kt -- the pure, injection-safe markdown subset renderer,
//     plus its exhaustive JVM suite.
//   - M2.2: ui/kit/ToolCard.kt -- the tool card kit component keyed by the flat
//     `tool_kind` glyph vocabulary; InteractionItem carries the additive fields
//     (toolKind, source, turnId, tsUnixMs) and FacadeBridge maps them, so no screen
//     parses Body to draw a glyph or a separator (IS-TOOL-1).
//   - M2.3: the incremental transcript model and its burst/scroll JVM suite.
//   - M2.4/M2.5: the composer's visible pending/sent/refused send states with the
//     gentle stale_turn notice and the status-driven placeholder.
//   - M3.2 + M3 deep-links: the session-detail open policy (throttled auto-backfill)
//     and the deep-link anchor that lands on an item_id or misses honestly.
//
// OBSIDIAN IS NOT RE-FENCED HERE: s24_screens_test.go's PB-DS-11/PB-DS-6 sweeps cover
// all production Kotlin, so every new file is inside the existing fence on creation.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func r6KitDir(t *testing.T) string {
	return filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone", "ui", "kit")
}

func r6UIDir(t *testing.T) string {
	return filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone", "ui")
}

func r6ReadSource(t *testing.T, path, what string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s does not exist yet: %v", what, err)
	}
	return string(raw)
}

// TestR6_MarkdownRendererExistsAndIsPure: M2.1's renderer is a pure String -> blocks
// model in the kit -- no android.text.Html, no WebView, ever: both interpret markup, and
// interpretation is the injection surface the subset renderer exists to not have.
func TestR6_MarkdownRendererExistsAndIsPure(t *testing.T) {
	src := r6ReadSource(t, filepath.Join(r6KitDir(t), "Markdown.kt"), "M2.1's markdown renderer")
	for _, sym := range []string{"object Markdown", "fun parse", "MarkdownBlock"} {
		if !strings.Contains(src, sym) {
			t.Errorf("Markdown.kt lacks %q; the JVM suite drives exactly that contract", sym)
		}
	}
	for _, forbidden := range []string{"android.text.Html", "WebView", "loadData"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("Markdown.kt mentions %q; the renderer is a pure parser, never an HTML "+
				"interpreter (M2.1: escaping-safe)", forbidden)
		}
	}
}

// TestR6_ToolCardExistsAndReadsTheFlatToolKind: M2.2's kit card picks its glyph from the
// item's flat toolKind field and never parses Body.
func TestR6_ToolCardExistsAndReadsTheFlatToolKind(t *testing.T) {
	src := r6ReadSource(t, filepath.Join(r6KitDir(t), "ToolCard.kt"), "M2.2's tool card")
	for _, sym := range []string{"ToolCard", "toolKind"} {
		if !strings.Contains(src, sym) {
			t.Errorf("ToolCard.kt lacks %q", sym)
		}
	}
	if strings.Contains(src, "JSONObject") {
		t.Error("ToolCard.kt parses JSON; the glyph reads the flat toolKind field (IS-TOOL-1's " +
			"posture at this boundary) and composition stays with the screen")
	}
}

// TestR6_InteractionItemCarriesTheAdditiveFieldsAndTheBridgeMapsThem: turnId and
// tsUnixMs (M2.2's separators and timestamps, "finally mapped"), toolKind (the glyph),
// source (M2.4's honest attribution) -- on the data class AND mapped in FacadeBridge's
// transcript(), so the screens receive facts, not absences.
//
// operationId and interactionId JOINED THIS LIST 2026-08-26, AND THIS FENCE IS PRECISELY WHAT
// WOULD HAVE CAUGHT THEM BEING ABSENT. Both crossed the facade and stopped one hop short of the
// screen, under a comment in FacadeBridge.kt that says the mapping exists "so they cross the
// boundary instead of dying one hop short of the screen" -- the comment described the fix and the
// field was not in the list beneath it. The cost was not cosmetic: without operationId the phone
// cannot match an agent's echo to the message it sent, so owner ruling R6 (a sent bubble is
// PENDING until the transcript echoes it back) had no mechanism at all, and the composer rendered
// the daemon's "bytes written into a PTY" as the word "Sent". A design-honesty review found it by
// reading the comment against the list; this loop finds it by running.
func TestR6_InteractionItemCarriesTheAdditiveFieldsAndTheBridgeMapsThem(t *testing.T) {
	item := r6ReadSource(t, filepath.Join(r6UIDir(t), "InteractionItem.kt"), "InteractionItem")
	for _, field := range []string{"turnId", "tsUnixMs", "toolKind", "source", "detail", "operationId", "interactionId"} {
		if !strings.Contains(item, field) {
			t.Errorf("InteractionItem.kt lacks the additive field %q", field)
		}
	}
	// THE FIELD LOOP ABOVE IS A DECLARATION CHECK ON PURPOSE -- it asks whether the data class
	// HOLDS the field -- and it is scoped to the one file that would declare it, so it cannot be
	// satisfied by a foreign declaration. The getter loop below is a REACH check, the class that
	// round 3's closing review found self-satisfying three times, and it is safe for a structural
	// reason: every `getX` here is generated by gomobile onto the AAR's Go type, so no Kotlin
	// declares any of them and a match in FacadeBridge.kt can only be the call.
	bridge := r6ReadSource(t, filepath.Join(r6UIDir(t), "FacadeBridge.kt"), "FacadeBridge")
	for _, getter := range []string{"getTurnID", "getTSUnixMs", "getToolKind", "getSource", "getDetail", "getOperationID"} {
		if !strings.Contains(bridge, getter) {
			t.Errorf("FacadeBridge.transcript() never maps %s; the field would cross the "+
				"boundary and die one hop short of the screen", getter)
		}
	}
}

// TestR6_ComposerCarriesTheVisibleSendStates: ADR-009 (6) -- pending -> sent -> refused
// is a visible per-send state, the stale_turn refusal has its own gentle notice, and the
// placeholder is status-driven (M2.5).
func TestR6_ComposerCarriesTheVisibleSendStates(t *testing.T) {
	src := r6ReadSource(t, filepath.Join(r6KitDir(t), "Composer.kt"), "the composer")
	for _, sym := range []string{"SendState", "PENDING", "SENT", "REFUSED", "STALE_TURN"} {
		if !strings.Contains(src, sym) {
			t.Errorf("Composer.kt lacks %q; a send that cannot be seen pending, sent or refused "+
				"is a message silently swallowed (PB-INPUT-2, ADR-009 (6))", sym)
		}
	}
}

// TestR6_TheChatKitIsReachedFromProductionKotlin is the FIX-PACK's fence for review finding
// B8, and it is the one this wave most needed and did not have.
//
// THE FINDING: the entire chat kit M2 built was PARKED. Nothing in android/app/src/main
// referenced Markdown, ToolCard, TranscriptIncremental, ComposerModel, SessionDetailOpen or
// DeepLinkAnchor -- the single grep hit across the whole app was a COMMENT in ErrorRouting.kt
// -- while every one of them had an exhaustive JVM suite and the evidence file marked their
// rows GREEN. Playbook 8.1 is a PHYSICAL demonstration by the owner on a real handset, so a
// kit nothing composes is a wave that cannot be demonstrated at all.
//
// WHY A GREP AND NOT A KOTLIN TEST. This is exactly the gomobile blindness one layer up: a
// pure model compiles, its suite passes, and whether any screen CALLS it is invisible to both.
// A Robolectric test can assert what a composition draws; it cannot assert that the
// composition is the one the app runs. android/unbound-verbs.tsv makes the same argument for
// facade verbs and is the direct precedent -- this is that control turned on the kit.
// EVERY SYMBOL BELOW IS A DOTTED MEMBER REFERENCE and therefore cannot be satisfied by its own
// declaration, which is the defect class round 3's closing review caught twice in this file:
// `object ToolCard {` and `fun parse(` hold no dot, so `ToolCard.` and `Markdown.parse` can only
// be spent at a call. `markdownBody` was the EXCEPTION and the sweep's own second find -- it is a
// top-level `fun markdownBody(` in Markdown.kt, inside the very blob this check reads, so the
// module-wide form matched the declaration and said nothing about any caller. It is scoped below
// to the one screen file that has to spend it, which does not declare it (it imports it).
func TestR6_TheChatKitIsReachedFromProductionKotlin(t *testing.T) {
	body := stripKotlinComments(appKotlinSource(t))
	for _, want := range []struct{ symbol, in, row string }{
		{"Markdown.parse", "", "M2.1 -- no screen renders the agent's prose as markdown"},
		{"markdownBody(", "screens/TranscriptView.kt", "M2.1 -- the parsed spans never become type on a row"},
		{"ToolCard.", "", "M2.2 -- no screen draws a tool card's glyph, time or expansion"},
		{"TranscriptIncremental.", "", "M2.3 -- the transcript is still rebuilt whole per event"},
		{"ComposerModel.", "", "M2.4/M2.5 -- the composer's states are a table nobody reads"},
		{"SessionDetailOpen.", "", "M3.2 -- nothing backfills a cold session-detail open"},
		{"DeepLinkAnchor.", "", "M3 -- a tap that lands on nothing still says nothing"},
	} {
		haystack, where := body, "production Kotlin"
		if want.in != "" {
			haystack = stripKotlinComments(r6ReadSource(t,
				filepath.Join(r6UIDir(t), filepath.FromSlash(want.in)), want.in))
			where = want.in
		}
		if !strings.Contains(haystack, want.symbol) {
			t.Errorf("Wave R6 finding B8: %s never references %q, so %s. A model with an "+
				"exhaustive suite and no caller is the parked-kit defect this wave shipped once "+
				"already; the evidence file must not read GREEN over one.", where, want.symbol, want.row)
		}
	}
}

// TestR6_TheTranscriptRendersTheTearAndTheChatFacts fences the SCREEN MODEL's own vocabulary,
// because a grep for a symbol cannot tell the difference between drawing a fact and holding it.
//
// The four fields below are the ones a reader of playbook 8.1 has to be able to SEE: the
// markdown of a message, the glyph of a tool call, ADR-017's proven tear, and IS-CAP-2's offer
// to fetch what was clipped. `gap` is the load-bearing one -- ADR-017 T2 rule 2 forbids a
// structured_gap being silently bridged, and the arm that draws it is what stops the neutral
// row printing the wire's kind name at a person.
func TestR6_TheTranscriptRendersTheTearAndTheChatFacts(t *testing.T) {
	panel := r6ReadSource(t,
		filepath.Join(r6UIDir(t), "screens", "TranscriptPanel.kt"), "the transcript screen model")
	for _, field := range []string{"markdown", "glyph", "timestamp", "gap", "offersDetail", "structureTorn"} {
		if !strings.Contains(panel, field) {
			t.Errorf("TranscriptPanel.kt carries no %q, so the screen cannot say it", field)
		}
	}
	if !strings.Contains(panel, "structured_gap") {
		t.Error("TranscriptPanel.kt has no structured_gap arm, so ADR-017's proven boundary falls " +
			"to the neutral row -- which prints the wire's kind name at a reader, or, before " +
			"finding B4, drew the rows either side of the tear adjacent")
	}
	view := r6ReadSource(t,
		filepath.Join(r6UIDir(t), "screens", "TranscriptView.kt"), "the transcript view")
	// A REACH CHECK, and dotted, so it cannot match its own declaration: the constant is declared
	// inside `object TranscriptTag {` in this same file as a bare `GAP`, and only a spend of it
	// carries the qualifier. (The field loop above is a declaration check by design, scoped to
	// the model file that would declare them.)
	if !strings.Contains(view, "TranscriptTag.GAP") {
		t.Error("TranscriptView.kt draws no tagged tear, so the boundary has no view of its own " +
			"and the conversation reads as continuous across it (ADR-017 T2 rule 2)")
	}
}

// TestR6_TheJVMSuitesExist: each M-row's contract has its named suite in the test tree,
// so the gradle lane compiles the frozen contracts (compile-RED until the models exist).
func TestR6_TheJVMSuitesExist(t *testing.T) {
	testKotlin := filepath.Join(appModule(t), "src", "test", "kotlin", "dev", "swarm", "phone")
	suites := []struct{ rel, row string }{
		{filepath.Join("ui", "kit", "MarkdownTest.kt"), "M2.1"},
		{filepath.Join("ui", "kit", "MarkdownLinkHonestyTest.kt"), "M2.1 / finding B11"},
		{filepath.Join("ui", "screens", "TranscriptChatRenderTest.kt"), "M2.1+M2.2 / finding B8"},
		{filepath.Join("ui", "screens", "TranscriptIncrementalPositionTest.kt"), "M2.3 / finding B12"},
		{filepath.Join("ui", "screens", "TranscriptIncrementalRedrawTest.kt"), "M2.3 view half"},
		{filepath.Join("ui", "screens", "SessionDetailComposerTest.kt"), "M2.4/M2.5 view half"},
		{filepath.Join("ui", "kit", "ToolCardTest.kt"), "M2.2"},
		{filepath.Join("ui", "screens", "TranscriptIncrementalTest.kt"), "M2.3"},
		{filepath.Join("ui", "kit", "ComposerSendStateTest.kt"), "M2.4/M2.5"},
		{filepath.Join("ui", "screens", "SessionDetailOpenTest.kt"), "M3.2 + deep-links"},
	}
	for _, s := range suites {
		if _, err := os.Stat(filepath.Join(testKotlin, s.rel)); err != nil {
			t.Errorf("%s has no JVM suite at %s: %v", s.row, s.rel, err)
		}
	}
}

// TestR6R2_EveryChatVerbsMachineAnswerIsClaimedOnTheSurface is Wave R6 review ROUND 2's
// headline blocker, fenced where it happened.
//
// THE FINDING. `VerbDispatch.press` settles on the result of the FACADE CALL, and all four R6
// verbs return their `*Op` the instant the envelope is appended to the mailbox --
// `signedCommand`/`sealSignedCommand` for the composer and the Stop, `unsignedRead` for the two
// M3 reads. The four presses discarded that Op and never polled it, so:
//
//   - the composer said "Sent" and erased the draft on LOCAL SEALING, before the machine had
//     seen the message, and a refused send was shown as sent with the user's words gone;
//   - the daemon's `stale_turn` could not route, making the one refusal M2.4 wrote gentle copy
//     for unreachable;
//   - `adoptInteractionRead` is called only from `App.Outcome`, so NO history page and NO
//     detail body ever folded -- "Load earlier" did nothing and never disappeared, and a tap
//     on a clipped card changed nothing while reporting success.
//
// Every OTHER machine-answering control on this surface keeps the op id and claims it on a
// later draw -- kill, approve, take_control, the two preset verbs -- all through
// `bridge.launchOutcome(...)`. There is no generic outcome drain, so a verb that does not do
// this is fire-and-forget, silently.
//
// WHY A GREP. Same reason as TestR6_TheChatKitIsReachedFromProductionKotlin one function up:
// the claim is a fact about the PRODUCTION composition, and no JVM test can reach it -- the
// phone core is a gomobile AAR this test JVM does not load. What the claim MEANS is pinned as
// a value in SessionDetailVerdictTest; this is what pins that it happens at all.
func TestR6R2_EveryChatVerbsMachineAnswerIsClaimedOnTheSurface(t *testing.T) {
	body := stripKotlinComments(appKotlinSource(t))
	// THE THREE CHECKS IN THIS LOOP STAY MODULE-WIDE, and each is structurally unable to match
	// its own declaration, which is the property the scoped loop below had to be given by hand:
	// `app.composerSend(` is a RECEIVER-QUALIFIED call and the declaration it reaches is Go
	// behind the AAR, not Kotlin in this blob; `composerOp = ` cannot match
	// `private var composerOp: String = ""` (the type annotation sits between the name and the
	// `=`); and `launchOutcome(composerOp)` names an ARGUMENT, which no declaration does. Which
	// function does the latching is deliberately not fenced -- the composer latches in the press
	// and the reads latch in two different places -- so the fact under test is that the id
	// survives to a claim at all.
	for _, want := range []struct{ latch, claim, verb, row string }{
		{"composerOp = ", "launchOutcome(composerOp)", "app.composerSend(",
			"M2.4 -- a composer_send is fire-and-forget"},
		{"interruptOp = ", "launchOutcome(interruptOp)", "app.interrupt(",
			"M2.4 -- a Stop's refusal never reaches the reader"},
		// History is a ledger rather than a scalar: cold-open and reader presses may both seal
		// before either outcome arrives, so one `historyOp` would overwrite an unclaimed reply.
		{"historyReads[issued.operationID]", "launchOutcome(operationID)",
			"app.loadEarlierInteractions(", "M3.1 -- a history page never folds"},
		{"detailOp = ", "launchOutcome(detailOp)", "app.loadInteractionDetail(",
			"M3.3 -- a clipped card never expands"},
	} {
		if !strings.Contains(body, want.verb) {
			t.Errorf("no production Kotlin calls %s at all (%s)", want.verb, want.row)
			continue
		}
		if !strings.Contains(body, want.latch) {
			t.Errorf("%s is never latched, so the operation id the machine keys its answer by is "+
				"gone before the answer exists (%s)", want.latch, want.row)
		}
		if !strings.Contains(body, want.claim) {
			t.Errorf("no draw claims %s through bridge.launchOutcome, so %s. Every other "+
				"machine-answering control on this surface claims its own operation by id "+
				"(PB-SYNC-2); there is no generic drain that would catch this one", want.latch, want.row)
		}
	}
	// AND THE SCOPED HALF (round 3's CLOSING review, second instance).
	//
	// These four were checked with strings.Contains over `appKotlinSource` -- the WHOLE app
	// module -- where every one of them matched its own DECLARATION and so said nothing about
	// the CALL: `fun composerVerdictFor(` and `val clearsDraft:` and `fun interruptNoticeFor(`
	// in SessionDetailPanel.kt, `fun historyAtCapacity(` in FacadeBridge.kt. The closing review
	// PROVED it by execution: gutting renderComposerVerdict -- keeping `bridge.launchOutcome(
	// composerOp)` and `composerOp = ""` so the latch loop above still passed, and replacing the
	// verdict with an unconditional `composerSendState = SendState.SENT; composerRefusal = "";
	// typed.text.clear()` -- plus gutting renderInterruptVerdict's notice, left this whole gate
	// package at exit 0 with the round-2 headline blocker live again: a REFUSED send displayed
	// as "Sent" with the user's typed words erased, and stale_turn's gentle copy dead.
	//
	// This is TestR6R3_TheTwoM3ReadsSayWhatTheMachineSaid's correction, one function down, made
	// in the sweep that fixed the sibling and missed this one. Each check now reads the single
	// production function that has to make the call, so the fence fails on the deletion it
	// exists to catch.
	for _, want := range []struct{ fn, symbol, row string }{
		{"renderComposerVerdict", "composerVerdictFor(",
			"the composer decides on the MACHINE's answer"},
		{"renderComposerVerdict", "clearsDraft",
			"the field is emptied by the verdict rather than by the local seal"},
		{"renderInterruptVerdict", "interruptNoticeFor(",
			"a refused Stop says the turn was not stopped"},
		// BOTH HALVES OF ADR-014 A8's capacity fact, which are two different sentences to the
		// reader: renderReadVerdicts SAYS it once where the finger was, and detailPanel DROPS
		// the "Load earlier" control on the same fact. Deleting either one alone is a defect.
		{"renderReadVerdicts", "historyAtCapacity",
			"the reader is never told when this phone can hold no more history"},
		{"detailPanel", "historyAtCapacity",
			"the exhausted control goes on offering a page this phone cannot hold"},
	} {
		arm := r6SurfaceFunc(t, want.fn, "the call it must make has nowhere to live")
		if !strings.Contains(arm, want.symbol) {
			t.Errorf("%s -- the one production function that must make the call -- never "+
				"references %q, so %s (Wave R6 round 3, closing review). Body:\n%s",
				want.fn, want.symbol, want.row, arm)
		}
	}
}

// TestR6R3_TheTwoM3ReadsSayWhatTheMachineSaid is Wave R6 review ROUND 3's finding F4, fenced
// where it happened.
//
// THE FINDING. Every machine-answering verb on session detail says a VERB-SPECIFIC sentence and
// puts the daemon's own words in the detail cell -- the composer, the Stop, the kill, the
// approval. The two M3 reads did not: both called
// `say(PressFeedback.ofRefusal(ErrorRouter.routeMachineCode(outcome.code)))`, the ONE-ARG
// overload, which sets no detail at all. So the machine's sentence was dropped, and because
// `MachineRefusalCodes.toToken` deliberately holds only stale_turn and rate_limit -- one-verb
// facts belong to the screen, which is that map's own stated rule -- `unavailable` and
// `invalid_field` fell to `ErrorState.UNKNOWN`: "Something failed in a way the app does not
// recognise. Try again, and report it if it keeps happening."
//
// WHAT THAT MEANT ON THE SCREEN. Tapping a clipped card whose body the daemon had EVICTED
// (IS-CAP-3's `unavailable`) showed that sentence -- advice to retry what retrying can never fix
// -- and the offer under the card, derived from fields journalled at CAPTURE time, stayed
// tappable forever.
//
// WHY A GREP: TestR6R2_EveryChatVerbsMachineAnswerIsClaimedOnTheSurface's reason exactly. What
// the sentences SAY is pinned as values in SessionDetailVerdictTest; this pins that the surface
// says them.
//
// AND WHY THE GREP IS SCOPED (round 3's CLOSING review, and F1's defect class recurring inside
// the round mandated to sweep for it). These symbols were first checked with strings.Contains
// over `appKotlinSource` -- the WHOLE app Kotlin -- where every one of them matched its own
// DECLARATION (`fun historyReadNoticeFor(` in SessionDetailPanel.kt, the `withoutDetail:
// Set<String>` parameters in TranscriptPanel.kt) and so said nothing about the CALL. Gutting all
// four call sites -- hardcoding the two notice/detail pairs, deleting the `detailRefused.add`
// block, deleting `withoutDetail = detailRefused` from the transcript draw -- left this gate and
// the whole JVM suite green. Each check below now reads the one production function that has to
// make the call, so the fence fails on the deletion it exists to catch.
func TestR6R3_TheTwoM3ReadsSayWhatTheMachineSaid(t *testing.T) {
	arm := r6ReadVerdictsBody(t)
	for _, want := range []struct{ symbol, row string }{
		{"historyReadNoticeFor(", "a refused history page falls back to a generic routed remedy"},
		{"historyReadDetailFor(", "the machine's own words about a refused page are dropped"},
		{"detailReadNoticeFor(", "a refused detail read falls back to a generic routed remedy"},
		{"detailReadDetailFor(", "the machine's own words about a refused body are dropped"},
		{"detailReadIsTerminal(", "an evicted body goes on offering a fetch that can never succeed"},
		{"detailRefused.add(", "nothing ever records that the machine has settled this card"},
	} {
		if !strings.Contains(arm, want.symbol) {
			t.Errorf("renderReadVerdicts -- the one function that answers the two M3 reads -- never "+
				"calls %q, so %s (Wave R6 round 3, finding F4). Body:\n%s",
				want.symbol, want.row, arm)
		}
	}
	// AND THE RECORD REACHES THE DRAW. `detailRefused` is only worth keeping if the transcript is
	// told about it: the withdrawal is the visible half of F4, and it is one named argument away
	// from being lost with every other fence still green.
	if draw := r6DetailPanelBody(t); !strings.Contains(draw, "withoutDetail = detailRefused") {
		t.Errorf("detailPanel draws the transcript without passing `withoutDetail = detailRefused`, "+
			"so the cards the machine has answered `unavailable` for go on offering a fetch that "+
			"can never succeed -- the reader taps, reads that the body is gone, and is invited to "+
			"tap again (Wave R6 round 3, finding F4). Body:\n%s", draw)
	}
	// AND THE ROUTER IS NOT BACK. The defect is not the absence of the four symbols above but
	// the PRESENCE of the generic route on these two answers, so the fence reads the one
	// function that draws them.
	if strings.Contains(arm, "routeMachineCode") {
		t.Errorf("renderReadVerdicts routes an M3 read's wire code through "+
			"ErrorRouter.routeMachineCode again. `unavailable` and `invalid_field` are absent "+
			"from MachineRefusalCodes.toToken ON PURPOSE -- they are facts about one verb -- so "+
			"routing them answers ErrorState.UNKNOWN's \"Something failed in a way the app does "+
			"not recognise. Try again\" to a user whose retry can never work, with the daemon's "+
			"own sentence discarded by the detail-less ofRefusal overload. Body:\n%s", arm)
	}
}

// r6ReadVerdictsBody returns PhoneSurface's renderReadVerdicts, comments stripped.
func r6ReadVerdictsBody(t *testing.T) string {
	t.Helper()
	return r6SurfaceFunc(t, "renderReadVerdicts",
		"the two M3 reads claim no answer at all")
}

// r6DetailPanelBody returns PhoneSurface's detailPanel -- the one place session detail's
// transcript is drawn -- comments stripped.
func r6DetailPanelBody(t *testing.T) string {
	t.Helper()
	return r6SurfaceFunc(t, "detailPanel",
		"nothing composes session detail's transcript")
}

// r6SurfaceFunc returns the body of one private method of PhoneSurface, comments stripped, so a
// fence can assert about the CALL SITE rather than about a symbol that also matches its own
// declaration somewhere else in the module.
func r6SurfaceFunc(t *testing.T, name, missing string) string {
	t.Helper()
	src := stripKotlinComments(r6ReadSource(t,
		filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone", "PhoneSurface.kt"),
		"the phone surface"))
	opener := "private fun " + name
	at := strings.Index(src, opener)
	if at < 0 {
		t.Fatalf("PhoneSurface.kt has no %s: %s", name, missing)
	}
	rest := src[at+len(opener):]
	if end := strings.Index(rest, "\n    private fun "); end >= 0 {
		return rest[:end]
	}
	return rest
}
