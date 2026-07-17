package main

// FIX 3(a): `swarm hook` must parse the JSON payload Claude Code posts on stdin and
// extract its fields for the callback payload (the engine then normalizes
// event+payload -> status via SignalSources).

import (
	"strings"
	"testing"
)

func TestParseHookStdin_ExtractsClaudePayloadFields(t *testing.T) {
	// A real-format Claude Notification hook payload on stdin.
	in := `{"session_id":"3f2a1c9e","transcript_path":"/home/u/x.jsonl",` +
		`"hook_event_name":"Notification","cwd":"/home/u/proj",` +
		`"message":"Claude needs your permission to use Bash","notification_type":"permission",` +
		`"stop_hook_active":false}`

	got := parseHookStdin(strings.NewReader(in))

	for k, want := range map[string]string{
		"hook_event_name":   "Notification",
		"notification_type": "permission",
		"cwd":               "/home/u/proj",
	} {
		if got[k] != want {
			t.Errorf("parseHookStdin[%q] = %q; want %q", k, got[k], want)
		}
	}
	// A non-string field is skipped (not coerced), never panics.
	if _, ok := got["stop_hook_active"]; ok {
		t.Errorf("parseHookStdin kept a non-string field stop_hook_active=%q; want it skipped", got["stop_hook_active"])
	}
}

func TestParseHookStdin_Totality(t *testing.T) {
	for _, in := range []string{"", "   ", "not json", "[1,2,3]", `{"a":`, `12345`} {
		if got := parseHookStdin(strings.NewReader(in)); got == nil {
			t.Errorf("parseHookStdin(%q) returned nil; want a non-nil (possibly empty) map", in)
		}
	}
	if got := parseHookStdin(nil); got == nil {
		t.Errorf("parseHookStdin(nil) returned nil; want a non-nil empty map")
	}
}

// TestParseHookStdin_SkipsReservedDimensionKeys proves a stdin payload cannot inject
// a status dimension directly — deriving turn/interaction from the event is the
// engine's job.
func TestParseHookStdin_SkipsReservedDimensionKeys(t *testing.T) {
	got := parseHookStdin(strings.NewReader(`{"turn":"active","interaction":"permission","message":"hi"}`))
	if _, ok := got["turn"]; ok {
		t.Errorf("parseHookStdin honored a client-supplied turn; reserved dims must be skipped")
	}
	if _, ok := got["interaction"]; ok {
		t.Errorf("parseHookStdin honored a client-supplied interaction; reserved dims must be skipped")
	}
	if got["message"] != "hi" {
		t.Errorf("parseHookStdin dropped a non-reserved field; message = %q", got["message"])
	}
}
