package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the PRODUCER GLUE: the one path that turns an adapter's
// shaped interactions into journal `interaction` records (ADR-009, ADR-010,
// docs/specifications/interaction-schema.md, all Accepted 2026-08-07).
//
// FOUR WORKPACKAGES LANDED FOUR HALVES AND NO SPINE. internal/adapter shapes an Interaction
// and can say whether an adapter shapes any at all; internal/daemon can append one item;
// internal/remotegw can space a stream of them under ADR-010 §7's floor; internal/phonecore
// can fold them. NOTHING CALLED ANY OF IT -- which is exactly what internal/verify's B94
// reachability fence reported, naming adapter.AsInteractionSource and Interaction.Validate.
// This file is the fence's positive form: the glue, tested, at the seam where the three
// packages meet.
//
// WHY THE GLUE IS HERE AND NOT IN internal/daemon. internal/remotegw ALREADY DEPENDS ON
// internal/daemon (remotegw -> protocol -> daemon), so a daemon that imported the append
// floor would be an import cycle -- W3's handoff note ("it must be constructed daemon-side")
// is not implementable as written. internal/skeleton is the assembly layer that imports all
// three, and skeleton.Daemon IS the assembled daemon, so the floor is constructed here and
// the capture runs here.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/refadapter"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/journal"
)

// ---- doubles ---------------------------------------------------------------

// captureAdapter is an adapter that DOES implement ADR-010's optional capture extension.
// Everything else about it is the reference adapter, so the embedded contract is real.
type captureAdapter struct {
	adapter.Adapter
	items []adapter.Interaction
	// script, when set, hands out a DIFFERENT batch per captured event, so successive hook
	// posts shape a stream rather than one repeated record.
	script *interactionScript
}

func newCaptureAdapter(items ...adapter.Interaction) *captureAdapter {
	return &captureAdapter{Adapter: refadapter.New(adapter.Fixture{}), items: items}
}

func (c *captureAdapter) Interactions(adapter.HookPayload) []adapter.Interaction {
	if c.script != nil {
		return c.script.take()
	}
	return c.items
}

func (c *captureAdapter) Decision(string, string) (adapter.DecisionAction, bool) {
	return adapter.DecisionAction{}, false
}

// plainAdapter implements NOTHING of the extension: ADR-010 §5's generic-fallback signal.
type plainAdapter struct{ adapter.Adapter }

func newPlainAdapter() *plainAdapter {
	return &plainAdapter{Adapter: refadapter.New(adapter.Fixture{})}
}

// ---- helpers ---------------------------------------------------------------

// interactionItems reads every `interaction` record the daemon journalled for session, as
// decoded item objects in cursor order. It reads the JOURNAL rather than any in-memory
// accumulator: the journal is what the gateway forwards, so anything invisible here is
// invisible to the phone.
func interactionItems(t *testing.T, sk *Daemon, session string) []map[string]any {
	t.Helper()
	res, err := sk.Core().JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}
	var out []map[string]any
	for _, rec := range res.Events {
		if rec.Type != journal.TypeInteraction || rec.SessionID != session {
			continue
		}
		var item map[string]any
		if err := json.Unmarshal(rec.Payload, &item); err != nil {
			t.Fatalf("journalled interaction payload is not a JSON object: %v (%s)", err, rec.Payload)
		}
		out = append(out, item)
	}
	return out
}

