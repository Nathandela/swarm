package claude

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/codex"
)

const claudeIdentityUUID = "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"

// TestConversationIdentitySource_ClaudeImplementsTheOptionalExtension compile-pins the
// additive adapter seam. Identity extraction is optional, just like InteractionSource; it must
// not widen the frozen Adapter method set or make providers without hook identity unsupported.
func TestConversationIdentitySource_ClaudeImplementsTheOptionalExtension(t *testing.T) {
	var _ adapter.ConversationIdentitySource = New()
	var _ func(adapter.Adapter) (adapter.ConversationIdentitySource, bool) = adapter.AsConversationIdentitySource //nolint:staticcheck // compile-pin the exact optional seam

	if src, ok := adapter.AsConversationIdentitySource(New()); !ok || src == nil {
		t.Fatalf("AsConversationIdentitySource(claude) = (%v, %v), want a non-nil source", src, ok)
	}
}

// TestConversationIdentitySource_ClaudeAcceptsOnlyOneCanonicalTopLevelSessionID is the parser's
// fail-closed table. Claude's authenticated body owns one top-level session_id; prose, nested
// values, duplicate keys, alternate spellings and trailing JSON are never candidates.
func TestConversationIdentitySource_ClaudeAcceptsOnlyOneCanonicalTopLevelSessionID(t *testing.T) {
	src, ok := adapter.AsConversationIdentitySource(New())
	if !ok {
		t.Fatal("Claude does not implement ConversationIdentitySource")
	}
	cases := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"canonical", json.RawMessage(`{"session_id":"` + claudeIdentityUUID + `"}`), claudeIdentityUUID},
		{"canonical with trailing whitespace", json.RawMessage("{\"session_id\":\"" + claudeIdentityUUID + "\"}\n\t "), claudeIdentityUUID},
		{"canonical alongside ordinary fields", json.RawMessage(`{"hook_event_name":"Stop","session_id":"` + claudeIdentityUUID + `","last_assistant_message":"done"}`), claudeIdentityUUID},
		{"nil", nil, ""},
		{"empty", json.RawMessage{}, ""},
		{"malformed", json.RawMessage(`{"session_id":`), ""},
		{"truncated string", json.RawMessage(`{"session_id":"1389ef09-4c19`), ""},
		{"not UUID", json.RawMessage(`{"session_id":"conversation-7"}`), ""},
		{"null", json.RawMessage(`{"session_id":null}`), ""},
		{"number", json.RawMessage(`{"session_id":7}`), ""},
		{"object", json.RawMessage(`{"session_id":{"value":"` + claudeIdentityUUID + `"}}`), ""},
		{"array value", json.RawMessage(`{"session_id":["` + claudeIdentityUUID + `"]}`), ""},
		{"uppercase UUID", json.RawMessage(`{"session_id":"1389EF09-4C19-4D50-8FDD-1FC95BDCFD4A"}`), ""},
		{"braced UUID", json.RawMessage(`{"session_id":"{1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a}"}`), ""},
		{"leading whitespace in value", json.RawMessage(`{"session_id":" 1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"}`), ""},
		{"alternate key", json.RawMessage(`{"sessionId":"` + claudeIdentityUUID + `"}`), ""},
		{"nested only", json.RawMessage(`{"payload":{"session_id":"` + claudeIdentityUUID + `"}}`), ""},
		{"prose only", json.RawMessage(`{"prompt":"resume ` + claudeIdentityUUID + `"}`), ""},
		{"duplicate same key", json.RawMessage(`{"session_id":"` + claudeIdentityUUID + `","session_id":"` + claudeIdentityUUID + `"}`), ""},
		{"duplicate conflicting key", json.RawMessage(`{"session_id":"` + claudeIdentityUUID + `","session_id":"00000000-0000-4000-8000-000000000000"}`), ""},
		{"trailing JSON", json.RawMessage(`{"session_id":"` + claudeIdentityUUID + `"}{}`), ""},
		{"array envelope", json.RawMessage(`[{"session_id":"` + claudeIdentityUUID + `"}]`), ""},
		{"oversized", json.RawMessage(`{"session_id":"` + claudeIdentityUUID + `","padding":"` + strings.Repeat("x", 1<<20) + `"}`), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, gotOK := src.ConversationIDFromEvent(adapter.HookPayload{Event: "Stop", Raw: tc.raw})
			if tc.want == "" {
				if gotOK || got != "" {
					t.Fatalf("ConversationIDFromEvent = (%q, %v), want (\"\", false)", got, gotOK)
				}
				return
			}
			if !gotOK || got != tc.want {
				t.Fatalf("ConversationIDFromEvent = (%q, %v), want (%q, true)", got, gotOK, tc.want)
			}
		})
	}
}

