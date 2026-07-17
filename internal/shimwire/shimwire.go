// Package shimwire is the G2 daemon<->shim message set (build-plan.md gap
// resolution G2): the JSON control payloads carried inside wire.TControl
// frames on the per-session shim socket.
//
// Decode is intentionally tolerant: unknown fields and unknown Type strings
// are not errors, so a newer daemon speaking a superset of ops does not break
// an older shim's decoder (Epic 5 owns the semantics; this package only owns
// the wire format).
package shimwire

import "encoding/json"

// Version (the shimwire protocol version carried in a hello message's
// WireVersion field) is defined in version.go. It is split into build-tagged
// files ONLY so the E14.3 compat-matrix test can compile adjacent-version shim
// binaries; the default build is unchanged at 1. See version.go.

// Message type vocabulary, shared verbatim between daemon and shim.
const (
	TypeHello      = "hello"
	TypeAttach     = "attach"
	TypeResize     = "resize"
	TypeSignal     = "signal"
	TypeExitReport = "exit_report"
)

// Signal vocabulary for a Control{Type: TypeSignal}.
const (
	SigTerm = "term"
	SigKill = "kill"
)

// Control is the single message envelope for every shimwire control message;
// which fields are meaningful depends on Type.
type Control struct {
	Type        string `json:"type"`
	WireVersion int    `json:"wire_version,omitempty"` // hello
	Cols        int    `json:"cols,omitempty"`         // resize
	Rows        int    `json:"rows,omitempty"`         // resize
	Sig         string `json:"sig,omitempty"`          // signal: SigTerm|SigKill
	ExitCode    *int   `json:"exit_code,omitempty"`    // exit_report
	ExitSignal  string `json:"exit_signal,omitempty"`  // exit_report
}

// Encode serializes c to its JSON wire form.
func Encode(c Control) ([]byte, error) {
	return json.Marshal(c)
}

// Decode parses a Control from its JSON wire form. Unknown fields and an
// unrecognized Type string are tolerated, not errors; only malformed JSON
// fails.
func Decode(b []byte) (Control, error) {
	var c Control
	if err := json.Unmarshal(b, &c); err != nil {
		return Control{}, err
	}
	return c, nil
}
