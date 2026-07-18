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
	OpRename    = "rename"
	OpAttach    = "attach"
	OpDetach    = "detach"
	OpResize    = "resize"
	OpSubscribe = "subscribe"
	OpEvent     = "event"
	OpLease     = "lease"
	OpOK        = "ok"
	OpError     = "error"
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
	BuildVersion string   `json:"build_version,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Generation   uint64   `json:"generation,omitempty"`
	SnapshotLen  int      `json:"snapshot_len,omitempty"`
	Cols         int      `json:"cols,omitempty"`
	Rows         int      `json:"rows,omitempty"`
	// Name is the new session label carried on a rename op (v0.5). It is re-validated
	// and sanitized server-side (sanitizeName) before it reaches the daemon, exactly
	// like the label in a launch request.
	Name     string        `json:"name,omitempty"`
	Launch   *LaunchReq    `json:"launch,omitempty"`
	Sessions []SessionView `json:"sessions,omitempty"`
	Session  *SessionView  `json:"session,omitempty"`
	Error    string        `json:"error,omitempty"`
}

// SessionView is one general-view row (V-4), stamped for the receiving client: a
// namespaced id + endpoint id + the daemon-computed status Group (E6.9 — clients
// never call status.Derive), alongside the three raw status dimensions.
type SessionView struct {
	EndpointID   string        `json:"endpoint_id"`
	ID           string        `json:"id"` // namespaced: <endpoint_id>/<local>
	Agent        string        `json:"agent"`
	Name         string        `json:"name,omitempty"` // user-provided label; empty (or absent, from an older daemon) falls back to Agent at display
	Cwd          string        `json:"cwd"`
	Status       status.Status `json:"status"` // the three raw dims
	Group        status.Group  `json:"group"`  // precomputed server-side (E6.9)
	LastActivity time.Time     `json:"last_activity"`
	CreatedAt    time.Time     `json:"created_at"`
	Summary      string        `json:"summary"` // V-4 one-line last-output summary
}

// LaunchReq is a client's request to launch a new session. Every field is
// re-validated server-side (E6.6) before it reaches the DaemonAPI.
type LaunchReq struct {
	Agent         string            `json:"agent"`
	Name          string            `json:"name,omitempty"` // optional user-provided session label; re-validated + sanitized server-side (E6.6)
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
