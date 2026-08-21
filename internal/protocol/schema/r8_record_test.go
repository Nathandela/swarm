package schema

// WAVE R8 / SLICE S1+S2 -- THE CAPABILITY RECORD IS THE ROUTER, SO IT MUST BE A RECORD THAT
// CANNOT LIE. Failing-first (TDD RED, GG-5).
//
// ADR-017 T2 rule 4 -- "no route to the fallback from a healthy structured session" -- is
// today a property of ONE DERIVATION and not of the record. `internal/skeleton/capability.go:
// 375-376` sets `TerminalFallback: !structured`, and `SetStructuredChat` (capability.go:29-38)
// forces `TerminalFallback = true` on a degrade. Those two writers happen to keep the pair
// consistent. Nothing else does: the struct admits `{structured_chat:true, terminal_fallback:
// true}`, no decoder rejects it, and every gate written over ONE of the two booleans enforces
// T2 rule 4 only for as long as the derivation stays right. This file is amendment T2-b's
// fence -- the pair becomes a VALIDITY RULE, checked where the record is authored, where it is
// decoded off the wire, and where it is decoded on the phone.
//
// It also lands the three things ADR-017 binds by name and the repository does not have:
//
//   - T8-a: a per-incarnation SESSION INSTANCE identifier. The ADR binds the record, the
//     control generation and every snapshot to "the session instance"; there is no such
//     identifier in the tree (`grep -rn "SessionInstance\|instance_id" internal/ mobile/` finds
//     only test and comment names), only a session id that survives a shim restart, a resume
//     and a daemon restart. Without it, "a generation dies with its instance" is unenforceable
//     and a stale generation authorizes bytes against a NEW pty.
//   - T6-b: a distinct daemon-authored `terminal_control` field. Control authority is granted
//     only where `terminal_fallback` was authored TRUE AT LAUNCH, never where it was derived
//     by a degrade. Deriving it on the phone from `terminal_fallback` inverts the ruling for
//     exactly the sessions it was written about.
//   - T2-a: an ABSENT record is the status card and both verbs are refused. `SessionView.
//     Capabilities` is a pointer with omitempty precisely so absence is wire-distinguishable
//     (schema.go:244-249) -- and nothing today says what absence MEANS.
//
// THE SEAMS (undefined symbols -> compile-fail RED):
//
//	SessionCapabilities gains: SessionInstance string `json:"session_instance"`
//	SessionCapabilities gains: TerminalControl bool   `json:"terminal_control"`
//	func (SessionCapabilities) Validate() error
//	var ErrCapabilityInconsistent error
//	func (*SessionCapabilities) AllowsTerminalWatch() bool    // nil-safe, both booleans
//	func (*SessionCapabilities) AllowsTerminalControl() bool  // nil-safe, both booleans
//
// NOTE FOR GREEN, STATED RATHER THAN LEFT TO BE DISCOVERED. `TestSessionCapabilities_WireShape`
// pins the record's JSON byte-for-byte at six keys. Adding the two fields above BREAKS it, and
// the repair is a STRENGTHENING and the only shape such an edit may take: the pinned literal
// gains two keys and loses none. If that edit ever removes a key, the rule was weakened.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestR8Record_StructuredAndFallbackIsAnInvalidRecord is amendment T2-b.
//
// The inconsistent record is the shape an attacker, a downlevel machine, a partially-written
// capabilities.json or a future derivation bug produces. It must be REJECTED, not resolved:
// resolving it means choosing which boolean to believe, and either choice is a routing
// decision taken by the reader rather than by the daemon that authored the record.
func TestR8Record_StructuredAndFallbackIsAnInvalidRecord(t *testing.T) {
	cases := []struct {
		name    string
		rec     SessionCapabilities
		wantErr bool
		why     string
	}{
		{
			name:    "structured_and_fallback_together",
			rec:     SessionCapabilities{Provider: "claude", SessionInstance: "i1", StructuredChat: true, TerminalFallback: true},
			wantErr: true,
			why:     "T2 rule 4: a healthy structured session has no route to the fallback, so the pair is unrepresentable",
		},
		{
			name:    "healthy_structured",
			rec:     SessionCapabilities{Provider: "claude", SessionInstance: "i1", StructuredChat: true},
			wantErr: false,
		},
		{
			name:    "fallback_provider",
			rec:     SessionCapabilities{Provider: "opencode", SessionInstance: "i1", TerminalFallback: true},
			wantErr: false,
		},
		{
			name:    "neither_is_the_status_card",
			rec:     SessionCapabilities{Provider: "somecli", SessionInstance: "i1"},
			wantErr: false,
			why:     "T1's third destination: neither structured nor fallback is the honest status card, which is a VALID record",
		},
		{
			name:    "control_without_fallback",
			rec:     SessionCapabilities{Provider: "opencode", SessionInstance: "i1", TerminalControl: true},
			wantErr: true,
			why:     "T6: control is entered ON a fallback surface; terminal_control without terminal_fallback authorizes bytes to a screen that does not exist",
		},
		{
			name:    "no_session_instance",
			rec:     SessionCapabilities{Provider: "opencode", TerminalFallback: true},
			wantErr: true,
			why:     "T8-a: everything bound to 'the session instance' binds to a minted identifier; a record with none cannot bind a generation",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rec.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("Validate() = nil for %+v; want a refusal. %s", tc.rec, tc.why)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Validate() = %v for %+v; want nil", err, tc.rec)
			}
			if tc.wantErr && err != nil && !errors.Is(err, ErrCapabilityInconsistent) {
				t.Errorf("Validate() = %v; want it to wrap ErrCapabilityInconsistent so every seam can "+
					"recognise the same refusal rather than string-matching", err)
			}
		})
	}
}

