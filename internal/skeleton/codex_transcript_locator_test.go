package skeleton

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// These are locator fixtures, not recovery fixtures. Recovery correlates cwd and a
// short creation window to discover an unknown thread ID; the locator already has a
// captured ID and must search Codex's dated tree for the current rollout of that thread.
func codexLocatorMeta(home string) persist.Meta {
	return legacySource("codex", filepath.Join(home, "work"), legacyCreatedAt)
}

func codexLocatorName(when time.Time, threadID, rolloutID, extension string) string {
	ids := threadID
	if rolloutID != "" && rolloutID != threadID {
		ids += "_" + rolloutID
	}
	return "rollout-" + when.UTC().Format("2006-01-02T15-04-05") + "-" + ids + extension
}

func codexLocatorPath(home string, when time.Time, name string) string {
	utc := when.UTC()
	return filepath.Join(home, ".codex", "sessions", utc.Format("2006"), utc.Format("01"), utc.Format("02"), name)
}

func writeCodexLocatorRecord(t *testing.T, home string, when time.Time, threadID, rolloutID, extension string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"timestamp": when.UTC().Format(time.RFC3339Nano),
		"type":      "session_meta",
		"payload": map[string]any{
			// Identity is the stable THREAD id, including in a reverted rollout whose
			// filename carries a distinct rollout id after an underscore.
			"id": threadID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	name := codexLocatorName(when, threadID, rolloutID, extension)
	return writeRawCodexLocatorRecord(t, home, when, name, append(line, '\n'))
}

func writeRawCodexLocatorRecord(t *testing.T, home string, when time.Time, name string, body []byte) string {
	t.Helper()
	p := codexLocatorPath(home, when, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func locateCodexTranscript(t *testing.T, home, threadID string, limits resumeHistoryLimits) (string, resumeHistoryOutcome) {
	t.Helper()
	return newFilesystemResumeHistoryResolver(home, limits).LocateTranscript(codexLocatorMeta(home), threadID)
}

func requireCodexTranscript(t *testing.T, gotPath string, gotOutcome resumeHistoryOutcome, wantPath string) {
	t.Helper()
	if gotOutcome != resumeHistoryFound || gotPath != wantPath {
		t.Fatalf("LocateTranscript = (%q, %v), want (%q, Found)", gotPath, gotOutcome, wantPath)
	}
}

func TestCodexTranscriptLocator_LocatesCurrentAndRevertedRollouts(t *testing.T) {
	t.Run("ordinary current rollout", func(t *testing.T) {
		home := t.TempDir()
		want := writeCodexLocatorRecord(t, home, legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())

		requireCodexTranscript(t, got, outcome, want)
	})

	t.Run("newer reverted rollout across the dated tree", func(t *testing.T) {
		home := t.TempDir()
		writeCodexLocatorRecord(t, home, legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
		// A Codex thread may run for days. The stable thread ID cannot be mapped back to
		// one day, so known-ID lookup must traverse the bounded YYYY/MM/DD tree rather
		// than inherit recovery's creation-window heuristic.
		want := writeCodexLocatorRecord(t, home, legacyCreatedAt.Add(48*time.Hour), legacyCodexRootID, legacyCodexChildID, ".jsonl")

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())

		requireCodexTranscript(t, got, outcome, want)
	})

	t.Run("rollout UUID is the deterministic same-second tie breaker", func(t *testing.T) {
		home := t.TempDir()
		when := legacyCreatedAt.Add(time.Second)
		writeCodexLocatorRecord(t, home, when, legacyCodexRootID, legacyCodexChildID, ".jsonl")
		want := writeCodexLocatorRecord(t, home, when, legacyCodexRootID, legacyCodexOtherID, ".jsonl")

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())

		requireCodexTranscript(t, got, outcome, want)
	})
}

func TestCodexTranscriptLocator_VerifiesTheFirstCompleteSessionMetaRecord(t *testing.T) {
	validName := codexLocatorName(legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
	cases := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"partial without newline", `{"type":"session_meta","payload":{"id":"` + legacyCodexRootID + `"}}`},
		{"not JSON", "not-json\n"},
		{"wrong record type", `{"type":"event_msg","payload":{"id":"` + legacyCodexRootID + `"}}` + "\n"},
		{"payload is not an object", `{"type":"session_meta","payload":[]}` + "\n"},
		{"missing id", `{"type":"session_meta","payload":{}}` + "\n"},
		{"duplicate id", `{"type":"session_meta","payload":{"id":"` + legacyCodexRootID + `","id":"` + legacyCodexRootID + `"}}` + "\n"},
		{"mismatched id", `{"type":"session_meta","payload":{"id":"` + legacyCodexOtherID + `"}}` + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeRawCodexLocatorRecord(t, home, legacyCreatedAt, validName, []byte(tc.body))

			got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())

			if got != "" || outcome == resumeHistoryFound {
				t.Fatalf("unverified transcript located as (%q, %v), want a refusal with no path", got, outcome)
			}
		})
	}

	t.Run("only the identity record is read", func(t *testing.T) {
		home := t.TempDir()
		line := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q}}`, legacyCodexRootID)
		want := writeRawCodexLocatorRecord(t, home, legacyCreatedAt, validName,
			[]byte(line+"\n"+strings.Repeat("x", 2<<20)))
		limits := generousResumeHistoryLimits()
		limits.MaxTotalBytes = int64(len(line) + 1)

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, limits)

		requireCodexTranscript(t, got, outcome, want)
	})

	t.Run("oversized identity record", func(t *testing.T) {
		home := t.TempDir()
		line := fmt.Sprintf(`{"padding":%q,"type":"session_meta","payload":{"id":%q}}`, strings.Repeat("x", 256), legacyCodexRootID)
		writeRawCodexLocatorRecord(t, home, legacyCreatedAt, validName, []byte(line+"\n"))
		limits := generousResumeHistoryLimits()
		limits.MaxRecordBytes = 64

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, limits)

		if got != "" || outcome != resumeHistoryUnsafe {
			t.Fatalf("oversized identity located as (%q, %v), want (empty, Unsafe)", got, outcome)
		}
	})
}

func TestCodexTranscriptLocator_MalformedTargetNamesBlockStaleFallback(t *testing.T) {
	identity := []byte(fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q}}`+"\n", legacyCodexRootID))

	t.Run("newer malformed revert for the requested thread is unsafe", func(t *testing.T) {
		home := t.TempDir()
		writeCodexLocatorRecord(t, home, legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
		newer := legacyCreatedAt.Add(48 * time.Hour)
		name := "rollout-" + newer.UTC().Format("2006-01-02T15-04-05") + "-" +
			legacyCodexRootID + "_not-a-uuid.jsonl"
		writeRawCodexLocatorRecord(t, home, newer, name, identity)

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())

		if got != "" || outcome != resumeHistoryUnsafe {
			t.Fatalf("target-prefixed malformed result = (%q, %v), want (empty, Unsafe)", got, outcome)
		}
	})

	t.Run("unrelated malformed rollout does not poison a full-tree lookup", func(t *testing.T) {
		home := t.TempDir()
		want := writeCodexLocatorRecord(t, home, legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
		newer := legacyCreatedAt.Add(48 * time.Hour)
		name := "rollout-" + newer.UTC().Format("2006-01-02T15-04-05") + "-" +
			legacyCodexOtherID + "_not-a-uuid.jsonl"
		writeRawCodexLocatorRecord(t, home, newer, name, identity)

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())

		requireCodexTranscript(t, got, outcome, want)
	})
}

