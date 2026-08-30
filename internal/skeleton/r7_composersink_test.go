package skeleton

// FAILING-FIRST (TDD RED, GG-5) for Mirror M4.4: composer_send maps to turn/steer mid-turn or
// turn/start when idle, interrupt maps to turn/interrupt -- PER-CLI INBOUND DISPATCH BEHIND
// THE ONE OP R6 ALREADY BUILT. Bead: agents-tracker-hggx.8. ADR-013 §R7.5, playbook §8.2.
//
// THIS IS THE CORRECTION OF A LIVE DEFECT, NOT ONLY NEW WORK, and the defect is reachable at
// HEAD before any R7 code. Daemon.composerSend (chat.go:113) resolves the session, checks
// expected_turn, and calls injectComposerText (chat.go:227) -- which writes the text and a CR
// INTO THE PTY -- for EVERY PROVIDER, WITH NO SEAM AND NO PROVIDER CHECK ANYWHERE ON THE PATH
// (protocol/remote_chat.go:108, remotegw/command_loop.go:930, skeleton/chat.go:185). A phone
// send to a Codex session TODAY types into the Codex TUI. That is what playbook §8.2 forbids
// in as many words.
//
// R7 closes it STRUCTURALLY rather than by naming `codex` in the daemon:
//
//	composer_send resolves a MESSAGE SINK per session instance:
//	  live backend        -> turn/start when the daemon's turn is empty,
//	                         turn/steer when it is not, carrying the native expectedTurnId
//	  no backend          -> the adapter's KEYSTROKE seam, IF IT PROVES ONE
//	  neither             -> refuse structured_unsupported, HAVING TYPED NOTHING
//
// The Codex adapter implements no keystroke seam and never will, so the fallback is
// STRUCTURALLY UNREACHABLE for it -- ADR-010 §5's posture doing the work a provider name would
// otherwise have to do.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/Nathandela/swarm/internal/protocol"
)

// r7ComposerRig is a Codex-shaped session with a live backend and an attached owner, so both
// "the RPC went out" and "nothing was typed" are observations.
type r7ComposerRig struct {
	sk      *Daemon
	local   string
	session string
	att     *protocol.Attachment
	backend *r7FakeBackend
	adapter *r7CodexAdapter
}

func newR7ComposerRig(t *testing.T, withBackend bool) *r7ComposerRig {
	t.Helper()
	ad := &r7CodexAdapter{Adapter: newPlainAdapter().Adapter}
	sk := assemble(t)
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return ad, true })

	m := launchFake(t, sk, r7StdinScript)
	session := protocol.NamespacedID(sk.api.endpointID, m.ID)
	oc := dialClient(t, sk)
	att, err := oc.Attach(session)
	if err != nil {
		t.Fatalf("owner attach: %v", err)
	}
	t.Cleanup(func() { _ = att.Detach() })

	r := &r7ComposerRig{sk: sk, local: m.ID, session: session, att: att, adapter: ad}
	if withBackend {
		r.backend = newR7FakeBackend()
		r.backend.reply["turn/start"] = json.RawMessage(
			`{"turn":{"id":"01a0033b-d0be-77e1-88e7-584ddeea562d","items":[],"itemsView":"notLoaded","status":"inProgress"}}`)
		r.backend.reply["turn/steer"] = json.RawMessage(`{"turnId":"01a0033b-d0be-77e1-88e7-584ddeea562d"}`)
		sk.registerBackend(m.ID, "01a00339-a80e-72a0-966f-116427b6b9ce", r.backend)
	}
	return r
}

