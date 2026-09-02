package protocol

import (
	"errors"
	"net"
	"os"

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
	PushPrefs         = schema.PushPrefs
)

// Refusal-reason taxonomy (R-PROT.7); ErrorCode.Transient reports retryability.
const (
	CodePolicy         = schema.CodePolicy
	CodeKillSwitch     = schema.CodeKillSwitch
	CodeRateLimit      = schema.CodeRateLimit
	CodeStaleApproval  = schema.CodeStaleApproval
	CodeNotAuthorized  = schema.CodeNotAuthorized
	CodeInvalidField   = schema.CodeInvalidField
	CodeStaleRevision  = schema.CodeStaleRevision
	CodeNotImplemented = schema.CodeNotImplemented
)

// Canonical action strings signed over the remote command tuple (D4/R-POL.9), and
// the reserved Session value a launch signs (a launch has no session yet).
const (
	ActionLaunch                   = schema.ActionLaunch
	ActionKill                     = schema.ActionKill
	ActionDelete                   = schema.ActionDelete
	ActionApprove                  = schema.ActionApprove
	ActionDeviceRevoke             = schema.ActionDeviceRevoke
	ActionTakeControl              = schema.ActionTakeControl
	ActionTerminalWatch            = schema.ActionTerminalWatch
	ActionTerminalRenew            = schema.ActionTerminalRenew
	ActionTerminalInput            = schema.ActionTerminalInput
	ActionTerminalControlKeepalive = schema.ActionTerminalControlKeepalive
	ActionTerminalUnwatch          = schema.ActionTerminalUnwatch
	ActionPushPrefs                = schema.ActionPushPrefs
	ActionJournalResync            = schema.ActionJournalResync

	// The two Mirror M3 reads (ADR-014), journal_resync's UNSIGNED class verbatim.
	ActionInteractionHistory = schema.ActionInteractionHistory
	ActionInteractionDetail  = schema.ActionInteractionDetail

	// The R1 "refusal-ops" semantic vocabulary (playbook §6.3, ADR-017 T5, ADR-007 B144);
	// see schema.ActionSessionLaunch's doc comment for the full contract.
	ActionSessionLaunch        = schema.ActionSessionLaunch
	ActionComposerSend         = schema.ActionComposerSend
	ActionOperationStatus      = schema.ActionOperationStatus
	ActionTurnInterrupt        = schema.ActionTurnInterrupt
	ActionTerminalControlBegin = schema.ActionTerminalControlBegin
	ActionTerminalControlEnd   = schema.ActionTerminalControlEnd

	// ActionLaunchPresets is the Wave R5 signed read of the machine-authored preset
	// list; see schema.ActionLaunchPresets for the contract.
	ActionLaunchPresets = schema.ActionLaunchPresets

	LaunchSessionSentinel    = schema.LaunchSessionSentinel
	OperationSessionSentinel = schema.OperationSessionSentinel
)

// Stable preset refusal codes (Wave R5), re-exported beside their siblings above.
const (
	CodeUnknownPreset = schema.CodeUnknownPreset
	CodeStalePreset   = schema.CodeStalePreset
)

// SessionLaunchContentHash re-exports the canonical session_launch content binding for
// LaunchContentHash's reason (see types.go): signer and verifier must compute the SAME
// bytes, so there is exactly one implementation, in the daemon-free schema package.
var SessionLaunchContentHash = schema.SessionLaunchContentHash

// ErrUnknownPreset / ErrStalePreset are the LaunchPresetSource sentinels (Wave R5):
// an id this machine never authored resolves to ErrUnknownPreset (stable code
// unknown_preset), and a right id at a changed revision resolves to ErrStalePreset
// (stable code stale_preset, playbook:447-448) -- both decided machine-side BEFORE
// any argv composition, never trusted from the phone.
var (
	ErrUnknownPreset = errors.New("protocol: unknown launch preset")
	ErrStalePreset   = errors.New("protocol: stale launch preset revision")
)