// awaitItems polls until the journal holds want interaction records for session. The wait is
// real rather than incidental: ADR-010 §7 admits at most ONE item per DefaultAppendWindow
// machine-wide, so a second item offered in the same window is released by the floor's own
// driver a window later.
func awaitItems(t *testing.T, sk *Daemon, session string, want int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var got []map[string]any
	for time.Now().Before(deadline) {
		got = interactionItems(t, sk, session)
		if len(got) >= want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the journal holds %d interaction record(s) for %s after 10s; want %d", len(got), session, want)
	return nil
}

func itemString(t *testing.T, item map[string]any, key string) string {
	t.Helper()
	v, ok := item[key].(string)
	if !ok {
		t.Fatalf("item has no string %q: %v", key, item)
	}
	return v
}

// ---- ADR-010 §5: the extension is OPTIONAL ---------------------------------

// TestInteractionCapture_AnAdapterWithoutTheExtensionEmitsNothing pins the decision point
// AsInteractionSource exists to be: ok == false is the GENERIC-FALLBACK SIGNAL, and the
// adapter is complete and fully supported. The failure this forbids is the daemon treating a
// non-capturing adapter as a defect -- every shipped adapter is one today.
func TestInteractionCapture_AnAdapterWithoutTheExtensionEmitsNothing(t *testing.T) {
	sk := assemble(t)
	if n := sk.captureInteractions("s-plain", newPlainAdapter(), adapter.HookPayload{Event: "Stop"}); n != 0 {
		t.Fatalf("captureInteractions offered %d item(s) for an adapter that implements no capture extension; "+
			"want 0 -- ADR-010 §5 makes native capture an upgrade, never a precondition", n)
	}
	if got := interactionItems(t, sk, "s-plain"); len(got) != 0 {
		t.Fatalf("the journal grew %d interaction record(s) from an adapter that shapes none: %v", len(got), got)
	}
}

// TestInteractionCapture_AnUnshapeableInteractionEmitsNothing is IS-ENV-3's all-or-nothing,
// applied where the schema puts it: BEFORE the append. A consumer's only recourse for a
// partial item is to skip it, and a skipped record has still burned a cursor -- so the
// producer must emit nothing at all. The valid sibling in the same batch must still land, or
// one bad shape would silence a whole event.
func TestInteractionCapture_AnUnshapeableInteractionEmitsNothing(t *testing.T) {
	sk := assemble(t)
	ad := newCaptureAdapter(
		adapter.Interaction{Kind: "no_such_kind", Text: "junk"},
		adapter.Interaction{Kind: adapter.KindUserMessage, Source: adapter.SourceOwner, Text: "real"},
	)
	if n := sk.captureInteractions("s-bad", ad, adapter.HookPayload{Event: "UserPromptSubmit"}); n != 1 {
		t.Fatalf("captureInteractions offered %d item(s); want 1 -- the unshapeable one is dropped "+
			"(Interaction.Validate, IS-ENV-3) and its valid sibling still lands", n)
	}
	got := awaitItems(t, sk, "s-bad", 1)
	if len(got) != 1 {
		t.Fatalf("the journal holds %d interaction record(s); want exactly 1", len(got))
	}
	if k := itemString(t, got[0], "kind"); k != adapter.KindUserMessage {
		t.Errorf("journalled kind = %q; want %q -- the invalid item was journalled instead of dropped", k, adapter.KindUserMessage)
	}
}

// ---- §2: the envelope the daemon owns --------------------------------------

// TestInteractionCapture_ShapesTheEnvelopeAndTheKindFieldsOntoTheJournal is the shape
// assertion. ADR-010 §3 makes the daemon the SOLE producer of what goes on the wire: the
// adapter supplied `summary`/`mode`/`decisions` and NOT `v`, `item_id` or `ts`, and the item
// on the journal must carry all six -- the §3 kind fields FLAT beside the §2 envelope, which
// is what "the fields below are additional to the envelope" means.
func TestInteractionCapture_ShapesTheEnvelopeAndTheKindFieldsOntoTheJournal(t *testing.T) {
	sk := assemble(t)
	ad := newCaptureAdapter(adapter.Interaction{
		Kind:      adapter.KindApprovalRequest,
		Status:    adapter.StatusInProgress,
		Ref:       "cli-req-7",
		Summary:   "write src/main.rs",
		Mode:      adapter.ModeCard,
		Decisions: []adapter.DecisionChoice{{ID: "allow", Label: "Allow"}},
		Action:    adapter.ToolAction{Type: "write", Path: "src/main.rs"},
	})
	if n := sk.captureInteractions("s-shape", ad, adapter.HookPayload{Event: "PreToolUse"}); n != 1 {
		t.Fatalf("captureInteractions offered %d item(s); want 1", n)
	}
	item := awaitItems(t, sk, "s-shape", 1)[0]

	if v, _ := item["v"].(float64); int(v) != 1 {
		t.Errorf("item v = %v; want 1 -- the item's own schema version, minted by the daemon (§2)", item["v"])
	}
	if id := itemString(t, item, "item_id"); id == "" {
		t.Error("item_id is empty; the daemon mints it and it is the ONLY id on the wire (IS-APR-1)")
	}
	if ts := itemString(t, item, "ts"); ts == "" {
		t.Error("ts is empty; the wire record carries no timestamp, so a consumer would have to " +
			"substitute arrival time -- the PB-APP-11 mistake")
	}
	if k := itemString(t, item, "kind"); k != adapter.KindApprovalRequest {
		t.Errorf("kind = %q; want %q", k, adapter.KindApprovalRequest)
	}
	if s := itemString(t, item, "status"); s != adapter.StatusInProgress {
		t.Errorf("status = %q; want %q", s, adapter.StatusInProgress)
	}
	if s := itemString(t, item, "summary"); s != "write src/main.rs" {
		t.Errorf("summary = %q; want the adapter's own text -- §3.5's fields ride FLAT beside the envelope", s)
	}
	if m := itemString(t, item, "mode"); m != adapter.ModeCard {
		t.Errorf("mode = %q; want %q", m, adapter.ModeCard)
	}
	if _, ok := item["decisions"]; !ok {
		t.Error("decisions is absent; the card labels its buttons from decisions[].label (IS-APR-3)")
	}

	// IS-APR-3 / IS-LIFE-6: the decision->keystroke map is MACHINE-SIDE and must never reach
	// the item. Asserted here because this is the only seam that could copy it across.
	if _, leaked := item["keystrokes"]; leaked {
		t.Error("the item carries `keystrokes`. The decision->keystroke map is held machine-side and " +
			"SHALL NOT be copied onto the item (IS-APR-3), because a phone that could author the " +
			"keystroke is the blind injection ADR-007 D7 forbids")
	}
	// The adapter's own CLI id is machine-side too: IS-APR-1 leaves exactly one id on the wire.
	for _, v := range item {
		if s, ok := v.(string); ok && s == "cli-req-7" {
			t.Errorf("the item carries the CLI's own ref %q; the daemon maps it to the minted item_id "+
				"and IS-APR-1 leaves exactly one id on the wire", s)
		}
	}
}

// ---- IS-DELTA-1/-3: the fold key -------------------------------------------

// TestInteractionCapture_SuccessiveRecordsOfOneRefShareOneItemID. IS-ENV-2 folds by item_id
// and never by position, so successive records of ONE interaction -- an agent_message's
// increments, a tool_run's open and close -- must carry the SAME item_id. The adapter is the
// only party that sees the CLI's own id (Interaction.Ref), so the daemon's ref->item_id map
// is the only place that correlation can be made. Without it every increment is a new item
// and a streamed message renders as a column of fragments.
func TestInteractionCapture_SuccessiveRecordsOfOneRefShareOneItemID(t *testing.T) {
	sk := assemble(t)
	open := newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindToolRun, Status: adapter.StatusInProgress, Ref: "call-1",
		Tool: "Read", Action: adapter.ToolAction{Type: "read", Path: "src/main.rs"},
	})
	done := newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindToolRun, Status: adapter.StatusCompleted, Ref: "call-1",
		Tool: "Read", OutputExcerpt: "fn main() {}",
	})
	sk.captureInteractions("s-fold", open, adapter.HookPayload{Event: "PreToolUse"})
	sk.captureInteractions("s-fold", done, adapter.HookPayload{Event: "PostToolUse"})

	got := awaitItems(t, sk, "s-fold", 1)
	first := itemString(t, got[0], "item_id")
	for i, item := range got {
		if id := itemString(t, item, "item_id"); id != first {
			t.Fatalf("record %d carries item_id %q, record 0 carries %q; two records of ONE tool call "+
				"must fold under one id (IS-ENV-2, IS-DELTA-3)", i, id, first)
		}
	}

	// A DIFFERENT ref is a different item: the map must key on the ref, not merely remember
	// the last id it minted.
	other := newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindToolRun, Status: adapter.StatusInProgress, Ref: "call-2", Tool: "Bash",
	})
	sk.captureInteractions("s-fold", other, adapter.HookPayload{Event: "PreToolUse"})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ids := map[string]bool{}
		for _, item := range interactionItems(t, sk, "s-fold") {
			ids[itemString(t, item, "item_id")] = true
		}
		if len(ids) == 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a second tool call never produced a second item_id; the fold key collapses distinct calls")
}

