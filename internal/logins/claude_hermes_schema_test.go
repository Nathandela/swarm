package logins

import (
	"encoding/json"
	"testing"
)

func TestHermesSchemaFiveIsUnrelatedToClaudeCustody(t *testing.T) {
	root := t.TempDir()
	base := map[string]any{"schema_version": 5, "id": "hermes", "agent_type": "hermes", "hermes_native": true, "cli_identity": map[string]string{"version": "0.22.5"}}
	check := func(fields map[string]any, valid bool) {
		t.Helper()
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		_, parsed := parseClaudeSessionMeta(raw, root, "hermes")
		unmanaged := claudeUnmanagedRow(raw, root, "hermes")
		if valid && (parsed != nil || unmanaged != nil) || !valid && (parsed == nil || unmanaged == nil) {
			t.Fatalf("runtime classification parsed=%v unmanaged=%v valid=%v", parsed, unmanaged, valid)
		}
	}
	check(base, true)
	base["shim_pid"], base["shim_start_time"] = 123, 0
	check(base, true)
	for _, change := range []map[string]any{
		{"agent_type": "claude"}, {"hermes_native": false}, {"schema_version": 6},
		{"required_runtime": "claude-chat"}, {"login_runtime": map[string]any{}},
		{"claude_retirement": map[string]any{}}, {"cli_identity": nil}, {"Hermes_Native": true},
	} {
		fields := make(map[string]any, len(base)+len(change))
		for key, value := range base {
			fields[key] = value
		}
		for key, value := range change {
			fields[key] = value
		}
		check(fields, false)
	}
}
