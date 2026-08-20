package protocol

// FAILING-FIRST (TDD RED, GG-5) for Wave R6 review findings B2 and B7.
//
// B2 (BLOCKER, security, orchestrator-confirmed). The two UNSIGNED M3 reads honored NEITHER
// the kill switch NOR capability negotiation, while the precedent they cited in their own
// header comment honored both. handleJournalRead does BOTH -- cc.journalBackend()'s
// capability check AND `if cc.srv.remoteTier && cc.srv.remoteControlDisabled()` ->
// CodeKillSwitch (remote tier ONLY; the owner tier shares the coreAPI and must never be
// gated). handleInteractionHistory and handleInteractionDetail called their seams directly.
//
// PROBED: with the kill switch OFF, journal_read refused while interaction_history served
// {"item_id":"01JA","kind":"user_message","text":"SECRET PROMPT"} and interaction_detail
// served the full output_excerpt. With NO capability negotiated, journal_read refused and
// interaction_history still served. `off` means off for every read of this plane, or `off`
// means nothing.
//
// B7 (MAJOR). turn_interrupt carried no turn coordinate at all, so a Stop rendered against
// turnA typed the cancel sequence into whatever turn was current when it ARRIVED. These
// fences pin the wire half: the body is required, its session must agree with the command's,
// its expected_turn must be present, and the tuple's content hash binds it.

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// r6FixGateBackend is a remote-tier backend that serves the two M3 reads AND owns a
// toggleable kill switch, so one rig can drive both halves of finding B2.
type r6FixGateBackend struct {
	*r6HistoryBackend
	enabled bool
}

func newR6FixGateBackend() *r6FixGateBackend {
	b := &r6FixGateBackend{r6HistoryBackend: newR6HistoryBackend(), enabled: true}
	b.history = []JournalRecord{{
		Cursor: 10, SessionID: "sess1", Type: "interaction",
		Item: json.RawMessage(`{"v":1,"item_id":"01JA","kind":"user_message","text":"SECRET PROMPT"}`),
	}}
	b.detail = json.RawMessage(`{"v":1,"item_id":"01JA","kind":"tool_run","output_excerpt":"SECRET OUTPUT"}`)
	return b
}

func (b *r6FixGateBackend) RemoteControlEnabled() bool { return b.enabled }

// JournalReadFrom makes the backend a JournalBackend too, so the CONTROL case -- journal_read
// under the same conditions -- can be asserted in the same test. Without it the "journal_read
// refuses, the M3 read serves" asymmetry could not be shown on one connection.
func (b *r6FixGateBackend) JournalReadFrom(uint64) (JournalResume, error) {
	return JournalResume{Cursor: 1}, nil
}
func (b *r6FixGateBackend) JournalSubscribe() (<-chan JournalRecord, func()) {
	ch := make(chan JournalRecord)
	return ch, func() { close(ch) }
}

var _ JournalBackend = (*r6FixGateBackend)(nil)

