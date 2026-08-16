package schema

// Wave R5 (bead agents-tracker-hggx.6, ADR-007 B144(b), playbook "Wave R5 -- phone
// remote launch"): the wire vocabulary of the machine-authored launch-preset flow.
// The phone SELECTS and CONFIRMS a preset the machine authored; it never composes
// argv, cwd, env, or options. These types live in the daemon-free schema package for
// PB-BIND-0's reason: the gomobile-bound facade is a SIGNER of session_launch and a
// renderer of the preset list, and must reach both without dragging internal/daemon
// onto a handset.

import (
	"crypto/sha256"
)

// ActionLaunchPresets is the signed READ of the machine-authored launch-preset list
// (playbook "launch_presets"). Unlike terminal_watch/journal_resync it IS signed and
// IS forwarded to the daemon's device authenticator: the preset custody lives
// daemon-side, and the list is what the phone's confirm sheet renders, so it rides
// the one authorization plane every semantic op rides. Its capability class is READ
// (skeleton/deviceauth.go): listing what could be launched starts nothing.
const ActionLaunchPresets = "launch_presets"

// SessionLaunchReq is the session_launch body: the phone's selection of ONE
// machine-authored preset at the CONFIRMED revision, plus the single free-text field
// the phone may contribute (the initial prompt) and the cosmetic terminal geometry.
// There is deliberately no cwd, no argv, no env, and no options field: everything
// that decides WHAT runs and WHERE is the resolved preset's, machine-side (D8).
type SessionLaunchReq struct {
	PresetID string `json:"preset_id"`
	// PresetRevision is the staleness binding (playbook:447-448): the phone echoes
	// exactly the revision it displayed, so a machine-side re-authoring between the
	// list and the confirm answers stale_preset instead of silently launching
	// different policy.
	PresetRevision string `json:"preset_revision"`
	InitialPrompt  string `json:"initial_prompt,omitempty"`
	Cols           int    `json:"cols,omitempty"`
	Rows           int    `json:"rows,omitempty"`
}

// LaunchPresetView is one machine-authored preset as the wire carries it to the
// phone: the stable opaque id the phone selects by, the display facts the confirm
// sheet renders, the CANONICAL (symlink-resolved) workspace root, the allowlisted
// options, the worktree-isolation default, and the content-bound revision the phone
// must echo in its confirm.
type LaunchPresetView struct {
	ID          string            `json:"id"`
	DisplayName string            `json:"display_name"`
	Agent       string            `json:"agent"`
	Root        string            `json:"root"`
	Options     map[string]string `json:"options,omitempty"`
	Worktree    bool              `json:"worktree,omitempty"`
	Revision    string            `json:"revision"`
}

// Stable refusal codes of the preset flow, joining the existing kill_switch /
// not_authorized / policy / invalid_field taxonomy. Two codes rather than one
// because the remedies differ: unknown_preset means re-list (the machine never
// authored this id), stale_preset means re-confirm (the machine re-authored it
// between the phone's list and its confirm).
const (
	CodeUnknownPreset ErrorCode = "unknown_preset"
	CodeStalePreset   ErrorCode = "stale_preset"
	// CodeOutcomeUnknown is the session_launch reply for a launch whose outcome the
	// machine cannot decide (round 4, review MAJOR 2): the signed operation is in
	// flight under another driver and may yet apply or roll back. It is NOT a refusal
	// -- replied as `policy` the phone would tell the user the machine turned the
	// launch away, which is a different untruth from the "applied" it replaced. The
	// value is OutcomeUnknown's, deliberately: this is the same ADR-017 T9 delivery
	// state operation_status reports, reaching the phone one hop earlier.
	CodeOutcomeUnknown ErrorCode = ErrorCode(OutcomeUnknown)
)

// Operation-outcome states of the operation_status reconciliation read (ADR-017 T9
// delivery vocabulary, machine side; ADR-007 D6's two-phase record is the source of
// truth). applied is authoritative and names the session; outcome_unknown is honest
// undecidability (a launch that died mid-flight, never silent); unknown_operation is
// an id this machine has no record of -- never an invented outcome.
const (
	OutcomeApplied          = "applied"
	OutcomeUnknown          = "outcome_unknown"
	OutcomeUnknownOperation = "unknown_operation"
)

// OperationOutcomeView is the operation_status reply body.
type OperationOutcomeView struct {
	State     string `json:"state"`
	SessionID string `json:"session_id,omitempty"`
}

// ActivityRecord is one remote-originated action's audit entry (ADR-007 D10): every
// remote mutation AND its refusal is logged, so the terminal owner can audit what a
// paired phone did -- or was refused -- on this machine.
type ActivityRecord struct {
	Action      string    `json:"action"`
	DeviceID    string    `json:"device_id"`
	OperationID string    `json:"operation_id"`
	SessionID   string    `json:"session_id,omitempty"`
	Outcome     string    `json:"outcome"` // OutcomeApplied, or "refused"
	Code        ErrorCode `json:"code,omitempty"`
}

// SessionLaunchContentHash is the 32-byte content hash bound into a session_launch
// command's signature (R-POL.9's LaunchContentHash rule applied to the preset op):
// it binds the signed tuple to the preset id, the confirmed revision, and the
// phone's one free-text contribution, so a compromised gateway cannot re-point a
// validly-signed launch at a different preset or prompt. Cols/Rows are excluded as
// cosmetic, exactly as LaunchContentHash excludes them. A nil body hashes as the
// zero body: structurally refused later, but never a panic at the choke point.
//
// Signer (phonecore) and verifier (protocol server) both call THIS function;
// re-deriving the length-prefixed encoding elsewhere is forbidden for
// LaunchContentHash's stated reason.
func SessionLaunchContentHash(req *SessionLaunchReq) []byte {
	if req == nil {
		req = &SessionLaunchReq{}
	}
	h := sha256.New()
	writeHashField(h, []byte(req.PresetID))
	writeHashField(h, []byte(req.PresetRevision))
	writeHashField(h, []byte(req.InitialPrompt))
	return h.Sum(nil)
}

// OperationStatusContentHash binds an operation_status query's SUBJECT -- the
// operation being asked about -- into the signed tuple (round-2 review, MAJOR 2).
// Unbound, a compromised gateway could re-point a validly-signed status query at any
// other operation id and read back that operation's namespaced session id; bound,
// swapping the subject breaks the signature, exactly as handleDeviceRevoke binds its
// target and session_launch binds its preset. The session slot stays the operation
// sentinel (the query names no session instance); the subject rides the content
// slot. Signer (phonecore, when the facade verb lands) and verifier (protocol
// server) both call THIS function -- LaunchContentHash's no-re-derivation rule.
func OperationStatusContentHash(subjectOperationID string) []byte {
	h := sha256.New()
	writeHashField(h, []byte(subjectOperationID))
	return h.Sum(nil)
}
