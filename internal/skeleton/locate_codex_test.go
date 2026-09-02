package skeleton

// FAILING-FIRST for ADR-010 Amendment 5 F1: a hands-off handoff from a CODEX source.
// Codex files one rollout per thread under ~/.codex/sessions/<UTC day>/rollout-<stamp>-
// <id>.jsonl and its thread ids are UUIDv7, so the day is read out of the id (the
// adapter's knowledge) and that day is listed for the one file naming the id (the
// resolver's anchored, budgeted walk). A resumed thread is the ORDINARY codex case, so
// the swarm session's own creation date plays no part in finding the file. Every
// refusal launches nothing (E7).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// uuidv7At composes a canonical UUIDv7 whose 48-bit prefix is when's Unix millisecond,
// the way codex mints thread ids.
func uuidv7At(when time.Time) string {
	ms := fmt.Sprintf("%012x", when.UnixMilli())
	return ms[:8] + "-" + ms[8:12] + "-7000-8000-000000000000"
}

func codexSource(local, cwd, convID string, created time.Time) persist.Meta {
	return persist.Meta{
		ID:             local,
		AgentType:      "codex",
		ConversationID: convID,
		Cwd:            cwd,
		CreatedAt:      created,
		LastActivity:   created,
		Status:         status.Status{Process: status.ProcessRunning},
	}
}

func codexSessionMetaLine(t *testing.T, id, cwd string, when time.Time) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"timestamp": when.UTC().Format(time.RFC3339Nano),
		"type":      "session_meta",
		"payload":   map[string]any{"id": id, "timestamp": when.UTC().Format(time.RFC3339Nano), "cwd": cwd, "source": "cli"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

var codexThreadStart = time.Date(2026, 9, 2, 0, 32, 54, 0, time.UTC)

func TestLocateTranscript_CodexFindsTheRolloutUnderTheIDsDay(t *testing.T) {
	home := t.TempDir()
	id := uuidv7At(codexThreadStart)
	cwd := filepath.Join(home, "work")
	// Created three days after the thread: a swarm session that RESUMED an older thread.
	src := codexSource("srclocal", cwd, id, codexThreadStart.Add(72*time.Hour))
	want := writeCodexHistory(t, home, id, cwd, codexThreadStart, "", "cli", "")

	got, outcome := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()).LocateTranscript(src, id)
	if outcome != resumeHistoryFound || got != want {
		t.Fatalf("LocateTranscript = (%q, %v), want (%q, found)", got, outcome, want)
	}
}

// TestLocateTranscript_CodexTriesTheNeighbouringDays: the id carries the millisecond the
// thread was minted, the file carries the second codex wrote it; across midnight those
// fall in different day directories.
func TestLocateTranscript_CodexTriesTheNeighbouringDays(t *testing.T) {
	home := t.TempDir()
	minted := time.Date(2026, 9, 1, 23, 59, 59, 0, time.UTC)
	id := uuidv7At(minted)
	cwd := filepath.Join(home, "work")
	src := codexSource("srclocal", cwd, id, minted)
	want := writeCodexHistory(t, home, id, cwd, minted.Add(2*time.Second), "", "cli", "") // sessions/2026/09/02

	got, outcome := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()).LocateTranscript(src, id)
	if outcome != resumeHistoryFound || got != want {
		t.Fatalf("LocateTranscript = (%q, %v), want (%q, found)", got, outcome, want)
	}
}

func TestLocateTranscript_CodexRefusesByName(t *testing.T) {
	id := uuidv7At(codexThreadStart)
	for _, tc := range []struct {
		name    string
		arrange func(t *testing.T, home, cwd string)
		convID  string
		want    resumeHistoryOutcome
	}{
		{"no rollout on disk", func(*testing.T, string, string) {}, id, resumeHistoryNoMatch},
		{"the file names another thread", func(t *testing.T, home, cwd string) {
			other := uuidv7At(codexThreadStart.Add(time.Minute))
			writeRawCodexHistory(t, home, id, codexThreadStart, codexSessionMetaLine(t, other, cwd, codexThreadStart))
		}, id, resumeHistoryNoMatch},
		{"the thread ran in another checkout", func(t *testing.T, home, _ string) {
			writeCodexHistory(t, home, id, filepath.Join(home, "elsewhere"), codexThreadStart, "", "cli", "")
		}, id, resumeHistoryNoMatch},
		{"a first record that is not a session_meta", func(t *testing.T, home, _ string) {
			writeRawCodexHistory(t, home, id, codexThreadStart, `{"type":"response_item","payload":{}}`)
		}, id, resumeHistoryNoMatch},
		{"a first record that is not JSON", func(t *testing.T, home, _ string) {
			writeRawCodexHistory(t, home, id, codexThreadStart, "not json")
		}, id, resumeHistoryNoMatch},
		{"a canonical id that carries no day", func(*testing.T, string, string) {}, "f41b0e35-6fa4-4c8b-bfea-8687b311255b", resumeHistoryNoMatch},
		{"a non-canonical id", func(*testing.T, string, string) {}, "../../etc/passwd", resumeHistoryUnsafe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cwd := filepath.Join(home, "work")
			tc.arrange(t, home, cwd)
			src := codexSource("srclocal", cwd, tc.convID, codexThreadStart)

			got, outcome := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()).LocateTranscript(src, tc.convID)
			if outcome != tc.want || got != "" {
				t.Fatalf("LocateTranscript = (%q, %v), want (\"\", %v)", got, outcome, tc.want)
			}
		})
	}
}

