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
	OpRename    = "rename"
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

	// OpDeviceRegrant is PB-KEY-3's OWNER-TIER machine-side unblock behind
	// `swarm remote regrant`: mint a fresh sealed epoch grant for a still-registered
	// device and converge its record onto the current machine epoch.
	//
	// Owner-only, and not because of tiering taste. A device whose grant was lost holds no
	// epoch CONTENT key, so it cannot seal a command for the gateway at all -- a remote-tier
	// regrant verb would be unusable by exactly the device that needs it, while handing any
	// remote caller a way to make the machine re-issue key material.
	OpDeviceRegrant = "device_regrant"

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

	// OpSendInput is the OWNER-TIER one-shot steering write (ADR-010 Amendment 1 A2):
	// the daemon writes one message into a session's shim through the same input funnel
	// every lease write uses, serialized against concurrent lease input so the whole
	// message is atomic. It never takes or supersedes the attach lease — that is the
	// whole reason it is a new op rather than a local control session (A1). Like attach
	// it is gated by TIER and not by a negotiated capability, and like attach it is
	// refused outright on the remote tier, which keeps its own signed take_control lane.
	OpSendInput = "send_input"

	// OpPushPrefs is the signed remote op behind ActionPushPrefs (PB-PUSH-8): the phone
	// asks the machine to change which transitions may wake it. The daemon AUTHORIZES it
	// and nothing more -- the durable record and the delivery decision live at the gateway,
	// because PB-PUSH-10 puts durability where delivery is decided. It exists as a daemon
	// op precisely so the verb rides the one authorization plane (requireRemoteAuthz)
	// instead of growing a second one inside a gateway that holds no device key.
	OpPushPrefs = "push_prefs"

	// OpApprove is the signed remote MUTATING op behind ActionApprove (IS-LIFE-4): the phone
	// answers ONE pending approval_request. The body rides on Control.Approve, which has
	// carried the ApproveReq shape since amendment D.0-A6 and had no op to arrive on.
	//
	// The DECISION inside the body is deliberately unsigned (IS-LIFE-4) -- ADR-007 D7 spends
	// the signed tuple's one content slot on the interaction content, which the phone echoes
	// verbatim (IS-APR-2) and so cannot fold a choice into. THE OP IS NOT: it runs through
	// requireRemoteAuthz like kill, launch, device_revoke and take_control, because D4 admits
	// no remote-class mutating op without a valid device signature.
	OpApprove = "approve"

	// The R1 "refusal-ops" semantic op vocabulary (playbook §6.3, ADR-017 T5, ADR-007
	// B144): each is MAPPED (via actionClass) and dispatched behind requireRemoteAuthz
	// exactly like kill/delete/launch/approve, but currently answers only the sealed,
	// stable op_not_implemented refusal (CodeNotImplemented) -- the real handler does
	// not exist yet. Op and Action share the same string for each, mirroring kill/
	// delete/approve/push_prefs. See handleRefusalOp and schema.ActionSessionLaunch's
	// doc comment for the full contract.
	// Wave R5 (ADR-007 B144(b)): session_launch and operation_status now have REAL
	// handlers (handleSessionLaunch / handleOperationStatus) behind the same
	// requireRemoteAuthz choke point; the remaining four still answer the sealed
	// op_not_implemented refusal via handleRefusalOp.
	OpSessionLaunch        = "session_launch"
	OpComposerSend         = "composer_send"
	OpOperationStatus      = "operation_status"
	OpTurnInterrupt        = "turn_interrupt"
	OpTerminalControlBegin = "terminal_control_begin"
	OpTerminalControlEnd   = "terminal_control_end"

	// OpLaunchPresets is the signed READ of the machine-authored launch-preset list
	// (Wave R5, playbook "launch_presets"): Op == Action like every mapped sibling,
	// answered with Control.Presets + Control.PresetPolicyRevision.
	OpLaunchPresets = "launch_presets"

	// The Wave R6 M3 read ops (Mirror M3.1 / M3.3, ADR-014). Both are UNSIGNED reads on
	// the ActionTerminalWatch precedent (IS-CAP-2): no device fields, the device
	// authenticator is never consulted, and no new device-signed action exists for
	// either -- PB-SYNC-5's actionClass switch stays closed. interaction_history answers
	// on Control.Journal + Control.HistoryFloor; interaction_detail answers one Journal
	// record whose Item is the full pre-truncation body, or IS-CAP-3's `unavailable`.
	OpInteractionHistory = "interaction_history"
	OpInteractionDetail  = "interaction_detail"

	// The Wave R8 UNSIGNED frame kinds (ADR-017 T6). They ride the E2EE frame's
	// authenticated sender/sequence plus the CONFIRMED GENERATION, and are not
	// individually signed -- "the sole exception to full-body signatures in the remote
	// protocol, and it is deliberately the SAME exception that already exists"
	// (ADR-007's 2026-07-24 Decision 1).
	//
	// THE EXCEPTION IS EXACTLY TWO BODY TYPES AND ONE LIVE GENERATION, which is what
	// keeps it an exception. Neither is a mapped device action: PB-SYNC-5's actionClass
	// switch stays closed, exactly as it does for the two M3 reads.
	OpTerminalInput            = "terminal_input"
	OpTerminalControlKeepalive = "terminal_control_keepalive"
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
	// CapExternalResume permits an owner-tier client to launch a session from a
	// provider-native conversation id that was discovered outside swarm. Older
	// daemons do not negotiate it, so a new CLI cannot silently fresh-launch.
	CapExternalResume = "external-resume"
	// CapHandsOffHandoff permits an owner-tier client to launch a successor session
	// that is handed the SOURCE session's conversation pointers (ADR-010 Amendment 4,
	// the `handoff_from` launch option). Older daemons do not negotiate it, so a new
	// CLI cannot silently degrade to a bare, context-free launch.
	CapHandsOffHandoff = "hands-off-handoff"
)