// TestInteractionCapture_TheTurnOpensOnAUserMessageAndClosesOnATerminalAgentMessage is
// IS-ENV-1, which is the daemon's rule and nobody else's -- the adapter sources no turn.
func TestInteractionCapture_TheTurnOpensOnAUserMessageAndClosesOnATerminalAgentMessage(t *testing.T) {
	sk := assemble(t)
	sk.captureInteractions("s-turn", newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindUserMessage, Source: adapter.SourceOwner, Text: "do it",
	}), adapter.HookPayload{Event: "UserPromptSubmit"})
	sk.captureInteractions("s-turn", newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindAgentMessage, Status: adapter.StatusCompleted, Ref: "msg-1",
		Text: "done", StopReason: "end_turn",
	}), adapter.HookPayload{Event: "Stop"})
	got := awaitItems(t, sk, "s-turn", 2)

	turn := itemString(t, got[0], "turn_id")
	if turn == "" {
		t.Fatal("a user_message opened no turn; IS-ENV-1 opens a turn on user_message and every " +
			"item inside it carries the turn's id")
	}
	if got1 := itemString(t, got[1], "turn_id"); got1 != turn {
		t.Fatalf("the agent_message carries turn_id %q, the user_message opened %q; the reply belongs "+
			"to the turn that asked for it", got1, turn)
	}

	// The terminal agent_message CLOSED the turn: what follows is outside one.
	sk.captureInteractions("s-turn", newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindToolRun, Status: adapter.StatusCompleted, Ref: "stray", Tool: "Read",
	}), adapter.HookPayload{Event: "PostToolUse"})
	after := awaitItems(t, sk, "s-turn", 3)
	if got2 := after[2]["turn_id"]; got2 != nil && got2 != "" {
		t.Errorf("an item after the turn's terminal agent_message carries turn_id %v; IS-ENV-1 closes "+
			"the turn on any terminal agent_message status, and `turn_id` is empty outside a turn", got2)
	}
}

