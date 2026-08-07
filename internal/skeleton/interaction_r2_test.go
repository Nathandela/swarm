package skeleton

// FAILING-FIRST (TDD RED, GG-5) for review finding R2: interaction-schema.md §5's per-field
// caps and IS-CAP-1's truncator do not exist, so an over-cap item is DROPPED at the append
// boundary instead of being clipped and shipped.
//
// EVERY NUMBER BELOW IS SPELLED AS THE SPEC'S OWN LITERAL, not as a production constant. These
// tests pin §5's TABLE, so they must keep failing if the table is honoured by a constant with
// the wrong value -- and it makes the RED behavioural (a field arrives uncapped, or the item
// never arrives at all) rather than a compile error against a symbol that does not exist yet.
//
// The path exercised is the SHIPPED one end to end: captureInteractions -> Interaction.Validate
// -> shapeItem -> ItemAdmission.Offer -> daemon.RecordInteractionRaw -> the journal. Reading the
// journal is deliberate: it is what the gateway forwards, so anything invisible there is
// invisible to the phone.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/journal"
)

// interaction-schema.md §5, verbatim from the table. Local to the test on purpose (see above).
const (
	specMaxTextBytes       = 4 << 10 // `text`, `output_excerpt`, `diff_excerpt`
	specMaxSummaryBytes    = 256     // `summary`, each `action` string field, each decisions[].label
	specMaxPromptLines     = 40      // `prompt_lines`, lines
	specMaxPromptLineRunes = 200     // `prompt_lines`, runes per line
	specMaxSteps           = 64      // `plan_update.steps`
	specMaxStepBytes       = 200     // bytes per step
	specMaxDecisions       = 8       // `decisions`
)

// ---- helpers ---------------------------------------------------------------

// interactionPayloads reads the journalled interaction records for session as RAW BYTES, which
// is what §5's MaxItemBytes is measured on.
func interactionPayloads(t *testing.T, sk *Daemon, session string) []json.RawMessage {
	t.Helper()
	res, err := sk.Core().JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}
	var out []json.RawMessage
	for _, rec := range res.Events {
		if rec.Type == journal.TypeInteraction && rec.SessionID == session {
			out = append(out, json.RawMessage(rec.Payload))
		}
	}
	return out
}

// captureOne shapes ONE interaction through the shipped producer and returns the item the
// journal holds, plus its bytes. A DROPPED item fails here in the finding's own terms.
func captureOne(t *testing.T, sk *Daemon, session string, in adapter.Interaction) (map[string]any, json.RawMessage) {
	t.Helper()
	sk.captureInteractions(session, newCaptureAdapter(in), adapter.HookPayload{Event: "PostToolUse"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw := interactionPayloads(t, sk, session); len(raw) > 0 {
			var item map[string]any
			if err := json.Unmarshal(raw[0], &item); err != nil {
				t.Fatalf("journalled interaction payload is not a JSON object: %v (%s)", err, raw[0])
			}
			return item, raw[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the producer journalled NO interaction record for session %q. An item that exceeds "+
		"an interaction-schema.md §5 cap is TRUNCATED at a rune boundary with `truncated` and "+
		"`full_bytes` set (IS-CAP-1) and its full body left fetchable (IS-CAP-2) -- it is never "+
		"dropped at the append boundary", session)
	return nil, nil
}

// itemStrings pulls a []string field (prompt_lines).
func itemStrings(t *testing.T, item map[string]any, key string) []string {
	t.Helper()
	raw, ok := item[key].([]any)
	if !ok {
		t.Fatalf("item has no array %q: %v", key, item)
	}
	out := make([]string, len(raw))
	for i, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("%s[%d] is not a string: %v", key, i, v)
		}
		out[i] = s
	}
	return out
}

// itemObjects pulls an array-of-objects field (decisions, steps).
func itemObjects(t *testing.T, item map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := item[key].([]any)
	if !ok {
		t.Fatalf("item has no array %q: %v", key, item)
	}
	out := make([]map[string]any, len(raw))
	for i, v := range raw {
		o, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%s[%d] is not an object: %v", key, i, v)
		}
		out[i] = o
	}
	return out
}

