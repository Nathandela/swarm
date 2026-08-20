package codex

// FAILING-FIRST (TDD RED, GG-5) for Mirror M4.2/M4.3/M4.5's ADAPTER half: the Codex
// InteractionSource. Bead: agents-tracker-hggx.8. ADR-010 (the frozen seam), ADR-013 §R7.3.
//
// EVERY FRAME DRIVEN HERE IS A FILE ON DISK, and the file says which class it is
// (docs/verification/r1-codex-fixtures/r7-PROVENANCE.md):
//
//   - RECORDED, off a live 0.147.0 connection during the R1 gate: frame-samples.json,
//     approval-request.json, turn-started.json, turn-completed-interrupted.json.
//     A test may assert on these as observed truth.
//   - SCHEMA-DERIVED, from `codex app-server generate-ts` / `generate-json-schema` run
//     offline on the same binary: r7-schema-derived-frames.json. Field NAMES and string
//     unions are authoritative; VALUES are illustrative. Used only where the gate captured
//     no frame at all -- commandExecution's item lifecycle, and the clientId echo key.
//
// Nothing here is invented. If a shape is missing from both, the test is not written and the
// question is in r7-open-questions.md instead.
//
// THE CONTRACT: `Interactions(p adapter.HookPayload) []adapter.Interaction` where p.Event is
// the JSON-RPC METHOD and p.Raw is the WHOLE FRAME verbatim (ADR-013 §R7.3's table). Pure and
// total on ExtractConversationID's terms. Normalized fields and nothing else: item ids,
// ordering, the turn, timestamps, caps, redaction, hashing and expiry are all daemon-side.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

// r7FixtureDir is the R1 gate's fixture directory, from this package.
const r7FixtureDir = "../../../docs/verification/r1-codex-fixtures"

// r7RecordedFrame returns the RECORDED frame from frame-samples.json whose method is
// method and whose direction is recv, verbatim.
func r7RecordedFrame(t *testing.T, method string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r7FixtureDir, "frame-samples.json"))
	if err != nil {
		t.Fatalf("read the RECORDED frame corpus: %v", err)
	}
	var samples []struct {
		Dir string          `json:"dir"`
		Msg json.RawMessage `json:"msg"`
	}
	if err := json.Unmarshal(data, &samples); err != nil {
		t.Fatalf("decode frame-samples.json: %v", err)
	}
	for _, s := range samples {
		if s.Dir != "recv" {
			continue
		}
		var probe struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(s.Msg, &probe) == nil && probe.Method == method {
			return s.Msg
		}
	}
	t.Fatalf("frame-samples.json records no recv frame for method %q; a test may not invent one "+
		"(ADR-010 obligation 4: fixtures are RECORDED)", method)
	return nil
}

// r7WholeFixture returns one whole fixture file (approval-request.json, turn-*.json).
func r7WholeFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r7FixtureDir, name))
	if err != nil {
		t.Fatalf("read RECORDED fixture %s: %v", name, err)
	}
	return data
}

