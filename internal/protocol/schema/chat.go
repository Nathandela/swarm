package schema

// Wave R6 (bead agents-tracker-hggx.7, Mirror M2.4 / M3.1 / M3.3, ADR-009 (5)/(8),
// interaction-schema.md IS-LIFE-5 and IS-CAP-2/-3): the wire vocabulary of the complete
// structured chat -- the composer send body, the paged interaction-history read, and the
// detail-on-demand read. These types live in the daemon-free schema package for PB-BIND-0's
// reason: the gomobile-bound facade is the SIGNER of composer_send and must reach the body
// and its content binding without dragging internal/daemon onto a handset.

import "crypto/sha256"

// ComposerSendReq is the composer_send body (IS-LIFE-5): one structured message from the
// phone into a session's agent CLI. ExpectedTurn is the turn the phone RENDERED the send
// against -- the daemon refuses stale_turn when it no longer names the session's current
// turn, so a tap that lands after the conversation moved on is refused, never misapplied.
// An idle session (no open turn) is matched by the empty ExpectedTurn.
type ComposerSendReq struct {
	Session      string `json:"session"`
	ExpectedTurn string `json:"expected_turn,omitempty"`
	Text         string `json:"text"`
}

// TurnInterruptReq is the turn_interrupt body (Mirror M2.4).
//
// THE OP USED TO BE BODYLESS, and that was a defect, not an economy (R6 review finding B7,
// pre-recorded in docs/verification/r6-red/chat-red.txt). composer_send carries
// expected_turn for one stated reason -- "a tap lands later than it was rendered" -- and a
// Stop button is tapped under exactly that race. Probed: with turnA superseded by turnB, a
// composer_send rendered against turnA was refused stale_turn while the interrupt rendered
// against the SAME turnA succeeded and typed the cancel sequence into turnB. In playbook
// §8.1 that turnB is the turn the OWNER just started from the terminal, and the adapter's
// own note records that the cancel key at an idle Claude prompt CLEARS the composer -- so a
// late phone Stop wipes the terminal user's half-typed line.
//
// ExpectedTurn is therefore REQUIRED and non-empty: an interrupt names the turn it stops or
// it is not an interrupt. That also closes the idle case by construction -- there is no
// spelling of "interrupt whatever is running", so a Stop can never land on a turn nobody
// rendered it against. Refusals reuse the composer's own CodeStaleTurn: same subject, same
// remedy (re-read the transcript).
type TurnInterruptReq struct {
	Session      string `json:"session"`
	ExpectedTurn string `json:"expected_turn"`
}

// TurnInterruptContentHash is the 32-byte content hash bound into a turn_interrupt command's
// signature, mirroring ComposerSendContentHash exactly: it binds the signed tuple to the
// session AND the turn, so a compromised gateway cannot re-point a validly-signed Stop at a
// different turn. No new crypto -- the same length-prefixed encoding under the same signed
// content slot the tuple already had. A nil body hashes as the zero body: structurally
// refused later, never a panic at the choke point.
func TurnInterruptContentHash(req *TurnInterruptReq) []byte {
	if req == nil {
		req = &TurnInterruptReq{}
	}
	h := sha256.New()
	writeHashField(h, []byte(req.Session))
	writeHashField(h, []byte(req.ExpectedTurn))
	return h.Sum(nil)
}

// InteractionHistoryReq is the interaction_history body (Mirror M3.1, ADR-014): the paged,
// per-session read of older transcript items. BeforeItem names the item_id the page must end
// strictly before; Limit bounds the page. The reply rides the EXISTING Control.Journal
// carrier plus Control.HistoryFloor.
type InteractionHistoryReq struct {
	Session    string `json:"session"`
	BeforeItem string `json:"before_item"`
	Limit      int    `json:"limit"`
}

// InteractionDetailReq is the interaction_detail body (Mirror M3.3, IS-CAP-2): the unsigned
// read of ONE item's full pre-truncation body. Outside retention the answer is IS-CAP-3's
// `unavailable`, never a partial body presented as whole.
type InteractionDetailReq struct {
	Session string `json:"session"`
	ItemID  string `json:"item_id"`
}

// Stable refusal codes of the complete chat (Wave R6), joining the existing taxonomy.
const (
	// CodeStaleTurn refuses a composer_send whose expected_turn no longer names the
	// session's current turn (IS-LIFE-5). Its OWN code, distinct from stale_approval:
	// different subject, different remedy (re-read the transcript, not re-list cards).
	CodeStaleTurn ErrorCode = "stale_turn"
	// CodeInterruptUnsupported is the honest ADR-017-shaped refusal for a turn_interrupt
	// against a session whose adapter proves no semantic interrupt seam: the caller is
	// fine, the capability is absent, and a guessed keystroke is forbidden (IS-TOOL-2's
	// posture one layer down).
	CodeInterruptUnsupported ErrorCode = "interrupt_unsupported"
	// CodeUnavailable is IS-CAP-3's answer for a detail read outside the bounded
	// retention window: the full body is gone and saying so beats presenting a partial
	// body as whole.
	CodeUnavailable ErrorCode = "unavailable"
	// CodeStructuredUnsupported refuses a composer_send against a session whose
	// structured_chat capability is absent or has been degraded by a proven
	// structured_gap (ADR-017 T2 rule 2, Mirror M5.5). It is the composer's sibling of
	// CodeInterruptUnsupported and exists for the same reason: a degraded session has no
	// message sink, so accepting the send would type the user's words into a PTY whose
	// transcript can never show them -- the gap silently bridged, which is the one thing
	// ADR-017 forbids. The caller is fine; the capability is absent.
	CodeStructuredUnsupported ErrorCode = "structured_unsupported"
	// CodeInputBusy refuses a composer_send whose message could not be written as one
	// message: somebody has written to this session's PTY since the last submit, so the
	// text would join whatever is already on the input line and the carriage return would
	// submit the concatenation -- the B13 merge (skeleton/chat.go:337-345), and the
	// two-sends-interleave defect (agents-tracker-bzfe) which is the same hazard with the
	// phone on both sides of it.
	//
	// IT CLAIMS NOTHING ABOUT THE CLI'S INPUT REGION, deliberately. ADR-017:175's
	// expected_input_revision would require characterising it, which chat.go:345-357
	// rightly refuses to guess at. This is the strictly weaker fact the shim owns
	// absolutely, because the shim holds the PTY's only serialised writer: bytes have been
	// written since the last submit. It is conservative in the safe direction -- a draft
	// typed and deleted back to empty still refuses -- and a refusal writes NOTHING, which
	// is the posture composer_send already takes for an over-long body.
	CodeInputBusy ErrorCode = "input_busy"
)

// ComposerSendContentHash is the 32-byte content hash bound into a composer_send command's
// signature, mirroring SessionLaunchContentHash exactly: it binds the signed tuple to the
// session, the expected turn and the text, so a compromised gateway cannot re-point a
// validly-signed send at different text or a different turn. A nil body hashes as the zero
// body: structurally refused later, never a panic at the choke point.
//
// Signer (phonecore.SignComposerSend) and verifier (protocol handleComposerSend) both call
// THIS function; re-deriving the length-prefixed encoding elsewhere is forbidden for
// LaunchContentHash's stated reason.
func ComposerSendContentHash(req *ComposerSendReq) []byte {
	if req == nil {
		req = &ComposerSendReq{}
	}
	h := sha256.New()
	writeHashField(h, []byte(req.Session))
	writeHashField(h, []byte(req.ExpectedTurn))
	writeHashField(h, []byte(req.Text))
	return h.Sum(nil)
}
