package schema

import (
	"crypto/sha256"
	"strconv"
	"time"
)

// WAVE R8 -- THE TERMINAL FALLBACK'S WIRE BODIES AND ITS SEALED REFUSALS (ADR-017 T6).
//
// terminal_control_begin/end are SIGNED ops on the ActionControl tier (T6-a ratifies the
// mapping internal/skeleton/deviceauth.go already carries). terminal_input and
// terminal_control_keepalive are NOT individually signed: they ride the E2EE frame's
// authenticated sender/sequence plus the confirmed generation, and that is "the sole
// exception to full-body signatures in the remote protocol, and it is deliberately the
// SAME exception that already exists" (ADR-007's 2026-07-24 Decision 1). The exception is
// held to EXACTLY TWO BODY TYPES and one live generation, which is what keeps it an
// exception rather than a policy.

const (
	// CodeCapabilityRefused is the sealed, stable refusal for a terminal verb the
	// SESSION's capability record does not permit (ADR-017 T2-c, playbook:450-451). An
	// old, downlevel or rolled-back client degrades legibly and never receives a
	// malformed screen, which needs a code it can recognise rather than a message it
	// must parse. It covers all four fail-closed states with one wire value, because
	// they are one predicate: no record, an inconsistent record, a record with no
	// session instance, and a record whose destination is not the terminal.
	CodeCapabilityRefused ErrorCode = "capability_refused"

	// CodeStaleGeneration refuses an input or keepalive frame whose control generation
	// is no longer live: the horizon passed, or control was released. The remedy is to
	// ENTER CONTROL AGAIN, on a session that is still there.
	CodeStaleGeneration ErrorCode = "stale_generation"

	// CodeStaleInstance refuses a frame whose session instance no longer matches the
	// session's current incarnation (ADR-017 T8-a). It is a DIFFERENT fact with a
	// DIFFERENT remedy from the one above, and collapsing the two is how a user is told
	// to "try again" about a screen that no longer exists: the session was REPLACED, and
	// entering control on the new incarnation is a separate, deliberate act.
	CodeStaleInstance ErrorCode = "stale_instance"
)

// TerminalControlBeginReq is the signed request that mints one non-transferable control
// generation over a terminal_fallback session.
//
// It binds the SESSION INSTANCE and the SELECTED REMOTE PROFILE as well as the session,
// because a signature that verifies over bytes which do not name what it authorised is a
// signature over the wrong thing: without the instance a generation minted against one
// PTY authorises bytes into its replacement, and without the profile a phone's idea of
// what "control" means can differ from the machine's.
type TerminalControlBeginReq struct {
	Session         string     `json:"session"`
	SessionInstance string     `json:"session_instance"`
	Profile         int        `json:"profile"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
}

// TerminalInputReq is one unsigned raw-input frame. It names the generation it was
// authorised under and the instance that generation belongs to, so both can be
// re-evaluated per frame (ADR-017 T6-e) rather than trusted from the begin.
//
// IT IS NOT AN APPROVAL VERB AND MUST NEVER BECOME ONE (T6). An approval answered from a
// fallback screen still travels as the signed ActionApprove of IS-LIFE-4, or the button
// is not shown.
type TerminalInputReq struct {
	Session           string `json:"session"`
	SessionInstance   string `json:"session_instance"`
	ControlGeneration string `json:"control_generation"`
	Bytes             []byte `json:"bytes"`
}

// TerminalControlBeginContentHash is the canonical body hash a device signs over for
// terminal_control_begin, on ComposerSendContentHash's exact pattern: the daemon
// RECOMPUTES it from the forwarded body, so a gateway that re-points the op at another
// session, another incarnation or another profile breaks the signature.
//
// The instance and the profile are inside the hash for the reason T6 gives: the op "is
// bound to the selected remote profile, the paired device's command signing key, and the
// fallback session instance". A field outside the hash is a field a blind conduit may
// rewrite.
func TerminalControlBeginContentHash(req *TerminalControlBeginReq) []byte {
	if req == nil {
		req = &TerminalControlBeginReq{}
	}
	h := sha256.New()
	writeHashField(h, []byte(req.Session))
	writeHashField(h, []byte(req.SessionInstance))
	writeHashField(h, []byte(strconv.Itoa(req.Profile)))
	return h.Sum(nil)
}
