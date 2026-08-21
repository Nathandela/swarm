package schema

import "time"

// CurrentTerminalViewVersion is the TerminalViewV1 body version a machine declares in its
// remote profile (RemoteProfileV1.TerminalViewVersion). A phone that reads zero there
// concludes NO FALLBACK EXISTS on that machine, never "unversioned, therefore current"
// (ADR-017 T5-a).
const CurrentTerminalViewVersion = 1

// TerminalViewV1 is ADR-017 T4's read path: a FULL COALESCED SNAPSHOT of a
// terminal_fallback session's sanitized screen. There is no patch language and no delta
// -- a slow observer drops superseded snapshots and receives the newest complete
// revision, which is what internal/remotegw/coalesce.go already does and what T4 makes a
// WIRE CONTRACT.
//
// WHY BOTH ViewEpoch AND Revision (amendment T4-a). The daemon's render loop is PER
// INVOCATION and the gateway's watcher re-runs it after every transport hiccup with a
// fresh emulator. A bare revision restarted at 1 while the phone holds revision N makes
// the phone's only sane rule -- "drop anything not strictly greater" -- discard every
// snapshot of the second run, with no error on either side: the user reads a plausible,
// frozen screen. So the epoch is minted per render-loop start and the revision is
// monotonic WITHIN it, and the phone's rule is: differing epoch = hard reset that
// discards prior state; same epoch = strictly greater revision only.
//
// A watch grants NO INPUT AUTHORITY (T4). Nothing in this body carries or implies one.
type TerminalViewV1 struct {
	Session         string    `json:"session"`
	SessionInstance string    `json:"session_instance"` // T8-a: which incarnation this screen belongs to
	ViewEpoch       uint64    `json:"view_epoch"`       // minted per render-loop start (T4-a)
	Revision        uint64    `json:"revision"`         // strictly increasing within one epoch
	Reset           bool      `json:"reset"`            // true on the FIRST snapshot of every epoch, on every path
	Cols            int       `json:"cols"`
	Rows            int       `json:"rows"`
	Lines           []string  `json:"lines"`       // machine-sanitized, one string per grid row (vt.SnapText)
	RenderedAt      time.Time `json:"rendered_at"` // the MACHINE's clock: T4-b's staleness is derived from the snapshot's own age
}

// TerminalViewBounds is the ceiling a phone renders under. It is resolved from the
// machine-declared profile fields, clamped by the phone's own built-in ceiling.
type TerminalViewBounds struct {
	MaxLineBytes int
	MaxRows      int
	MaxRateHz    int
}

// The phone's own conservative built-ins. Zero means CLAMP TO THESE, never "unlimited"
// (ADR-017 T5-a): the bound's whole job is to tell a phone the ceiling it is rendering
// under BEFORE it renders, and "unlimited" is the answer that makes the bound decorative
// on exactly the machines that never set it -- which today is every deployed machine.
//
// MaxRateHz is 8 because the append budget is 8/s COMBINED per target (ADR-009:156-165)
// and the terminal stream spends from it.
const (
	builtinTerminalViewMaxLineBytes = 4096
	builtinTerminalViewMaxRows      = 200
	builtinTerminalViewMaxRateHz    = 8
)

// The bounds a MACHINE publishes in its remote profile. They are the built-ins spelled
// again on the producing side rather than left zero: zero clamps to the phone's built-in
// and is therefore safe, but it is also indistinguishable from "this machine has no
// opinion", and a declared ceiling is checkable against what the machine actually sends
// while an absent one is not (ADR-017 T5-a). A machine that renders less may lower them;
// nothing it declares can RAISE the phone's ceiling.
const (
	DeclaredTerminalViewMaxLineBytes = builtinTerminalViewMaxLineBytes
	DeclaredTerminalViewMaxRows      = builtinTerminalViewMaxRows
	DeclaredTerminalViewMaxRateHz    = builtinTerminalViewMaxRateHz
)

// TerminalViewBounds resolves the bounds this profile declares. A zero field clamps to
// the built-in; a declared field is believed only where it is LOWER. The profile tells a
// phone the machine's ceiling; the phone's own ceiling is not the machine's to move, or a
// compromised or skewed machine grants itself an unbounded render.
func (p RemoteProfileV1) TerminalViewBounds() TerminalViewBounds {
	return TerminalViewBounds{
		MaxLineBytes: clampBound(p.TerminalViewMaxLineBytes, builtinTerminalViewMaxLineBytes),
		MaxRows:      clampBound(p.TerminalViewMaxRows, builtinTerminalViewMaxRows),
		MaxRateHz:    clampBound(p.TerminalViewMaxRateHz, builtinTerminalViewMaxRateHz),
	}
}

// clampBound resolves one declared bound: non-positive means the machine declared
// nothing, so the built-in applies; anything above the built-in is capped at it.
func clampBound(declared, builtin int) int {
	if declared <= 0 || declared > builtin {
		return builtin
	}
	return declared
}

// OffersTerminalView reports whether this machine declares a TerminalView the phone knows
// how to read. Zero is "no fallback exists on this machine" (T5-a) -- which is literally
// what every machine deployed before Wave R8 sends.
func (p RemoteProfileV1) OffersTerminalView() bool {
	return p.TerminalViewVersion > 0 && p.TerminalViewVersion <= CurrentTerminalViewVersion
}

// TrustsCapabilityRecord reports whether this machine declares a capability-record
// version. Zero is "record untrusted", which composes with T2-a into one predicate:
// status card, both verbs refused.
func (p RemoteProfileV1) TrustsCapabilityRecord() bool {
	return p.CapabilityRecordVersion > 0
}
