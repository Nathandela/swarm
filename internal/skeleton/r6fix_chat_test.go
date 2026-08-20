package skeleton

// FAILING-FIRST (TDD RED, GG-5) for Wave R6 review findings B3, B4(a), B5, B7 and B9 -- the
// daemon-side half of the fix-pack. Every test here is a reviewer's temporary probe made
// permanent.
//
// B3 (BLOCKER, ADR-017). composerSend checked session resolution, core.Get and expected_turn
// and NEVER the session's structured_chat capability, so a session degraded by a proven
// structured_gap accepted a phone send, had the text typed into its PTY, and replied OK.
// Probed: registered {StructuredChat:true, Interrupt:true}, markSessionDegraded, record now
// {StructuredChat:false, TerminalFallback:true}; ComposerSend returned code="" err=nil and
// the fake CLI got the text on stdin. ADR-017 T2 rule 2 and Mirror M5.5 say a fallback
// session has NO structured composer because it has no message sink -- the user's message
// goes in and the transcript can never show it. turn_interrupt already refused this shape
// correctly; the composer is now symmetric with it.
//
// B4(a) (BLOCKER, ADR-017 gap honesty). interactionHistory kept only rec.Type ==
// "interaction", so every journal.TypeStructuredGap record was dropped and each history page
// spanned a proven tear CONTIGUOUSLY with nothing marking it.
//
// B5 (BLOCKER). interactionHistory trimmed by RAW JOURNAL RECORD (older[len(older)-limit:])
// over a channel the phone pages by ITEM ID, so a multi-record agent_message could arrive as
// a headless TAIL -- rendered by the phone as a whole message -- with its earlier records
// permanently unreachable, because the next page asks for what is older than the item's FIRST
// record.
//
// B7 (MAJOR). interruptTurn carried no turn coordinate; see r6_interruptapply_test.go's
// header for the probe.
//
// B9 (MAJOR). stampComposerEchoLocked consumed the first pending entry whose text EQUALED the
// echoed prompt, and entries expired only by match, failed write, or the 8-deep FIFO. Probed:
// an owner-typed "yes" was stamped source=phone with the phone's operation_id.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

// r6FixOpenTurn captures one user_message and returns the session's resulting CURRENT turn,
// read from the daemon's OWN turnIDs state rather than reconstructed -- the same state
// composerSend and interruptTurn check against, so a test can never pass by agreeing with a
// reimplementation of the rule. shapeItem runs inside captureInteractions, so the turn is set
// by the time it returns.
func r6FixOpenTurn(t *testing.T, sk *Daemon, local, text string) string {
	t.Helper()
	sk.captureInteractions(local, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindUserMessage, Text: text, Source: adapter.SourceOwner,
	}), adapter.HookPayload{Event: "UserPromptSubmit"})
	sk.itemMu.Lock()
	sk.initInteractionsLocked()
	turn := sk.turnIDs[local]
	sk.itemMu.Unlock()
	if turn == "" {
		t.Fatalf("capturing %q opened no turn for session %q", text, local)
	}
	return turn
}

// ---- B3: a degraded session has no structured composer ------------------------------------

// TestR6Fix_ADegradedSessionRefusesTheComposerAndTypesNothing is the probe, frozen.
func TestR6Fix_ADegradedSessionRefusesTheComposerAndTypesNothing(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-degraded-composer"))
	turn := r6FixOpenTurn(t, r.sk, r.local, "before the gap")

	// The session proves a structured chat...
	r.sk.registerSessionCapabilities(r.local, protocol.SessionCapabilities{
		StructuredChat: true, Interrupt: true,
	})
	// ...and then proves a gap. ADR-017 T2 rule 2: structured_chat is disabled for this
	// session instance, one-way, durably.
	r.sk.markSessionDegraded(r.local)
	if c, ok := r.sk.sessionCapabilities(r.local); !ok || c.StructuredChat || !c.TerminalFallback {
		t.Fatalf("CONTROL BROKEN: after markSessionDegraded the record is %+v (ok=%v); this test "+
			"cannot measure the composer's behaviour on a degraded session if the session is not "+
			"degraded", c, ok)
	}

	code, err := r.sk.api.ComposerSend(r.sk.api.endpointID, "devA:01JDEGRADED",
		protocol.ComposerSendReq{Session: r.session, ExpectedTurn: turn, Text: "please continue"})
	if err == nil {
		t.Fatalf("composer_send on a TERMINAL-FALLBACK session was ACCEPTED (code %q): the user's "+
			"message is typed into a PTY whose transcript can never show it -- ADR-017's "+
			"silently-bridged gap, which is the one move the ADR forbids", code)
	}
	if code != protocol.CodeStructuredUnsupported {
		t.Errorf("degraded composer_send = code %q, want %q: the caller is fine and the capability "+
			"is absent, which is a nameable state and not a generic failure",
			code, protocol.CodeStructuredUnsupported)
	}
	r.assertNothingWasTyped(t)
}

