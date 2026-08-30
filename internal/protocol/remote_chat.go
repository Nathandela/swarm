package protocol

// Wave R6 (bead agents-tracker-hggx.7, Mirror M2.4 / M3.1 / M3.3, playbook §8.1): the REAL
// composer_send / turn_interrupt handlers -- replacing the Wave R1 op_not_implemented stub
// for exactly these two ops, as R5 did for session_launch/operation_status -- plus the two
// M3 read ops, interaction_history and interaction_detail.
//
// The two MUTATING ops run requireRemoteAuthz FIRST, at the SAME choke point kill/delete/
// launch/approve/session_launch ride, then the R1 body-version gate, then their structural
// checks, then the optional backend seam. A backend without the seam refuses loudly rather
// than replying OK to a send nothing delivered or a Stop nothing stopped.
//
// The two READ ops are IS-CAP-2's ActionTerminalWatch precedent: gateway-routed, never
// forwarded to the device authenticator, no device fields required, no new device-signed
// action -- PB-SYNC-5's actionClass switch stays closed.
//
// THE GATE THIS COMMENT ONCE CLAIMED DID NOT EXIST (Wave R6 review finding B2, corrected
// here). It read "Gating (capability, kill switch) is daemon-side, behind the seams", and it
// was false in both halves: the two handlers called their seams directly, so with the kill
// switch OFF interaction_history served a user_message's text verbatim while journal_read --
// the very precedent cited -- refused, and with NO capability negotiated interaction_history
// still served. The gate is now IN THE HANDLER, where handleJournalRead's is, and it is the
// SAME code (requireJournalPlaneRead): the negotiated `journal` capability, then the
// remote-tier kill switch. It is not behind the seams, because a seam is free to be absent
// and a gate is not.

