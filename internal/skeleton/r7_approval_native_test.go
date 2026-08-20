package skeleton

// FAILING-FIRST (TDD RED, GG-5) for Mirror M4.3: NATIVE approvals. The phone's Approve answers
// the server-initiated approvalRequest BY RPC; first-answer-wins is SERVER-SIDE; NO KEYSTROKE
// INJECTION ON CODEX, EVER. Bead: agents-tracker-hggx.8. ADR-013 §R7.5, playbook §8.2.
//
// WHAT approveInteraction DOES TODAY, and why Codex is only accidentally safe. It falls
// straight through to applyDecision -> dialogTap -> ApprovalKeys -> PTY (approval.go:538,
// inject.go:73-124). Codex is saved from being typed at ONLY because AsApprovalApplier is
// false and the whole thing refuses errNoApplier. R7 branches on the BACKEND FIRST and calls
// InteractionSource.Decision(ref, decisionID) -- which HAS NO PRODUCTION CALLER ANYWHERE IN
// THE REPO TODAY, a fact worth stating plainly -- and writes its DecisionAction.Reply as the
// JSON-RPC response.
//
// TWO PROPERTIES FALL OUT AND BOTH ARE RECORDED:
//
//   - the reply must go out on THE DAEMON'S OWN CONNECTION with THE ID THAT CONNECTION
//     RECEIVED (JSON-RPC ids are per-connection); the pending request is matched by
//     params.itemId, which approval-request.json carries;
//   - resolution still arrives only BY OBSERVATION -- here as the server's own
//     `serverRequest/resolved` broadcast, which is strictly better evidence than the grid
//     observation decision 3 step 3 settles for on Claude.
//
// THE CONTRACT these tests freeze:
//
//	type backendConn interface {
//	    Call(ctx context.Context, method string, params, out any) error
//	    Respond(ctx context.Context, id json.RawMessage, result any) error
//	}
//	func (d *Daemon) registerBackend(local, threadID string, conn backendConn)
//	func (d *Daemon) noteServerRequest(local, itemRef string, id json.RawMessage)

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/protocol"
)

// ---------------------------------------------------------------------------
// A backend double that RECORDS every RPC, so "nothing was typed" and "the RPC went out"
// are both observations rather than inferences.
// ---------------------------------------------------------------------------

type r7Call struct {
	Method string
	Params json.RawMessage
}

type r7FakeBackend struct {
	mu        sync.Mutex
	calls     []r7Call
	responses []struct {
		ID     json.RawMessage
		Result json.RawMessage
	}
	callErr map[string]error
	reply   map[string]json.RawMessage
	closed  int
}

func newR7FakeBackend() *r7FakeBackend {
	return &r7FakeBackend{callErr: map[string]error{}, reply: map[string]json.RawMessage{}}
}

func (b *r7FakeBackend) Call(_ context.Context, method string, params, out any) error {
	body, _ := json.Marshal(params)
	b.mu.Lock()
	b.calls = append(b.calls, r7Call{Method: method, Params: body})
	err := b.callErr[method]
	rep := b.reply[method]
	b.mu.Unlock()
	if err != nil {
		return err
	}
	if out != nil && len(rep) > 0 {
		return json.Unmarshal(rep, out)
	}
	return nil
}

// Close records that the daemon released the connection. It is part of backendConn because a
// registration the daemon drops while leaving the socket open is a read loop and an fd nobody
// owns (review MEDIUM 4).
func (b *r7FakeBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed++
	return nil
}

// closes reports how many times the daemon closed this connection.
func (b *r7FakeBackend) closes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// recorded returns a copy of every RPC this connection received.
func (b *r7FakeBackend) recorded() []r7Call {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]r7Call(nil), b.calls...)
}

func (b *r7FakeBackend) Respond(_ context.Context, id json.RawMessage, result any) error {
	body, _ := json.Marshal(result)
	b.mu.Lock()
	b.responses = append(b.responses, struct {
		ID     json.RawMessage
		Result json.RawMessage
	}{append(json.RawMessage(nil), id...), body})
	b.mu.Unlock()
	return nil
}

