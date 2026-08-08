package schema

import (
	"encoding/json"
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
// not carried on the wire, with the single exception of Item below.
type JournalRecord struct {
	Cursor    uint64       `json:"cursor"`
	SessionID string       `json:"session_id"`
	Type      string       `json:"type"`
	Group     status.Group `json:"group,omitempty"`
	// Agent is the session's agent identity (persist.Meta.AgentType), carried so the
	// phone can label a session with the CLI running it. Like Group it is omitempty
	// because most record types do not carry it: the roster snapshot does, and a bare
	// event may not. An absent agent means the record does not carry one -- readers must
	// not read the empty string as an agent named "".
	Agent string `json:"agent,omitempty"`
	// Item is the interaction item object -- one unit of the phone's chat transcript
	// (ADR-009, docs/specifications/interaction-schema.md §1 and §2). It is populated ONLY
	// when Type is "interaction", where the daemon record's payload IS the item
	// (IS-LAYER-1); every other type's payload stays daemon-internal, and
	// internal/skeleton/api.go's toWireJournalRecord is the sole producer of this field.
	//
	// It is carried raw. The gateway seals and forwards items and parses none of them
	// (interaction-schema.md §10), so the item's `kind` discriminator is only ever read
	// inside the AEAD-covered plaintext and never reaches SenderKeyID or EpochID
	// (IS-LAYER-2, PB-SYNC-1). An unknown kind or an unknown field costs a consumer a skip,
	// not a decode failure (IS-COMPAT-1/-2). omitempty keeps every record type that
	// predates the field byte-identical.
	//
	// Documenting it is a PROCEDURAL obligation, not a fenced one: GG-7's drift check
	// (internal/protocol/protocolmd_test.go) reflects Control, SessionView, LaunchReq and
	// TerminalSnapshot only, so no build fails on a missing row. Its row lives in
	// docs/specifications/protocol.md under "The JournalRecord message" and was written in
	// the same change as this field.
	Item json.RawMessage `json:"item,omitempty"`
}

// Canonical action strings signed over the remote command tuple (D4/R-POL.9). They
// are a wire contract: the phone-core signs the SAME string the daemon authorizes
// against, so they must never drift. Each maps to a capability action class in the
// authenticator (launch/kill/delete are all control-class).
const (
	ActionLaunch       = "launch"
	ActionKill         = "kill"
	ActionDelete       = "delete"
	ActionApprove      = "approve"
	ActionDeviceRevoke = "device_revoke"
	ActionTakeControl  = "take_control"

	// ActionTerminalWatch / ActionTerminalUnwatch start/stop a server-rendered terminal
	// peek for a session (A7 F2 wiring). Unlike the mutating actions above they are a
	// READ: the phone seals an UNSIGNED RemoteCommand carrying only the action + target
	// session, and the gateway routes it to its TerminalWatcher WITHOUT forwarding to the
	// daemon's device authenticator. The daemon still gates the peek itself (cap
	// remote-gateway + the kill switch, re-checked per snapshot in handleTerminalSubscribe),
	// so no device signature is required to merely ask the gateway to open the read.
	ActionTerminalWatch   = "terminal_watch"
	ActionTerminalUnwatch = "terminal_unwatch"

	// ActionJournalResync asks the gateway to republish an atomic roster+events snapshot
	// so the phone can repair a stale journal channel (PB-SYNC-2). Like the two reads
	// above and UNLIKE ActionPushPrefs it is UNSIGNED and is NOT forwarded to the daemon's
	// device authenticator, and that is PB-SYNC-5's decision rather than an omission.
	//
	// A device-SIGNED resync walks into a trap the spec names: actionClass
	// (internal/skeleton/deviceauth.go) is a CLOSED switch that fails closed on an
	// unmapped action, the only fitting existing class is ActionControl -- which would make
	// a READ REPAIR require the control tier -- and device capability is pinned at
	// enrollment and never read from the wire, so an observe-tier device could never
	// resync its own view at all. Sealing under the epoch content key is already proof the
	// asker is the paired device; the gate that matters is the DAEMON's, which serves
	// journal_read on the negotiated `journal` capability plus the kill switch
	// (PB-SYNC-4). This constant therefore has NO actionClass mapping, exactly as
	// terminal_watch and terminal_unwatch have none.
	ActionJournalResync = "journal_resync"

	// ActionPushPrefs sets the machine-side push preference for the signing device
	// (PB-PUSH-8): which categories of transition may wake it. Unlike the reads above it
	// IS signed and IS forwarded to the daemon's device authenticator -- the gateway holds
	// no device key, so a locally-decided preference would let anyone who can inject a
	// plaintext-shaped mailbox frame silence the owner's notifications. Its capability
	// class is READ (skeleton/deviceauth.go): it cannot start, stop or type into anything,
	// and a control-class mapping would leave a read-only paired phone receiving
	// notifications it has no way to silence. ADR-007 B20 records the consequence that the
	// capability check therefore cannot refuse this verb; the SIGNATURE is its gate.
	ActionPushPrefs = "push_prefs"
)

// PushPrefs is the machine-authoritative record of which push categories may wake the
// paired device (PB-PUSH-8, PB-PUSH-10). The two toggles are exactly PB-APP-7's coarse
// categories: NeedsInput covers a transition into needs_input, Finished covers one into
// ready_for_review or completed.
//
// Version is a device-supplied monotonic counter, not a timestamp: the relay is the
// declared adversary and may reorder or replay what it retains, so a preference frame
// from before the user turned pushes off must not turn them back on. The machine refuses
// any update whose Version does not strictly exceed the stored one. Version 0 is reserved
// for the never-configured bootstrap record, so the phone's first real update always wins.
type PushPrefs struct {
	Version    uint64 `json:"version"`
	NeedsInput bool   `json:"needs_input"`
	Finished   bool   `json:"finished"`
}

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
// instead by ContentHash = protocol.LaunchContentHash(spec), which the daemon recomputes from
// the forwarded spec, so a gateway that alters the spec breaks the signature.
type RemoteCommand struct {
	DeviceCommandAuth
	// PushPrefs is the push_prefs body (PB-PUSH-8). It is deliberately NOT bound by
	// ContentHash the way a launch spec is, and the difference is load-bearing: a launch
	// spec is forwarded through the gateway IN CLEARTEXT (Control.Launch), so the hash is
	// what stops the gateway altering it, whereas a preference body never leaves the
	// gateway -- it arrives sealed under the epoch content key, which the relay cannot
	// forge, and the gateway is itself the custodian that decides delivery. A hash it
	// recomputed against its own file would protect nothing it could not simply overwrite.
	PushPrefs *PushPrefs `json:"push_prefs,omitempty"`
	// Approve is the approve body (IS-LIFE-4): the ApproveReq the gateway reconstructs
	// Control.Approve from. It is the ONE wire field this spec adds, and it adds no signed
	// action -- ActionApprove already exists.
	//
	// It is bound to the signature the way a launch spec is, and only in part. ContentHash is
	// the digest of the INTERACTION CONTENT the phone echoes verbatim (IS-APR-2, ADR-007 D7),
	// so a gateway that swaps the hash to redirect the approval breaks the signature. The
	// remaining fields are not covered, and do not need to be: agent_instance, interaction_id
	// and expires_at are checked against the daemon's own stored binding tuple, so altering
	// any of them yields CodeStaleApproval rather than a misapplied decision. The DECISION is
	// deliberately unsigned (IS-LIFE-4) -- the signed tuple has one content slot and D7 spends
	// it on the content -- riding instead inside the epoch-sealed frame, unforgeable by the
	// relay and alterable only by the gateway, which is the documented D4/D5 owner-uid
	// residual and not a new one.
	Approve    *ApproveReq `json:"approve,omitempty"`
	Launch     *LaunchReq  `json:"launch,omitempty"`
	GateToken  string      `json:"gate_token,omitempty"`  // take_control: one-shot gate token; the gateway reconstructs Control.GateToken from it. Bound into the signature via ContentHash=SHA256(GateToken), not carried in the signed tuple.
	TTLSeconds int         `json:"ttl_seconds,omitempty"` // take_control: caller-requested control-session lifetime (seconds), clamped server-side. Not signed (cosmetic like Cols/Rows).
	// ResyncCursor is journal_resync's from-cursor: the boundary the phone's session cache
	// currently stands at. The gateway's journal_read(from) answers with the complete roster
	// as-of a new boundary plus the events in between, which is exactly JournalReseed's
	// {Roster, Events, Cursor} -- so without it the gateway must read from 0 and re-send
	// every event the machine has ever journalled to repair one hole.
	//
	// Not signed, like TTLSeconds, and it does not need to be: the frame is sealed under the
	// epoch content key, which the relay cannot forge, and the worst a wrong value can do is
	// make the reseed carry more or fewer events than needed -- the roster it replaces the
	// cache with is complete either way.
	ResyncCursor uint64 `json:"resync_cursor,omitempty"`
}
