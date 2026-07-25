package schema

import (
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// ErrorCode is the stable machine-readable refusal-reason taxonomy every
// R-POL/R-KS/R-IDP/R-REL refusal carries (plan R-PROT.7, amendment D.0-A11), so the
// phone can drive retry policy — a string-only error cannot. It rides on
// Control.error_code alongside the human-readable Error prose.
type ErrorCode string

const (
	CodePolicy        ErrorCode = "policy"
	CodeKillSwitch    ErrorCode = "kill_switch"
	CodeRateLimit     ErrorCode = "rate_limit"
	CodeStaleApproval ErrorCode = "stale_approval"
	CodeNotAuthorized ErrorCode = "not_authorized"
	CodeInvalidField  ErrorCode = "invalid_field"
)

// Transient reports whether a refusal is worth retrying: only rate_limit is
// transient; policy / kill_switch / stale_approval / not_authorized / invalid_field
// are permanent (retrying reproduces the same refusal).
func (c ErrorCode) Transient() bool { return c == CodeRateLimit }

// JournalRecord is one wire-facing journal event (R-PROT.3). It mirrors the
// daemon journal's record fields the phone needs; the daemon-internal payload is
// not carried on the wire.
type JournalRecord struct {
	Cursor    uint64       `json:"cursor"`
	SessionID string       `json:"session_id"`
	Type      string       `json:"type"`
	Group     status.Group `json:"group,omitempty"`
}

// Canonical action strings signed over the remote command tuple (D4/R-POL.9). They
// are a wire contract: the phone-core signs the SAME string the daemon authorizes
// against, so they must never drift. Each maps to a capability action class in the
// authenticator (launch/kill/delete are all control-class).
const (
	ActionLaunch       = "launch"
	ActionKill         = "kill"
	ActionDelete       = "delete"
	ActionApprove      = "approve"
	ActionDeviceRevoke = "device_revoke"
	ActionTakeControl  = "take_control"

	// ActionTerminalWatch / ActionTerminalUnwatch start/stop a server-rendered terminal
	// peek for a session (A7 F2 wiring). Unlike the mutating actions above they are a
	// READ: the phone seals an UNSIGNED RemoteCommand carrying only the action + target
	// session, and the gateway routes it to its TerminalWatcher WITHOUT forwarding to the
	// daemon's device authenticator. The daemon still gates the peek itself (cap
	// remote-gateway + the kill switch, re-checked per snapshot in handleTerminalSubscribe),
	// so no device signature is required to merely ask the gateway to open the read.
	ActionTerminalWatch   = "terminal_watch"
	ActionTerminalUnwatch = "terminal_unwatch"

	// ActionPushPrefs sets the machine-side push preference for the signing device
	// (PB-PUSH-8): which categories of transition may wake it. Unlike the reads above it
	// IS signed and IS forwarded to the daemon's device authenticator -- the gateway holds
	// no device key, so a locally-decided preference would let anyone who can inject a
	// plaintext-shaped mailbox frame silence the owner's notifications. Its capability
	// class is READ (skeleton/deviceauth.go): it cannot start, stop or type into anything,
	// and a control-class mapping would leave a read-only paired phone receiving
	// notifications it has no way to silence. ADR-007 B20 records the consequence that the
	// capability check therefore cannot refuse this verb; the SIGNATURE is its gate.
	ActionPushPrefs = "push_prefs"
)

// PushPrefs is the machine-authoritative record of which push categories may wake the
// paired device (PB-PUSH-8, PB-PUSH-10). The two toggles are exactly PB-APP-7's coarse
// categories: NeedsInput covers a transition into needs_input, Finished covers one into
// ready_for_review or completed.
//
// Version is a device-supplied monotonic counter, not a timestamp: the relay is the
// declared adversary and may reorder or replay what it retains, so a preference frame
// from before the user turned pushes off must not turn them back on. The machine refuses
// any update whose Version does not strictly exceed the stored one. Version 0 is reserved
// for the never-configured bootstrap record, so the phone's first real update always wins.
type PushPrefs struct {
	Version    uint64 `json:"version"`
	NeedsInput bool   `json:"needs_input"`
	Finished   bool   `json:"finished"`
}

// LaunchSessionSentinel is the canonical Session value signed over a launch command
// (D4/R-POL.9): a launch has no target session yet, but the signed tuple requires a
// non-empty Session, so both the phone-core (signer) and the daemon (verifier) use this
// reserved value. It contains no "/" so it can never collide with a namespaced session
// id (endpoint/local).
const LaunchSessionSentinel = "@launch"

// DeviceCommandAuth is the authenticated context of one remote mutating op, passed
// to the DeviceAuthenticator (R-POL.9). Its fields are exactly the canonical command
// tuple (D4) plus the detached signature: the authenticator reconstructs the signing
// input from them, verifies the signature against the device's pinned command-signing
// key, and checks the device's capability permits Action — returning nil only if both
// hold.
type DeviceCommandAuth struct {
	DeviceID    string    // registry lookup key; never trusted alone (A1)
	Action      string    // canonical action string (also selects the capability class)
	Machine     string    // endpoint id
	Session     string    // namespaced session id ("" for launch, which creates one)
	OperationID string    // idempotency identity; single-use, binds the signature
	ExpiresAt   time.Time // daemon-authoritative expiry; a past value is refused
	ContentHash []byte    // optional 32-byte hash binding op content (e.g. a launch spec)
	Sig         string    // detached Ed25519 signature (device_sig) over the tuple
}

// RemoteCommand is the plaintext a phone seals into a command envelope for the
// untrusted relay: the signed command tuple plus, for a launch, the LaunchReq spec
// it is bound to. DeviceCommandAuth is embedded (its fields inline in the JSON, no
// tags), and Launch is omitempty, so this wrapper is byte-compatible with a bare
// DeviceCommandAuth envelope in BOTH directions -- a bare-auth envelope decodes here
// with Launch nil, and a RemoteCommand decodes as a plain DeviceCommandAuth ignoring
// the extra field. The launch spec is NOT part of the signed tuple; it is bound
// instead by ContentHash = protocol.LaunchContentHash(spec), which the daemon recomputes from
// the forwarded spec, so a gateway that alters the spec breaks the signature.
type RemoteCommand struct {
	DeviceCommandAuth
	// PushPrefs is the push_prefs body (PB-PUSH-8). It is deliberately NOT bound by
	// ContentHash the way a launch spec is, and the difference is load-bearing: a launch
	// spec is forwarded through the gateway IN CLEARTEXT (Control.Launch), so the hash is
	// what stops the gateway altering it, whereas a preference body never leaves the
	// gateway -- it arrives sealed under the epoch content key, which the relay cannot
	// forge, and the gateway is itself the custodian that decides delivery. A hash it
	// recomputed against its own file would protect nothing it could not simply overwrite.
	PushPrefs  *PushPrefs `json:"push_prefs,omitempty"`
	Launch     *LaunchReq `json:"launch,omitempty"`
	GateToken  string     `json:"gate_token,omitempty"`  // take_control: one-shot gate token; the gateway reconstructs Control.GateToken from it. Bound into the signature via ContentHash=SHA256(GateToken), not carried in the signed tuple.
	TTLSeconds int        `json:"ttl_seconds,omitempty"` // take_control: caller-requested control-session lifetime (seconds), clamped server-side. Not signed (cosmetic like Cols/Rows).
}