// TestR6Fix_TheM3ReadsHonorTheKillSwitchExactlyAsJournalReadDoes is B2's first half, frozen
// as the probe ran it: journal_read is asserted in the SAME test, so the fence can never pass
// by making both reads leak.
func TestR6Fix_TheM3ReadsHonorTheKillSwitchExactlyAsJournalReadDoes(t *testing.T) {
	b := newR6FixGateBackend()
	b.enabled = false // the owner has turned remote control off
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway, CapJournal})
	sid := rep.EndpointID + "/sess1"

	// The control: journal_read refuses. If this ever stops refusing, the comparison below
	// is meaningless and this test says so first.
	rc.writeControl(Control{Op: OpJournalRead, EndpointID: rep.EndpointID, Cursor: 0})
	if got := rc.readControl(); got.Op != OpError || got.ErrorCode != CodeKillSwitch {
		t.Fatalf("CONTROL BROKEN: journal_read with the kill switch off = op %q code %q, want an "+
			"error with %q; the asymmetry this test measures cannot be measured", got.Op, got.ErrorCode, CodeKillSwitch)
	}

	rc.writeControl(Control{Op: OpInteractionHistory, EndpointID: rep.EndpointID, SessionID: sid,
		History: &InteractionHistoryReq{Session: sid, BeforeItem: "01JC", Limit: 5}})
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeKillSwitch {
		t.Errorf("interaction_history with the kill switch OFF = op %q code %q carrying %d records; "+
			"want the same %q journal_read gives. This served a user_message's text verbatim to a "+
			"phone the owner had just cut off.", got.Op, got.ErrorCode, len(got.Journal), CodeKillSwitch)
	}
	if len(got.Journal) != 0 {
		t.Errorf("the refused history read carried %d journal records; a refusal must carry no "+
			"content at all", len(got.Journal))
	}

	rc.writeControl(Control{Op: OpInteractionDetail, EndpointID: rep.EndpointID, SessionID: sid,
		Detail: &InteractionDetailReq{Session: sid, ItemID: "01JA"}})
	got = rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeKillSwitch {
		t.Errorf("interaction_detail with the kill switch OFF = op %q code %q carrying %d records; "+
			"want %q. This served a full pre-truncation output body to a cut-off phone.",
			got.Op, got.ErrorCode, len(got.Journal), CodeKillSwitch)
	}
	if len(got.Journal) != 0 {
		t.Errorf("the refused detail read carried %d journal records; want none", len(got.Journal))
	}
}

// TestR6Fix_TheM3ReadsRequireTheNegotiatedJournalCapability is B2's second half. The reads
// answer WITH journal records ON the journal carrier, so the plane they read is the one the
// `journal` capability names; serving them to a connection that never negotiated it is a read
// of a plane the peer did not ask for and the daemon did not agree to.
func TestR6Fix_TheM3ReadsRequireTheNegotiatedJournalCapability(t *testing.T) {
	b := newR6FixGateBackend()
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	// Deliberately NOT negotiating CapJournal.
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpJournalRead, EndpointID: rep.EndpointID, Cursor: 0})
	if got := rc.readControl(); got.Op != OpError {
		t.Fatalf("CONTROL BROKEN: journal_read without the capability = op %q; want an error", got.Op)
	}

	for _, tc := range []struct {
		name string
		ctrl Control
	}{
		{"interaction_history", Control{Op: OpInteractionHistory, EndpointID: rep.EndpointID, SessionID: sid,
			History: &InteractionHistoryReq{Session: sid, BeforeItem: "01JC", Limit: 5}}},
		{"interaction_detail", Control{Op: OpInteractionDetail, EndpointID: rep.EndpointID, SessionID: sid,
			Detail: &InteractionDetailReq{Session: sid, ItemID: "01JA"}}},
	} {
		rc.writeControl(tc.ctrl)
		got := rc.readControl()
		if got.Op != OpError {
			t.Errorf("%s with NO capability negotiated = op %q carrying %d records; want the same "+
				"refusal journal_read gives", tc.name, got.Op, len(got.Journal))
		}
	}
}

// TestR6Fix_TheOwnerTierIsNeverGatedByTheRemoteKillSwitch is finding B's rule (the one
// handleJournalRead already carried) extended to the two new reads: the owner tier shares the
// KillSwitch-implementing coreAPI, so gating it would break the local user's own transcript
// whenever they turned REMOTE control off.
func TestR6Fix_TheOwnerTierIsNeverGatedByTheRemoteKillSwitch(t *testing.T) {
	b := newR6FixGateBackend()
	b.enabled = false
	sock := tmpSock(t)
	srv, err := Serve(b, sock) // OWNER tier
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapJournal})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(Control{Op: OpInteractionHistory, EndpointID: rep.EndpointID, SessionID: sid,
		History: &InteractionHistoryReq{Session: sid, BeforeItem: "01JC", Limit: 5}})
	if got := rc.readControl(); got.ErrorCode == CodeKillSwitch {
		t.Errorf("owner-tier interaction_history was refused %q; the remote kill switch gates the "+
			"REMOTE tier only -- gating the owner would take the local user's own history away when "+
			"they cut off their phone", got.ErrorCode)
	}
}