// r7DerivedFrame returns the SCHEMA-DERIVED frame named name. See r7-PROVENANCE.md: shape is
// authoritative, values are illustrative.
func r7DerivedFrame(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r7FixtureDir, "r7-schema-derived-frames.json"))
	if err != nil {
		t.Fatalf("read the schema-derived frames: %v", err)
	}
	var doc struct {
		Frames []struct {
			Name string          `json:"name"`
			Msg  json.RawMessage `json:"msg"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode r7-schema-derived-frames.json: %v", err)
	}
	for _, f := range doc.Frames {
		if f.Name == name {
			return f.Msg
		}
	}
	t.Fatalf("no schema-derived frame named %q", name)
	return nil
}

// r7Source is the adapter under test as an InteractionSource, or a fatal.
func r7Source(t *testing.T) adapter.InteractionSource {
	t.Helper()
	src, ok := adapter.AsInteractionSource(New())
	if !ok {
		t.Fatal("the codex adapter proves no InteractionSource; M4.2 makes Codex a structured " +
			"chat provider EQUAL to Claude, and without this seam captureInteractions returns 0 " +
			"for every frame (internal/skeleton/interaction.go's first branch)")
	}
	return src
}

// r7Shape runs one frame through the adapter and Validates every item it shaped.
func r7Shape(t *testing.T, method string, raw []byte) []adapter.Interaction {
	t.Helper()
	out := r7Source(t).Interactions(adapter.HookPayload{Event: method, Raw: raw})
	for i, in := range out {
		if err := in.Validate(); err != nil {
			t.Fatalf("%s shaped an item[%d] that fails the schema: %v", method, i, err)
		}
	}
	return out
}

// r7Only asserts exactly one item was shaped and returns it.
func r7Only(t *testing.T, method string, raw []byte) adapter.Interaction {
	t.Helper()
	out := r7Shape(t, method, raw)
	if len(out) != 1 {
		t.Fatalf("%s shaped %d items, want exactly 1: %+v", method, len(out), out)
	}
	return out[0]
}

// ---------------------------------------------------------------- M4.2: user_message

// TestR7CodexInteractions_UserMessageItemBecomesAUserMessage drives the RECORDED
// `item/started` frame whose item type is `userMessage` (frame-samples.json).
func TestR7CodexInteractions_UserMessageItemBecomesAUserMessage(t *testing.T) {
	in := r7Only(t, "item/started", r7RecordedFrame(t, "item/started"))

	if in.Kind != adapter.KindUserMessage {
		t.Fatalf("kind = %q, want %q", in.Kind, adapter.KindUserMessage)
	}
	const want = "Count from 1 to 40. Put each number on its own line and write one full sentence " +
		"of trivia about each number. Take your time."
	if in.Text != want {
		t.Errorf("text = %q, want the RECORDED content[0].text %q; the frame's `content` is an "+
			"ARRAY of UserInput and the text arm carries `text`", in.Text, want)
	}
	if in.Ref != "01a0033b-d17f-7070-9744-a3fb14dee165" {
		t.Errorf("ref = %q, want params.item.id; Ref is the CLI's own id and is what the daemon "+
			"folds successive records under one item_id by (skeleton's itemIDLocked)", in.Ref)
	}
	if in.Source != adapter.SourceOwner {
		t.Errorf("source = %q, want %q: the adapter reports every prompt as owner-typed because it "+
			"CANNOT KNOW, and the daemon re-attributes the one it injected (ADR-010 §3)",
			in.Source, adapter.SourceOwner)
	}
}

// TestR7CodexInteractions_UserMessageCarriesTheClientIdEchoKey drives the SCHEMA-DERIVED
// frame that proves the exact composer-echo correlation. `TurnStartParams` and
// `TurnSteerParams` both carry `clientUserMessageId` and the `userMessage` ThreadItem
// carries `clientId`, so the daemon that minted the id reads it straight back -- no text
// matching, which is the defect ADR-013 §R7.5 refuses to carry onto a new provider.
func TestR7CodexInteractions_UserMessageCarriesTheClientIdEchoKey(t *testing.T) {
	in := r7Only(t, "item/started", r7DerivedFrame(t, "userMessageStartedWithClientId"))

	if in.ClientRef != "swarm:01JQ7Z0V0000000000000000" {
		t.Errorf("client_ref = %q, want the frame's params.item.clientId; this is the ONLY exact "+
			"echo key on any provider and dropping it forces Codex back onto text correlation, "+
			"whose own probed mis-attribution is recorded at internal/skeleton/chat.go:52-70", in.ClientRef)
	}
}

// ------------------------------------------------------- M4.2: agent_message increments

// TestR7CodexInteractions_AgentMessageDeltaIsAnIncrementFoldedByItemId drives the RECORDED
// `item/agentMessage/delta` frame. IS-DELTA-1: Text on an agent_message is the INCREMENT
// this record appends, never the accumulated body.
func TestR7CodexInteractions_AgentMessageDeltaIsAnIncrementFoldedByItemId(t *testing.T) {
	in := r7Only(t, "item/agentMessage/delta", r7RecordedFrame(t, "item/agentMessage/delta"))

	if in.Kind != adapter.KindAgentMessage {
		t.Fatalf("kind = %q, want %q", in.Kind, adapter.KindAgentMessage)
	}
	if in.Status != adapter.StatusInProgress {
		t.Errorf("status = %q, want %q; a delta is non-terminal and IS-ST-1 allows at most one "+
			"terminal status per item -- a delta marked completed would close the message at its "+
			"first token", in.Status, adapter.StatusInProgress)
	}
	if in.Text != "1" {
		t.Errorf("text = %q, want the RECORDED params.delta %q -- the INCREMENT, not an accumulation", in.Text, "1")
	}
	if in.Ref != "msg_0974dc2bafd212c4016a7fcdc8d7c08191aafd5c23b36f9a53" {
		t.Errorf("ref = %q, want params.itemId; this is what folds 586 delta frames into ONE "+
			"agent_message (r1-codex-gate.md:104-105)", in.Ref)
	}
	if in.StopReason != "" {
		t.Errorf("a non-terminal delta carries stop_reason %q; §3.2 puts it on the TERMINAL record only", in.StopReason)
	}
}

// TestR7CodexInteractions_TwoDeltasOfOneMessageShareARefAndNeverAccumulate is the fold
// contract stated as a property rather than as one frame: the adapter is STATELESS, so two
// deltas must carry the same Ref and their OWN text, and the daemon does the folding.
func TestR7CodexInteractions_TwoDeltasOfOneMessageShareARefAndNeverAccumulate(t *testing.T) {
	base := r7RecordedFrame(t, "item/agentMessage/delta")
	second := []byte(strings.Replace(string(base), `"delta": "1"`, `"delta": ". One"`, 1))
	if string(second) == string(base) {
		second = []byte(strings.Replace(string(base), `"delta":"1"`, `"delta":". One"`, 1))
	}

	a := r7Only(t, "item/agentMessage/delta", base)
	b := r7Only(t, "item/agentMessage/delta", second)

	if a.Ref != b.Ref {
		t.Errorf("two deltas of one message carry different refs %q / %q; they would become two "+
			"items and the phone would render one sentence as two bubbles", a.Ref, b.Ref)
	}
	if b.Text != ". One" {
		t.Errorf("the second delta's text = %q, want %q: the ADAPTER IS STATELESS and must not "+
			"accumulate -- accumulation here plus the daemon's own fold would double every token", b.Text, ". One")
	}
}

// ------------------------------------------------------------------- M4.2: turn lifecycle

// TestR7CodexInteractions_TurnCompletedClosesTheTurnEvenWithNoAgentMessage is the R6 blocker
// ADR-013 §R7.8 (1) names in as many words. BOTH the daemon (interaction.go:329-333) and the
// phone close a turn ONLY on a terminal agent_message. An INTERRUPTED Codex turn recorded
// `items: []` with `itemsView: "notLoaded"` (turn-completed-interrupted.json), so there may be
// no agent message to close it with -- and a turn that never closes means expected_turn never
// goes empty and EVERY subsequent phone send is refused stale_turn forever, which is exactly
// the R6 round-2 blocker that broke idle replies 100% of the time.
func TestR7CodexInteractions_TurnCompletedClosesTheTurnEvenWithNoAgentMessage(t *testing.T) {
	out := r7Shape(t, "turn/completed", r7WholeFixture(t, "turn-completed-interrupted.json"))
	if len(out) == 0 {
		t.Fatal("turn/completed on an INTERRUPTED turn with items:[] shaped nothing; the turn " +
			"never closes, expected_turn never empties, and every later phone send is refused " +
			"stale_turn for the life of the session (ADR-013 §R7.8 rule 1)")
	}
	var terminal *adapter.Interaction
	for i := range out {
		if out[i].Kind == adapter.KindAgentMessage && terminalR7(out[i].Status) {
			terminal = &out[i]
		}
	}
	if terminal == nil {
		t.Fatalf("turn/completed shaped %+v with no TERMINAL agent_message; only that closes a turn "+
			"(IS-ENV-1, skeleton/interaction.go's turnIDLocked)", out)
	}
	if terminal.StopReason != "interrupted" {
		t.Errorf("stop_reason = %q, want %q from the RECORDED turn.status", terminal.StopReason, "interrupted")
	}
	if terminal.Text != "" {
		t.Errorf("the synthesized terminal record carries text %q; the RECORDED turn had items:[] "+
			"so there is no text to carry and inventing one would put words in the agent's mouth", terminal.Text)
	}
	if terminal.Ref != "01a0033d-812e-7fe1-82d1-c8703ec5e834" {
		t.Errorf("ref = %q, want the RECORDED turn.id; the terminal record must fold onto the "+
			"turn's own message when there is one and stand alone when there is not", terminal.Ref)
	}
}

// TestR7CodexInteractions_TurnCompletedFoldsOntoTheTurnsOwnAgentMessageWhenItemsNamesIt is
// the other arm, driven by the RECORDED completed (not interrupted) frame, whose
// `itemsView` is "summary" and whose items[0] IS the agent message.
func TestR7CodexInteractions_TurnCompletedFoldsOntoTheTurnsOwnAgentMessageWhenItemsNamesIt(t *testing.T) {
	out := r7Shape(t, "turn/completed", r7RecordedFrame(t, "turn/completed"))
	if len(out) == 0 {
		t.Fatal("the RECORDED completed turn/completed shaped nothing")
	}
	last := out[len(out)-1]
	if last.Kind != adapter.KindAgentMessage || !terminalR7(last.Status) {
		t.Fatalf("last item = %+v, want a TERMINAL agent_message", last)
	}
	if last.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want %q for a RECORDED status of \"completed\"", last.StopReason, "end_turn")
	}
	if last.Ref != "msg_0974dc2bafd212c4016a7fcdd4620c819188a770bcca8faf1e" {
		t.Errorf("ref = %q, want the RECORDED items[0].id -- so the terminal record folds onto the "+
			"very item the 586 deltas built, rather than opening a second bubble", last.Ref)
	}
	if last.Text != "" {
		t.Errorf("the terminal record carries text %q; the deltas already delivered the body and "+
			"IS-DELTA-1 makes text an INCREMENT, so repeating it here doubles the message", last.Text)
	}
}

// TestR7CodexInteractions_TurnStartedShapesNoItem pins the negative. `turn/started` is a
// LIFECYCLE fact the daemon reads for status (M4.5), not content: it carries items:[] and an
// item shaped from it would be an empty bubble at the head of every turn.
func TestR7CodexInteractions_TurnStartedShapesNoItem(t *testing.T) {
	if out := r7Shape(t, "turn/started", r7WholeFixture(t, "turn-started.json")); len(out) != 0 {
		t.Errorf("turn/started shaped %+v; it carries items:[] and belongs to the STATUS path "+
			"(M4.5's ApplyTypedEvent), not the item path", out)
	}
}

// ------------------------------------------------------------------------ M4.2: tool_run

// TestR7CodexInteractions_CommandExecutionOpensAToolRunWithItsArgs drives the
// SCHEMA-DERIVED `item/started` for a commandExecution. M4.2's row: "commandExecution to
// tool_run kind execute with args AND results" -- this is the args half.
func TestR7CodexInteractions_CommandExecutionOpensAToolRunWithItsArgs(t *testing.T) {
	in := r7Only(t, "item/started", r7DerivedFrame(t, "commandExecutionStarted"))

	if in.Kind != adapter.KindToolRun {
		t.Fatalf("kind = %q, want %q", in.Kind, adapter.KindToolRun)
	}
	if in.Status != adapter.StatusInProgress {
		t.Errorf("status = %q, want %q; IS-DELTA-3 opens a tool_run and closes it on a second record",
			in.Status, adapter.StatusInProgress)
	}
	if in.Action.Type != "execute" {
		t.Errorf("action.type = %q, want \"execute\"; M4.2 names the kind explicitly", in.Action.Type)
	}
	if in.Action.Command != "bash -lc 'ls -la ws'" {
		t.Errorf("action.command = %q, want the frame's params.item.command; §7's structured "+
			"summary is what makes a card read as the command it ran", in.Action.Command)
	}
	if in.Ref != "exec-251cb6db-2df1-41d6-936d-4e2a58fac5e7" {
		t.Errorf("ref = %q, want params.item.id, so the close folds onto the open", in.Ref)
	}
	if in.OutputExcerpt != "" || in.ExitCode != 0 {
		t.Errorf("the OPEN record carries results (output %q, exit %d); aggregatedOutput and "+
			"exitCode are null at item/started per the binding", in.OutputExcerpt, in.ExitCode)
	}
}

// TestR7CodexInteractions_CommandExecutionClosesWithItsResults is the results half, and it
// is what settles ADR-013 §R7.9's first open question in the lean direction: item/completed
// carries `aggregatedOutput` and `exitCode`, so R7 needs no outputDelta accumulator at all.
func TestR7CodexInteractions_CommandExecutionClosesWithItsResults(t *testing.T) {
	in := r7Only(t, "item/completed", r7DerivedFrame(t, "commandExecutionCompleted"))

	if in.Kind != adapter.KindToolRun || in.Status != adapter.StatusCompleted {
		t.Fatalf("got kind %q status %q, want tool_run/completed", in.Kind, in.Status)
	}
	if !strings.Contains(in.OutputExcerpt, "README.md") {
		t.Errorf("output_excerpt = %q, want the frame's params.item.aggregatedOutput", in.OutputExcerpt)
	}
	if in.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0 from params.item.exitCode", in.ExitCode)
	}
	if in.Ref != "exec-251cb6db-2df1-41d6-936d-4e2a58fac5e7" {
		t.Errorf("ref = %q, want the OPEN record's ref so the two fold into ONE tool_run", in.Ref)
	}
}

// TestR7CodexInteractions_ANonZeroExitClosesTheToolRunFailed drives the `failed` member of
// the RECORDED CommandExecutionStatus union.
func TestR7CodexInteractions_ANonZeroExitClosesTheToolRunFailed(t *testing.T) {
	in := r7Only(t, "item/completed", r7DerivedFrame(t, "commandExecutionFailed"))
	if in.Status != adapter.StatusFailed {
		t.Errorf("status = %q, want %q for a params.item.status of \"failed\"", in.Status, adapter.StatusFailed)
	}
	if in.ExitCode != 3 {
		t.Errorf("exit_code = %d, want 3", in.ExitCode)
	}
}

// TestR7CodexInteractions_OutputDeltaShapesNothing pins the decision ADR-013 §R7.9 leaves
// open and the schema settles: the delta frames are DROPPED, because item/completed already
// carries the whole aggregated output. Shaping them too would double every byte of tool
// output into the transcript AND spend one append-floor slot per chunk.
func TestR7CodexInteractions_OutputDeltaShapesNothing(t *testing.T) {
	raw := r7RecordedFrame(t, "item/commandExecution/outputDelta")
	if out := r7Shape(t, "item/commandExecution/outputDelta", raw); len(out) != 0 {
		t.Errorf("item/commandExecution/outputDelta shaped %+v; item/completed carries "+
			"aggregatedOutput in full, so shaping the deltas duplicates the output and burns a "+
			"slot per chunk", out)
	}
}

// ------------------------------------------------------------- M4.3: approval_request

// TestR7CodexInteractions_FileChangeApprovalBecomesACardWithTheCLIsOwnDecisions drives the
// RECORDED server-request the gate captured. §3.5 keeps decision ids the CLI's OWN.
func TestR7CodexInteractions_FileChangeApprovalBecomesACardWithTheCLIsOwnDecisions(t *testing.T) {
	in := r7Only(t, "item/fileChange/requestApproval", r7WholeFixture(t, "approval-request.json"))

	if in.Kind != adapter.KindApprovalRequest || in.Status != adapter.StatusInProgress {
		t.Fatalf("got kind %q status %q, want approval_request/in_progress", in.Kind, in.Status)
	}
	if in.Mode != adapter.ModeCard {
		t.Errorf("mode = %q, want %q; Codex answers natively by RPC and MUST NEVER be a prompt_card, "+
			"because prompt_card is the path that TYPES A KEYSTROKE (IS-LIFE-6)", in.Mode, adapter.ModeCard)
	}
	if len(in.Keystrokes) != 0 {
		t.Errorf("the Codex approval carries a keystroke map %v; there is no key to press on this "+
			"provider, ever (playbook §8.2)", in.Keystrokes)
	}
	if in.Ref != "exec-29bcdedd-84f6-423c-931d-0f0433cc3328" {
		t.Errorf("ref = %q, want params.itemId; Decision(ref, id) is called with it and the pending "+
			"server-request is matched by itemId", in.Ref)
	}

	// The RECORDED FileChangeApprovalDecision union, from the generated schema
	// (r7-schema-derived-frames.json decisionVocabulary), with its polarity.
	want := map[string]string{
		"accept":           adapter.VerdictAllow,
		"acceptForSession": adapter.VerdictAllow,
		"decline":          adapter.VerdictDeny,
		"cancel":           adapter.VerdictDeny,
	}
	got := map[string]string{}
	for _, d := range in.Decisions {
		if d.Label == "" {
			t.Errorf("decision %q has no label; the card labels its buttons from decisions[].label (IS-APR-3)", d.ID)
		}
		got[d.ID] = d.Verdict
	}
	for id, verdict := range want {
		v, ok := got[id]
		if !ok {
			t.Errorf("the card omits decision %q, which the generated FileChangeApprovalDecision "+
				"union offers; a card that cannot decline is not a decision", id)
			continue
		}
		if v != verdict {
			t.Errorf("decision %q carries verdict %q, want %q; the verdict is the ONE normalized "+
				"thing about a decision and is what §3.6's allowed/denied split is classified from",
				id, v, verdict)
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("the card offers decision %q, which the 0.147.0 union does not have", id)
		}
	}
}

// TestR7CodexInteractions_CommandExecutionApprovalOffersOnlyTheStringDecisions drives the
// SCHEMA-DERIVED sibling -- the one the adapter has DECLARED since Epic 11 (codex.go:44) and
// that the gate never captured.
//
// Its union has two OBJECT variants (acceptWithExecpolicyAmendment, applyNetworkPolicyAmendment)
// carrying required parameters a user would have to choose. DecisionChoice.ID is a string and
// Decision(ref, decisionID string) is handed nothing else, so those two are NOT OFFERED:
// IS-TOOL-2's posture, a decision the adapter cannot place is declared rather than guessed at.
// Recorded as Q1 in r7-open-questions.md.
func TestR7CodexInteractions_CommandExecutionApprovalOffersOnlyTheStringDecisions(t *testing.T) {
	in := r7Only(t, "item/commandExecution/requestApproval", r7DerivedFrame(t, "commandExecutionRequestApproval"))

	if in.Kind != adapter.KindApprovalRequest || in.Mode != adapter.ModeCard {
		t.Fatalf("got kind %q mode %q, want approval_request/card", in.Kind, in.Mode)
	}
	if !strings.Contains(in.Summary, "rm -rf build") {
		t.Errorf("summary = %q, want the frame's params.command; a card whose headline omits the "+
			"command asks the owner to approve something they cannot see", in.Summary)
	}
	if in.Action.Type != "execute" {
		t.Errorf("action.type = %q, want \"execute\"", in.Action.Type)
	}
	for _, d := range in.Decisions {
		switch d.ID {
		case "accept", "acceptForSession", "decline", "cancel":
		default:
			t.Errorf("the card offers decision %q; the two OBJECT variants of "+
				"CommandExecutionApprovalDecision carry required parameters that cannot ride a "+
				"string id, so they are not offered at all (r7-open-questions.md Q1)", d.ID)
		}
	}
}

// TestR7CodexInteractions_DecisionReturnsTheJSONRPCReplyBodyAndNoKeystroke is ADR-010 §4's
// descriptor rule on the branch that has NO PRODUCTION CALLER ANYWHERE IN THE REPO TODAY --
// a fact worth stating plainly, and what M4.3 changes.
func TestR7CodexInteractions_DecisionReturnsTheJSONRPCReplyBodyAndNoKeystroke(t *testing.T) {
	src := r7Source(t)
	const ref = "exec-29bcdedd-84f6-423c-931d-0f0433cc3328"

	for _, id := range []string{"accept", "acceptForSession", "decline", "cancel"} {
		act, ok := src.Decision(ref, id)
		if !ok {
			t.Fatalf("Decision(%q, %q) answered ok==false; every offered decision must be applicable "+
				"or it should not have been offered", ref, id)
		}
		var body struct {
			Decision string `json:"decision"`
		}
		if err := json.Unmarshal(act.Reply, &body); err != nil {
			t.Fatalf("Decision(%q).Reply is not JSON: %v (%s)", id, err, act.Reply)
		}
		if body.Decision != id {
			t.Errorf("Decision(%q).Reply carries decision %q; the RECORDED reply envelope is "+
				"{\"decision\": <id>} (r1-codex-gate.md:128) and the id is the CLI's own", id, body.Decision)
		}
	}
	if _, ok := src.Decision(ref, "yes"); ok {
		t.Error("Decision accepted \"yes\", which is not in the 0.147.0 union; a reply the server " +
			"rejects leaves the approval pending with the phone believing it answered")
	}
}

// --------------------------------------------------------------------------- purity

// TestR7CodexInteractions_IsPureAndTotalOnEveryPathologicalBody is the same totality bar
// ExtractConversationID already meets: never panics on a nil, truncated, garbage or unbounded
// body; deterministic; returns no content it did not read out of p.Raw.
func TestR7CodexInteractions_IsPureAndTotalOnEveryPathologicalBody(t *testing.T) {
	src := r7Source(t)
	bodies := [][]byte{
		nil,
		{},
		[]byte("{"),
		[]byte("null"),
		[]byte("[]"),
		[]byte(`{"method":"item/agentMessage/delta"}`),
		[]byte(`{"method":"item/agentMessage/delta","params":null}`),
		[]byte(`{"method":"item/agentMessage/delta","params":{"itemId":null,"delta":null}}`),
		[]byte(`{"params":{"item":{"type":"commandExecution"}}}`),
		[]byte(`{"params":{"turn":{"status":"nonsense"}}}`),
		[]byte(strings.Repeat("a", 1<<20)),
		[]byte(`{"params":{"item":{"type":"userMessage","content":` + strings.Repeat("[", 512) + `}}}`),
	}
	events := []string{
		"", "item/started", "item/completed", "item/agentMessage/delta", "turn/completed",
		"turn/started", "item/fileChange/requestApproval", "serverRequest/resolved",
		"app/list/updated", "\x00garbage",
	}
	for _, ev := range events {
		for _, b := range bodies {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("Interactions panicked on event %q body %.40q: %v", ev, b, r)
					}
				}()
				first := src.Interactions(adapter.HookPayload{Event: ev, Raw: b})
				second := src.Interactions(adapter.HookPayload{Event: ev, Raw: b})
				if len(first) != len(second) {
					t.Errorf("Interactions is not deterministic for event %q body %.40q", ev, b)
				}
				for _, in := range first {
					if err := in.Validate(); err != nil {
						t.Errorf("event %q body %.40q shaped an invalid item: %v", ev, b, err)
					}
				}
			}()
		}
	}
}

// TestR7CodexInteractions_AnUnrecognizedMethodShapesZeroItems is ADR-013 §R7.6's upgrade
// safety property: a frame shape this adapter revision does not know shapes NOTHING rather
// than a guess, so a CLI upgrade degrades to the grid heuristic instead of inventing content.
func TestR7CodexInteractions_AnUnrecognizedMethodShapesZeroItems(t *testing.T) {
	raw := r7RecordedFrame(t, "item/agentMessage/delta")
	for _, ev := range []string{"item/reasoning/textDelta", "item/plan/delta", "thread/compacted", "future/method"} {
		if out := r7Shape(t, ev, raw); len(out) != 0 {
			t.Errorf("unrecognized method %q shaped %+v; an unrecognized method must shape ZERO "+
				"interactions (ADR-013 §R7.6)", ev, out)
		}
	}
}

// terminalR7 mirrors the schema's §4 terminal set for this test file.
func terminalR7(s string) bool {
	return s == adapter.StatusCompleted || s == adapter.StatusFailed || s == adapter.StatusDeclined
}