// ---- the production call site ----------------------------------------------

// TestInteractionCapture_AnAuthenticatedHookReachesTheProducer is the WIRING assertion, and
// the reason the two adapter symbols are reachable from a `cmd/` main at all: serveHook is
// the daemon's live hook ingest, and an authenticated callback for a session whose adapter
// captures must reach the producer.
//
// AUTHENTICATION IS THE LOAD-BEARING HALF. The engine authenticates a callback against the
// session's per-invocation token (S6/G5); a post that fails it must not reach the transcript
// either, or an unauthenticated local process could write the owner's conversation.
func TestInteractionCapture_AnAuthenticatedHookReachesTheProducer(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print HOOKED\nidle 60s\n")
	token := hookTokenFor(t, sk.stateDir, m.ID)

	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) {
		return newCaptureAdapter(adapter.Interaction{
			Kind: adapter.KindAgentMessage, Status: adapter.StatusCompleted,
			Ref: "hook-msg", Text: "from the hook", StopReason: "end_turn",
		}), true
	})

	// A FOREIGN token first: it must change nothing. Asserted before the good post so a later
	// item cannot be mistaken for this one.
	if err := hookclient.Post(sk.SocketPath(), engine.Callback{
		SessionID: m.ID, Token: "not-the-token", Sequence: 1, Event: "Stop",
	}); err != nil {
		t.Fatalf("post a foreign-token hook: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if got := interactionItems(t, sk, m.ID); len(got) != 0 {
		t.Fatalf("an UNAUTHENTICATED hook post produced %d interaction record(s): %v. The engine's "+
			"token check (S6) is what stands between a local process and the owner's transcript", len(got), got)
	}

	if err := hookclient.Post(sk.SocketPath(), engine.Callback{
		SessionID: m.ID, Token: token, Sequence: 1, Event: "Stop",
	}); err != nil {
		t.Fatalf("post an authenticated hook: %v", err)
	}
	got := awaitItems(t, sk, m.ID, 1)
	if txt := itemString(t, got[0], "text"); txt != "from the hook" {
		t.Errorf("journalled text = %q; want the adapter's shaped text. serveHook decoded the callback "+
			"and never handed its body to the producer", txt)
	}
}

// hookTokenFor reads a session's per-session hook token out of the 0600 shim-launch.json the
// daemon writes at spawn -- the only place it lives besides the agent's env (ADR-004).
func hookTokenFor(t *testing.T, stateDir, id string) string {
	t.Helper()
	path := filepath.Join(stateDir, id, "shim-launch.json")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			var cfg struct {
				Env []string `json:"env"`
			}
			if json.Unmarshal(data, &cfg) == nil {
				for _, kv := range cfg.Env {
					if v, ok := strings.CutPrefix(kv, hookclient.EnvToken+"="); ok && v != "" {
						return v
					}
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the hook token never appeared in %s", path)
	return ""
}
