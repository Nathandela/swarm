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
	// TypeSnapshotInfo is the shim->daemon preamble that precedes a CHUNKED
	// snapshot: it declares the snapshot's total byte length up front (SnapshotLen)
	// so the daemon reader knows how many TSnapshot chunk bytes to reassemble
	// WITHOUT waiting for a following frame (an idle session must not hang). It is
	// sent only when snapshot chunking was negotiated at hello (see SnapshotChunking);
	// otherwise the shim sends today's single TSnapshot frame. Mirrors the
	// daemon->client OpLease.SnapshotLen preamble.
	TypeSnapshotInfo = "snapshot_info"
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
	// SnapshotChunking is an OPTIONAL hello capability advertised by BOTH peers:
	// the daemon sets it in its hello to tell the shim it can reassemble a chunked
	// snapshot, and the shim sets it in its hello reply to tell the daemon it will
	// chunk. It is negotiated at hello WITHOUT bumping WireVersion (it stays 1);
	// Decode tolerates it as an unknown field on an old peer, which never sets it,
	// so an old<->new pair degrades to today's single-frame snapshot path (G-D).
	SnapshotChunking bool `json:"snapshot_chunking,omitempty"` // hello (both directions)
	// SnapshotLen is the snapshot's total byte length, carried in a TypeSnapshotInfo
	// preamble so the daemon reader reassembles exactly that many chunk bytes.
	SnapshotLen int `json:"snapshot_len,omitempty"` // snapshot_info
}

// Caps is the set of OPTIONAL capabilities a peer advertised in its hello
// message (all negotiated without bumping WireVersion; an old peer that sets
// none degrades to the pre-capability behavior, G-D). The daemon captures the
// shim's reply Caps at hello and threads them to the code that must ENFORCE
// them (e.g. protocol.readSnapshot reassembles a chunked snapshot only when
// SnapshotChunking was advertised — R1.2.2).
type Caps struct {
	SnapshotChunking bool
}

// Caps extracts the capability fields from a hello Control.
func (c Control) Caps() Caps {
	return Caps{SnapshotChunking: c.SnapshotChunking}
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
