package skeleton

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestResumeHistory_CodexSkipsTornRollouts pins ADR-010 Amendment 7 H2: a rollout codex
// tore while writing it is not a record and cannot name an id, so the scan passes over it
// and judges the session on its other rollouts. The three shapes are the measured ones: a
// zero-byte file, a first line still being written, and a first record cut by a raw newline
// with a second header starting on line two. A matching rollout sits beside each.
func TestResumeHistory_CodexSkipsTornRollouts(t *testing.T) {
	cwd := "/work/project"
	when := legacyCreatedAt.Add(time.Second)
	for _, tc := range []struct {
		name string
		body string
	}{
		{"zero-byte rollout", ""},
		{"first line still being written", `{"type":"session_meta","payload":{"id":"` + legacyCodexOtherID + `"`},
		{"first record torn by a raw newline", `{"type":"session_meta","payload":{"id":"` + legacyCodexOtherID + `","base_instructions":"cut here` + "\n" +
			`{"timestamp":"2026-09-01T14:52:06.628Z","type":"session_meta","payload":{}}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeCodexHistory(t, home, legacyCodexRootID, cwd, when, "", "cli", "")
			torn := codexHistoryPath(home, when.Add(2*time.Second), legacyCodexOtherID)
			if err := os.WriteFile(torn, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
			requireHistoryResult(t, got, resumeHistoryFound, legacyCodexRootID)
		})
	}
}

// TestLocateTranscript_CodexNamedFileWithoutARecordIsNotFound is H2's face on the hands-off
// locator: the file the id names exists but holds no complete record, which is "the
// transcript is not there", not "unsafe to inspect".
func TestLocateTranscript_CodexNamedFileWithoutARecordIsNotFound(t *testing.T) {
	home := t.TempDir()
	created := time.Date(2026, 9, 1, 9, 2, 54, 0, time.UTC)
	id := uuidv7At(created)
	p := codexHistoryPath(home, created, id)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	src := codexSource("legacy-source", "/work/project", id, created)
	got, outcome := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()).LocateTranscript(src, id)
	if outcome != resumeHistoryNoMatch || got != "" {
		t.Fatalf("LocateTranscript = (%q, %v), want (\"\", %v)", got, outcome, resumeHistoryNoMatch)
	}
}