func TestCodexTranscriptLocator_RefusesUnsafeFilesystemObjects(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		home := t.TempDir()
		name := codexLocatorName(legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
		p := codexLocatorPath(home, legacyCreatedAt, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.jsonl")
		if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, p); err != nil {
			t.Fatal(err)
		}

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())
		if got != "" || outcome != resumeHistoryUnsafe {
			t.Fatalf("symlink located as (%q, %v), want (empty, Unsafe)", got, outcome)
		}
	})

	t.Run("directory in the file slot", func(t *testing.T) {
		home := t.TempDir()
		name := codexLocatorName(legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
		if err := os.MkdirAll(codexLocatorPath(home, legacyCreatedAt, name), 0o700); err != nil {
			t.Fatal(err)
		}

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())
		if got != "" || outcome != resumeHistoryUnsafe {
			t.Fatalf("directory located as (%q, %v), want (empty, Unsafe)", got, outcome)
		}
	})

	t.Run("FIFO", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("mkfifo is a Unix fixture; supported release targets are Darwin and Linux")
		}
		home := t.TempDir()
		name := codexLocatorName(legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
		p := codexLocatorPath(home, legacyCreatedAt, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Fatal(err)
		}

		result := make(chan struct {
			path    string
			outcome resumeHistoryOutcome
		}, 1)
		go func() {
			path, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())
			result <- struct {
				path    string
				outcome resumeHistoryOutcome
			}{path, outcome}
		}()
		select {
		case got := <-result:
			if got.path != "" || got.outcome != resumeHistoryUnsafe {
				t.Fatalf("FIFO located as (%q, %v), want (empty, Unsafe)", got.path, got.outcome)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("locator blocked on a FIFO")
		}
	})
}