// assertTruncationPair is IS-CAP-1's other half: clipping SETS `truncated`, and `full_bytes`
// carries the untruncated size (§2), which must exceed what shipped or it says nothing.
func assertTruncationPair(t *testing.T, item map[string]any, payload json.RawMessage) {
	t.Helper()
	if tr, _ := item["truncated"].(bool); !tr {
		t.Errorf("a clipped item carries truncated = %v; IS-CAP-1 says truncation SHALL set it, and "+
			"without it a consumer renders a cut body as the whole message (IS-DELTA-4)", item["truncated"])
	}
	full, _ := item["full_bytes"].(float64)
	if int(full) <= len(payload) {
		t.Errorf("full_bytes = %v with a %d-byte payload; §2 carries the byte length of the "+
			"UNTRUNCATED payload, so it must exceed what shipped", item["full_bytes"], len(payload))
	}
}

// assertFitsItemCap is the whole point of the fix: the producer clips FIELD-WISE until the item
// fits, so the append boundary only ever refuses a genuinely malformed item.
func assertFitsItemCap(t *testing.T, payload json.RawMessage) {
	t.Helper()
	if len(payload) > daemon.MaxItemBytes {
		t.Errorf("the journalled item is %d bytes, over §5's %d-byte MaxItemBytes; the producer must "+
			"clip its fields to fit BEFORE the append boundary", len(payload), daemon.MaxItemBytes)
	}
}

// ---- §5: `text`, `output_excerpt`, `diff_excerpt` at MaxTextBytes ----------

func TestInteractionR2_AgentMessageTextIsClippedAtMaxTextBytes(t *testing.T) {
	sk := assemble(t)
	item, payload := captureOne(t, sk, "s-r2-text", adapter.Interaction{
		Kind: adapter.KindAgentMessage, Status: adapter.StatusInProgress, Ref: "msg-1",
		Text: strings.Repeat("a", 3*specMaxTextBytes),
	})
	// EXACTLY the cap, not merely under it. An upper bound alone passes for an implementation
	// that clips far harder than §5 asks -- and the whole cost of this fix is measured in the
	// transcript text it does not ship. Nothing else here forces a further cut: the capped item
	// is ~4.3 KiB, well under MaxItemBytes.
	if got := len(itemString(t, item, "text")); got != specMaxTextBytes {
		t.Errorf("journalled text is %d bytes; §5 caps `text` at MaxTextBytes = %d, and an ASCII body "+
			"three times that long clips to exactly the cap", got, specMaxTextBytes)
	}
	assertTruncationPair(t, item, payload)
	assertFitsItemCap(t, payload)
}

func TestInteractionR2_ToolRunOutputExcerptIsClippedAtMaxTextBytes(t *testing.T) {
	sk := assemble(t)
	item, payload := captureOne(t, sk, "s-r2-out", adapter.Interaction{
		Kind: adapter.KindToolRun, Status: adapter.StatusCompleted, Ref: "call-1",
		Tool: "Bash", Action: adapter.ToolAction{Type: "execute", Command: "ls"},
		OutputExcerpt: strings.Repeat("o", 3*specMaxTextBytes),
	})
	if got := len(itemString(t, item, "output_excerpt")); got != specMaxTextBytes {
		t.Errorf("journalled output_excerpt is %d bytes; §5 caps it at MaxTextBytes = %d, exactly",
			got, specMaxTextBytes)
	}
	assertTruncationPair(t, item, payload)
	assertFitsItemCap(t, payload)
}