// The closed spawn-intent vocabulary (ADR-010 D4), re-exported from the schema
// package so callers name it through the same spelling they use for LaunchReq.
const (
	SpawnIntentHandoff  = schema.SpawnIntentHandoff
	SpawnIntentDelegate = schema.SpawnIntentDelegate
)

// The closed supervision-mode vocabulary (ADR-010 Amendment 3 C1), re-exported for
// the same reason.
const (
	SupervisionPassive = schema.SupervisionPassive
	SupervisionManual  = schema.SupervisionManual
	SupervisionNone    = schema.SupervisionNone
)

// The JSON message types are declared in the daemon-free subpackage schema and
// aliased here, so every existing importer (and the GG-7 drift check, which
// reflects them) sees exactly the same types. The split is PB-BIND-0: a Go
// dependency closure is per package, so while these types lived beside the
// daemon-wrapping Server the phone core could not name Control without shipping
// the daemon, the shim and the VT emulator into the bound Android app
// (docs/specifications/remote-phaseB-requirements.md 4.2, ADR-007 Decision 2).
type (
	Control             = schema.Control
	TerminalSnapshot    = schema.TerminalSnapshot
	ApproveReq          = schema.ApproveReq
	AgentInstanceRef    = schema.AgentInstanceRef
	SessionView         = schema.SessionView
	DeviceView          = schema.DeviceView
	PolicyView          = schema.PolicyView
	PairingControl      = schema.PairingControl
	LaunchReq           = schema.LaunchReq
	ReconcileRecord     = schema.ReconcileRecord
	SendInputReq        = schema.SendInputReq
	RemoteProfileV1     = schema.RemoteProfileV1
	SessionCapabilities = schema.SessionCapabilities
)

// The wire schema now has two spellings (protocol.X and schema.X) and Go gives them no
// compile-time tie of its own. This assignment compiles ONLY while they are the SAME type:
// two DEFINED types with identical underlying types are not assignable. Un-alias Control --
// most likely by wanting to add a method, which the alias forbids in this package -- and
// this breaks loudly, rather than remotegw sealing one struct while phonecore opens another
// that has silently drifted (S1 review R3).
var _ Control = schema.Control{}

// LaunchContentHash re-exports the canonical launch content binding, which lives in the
// daemon-free schema package so the gomobile-bound phone facade -- the SIGNER -- can reach
// it without dragging internal/daemon into the closure shipped to a handset (PB-BIND-0).
// Verifier and signer must compute the SAME bytes: a divergent reimplementation produces
// silent signature-verification failures with no compile error, so there is exactly one.
var LaunchContentHash = schema.LaunchContentHash

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

// MessageSubmitter is the OPTIONAL half of a SessionStream that can deliver one whole
// message atomically -- its text and the carriage return that runs it, under a single hold
// of the PTY's only serialized writer (Slice 0, agents-tracker-bzfe).
//
// IT IS AN OPTIONAL INTERFACE RATHER THAN A SIXTH METHOD ON SessionStream, on the same
// reasoning as the daemon adapter's stopEvents: a stream that cannot do it is not broken,
// it is OLD -- a shim from before the transaction existed -- and every test double in the
// tree would otherwise have to grow a method it has no PTY to implement. A caller that
// gets ErrSubmitUnsupported degrades to two unlocked writes, which is where the merge is
// still possible; that degrade is disclosed, never silent.
type MessageSubmitter interface {
	Submit(text string) error
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
	Rename(id, name string) error            // update a session's display label (v0.5)
	Attach(id string) (SessionStream, error) // opened once per lease
	Events() <-chan persist.Meta             // single status-change source; Server fans out
}
