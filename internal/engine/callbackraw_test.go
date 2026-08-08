package engine

// FAILING-FIRST (TDD RED, GG-5) for ADR-010 §6's carriage, layer 1: `engine.Callback` gains
// `Raw json.RawMessage` alongside `Payload`.
//
// THE WHOLE POINT OF THE FIELD IS THAT THE ENGINE DOES NOT READ IT. §6 is explicit: the raw
// body is UNTRUSTED TOOL OUTPUT, `deriveDims` never reads it, status is still derived from the
// flat descriptor and the flat payload, and B5's degrade-to-none survives. So the two
// assertions here are (a) the field exists and survives a callback, and (b) a body crafted to
// look exactly like a status dimension changes nothing about the status the engine commits.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// hostileRaw is a body shaped to be mistaken for status dimensions if anything downstream ever
// flattened it: the reserved keys the payload path already guards, plus the subtype field the
// Notification descriptor maps on. A CLI's hook body is tool output — it may contain anything,
// including a user's prompt text quoting these keys.
const hostileRaw = `{"turn":"active","interaction":"permission","notification_type":"permission"}`

// TestCallbackRaw_NeverInfluencesTheDerivedStatus drives the SAME callback twice — once bare,
// once carrying hostileRaw — and requires the committed status to be identical. A `permission`
// on the right-hand side would be the engine asserting a prompt it cannot confirm (B5).
func TestCallbackRaw_NeverInfluencesTheDerivedStatus(t *testing.T) {
	run := func(raw json.RawMessage) status.Status {
		t.Helper()
		rec := &emitRecorder{}
		e := newEngine(newClock(), constCPU(0), rec, time.Minute, time.Second)
		e.RegisterSession("s1", "tok1", 1, mappingSources())
		// An idle-subtype Notification: the descriptor's NOMINAL interaction is permission and
		// the payload subtype is what demotes it to none. If Raw were ever consulted, the
		// hostile body's `notification_type` would flip it back.
		cb := Callback{
			SessionID: "s1", Token: "tok1", Sequence: 1, Event: "Notification",
			Payload: map[string]string{"notification_type": "idle"},
			Raw:     raw,
		}
		if err := e.HandleCallback(cb); err != nil {
			t.Fatalf("HandleCallback(raw=%d bytes): %v", len(raw), err)
		}
		call, ok := rec.last()
		if !ok {
			t.Fatalf("HandleCallback(raw=%d bytes) emitted no status change", len(raw))
		}
		return call.s
	}

	bare := run(nil)
	withRaw := run(json.RawMessage(hostileRaw))
	if bare != withRaw {
		t.Errorf("a callback carrying a raw body committed %+v; the same callback without one committed %+v. "+
			"ADR-010 §6: deriveDims never reads Raw, status is derived from the flat descriptor and the "+
			"flat payload, and a raw body is untrusted tool output that never influences status", withRaw, bare)
	}
	if withRaw.Interaction != status.InteractionNone {
		t.Errorf("interaction = %q; want %q -- the payload subtype is `idle` and B5 degrades anything "+
			"unconfirmed to none. A permission here is the engine believing the body", withRaw.Interaction, status.InteractionNone)
	}
}

// TestCallbackRaw_SurvivesTheHookWireVerbatim. The field is carriage: it is JSON-encoded by
// hookclient.Post and decoded daemon-side, and the producer downstream re-parses the CLI's own
// body out of it, so a field that arrived re-ordered or with a key dropped would break the
// shaper. The shipped wire crosses `<`, `>` and `&` byte-exact too (Post encodes with
// SetEscapeHTML(false); fenced in internal/hookclient/post_verbatim_test.go); this test pins
// the round-trip on a plain body, the one below pins the semantic floor for any encoder.
func TestCallbackRaw_SurvivesTheHookWireVerbatim(t *testing.T) {
	in := Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "PreToolUse",
		Raw: json.RawMessage(`{"tool_name":"Read","tool_input":{"file_path":"/tmp/x.txt"}}`)}
	enc, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal callback: %v", err)
	}
	var out Callback
	if err := json.Unmarshal(enc, &out); err != nil {
		t.Fatalf("unmarshal callback: %v", err)
	}
	if string(out.Raw) != string(in.Raw) {
		t.Errorf("Raw crossed the wire as %s; want %s -- the nested `tool_input` object is exactly what "+
			"the flattened payload cannot carry, and it must arrive unaltered", out.Raw, in.Raw)
	}
}

// TestCallbackRaw_SemanticContentSurvivesAnyEncoder pins the FLOOR the carriage must never
// fall through: whatever encoder carries a Callback, the captured body decodes to the same
// value, so every shaped field is the CLI's own. The shipped wire is stronger than this
// floor — hookclient.Post encodes with SetEscapeHTML(false), so `<`, `>` and `&` cross
// byte-exact (fenced where Post lives: internal/hookclient/post_verbatim_test.go; added
// after the i1 review measured default HTML escaping expanding an untrusted body six wire
// bytes per input byte, enough to push a near-cap body past a transport limit and kill the
// session's status post — the inversion ADR-010 §6 forbids). This test deliberately uses
// bare json.Marshal, the worst sanctioned encoder, so the semantic floor holds even for a
// future caller that forgets the escaping discipline.
func TestCallbackRaw_SemanticContentSurvivesAnyEncoder(t *testing.T) {
	raw := json.RawMessage(`{"tool_name":"Bash","tool_input":{"command":"grep -c a b && echo <done>"}}`)

	enc, err := json.Marshal(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "PreToolUse", Raw: raw})
	if err != nil {
		t.Fatalf("marshal callback: %v", err)
	}
	var out Callback
	if err := json.Unmarshal(enc, &out); err != nil {
		t.Fatalf("unmarshal callback: %v", err)
	}
	// The semantic floor: the shaper unmarshals, so the command it reads is the command the
	// CLI wrote, whatever encoding carried it.
	var got, want struct {
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal(out.Raw, &got); err != nil {
		t.Fatalf("decode the body as a shaper would: %v", err)
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode the CLI's own body: %v", err)
	}
	if got.ToolInput.Command != want.ToolInput.Command {
		t.Errorf("the shaper reads command %q; the CLI wrote %q -- the escaping must be an ENCODING "+
			"difference and nothing more", got.ToolInput.Command, want.ToolInput.Command)
	}
}

// TestCallbackRaw_IsOmittedWhenAbsent keeps the ordinary status post the size it has always
// been: every hook event that declares no capture row posts one, and an empty `"raw":null` on
// each would be bytes on the daemon socket for nothing.
func TestCallbackRaw_IsOmittedWhenAbsent(t *testing.T) {
	enc, err := json.Marshal(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "Stop"})
	if err != nil {
		t.Fatalf("marshal callback: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(enc, &fields); err != nil {
		t.Fatalf("decode encoded callback: %v", err)
	}
	if _, present := fields["raw"]; present {
		t.Errorf("a callback with no captured body encoded %s; want no `raw` key at all", enc)
	}
}