// TestR6Fix_ADegradeWithNoPriorRecordStillRefusesTheComposer is the PRODUCTION shape of
// finding B3, and the reason the gate reads the durable degrade MARKER rather than the
// capability record.
//
// registerSessionCapabilities has no production caller (Daemon.sessionDegraded's doc records
// the measurement), so a live session has NO record at all -- and markSessionDegraded's own
// comment says that with no record "the marker above is the whole degrade". A gate that
// consulted only the record would therefore be a gate that never fires on any real session,
// which is finding B3 reintroduced one layer in.
func TestR6Fix_ADegradeWithNoPriorRecordStillRefusesTheComposer(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-nocap-composer"))
	turn := r6FixOpenTurn(t, r.sk, r.local, "before")

	// No registerSessionCapabilities anywhere: exactly what a real session looks like.
	if _, ok := r.sk.sessionCapabilities(r.local); ok {
		t.Fatalf("CONTROL BROKEN: this session already has a capability record, so it is not the " +
			"production shape this test measures")
	}
	r.sk.markSessionDegraded(r.local)

	code, err := r.sk.api.ComposerSend(r.sk.api.endpointID, "devA:01JNOCAP",
		protocol.ComposerSendReq{Session: r.session, ExpectedTurn: turn, Text: "please continue"})
	if err == nil {
		t.Fatalf("composer_send on a session with a PROVEN structured gap and no capability record "+
			"was ACCEPTED (code %q): in production no record is ever authored, so this is the only "+
			"shape the degrade actually takes", code)
	}
	if code != protocol.CodeStructuredUnsupported {
		t.Errorf("degraded composer_send = code %q, want %q", code, protocol.CodeStructuredUnsupported)
	}
	r.assertNothingWasTyped(t)
}

// TestR6Fix_AnUndegradedSessionWithNoRecordStillAcceptsTheComposer is the disclosed
// compromise, pinned so it cannot drift silently. An ABSENT record is not treated as an
// absent capability: with registerSessionCapabilities unwired, refusing on absence would
// refuse every send on every session -- feature-off dressed as fail-closed. See
// requireStructuredComposer's doc; docs/verification/r6-chat.md carries it as a residual.
func TestR6Fix_AnUndegradedSessionWithNoRecordStillAcceptsTheComposer(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-norecord-composer"))
	turn := r6FixOpenTurn(t, r.sk, r.local, "before")

	code, err := r.sk.api.ComposerSend(r.sk.api.endpointID, "devA:01JNOREC",
		protocol.ComposerSendReq{Session: r.session, ExpectedTurn: turn, Text: "keep going"})
	if err != nil || code != "" {
		t.Fatalf("composer_send on an UNDEGRADED session with no record was refused: code %q err %v. "+
			"Until the capability-publication slice wires registerSessionCapabilities, that is every "+
			"session there is, and refusing here disables the composer entirely.", code, err)
	}
}

// TestR6Fix_AStructuredSessionStillAcceptsTheComposer is the anti-vacuity control for the two
// above: a fence that refused EVERY send would pass them both and break the feature.
func TestR6Fix_AStructuredSessionStillAcceptsTheComposer(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-structured-composer"))
	turn := r6FixOpenTurn(t, r.sk, r.local, "before")
	r.sk.registerSessionCapabilities(r.local, protocol.SessionCapabilities{StructuredChat: true})

	code, err := r.sk.api.ComposerSend(r.sk.api.endpointID, "devA:01JOK",
		protocol.ComposerSendReq{Session: r.session, ExpectedTurn: turn, Text: "keep going"})
	if err != nil || code != "" {
		t.Fatalf("composer_send on a STRUCTURED session was refused: code %q err %v", code, err)
	}
	if got := r.readBack(t); !strings.Contains(got, "keep going") {
		t.Errorf("the session's stdin held %q, want the accepted message", got)
	}
}

