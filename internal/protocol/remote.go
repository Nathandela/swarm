package protocol

import (
	"errors"
	"net"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// errRemoteMissingKillSwitch / errRemoteMissingOperationClaimer are the fail-closed
// construction refusals a remote-tier Server returns when its backend lacks a mandatory
// guard (A5 review R2). The remote tier grants take_control (and other mutating ops) only if
// it can globally halt remote control (KillSwitch) and can make each grant single-use
// (OperationClaimer); a backend missing either would silently yield unkillable /
// replayable control, so the Server refuses to serve rather than start the listener.
var (
	errRemoteMissingKillSwitch       = errors.New("remote-tier backend must implement KillSwitch (fail-closed construction guard)")
	errRemoteMissingOperationClaimer = errors.New("remote-tier backend must implement OperationClaimer (fail-closed construction guard)")
)

// The remote wire types and the signed-command vocabulary are declared in the
// daemon-free subpackage schema and aliased here (PB-BIND-0, see types.go): these
// are exactly the names the phone core must reach without importing the daemon.
type (
	ErrorCode         = schema.ErrorCode
	JournalRecord     = schema.JournalRecord
	DeviceCommandAuth = schema.DeviceCommandAuth
	RemoteCommand     = schema.RemoteCommand
)

// Refusal-reason taxonomy (R-PROT.7); ErrorCode.Transient reports retryability.
const (
	CodePolicy        = schema.CodePolicy
	CodeKillSwitch    = schema.CodeKillSwitch
	CodeRateLimit     = schema.CodeRateLimit
	CodeStaleApproval = schema.CodeStaleApproval
	CodeNotAuthorized = schema.CodeNotAuthorized
	CodeInvalidField  = schema.CodeInvalidField
)

// Canonical action strings signed over the remote command tuple (D4/R-POL.9), and
// the reserved Session value a launch signs (a launch has no session yet).
const (
	ActionLaunch          = schema.ActionLaunch
	ActionKill            = schema.ActionKill
	ActionDelete          = schema.ActionDelete
	ActionApprove         = schema.ActionApprove
	ActionDeviceRevoke    = schema.ActionDeviceRevoke
	ActionTakeControl     = schema.ActionTakeControl
	ActionTerminalWatch   = schema.ActionTerminalWatch
	ActionTerminalUnwatch = schema.ActionTerminalUnwatch

	LaunchSessionSentinel = schema.LaunchSessionSentinel
)

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

// KillSwitch is the optional interface a remote-tier DaemonAPI implements to expose a
// global remote-control master switch (R-KS.1): when RemoteControlEnabled reports false,
// requireRemoteAuthz refuses EVERY remote mutating op with CodeKillSwitch as its FIRST
// gate — before operation_id and the DeviceAuthenticator — so a valid device signature
// cannot bypass it (fail-closed-before-signature). A backend that does NOT implement it
// is unaffected (behavior unchanged); the durable default state is slice 2b.
type KillSwitch interface {
	RemoteControlEnabled() bool
}

// RemoteControlSetter is the optional interface a DaemonAPI implements to expose the
// OWNER-TIER manual override behind `swarm remote off`/`on` (A4): SetRemoteControl(false)
// durably DISABLES remote control regardless of paired devices (manual off WINS over
// device presence), and SetRemoteControl(true) returns to the device-derived value. It is
// the durable write side of KillSwitch's RemoteControlEnabled read; handleRemoteSetControl
// serves it OWNER-TIER ONLY (refused not_authorized on the remote tier, mirroring
// handlePairStart), so a remote device can never re-enable a switch its owner turned off. A
// backend that does NOT implement it leaves the toggle unsupported (behavior unchanged).
type RemoteControlSetter interface {
	SetRemoteControl(enabled bool) error
}

// TerminalTapper is the optional interface a remote-tier DaemonAPI implements to expose a
// READ-ONLY terminal tap (A7 renderer slice F2): TerminalTap opens a per-session output
// stream the Server renders server-side and streams to the phone as sanitized
// terminal_snapshot frames. The tap is READ-ONLY — the returned stream's Input/Resize are
// no-ops — so a remote peek OBSERVES without ever driving the session, and the
// terminal_subscribe handler NEVER forwards input on this path. The Server serves
// terminal_subscribe only when its backend satisfies this AND the remote-gateway capability
// was negotiated (mirrors JournalBackend's cap+backend seam), and refuses fail-closed when
// the kill switch is off (terminal content is more sensitive than journal metadata).
type TerminalTapper interface {
	TerminalTap(local string) (SessionStream, error)
}

// LaunchPolicy confines a remote launch to machine-configured cwd roots (R-POL.3). On the
// remote tier, handleLaunch resolves the request cwd with filepath.EvalSymlinks and calls
// RemoteLaunchAllowed(resolvedCwd); a non-nil error refuses the launch with CodePolicy —
// AFTER authz but BEFORE the cwd stat / any daemon side effect (R-POL.2), so a resolved cwd
// outside every root is refused with no side effect. An EMPTY root set denies every launch
// (fail-closed). A backend that does NOT implement it AT ALL is refused too (F4,
// fail-closed-absent): handleLaunch replies CodePolicy for every remote launch rather than
// skipping confinement, mirroring requireRemoteAuthz's fail-closed-absent DeviceAuthenticator
// handling. Production also delivers fail-closed via the assembly ALWAYS wiring a
// config-derived policy (empty-allowed by default) onto the coreAPI; the protocol-layer
// refusal is defense in depth against a misassembled backend, not the sole safeguard.
type LaunchPolicy interface {
	RemoteLaunchAllowed(resolvedCwd string) error
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
	// Fail-closed construction guard (A5 review R2): a remote-tier Server must not serve
	// control it cannot make single-use (OperationClaimer) or cannot kill (KillSwitch). This
	// enforces at construction what requireRemoteAuthz enforces for DeviceAuthenticator at
	// request time, but once — so a misassembled remote server (an adapter that forwards
	// DeviceAuthenticator while dropping these) never accepts a single connection.
	if _, ok := d.(KillSwitch); !ok {
		_ = ln.Close()
		return nil, errRemoteMissingKillSwitch
	}
	if _, ok := d.(OperationClaimer); !ok {
		_ = ln.Close()
		return nil, errRemoteMissingOperationClaimer
	}
	s := newServer(d)
	s.endpointID = endpointID
	s.ln = ln
	s.remoteTier = true
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}
