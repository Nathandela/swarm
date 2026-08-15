package schema

// FAILING-FIRST (TDD RED, GG-5) tests for ADR-017 T2 / playbook 6.2's per-session
// capability record: a daemon-authored, per-session-instance record naming the provider,
// its detected version, the adapter revision that produced the record, and three of the
// seams and fields T2's table names at minimum -- structured_chat, terminal_fallback,
// interrupt. T2 additionally names probe_result and the steer/approvals/history seams;
// this slice does not carry them yet (bd agents-tracker-hggx.2.3) -- adding them needs a
// new RED pass, since TestSessionCapabilities_WireShape below pins the exact 6-key JSON
// shape byte-for-byte.
//
// WIRE CARRIAGE: the record rides schema.SessionView ADDITIVELY (T2: "The phone renders
// from that record; it never infers support from whether a transcript happens to be
// empty"), exactly like SessionView's existing additive fields (RemoteControlled,
// SpawnedFrom/SpawnIntent) -- a pointer with omitempty, so an older daemon's roster row
// stays byte-identical and a NIL record is honestly distinguishable from a record that
// says structured_chat=false.
//
// DEGRADE-ONLY: T2 rule 2 -- "A runtime integrity failure may only degrade the record
// ... it cannot upgrade a fallback session in place" -- is enforced IN THE SETTER
// (SetStructuredChat), the one mutation path a capability record has after it is
// authored at launch.
//
// THE SEAMS these tests pin (undefined symbols -> compile-fail RED):
//
//	type SessionCapabilities struct {
//	    Provider          string
//	    ProviderVersion   string
//	    AdapterRevision   string
//	    StructuredChat    bool
//	    TerminalFallback  bool
//	    Interrupt         bool
//	}
//	SessionView gains: Capabilities *SessionCapabilities `json:"capabilities,omitempty"`
//	func (*SessionCapabilities) SetStructuredChat(v bool) error
//	var ErrCapabilityUpgrade error

import (
	"encoding/json"
	"errors"
	"testing"
)

// wireSessionCapabilities is the committed JSON of testSessionCapabilities below.
const wireSessionCapabilities = `{"provider":"claude","provider_version":"1.2.3","adapter_revision":"rev-1","structured_chat":true,"terminal_fallback":false,"interrupt":false}`

func testSessionCapabilities() SessionCapabilities {
	return SessionCapabilities{
		Provider:         "claude",
		ProviderVersion:  "1.2.3",
		AdapterRevision:  "rev-1",
		StructuredChat:   true,
		TerminalFallback: false,
		Interrupt:        false,
	}
}

// TestSessionCapabilities_WireShape pins field names and order (9 rule 4: the wire shape
// itself is the criterion).
func TestSessionCapabilities_WireShape(t *testing.T) {
	got, err := json.Marshal(testSessionCapabilities())
	if err != nil {
		t.Fatalf("marshal SessionCapabilities: %v", err)
	}
	if string(got) != wireSessionCapabilities {
		t.Fatalf("SessionCapabilities wire shape =\n  %s\nwant\n  %s", got, wireSessionCapabilities)
	}
}

// TestSessionCapabilities_DecodesFromTheCommittedBytes closes the loop.
func TestSessionCapabilities_DecodesFromTheCommittedBytes(t *testing.T) {
	var got SessionCapabilities
	if err := json.Unmarshal([]byte(wireSessionCapabilities), &got); err != nil {
		t.Fatalf("unmarshal committed bytes: %v", err)
	}
	if got != testSessionCapabilities() {
		t.Fatalf("decoded record = %+v; want %+v", got, testSessionCapabilities())
	}
}

