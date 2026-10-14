package skeleton

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCodexModelTranscript writes a rollout under profile and returns its absolute path.
func writeCodexModelTranscript(t *testing.T, profile string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(profile, "sessions", "2026", "10", "10")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create rollout dir: %v", err)
	}
	p := filepath.Join(dir, "rollout-2026-10-10T08-00-00-01a0f23b-0000-7000-8000-000000000001.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	return p
}

// oversizedCodexLine is a record longer than the 8 MiB line cap, shaped like the live
// function_call_output records of 11-19.9 MB that made two rotation resumes fail.
func oversizedCodexLine(recordType string) string {
	return `{"timestamp":"2026-10-10T08:00:01Z","type":"` + recordType + `","payload":{"type":"function_call_output","output":"` +
		strings.Repeat("x", 9<<20) + `"}}`
}

// TestReadCodexTranscriptModel_SkipsAToolOutputLineLongerThanTheLineCap pins the live failure:
// one tool-output line past 8 MiB made the whole rollout "unreadable" and refused every managed
// resume of that conversation with native-resume-model-unavailable. The model lives in small
// turn_context records, so an oversized non-turn_context line must be skipped, not fatal.
func TestReadCodexTranscriptModel_SkipsAToolOutputLineLongerThanTheLineCap(t *testing.T) {
	profile := t.TempDir()
	path := writeCodexModelTranscript(t, profile,
		`{"timestamp":"2026-10-10T08:00:00Z","type":"session_meta","payload":{"id":"01a0f23b-0000-7000-8000-000000000001"}}`,
		`{"timestamp":"2026-10-10T08:00:00Z","type":"turn_context","payload":{"model":"gpt-6-astra"}}`,
		oversizedCodexLine("response_item"),
		`{"timestamp":"2026-10-10T08:00:02Z","type":"turn_context","payload":{"model":"gpt-6.1-sol"}}`,
	)
	model, seen, err := readCodexTranscriptModel(profile, path)
	if err != nil || !seen || model != "gpt-6.1-sol" {
		t.Fatalf("readCodexTranscriptModel = (%q, %v, %v), want (\"gpt-6.1-sol\", true, nil)", model, seen, err)
	}
}

// TestReadCodexTranscriptModel_AnOversizedTurnContextStillFailsClosed keeps the proof exact: a
// turn_context that cannot be read could name the newest model, so skipping it would prove a
// stale one.
func TestReadCodexTranscriptModel_AnOversizedTurnContextStillFailsClosed(t *testing.T) {
	profile := t.TempDir()
	path := writeCodexModelTranscript(t, profile,
		`{"timestamp":"2026-10-10T08:00:00Z","type":"turn_context","payload":{"model":"gpt-6-astra"}}`,
		oversizedCodexLine("turn_context"),
	)
	if model, _, err := readCodexTranscriptModel(profile, path); err == nil {
		t.Fatalf("readCodexTranscriptModel = %q with no error; an unreadable turn_context must refuse", model)
	}
}
