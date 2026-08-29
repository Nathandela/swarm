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
	BuildVersion string   `json:"build_version,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Generation   uint64   `json:"generation,omitempty"`
	SnapshotLen  int      `json:"snapshot_len,omitempty"`
	Cols         int      `json:"cols,omitempty"`
	Rows         int      `json:"rows,omitempty"`
	// Name is the new session label carried on a rename op (v0.5). It is re-validated
	// and sanitized server-side (sanitizeName) before it reaches the daemon, exactly
	// like the label in a launch request. Restored in the 2026-08-02 merge: the schema
	// split forked before rename landed on main and the port predated the field.
	Name string `json:"name,omitempty"`
	// Tag is the manual grouping label carried on set_tag. Empty clears it.
	Tag      string        `json:"tag,omitempty"`
	Launch   *LaunchReq    `json:"launch,omitempty"`
	Sessions []SessionView `json:"sessions,omitempty"`
	Session  *SessionView  `json:"session,omitempty"`
	Error    string        `json:"error,omitempty"`

	// Remote-tier additive fields (R-PROT.2/.3/.7, amendments D.0-A1/A3/A6/A11).
	// Every field is omitempty so an existing-shape Control serializes
	// byte-identically (GG-7); the daemon-authoritative times are pointers so a zero
	// Control emits no new key (a zero time.Time is NOT omitted by encoding/json).
	OperationID     string          `json:"operation_id,omitempty"`      // idempotency key of a remote mutating op
	InteractionID   string          `json:"interaction_id,omitempty"`    // the agent interaction being approved (A6)
	DeviceID        string          `json:"device_id,omitempty"`         // pairing device id (never trusted alone, A1)
	DeviceSig       string          `json:"device_sig,omitempty"`        // detached Ed25519 over the canonical op tuple (D4)
	Cursor          uint64          `json:"cursor,omitempty"`            // journal cursor (journal_read/journal_event)
	JournalMaxBytes int             `json:"journal_max_bytes,omitempty"` // journal_read request: opt into bounded response pages
	JournalMore     bool            `json:"journal_more,omitempty"`      // journal_read response: more pages from this atomic read follow
	IssuedAt        *time.Time      `json:"issued_at,omitempty"`         // daemon-authoritative issue time
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`        // daemon-authoritative expiry
	Approve         *ApproveReq     `json:"approve,omitempty"`           // remote approval request (A6)
	ErrorCode       ErrorCode       `json:"error_code,omitempty"`        // machine-readable refusal reason (R-PROT.7)
	Journal         []JournalRecord `json:"journal,omitempty"`           // journal records (journal_read/journal_event)
	Roster          []JournalRecord `json:"roster,omitempty"`            // live sessions as-of Cursor on a journal_read snapshot (R-JRN.4)
	FullResync      bool            `json:"full_resync,omitempty"`       // the caller's cursor fell below the retained floor
	Devices         []DeviceView    `json:"devices,omitempty"`           // paired-device roster, carried on the device_list reply
	Policy          *PolicyView     `json:"policy,omitempty"`            // remote launch policy, carried on the policy_query reply
	TargetDeviceID  string          `json:"target_device_id,omitempty"`  // device_revoke: the device to REVOKE, distinct from the caller DeviceID (A3.2)
	Pairing         *PairingControl `json:"pairing,omitempty"`           // owner-tier pairing payload (pair_start/pair_pending/pair_confirm/pair_result, A3.3-a)
	TTLSeconds      int             `json:"ttl_seconds,omitempty"`       // take_control: caller-requested control-session lifetime (seconds), clamped server-side (A5-b)
	GateToken       string          `json:"gate_token,omitempty"`        // take_control: one-shot gate token bound into the device signature via content_hash and made single-use (A5-c)
	RemoteControl   *bool           `json:"remote_control,omitempty"`    // remote_set_control: the DESIRED remote-control master state (true=on, false=manual off). Pointer so false is transmittable and a zero Control emits no key (A4)

	Terminal *TerminalSnapshot `json:"terminal,omitempty"` // server-rendered terminal snapshot, carried on terminal_snapshot (A7 slice B)

	// TerminalView is the SAME snapshot, versioned (ADR-017 T4/T4-a/T8-a). It rides the SAME
	// terminal_snapshot op beside Terminal rather than on an op of its own, which is the
	// smallest step that puts view_epoch, revision, reset, session_instance and rendered_at on
	// the wire: an old gateway ignores an unknown key, a new one prefers this body, and no
	// RemoteProfileV1 version has to move (ADR-016's profile-version coordination is parked).
	//
	// WITHOUT IT THE FIELDS WERE PRODUCED AND NEVER SENT. `RenderTerminalView` minted the epoch
	// and the daemon threw it away; a phone watching a session REPLACED under the same id read
	// the new incarnation as a seamless continuation of the old screen, which is exactly what
	// T4-a and T8-a exist to prevent (closing review, finding 5).
	TerminalView *TerminalViewV1 `json:"terminal_view,omitempty"`

	SendInput *SendInputReq `json:"send_input,omitempty"` // owner-tier one-shot steering message, carried on send_input (ADR-010 A2)

	// BodyVersion is the per-op body-version tag the R1 refusal-ops vocabulary requires
	// (playbook §6.3, ADR-017 T5): the phone binds every session_launch/composer_send/
	// operation_status/turn_interrupt/terminal_control_begin/terminal_control_end to the
	// profile version it read from RemoteProfileV1. There is no body version 0: a Control
	// that omits this key is refused identically to a wrong version, never treated as an
	// implicit "version 1".
	BodyVersion int `json:"body_version,omitempty"`

	// Wave R5 launch-preset additive fields (ADR-007 B144(b), playbook "Wave R5").
	// SessionLaunch is the session_launch body: the phone's confirmed preset selection,
	// bound into the device signature via SessionLaunchContentHash.
	SessionLaunch *SessionLaunchReq `json:"session_launch,omitempty"`
	// Presets + PresetPolicyRevision ride the launch_presets reply: exactly the
	// machine-authored list (empty custody answers empty, never an invented default,
	// ADR-007 B135) plus the revision of the policy that produced it.
	Presets              []LaunchPresetView `json:"presets,omitempty"`
	PresetPolicyRevision string             `json:"preset_policy_revision,omitempty"`
	// SubjectOperationID is operation_status's QUERY SUBJECT -- the operation being
	// asked about -- distinct from the query's own OperationID exactly as ADR-007 D7
	// separates operation_id from interaction_id: the asking op and the asked-about op
	// are different coordinates.
	SubjectOperationID string `json:"subject_operation_id,omitempty"`
	// OperationOutcome rides the operation_status reply: applied (authoritative, with
	// the session id), outcome_unknown (honest undecidability), or unknown_operation
	// (no record; never invented).
	OperationOutcome *OperationOutcomeView `json:"operation_outcome,omitempty"`
	// Wave R6 complete-chat additive fields (Mirror M2.4 / M3.1 / M3.3, ADR-009 (8)).
	// ComposerSend is the composer_send body: one structured message plus the turn it was
	// rendered against, bound into the device signature via ComposerSendContentHash.
	ComposerSend *ComposerSendReq `json:"composer_send,omitempty"`
	// TurnInterrupt is the turn_interrupt body (Wave R6 review fix-pack B7): the session
	// plus the turn the phone RENDERED the Stop against, bound into the device signature
	// via TurnInterruptContentHash. See schema/chat.go for why the op stopped being
	// bodyless.
	TurnInterrupt *TurnInterruptReq `json:"turn_interrupt,omitempty"`
	// History is the interaction_history body (M3.1, ADR-014); the reply rides the
	// existing Journal carrier plus HistoryFloor.
	History *InteractionHistoryReq `json:"interaction_history,omitempty"`
	// Detail is the interaction_detail body (M3.3, IS-CAP-2's unsigned read); the reply
	// carries exactly one Journal record whose Item is the full pre-truncation body.
	Detail *InteractionDetailReq `json:"interaction_detail,omitempty"`
	// The Wave R8 terminal-fallback bodies (ADR-017 T6). TerminalControlBegin is the
	// SIGNED begin, bound into the device signature via TerminalControlBeginContentHash;
	// TerminalInput is one UNSIGNED raw-input frame naming the generation it was
	// authorised under. ControlGeneration rides the begin's REPLY (the minted, non-
	// transferable generation) and every keepalive.
	TerminalControlBegin *TerminalControlBeginReq `json:"terminal_control_begin,omitempty"`
	TerminalInput        *TerminalInputReq        `json:"terminal_input,omitempty"`
	ControlGeneration    string                   `json:"control_generation,omitempty"`
	// HistoryFloor rides the interaction_history reply: nothing older than the returned
	// page is retained, so the phone renders a retention floor instead of a spinner.
	HistoryFloor bool `json:"history_floor,omitempty"`
	// DeviceCapability rides the launch_presets reply: the SIGNING device's own
	// authorization tier ("full"/"read_only"/"read_approve") as pinned machine-side in
	// the device registry at enrollment (round-2 fix-pack). It is the phone's only
	// honest wire source for its own tier -- capability is never read FROM the wire
	// (deviceauth's rule), but nothing forbade the machine STATING it, and without it
	// the launch screen's tier-denied state had no data source. Empty when the backend
	// exposes no capability seam: absent fact, never an invented tier.
	DeviceCapability string `json:"device_capability,omitempty"`
}