func TestInteractionR2_FileChangeDiffExcerptIsClippedAtMaxTextBytes(t *testing.T) {
	sk := assemble(t)
	item, payload := captureOne(t, sk, "s-r2-diff", adapter.Interaction{
		Kind: adapter.KindFileChange, Status: adapter.StatusCompleted, Ref: "fc-1",
		Path: "src/main.rs", Change: "modify", Added: 3, Removed: 1,
		DiffExcerpt: strings.Repeat("+line\n", specMaxTextBytes),
	})
	if got := len(itemString(t, item, "diff_excerpt")); got != specMaxTextBytes {
		t.Errorf("journalled diff_excerpt is %d bytes; §5 caps it at MaxTextBytes = %d, exactly",
			got, specMaxTextBytes)
	}
	assertTruncationPair(t, item, payload)
	assertFitsItemCap(t, payload)
}

// ---- §5: `summary`, action fields, decision labels at MaxSummaryBytes ------

func TestInteractionR2_SummaryActionAndDecisionLabelsAreClippedAtMaxSummaryBytes(t *testing.T) {
	sk := assemble(t)
	decisions := make([]adapter.DecisionChoice, 0, 3*specMaxDecisions)
	for i := 0; i < 3*specMaxDecisions; i++ {
		decisions = append(decisions, adapter.DecisionChoice{
			ID:    string(rune('a'+i%26)) + "-decision",
			Label: strings.Repeat("L", 4*specMaxSummaryBytes),
		})
	}
	item, payload := captureOne(t, sk, "s-r2-sum", adapter.Interaction{
		Kind: adapter.KindApprovalRequest, Status: adapter.StatusInProgress, Ref: "req-1",
		Mode:      adapter.ModeCard,
		Summary:   strings.Repeat("S", 4*specMaxSummaryBytes),
		Action:    adapter.ToolAction{Type: "execute", Command: strings.Repeat("C", 4*specMaxSummaryBytes)},
		Decisions: decisions,
	})

	// EXACT again: the capped item is ~2.8 KiB, so nothing downstream cuts further and each
	// field must sit precisely on §5's number.
	if got := len(itemString(t, item, "summary")); got != specMaxSummaryBytes {
		t.Errorf("journalled summary is %d bytes; §5 caps it at MaxSummaryBytes = %d, exactly",
			got, specMaxSummaryBytes)
	}
	action, ok := item["action"].(map[string]any)
	if !ok {
		t.Fatalf("item has no action object: %v", item)
	}
	if cmd, _ := action["command"].(string); len(cmd) != specMaxSummaryBytes {
		t.Errorf("journalled action.command is %d bytes; §5 caps EACH action string field at "+
			"MaxSummaryBytes = %d, exactly", len(cmd), specMaxSummaryBytes)
	}
	if typ, _ := action["type"].(string); typ != "execute" {
		t.Errorf("journalled action.type = %q; want %q -- §7's action type is a CLOSED vocabulary "+
			"and a cap must not cut it into a value no client can switch on", typ, "execute")
	}
	got := itemObjects(t, item, "decisions")
	if len(got) != specMaxDecisions {
		t.Errorf("journalled decisions holds %d entries; §5 caps MaxDecisions at %d, and the request "+
			"offered %d", len(got), specMaxDecisions, 3*specMaxDecisions)
	}
	for i, d := range got {
		if s, _ := d["label"].(string); len(s) != specMaxSummaryBytes {
			t.Errorf("journalled decisions[%d].label is %d bytes; §5 caps each label at "+
				"MaxSummaryBytes = %d, exactly", i, len(s), specMaxSummaryBytes)
		}
	}
	assertTruncationPair(t, item, payload)
	assertFitsItemCap(t, payload)
}

// ---- §5: `prompt_lines` at 40 lines x 200 RUNES ----------------------------

// promptCard builds an approval_request carrying lines, with everything else small enough that
// prompt_lines is the only thing any cap can bite.
func promptCard(ref string, lines []string) adapter.Interaction {
	return adapter.Interaction{
		Kind: adapter.KindApprovalRequest, Status: adapter.StatusInProgress, Ref: ref,
		Mode:        adapter.ModePromptCard,
		Summary:     "run a shell command",
		Decisions:   []adapter.DecisionChoice{{ID: "accept", Label: "Accept"}, {ID: "cancel", Label: "Cancel"}},
		PromptLines: lines,
	}
}