import (
	"encoding/json"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// The chat wire types and refusal codes, aliased from the daemon-free schema package for
// PB-BIND-0's reason (see types.go).
type (
	ComposerSendReq       = schema.ComposerSendReq
	TurnInterruptReq      = schema.TurnInterruptReq
	InteractionHistoryReq = schema.InteractionHistoryReq
	InteractionDetailReq  = schema.InteractionDetailReq
)

const (
	CodeStaleTurn             = schema.CodeStaleTurn
	CodeInterruptUnsupported  = schema.CodeInterruptUnsupported
	CodeStructuredUnsupported = schema.CodeStructuredUnsupported
	CodeInputBusy             = schema.CodeInputBusy
	CodeUnavailable           = schema.CodeUnavailable
)

// maxSendInputText is send_input's own text ceiling, restated under its historical spelling:
// the composer rides the same PTY write path, so it answers to the same bound, and a silent
// truncation would submit a DIFFERENT message than the one the user signed.
const maxSendInputText = MaxSendInputText

// ComposerSender is the optional DaemonAPI seam handleComposerSend dispatches to, mirroring
// InteractionApprover: the daemon-side application of one composer_send (the expected_turn
// precondition, the PTY write with submit framing, and the injection-time attribution),
// returning a D10 ErrorCode beside the error so refusals surface verbatim.
type ComposerSender interface {
	ComposerSend(machine, operationID string, req ComposerSendReq) (ErrorCode, error)
}

// TurnInterrupter is the optional DaemonAPI seam handleTurnInterrupt dispatches to: the
// daemon-side semantic interrupt of ONE NAMED TURN of a session. A session whose adapter
// proves no interrupt seam refuses CodeInterruptUnsupported; a turn that is no longer the
// session's current turn refuses CodeStaleTurn, having typed nothing.
//
// The req carries the turn (Wave R6 fix-pack B7). It used to carry nothing at all, on the
// reasoning that "the signed tuple's session is its whole subject" -- which was true of the
// AUTHORIZATION and false of the SUBJECT: session is who may be interrupted, turn is what.
// Still no new crypto: the body is bound through the tuple's existing content slot via
// schema.TurnInterruptContentHash, composer_send's own arrangement.
type TurnInterrupter interface {
	InterruptTurn(machine, operationID string, req TurnInterruptReq) (ErrorCode, error)
}

// InteractionHistorian is the optional DaemonAPI seam behind interaction_history (M3.1,
// ADR-014): the page strictly older than beforeItem, ascending by cursor, bounded by limit,
// plus the honest "nothing older is retained" floor.
type InteractionHistorian interface {
	InteractionHistory(session, beforeItem string, limit int) ([]schema.JournalRecord, bool, ErrorCode, error)
}

// InteractionDetailer is the optional DaemonAPI seam behind interaction_detail (M3.3):
// one item's FULL pre-truncation body, or IS-CAP-3's `unavailable` outside retention.
type InteractionDetailer interface {
	InteractionDetail(session, itemID string) (json.RawMessage, ErrorCode, error)
}

// requireJournalPlaneRead is the WHOLE gate the two M3 reads ride, and it is deliberately
// identical to handleJournalRead's: the negotiated `journal` capability first (the reads
// answer with journal records on the journal carrier, so the plane they read is the one that
// capability names), then the remote-tier kill switch. Both refusals are the exact ones
// journal_read already emits, so a phone cannot tell the three reads apart by their refusal
// and conclude that one of them is a way around `off`.
func (cc *clientConn) requireJournalPlaneRead() bool {
	if !cc.hasCap(CapJournal) {
		cc.replyError("journal capability not negotiated")
		return false
	}
	return cc.allowUnsignedJournalPlaneRead()
}

// handleComposerSend serves the signed composer_send op (IS-LIFE-5, ADR-009 (8)): one
// structured message from the phone into a session. Authz runs FIRST with the body bound
// via ComposerSendContentHash -- recomputed from the forwarded body, so a gateway that
// alters text or expected_turn breaks the signature -- then the body-version gate, then the
// structural checks, then the seam.
func (cc *clientConn) handleComposerSend(c Control) {
	body := c.ComposerSend
	if !cc.requireRemoteAuthz(c, ActionComposerSend, c.SessionID, schema.ComposerSendContentHash(body)) {
		return
	}
	if !cc.requireBodyVersion(c) {
		return
	}
	switch {
	case body == nil:
		// The gateway refuses a stripped body too; this is the daemon's own gate.
		cc.replyErrorCode("composer_send: missing composer_send body", CodeInvalidField)
		return
	case body.Session != c.SessionID:
		// handleApprove's collision rule: the gateway is the documented owner-uid residual,
		// and two session coordinates free to differ would let it point a signature
		// authorized for one session's composer at another session's PTY.
		cc.replyErrorCode("composer_send: body names a session the command does not", CodeInvalidField)
		return
	case body.Text == "":
		cc.replyErrorCode("composer_send: empty text; there is nothing to inject", CodeInvalidField)
		return
	case len(body.Text) > maxSendInputText:
		// Refused, never truncated: a clipped send submits a different message than the
		// one the signature covered.
		cc.replyErrorCode("composer_send: text exceeds the input-path bound", CodeInvalidField)
		return
	}
	cs, ok := cc.srv.d.(ComposerSender)
	if !ok {
		// handleApprove's rule: OK here is a sent message the agent never received.
		cc.replyErrorCode("composer_send: not supported by this daemon; nothing was typed", CodeNotImplemented)
		return
	}
	if code, err := cs.ComposerSend(cc.endpointID, c.OperationID, *body); err != nil {
		cc.replyErrorCode("composer_send: "+err.Error(), code)
		return
	}
	cc.replyOK(c.SessionID)
}

// handleTurnInterrupt serves the signed turn_interrupt op (Mirror M2.4: "Stop becomes a
// signed interrupt op"). Authz runs FIRST with the body bound via TurnInterruptContentHash
// -- recomputed from the forwarded body, so a gateway that re-points expected_turn breaks
// the signature -- then the body-version gate, then the structural checks, then the seam.
// Structurally it is composer_send's twin, and that is the point of fix-pack B7: the two ops
// answer the same race and must answer it the same way.
func (cc *clientConn) handleTurnInterrupt(c Control) {
	body := c.TurnInterrupt
	if !cc.requireRemoteAuthz(c, ActionTurnInterrupt, c.SessionID, schema.TurnInterruptContentHash(body)) {
		return
	}
	if !cc.requireBodyVersion(c) {
		return
	}
	switch {
	case c.SessionID == "":
		cc.replyErrorCode("turn_interrupt: missing session_id; an interrupt names a session or nothing", CodeInvalidField)
		return
	case body == nil:
		// The gateway refuses a stripped body too; this is the daemon's own gate.
		cc.replyErrorCode("turn_interrupt: missing turn_interrupt body", CodeInvalidField)
		return
	case body.Session != c.SessionID:
		// handleComposerSend's collision rule, for the same reason: two session
		// coordinates free to differ would let a gateway point a signature authorized for
		// one session's Stop at another session's PTY.
		cc.replyErrorCode("turn_interrupt: body names a session the command does not", CodeInvalidField)
		return
	case body.ExpectedTurn == "":
		// An interrupt names the turn it stops or it is not an interrupt (B7). There is
		// deliberately no spelling of "interrupt whatever is running": that spelling is
		// what let a late Stop land its cancel key at an IDLE prompt, where the adapter's
		// own note records it clears the terminal user's half-typed composer line.
		cc.replyErrorCode(
			"turn_interrupt: missing expected_turn; a Stop that names no turn is refused, never applied to whichever turn is current",
			CodeInvalidField)
		return
	}
	ti, ok := cc.srv.d.(TurnInterrupter)
	if !ok {
		// OK here is a Stop button that "worked" while the agent kept running -- the exact
		// defect the signed op exists to end.
		cc.replyErrorCode("turn_interrupt: not supported by this daemon; nothing was interrupted", CodeNotImplemented)
		return
	}
	if code, err := ti.InterruptTurn(cc.endpointID, c.OperationID, *body); err != nil {
		cc.replyErrorCode("turn_interrupt: "+err.Error(), code)
		return
	}
	cc.replyOK(c.SessionID)
}

// handleInteractionHistory serves the UNSIGNED interaction_history read (M3.1, ADR-014,
// IS-CAP-2's terminal_watch precedent: the device authenticator is never consulted). The
// reply rides the existing Journal carrier, ascending by cursor, plus HistoryFloor. An empty
// BeforeItem is a present body asking the historian for the newest retained page; it is not a
// shape error at this layer.
func (cc *clientConn) handleInteractionHistory(c Control) {
	if !cc.requireJournalPlaneRead() {
		return
	}
	req := c.History
	if req == nil {
		cc.replyErrorCode("interaction_history: missing interaction_history body", CodeInvalidField)
		return
	}
	h, ok := cc.srv.d.(InteractionHistorian)
	if !ok {
		cc.replyErrorCode("interaction_history: not supported by this daemon", CodeNotImplemented)
		return
	}
	recs, floor, code, err := h.InteractionHistory(req.Session, req.BeforeItem, req.Limit)
	if err != nil {
		cc.replyErrorCode("interaction_history: "+err.Error(), code)
		return
	}
	_ = cc.writeControl(Control{Op: OpInteractionHistory, EndpointID: cc.endpointID,
		OperationID: cc.opID, SessionID: req.Session, Journal: recs, HistoryFloor: floor})
}

// handleInteractionDetail serves the UNSIGNED interaction_detail read (M3.3): exactly one
// Journal record whose Item is the FULL pre-truncation body, or a coded refusal carrying
// no records at all -- a partial body beside a refusal is the ambiguity IS-CAP-3 forbids.
func (cc *clientConn) handleInteractionDetail(c Control) {
	if !cc.requireJournalPlaneRead() {
		return
	}
	req := c.Detail
	if req == nil {
		cc.replyErrorCode("interaction_detail: missing interaction_detail body", CodeInvalidField)
		return
	}
	det, ok := cc.srv.d.(InteractionDetailer)
	if !ok {
		cc.replyErrorCode("interaction_detail: not supported by this daemon", CodeNotImplemented)
		return
	}
	full, code, err := det.InteractionDetail(req.Session, req.ItemID)
	if err != nil {
		cc.replyErrorCode("interaction_detail: "+err.Error(), code)
		return
	}
	_ = cc.writeControl(Control{Op: OpInteractionDetail, EndpointID: cc.endpointID,
		OperationID: cc.opID, SessionID: req.Session,
		Journal: []JournalRecord{{SessionID: req.Session, Type: "interaction", Item: full}}})
}