// ---- B7: an interrupt names the turn it stops ---------------------------------------------

// TestR6Fix_AStaleInterruptIsRefusedAndTypesNothing is the probe, frozen: the interrupt
// rendered against turnA must not reach turnB.
func TestR6Fix_AStaleInterruptIsRefusedAndTypesNothing(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-stale-interrupt"))
	turnA := r6FixOpenTurn(t, r.sk, r.local, "the first thing")
	turnB := r6FixOpenTurn(t, r.sk, r.local, "the second thing")
	if turnA == turnB {
		t.Fatalf("CONTROL BROKEN: two user messages produced one turn %q; the supersession this "+
			"test measures did not happen", turnA)
	}

	code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID, "devA:01JSTALE",
		protocol.TurnInterruptReq{Session: r.session, ExpectedTurn: turnA})
	if err == nil {
		t.Fatalf("a Stop rendered against the SUPERSEDED turn %q was applied (code %q). In "+
			"playbook §8.1 the turn it would have reached is the one the OWNER just started at "+
			"the terminal, and the cancel key at an idle prompt clears their half-typed line.",
			turnA, code)
	}
	if code != protocol.CodeStaleTurn {
		t.Errorf("stale interrupt = code %q, want %q -- the composer's own code, because it is "+
			"the same race with the same remedy (re-read the transcript)", code, protocol.CodeStaleTurn)
	}
	r.assertNothingWasTyped(t)
}

// TestR6Fix_AnInterruptNamingNoTurnIsRefusedAtTheSeam: the seam carries its own contract, so
// "interrupt whatever is running" has no spelling at ANY caller, not merely at the wire.
func TestR6Fix_AnInterruptNamingNoTurnIsRefusedAtTheSeam(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-turnless-interrupt"))
	r6FixOpenTurn(t, r.sk, r.local, "running")

	code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID, "devA:01JNOTURN",
		protocol.TurnInterruptReq{Session: r.session})
	if err == nil || code != protocol.CodeInvalidField {
		t.Fatalf("interrupt with an empty expected_turn = code %q err %v, want %q",
			code, err, protocol.CodeInvalidField)
	}
	r.assertNothingWasTyped(t)
}

// ---- B9: the echo correlation is time-bounded ---------------------------------------------

// TestR6Fix_AnExpiredInjectionNoLongerClaimsAnIdenticalOwnerPrompt is the probe, frozen: an
// owner-typed word must not inherit a phone attribution because the phone happened to send the
// same word earlier. The clock is the daemon's own seam, so no wall-clock sleep is involved.
func TestR6Fix_AnExpiredInjectionNoLongerClaimsAnIdenticalOwnerPrompt(t *testing.T) {
	sk := assemble(t)
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sk.itemClock = func() time.Time { return now }

	const local = "s-echo-ttl"
	sk.itemMu.Lock()
	sk.initInteractionsLocked()
	sk.pendingSends = map[string][]pendingSend{
		local: {{text: "yes", operationID: "devA:01JPHONE", at: now}},
	}
	sk.itemMu.Unlock()

	// Well past the bound: the injection's echo never came, and this "yes" is somebody
	// else's.
	now = now.Add(pendingSendTTL + time.Second)

	fields := map[string]any{"source": adapter.SourceOwner}
	sk.itemMu.Lock()
	sk.stampComposerEchoLocked(local, "yes", "", fields)
	sk.itemMu.Unlock()

	if fields["source"] != adapter.SourceOwner {
		t.Errorf("an owner-typed %q was stamped source=%v with operation_id=%v, %v after the "+
			"injection it matched. Attribution must be a fact the daemon OBSERVED, and it observed "+
			"no injection within the window.", "yes", fields["source"], fields["operation_id"],
			pendingSendTTL+time.Second)
	}
	if _, ok := fields["operation_id"]; ok {
		t.Errorf("the owner prompt carries operation_id %v, binding a phone op to a message the "+
			"phone did not send", fields["operation_id"])
	}
	sk.itemMu.Lock()
	left := len(sk.pendingSends[local])
	sk.itemMu.Unlock()
	if left != 0 {
		t.Errorf("%d expired injections are still pending; expiry must drop them, or the queue "+
			"keeps a stale claimant for every later identical prompt", left)
	}
}

