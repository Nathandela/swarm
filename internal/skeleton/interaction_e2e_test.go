package skeleton

// THE INTERACTION PROGRAM'S EXIT DEMONSTRATION -- the chain ADR-009/ADR-010 describe, end to
// end, with every hop the shipped system has:
//
//	adapter        REAL contract, FAKE CLI. internal/adapter.InteractionSource, discovered by
//	               the production AsInteractionSource type assertion. The CLI behind it is a
//	               double because no shipped adapter implements the extension yet (W1 measured
//	               that: all four pass conformance with zero source changes) and the Claude
//	               Code / Codex producers are out of this program's scope. Everything the
//	               daemon does with what it returns is production code.
//	hook ingest    REAL. `swarm hook`'s wire post over the daemon's own socket, demuxed by
//	               serveHook, AUTHENTICATED by the engine against the session's per-invocation
//	               token. The unauthenticated post is asserted separately in
//	               interaction_capture_test.go; this file uses the real token.
//	producer       REAL. The daemon's capture: AsInteractionSource -> Interaction.Validate ->
//	               §2 envelope -> ADR-010 §7's append floor -> journal.
//	journal        REAL. A bare `interaction` record, opaque payload, no mailbox kind
//	               (IS-LAYER-1).
//	gateway        REAL, and a SEPARATE PROCESS: the cmd/swarm-remote binary resolving its own
//	               params and delivering its own epoch grant, exactly as s19 runs it.
//	relay          REAL internal/remote/relay.Server over a real localhost WebSocket.
//	phone          REAL. The bound swarmmobile.App over a durable phonecore.Core, paired
//	               through the production ceremony.
//
// WHY IT REUSES THE s19 RIG. s19's rig IS the shipped stack with three named substitutions
// (key custody, `remote init`'s supervision half, the fake agent), each justified in that
// file. Standing up a second, lighter rig would mean a second phone that never runs
// phonecore's durable receive transaction, and the interaction plane's whole consumer half
// lives there. The rig is reused; TestPBE2E1 is not touched.
//
// THE ONE HOP THIS DOES NOT PROVE is the producer's own content: a real CLI's hook body
// shaped by a real adapter into real items. That is the Claude Code / Codex producer slice,
// excluded from this program by the task, and it is exactly the seam the double occupies.

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// TestInteractionE2E_ApprovalAndMessageReachThePhoneAndSurviveAReseed.
//
// EVERY LEG ASSERTS POSITIVELY, on the s19 rule that a chain of nil errors is satisfied by a
// system that seals nothing: the phone must SHOW an approval card it never asked for, the
// prose item beside it, and it must still hold both after a journal repair has replaced its
// roster underneath them.
func TestInteractionE2E_ApprovalAndMessageReachThePhoneAndSurviveAReseed(t *testing.T) {
	rig := newS19Rig(t)
	rig.Pair()
	rig.StartGateway()
	rig.Eventually("the machine's reconcile record reached the phone", func() bool {
		return rig.Summary().Reconciled
	})

	// A session the MACHINE launched, so the transcript below belongs to work the phone did
	// not cause -- which is the case ADR-009 exists for.
	sessionID := rig.LaunchOnMachine("print E2E_INTERACTION\nidle 600s\n")
	rig.Eventually("the phone's roster shows the session the machine launched", func() bool {
		return rig.RosterHas(sessionID)
	})
	_, localID, ok := protocol.ParseID(sessionID)
	if !ok {
		t.Fatalf("owner Launch returned %q, which is not a namespaced id", sessionID)
	}

	// The events the phone must see. Kinds chosen deliberately: an approval_request is the
	// item ADR-010 §4 wakes on and IS-LIFE-3 keeps alive, and an agent_message is the one kind
	// the floor merges by text (IS-DELTA-1) -- between them they cover both admission classes.
	items := &interactionScript{items: [][]adapter.Interaction{
		{{
			Kind: adapter.KindApprovalRequest, Status: adapter.StatusInProgress, Ref: "e2e-approval",
			Summary: "write src/main.rs", Mode: adapter.ModeCard,
			Decisions: []adapter.DecisionChoice{{ID: "allow", Label: "Allow"}, {ID: "deny", Label: "Deny"}},
			Action:    adapter.ToolAction{Type: "write", Path: "src/main.rs"},
		}},
		{{
			Kind: adapter.KindAgentMessage, Status: adapter.StatusCompleted, Ref: "e2e-message",
			Text: "I will write the file.", StopReason: "end_turn",
		}},
	}}
	rig.sk.adapterFor = func(string) (adapter.Adapter, bool) {
		return &captureAdapter{Adapter: newPlainAdapter(), items: nil, script: items}, true
	}

	// The event plane is armed BEFORE the first post: an item that lands is supposed to WAKE a
	// screen, and a listener attached afterwards would be satisfied by a system that raises no
	// event at all and is merely polled.
	events := &interactionListener{}
	if err := rig.App().SetEventListener(events); err != nil {
		t.Fatalf("App.SetEventListener: %v", err)
	}

	token := hookTokenFor(t, rig.stateDir, localID)
	for i := range items.items {
		if err := hookclient.Post(rig.sk.SocketPath(), engine.Callback{
			SessionID: localID, Token: token, Sequence: uint64(i + 1), Event: "PreToolUse",
		}); err != nil {
			t.Fatalf("post hook %d: %v", i, err)
		}
	}

	// ---- the phone holds both items ----------------------------------------
	var transcript []swarmmobile.TranscriptItem
	rig.Eventually("both interaction items reached the phone's transcript", func() bool {
		transcript = readTranscript(t, rig, sessionID)
		return len(transcript) >= 2
	})
	byKind := map[string]swarmmobile.TranscriptItem{}
	for _, it := range transcript {
		byKind[it.Kind] = it
	}
	apr, hasApr := byKind["approval_request"]
	msg, hasMsg := byKind["agent_message"]
	if !hasApr || !hasMsg {
		t.Fatalf("the phone's transcript holds %d item(s) of kinds %v; want an approval_request and an "+
			"agent_message%s", len(transcript), transcriptKinds(transcript), rig.gatewayTail())
	}
	if msg.Text != "I will write the file." {
		t.Errorf("agent_message text = %q; want the machine's own text -- the item crossed four hops "+
			"and its body must arrive intact", msg.Text)
	}
	if apr.ItemID == "" || apr.ItemID == msg.ItemID {
		t.Errorf("the two items carry item_ids %q and %q; each item is minted its own ULID and IS-ENV-2 "+
			"folds by it", apr.ItemID, msg.ItemID)
	}
	// §3.5's fields ride inside the item body verbatim: gomobile binds no map, so the client
	// decodes them (IS-COMPAT-1/-2). A card with no summary is an unactionable card.
	var body map[string]any
	if err := json.Unmarshal([]byte(apr.Body), &body); err != nil {
		t.Fatalf("the approval item's body is not a JSON object: %v (%q)", err, apr.Body)
	}
	if body["summary"] != "write src/main.rs" {
		t.Errorf("the approval card's summary is %v; want the machine's own text", body["summary"])
	}

	// ---- the item raised its own event -------------------------------------
	// IS-SS-1: a transcript item is not a roster event. A screen listening for one must not
	// have to filter the other, and the wake must say WHAT arrived or every screen re-reads the
	// whole transcript to find out whether it was prose or a card.
	rig.Eventually("an interaction event named the approval that arrived", func() bool {
		for _, e := range events.snapshot() {
			if e.Kind == "interaction" && e.SessionID == sessionID && e.Message == "approval_request" {
				return true
			}
		}
		return false
	})

	// ---- the pending card --------------------------------------------------
	pending := readPendingApprovals(t, rig)
	if len(pending) != 1 || pending[0].ItemID != apr.ItemID {
		t.Fatalf("PendingApprovals holds %d card(s) %v; want exactly the unresolved %q. IS-LIFE-2 makes "+
			"every request reach exactly one resolution, and until it does the card is what tells the "+
			"owner the machine is blocked", len(pending), transcriptIDs(pending), apr.ItemID)
	}

	// ---- IS-LIFE-3: the card survives a journal repair ----------------------
	// A reseed REPLACES the roster (PB-SYNC-8) and MERGES the transcript, because a transcript
	// is a cursor-ordered log whose events half IS-CAP-4 may cut to fit one frame -- replacing
	// it would delete history on every repair, and the unresolved approval_request is precisely
	// what IS-LIFE-3 keeps deliverable across one.
	if err := rig.App().Resync(phonecoreStreamJournal); err != nil {
		t.Fatalf("App.Resync(journal): %v", err)
	}
	// THE REPAIR MUST BE OBSERVED TO HAVE LANDED before anything is asserted about surviving
	// it. Resync returns once the request is sealed, not once the answer is folded, so an
	// assertion taken here would hold on a phone that never received a reseed at all -- which
	// is a test that passes on a system where the reseed wipes the transcript. The facade
	// raises State "resynced" from accept() only after the core has committed the frame.
	rig.Eventually("the journal repair landed on the phone", func() bool {
		for _, e := range events.snapshot() {
			if e.Kind == "journal" && e.State == "resynced" {
				return true
			}
		}
		return false
	})
	if !rig.RosterHas(sessionID) {
		t.Fatalf("the session vanished from the roster the reseed REPLACED (PB-SYNC-8); the repair "+
			"named %s and the roster does not%s", sessionID, rig.gatewayTail())
	}
	after := readTranscript(t, rig, sessionID)
	if len(after) < 2 {
		t.Fatalf("the transcript holds %d item(s) after a journal reseed; it held %d before. A reseed "+
			"that REPLACED the transcript would delete every item whose cursor the events half no "+
			"longer carries -- content the phone asked for and lost (IS-LIFE-3, IS-CAP-4)",
			len(after), len(transcript))
	}
	stillPending := readPendingApprovals(t, rig)
	if len(stillPending) != 1 || stillPending[0].ItemID != apr.ItemID {
		t.Fatalf("PendingApprovals holds %d card(s) %v after the repair; want the same unresolved %q. "+
			"An approval the machine is still blocked on must not be dropped by the very mechanism "+
			"IS-LIFE-3 re-delivers it on", len(stillPending), transcriptIDs(stillPending), apr.ItemID)
	}
}