func TestInteractionR2_PromptLinesAreCappedInLineCount(t *testing.T) {
	sk := assemble(t)
	lines := make([]string, 0, 4*specMaxPromptLines)
	for i := 0; i < 4*specMaxPromptLines; i++ {
		lines = append(lines, "rm -rf /tmp/x")
	}
	item, payload := captureOne(t, sk, "s-r2-lines", promptCard("req-lines", lines))

	got := itemStrings(t, item, "prompt_lines")
	if len(got) != specMaxPromptLines {
		t.Errorf("journalled prompt_lines holds %d lines for a %d-line prompt; §5 caps "+
			"MaxPromptLines at %d lines, exactly", len(got), len(lines), specMaxPromptLines)
	}
	for i, l := range got {
		if l != "rm -rf /tmp/x" {
			t.Errorf("prompt_lines[%d] = %q; a LINE-COUNT cap must not also cut the lines it keeps", i, l)
		}
	}
	assertTruncationPair(t, item, payload)
	assertFitsItemCap(t, payload)
}

// TestInteractionR2_PromptLinesAreCappedInRunesNotBytes is the rune/byte distinction on its own.
// §5 caps a prompt line at 200 RUNES; a byte cap cuts this two-byte-per-rune line at 100 runes
// and still passes every "no more than 200" assertion, while halving the prompt the owner is
// being asked to approve. Only ten lines, so the whole item stays ~4.2 KiB and nothing
// downstream cuts further -- the count here is exact by construction.
func TestInteractionR2_PromptLinesAreCappedInRunesNotBytes(t *testing.T) {
	sk := assemble(t)
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = strings.Repeat("é", 2*specMaxPromptLineRunes)
	}
	item, payload := captureOne(t, sk, "s-r2-prompt", promptCard("req-runes", lines))

	got := itemStrings(t, item, "prompt_lines")
	if len(got) != len(lines) {
		t.Fatalf("journalled prompt_lines holds %d lines; want %d -- no §5 count cap binds here",
			len(got), len(lines))
	}
	for i, l := range got {
		if n := utf8.RuneCountInString(l); n != specMaxPromptLineRunes {
			t.Errorf("journalled prompt_lines[%d] holds %d runes (%d bytes); §5 caps a line at %d "+
				"RUNES, exactly -- %d runes is a BYTE cap wearing the right number",
				i, n, len(l), specMaxPromptLineRunes, specMaxPromptLineRunes/2)
		}
		if !utf8.ValidString(l) {
			t.Errorf("journalled prompt_lines[%d] is not valid UTF-8; IS-CAP-1 truncates at a rune "+
				"boundary, never mid-rune", i)
		}
	}
	assertTruncationPair(t, item, payload)
	assertFitsItemCap(t, payload)
}

// ---- §5: `plan_update.steps` at 64 steps x 200 B ---------------------------

func TestInteractionR2_PlanStepsAreCappedInCountAndBytes(t *testing.T) {
	sk := assemble(t)
	steps := make([]adapter.PlanStep, 0, 3*specMaxSteps)
	for i := 0; i < 3*specMaxSteps; i++ {
		steps = append(steps, adapter.PlanStep{Text: strings.Repeat("t", 4*specMaxStepBytes), State: "pending"})
	}
	item, payload := captureOne(t, sk, "s-r2-plan", adapter.Interaction{
		Kind: adapter.KindPlanUpdate, Revision: 3, Steps: steps,
	})

	// The COUNT is exact; the per-step TEXT is an upper bound on purpose. §5's own step maxima
	// are jointly insufficient the same way §3.5's are -- 64 steps x 200 B is 12.8 KiB of text
	// alone, half again over the 8 KiB MaxItemBytes -- so the fit stage legitimately cuts these
	// steps below 200 B. What must not happen is a step being emptied or a state being cut.
	got := itemObjects(t, item, "steps")
	if len(got) != specMaxSteps {
		t.Errorf("journalled steps holds %d entries for a %d-step plan; §5 caps MaxSteps at %d, "+
			"exactly", len(got), 3*specMaxSteps, specMaxSteps)
	}
	for i, s := range got {
		txt, _ := s["text"].(string)
		if len(txt) > specMaxStepBytes {
			t.Errorf("journalled steps[%d].text is %d bytes; §5 caps a step at %d B", i, len(txt), specMaxStepBytes)
		}
		if txt == "" {
			t.Errorf("journalled steps[%d].text is empty; a plan of blank steps renders nothing (§3.7)", i)
		}
		if st, _ := s["state"].(string); st != "pending" {
			t.Errorf("journalled steps[%d].state = %q; a step's state is a CLOSED vocabulary (§3.7) "+
				"and must survive truncation intact", i, st)
		}
	}
	assertTruncationPair(t, item, payload)
	assertFitsItemCap(t, payload)
}