func (b *r7FakeBackend) lastResponse() (json.RawMessage, json.RawMessage, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.responses) == 0 {
		return nil, nil, false
	}
	r := b.responses[len(b.responses)-1]
	return r.ID, r.Result, true
}

// r7CodexAdapter is the Codex shape: an InteractionSource with a native Decision, and NO
// ApprovalApplier, NO TurnInterrupter and NO KeystrokeComposer. That absence is the point.
type r7CodexAdapter struct {
	adapter.Adapter
	items []adapter.Interaction
}

// Interactions returns the canned items when a test set them, and otherwise SHAPES THE REAL
// FRAME with the real Codex adapter.
//
// The delegation is not a convenience: TestR7ComposerSink_TheBackendBranchCorrelatesTheEcho...
// drives two RECORDED item/started frames that differ ONLY in `clientId`, and a canned list
// cannot carry a field it does not read out of the frame -- so with `items` nil that test
// could only ever have journalled nothing. Delegating drives the shipped shaper, which is
// strictly stronger than any double.
func (a *r7CodexAdapter) Interactions(p adapter.HookPayload) []adapter.Interaction {
	if a.items != nil {
		return a.items
	}
	src, ok := adapter.AsInteractionSource(codex.New())
	if !ok {
		return nil
	}
	return src.Interactions(p)
}

func (a *r7CodexAdapter) Decision(ref, decisionID string) (adapter.DecisionAction, bool) {
	switch decisionID {
	case "accept", "acceptForSession", "decline", "cancel":
		body, _ := json.Marshal(map[string]string{"decision": decisionID})
		return adapter.DecisionAction{Reply: body}, true
	}
	return adapter.DecisionAction{}, false
}

func (a *r7CodexAdapter) Backend(spec adapter.BackendSpec) (adapter.BackendPlan, bool) {
	return adapter.BackendPlan{
		Program:   "codex",
		Args:      []string{"app-server", "--listen", "unix://" + spec.SocketPath},
		AgentArgs: []string{"--remote", "unix://" + spec.SocketPath},
	}, true
}

// r7StdinScript is the fake CLI's script for every R7 rig that OBSERVES the session's stdin.
//
// `ask` is the only directive that reads stdin and reports what it read ("got: <line>"), which
// is what turns "nothing was typed" into an OBSERVATION rather than an inference -- the same
// mechanism approval_inject_test.go's readBack already uses. A script with no `ask` can never
// report, and one whose first directive is unknown (e.g. a bare `sleep 60`) makes the fake
// agent exit before the session is even usable.
const r7StdinScript = "ask ?\nask ?\nask ?\nask ?\nask ?\nask ?\nidle 600s\n"

// r7CodexApproval is the pending card, shaped from the RECORDED approval-request.json's own
// itemId and the generated FileChangeApprovalDecision union.
func r7CodexApproval() adapter.Interaction {
	return adapter.Interaction{
		Kind: adapter.KindApprovalRequest, Status: adapter.StatusInProgress,
		Ref:     "exec-29bcdedd-84f6-423c-931d-0f0433cc3328",
		Mode:    adapter.ModeCard,
		Summary: "Apply the following edits to ws/hello.txt",
		Action:  adapter.ToolAction{Type: "write", Path: "ws/hello.txt"},
		Decisions: []adapter.DecisionChoice{
			{ID: "accept", Label: "Yes, proceed", Verdict: adapter.VerdictAllow},
			{ID: "acceptForSession", Label: "Yes, and don't ask again", Verdict: adapter.VerdictAllow},
			{ID: "decline", Label: "No, keep going", Verdict: adapter.VerdictDeny},
			{ID: "cancel", Label: "No, and stop", Verdict: adapter.VerdictDeny},
		},
	}
}

// r7BackendRig is a Codex-shaped session with a live backend, one pending card, and an owner
// attachment already drained past the repaint -- so any keystroke the daemon types after this
// point is OBSERVABLE on the fake CLI's stdin.
type r7BackendRig struct {
	*injectRig
	backend *r7FakeBackend
	rpcID   json.RawMessage
}