// phonecoreStreamJournal is phonecore.StreamJournal's value. It is spelled out rather than
// imported so this file's imports stay the ones a reader must trust.
const phonecoreStreamJournal = "journal"

// ---- doubles and readers ---------------------------------------------------

// interactionScript hands out one batch of interactions per captured event, so successive
// hook posts shape different items -- a CLI's stream, not one repeated record.
type interactionScript struct {
	mu    sync.Mutex
	items [][]adapter.Interaction
	next  int
}

func (s *interactionScript) take() []adapter.Interaction {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next >= len(s.items) {
		return nil
	}
	out := s.items[s.next]
	s.next++
	return out
}

// interactionListener collects what the facade dispatched. The facade delivers from its own
// goroutine while the test polls, so the slice is guarded.
type interactionListener struct {
	mu     sync.Mutex
	events []swarmmobile.Event
}

func (l *interactionListener) OnEvent(e *swarmmobile.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, *e)
}

func (l *interactionListener) snapshot() []swarmmobile.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]swarmmobile.Event(nil), l.events...)
}

func readTranscript(t *testing.T, r *s19Rig, session string) []swarmmobile.TranscriptItem {
	t.Helper()
	page, err := r.App().ReadTranscript(session, 0, 0)
	if err != nil {
		t.Fatalf("App.ReadTranscript(%q): %v", session, err)
	}
	return pageItems(t, page)
}

func readPendingApprovals(t *testing.T, r *s19Rig) []swarmmobile.TranscriptItem {
	t.Helper()
	page, err := r.App().PendingApprovals()
	if err != nil {
		t.Fatalf("App.PendingApprovals: %v", err)
	}
	return pageItems(t, page)
}

func pageItems(t *testing.T, page *swarmmobile.TranscriptPage) []swarmmobile.TranscriptItem {
	t.Helper()
	n, err := page.Count()
	if err != nil {
		t.Fatalf("TranscriptPage.Count: %v", err)
	}
	out := make([]swarmmobile.TranscriptItem, 0, n)
	for i := 0; i < n; i++ {
		it, err := page.At(i)
		if err != nil {
			t.Fatalf("TranscriptPage.At(%d): %v", i, err)
		}
		out = append(out, *it)
	}
	return out
}

func transcriptKinds(items []swarmmobile.TranscriptItem) string {
	var out []string
	for _, it := range items {
		out = append(out, it.Kind)
	}
	return strings.Join(out, ",")
}

func transcriptIDs(items []swarmmobile.TranscriptItem) string {
	var out []string
	for _, it := range items {
		out = append(out, it.ItemID)
	}
	return strings.Join(out, ",")
}