// ---- the finding's headline case -------------------------------------------

// TestInteractionR2_AnApprovalRequestAtTheDocumentedMaximaIsTruncatedNotDropped.
//
// §5's per-field caps are NOT jointly sufficient: an approval_request sitting exactly on them --
// 40 prompt lines x 200 runes, a 256 B summary, 256 B action strings, 8 decisions with 256 B
// labels -- serializes well past the 8 KiB MaxItemBytes. IS-CAP-1 makes that item TRUNCATED;
// today it is refused at the append boundary and the owner is never asked, which is the worst
// possible failure for the one kind that blocks the agent.
//
// The card must survive as a CARD: IS-APR-3 labels its buttons from decisions[].label, so a fit
// that empties the labels or drops the choices is not a fit.
func TestInteractionR2_AnApprovalRequestAtTheDocumentedMaximaIsTruncatedNotDropped(t *testing.T) {
	sk := assemble(t)
	lines := make([]string, specMaxPromptLines)
	for i := range lines {
		lines[i] = strings.Repeat("p", specMaxPromptLineRunes)
	}
	decisions := make([]adapter.DecisionChoice, specMaxDecisions)
	for i := range decisions {
		decisions[i] = adapter.DecisionChoice{
			ID:    string(rune('a'+i)) + "-decision",
			Label: strings.Repeat("L", specMaxSummaryBytes),
		}
	}
	item, payload := captureOne(t, sk, "s-r2-max", adapter.Interaction{
		Kind: adapter.KindApprovalRequest, Status: adapter.StatusInProgress, Ref: "req-max",
		Mode:    adapter.ModePromptCard,
		Summary: strings.Repeat("S", specMaxSummaryBytes),
		Action: adapter.ToolAction{
			Type:    "execute",
			Path:    strings.Repeat("P", specMaxSummaryBytes),
			Query:   strings.Repeat("Q", specMaxSummaryBytes),
			Command: strings.Repeat("C", specMaxSummaryBytes),
		},
		Decisions:   decisions,
		PromptLines: lines,
	})

	assertFitsItemCap(t, payload)
	assertTruncationPair(t, item, payload)

	if k := itemString(t, item, "kind"); k != adapter.KindApprovalRequest {
		t.Fatalf("journalled kind = %q; want %q", k, adapter.KindApprovalRequest)
	}
	if s := itemString(t, item, "summary"); s == "" {
		t.Error("the fit emptied `summary`; §3.5 makes it the card headline, and a headline-less " +
			"approval card is not a card")
	}
	if m := itemString(t, item, "mode"); m != adapter.ModePromptCard {
		t.Errorf("journalled mode = %q; want %q -- `mode` is a CLOSED vocabulary (§3.5) and must "+
			"survive truncation intact, or the phone cannot tell which apply path this is", m, adapter.ModePromptCard)
	}
	got := itemObjects(t, item, "decisions")
	if len(got) != specMaxDecisions {
		t.Fatalf("journalled decisions holds %d entries; the request offered %d, exactly §5's cap -- "+
			"a fit that drops a choice removes an answer the owner can give", len(got), specMaxDecisions)
	}
	for i, d := range got {
		if id, _ := d["id"].(string); id == "" {
			t.Errorf("decisions[%d] lost its id; the card resolves a decision BY id (§3.5)", i)
		}
		if lbl, _ := d["label"].(string); lbl == "" {
			t.Errorf("decisions[%d] lost its label; IS-APR-3 labels the card's buttons from it, so an "+
				"empty label renders an unactionable button", i)
		}
	}
	if len(itemStrings(t, item, "prompt_lines")) == 0 {
		t.Error("the fit emptied `prompt_lines`; a prompt_card with no prompt shows the owner nothing " +
			"to decide on (§3.5, IS-APR-3)")
	}
}