// TestConversationIdentitySource_AbsenceIsDetectableForOtherProviders proves the extension is
// actually optional. Codex learns identity from its app-server backend, so advertising a hook
// identity source there would route unauthenticated or irrelevant hook prose into persistence.
func TestConversationIdentitySource_AbsenceIsDetectableForOtherProviders(t *testing.T) {
	if src, ok := adapter.AsConversationIdentitySource(codex.New()); ok || src != nil {
		t.Fatalf("AsConversationIdentitySource(codex) = (%v, %v), want (nil, false)", src, ok)
	}
}

// TestConversationIdentitySource_IsTotalDeterministicAndEventAgnostic applies the conformance
// properties directly. The identity belongs to Claude's callback envelope, not to one shaped
// event row, so any authenticated event carrying the canonical field yields the same answer.
func TestConversationIdentitySource_IsTotalDeterministicAndEventAgnostic(t *testing.T) {
	src, ok := adapter.AsConversationIdentitySource(New())
	if !ok {
		t.Fatal("Claude does not implement ConversationIdentitySource")
	}
	raw := json.RawMessage(`{"session_id":"` + claudeIdentityUUID + `"}`)
	for _, event := range []string{"", "Stop", "UserPromptSubmit", "Notification", "future-event"} {
		p := adapter.HookPayload{Event: event, Raw: raw, ReceivedAtMs: 123}
		for i := 0; i < 2; i++ {
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Fatalf("ConversationIDFromEvent panicked for event %q: %v", event, recovered)
					}
				}()
				if got, ok := src.ConversationIDFromEvent(p); !ok || got != claudeIdentityUUID {
					t.Fatalf("event %q call %d = (%q, %v), want (%q, true)", event, i, got, ok, claudeIdentityUUID)
				}
			}()
		}
	}
}

// TestConversationIdentitySource_DoesNotChangeInteractionShaping prevents identity extraction
// from being smuggled into the existing shaper. Reading a callback's identity before shaping
// must leave the same HookPayload and therefore the exact same interaction sequence.
func TestConversationIdentitySource_DoesNotChangeInteractionShaping(t *testing.T) {
	a := New()
	identity, ok := adapter.AsConversationIdentitySource(a)
	if !ok {
		t.Fatal("Claude does not implement ConversationIdentitySource")
	}
	interactions, ok := adapter.AsInteractionSource(a)
	if !ok {
		t.Fatal("Claude does not implement InteractionSource")
	}
	p := adapter.HookPayload{
		Event: "Stop",
		Raw: json.RawMessage(`{"session_id":"` + claudeIdentityUUID +
			`","last_assistant_message":"identity and shaping stay independent"}`),
		ReceivedAtMs: 456,
	}
	want := interactions.Interactions(p)
	if got, ok := identity.ConversationIDFromEvent(p); !ok || got != claudeIdentityUUID {
		t.Fatalf("identity = (%q, %v), want (%q, true)", got, ok, claudeIdentityUUID)
	}
	got := interactions.Interactions(p)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("identity extraction changed interaction shaping:\n before=%+v\n after=%+v", want, got)
	}
}