func TestCodexTranscriptLocator_RejectsInvalidIDsBeforeOpeningProviderState(t *testing.T) {
	for _, threadID := range []string{"", "..", "../../../../etc/passwd", "./cmd/swarm/", strings.ToUpper(legacyCodexRootID)} {
		t.Run(threadID, func(t *testing.T) {
			home := t.TempDir()
			var mu sync.Mutex
			var opened []string
			resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())
			resolver.beforeOpen = func(path string) {
				mu.Lock()
				opened = append(opened, path)
				mu.Unlock()
			}

			got, outcome := resolver.LocateTranscript(codexLocatorMeta(home), threadID)

			if got != "" || outcome != resumeHistoryUnsafe {
				t.Fatalf("LocateTranscript(%q) = (%q, %v), want (empty, Unsafe)", threadID, got, outcome)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(opened) != 0 {
				t.Fatalf("invalid ID steered filesystem opens: %v", opened)
			}
		})
	}
}

func TestCodexTranscriptLocator_ResourceBudgetsFailClosed(t *testing.T) {
	t.Run("directory entries", func(t *testing.T) {
		home := t.TempDir()
		want := writeCodexLocatorRecord(t, home, legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
		if err := os.WriteFile(filepath.Join(filepath.Dir(want), "notes.txt"), []byte("ignored\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		limits := generousResumeHistoryLimits()
		limits.MaxEntries = 1

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, limits)

		if got != "" || outcome != resumeHistoryUnsafe {
			t.Fatalf("over-budget tree located as (%q, %v), want (empty, Unsafe)", got, outcome)
		}
	})

	t.Run("cumulative metadata bytes", func(t *testing.T) {
		home := t.TempDir()
		p := writeCodexLocatorRecord(t, home, legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		limits := generousResumeHistoryLimits()
		limits.MaxTotalBytes = int64(len(body) - 1)

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, limits)

		if got != "" || outcome != resumeHistoryUnsafe {
			t.Fatalf("over-budget record located as (%q, %v), want (empty, Unsafe)", got, outcome)
		}
	})
}

func TestCodexTranscriptLocator_CompressedAndDuplicateCandidatesAreNamed(t *testing.T) {
	t.Run("newest candidate is compressed and an older plain rollout is not used", func(t *testing.T) {
		home := t.TempDir()
		writeCodexLocatorRecord(t, home, legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
		writeCodexLocatorRecord(t, home, legacyCreatedAt.Add(48*time.Hour), legacyCodexRootID, legacyCodexChildID, ".jsonl.zst")

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())

		if got != "" || outcome != resumeHistoryCompressed {
			t.Fatalf("compressed transcript result = (%q, %v), want (empty, Compressed)", got, outcome)
		}
	})

	t.Run("plain copy hides its identical compressed sibling", func(t *testing.T) {
		home := t.TempDir()
		want := writeCodexLocatorRecord(t, home, legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
		writeCodexLocatorRecord(t, home, legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl.zst")

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())

		requireCodexTranscript(t, got, outcome, want)
	})

	t.Run("newest compressed rollout blocks stale plain fallback", func(t *testing.T) {
		home := t.TempDir()
		writeCodexLocatorRecord(t, home, legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
		writeCodexLocatorRecord(t, home, legacyCreatedAt.Add(24*time.Hour), legacyCodexRootID, legacyCodexChildID, ".jsonl.zst")

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())

		if got != "" || outcome != resumeHistoryCompressed {
			t.Fatalf("compressed-current result = (%q, %v), want (empty, Compressed)", got, outcome)
		}
	})

	t.Run("compressed sibling cannot clear an equal-key ambiguity", func(t *testing.T) {
		home := t.TempDir()
		writeCodexLocatorRecord(t, home, legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
		explicit := "rollout-" + legacyCreatedAt.UTC().Format("2006-01-02T15-04-05") + "-" +
			legacyCodexRootID + "_" + legacyCodexRootID + ".jsonl"
		body := []byte(fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q}}`+"\n", legacyCodexRootID))
		writeRawCodexLocatorRecord(t, home, legacyCreatedAt, explicit, body)
		writeRawCodexLocatorRecord(t, home, legacyCreatedAt, explicit+".zst", body)

		got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())

		if got != "" || outcome != resumeHistoryAmbiguous {
			t.Fatalf("equal-key transcript result = (%q, %v), want (empty, Ambiguous)", got, outcome)
		}
	})
}

func TestCodexTranscriptLocator_ArchivedHistoriesRemainOutOfScope(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(home, ".codex", "archived_sessions")
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	name := codexLocatorName(legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")
	body := []byte(fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q}}`+"\n", legacyCodexRootID))
	if err := os.WriteFile(filepath.Join(archive, name), body, 0o600); err != nil {
		t.Fatal(err)
	}

	got, outcome := locateCodexTranscript(t, home, legacyCodexRootID, generousResumeHistoryLimits())
	if got != "" || outcome != resumeHistoryNoMatch {
		t.Fatalf("archive-only locator result = {%q %v}, want no path and NoMatch", got, outcome)
	}
}

func TestCodexHandsOff_ComposesPointersAndPreservesHandoffInvariants(t *testing.T) {
	home := t.TempDir()
	const local = "codex-source"
	cwd := filepath.Join(home, "work")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	source := handsOffSource(local, cwd, legacyCodexRootID)
	source.AgentType = "codex"
	transcript := writeCodexLocatorRecord(t, home, legacyCreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl")

	got, err := composeHandsOffLaunch(
		handsOffSpec(testEndpoint, local, filepath.Join(home, "client-guess")),
		testEndpoint,
		srcGetter(local, source),
		newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()),
	)
	if err != nil {
		t.Fatalf("Codex hands-off source was refused: %v", err)
	}
	for _, pointer := range []string{legacyCodexRootID, transcript, cwd, "codex", local} {
		if !strings.Contains(got.InitialPrompt, pointer) {
			t.Errorf("composed prompt is missing pointer %q:\n%s", pointer, got.InitialPrompt)
		}
	}
	if got.Cwd != cwd {
		t.Errorf("successor cwd = %q, want source provider cwd %q", got.Cwd, cwd)
	}
	if got.SpawnedFrom != local || got.SpawnIntent != protocol.SpawnIntentHandoff || got.Supervision != "" {
		t.Errorf("lineage = {SpawnedFrom:%q SpawnIntent:%q Supervision:%q}, want local hands-off lineage",
			got.SpawnedFrom, got.SpawnIntent, got.Supervision)
	}
	if got.Options[protocol.OptionResumeFrom] != "" || got.Options[protocol.OptionResumeConversationID] != "" {
		t.Errorf("hands-off composition gained a resume option: %+v", got.Options)
	}
}

func TestCodexHandsOff_RecoversAStableThreadIDFromARevertedRollout(t *testing.T) {
	home := t.TempDir()
	const local = "codex-source"
	cwd := filepath.Join(home, "work")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
	revertWhen := legacyCreatedAt.Add(2 * time.Second)
	revertSource := writeCodexHistory(t, home, legacyCodexRootID, cwd, revertWhen, "", "cli", "")
	reverted := filepath.Join(filepath.Dir(revertSource),
		codexLocatorName(revertWhen, legacyCodexRootID, legacyCodexChildID, ".jsonl"))
	if err := os.Rename(revertSource, reverted); err != nil {
		t.Fatal(err)
	}
	source := handsOffSource(local, cwd, "")
	source.AgentType = "codex"

	got, err := composeHandsOffLaunch(
		handsOffSpec(testEndpoint, local, filepath.Join(home, "client-guess")),
		testEndpoint,
		srcGetter(local, source),
		newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()),
	)
	if err != nil {
		t.Fatalf("Codex revert with a recoverable thread identity was refused: %v", err)
	}
	for _, pointer := range []string{legacyCodexRootID, reverted} {
		if !strings.Contains(got.InitialPrompt, pointer) {
			t.Errorf("composed prompt is missing recovered pointer %q:\n%s", pointer, got.InitialPrompt)
		}
	}
}

func TestCodexRecovery_FilenameWallClockMayDifferFromUTCPayloadTimestamp(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "work")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}

	// Codex 0.150.1 on the owner's Europe/Zurich host wrote the observed rollout as
	// `2026-08-29T10-14-59` while session_meta.payload.timestamp was
	// `2026-08-29T08:14:59.465Z`. The filename is local wall clock; the metadata is an
	// absolute UTC instant. Recovery must correlate from the metadata timestamp rather
	// than require those two unlike clock representations to be byte-equal.
	payloadWhen := time.Date(2026, 8, 29, 8, 14, 59, 465_000_000, time.UTC)
	filenameWallClock := time.Date(2026, 8, 29, 10, 14, 59, 0, time.UTC)
	line, err := json.Marshal(map[string]any{
		"timestamp": payloadWhen.Add(3 * time.Second).Format(time.RFC3339Nano),
		"type":      "session_meta",
		"payload": map[string]any{
			"id":        legacyCodexRootID,
			"timestamp": payloadWhen.Format(time.RFC3339Nano),
			"cwd":       cwd,
			"source":    "vscode",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	name := codexLocatorName(filenameWallClock, legacyCodexRootID, legacyCodexRootID, ".jsonl")
	writeRawCodexLocatorRecord(t, home, payloadWhen, name, append(line, '\n'))

	got := resolveHistory(t, home, generousResumeHistoryLimits(),
		legacySource("codex", cwd, payloadWhen.Add(-time.Second)))

	requireHistoryResult(t, got, resumeHistoryFound, legacyCodexRootID)
}

func TestCodexRecovery_TwoOrdinaryCopiesStayAmbiguousAcrossARevert(t *testing.T) {
	o1 := codexHistoryCandidate{
		id: legacyCodexRootID, when: legacyCreatedAt.Add(time.Second),
		rolloutID: legacyCodexRootID,
	}
	r := codexHistoryCandidate{
		id: legacyCodexRootID, when: legacyCreatedAt.Add(2 * time.Second),
		rolloutID: legacyCodexChildID, reverted: true,
	}
	o2 := codexHistoryCandidate{
		id: legacyCodexRootID, when: legacyCreatedAt.Add(3 * time.Second),
		rolloutID: legacyCodexRootID,
	}
	for i, candidates := range [][]codexHistoryCandidate{
		{o1, r, o2}, {o1, o2, r}, {r, o1, o2},
		{r, o2, o1}, {o2, o1, r}, {o2, r, o1},
	} {
		got := resolveCodexCandidateGraph(candidates)
		if got.Outcome != resumeHistoryAmbiguous || got.ConversationID != "" {
			t.Errorf("permutation %d result = {Outcome:%v ConversationID:%q}, want Ambiguous with no id",
				i, got.Outcome, got.ConversationID)
		}
	}
}

func TestCodexHandsOff_DoesNotRecoverIdentityFromACompressedBasename(t *testing.T) {
	home := t.TempDir()
	resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())
	rig := newResumeAPIRigWithProcess(t, "codex", "", status.ProcessRunning, resolver)
	source := rig.meta(t)
	// The basename is a search hint, not identity proof. With session_meta inside a
	// zstd stream this slice deliberately has no evidence tying the file to the source.
	writeCodexLocatorRecord(t, home, source.CreatedAt, legacyCodexRootID, legacyCodexRootID, ".jsonl.zst")
	before := len(rig.core.List())

	_, err := rig.api.Launch(handsOffSpec(testEndpoint, rig.sourceID, filepath.Join(rig.stateDir, "new-work")))

	requireRefusedAndLaunchedNothing(t, rig, before, err, "no usable codex conversation id")
}