// assertPTYUntouched proves the PTY got nothing. It flushes the line discipline and reads back
// what the fake CLI saw, exactly as injectRig.assertNothingWasTyped does for approvals.
func (r *r7ComposerRig) assertPTYUntouched(t *testing.T) {
	t.Helper()
	if err := r.att.Input([]byte("\n")); err != nil {
		t.Fatalf("flush the session's line discipline: %v", err)
	}
	ok, drained := awaitFrames(r.att, "got:", 20*time.Second)
	if !ok {
		t.Fatalf("the fake CLI never reported its stdin; drained %q", drained)
	}
	i := strings.Index(drained, "got:")
	line := drained[i:]
	if j := strings.IndexAny(line, "\r\n"); j >= 0 {
		line = line[:j]
	}
	if got := strings.TrimSpace(strings.TrimPrefix(line, "got:")); got != "" {
		t.Errorf("the session's stdin held %q. NO CODEX SEMANTIC OPERATION MAY BE IMPLEMENTED BY "+
			"TERMINAL KEYSTROKE INJECTION (playbook §8.2); this is the live defect R7 exists to close", got)
	}
}

// r7Send drives composerSend through the coreAPI seam the protocol Server holds.
func (r *r7ComposerRig) send(t *testing.T, expectedTurn, text, opID string) (protocol.ErrorCode, error) {
	t.Helper()
	return r.sk.api.ComposerSend(r.sk.api.endpointID, opID, protocol.ComposerSendReq{
		Session: r.session, ExpectedTurn: expectedTurn, Text: text,
	})
}

// r7CallParams returns the params of the FIRST call to method, or fails.
func r7CallParams(t *testing.T, b *r7FakeBackend, method string) map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.calls {
		if c.Method != method {
			continue
		}
		var out map[string]any
		if err := json.Unmarshal(c.Params, &out); err != nil {
			t.Fatalf("decode %s params: %v (%s)", method, err, c.Params)
		}
		return out
	}
	t.Fatalf("no %s call was made; the backend saw %v", method, methodsOf(b))
	return nil
}

func methodsOf(b *r7FakeBackend) []string {
	out := make([]string, 0, len(b.calls))
	for _, c := range b.calls {
		out = append(out, c.Method)
	}
	return out
}

// ---------------------------------------------------------------------------
// The sink resolution
// ---------------------------------------------------------------------------