// ---- IS-CAP-1: the rune boundary --------------------------------------------

// TestInteractionR2_ByteCapTruncationLandsOnARuneBoundary puts a two-byte rune ASTRIDE the
// MaxTextBytes boundary. A naive s[:4096] splits it and ships invalid UTF-8, which IS-CAP-1
// forbids in as many words ("never mid-rune").
func TestInteractionR2_ByteCapTruncationLandsOnARuneBoundary(t *testing.T) {
	sk := assemble(t)
	text := strings.Repeat("a", specMaxTextBytes-1) + "é" + strings.Repeat("b", 128)
	item, payload := captureOne(t, sk, "s-r2-rune", adapter.Interaction{
		Kind: adapter.KindAgentMessage, Status: adapter.StatusInProgress, Ref: "msg-rune",
		Text: text,
	})

	got := itemString(t, item, "text")
	// The two-byte rune occupies bytes 4095-4096, so the only cut that is both under the cap and
	// on a boundary is 4095: the truncator backs up OFF the rune rather than through it.
	if len(got) != specMaxTextBytes-1 {
		t.Fatalf("journalled text is %d bytes; want %d -- §5 caps `text` at MaxTextBytes = %d and "+
			"IS-CAP-1 backs the cut up off the two-byte rune straddling it", len(got), specMaxTextBytes-1, specMaxTextBytes)
	}
	if !utf8.ValidString(got) {
		t.Errorf("journalled text is not valid UTF-8; IS-CAP-1: truncation SHALL be at a UTF-8 rune "+
			"boundary, never mid-rune (got %d bytes ending %q)", len(got), got[max(0, len(got)-4):])
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Error("journalled text holds U+FFFD; a rune was split at the cap and the JSON encoder " +
			"substituted a replacement character (IS-CAP-1)")
	}
	assertTruncationPair(t, item, payload)
	assertFitsItemCap(t, payload)
}

// ---- the control -----------------------------------------------------------

// TestInteractionR2_AnItemUnderEveryCapIsShippedVerbatim is the other half of the contract: the
// caps must not fire on a normal item, and `truncated`/`full_bytes` must stay ABSENT -- §2
// carries full_bytes "only with truncated", and a transcript that claimed every message was
// clipped would render an elision on every card (IS-DELTA-4).
func TestInteractionR2_AnItemUnderEveryCapIsShippedVerbatim(t *testing.T) {
	sk := assemble(t)
	const text = "the quick brown fox — jumps over the lazy dog"
	item, payload := captureOne(t, sk, "s-r2-small", adapter.Interaction{
		Kind: adapter.KindAgentMessage, Status: adapter.StatusCompleted, Ref: "msg-small",
		Text: text, StopReason: "end_turn",
	})
	if got := itemString(t, item, "text"); got != text {
		t.Errorf("journalled text = %q; want the adapter's own text verbatim -- no cap binds here", got)
	}
	if _, ok := item["truncated"]; ok {
		t.Errorf("an uncut item carries truncated = %v; §2 sets it only when a field WAS clipped", item["truncated"])
	}
	if _, ok := item["full_bytes"]; ok {
		t.Errorf("an uncut item carries full_bytes = %v; §2 carries it only with `truncated`", item["full_bytes"])
	}
	assertFitsItemCap(t, payload)
}
