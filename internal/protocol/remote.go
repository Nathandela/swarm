package protocol

import (
	"net"

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

// ServeRemote binds a REMOTE-TIER Server on socketPath: every connection is
// unconditionally remote-origin (amendment D.0-A1 — the gateway dials only this
// dedicated socket), so every remote MUTATING op (kill/launch/delete/...) MUST carry
// an operation_id or it is refused before any action (R-IDP.1/A4). Input is exempt.
func ServeRemote(d DaemonAPI, socketPath string) (*Server, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	s := newServer(d)
	s.ln = ln
	s.remoteTier = true
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}