// SendInputReq is one owner-tier steering message (ADR-010 Amendment 1 A2), carried in
// Control.send_input on a send_input op against Control.session_id. EXACTLY ONE MODE per
// request: Key names a single key from the closed vocabulary, or Text is the message,
// submitted with a trailing CR when Submit. Both set, neither set, an unknown key, or text
// past the server's bound are refused invalid_field with nothing written.
//
// The daemon -- not the caller and not the shim -- applies the r3p submit-boundary
// discipline to Text (internal/submitframe): a PTY write is never a mixture of text and
// the CR that runs it. All three fields are omitempty, so a control that carries no
// send_input serializes byte-identically to the pre-ADR-010 shape.
type SendInputReq struct {
	Text   string `json:"text,omitempty"`
	Submit bool   `json:"submit,omitempty"`
	Key    string `json:"key,omitempty"`
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
//
// Documenting it is a PROCEDURAL obligation and not a fenced one, exactly as for
// JournalRecord.Item: protocol.md documents RemoteCommand and its bodies at the field
// level in prose rather than as a wire table, and GG-7's drift check
// (internal/protocol/protocolmd_test.go) reflects Control, SessionView, LaunchReq and
// TerminalSnapshot only -- so no build fails on a missing row here
// (interaction-schema.md §1 says so in as many words).
type ApproveReq struct {
	Session       string           `json:"session"`
	AgentInstance AgentInstanceRef `json:"agent_instance"`
	InteractionID string           `json:"interaction_id"`
	ContentHash   string           `json:"content_hash"`
	ExpiresAt     *time.Time       `json:"expires_at,omitempty"`
	// Decision is the id of the decision the owner chose, in the CLI's OWN vocabulary
	// (interaction-schema.md §3.5: spike-SB captured Codex offering accept |
	// acceptWithExecpolicyAmendment | cancel). The card labels its buttons from
	// decisions[].label and answers with the matching id; the daemon refuses an id the
	// request never offered.
	//
	// It is DELIBERATELY UNSIGNED (IS-LIFE-4). ContentHash is the signed tuple's one
	// content slot and ADR-007 D7 spends it on the interaction content, which the phone
	// echoes verbatim (IS-APR-2) and so cannot fold a choice into. The field rides inside
	// the epoch-sealed frame -- unforgeable by the relay, alterable only by the gateway,
	// which is the documented D4/D5 owner-uid residual and not a new one.
	Decision string `json:"decision,omitempty"`
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
	EndpointID     string        `json:"endpoint_id"`
	ID             string        `json:"id"` // namespaced: <endpoint_id>/<local>
	Agent          string        `json:"agent"`
	Name           string        `json:"name,omitempty"` // user-provided label; empty (or absent, from an older daemon) falls back to Agent at display
	Tag            string        `json:"tag,omitempty"`  // user-assigned grouping label; empty means untagged
	Cwd            string        `json:"cwd"`
	Status         status.Status `json:"status"`           // the three raw dims
	Group          status.Group  `json:"group"`            // precomputed server-side (E6.9)
	GroupEnteredAt time.Time     `json:"group_entered_at"` // when the session entered Group; drives newest-first ordering
	LastActivity   time.Time     `json:"last_activity"`
	CreatedAt      time.Time     `json:"created_at"`
	Summary        string        `json:"summary"` // V-4 one-line last-output summary
	// SpawnedFrom / SpawnIntent expose the session's lineage (ADR-010 D4) so the
	// roster can show where a session came from. Both are omitempty: an ordinary
	// session's row serializes exactly as it did before the fields existed.
	SpawnedFrom string `json:"spawned_from,omitempty"`
	SpawnIntent string `json:"spawn_intent,omitempty"`
	// BackendPlanError is the persisted reason this session launched with no backend
	// although its adapter declared one (persist.Meta.BackendPlanError): the agent's
	// PTY runs but nothing serves the attach channel. Surfaced on the roster so the
	// degradation is visible where the user looks (`swarm ls`, the TUI) instead of
	// one daemon.log line. omitempty: a healthy row serializes exactly as it did
	// before the field existed, and an older daemon simply never populates it.
	BackendPlanError string `json:"backend_plan_error,omitempty"`
	// Supervision is the persisted supervision mode of a handoff child (ADR-010
	// Amendment 3 C1/C5) and SupervisionPending reports that an attention event of
	// that child awaits its source: LIVE daemon state, sampled at stamp time exactly
	// like RemoteControlled, so the board can show a supervisor pending or gone. Both
	// omitempty: an unsupervised row serializes exactly as it did before the fields
	// existed.
	Supervision        string `json:"supervision,omitempty"`
	SupervisionPending bool   `json:"supervision_pending,omitempty"`
	// RemoteControlled reports that a PAIRED DEVICE is driving this session: it holds
	// the remote-tier controller lease, OR it sent a message recently
	// (skeleton.phoneRecentlyActive). The second clause is what keeps the field
	// meaningful once take-control leaves the product -- composer_send never needed a
	// lease, so a lease-only answer would be false for every phone there is.
	//
	// THE CLAIM THAT USED TO STAND HERE IS WITHDRAWN. It read: "a TUI attach and a phone
	// take_control compete for the SAME single shim subscriber slot, so the two are
	// mutually exclusive and no live in-attach indicator is possible". M0.1 disproved it
	// (docs/verification/mirror-m0.md): production runs two protocol Servers over one
	// coreAPI with separate lease maps, and coreAPI.Attach subscribes to the SHARED
	// per-session tap, so the shim's single-subscriber slot is claimed once and both live
	// streams survive in both orders. The roster row is still where this is shown, but
	// because a live in-attach indicator needs a side channel attach.Session does not
	// have -- not because the two states cannot coexist.
	//
	// omitempty: an uncontrolled row serializes exactly as it did before the field
	// existed, so the released 0.8.0 gateway -- which relays these frames verbatim
	// and is untouched by this change -- sees no new bytes.
	RemoteControlled bool `json:"remote_controlled,omitempty"`
	// RemoteActivityAt is WHEN a paired device last delivered a message to this session,
	// carried only while that instant is still inside the daemon's horizon
	// (skeleton.phoneActiveHorizon). It is what the terminal's marker SAYS -- "phone sent
	// 09:41" -- and it is an instant rather than another flag because the row has to state
	// an EVENT: a bare noun sitting beside "supervisor pending" reads as a CONDITION, "a
	// phone is on this session", which is precisely the presence claim nobody on this wire
	// measures (conversation surface, plan G.5).
	//
	// A POINTER with omitempty, like Capabilities and for the same reason in a different
	// key: encoding/json does not omit a zero time.Time, so a value field would stamp
	// "0001-01-01T00:00:00Z" onto every row that has never seen a phone. Absent means no
	// message is in the window -- there is no in-band way to say "never" as opposed to
	// "not lately", and the marker needs no such distinction because both draw nothing.
	RemoteActivityAt *time.Time `json:"remote_activity_at,omitempty"`
	// Capabilities is the daemon-authored per-session capability record (ADR-017 T2): a
	// pointer with omitempty, so an absent record (an older daemon, or a session the
	// daemon has not stamped yet) is wire-distinguishable from a stamped record that says
	// structured_chat=false. The phone renders from this record and infers nothing from
	// whether a transcript happens to be empty (T2 rule 3).
	Capabilities *SessionCapabilities `json:"capabilities,omitempty"`
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
	// ShortCode is the pair_start reply's human-typeable spelling of the same ceremony
	// (ADR-007 B140). Additive and omitempty on both sides of the version skew.
	ShortCode  string   `json:"short_code,omitempty"`
	SAS        []string `json:"sas,omitempty"`
	DeviceName string   `json:"device_name,omitempty"`
	Allow      bool     `json:"allow,omitempty"`
	DeviceID   string   `json:"device_id,omitempty"`
	Name       string   `json:"name,omitempty"`
	// Failure is why a pair_result enrolled nothing (ADR-007 B71(1)): one code from
	// protocol.PairFailure's closed vocabulary, empty on success. It is a CODE and never
	// the daemon's error text -- the pairing path parses attacker-influenced bytes, and
	// the client normalises anything it does not recognise, so no wire-supplied string
	// can reach the owner's terminal.
	Failure string `json:"failure,omitempty"`
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
	// SpawnedFrom, when non-empty, is the LOCAL id of the session that requested this
	// launch (ADR-010 D4), mirroring the ResumedFrom pattern: a daemon-launched session
	// is no process-group descendant of its spawner (S-4), so lineage must be explicit
	// metadata. SpawnIntent tags the link and is one of SpawnIntentHandoff or
	// SpawnIntentDelegate, valid only alongside a SpawnedFrom. Both omitempty: an
	// un-lineaged launch is byte-identical to the pre-lineage shape.
	SpawnedFrom string `json:"spawned_from,omitempty"`
	SpawnIntent string `json:"spawn_intent,omitempty"`
	// Supervision is the supervision mode of a handoff child (ADR-010 Amendment 3
	// C1): one of SupervisionPassive, SupervisionManual or SupervisionNone, valid only
	// alongside SpawnIntentHandoff -- a mode names how the SOURCE follows its child,
	// so it is meaningless on any other launch. omitempty: an unsupervised launch is
	// byte-identical to the pre-supervision shape.
	Supervision string `json:"supervision,omitempty"`
}

// The closed spawn-intent vocabulary (ADR-010 D2/D4): the two flavors of spawn share
// their mechanics and differ only in recorded intent.
const (
	SpawnIntentHandoff  = "handoff"
	SpawnIntentDelegate = "delegate"
)

// The closed supervision-mode vocabulary (ADR-010 Amendment 3 C1): passive is
// daemon-managed (the source is woken only when the child needs attention), manual
// is the source's own watch loop, none is launch-and-report.
const (
	SupervisionPassive = "passive"
	SupervisionManual  = "manual"
	SupervisionNone    = "none"
)