// TestSessionView_CapabilitiesIsAdditiveAndOmitempty: a SessionView with no capability
// record carries no "capabilities" key at all (a pre-ADR-017 roster row, or a session the
// daemon has not stamped yet, is byte-identical to today's wire shape); a stamped one
// carries the full nested record, so a phone can tell "no record was sent" apart from
// "the record says structured_chat=false" -- the distinction T2 rule 3 requires ("never
// inferring support from whether a transcript happens to be empty").
func TestSessionView_CapabilitiesIsAdditiveAndOmitempty(t *testing.T) {
	t.Run("absent when nil", func(t *testing.T) {
		b, err := json.Marshal(SessionView{EndpointID: "ep1", ID: "ep1/a", Agent: "claude"})
		if err != nil {
			t.Fatalf("marshal SessionView: %v", err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(b, &keys); err != nil {
			t.Fatalf("SessionView is not a JSON object: %v", err)
		}
		if _, ok := keys["capabilities"]; ok {
			t.Fatalf("SessionView JSON carries a %q key with no capability record set; want it absent (%s)", "capabilities", b)
		}
	})

	t.Run("present when set, every field intact", func(t *testing.T) {
		caps := testSessionCapabilities()
		in := SessionView{EndpointID: "ep1", ID: "ep1/a", Agent: "claude", Capabilities: &caps}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal SessionView: %v", err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(b, &keys); err != nil {
			t.Fatalf("SessionView is not a JSON object: %v", err)
		}
		raw, ok := keys["capabilities"]
		if !ok {
			t.Fatalf("SessionView JSON missing %q key when Capabilities is set: %s", "capabilities", b)
		}
		if string(raw) != wireSessionCapabilities {
			t.Fatalf("nested capabilities = %s; want %s", raw, wireSessionCapabilities)
		}

		var got SessionView
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal SessionView: %v", err)
		}
		if got.Capabilities == nil {
			t.Fatalf("round trip lost the capability record entirely")
		}
		if *got.Capabilities != caps {
			t.Fatalf("round-tripped capability record = %+v; want %+v", *got.Capabilities, caps)
		}
	})
}

// TestSessionCapabilities_SetStructuredChat_DegradeOnlyNeverUpgrades is the table pin for
// ADR-017 T2 rule 2: the setter allows a healthy record to degrade (structured -> false,
// which forces terminal_fallback true), is idempotent in either steady state, and REFUSES
// an upgrade attempt (false -> true) leaving the record byte-for-byte unchanged.
func TestSessionCapabilities_SetStructuredChat_DegradeOnlyNeverUpgrades(t *testing.T) {
	cases := []struct {
		name    string
		start   SessionCapabilities
		setTo   bool
		wantErr bool
		want    SessionCapabilities
	}{
		{
			name:  "a healthy structured session degrades to fallback",
			start: SessionCapabilities{Provider: "claude", StructuredChat: true, TerminalFallback: false},
			setTo: false,
			want:  SessionCapabilities{Provider: "claude", StructuredChat: false, TerminalFallback: true},
		},
		{
			name:    "a fallback session refuses to upgrade back to structured",
			start:   SessionCapabilities{Provider: "codex", StructuredChat: false, TerminalFallback: true},
			setTo:   true,
			wantErr: true,
			want:    SessionCapabilities{Provider: "codex", StructuredChat: false, TerminalFallback: true},
		},
		{
			name:  "degrading an already-degraded record is idempotent",
			start: SessionCapabilities{Provider: "opencode", StructuredChat: false, TerminalFallback: true},
			setTo: false,
			want:  SessionCapabilities{Provider: "opencode", StructuredChat: false, TerminalFallback: true},
		},
		{
			name:  "setting true on an already-structured record is not an upgrade",
			start: SessionCapabilities{Provider: "claude", StructuredChat: true, TerminalFallback: false},
			setTo: true,
			want:  SessionCapabilities{Provider: "claude", StructuredChat: true, TerminalFallback: false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.start
			err := got.SetStructuredChat(tc.setTo)
			if tc.wantErr {
				if !errors.Is(err, ErrCapabilityUpgrade) {
					t.Fatalf("SetStructuredChat(%v) error = %v; want ErrCapabilityUpgrade", tc.setTo, err)
				}
			} else if err != nil {
				t.Fatalf("SetStructuredChat(%v): unexpected error %v", tc.setTo, err)
			}
			if got != tc.want {
				t.Fatalf("after SetStructuredChat(%v) = %+v; want %+v", tc.setTo, got, tc.want)
			}
		})
	}
}