// TestR7ComposerSink_AnIdleBackendSessionDispatchesTurnStartAndTypesNOTHING is the idle arm.
// `input` is an ARRAY of UserInput -- passing an object yields
// {"code":-32600,"message":"Invalid request: invalid type: map, expected a sequence"}
// (RECORDED: errors-observed.json).
func TestR7ComposerSink_AnIdleBackendSessionDispatchesTurnStartAndTypesNOTHING(t *testing.T) {
	r := newR7ComposerRig(t, true)

	code, err := r.send(t, "", "ship it", "devA:01JIDLE0000000000000000")
	if err != nil || code != "" {
		t.Fatalf("idle composer send on a backend session refused: code %q err %v", code, err)
	}

	params := r7CallParams(t, r.backend, "turn/start")
	if params["threadId"] != "01a00339-a80e-72a0-966f-116427b6b9ce" {
		t.Errorf("turn/start threadId = %v, want the session's registered thread", params["threadId"])
	}
	input, ok := params["input"].([]any)
	if !ok {
		t.Fatalf("turn/start input is %T, want an ARRAY of UserInput; a map is refused by the real "+
			"server with `invalid type: map, expected a sequence` (RECORDED: errors-observed.json)",
			params["input"])
	}
	if len(input) != 1 {
		t.Fatalf("turn/start input has %d elements, want 1", len(input))
	}
	first, _ := input[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "ship it" {
		t.Errorf("turn/start input[0] = %v, want {type:text, text:\"ship it\"}", first)
	}

	r.assertPTYUntouched(t)
}

// TestR7ComposerSink_TwoImmediateIdleSendsStartOnceThenSteerInOrder is the queue boundary:
// the app-server answers turn/start before its turn/started notification necessarily reaches
// the daemon. A second message accepted in that interval must use the native turn id returned by
// the first call and steer that turn; issuing a second turn/start makes the result timing-dependent
// and can create a competing conversation.
func TestR7ComposerSink_TwoImmediateIdleSendsStartOnceThenSteerInOrder(t *testing.T) {
	r := newR7ComposerRig(t, true)

	if code, err := r.send(t, "", "first", "devA:01JFIFOFIRST000000000000"); err != nil || code != "" {
		t.Fatalf("first idle send refused: code %q err %v", code, err)
	}
	if code, err := r.send(t, "", "second", "devA:01JFIFOSECOND00000000000"); err != nil || code != "" {
		t.Fatalf("second immediate send refused: code %q err %v", code, err)
	}

	calls := r.backend.recorded()
	if len(calls) != 2 {
		t.Fatalf("backend calls = %d, want 2", len(calls))
	}
	if calls[0].Method != "turn/start" || calls[1].Method != "turn/steer" {
		t.Fatalf("immediate sends dispatched %q then %q, want turn/start then turn/steer", calls[0].Method, calls[1].Method)
	}
	var steer struct {
		ExpectedTurnID string `json:"expectedTurnId"`
		Input          []struct {
			Text string `json:"text"`
		} `json:"input"`
	}
	if err := json.Unmarshal(calls[1].Params, &steer); err != nil {
		t.Fatalf("decode second call: %v", err)
	}
	if steer.ExpectedTurnID != r7NativeTurnID {
		t.Errorf("second send steered native turn %q, want turn/start's returned %q", steer.ExpectedTurnID, r7NativeTurnID)
	}
	if len(steer.Input) != 1 || steer.Input[0].Text != "second" {
		t.Errorf("second steer input = %+v, want the second message intact", steer.Input)
	}
}

// TestR7ComposerSink_AMidTurnSendDispatchesTurnSteerCarryingTheNATIVEExpectedTurnId is the
// mid-turn arm, and the guard is the CLI's own: turn/steer's expectedTurnId is documented in
// the generated binding as "Required active turn id precondition. The request fails when it
// does not match the currently active turn." R1 note 4 says to PROPAGATE it rather than
// inventing a Swarm-side guard, and the gate recorded the steer returning THE SAME turn id
// (turn-steer.json), proving it applied to that exact in-flight turn.
func TestR7ComposerSink_AMidTurnSendDispatchesTurnSteerCarryingTheNATIVEExpectedTurnId(t *testing.T) {
	r := newR7ComposerRig(t, true)

	// Open a turn the way the real stream does: the RECORDED item/started frame through the
	// REAL producer-edge pump and the REAL codex shaper. Round 1 opened it from a hand-built
	// Interaction that carried no turn id at all, which is why the assertion below could only
	// ever have been "some string".
	turn := r7OpenNativeTurn(t, r)

	code, err := r.send(t, turn, "actually, stop", "devA:01JSTEER000000000000000")
	if err != nil || code != "" {
		t.Fatalf("mid-turn composer send refused: code %q err %v", code, err)
	}

	if len(r.backend.calls) == 0 {
		t.Fatal("no RPC was made for a mid-turn send")
	}
	params := r7CallParams(t, r.backend, "turn/steer")
	// EQUALITY WITH THE CLI'S OWN ID, not non-emptiness (review MEDIUM 7). This assertion is
	// the one that would have caught round 1's defect: the daemon's minted ULID is a
	// perfectly non-empty string, and it matches nothing in the server's turn table.
	if got, _ := params["expectedTurnId"].(string); got != r7NativeTurnID {
		t.Errorf("turn/steer expectedTurnId = %q, want the CLI's OWN %q. It is REQUIRED by the "+
			"binding and it is the built-in optimistic-concurrency guard a two-surface composer "+
			"needs to avoid steering a turn that already ended -- against ITS turn table, not ours",
			got, r7NativeTurnID)
	}
	for _, m := range methodsOf(r.backend) {
		if m == "turn/start" {
			t.Error("a MID-TURN send dispatched turn/start; that queues a second turn instead of " +
				"steering the running one, so the owner's question and the phone's arrive as two " +
				"separate conversations")
		}
	}

	r.assertPTYUntouched(t)
}

// TestR7ComposerSink_RenderedTurnIsAdvisoryForSend pins the queue migration. expected_turn stays
// signed so old/new peers share one wire shape, but a composer message is ordered work rather than
// a delayed tap on a destructive target. The daemon dispatches it against its current native turn;
// Stop keeps the strict precondition in the interrupt tests below.
func TestR7ComposerSink_RenderedTurnIsAdvisoryForSend(t *testing.T) {
	r := newR7ComposerRig(t, true)
	r7OpenNativeTurn(t, r)

	code, err := r.send(t, "a-rendered-turn-that-is-no-longer-current", "use the latest context", "devA:01JADVISORY0000000000000")
	if err != nil || code != "" {
		t.Fatalf("send carrying stale rendered context was refused: code %q err %v", code, err)
	}
	if got := methodsOf(r.backend); len(got) != 1 || got[0] != "turn/steer" {
		t.Fatalf("advisory send dispatched %v, want one turn/steer against current state", got)
	}
	params := r7CallParams(t, r.backend, "turn/steer")
	if got, _ := params["expectedTurnId"].(string); got != r7NativeTurnID {
		t.Errorf("turn/steer expectedTurnId = %q, want current native turn %q", got, r7NativeTurnID)
	}
	r.assertPTYUntouched(t)
}

// TestR7ComposerSink_NoBackendAndNoKeystrokeSeamREFUSESHavingTypedNothing is the third arm of
// the sink resolution and the one that makes the whole thing safe WITHOUT the
// capability-publication slice: §R7.5's refusal is sufficient on its own.
func TestR7ComposerSink_NoBackendAndNoKeystrokeSeamREFUSESHavingTypedNothing(t *testing.T) {
	r := newR7ComposerRig(t, false) // Codex-shaped adapter, NO backend registered

	code, err := r.send(t, "", "ship it", "devA:01JNOSINK00000000000000")
	if code != protocol.CodeStructuredUnsupported {
		t.Fatalf("send with no sink = code %q err %v, want structured_unsupported. TODAY this path "+
			"writes the text and a CR into the PTY for EVERY provider, which is the live defect", code, err)
	}
	r.assertPTYUntouched(t)
}

// TestR7ComposerSink_AKeystrokeAdapterStillUsesThePTY is the Claude arm, unchanged. The seam
// exists so ABSENCE refuses; PRESENCE must still work exactly as it does at HEAD, or R7 has
// broken the provider it was not touching.
func TestR7ComposerSink_AKeystrokeAdapterStillUsesThePTY(t *testing.T) {
	r := newR7ComposerRig(t, false)
	r.sk.setAdapterForTest(func(string) (adapter.Adapter, bool) {
		return &r7KeystrokeAdapter{Adapter: newPlainAdapter().Adapter}, true
	})

	code, err := r.send(t, "", "ship it", "devA:01JKEYS0000000000000000")
	if err != nil || code != "" {
		t.Fatalf("send to a KEYSTROKE-composer session refused: code %q err %v", code, err)
	}
	ok, drained := awaitFrames(r.att, "got:", 20*time.Second)
	if !ok || !strings.Contains(drained, "ship it") {
		t.Errorf("the session's stdin reported %q, want the sent text; the Claude path is UNCHANGED "+
			"by R7 and a seam that breaks it is a regression, not a fence", drained)
	}
}

// r7KeystrokeAdapter is the Claude shape: it PROVES the keystroke composer seam.
type r7KeystrokeAdapter struct{ adapter.Adapter }

func (r7KeystrokeAdapter) ComposerKeys(text string) []byte { return []byte(text) }

// ---------------------------------------------------------------------------
// The echo correlation
// ---------------------------------------------------------------------------

// TestR7ComposerSink_TheBackendBranchCorrelatesTheEchoEXACTLYAndNeverByText is the one place
// R7 does better than the mechanism it inherits, and the reason it must: chat.go:52-70 records
// a PROBED mis-attribution -- an OWNER-typed "yes" stamped source=phone carrying the phone's
// operation_id, because a phone send of "yes" was still pending. Claude cannot do better (its
// UserPromptSubmit hook carries no injection id). THE BACKEND BRANCH CAN.
//
// TurnStartParams and TurnSteerParams both carry an optional `clientUserMessageId`, and the
// userMessage ThreadItem carries `clientId` (SCHEMA-DERIVED, r7-PROVENANCE.md item 4). The
// daemon mints the id, sends it, and reads it straight back. R7 does not carry a known
// attribution defect onto a new provider.
func TestR7ComposerSink_TheBackendBranchCorrelatesTheEchoEXACTLYAndNeverByText(t *testing.T) {
	r := newR7ComposerRig(t, true)
	const opID = "devA:01JECHO0000000000000000"

	if _, err := r.send(t, "", "yes", opID); err != nil {
		t.Fatalf("send: %v", err)
	}
	params := r7CallParams(t, r.backend, "turn/start")
	clientID, _ := params["clientUserMessageId"].(string)
	if clientID == "" {
		t.Fatal("turn/start carried no clientUserMessageId; without it the backend branch falls back " +
			"to matching on TEXT, and short strings are exactly the ones two parties type identically " +
			"(\"yes\", \"y\", \"continue\") -- the probed collision is not exotic")
	}

	// The OWNER types the same word at the terminal FIRST. It echoes with clientId null, so it
	// must keep the adapter's honest owner attribution.
	r.sk.ingestBackendFrame(r.local, []byte(
		`{"method":"item/started","params":{"item":{"type":"userMessage","id":"um-owner","clientId":null,`+
			`"content":[{"type":"text","text":"yes","text_elements":[]}]},"threadId":"t","turnId":"turn-o","startedAtMs":1}}`),
		time.Now().UnixMilli())
	// Then the PHONE's own send echoes, carrying the id the daemon minted.
	r.sk.ingestBackendFrame(r.local, []byte(
		`{"method":"item/started","params":{"item":{"type":"userMessage","id":"um-phone","clientId":`+
			jsonString(clientID)+`,"content":[{"type":"text","text":"yes","text_elements":[]}]},`+
			`"threadId":"t","turnId":"turn-p","startedAtMs":2}}`),
		time.Now().UnixMilli())
	r.sk.flushBackendFrames(r.local)

	items := awaitItems(t, r.sk, r.local, 2)
	var owner, phone map[string]any
	for _, it := range items {
		if it["kind"] != adapter.KindUserMessage || it["text"] != "yes" {
			continue
		}
		if it["source"] == adapter.SourcePhone {
			phone = it
		} else {
			owner = it
		}
	}
	if owner == nil {
		t.Error("the OWNER's identical prompt was stamped source=phone. That is the recorded " +
			"mis-attribution, reproduced on a provider that had the information to avoid it")
	}
	if phone == nil {
		t.Fatalf("the phone's own echo was never attributed; items: %v", items)
	}
	if phone["operation_id"] != opID {
		t.Errorf("the phone's echo carries operation_id %v, want %q", phone["operation_id"], opID)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ---------------------------------------------------------------------------
// turn_interrupt
// ---------------------------------------------------------------------------

// TestR7Interrupt_ABackendSessionDispatchesTurnInterruptAndTypesNOTHING. RECORDED:
// turn/interrupt returns {} (turn-interrupt.json) and the server immediately emits
// turn/completed with "status": "interrupted" for that exact turn id
// (turn-completed-interrupted.json). The TUI displayed the interruption -- again with no
// keystroke sent to the terminal.
func TestR7Interrupt_ABackendSessionDispatchesTurnInterruptAndTypesNOTHING(t *testing.T) {
	r := newR7ComposerRig(t, true)
	turn := r7OpenNativeTurn(t, r)

	code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID, "devA:01JSTOP0000000000000000",
		protocol.TurnInterruptReq{Session: r.session, ExpectedTurn: turn})
	if err != nil || code != "" {
		t.Fatalf("turn_interrupt on a backend session refused: code %q err %v. Today the codex "+
			"adapter proves no TurnInterrupter, so this refuses interrupt_unsupported -- correct at "+
			"HEAD and wrong once the RPC exists", code, err)
	}
	params := r7CallParams(t, r.backend, "turn/interrupt")
	// The RECORDED {threadId, turnId}, BY VALUE. Round 1 asserted only that both keys were
	// present, which is what let a Stop naming a turn the server never minted pass as a fence.
	if params["threadId"] != r7NativeThreadID {
		t.Errorf("turn/interrupt threadId = %v, want %q", params["threadId"], r7NativeThreadID)
	}
	if got, _ := params["turnId"].(string); got != r7NativeTurnID {
		t.Errorf("turn/interrupt turnId = %q, want the CLI's OWN %q", got, r7NativeTurnID)
	}
	r.assertPTYUntouched(t)
}

// TestR7Interrupt_NoActiveTurnIsBENIGNAndNeverAnErrorSurface. `turn/interrupt` on an
// already-finished turn returns {"code":-32600,"message":"no active turn to interrupt"}
// (RECORDED: errors-observed.json). The daemon's own stale_turn precondition already refuses
// that case BEFORE the RPC is sent; when the race is lost anyway, the error must not reach the
// owner as a failure.
func TestR7Interrupt_NoActiveTurnIsBENIGNAndNeverAnErrorSurface(t *testing.T) {
	r := newR7ComposerRig(t, true)
	r.backend.callErr["turn/interrupt"] = &appserver.RPCError{Code: -32600, Message: "no active turn to interrupt"}

	// A turn the CLI DID name, so the RPC is genuinely sent and its answer is genuinely the
	// server's. Without a native id the interrupt is now refused BEFORE the RPC (review
	// BLOCKING 1), and a benign-swallow test that never sends is no test of the swallow.
	turn := r7OpenNativeTurn(t, r)

	code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID, "devA:01JBENIGN00000000000000",
		protocol.TurnInterruptReq{Session: r.session, ExpectedTurn: turn})
	if code != "" || err != nil {
		t.Errorf("`no active turn to interrupt` surfaced as code %q err %v; the turn the owner "+
			"wanted stopped is stopped, and reporting a failure teaches them to press Stop again",
			code, err)
	}
	var rpcErr *appserver.RPCError
	if errors.As(err, &rpcErr) {
		t.Errorf("the RPC error leaked to the caller verbatim: %v", rpcErr)
	}
	r.assertPTYUntouched(t)
}

// TestR7Interrupt_ANoBackendCodexSessionREFUSESRatherThanTypingACancelKey is the same
// three-branch shape as the composer: backend -> RPC; else AsTurnInterrupter; else
// interrupt_unsupported, having typed NOTHING.
func TestR7Interrupt_ANoBackendCodexSessionREFUSESRatherThanTypingACancelKey(t *testing.T) {
	r := newR7ComposerRig(t, false)
	r.adapter.items = []adapter.Interaction{{
		Kind: adapter.KindUserMessage, Text: "write an essay", Source: adapter.SourceOwner, Ref: "um-1",
	}}
	turn := r6OpenTurn(t, r.sk, r.local, "write an essay", len(interactionItems(t, r.sk, r.local)))

	code, _ := r.sk.api.InterruptTurn(r.sk.api.endpointID, "devA:01JNOINT000000000000000",
		protocol.TurnInterruptReq{Session: r.session, ExpectedTurn: turn})
	if code != protocol.CodeInterruptUnsupported {
		t.Errorf("interrupt with no backend and no seam = %q, want interrupt_unsupported", code)
	}
	r.assertPTYUntouched(t)
}