func newR7BackendRig(t *testing.T) *r7BackendRig {
	t.Helper()
	ad := &r7CodexAdapter{Adapter: newPlainAdapter().Adapter, items: []adapter.Interaction{r7CodexApproval()}}
	sk := assemble(t)
	sk.adapterFor = func(string) (adapter.Adapter, bool) { return ad, true }

	m := launchFake(t, sk, r7StdinScript)
	local := m.ID
	session := protocol.NamespacedID(sk.api.endpointID, local)

	oc := dialClient(t, sk)
	att, err := oc.Attach(session)
	if err != nil {
		t.Fatalf("owner attach: %v", err)
	}
	t.Cleanup(func() { _ = att.Detach() })

	backend := newR7FakeBackend()
	sk.registerBackend(local, "01a00335-9a50-79e2-8253-e08861d67c4d", backend)

	rpcID := json.RawMessage(`0`)
	sk.noteServerRequest(local, "exec-29bcdedd-84f6-423c-931d-0f0433cc3328", rpcID)

	item := openApprovalFrom(t, sk, local, r7CodexApproval())
	return &r7BackendRig{
		injectRig: &injectRig{sk: sk, local: local, session: session, att: att, item: item},
		backend:   backend,
		rpcID:     rpcID,
	}
}

// r7Approve builds the approve request the phone would send, echoing the card's own fields.
func r7Approve(t *testing.T, r *r7BackendRig, decision string) (protocol.ErrorCode, error) {
	t.Helper()
	m, ok := r.sk.core.Get(r.local)
	if !ok {
		t.Fatalf("session %s is gone", r.local)
	}
	expires, err := time.Parse(time.RFC3339Nano, itemString(t, r.item, "expires_at"))
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	return r.sk.api.ApproveInteraction(r.sk.api.endpointID, "devA:01JAPPROVE0000000000000000", protocol.ApproveReq{
		Session:       r.session,
		InteractionID: itemString(t, r.item, "item_id"),
		Decision:      decision,
		ContentHash:   itemString(t, r.item, "content_hash"),
		ExpiresAt:     &expires,
		AgentInstance: protocol.AgentInstanceRef{ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime},
	})
}

// ---------------------------------------------------------------------------

// TestR7NativeApproval_ThePhonesApproveGoesOutAsAJSONRPCReplyAndNOTHINGIsTyped is M4.3's
// headline, and it is TWO assertions that must both hold: the RPC went out, AND the session's
// stdin is untouched. R1 leg 4 recorded exactly this against the real CLI -- "NO KEY WAS EVER
// PRESSED IN THE TUI, yet the TUI's approval dialog closed" (r1-codex-gate.md:130-134).
func TestR7NativeApproval_ThePhonesApproveGoesOutAsAJSONRPCReplyAndNOTHINGIsTyped(t *testing.T) {
	r := newR7BackendRig(t)

	code, err := r7Approve(t, r, "accept")
	if err != nil || code != "" {
		t.Fatalf("approve on a live-backend Codex session refused: code %q err %v. Today this path "+
			"falls through to ApprovalKeys and refuses errNoApplier, which is the ACCIDENT that has "+
			"kept Codex from being typed at", code, err)
	}

	id, result, ok := r.backend.lastResponse()
	if !ok {
		t.Fatal("no JSON-RPC response was sent for the approval; the phone's tap resolved nothing")
	}
	if string(id) != string(r.rpcID) {
		t.Errorf("the reply carries id %s, want %s -- the id THAT CONNECTION received. JSON-RPC "+
			"ids are per-connection, so a reply with a different id answers nothing and the agent "+
			"stays blocked", id, r.rpcID)
	}
	var body struct {
		Decision string `json:"decision"`
	}
	if json.Unmarshal(result, &body) != nil || body.Decision != "accept" {
		t.Errorf("the reply body is %s, want {\"decision\":\"accept\"} -- the CLI's OWN vocabulary "+
			"(§3.5), which the adapter produced through Decision()", result)
	}

	// The stdin fence. This is the whole of playbook §8.2 for this op.
	r.assertNothingWasTyped(t)
}

