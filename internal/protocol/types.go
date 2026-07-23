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
package protocol

import (
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
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

// Control is the single JSON envelope for every control message (F-1: every
// message carries endpoint_id; a session-scoped op carries a namespaced
// session_id, <endpoint_id>/<local>). Which other fields matter depends on Op.
type Control struct {
	Op              string `json:"op"`
	EndpointID      string `json:"endpoint_id"`
	SessionID       string `json:"session_id,omitempty"`
	ProtocolVersion int    `json:"protocol_version,omitempty"`
	// BuildVersion is the daemon's internal/version.Version, carried on the hello
	// reply (E13.2). It is ADDITIVE: unlike ProtocolVersion (the wire skew gate,
	// unchanged by this field), a mismatch here is not fatal to the handshake — it
	// lets a client notice it is talking to a different-build daemon and nudge
	// `swarm daemon restart` even when the wire protocol still matches.
	BuildVersion string        `json:"build_version,omitempty"`
	Capabilities []string      `json:"capabilities,omitempty"`
	Generation   uint64        `json:"generation,omitempty"`
	SnapshotLen  int           `json:"snapshot_len,omitempty"`
	Cols         int           `json:"cols,omitempty"`
	Rows         int           `json:"rows,omitempty"`
	Launch       *LaunchReq    `json:"launch,omitempty"`
	Sessions     []SessionView `json:"sessions,omitempty"`
	Session      *SessionView  `json:"session,omitempty"`
	Error        string        `json:"error,omitempty"`

	// Remote-tier additive fields (R-PROT.2/.3/.7, amendments D.0-A1/A3/A6/A11).
	// Every field is omitempty so an existing-shape Control serializes
	// byte-identically (GG-7); the daemon-authoritative times are pointers so a zero
	// Control emits no new key (a zero time.Time is NOT omitted by encoding/json).
	OperationID    string          `json:"operation_id,omitempty"`     // idempotency key of a remote mutating op
	InteractionID  string          `json:"interaction_id,omitempty"`   // the agent interaction being approved (A6)
	DeviceID       string          `json:"device_id,omitempty"`        // pairing device id (never trusted alone, A1)
	DeviceSig      string          `json:"device_sig,omitempty"`       // detached Ed25519 over the canonical op tuple (D4)
	Cursor         uint64          `json:"cursor,omitempty"`           // journal cursor (journal_read/journal_event)
	IssuedAt       *time.Time      `json:"issued_at,omitempty"`        // daemon-authoritative issue time
	ExpiresAt      *time.Time      `json:"expires_at,omitempty"`       // daemon-authoritative expiry
	Approve        *ApproveReq     `json:"approve,omitempty"`          // remote approval request (A6)
	ErrorCode      ErrorCode       `json:"error_code,omitempty"`       // machine-readable refusal reason (R-PROT.7)
	Journal        []JournalRecord `json:"journal,omitempty"`          // journal records (journal_read/journal_event)
	Roster         []JournalRecord `json:"roster,omitempty"`           // live sessions as-of Cursor on a journal_read snapshot (R-JRN.4)
	FullResync     bool            `json:"full_resync,omitempty"`      // the caller's cursor fell below the retained floor
	Devices        []DeviceView    `json:"devices,omitempty"`          // paired-device roster, carried on the device_list reply
	Policy         *PolicyView     `json:"policy,omitempty"`           // remote launch policy, carried on the policy_query reply
	TargetDeviceID string          `json:"target_device_id,omitempty"` // device_revoke: the device to REVOKE, distinct from the caller DeviceID (A3.2)
	Pairing        *PairingControl `json:"pairing,omitempty"`          // owner-tier pairing payload (pair_start/pair_pending/pair_confirm/pair_result, A3.3-a)
	TTLSeconds     int             `json:"ttl_seconds,omitempty"`      // take_control: caller-requested control-session lifetime (seconds), clamped server-side (A5-b)
	GateToken      string          `json:"gate_token,omitempty"`       // take_control: one-shot gate token bound into the device signature via content_hash and made single-use (A5-c)
}

// ApproveReq is a remote approval of an agent interaction (amendment D.0-A6):
// operation_id (the idempotency identity of the approve op, on the enclosing
// Control) is separated from interaction_id (the agent interaction being approved).
// ExpiresAt is daemon-authoritative and omitempty.
type ApproveReq struct {
	Session       string           `json:"session"`
	AgentInstance AgentInstanceRef `json:"agent_instance"`
	InteractionID string           `json:"interaction_id"`
	ContentHash   string           `json:"content_hash"`
	ExpiresAt     *time.Time       `json:"expires_at,omitempty"`
}

// AgentInstanceRef pins the agent-instance the approval binds to, mapping to the
// daemon's (shim PID, start-time) identity check (A6/shimIdentityMatches).
type AgentInstanceRef struct {
	ShimPID       int   `json:"shim_pid"`
	ShimStartTime int64 `json:"shim_start_time"`
}

// SessionView is one general-view row (V-4), stamped for the receiving client: a
// namespaced id + endpoint id + the daemon-computed status Group (E6.9 — clients
// never call status.Derive), alongside the three raw status dimensions.
type SessionView struct {
	EndpointID   string        `json:"endpoint_id"`
	ID           string        `json:"id"` // namespaced: <endpoint_id>/<local>
	Agent        string        `json:"agent"`
	Cwd          string        `json:"cwd"`
	Status       status.Status `json:"status"` // the three raw dims
	Group        status.Group  `json:"group"`  // precomputed server-side (E6.9)
	LastActivity time.Time     `json:"last_activity"`
	CreatedAt    time.Time     `json:"created_at"`
	Summary      string        `json:"summary"` // V-4 one-line last-output summary
}

// DeviceView is one paired-device row (R-DEV.1), carried on the device_list
// reply. Capability is the device's authorization tier rendered as its stable
// snake_case text (e.g. "full"/"read_only"/"read_approve").
type DeviceView struct {
	DeviceID   string    `json:"device_id"`
	Name       string    `json:"name"`
	Capability string    `json:"capability"`
	PairedAt   time.Time `json:"paired_at"`
}

// PolicyView is the machine's remote launch policy (R-POL.3), carried on the
// policy_query reply: the configured allowed cwd roots a remote launch is
// confined to.
type PolicyView struct {
	AllowedCwdRoots []string `json:"allowed_cwd_roots"`
}

// PairingControl is the owner-tier pairing payload (slice A3.3-a, ADR-007
// amendment "Pairing host: Option A"): wire type only in this slice — no
// handlers, no pairing logic. Each pair_* op uses a distinct field subset:
// pair_start carries a request subset (Capability/TTLSeconds) outbound and a
// reply subset (QR/RendezvousID/ExpiresAt) inbound; pair_pending carries
// SAS/DeviceName/RendezvousID; pair_confirm carries Allow/RendezvousID;
// pair_result carries DeviceID/Name.
type PairingControl struct {
	Capability   string     `json:"capability,omitempty"`
	TTLSeconds   int        `json:"ttl_seconds,omitempty"`
	QR           string     `json:"qr,omitempty"`
	RendezvousID string     `json:"rendezvous_id,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	SAS          []string   `json:"sas,omitempty"`
	DeviceName   string     `json:"device_name,omitempty"`
	Allow        bool       `json:"allow,omitempty"`
	DeviceID     string     `json:"device_id,omitempty"`
	Name         string     `json:"name,omitempty"`
}

// LaunchReq is a client's request to launch a new session. Every field is
// re-validated server-side (E6.6) before it reaches the DaemonAPI.
type LaunchReq struct {
	Agent         string            `json:"agent"`
	Cwd           string            `json:"cwd"`
	Options       map[string]string `json:"options"`
	Env           []string          `json:"env"`
	Cols          int               `json:"cols"`
	Rows          int               `json:"rows"`
	InitialPrompt string            `json:"initial_prompt"`
	// Worktree opts this session into launch-time git-worktree isolation (Epic 12):
	// the daemon runs the session's agent in a fresh isolated worktree/branch. It is
	// carried to the daemon's PreLaunch/PreDelete hooks by the assembly (skeleton),
	// which registers worktree.Create/Remove gated on this flag; the protocol layer
	// only transports it.
	Worktree bool `json:"worktree,omitempty"`
}

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