// TestR6Fix_AFreshInjectionStillClaimsItsEcho is the anti-vacuity control: a bound that
// expired everything would pass the test above and silently end phone attribution entirely.
func TestR6Fix_AFreshInjectionStillClaimsItsEcho(t *testing.T) {
	sk := assemble(t)
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	sk.itemClock = func() time.Time { return now }

	const local = "s-echo-fresh"
	sk.itemMu.Lock()
	sk.initInteractionsLocked()
	sk.pendingSends = map[string][]pendingSend{
		local: {{text: "yes", operationID: "devA:01JPHONE", at: now}},
	}
	sk.itemMu.Unlock()

	now = now.Add(pendingSendTTL / 2)

	fields := map[string]any{"source": adapter.SourceOwner}
	sk.itemMu.Lock()
	sk.stampComposerEchoLocked(local, "yes", "", fields)
	sk.itemMu.Unlock()

	if fields["source"] != adapter.SourcePhone {
		t.Errorf("a phone injection echoed back INSIDE the window was attributed %v, not phone: "+
			"the bound has eaten the fact the daemon actually observed", fields["source"])
	}
	if fields["operation_id"] != "devA:01JPHONE" {
		t.Errorf("echo operation_id = %v, want the phone op's own id", fields["operation_id"])
	}
}

// ---- B4(a) + B5: history pages carry the tear, and never begin mid-item -------------------

// r6FixHistoryRecords builds a synthetic journal page the way JournalReadFrom delivers one,
// so the paging rules can be exercised over shapes (a multi-record streamed message, a
// structured_gap) that are awkward to provoke through a live capture and are exactly the
// shapes both findings are about.
func r6FixHistoryRecord(session string, cursor uint64, itemID, kind, text string) protocol.JournalRecord {
	payload, err := json.Marshal(map[string]any{
		"v": 1, "item_id": itemID, "kind": kind, "text": text,
	})
	if err != nil {
		panic(err)
	}
	return protocol.JournalRecord{
		SessionID: session, Cursor: cursor, Type: "interaction", Item: payload,
	}
}

func r6FixGapRecord(session string, cursor uint64) protocol.JournalRecord {
	return protocol.JournalRecord{SessionID: session, Cursor: cursor, Type: structuredGapRecordType}
}

// TestR6Fix_APageNeverBeginsInTheMiddleOfAnItem is B5's probe, frozen. A three-record
// agent_message under one item_id, a limit that would cut through it, and the assertion that
// the page starts at an item boundary instead.
func TestR6Fix_APageNeverBeginsInTheMiddleOfAnItem(t *testing.T) {
	const sess = "s1"
	older := []protocol.JournalRecord{
		r6FixHistoryRecord(sess, 1, "01JA", "user_message", "ask"),
		r6FixHistoryRecord(sess, 2, "01JB", "agent_message", "one "),
		r6FixHistoryRecord(sess, 3, "01JB", "agent_message", "two "),
		r6FixHistoryRecord(sess, 4, "01JB", "agent_message", "three"),
	}
	// limit 2 would have sliced older[2:] -- the TAIL of 01JB with its head missing, which
	// the phone folds and renders as a whole message, and which no later page can return
	// (the next page asks for what is older than 01JB's FIRST record, cursor 2).
	start := historyPageStart(older, 2)
	if start != 0 && historyItemID(older[start]) != "01JB" {
		t.Fatalf("the page begins at index %d, record %+v: that is the middle of an item",
			start, older[start])
	}
	if start == 2 || start == 3 {
		t.Fatalf("the page begins at index %d -- inside item 01JB's run. The phone folds by "+
			"item_id and cannot know a head is missing, so it renders the tail of the agent's "+
			"message as the whole of it, and the earlier records are unreachable forever.", start)
	}
	if start != 1 {
		t.Errorf("page start = %d, want 1: the largest suffix of WHOLE items fitting limit 2 is "+
			"item 01JB alone, whose three records are shipped whole (an item too large to fit "+
			"ships alone and over limit rather than being cut -- refusing it would return an "+
			"empty page with floor=false, and the phone would ask forever)", start)
	}
}

