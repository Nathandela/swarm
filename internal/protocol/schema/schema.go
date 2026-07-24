// Package schema holds the daemon-free wire message types of the client<->daemon
// control surface: the Control envelope, its payload types, and the canonical
// remote-command tuple a phone signs (remote.go). Package protocol aliases every
// name declared here, so the split is invisible on the wire and to protocol's
// importers -- an alias IS the type.
//
// It is a separate package because the problem is a dependency EDGE, not a type
// (PB-BIND-0, docs/specifications/remote-phaseB-requirements.md 4.2). A Go
// dependency closure is per PACKAGE: protocol also wraps a daemon.Daemon, so the
// phone core could not so much as name Control without dragging internal/daemon,
// internal/shim, internal/vt, internal/persist and github.com/creack/pty into the
// gomobile-bound closure shipped to a handset an adversary may hold -- against
// ADR-007 Decision 2, which deliberately keeps the PTY and the VT emulator off the
// network-facing edge.
//
// Nothing here may import outside the bound allowlist
// (internal/phonecore/deps_allowlist.txt): standard library and internal/status.
//
// The message schema is frozen and documented field-by-field in
// docs/specifications/protocol.md (kept in sync by the GG-7 drift check, which
// reflects these types through their protocol aliases).
package schema

import (
	"time"

	"github.com/Nathandela/swarm/internal/status"
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
	RemoteControl  *bool           `json:"remote_control,omitempty"`   // remote_set_control: the DESIRED remote-control master state (true=on, false=manual off). Pointer so false is transmittable and a zero Control emits no key (A4)

	Terminal *TerminalSnapshot `json:"terminal,omitempty"` // server-rendered terminal snapshot, carried on terminal_snapshot (A7 slice B)
}

// TerminalSnapshot is one server-rendered, sanitized terminal snapshot (A7 renderer
// slice B), carried in Control.Terminal on a terminal_snapshot op. The daemon renders
// the session's VT grid to plain text (every control byte already stripped) so only
// sanitized text crosses the daemon->gateway socket; the phone displays Lines as-is.
type TerminalSnapshot struct {
	Session string   `json:"session"` // namespaced session id the snapshot is for
	Lines   []string `json:"lines"`   // sanitized plain-text grid rows, top to bottom
	Cols    int      `json:"cols"`    // grid width the snapshot was rendered at
	Rows    int      `json:"rows"`    // grid height the snapshot was rendered at
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