// TestR8Record_BothGatesReadBothBooleans is the mutation fence for T2-b.
//
// The gate is written as `terminal_fallback && !structured_chat`, BOTH booleans, every time.
// A gate that tests one boolean passes the inconsistent record straight through -- which is
// the exact failure T2-b exists to make impossible -- so this table feeds the inconsistent
// record and demands the STATUS CARD answer from both accessors.
func TestR8Record_BothGatesReadBothBooleans(t *testing.T) {
	inconsistent := &SessionCapabilities{
		Provider: "claude", SessionInstance: "i1",
		StructuredChat: true, TerminalFallback: true, TerminalControl: true,
	}
	if inconsistent.AllowsTerminalWatch() {
		t.Errorf("ADR-017 T2-b: an inconsistent record {structured:true, fallback:true} was allowed to " +
			"watch. A gate written over terminal_fallback alone opens a peek onto a healthy Claude " +
			"session the moment any writer, decoder or attacker produces this pair.")
	}
	if inconsistent.AllowsTerminalControl() {
		t.Errorf("ADR-017 T2-b/T6: an inconsistent record was allowed to enter CONTROL. This is the same " +
			"defect one authority level up: raw bytes into a structured session's live TUI.")
	}

	fallback := &SessionCapabilities{Provider: "opencode", SessionInstance: "i1", TerminalFallback: true}
	if !fallback.AllowsTerminalWatch() {
		t.Errorf("a well-formed terminal_fallback record must be allowed to watch, or the wave ships nothing")
	}
	if fallback.AllowsTerminalControl() {
		t.Errorf("ADR-017 T6-b: terminal_fallback alone must NOT grant control. Control is granted by a " +
			"distinct daemon-authored terminal_control field, so that a session degraded into fallback " +
			"by structured_gap is read-only.")
	}

	controllable := &SessionCapabilities{Provider: "opencode", SessionInstance: "i1", TerminalFallback: true, TerminalControl: true}
	if !controllable.AllowsTerminalControl() {
		t.Errorf("a record authored with terminal_control=true at launch must grant control")
	}
}

