// Package protocol is the Epic 6 client<->daemon control surface (ADR-002): a
// versioned, capability-negotiated RPC (JSON control ops in wire.TControl frames)
// plus a data plane (wire.TDataIn/TDataOut/TSnapshot binary frames), layered over
// the shared G1 frame envelope (internal/wire). It wraps a daemon.Daemon (via the
// DaemonAPI subset) into the full client-facing surface: hello, list, launch,
// kill, delete, attach/detach, resize, subscribe.
//
// This is the low-reversibility wire surface. The message schema is frozen and
// documented field-by-field in docs/specifications/protocol.md (kept in sync by
// the GG-7 drift check). See that file and the ADRs for the normative contract.
//
// The message TYPES live in the daemon-free subpackage schema and are aliased here
// (PB-BIND-0): protocol.Control and schema.Control are the same type, so this
// package's surface, the wire encoding and the drift check are all unchanged.
package protocol

import (
	"errors"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// Version is the client<->daemon protocol version. A mismatch is fatal to the
// handshake (D-8): the client is told to run `swarm daemon restart`.
const Version = 1

// Control-plane op vocabulary (JSON, snake_case), carried in wire.TControl frames.
const (
	OpHello     = "hello"
	OpList      = "list"
	OpLaunch    = "launch"
	OpKill      = "kill"
	OpDelete    = "delete"
	OpAttach    = "attach"
	OpDetach    = "detach"
	OpResize    = "resize"
	OpSubscribe = "subscribe"
	OpEvent     = "event"
	OpLease     = "lease"
	OpOK        = "ok"
	OpError     = "error"

	// Remote journal ops (R-PROT.3): stream/read the daemon-wide journal.
	OpJournalSubscribe = "journal_subscribe"
	OpJournalRead      = "journal_read"
	OpJournalEvent     = "journal_event"

	// Remote control-plane read ops (slice A3.1): non-mutating, capability-gated
	// reads of the paired-device roster and the machine's remote launch policy.
	OpDeviceList  = "device_list"
	OpPolicyQuery = "policy_query"

	// OpDeviceRevoke is the remote control-plane MUTATING op (slice A3.2): removes a
	// paired device from the daemon's device registry.
	OpDeviceRevoke = "device_revoke"

	// OpRemoteSetControl is the OWNER-TIER op behind `swarm remote off`/`on` (A4): it
	// durably flips the manual remote-control master override the daemon reads at its
	// kill-switch choke points. Owner-only (refused not_authorized on the remote tier,
	// mirroring pair_start), so a remote device can never re-enable a switch its owner
	// turned off. The desired enabled state rides on Control.RemoteControl.
	OpRemoteSetControl = "remote_set_control"

	// OpTakeControl is the signed remote MUTATING op (slice A5-a) that acquires a
	// controller lease on a session — the anti-abuse gate that must precede any remote
	// keystroke reaching a session. It runs through requireRemoteAuthz like every other
	// remote mutating op and, on success, establishes a lease via the same attach path.
	OpTakeControl = "take_control"

	// OpTakeControlEnd is the caller-scoped teardown of one's OWN control session
	// (slice A5-b): it clears the connection's control session and releases its lease
	// (session_id + generation, mirroring detach; no device signature). Ending the
	// control session shuts the remote input gate.
	OpTakeControlEnd = "take_control_end"

	// Owner-tier pairing ops (slice A3.3-a, ADR-007 amendment "Pairing host: Option
	// A"): wire types only in this slice — no handlers, no pairing logic.
	OpPairStart   = "pair_start"
	OpPairPending = "pair_pending"
	OpPairConfirm = "pair_confirm"
	OpPairResult  = "pair_result"

	// Terminal-snapshot ops (A7 renderer slice B): terminal_subscribe requests the
	// server-rendered terminal snapshot stream for a session; terminal_snapshot carries
	// one sanitized, server-rendered snapshot to the phone (mirroring the
	// journal_subscribe/journal_event pair).
	OpTerminalSubscribe = "terminal_subscribe"
	OpTerminalSnapshot  = "terminal_snapshot"
)

// Negotiated capabilities. The legacy caps (attach, subscribe) plus the remote-tier
// caps (R-PROT.1): the hello handshake returns the intersection with the client's
// offer, and an op whose capability was not negotiated is refused.
const (
	CapAttach        = "attach"
	CapSubscribe     = "subscribe"
	CapRemoteGateway = "remote-gateway"
	CapJournal       = "journal"
	CapActivity      = "activity"
	CapPolicy        = "policy"
	CapPairing       = "pairing"
)

// The JSON message types are declared in the daemon-free subpackage schema and
// aliased here, so every existing importer (and the GG-7 drift check, which
// reflects them) sees exactly the same types. The split is PB-BIND-0: a Go
// dependency closure is per package, so while these types lived beside the
// daemon-wrapping Server the phone core could not name Control without shipping
// the daemon, the shim and the VT emulator into the bound Android app
// (docs/specifications/remote-phaseB-requirements.md 4.2, ADR-007 Decision 2).
type (
	Control          = schema.Control
	TerminalSnapshot = schema.TerminalSnapshot
	ApproveReq       = schema.ApproveReq
	AgentInstanceRef = schema.AgentInstanceRef
	SessionView      = schema.SessionView
	DeviceView       = schema.DeviceView
	PolicyView       = schema.PolicyView
	PairingControl   = schema.PairingControl
	LaunchReq        = schema.LaunchReq
)

// The wire schema now has two spellings (protocol.X and schema.X) and Go gives them no
// compile-time tie of its own. This assignment compiles ONLY while they are the SAME type:
// two DEFINED types with identical underlying types are not assignable. Un-alias Control --
// most likely by wanting to add a method, which the alias forbids in this package -- and
// this breaks loudly, rather than remotegw sealing one struct while phonecore opens another
// that has silently drifted (S1 review R3).
var _ Control = schema.Control{}

// Event is the client-facing subscribe payload: one status-changed session view.
type Event struct {
	Session SessionView
}

// ErrIncompatibleVersion is returned by Dial on a protocol-version mismatch. Its
// message names `swarm daemon restart` and states the restart is safe (D-8).
var ErrIncompatibleVersion = errors.New("protocol: incompatible daemon version")

// SessionStream is the daemon's single pipe to one session's shim: a snapshot, a
// live output stream, and the input/resize/close controls. The Server opens
// exactly one per session while a lease is held (ADR-002/L3).
type SessionStream interface {
	Snapshot() []byte
	Frames() <-chan []byte
	Input(p []byte) error
	Resize(cols, rows int) error
	Close() error
}

// OperationClaimer is the optional interface a DaemonAPI ALSO implements to claim a
// remote op's operation_id as single-use through the daemon's durable idempotency store
// (slice A5-c). handleTakeControl claims the op AFTER authorization: a duplicate
// operation_id (existed=true) is a REPLAY and is refused, so a captured take_control
// cannot re-establish a second lease. Unlike launch it is NOT redriven — take_control
// has no re-drivable side effect, so a consumed operation_id stays consumed. A backend
// that does NOT implement this interface leaves the A5-a/A5-b establishment path
// unchanged (the gate-token/single-use mechanism engages only with a real store).
type OperationClaimer interface {
	ClaimOperation(operationID, action, session string) (existed bool, err error)
}

// IdempotentExecutor is the optional interface a DaemonAPI ALSO implements to make a
// remote MUTATING op replay-safe by CACHED OUTCOME (slice DHI-3), backing handleKill/
// handleDelete. Unlike OperationClaimer (existed => refuse — correct for take_control,
// which must NOT re-establish a lease), a replayed kill/delete must return the ORIGINAL
// attempt's SUCCESS, executing the side effect exactly once.
//
// Fresh op: existed=false — the caller executes the side effect, then CommitIdempotentOp
// with its terminal outcome. Replayed op: existed=true, priorOK reports whether the
// ORIGINAL attempt COMPLETED (true) or FAILED (false); the caller returns that cached
// outcome and executes nothing. A backend that does NOT implement this interface leaves
// the existing non-idempotent kill/delete path unchanged.
type IdempotentExecutor interface {
	ClaimIdempotentOp(operationID, action, session string) (existed, priorOK bool, err error)
	CommitIdempotentOp(operationID string, ok bool) error
}

// DaemonAPI is the subset of a daemon the Server wraps. It is an interface so
// tests stub it; FromDaemon adapts a real *daemon.Daemon to it.
type DaemonAPI interface {
	List() []persist.Meta
	Launch(daemon.LaunchSpec) (persist.Meta, error)
	Kill(id string) error
	Delete(id string) error
	Attach(id string) (SessionStream, error) // opened once per lease
	Events() <-chan persist.Meta             // single status-change source; Server fans out
}