// TestR7NativeApproval_EveryOfferedDecisionIsApplicableWalksTheWholeRecordedUnion. A card that
// offers four buttons and can apply two is a card that lies.
func TestR7NativeApproval_EveryOfferedDecisionIsApplicable(t *testing.T) {
	for _, decision := range []string{"accept", "acceptForSession", "decline", "cancel"} {
		t.Run(decision, func(t *testing.T) {
			r := newR7BackendRig(t)
			code, err := r7Approve(t, r, decision)
			if err != nil || code != "" {
				t.Fatalf("approve %q refused: code %q err %v", decision, code, err)
			}
			_, result, ok := r.backend.lastResponse()
			if !ok {
				t.Fatalf("approve %q sent no reply", decision)
			}
			if !strings.Contains(string(result), decision) {
				t.Errorf("approve %q replied %s", decision, result)
			}
			r.assertNothingWasTyped(t)
		})
	}
}

// TestR7NativeApproval_ADecisionOutsideTheCLIsUnionIsREFUSEDAndNothingIsSent. The daemon may
// not invent a decision id: a reply the server rejects leaves the approval pending while the
// phone believes it answered, which is worse than a refusal the phone can see.
func TestR7NativeApproval_ADecisionOutsideTheCLIsUnionIsREFUSEDAndNothingIsSent(t *testing.T) {
	r := newR7BackendRig(t)

	code, _ := r7Approve(t, r, "yes")
	if code != protocol.CodeInvalidField {
		t.Errorf("approve with an unoffered decision = %q, want invalid_field", code)
	}
	if _, _, ok := r.backend.lastResponse(); ok {
		t.Error("a REFUSED approve still sent a JSON-RPC reply")
	}
	r.assertNothingWasTyped(t)
}

// TestR7NativeApproval_ServerRequestResolvedRetiresTheCardWithoutTheDaemonGuessing is the
// FIRST-ANSWER-WINS property, and the point is that the daemon does NOT arbitrate. When the
// OWNER answers at the terminal, the server broadcasts `serverRequest/resolved` (RECORDED:
// frame-samples.json) and that broadcast -- not a grid observation, not a timer -- is what
// retires the phone's card. This is strictly better evidence than Claude's path has.
func TestR7NativeApproval_ServerRequestResolvedRetiresTheCardWithoutTheDaemonGuessing(t *testing.T) {
	r := newR7BackendRig(t)

	// The owner answered at the terminal. The server tells every attached client.
	r.sk.ingestBackendFrame(r.local, []byte(
		`{"method":"serverRequest/resolved","params":{"threadId":"01a00335-9a50-79e2-8253-e08861d67c4d","requestId":0},"emittedAtMs":1786760261774}`),
		time.Now().UnixMilli())
	r.sk.flushBackendFrames(r.local)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if code, _ := r7Approve(t, r, "accept"); code == protocol.CodeStaleApproval {
			if _, _, ok := r.backend.lastResponse(); ok {
				t.Error("the daemon answered a request the SERVER had already resolved; first-answer-" +
					"wins is server-side and a second reply is at best ignored and at worst applied " +
					"to whatever replaced it")
			}
			r.assertNothingWasTyped(t)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the card was still pending after serverRequest/resolved; the phone would show a live " +
		"approve button for a request that is over, and tapping it does nothing forever")
}

// TestR7NativeApproval_ADeadBackendREFUSESRatherThanFallingBackToAKeystroke is the branch order
// that makes §8.2 structural. If the daemon fell back to the keystroke seam when the RPC
// channel is gone, a Codex approval would be typed into the TUI on exactly the day the backend
// crashed -- the worst possible day for it.
func TestR7NativeApproval_ADeadBackendREFUSESRatherThanFallingBackToAKeystroke(t *testing.T) {
	r := newR7BackendRig(t)
	r.sk.forgetBackend(r.local) // the app-server died (§R7.6)

	code, err := r7Approve(t, r, "accept")
	if code == "" && err == nil {
		t.Fatal("approve on a Codex session with a DEAD backend reported success; whatever it did, " +
			"it was not answering the server")
	}
	if _, _, ok := r.backend.lastResponse(); ok {
		t.Error("a reply was sent on a connection the daemon had forgotten")
	}
	r.assertNothingWasTyped(t)
}