// TestR8Record_AnAbsentRecordIsTheStatusCardAndRefusesBothVerbs is amendment T2-a.
//
// A nil record is the COMMON case today, not an edge: sessions launched before this ADR
// ships, resumed sessions, sessions re-adopted by a daemon-restart reconcile, and every
// session started from the TUI reach the phone with `capabilities` absent -- and
// `deriveSessionCapabilities` still has no production caller at all (capability.go:334-344
// says so in as many words). "No record" and `terminal_fallback=false` must take ONE code
// path, and the accessors are nil-safe so that path is not a nil check every caller writes
// for itself and one caller forgets.
func TestR8Record_AnAbsentRecordIsTheStatusCardAndRefusesBothVerbs(t *testing.T) {
	var absent *SessionCapabilities
	if absent.AllowsTerminalWatch() {
		t.Errorf("ADR-017 T2-a: an ABSENT capability record was allowed to watch. Absence must fail " +
			"closed to the status card: it is the state of every pre-R8 session, every resumed session " +
			"and every TUI-launched session, and 'unknown, therefore allow' opens the peek on all of them.")
	}
	if absent.AllowsTerminalControl() {
		t.Errorf("ADR-017 T2-a: an ABSENT capability record was allowed to enter control")
	}
	if err := absent.Validate(); err == nil {
		t.Errorf("ADR-017 T2-a: Validate() on an absent record returned nil. Absence is not validity; a " +
			"caller that validates before routing must be told there is nothing to route on.")
	}
}

// TestR8Record_SetStructuredChatNeverTouchesTerminalControl is D-DEGRADE-ORIGIN's first
// fence, and it guards against a change that will look like a bug fix.
//
// `SetStructuredChat` forces `TerminalFallback = true` on a degrade, "so the session gains
// the sanitized surface it lost structured chat for". The symmetrical-looking next step --
// also setting `TerminalControl = true`, for consistency -- silently inverts T6-b and hands
// a keyboard to a degraded Claude session whose live TUI has an uncharacterized input region
// (the `expected_input_revision` gap ADR-017 T9 discloses as still open). This test is the
// only thing that will be standing between that edit and the phone.
func TestR8Record_SetStructuredChatNeverTouchesTerminalControl(t *testing.T) {
	for _, start := range []bool{false, true} {
		rec := SessionCapabilities{
			Provider: "claude", SessionInstance: "i1",
			StructuredChat: true, TerminalControl: start,
		}
		if err := rec.SetStructuredChat(false); err != nil {
			t.Fatalf("SetStructuredChat(false) = %v; a degrade must be honoured", err)
		}
		if !rec.TerminalFallback {
			t.Errorf("a degrade must still force terminal_fallback true (the session needs a surface)")
		}
		if rec.TerminalControl != start {
			t.Errorf("ADR-017 T6-b / D-DEGRADE-ORIGIN: SetStructuredChat changed terminal_control from %v "+
				"to %v. Control authority is granted only where terminal_fallback was authored true AT "+
				"LAUNCH; a degrade grants a screen, never a keyboard.", start, rec.TerminalControl)
		}
	}
}

// TestR8Record_ADegradedRecordStaysValid pins the interaction between the two new rules: a
// degrade produces {structured:false, fallback:true}, which Validate must ACCEPT. A validity
// rule that rejected the output of the one sanctioned mutation path would make every degrade
// unrouteable, which fails closed in the wrong direction -- to a session with no surface at
// all rather than to the honest status card.
func TestR8Record_ADegradedRecordStaysValid(t *testing.T) {
	rec := SessionCapabilities{Provider: "claude", SessionInstance: "i1", StructuredChat: true}
	if err := rec.SetStructuredChat(false); err != nil {
		t.Fatalf("SetStructuredChat(false): %v", err)
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("a degraded record must remain valid, or every degrade becomes unrouteable: %v", err)
	}
	if !rec.AllowsTerminalWatch() {
		t.Errorf("a degraded session must be able to WATCH -- that is what the degrade gives it")
	}
	if rec.AllowsTerminalControl() {
		t.Errorf("ADR-017 T6-b: a degraded session must NOT be able to control")
	}
}

// TestR8Record_SessionInstanceRidesTheWire is T8-a's wire half.
//
// The instance is carried in the record, in every snapshot and in every control body, so it
// must be on the wire with NO omitempty: an absent instance and an empty instance must stay
// distinguishable, for the same reason RemoteProfileV1's fields may not carry omitempty --
// a reader routes on it, and "the key was missing" and "the key was empty" are different
// facts about a machine.
func TestR8Record_SessionInstanceRidesTheWire(t *testing.T) {
	b, err := json.Marshal(SessionCapabilities{Provider: "opencode", TerminalFallback: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"session_instance"`, `"terminal_control"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("ADR-017 T8-a/T6-b: %s is absent from the marshalled record (%s). Both fields are "+
				"routed on, and omitempty would make an absent key indistinguishable from a false one.",
				key, b)
		}
	}
}