// TestLocateTranscript_UncharacterizedProvidersStayUnsupported: absence is the signal.
// agy and opencode implement neither layout and are refused by name, even with a
// perfectly good codex tree on disk.
func TestLocateTranscript_UncharacterizedProvidersStayUnsupported(t *testing.T) {
	home := t.TempDir()
	id := uuidv7At(codexThreadStart)
	cwd := filepath.Join(home, "work")
	writeCodexHistory(t, home, id, cwd, codexThreadStart, "", "cli", "")
	for _, agent := range []string{"agy", "opencode"} {
		src := codexSource("srclocal", cwd, id, codexThreadStart)
		src.AgentType = agent
		got, outcome := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()).LocateTranscript(src, id)
		if outcome != resumeHistoryUnsupported || got != "" {
			t.Errorf("LocateTranscript(%s) = (%q, %v), want (\"\", unsupported)", agent, got, outcome)
		}
	}
}

// TestHandsOff_ComposesForACodexSourceFromItsDatedRollout is the handoff this slice was
// built for: a codex source, a claude target (handsOffSpec's AgentType), and the same
// five pointers, local lineage and empty supervision Amendment 4 composes for claude.
func TestHandsOff_ComposesForACodexSourceFromItsDatedRollout(t *testing.T) {
	home := t.TempDir()
	const local = "srclocal"
	id := uuidv7At(codexThreadStart)
	sourceCwd := filepath.Join(home, "work")
	if err := os.MkdirAll(sourceCwd, 0o700); err != nil {
		t.Fatal(err)
	}
	src := codexSource(local, sourceCwd, id, codexThreadStart.Add(48*time.Hour))
	transcript := writeCodexHistory(t, home, id, sourceCwd, codexThreadStart, "", "cli", "")
	resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())

	got, err := composeHandsOffLaunch(handsOffSpec(testEndpoint, local, "/new-work"), testEndpoint, srcGetter(local, src), resolver)
	if err != nil {
		t.Fatalf("hands-off composition of a RUNNING codex source was refused: %v", err)
	}
	for _, want := range []string{id, transcript, sourceCwd, "codex", local} {
		if !strings.Contains(got.InitialPrompt, want) {
			t.Errorf("composed prompt is missing the pointer %q:\n%s", want, got.InitialPrompt)
		}
	}
	if got.SpawnedFrom != local || got.SpawnIntent != protocol.SpawnIntentHandoff || got.Supervision != "" {
		t.Errorf("lineage = (%q, %q, supervision %q), want (%q, handoff, empty)", got.SpawnedFrom, got.SpawnIntent, got.Supervision, local)
	}
	if got.Cwd != sourceCwd {
		t.Errorf("Cwd = %q, want the source's working directory %q", got.Cwd, sourceCwd)
	}
	resolved, err := composeLaunchSpec(got, testEndpoint, "", srcGetter(local, src), stubLookPath)
	if err != nil {
		t.Fatalf("composed hands-off spec did not compose argv: %v", err)
	}
	if n := strings.Count(strings.Join(resolved.Argv, "\x00"), got.InitialPrompt); n != 1 {
		t.Errorf("argv carries the composed prompt %d times, want exactly once", n)
	}
}

// TestHandsOff_RecoversACodexConversationIDFromProviderHistory: on the owner's machine
// six live codex sessions hold no captured id (their app-server holds several threads
// and the daemon refuses to guess), so the id is re-derived from the day the swarm
// session was created, then located under the day the id itself names.
func TestHandsOff_RecoversACodexConversationIDFromProviderHistory(t *testing.T) {
	home := t.TempDir()
	const local = "srclocal"
	sourceCwd := filepath.Join(home, "work")
	if err := os.MkdirAll(sourceCwd, 0o700); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	id := uuidv7At(created.Add(time.Second))
	src := codexSource(local, sourceCwd, "", created)
	transcript := writeCodexHistory(t, home, id, sourceCwd, created.Add(time.Second), "", "cli", "")
	resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())

	got, err := composeHandsOffLaunch(handsOffSpec(testEndpoint, local, "/new-work"), testEndpoint, srcGetter(local, src), resolver)
	if err != nil {
		t.Fatalf("recoverable codex source refused: %v", err)
	}
	if !strings.Contains(got.InitialPrompt, id) || !strings.Contains(got.InitialPrompt, transcript) {
		t.Errorf("composed prompt does not carry the recovered identity:\n%s", got.InitialPrompt)
	}
}
