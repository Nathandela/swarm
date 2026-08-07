package adapter

// ADR-010 conformance obligation 1 — Interactions is PURE and TOTAL, on the same
// terms as ExtractConversationID: it never panics on a nil, truncated, garbage or
// unbounded body, and it is deterministic. It is fed untrusted tool output
// (ADR-010 §6), so a panic here would take down the ingest path.
//
// This fuzz targets a representative conformant shaper (captureAdapter); every
// real adapter's totality is probed by CheckConformance's battery, and a per-CLI
// fuzz target lives beside each producer when it lands.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func FuzzInteractionsTotality(f *testing.F) {
	f.Add("UserPromptSubmit", []byte(`{"prompt":"hi"}`))
	f.Add("PreToolUse", []byte(`{"tool":"Read","path":"a.go","ref":"t-1"}`))
	f.Add("PermissionRequest", []byte(`{"tool":"Bash","ref":"bash-1"}`))
	f.Add("PreToolUse", []byte(`{"tool_input":{`))            // truncated
	f.Add("", []byte(nil))                                    // no event, no body
	f.Add("Stop", []byte{0x00, 0xff, 0x1b, 0x5b})             // garbage bytes
	f.Add("PreToolUse", []byte(strings.Repeat("[", 512)))     // deeply nested
	f.Add("UserPromptSubmit", []byte(`{"prompt":"多字节 バイト"}`)) // multibyte

	a := captureAdapter{}
	f.Fuzz(func(t *testing.T, event string, raw []byte) {
		p := HookPayload{Event: event, Raw: json.RawMessage(raw)}
		got1 := a.Interactions(p) // must not panic
		got2 := a.Interactions(p) // deterministic
		if !reflect.DeepEqual(got1, got2) {
			t.Fatalf("nondeterministic: %+v then %+v", got1, got2)
		}
		for i, in := range got1 {
			if err := in.Validate(); err != nil {
				t.Fatalf("shaped item %d is malformed: %v", i, err)
			}
		}
		// Decision is total on the same terms: any ref/verdict, no panic.
		if _, ok := a.Decision(event, string(raw)); ok {
			_ = ok
		}
	})
}
