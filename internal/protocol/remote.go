package protocol

import (
	"net"
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

// JournalResume is journal_read's snapshot+range result (atomic per R-JRN.4).
type JournalResume struct {
	Cursor     uint64
	Roster     []JournalRecord // live sessions as-of Cursor (snapshot half of R-JRN.4)
	Events     []JournalRecord
	FullResync bool
}

// JournalBackend is the optional interface a DaemonAPI ALSO implements to expose
// journal ops (matching the existing stopEvents() optional-interface seam): the
// Server enables journal_subscribe/journal_read only when its backend satisfies it
// AND the `journal` capability was negotiated.
type JournalBackend interface {
	JournalReadFrom(from uint64) (JournalResume, error)
	JournalSubscribe() (<-chan JournalRecord, func()) // single source; the Server fans out (S9)
}

// Canonical action strings signed over the remote command tuple (D4/R-POL.9). They
// are a wire contract: the phone-core signs the SAME string the daemon authorizes
// against, so they must never drift. Each maps to a capability action class in the
// authenticator (launch/kill/delete are all control-class).
const (
	ActionLaunch  = "launch"
	ActionKill    = "kill"
	ActionDelete  = "delete"
	ActionApprove = "approve"
)

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
// instead by ContentHash = LaunchContentHash(spec), which the daemon recomputes from
// the forwarded spec, so a gateway that alters the spec breaks the signature.
type RemoteCommand struct {
	DeviceCommandAuth
	Launch *LaunchReq `json:"launch,omitempty"`
}

// DeviceAuthenticator is the optional interface a remote-tier DaemonAPI implements to
// authorize remote mutating ops (R-POL.9): AuthorizeCommand returns nil ONLY when the
// device signature verifies over the canonical tuple AND the device's capability
// permits the action. Any failure (unknown device, invalid/expired signature,
// insufficient capability) returns a non-nil error and the op is refused before any
// side effect. The Server refuses every remote mutating op when its backend does NOT
// implement this interface — fail-closed against a misassembled remote server.
type DeviceAuthenticator interface {
	AuthorizeCommand(a DeviceCommandAuth) error
}

// ServeRemote binds a REMOTE-TIER Server on socketPath: every connection is
// unconditionally remote-origin (amendment D.0-A1 — the gateway dials only this
// dedicated socket), so every remote MUTATING op (kill/launch/delete/...) MUST carry
// an operation_id or it is refused before any action (R-IDP.1/A4). Input is exempt.
func ServeRemote(d DaemonAPI, socketPath string) (*Server, error) {
	return ServeRemoteWithID(d, socketPath, "")
}

// ServeRemoteWithID is ServeRemote with an explicit STABLE endpoint id, so remote-tier
// namespaced session ids match the main tier and are stable across connections and
// restarts (a phone signs and addresses a session by the same id every client sees).
// The assembly passes the daemon's federation id here; an empty id falls back to a
// per-connection id (test-only).
func ServeRemoteWithID(d DaemonAPI, socketPath, endpointID string) (*Server, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	s := newServer(d)
	s.endpointID = endpointID
	s.ln = ln
	s.remoteTier = true
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}