// TestR6Fix_AnItemLargerThanTheLimitShipsWholeRatherThanLivelocking pins the escape hatch.
func TestR6Fix_AnItemLargerThanTheLimitShipsWholeRatherThanLivelocking(t *testing.T) {
	const sess = "s1"
	older := []protocol.JournalRecord{
		r6FixHistoryRecord(sess, 1, "01JB", "agent_message", "one "),
		r6FixHistoryRecord(sess, 2, "01JB", "agent_message", "two "),
		r6FixHistoryRecord(sess, 3, "01JB", "agent_message", "three"),
	}
	start := historyPageStart(older, 1)
	if start != 0 {
		t.Fatalf("page start = %d for an item of 3 records under limit 1; want 0 (the whole item). "+
			"Any other answer returns a partial item or an empty page with more available, and the "+
			"phone can never make progress", start)
	}
}

// TestR6Fix_AStructuredGapIsItsOwnPageBoundary: a gap is atomic, folds with nothing, and can
// therefore always begin a page.
func TestR6Fix_AStructuredGapIsItsOwnPageBoundary(t *testing.T) {
	const sess = "s1"
	older := []protocol.JournalRecord{
		r6FixHistoryRecord(sess, 1, "01JA", "user_message", "ask"),
		r6FixGapRecord(sess, 2),
		r6FixHistoryRecord(sess, 3, "01JB", "agent_message", "answer"),
	}
	if got := historyPageStart(older, 2); got != 1 {
		t.Errorf("page start = %d, want 1 (the gap record): a structured_gap carries no item_id "+
			"and folds with nothing, so it is always its own boundary", got)
	}
}

// TestR6Fix_TheHistoryPageCarriesTheStructuredGap is B4(a)'s probe, frozen, driven through the
// REAL interactionHistory against a real journal -- not through the helper above, because the
// defect was in what the SCAN kept, not in how the page was trimmed.
func TestR6Fix_TheHistoryPageCarriesTheStructuredGap(t *testing.T) {
	sk := assemble(t)
	const local = "s-history-gap"

	sk.captureInteractions(local, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindUserMessage, Text: "before the tear", Source: adapter.SourceOwner,
	}), adapter.HookPayload{Event: "UserPromptSubmit"})
	if err := sk.Core().EmitStructuredGap(local, "shim spool gap"); err != nil {
		t.Fatalf("EmitStructuredGap: %v", err)
	}
	sk.captureInteractions(local, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindUserMessage, Text: "after the tear", Source: adapter.SourceOwner,
	}), adapter.HookPayload{Event: "UserPromptSubmit"})

	items := awaitItems(t, sk, local, 2)
	last := items[len(items)-1]
	before, _ := last["item_id"].(string)
	if before == "" {
		t.Fatalf("the trailing item carries no item_id: %v", last)
	}

	session := protocol.NamespacedID(sk.api.endpointID, local)
	recs, _, code, err := sk.api.InteractionHistory(session, before, 50)
	if err != nil {
		t.Fatalf("interaction_history: code %q err %v", code, err)
	}
	var sawGap bool
	for _, rec := range recs {
		if rec.Type == structuredGapRecordType {
			sawGap = true
			if len(rec.Item) == 0 {
				t.Error("the structured_gap crossed the wire with an EMPTY body: the phone can see " +
					"that a tear exists only by inspecting the type, and has nothing to render")
			}
		}
	}
	if !sawGap {
		t.Fatalf("the history page (%d records) carries NO structured_gap. The daemon PROVED a "+
			"capability tear between these two messages and the page presents them as contiguous "+
			"-- ADR-017's silently-bridged gap, on the paging channel.", len(recs))
	}
}