// LaunchPresetSource is the optional interface a remote-tier DaemonAPI implements to
// expose the MACHINE-AUTHORED launch presets (Wave R5, ADR-007 B144(b)). Fail-closed
// absent, mirroring LaunchPolicy: a backend that does not implement it refuses every
// session_launch / launch_presets with CodePolicy rather than inventing an empty
// custody it does not hold.
type LaunchPresetSource interface {
	// LaunchPresetList returns exactly the machine-authored presets plus the preset
	// policy revision. An empty custody answers an empty list -- never a fabricated
	// default (ADR-007 B135).
	LaunchPresetList() ([]schema.LaunchPresetView, string)
	// ResolveLaunchPreset resolves one preset by id at the phone-confirmed revision.
	// ErrUnknownPreset / ErrStalePreset per the sentinel contract above; any other
	// error is a machine-side policy refusal (e.g. an unresolvable preset root).
	ResolveLaunchPreset(id, revision string) (schema.LaunchPresetView, error)
}

// OperationStatusSource is the optional interface a DaemonAPI implements to serve the
// operation_status reconciliation read (Wave R5): applied is authoritative with its
// session id, outcome_unknown is honest undecidability, and ok=false is an id the
// machine has no record of (answered unknown_operation, never invented). The read has
// NO side effect -- operation_status never authorizes a retry (playbook:449).
type OperationStatusSource interface {
	RemoteOperationOutcome(operationID string) (schema.OperationOutcomeView, bool)
}

// ActivityRecorder is the optional interface a DaemonAPI implements to receive the
// ADR-007 D10 activity log: every remote-originated mutation -- and its refusal -- is
// recorded, so the terminal owner can audit what a paired phone did on this machine.
type ActivityRecorder interface {
	RecordRemoteActivity(rec schema.ActivityRecord)
}

// DeviceCapabilitySource is the optional interface a DaemonAPI implements to STATE a
// paired device's registry-pinned authorization tier (round-2 fix-pack). The
// launch_presets reply stamps it for the authenticated signer, giving the phone its
// only honest wire source for the launch screen's tier-denied state. Direction
// matters: capability is never read FROM the wire (skeleton deviceauth's rule); this
// is the machine stating a fact it owns. ok=false -- unknown device, no registry --
// leaves the field empty: an absent fact, never an invented tier.
type DeviceCapabilitySource interface {
	DeviceCapability(deviceID string) (string, bool)
}

// JournalReseed is the machine->phone journal repair frame (PB-SYNC-2 / PB-SYNC-8), aliased
// from the daemon-free wire package so the phone's bound closure never reaches this one.
type JournalReseed = schema.JournalReseed

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

// JournalSubscribeFromBackend is the additive atomic resume surface. It is
// separate from JournalBackend so a new server remains source-compatible with an
// older daemon adapter and can negotiate/fall back explicitly.
type JournalSubscribeFromBackend interface {
	JournalSubscribeFrom(from uint64) (JournalResume, <-chan JournalRecord, func(), error)
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
// An optional ready channel binds the private socket immediately but delays
// accept/serve until the channel closes. Assembly supplies its publication
// barrier so bootstrap gates and crash-recovered embargoes precede both sockets;
// existing standalone callers omit it and serve immediately.
func ServeRemoteWithID(d DaemonAPI, socketPath, endpointID string, ready ...<-chan struct{}) (*Server, error) {
	if len(ready) > 1 {
		return nil, errors.New("protocol: multiple remote readiness gates")
	}
	var gate <-chan struct{}
	if len(ready) == 1 {
		if ready[0] == nil {
			return nil, errors.New("protocol: remote readiness gate is nil")
		}
		gate = ready[0]
	}
	return serveRemoteWithIDReady(d, socketPath, endpointID, gate)
}

func serveRemoteWithIDReady(d DaemonAPI, socketPath, endpointID string, ready <-chan struct{}) (*Server, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	// ADR-007 D4 specifies 0600 on the remote socket. net.Listen inherits the umask, so
	// until now the mode was whatever the operator's shell happened to set and the 0700
	// state dir was the only thing guarding it. That was tolerable while the socket existed
	// only for operators who opted in; since B15 every provisioned machine has one.
	// Fail-closed: a socket that cannot be made private is not served at all.
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
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
	go s.acceptLoop(ready)
	return s, nil
}