// ---- B7: turn_interrupt names the turn it stops -----------------------------------------

// TestR6Fix_TurnInterruptRefusesABodylessFrame: the op stopped being bodyless, and a frame
// that lost its body must be refused rather than applied to whichever turn is current.
func TestR6Fix_TurnInterruptRefusesABodylessFrame(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	frame := r6InterruptFrame(rep, "devA:01JNOBODY", sid)
	frame.TurnInterrupt = nil
	rc.writeControl(frame)
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("bodyless turn_interrupt = op %q code %q, want an error with %q: a Stop that names "+
			"no turn lands its cancel key wherever the conversation happens to be", got.Op, got.ErrorCode, CodeInvalidField)
	}
}

// TestR6Fix_TurnInterruptRefusesAnEmptyExpectedTurn: there is deliberately no spelling of
// "interrupt whatever is running". That spelling is what let a late Stop land the cancel key
// at an IDLE prompt, where the Claude adapter's own note records it clears the terminal
// user's half-typed composer line (playbook §8.1's last step).
func TestR6Fix_TurnInterruptRefusesAnEmptyExpectedTurn(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	rc.writeControl(r6InterruptFrameTurn(rep, "devA:01JNOTURN", sid, ""))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("turn_interrupt with an empty expected_turn = op %q code %q, want an error with %q",
			got.Op, got.ErrorCode, CodeInvalidField)
	}
}

// TestR6Fix_TurnInterruptRefusesABodyNamingAnotherSession is handleComposerSend's collision
// rule, inherited: two session coordinates free to differ would let a gateway point a
// signature authorized for one session's Stop at another session's PTY.
func TestR6Fix_TurnInterruptRefusesABodyNamingAnotherSession(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemote(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	frame := r6InterruptFrame(rep, "devA:01JXSESS", sid)
	frame.TurnInterrupt = &TurnInterruptReq{Session: rep.EndpointID + "/other", ExpectedTurn: "01JTURN"}
	rc.writeControl(frame)
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodeInvalidField {
		t.Fatalf("turn_interrupt whose body names another session = op %q code %q, want an error with %q",
			got.Op, got.ErrorCode, CodeInvalidField)
	}
}

// TestR6Fix_TurnInterruptBindsItsBodyIntoTheSignedContentSlot: the whole reason no new crypto
// is needed. The tuple's existing content slot carries TurnInterruptContentHash over
// (session, expected_turn), so a gateway that re-points a valid signature at a different turn
// breaks it -- the arrangement composer_send already had.
func TestR6Fix_TurnInterruptBindsItsBodyIntoTheSignedContentSlot(t *testing.T) {
	a := schema.TurnInterruptContentHash(&TurnInterruptReq{Session: "m/s1", ExpectedTurn: "01JA"})
	b := schema.TurnInterruptContentHash(&TurnInterruptReq{Session: "m/s1", ExpectedTurn: "01JB"})
	if len(a) != 32 {
		t.Fatalf("content hash is %d bytes, want 32", len(a))
	}
	if string(a) == string(b) {
		t.Fatal("two different expected_turns hash identically: the signature would not bind the " +
			"turn at all, and a gateway could re-point a valid Stop at any turn it liked")
	}
	if s := schema.TurnInterruptContentHash(&TurnInterruptReq{Session: "m/other", ExpectedTurn: "01JA"}); string(s) == string(a) {
		t.Fatal("two different sessions hash identically")
	}
	if schema.TurnInterruptContentHash(nil) == nil {
		t.Fatal("a nil body must hash as the zero body, never panic at the choke point")
	}
}
