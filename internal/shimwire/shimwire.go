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
	// TypeSnapshotReq is a daemon->shim one-shot snapshot request: the shim
	// answers on the SAME connection with its current grid snapshot (the same
	// encoding an attach snapshot uses, chunked iff SnapshotChunking was
	// negotiated) WITHOUT installing a subscriber — it never supersedes an
	// attached controller's stream, by construction. Sent only to a shim whose
	// hello reply advertised SnapshotOnly; callers use a DEDICATED connection
	// (a snapshot interleaved into an active attach stream on one connection is
	// unsupported). Added by the C3 fix wave for the grid tap.
	TypeSnapshotReq = "snapshot_req"
	// TypeBackendAttach is the daemon->shim GO-AHEAD of ADR-013 §R7.2e's spawn-ordering
	// handshake (Wave R7, Mirror M4.1). A shim configured with a backend binds its control
	// socket, spawns the backend, waits for the backend's socket to become servable, writes
	// backend.json -- and then BLOCKS, before spawning the agent, until this arrives.
	//
	// WHY THE VERB EXISTS AT ALL. launchConfirmTimeout waits for the shim's CONTROL socket,
	// which is bound BEFORE either process, so there is no edge the daemon can act on. The
	// property it buys -- the daemon is a connected JSON-RPC client BEFORE THE AGENT PROCESS
	// EXISTS -- is what makes it impossible to miss a `thread/started` and what removes the
	// gate's cold-start rollout race rather than retrying around it. It rides the per-session
	// control socket that already exists: no new listener, no new socket, no new auth surface.
	//
	// AgentArgs is appended to the agent argv VERBATIM. EMPTY IS THE ORDINARY CASE, not a
	// defect: it means "go ahead, I am connected, and I am not handing you a thread id".
	TypeBackendAttach = "backend_attach"
	// TypeSubmit is a daemon->shim SUBMIT TRANSACTION: one message's text and the
	// carriage return that runs it, delivered under a single hold of the PTY's only
	// serialized writer -- or refused, having written nothing.
	//
	// WHY IT IS A CONTROL FRAME AND NOT TWO TDataIn WRITES (Slice 0,
	// agents-tracker-bzfe). Text and CR sent as ordinary input cannot be made atomic
	// from the daemon's side: the daemon holds no lock the owner's own keystrokes
	// respect, and two phone sends racing each other produce text_A, text_B, CR, CR --
	// one submitted concatenation and one empty submit. Only the shim owns the writer,
	// so only the shim can make "nobody has written since the last submit, write the
	// text, wait the frame gap, write the return" one indivisible act.
	//
	// IT CLAIMS NOTHING ABOUT THE CLI'S INPUT REGION. The precondition is a fact about
	// the PTY, not about what the agent has drawn on it: bytes written since the last
	// submit. That is deliberately weaker than ADR-017:175's expected_input_revision and
	// is the reason this can ship without characterizing anybody's composer.
	TypeSubmit = "submit"
	// TypeSubmitResult is the shim's answer to exactly one TypeSubmit: Refused carries
	// the reason and is empty on success. A submit is answered on the same connection,
	// in order, so a caller may hold one in flight and wait for it.
	TypeSubmitResult = "submit_result"
)

// RefusedInputBusy is the one STABLE reason token a TypeSubmitResult carries: somebody
// has written to this PTY since the last submit, so the message was not written. It is a
// token rather than a sentence because the daemon maps it onto the wire's own refusal
// code, and a sentence would make that a string comparison against prose.
const RefusedInputBusy = "input_busy"

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
	// SnapshotOnly is an OPTIONAL hello capability advertised by the SHIM: it
	// will answer TypeSnapshotReq with a non-subscribing one-shot snapshot. An
	// old shim never sets it, so a new daemon falls back to attach-based
	// sampling (G-D); an old daemon ignores it entirely.
	SnapshotOnly bool `json:"snapshot_only,omitempty"` // hello (shim reply)
	// SnapshotLen is the snapshot's total byte length, carried in a TypeSnapshotInfo
	// preamble so the daemon reader reassembles exactly that many chunk bytes.
	SnapshotLen int `json:"snapshot_len,omitempty"` // snapshot_info
	// AgentArgs rides a backend_attach: extra argv elements the shim appends to the agent's
	// own argv verbatim before it spawns it. Absent on every other message type, and absent
	// on a bare go-ahead -- omitempty keeps an old shim's decode identical either way.
	AgentArgs []string `json:"agent_args,omitempty"` // backend_attach
	// SubmitTransaction is an OPTIONAL hello capability advertised by the SHIM: it
	// will answer TypeSubmit atomically. An old shim never sets it, so a new daemon
	// degrades to today's two unlocked writes (G-D) and the merge stays possible until
	// the shim is replaced -- which is a disclosed degrade, not a silent one.
	SubmitTransaction bool `json:"submit_transaction,omitempty"` // hello (shim reply)
	// Text rides a TypeSubmit: the whole of one message, without its carriage return.
	// The shim adds the return itself, because the point of the verb is that nothing
	// may land between the two.
	Text string `json:"text,omitempty"` // submit
	// Refused rides a TypeSubmitResult and is EMPTY ON SUCCESS. A non-empty reason
	// means nothing was written.
	Refused string `json:"refused,omitempty"` // submit_result
}

// Caps is the set of OPTIONAL capabilities a peer advertised in its hello
// message (all negotiated without bumping WireVersion; an old peer that sets
// none degrades to the pre-capability behavior, G-D). The daemon captures the
// shim's reply Caps at hello and threads them to the code that must ENFORCE
// them (e.g. protocol.readSnapshot reassembles a chunked snapshot only when
// SnapshotChunking was advertised — R1.2.2).
type Caps struct {
	SnapshotChunking  bool
	SnapshotOnly      bool
	SubmitTransaction bool
}

// Caps extracts the capability fields from a hello Control.
func (c Control) Caps() Caps {
	return Caps{
		SnapshotChunking:  c.SnapshotChunking,
		SnapshotOnly:      c.SnapshotOnly,
		SubmitTransaction: c.SubmitTransaction,
	}
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
