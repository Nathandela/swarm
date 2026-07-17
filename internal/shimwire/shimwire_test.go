// Package shimwire is the G2 daemon<->shim message set (build-plan.md gap
// resolution G2): the JSON control payloads carried inside wire.TControl frames
// on the per-session shim socket. These are FAILING-FIRST tests (Epic 4
// test-design pass) against the frozen production API a separate implementer
// will build; until it exists this package does not compile and the only errors
// must be "undefined".
//
// FROZEN CONTRACT (orchestrator brief, Epic 4):
//
//	const Version = 1
//	type Control struct {
//	    Type        string `json:"type"`         // hello, attach, resize, signal, exit_report
//	    WireVersion int    `json:"wire_version,omitempty"` // hello
//	    Cols, Rows  int    `json:"cols|rows,omitempty"`     // resize
//	    Sig         string `json:"sig,omitempty"`          // signal: "term"|"kill"
//	    ExitCode    *int   `json:"exit_code,omitempty"`    // exit_report
//	    ExitSignal  string `json:"exit_signal,omitempty"`  // exit_report
//	}
//
// DESIGN PINS beyond the brief (this suite is the contract for them):
//   - Encode/Decode helpers give the package a behavioral surface parallel to
//     wire.WriteFrame/ReadFrame, rather than making every caller reach for
//     encoding/json. Decode of unknown fields and unknown Type strings must NOT
//     error (forward compat: Epic 5 owns semantics, the codec stays permissive).
//   - Message "type" and signal vocab are exported constants so callers share
//     one spelling: TypeHello/TypeAttach/TypeResize/TypeSignal/TypeExitReport
//     and SigTerm/SigKill.
package shimwire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func intPtr(i int) *int { return &i }

func TestVersionIsOne(t *testing.T) {
	if Version != 1 {
		t.Fatalf("Version = %d, want 1", Version)
	}
}

func TestTypeConstants(t *testing.T) {
	// The vocabulary is fixed strings; pin the exact spellings so daemon and
	// shim can never disagree by a typo.
	cases := map[string]string{
		TypeHello:      "hello",
		TypeAttach:     "attach",
		TypeResize:     "resize",
		TypeSignal:     "signal",
		TypeExitReport: "exit_report",
		SigTerm:        "term",
		SigKill:        "kill",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("constant = %q, want %q", got, want)
		}
	}
}

func TestRoundTrip_EveryMessageType(t *testing.T) {
	cases := []struct {
		name string
		ctrl Control
	}{
		{"hello", Control{Type: TypeHello, WireVersion: Version}},
		{"attach", Control{Type: TypeAttach}},
		{"resize", Control{Type: TypeResize, Cols: 120, Rows: 40}},
		{"signal-term", Control{Type: TypeSignal, Sig: SigTerm}},
		{"signal-kill", Control{Type: TypeSignal, Sig: SigKill}},
		{"exit-report-clean", Control{Type: TypeExitReport, ExitCode: intPtr(0), ExitSignal: ""}},
		{"exit-report-code", Control{Type: TypeExitReport, ExitCode: intPtr(7)}},
		{"exit-report-signal", Control{Type: TypeExitReport, ExitCode: intPtr(137), ExitSignal: "SIGKILL"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Encode(tc.ctrl)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got, err := Decode(b)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !reflect.DeepEqual(got, tc.ctrl) {
				t.Errorf("round-trip mismatch:\n got = %+v\nwant = %+v", got, tc.ctrl)
			}
		})
	}
}

func TestExitCode_NilVersusZeroPreserved(t *testing.T) {
	// The pointer distinguishes "no exit code yet" (nil) from "exited 0"
	// (*0); the round-trip must preserve both, because a daemon reads exit_code
	// to decide a session is finished (S-4).
	withZero := Control{Type: TypeExitReport, ExitCode: intPtr(0)}
	b, err := Encode(withZero)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(string(b), `"exit_code":0`) {
		t.Errorf("encoded *0 exit code dropped from JSON: %s", b)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want non-nil *0", got.ExitCode)
	}

	noCode := Control{Type: TypeExitReport}
	b, err = Encode(noCode)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if strings.Contains(string(b), "exit_code") {
		t.Errorf("nil exit code must be omitted, got: %s", b)
	}
	got, err = Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.ExitCode != nil {
		t.Errorf("ExitCode = %v, want nil", got.ExitCode)
	}
}

func TestEncode_OmitsZeroValuedOptionalFields(t *testing.T) {
	// A hello carries only its own fields; resize/signal/exit fields are
	// omitempty and must not appear, keeping messages minimal and unambiguous.
	b, err := Encode(Control{Type: TypeHello, WireVersion: Version})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	s := string(b)
	for _, absent := range []string{"cols", "rows", "sig", "exit_code", "exit_signal"} {
		if strings.Contains(s, absent) {
			t.Errorf("hello message unexpectedly contains %q: %s", absent, s)
		}
	}
	if !strings.Contains(s, `"type":"hello"`) || !strings.Contains(s, `"wire_version":1`) {
		t.Errorf("hello missing required fields: %s", s)
	}
}

func TestDecode_UnknownTypePreserved(t *testing.T) {
	// Forward compat: a message whose "type" the shim does not recognize must
	// decode without error, carrying the unknown string through verbatim, so a
	// newer daemon speaking a superset of ops does not break an older shim's
	// decoder (Epic 5 handles the semantics).
	got, err := Decode([]byte(`{"type":"some_future_op","cols":10}`))
	if err != nil {
		t.Fatalf("Decode(unknown type) errored: %v", err)
	}
	if got.Type != "some_future_op" {
		t.Errorf("Type = %q, want the unknown string preserved", got.Type)
	}
}

func TestDecode_UnknownFieldsTolerated(t *testing.T) {
	// Unknown JSON fields must be ignored, not rejected (forward compat).
	got, err := Decode([]byte(`{"type":"hello","wire_version":1,"future_field":{"nested":true},"endpoint_id":"x"}`))
	if err != nil {
		t.Fatalf("Decode(unknown fields) errored: %v", err)
	}
	if got.Type != TypeHello || got.WireVersion != 1 {
		t.Errorf("known fields lost: %+v", got)
	}
}

func TestDecode_RejectsMalformedJSON(t *testing.T) {
	// Garbage is still an error — permissiveness is about unknown-but-valid
	// JSON, not broken bytes.
	if _, err := Decode([]byte("{not json")); err == nil {
		t.Errorf("Decode(garbage) returned nil error, want a decode error")
	}
}

func TestEncode_IsValidJSON(t *testing.T) {
	// Whatever Encode emits must be parseable as JSON (it is carried as the
	// payload of a wire.TControl frame and decoded by the peer).
	b, err := Encode(Control{Type: TypeResize, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Encode output is not valid JSON: %v (%s)", err, b)
	}
}
