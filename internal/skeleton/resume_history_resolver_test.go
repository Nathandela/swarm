package skeleton

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	legacyCodexRootID   = "01a00038-33ec-7643-a5fd-169228389460"
	legacyCodexChildID  = "01a00038-515c-7f52-9f70-4276d61f0d2b"
	legacyCodexOtherID  = "01a00039-1111-7222-8333-444444444444"
	legacyCodexFourthID = "01a00040-aaaa-7bbb-8ccc-dddddddddddd"
	legacyCodexGrandID  = "01a00041-bbbb-7ccc-8ddd-eeeeeeeeeeee"
	legacyClaudeID      = "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
	legacyClaudeOtherID = "2389ef09-4c19-4d50-8fdd-1fc95bdcfd4b"
)

var legacyCreatedAt = time.Date(2026, 8, 14, 12, 21, 18, 0, time.UTC)

func generousResumeHistoryLimits() resumeHistoryLimits {
	return resumeHistoryLimits{
		MaxEntries:     256,
		MaxOpenFiles:   128,
		MaxRecordBytes: 64 << 10,
		MaxTotalBytes:  2 << 20,
	}
}

func legacySource(agent, cwd string, createdAt time.Time) persist.Meta {
	return persist.Meta{
		ID:        "legacy-source",
		AgentType: agent,
		Cwd:       cwd,
		CreatedAt: createdAt,
		Status:    status.Status{Process: status.ProcessExited},
	}
}

func resolveHistory(t *testing.T, home string, limits resumeHistoryLimits, m persist.Meta) resumeHistoryResult {
	t.Helper()
	return newFilesystemResumeHistoryResolver(home, limits).Resolve(m)
}

func resolveHistoryWithBeforeOpen(t *testing.T, home string, limits resumeHistoryLimits, m persist.Meta, beforeOpen func(string)) resumeHistoryResult {
	t.Helper()
	resolver := newFilesystemResumeHistoryResolver(home, limits)
	resolver.beforeOpen = beforeOpen // private, test-only filesystem race seam; nil in production
	return resolver.Resolve(m)
}

func requireHistoryResult(t *testing.T, got resumeHistoryResult, outcome resumeHistoryOutcome, id string) {
	t.Helper()
	if got.Outcome != outcome || got.ConversationID != id {
		t.Fatalf("history result = {Outcome:%v ConversationID:%q}, want {%v %q}",
			got.Outcome, got.ConversationID, outcome, id)
	}
}

func codexHistoryPath(home string, when time.Time, id string) string {
	utc := when.UTC()
	name := "rollout-" + utc.Format("2006-01-02T15-04-05") + "-" + id + ".jsonl"
	return filepath.Join(home, ".codex", "sessions", utc.Format("2006"), utc.Format("01"), utc.Format("02"), name)
}

func writeCodexHistory(t *testing.T, home, id, cwd string, when time.Time, parent, source, tail string) string {
	t.Helper()
	p := codexHistoryPath(home, when, id)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("create Codex history dir: %v", err)
	}
	var parentValue any
	if parent != "" {
		parentValue = parent
	}
	line, err := json.Marshal(map[string]any{
		"timestamp": when.UTC().Format(time.RFC3339Nano),
		"type":      "session_meta",
		"payload": map[string]any{
			"id":               id,
			"timestamp":        when.UTC().Format(time.RFC3339Nano),
			"cwd":              cwd,
			"source":           source,
			"parent_thread_id": parentValue,
		},
	})
	if err != nil {
		t.Fatalf("marshal Codex session_meta: %v", err)
	}
	body := append(append(append([]byte(nil), line...), '\n'), []byte(tail)...)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatalf("write Codex history: %v", err)
	}
	return p
}

func writeRawCodexHistory(t *testing.T, home, id string, when time.Time, firstLine string) string {
	t.Helper()
	p := codexHistoryPath(home, when, id)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("create Codex history dir: %v", err)
	}
	if err := os.WriteFile(p, []byte(firstLine+"\n"), 0o600); err != nil {
		t.Fatalf("write raw Codex history: %v", err)
	}
	return p
}

func claudeProjectDir(home, cwd string) string {
	var encoded strings.Builder
	for _, r := range cwd {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			encoded.WriteRune(r)
		} else {
			encoded.WriteByte('-')
		}
	}
	return filepath.Join(home, ".claude", "projects", encoded.String())
}

func writeClaudeHistory(t *testing.T, home, id, cwd string, when time.Time, prefixLines, tail string) string {
	t.Helper()
	p := filepath.Join(claudeProjectDir(home, cwd), id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("create Claude history dir: %v", err)
	}
	line, err := json.Marshal(map[string]any{
		"sessionId": id,
		"cwd":       cwd,
		"timestamp": when.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("marshal Claude identity record: %v", err)
	}
	body := prefixLines + string(line) + "\n" + tail
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write Claude history: %v", err)
	}
	return p
}

func writeRawClaudeHistory(t *testing.T, home, id, cwd, body string) string {
	t.Helper()
	p := filepath.Join(claudeProjectDir(home, cwd), id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("create Claude history dir: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write raw Claude history: %v", err)
	}
	return p
}

// TestResumeHistory_CodexResolvesOneRootAndItsGuardianLineage models the live VM layout:
// one CLI/TUI conversation plus a guardian child. Source is deliberately NOT "cli"; lineage,
// not a provider-version-specific source label, is the invariant that makes the root unique.
func TestResumeHistory_CodexResolvesOneRootAndItsGuardianLineage(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "work")
	writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "vscode", "")
	writeCodexHistory(t, home, legacyCodexChildID, cwd, legacyCreatedAt.Add(2*time.Second), legacyCodexRootID, "subagent", "")

	got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
	requireHistoryResult(t, got, resumeHistoryFound, legacyCodexRootID)
}

// TestResumeHistory_CodexAllowsASoleExternallyParentedCandidate covers old sessions whose only
// in-window rollout is itself parented by a thread outside the bounded time window. With no
// competing candidate, refusing it would preserve the reported 1/10 VM failure unnecessarily.
func TestResumeHistory_CodexAllowsASoleExternallyParentedCandidate(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "work")
	writeCodexHistory(t, home, legacyCodexChildID, cwd, legacyCreatedAt.Add(time.Second),
		"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "subagent", "")

	got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
	requireHistoryResult(t, got, resumeHistoryFound, legacyCodexChildID)
}

func TestResumeHistory_CodexResolvesTransitiveAndExternallyRootedLineage(t *testing.T) {
	t.Run("root child grandchild", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "vscode", "")
		writeCodexHistory(t, home, legacyCodexChildID, cwd, legacyCreatedAt.Add(2*time.Second), legacyCodexRootID, "subagent", "")
		writeCodexHistory(t, home, legacyCodexGrandID, cwd, legacyCreatedAt.Add(3*time.Second), legacyCodexChildID, "subagent", "")
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryFound, legacyCodexRootID)
	})

	t.Run("externally parented local root and child", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		external := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
		writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), external, "subagent", "")
		writeCodexHistory(t, home, legacyCodexChildID, cwd, legacyCreatedAt.Add(2*time.Second), legacyCodexRootID, "subagent", "")
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryFound, legacyCodexRootID)
	})
}

func TestResumeHistory_CodexRejectsAmbiguousOrBrokenCandidateGraphs(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, home, cwd string)
	}{
		{"two independent roots", func(t *testing.T, home, cwd string) {
			writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
			writeCodexHistory(t, home, legacyCodexOtherID, cwd, legacyCreatedAt.Add(2*time.Second), "", "cli", "")
		}},
		{"cycle", func(t *testing.T, home, cwd string) {
			writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), legacyCodexChildID, "subagent", "")
			writeCodexHistory(t, home, legacyCodexChildID, cwd, legacyCreatedAt.Add(2*time.Second), legacyCodexRootID, "subagent", "")
		}},
		{"one root plus unrelated external lineage", func(t *testing.T, home, cwd string) {
			writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
			writeCodexHistory(t, home, legacyCodexChildID, cwd, legacyCreatedAt.Add(2*time.Second), legacyCodexRootID, "subagent", "")
			writeCodexHistory(t, home, legacyCodexOtherID, cwd, legacyCreatedAt.Add(3*time.Second), legacyCodexFourthID, "subagent", "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cwd := filepath.Join(home, "work")
			tc.build(t, home, cwd)
			got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
			requireHistoryResult(t, got, resumeHistoryAmbiguous, "")
		})
	}
}

// TestResumeHistory_CodexDuplicateIDAcrossFilesFailsClosed ensures a copied/moved rollout does
// not look like corroboration. There must be one candidate file, not merely one distinct id.
func TestResumeHistory_CodexDuplicateIDAcrossFilesFailsClosed(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "work")
	created := time.Date(2026, 8, 14, 23, 59, 50, 0, time.UTC)
	writeCodexHistory(t, home, legacyCodexRootID, cwd, created.Add(5*time.Second), "", "cli", "")
	writeCodexHistory(t, home, legacyCodexRootID, cwd, created.Add(15*time.Second), "", "cli", "")

	got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, created))
	requireHistoryResult(t, got, resumeHistoryAmbiguous, "")
}

// TestResumeHistory_CodexReadsOnlyTheSmallMetadataLine proves the cap applies through the first
// newline, not to total rollout size. Real rollouts are routinely megabytes after a small
// session_meta line; reading or rejecting that body recreated the characterization bug.
func TestResumeHistory_CodexReadsOnlyTheSmallMetadataLine(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "work")
	writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli",
		strings.Repeat("x", 3<<20))

	limits := generousResumeHistoryLimits()
	limits.MaxTotalBytes = 128 << 10
	got := resolveHistory(t, home, limits, legacySource("codex", cwd, legacyCreatedAt))
	requireHistoryResult(t, got, resumeHistoryFound, legacyCodexRootID)
}

func TestResumeHistory_CodexRequiresAbsoluteCWDsWithEqualCleanedValues(t *testing.T) {
	cases := []struct {
		name         string
		metaCWD      string
		candidateCWD string
		want         resumeHistoryOutcome
	}{
		{"both exact", "/work/project", "/work/project", resumeHistoryFound},
		{"meta relative", "work/project", "work/project", resumeHistoryNoMatch},
		{"candidate relative", "/work/project", "work/project", resumeHistoryNoMatch},
		{"meta syntactic equivalent", "/work/../work/project", "/work/project", resumeHistoryFound},
		{"candidate syntactic equivalent", "/work/project", "/work/../work/project", resumeHistoryFound},
		{"cleaned values equal despite bytes", "/work/project", "/work/./project", resumeHistoryFound},
		{"stored trailing slash", "/work/project/", "/work/project", resumeHistoryFound},
		{"absolute mismatch", "/work/project-a", "/work/project-b", resumeHistoryNoMatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeCodexHistory(t, home, legacyCodexRootID, tc.candidateCWD, legacyCreatedAt.Add(time.Second), "", "cli", "")
			got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", tc.metaCWD, legacyCreatedAt))
			wantID := ""
			if tc.want == resumeHistoryFound {
				wantID = legacyCodexRootID
			}
			requireHistoryResult(t, got, tc.want, wantID)
		})
	}
}

func TestResumeHistory_CodexEnforcesTheMeasuredTimeWindow(t *testing.T) {
	cases := []struct {
		name  string
		delta time.Duration
		want  resumeHistoryOutcome
	}{
		{"lower bound inclusive", -2 * time.Second, resumeHistoryFound},
		{"just before lower bound", -2*time.Second - time.Nanosecond, resumeHistoryNoMatch},
		{"upper bound inclusive", 30 * time.Second, resumeHistoryFound},
		{"just after upper bound", 30*time.Second + time.Nanosecond, resumeHistoryNoMatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cwd := filepath.Join(home, "work")
			writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(tc.delta), "", "cli", "")
			got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
			wantID := ""
			if tc.want == resumeHistoryFound {
				wantID = legacyCodexRootID
			}
			requireHistoryResult(t, got, tc.want, wantID)
		})
	}
}

func TestResumeHistory_CodexRejectsMalformedCriticalRecords(t *testing.T) {
	cwd := "/work/project"
	ts := legacyCreatedAt.Add(time.Second).Format(time.RFC3339Nano)
	cases := []struct {
		name string
		line string
	}{
		{"duplicate top-level type", fmt.Sprintf(`{"type":"session_meta","type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q}}`, legacyCodexRootID, ts, cwd)},
		{"duplicate top-level payload", fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q},"payload":{"id":%q,"timestamp":%q,"cwd":%q}}`, legacyCodexRootID, ts, cwd, legacyCodexRootID, ts, cwd)},
		{"duplicate id", fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"id":%q,"timestamp":%q,"cwd":%q}}`, legacyCodexRootID, legacyCodexRootID, ts, cwd)},
		{"duplicate cwd", fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q,"cwd":%q}}`, legacyCodexRootID, ts, cwd, cwd)},
		{"duplicate timestamp", fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"timestamp":%q,"timestamp":%q,"cwd":%q}}`, legacyCodexRootID, ts, ts, cwd)},
		{"duplicate parent", fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q,"parent_thread_id":null,"parent_thread_id":null}}`, legacyCodexRootID, ts, cwd)},
		{"trailing JSON", fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q}} {}`, legacyCodexRootID, ts, cwd)},
		{"wrong id type", fmt.Sprintf(`{"type":"session_meta","payload":{"id":7,"timestamp":%q,"cwd":%q}}`, ts, cwd)},
		{"wrong payload type", `{"type":"session_meta","payload":[]}`},
		{"missing id", fmt.Sprintf(`{"type":"session_meta","payload":{"timestamp":%q,"cwd":%q}}`, ts, cwd)},
		{"missing timestamp", fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q}}`, legacyCodexRootID, cwd)},
		{"missing cwd", fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"timestamp":%q}}`, legacyCodexRootID, ts)},
		{"invalid parent UUID", fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q,"parent_thread_id":"parent-prose"}}`, legacyCodexRootID, ts, cwd)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeRawCodexHistory(t, home, legacyCodexRootID, legacyCreatedAt.Add(time.Second), tc.line)
			got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
			requireHistoryResult(t, got, resumeHistoryUnsafe, "")
		})
	}
}

func TestResumeHistory_CodexRequiresCanonicalFilenameIdentityAndCompleteLine(t *testing.T) {
	cwd := "/work/project"
	ts := legacyCreatedAt.Add(time.Second).Format(time.RFC3339Nano)
	t.Run("filename payload mismatch", func(t *testing.T) {
		home := t.TempDir()
		line := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q}}`, legacyCodexOtherID, ts, cwd)
		writeRawCodexHistory(t, home, legacyCodexRootID, legacyCreatedAt.Add(time.Second), line)
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
	t.Run("uppercase UUID", func(t *testing.T) {
		home := t.TempDir()
		upper := strings.ToUpper(legacyCodexRootID)
		line := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q}}`, upper, ts, cwd)
		writeRawCodexHistory(t, home, upper, legacyCreatedAt.Add(time.Second), line)
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
	t.Run("trailing whitespace before newline accepted", func(t *testing.T) {
		home := t.TempDir()
		line := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q}}   `, legacyCodexRootID, ts, cwd)
		writeRawCodexHistory(t, home, legacyCodexRootID, legacyCreatedAt.Add(time.Second), line)
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryFound, legacyCodexRootID)
	})
	t.Run("missing newline is incomplete", func(t *testing.T) {
		home := t.TempDir()
		p := codexHistoryPath(home, legacyCreatedAt.Add(time.Second), legacyCodexRootID)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		line := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"timestamp":%q,"cwd":%q}}`, legacyCodexRootID, ts, cwd)
		if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
}

func TestResumeHistory_ClaudeFindsTheFirstIdentityRecordWithinTheBoundedPrefix(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "work", "project")
	prefix := `{"type":"progress","padding":"` + strings.Repeat("x", 32<<10) + `"}` + "\n"
	writeClaudeHistory(t, home, legacyClaudeID, cwd, legacyCreatedAt.Add(time.Second), prefix,
		strings.Repeat("y", 3<<20))

	limits := generousResumeHistoryLimits()
	limits.MaxTotalBytes = 128 << 10
	got := resolveHistory(t, home, limits, legacySource("claude", cwd, legacyCreatedAt))
	requireHistoryResult(t, got, resumeHistoryFound, legacyClaudeID)
}

// TestResumeHistory_ClaudeUsesProviderProjectDirectoryEncoding pins Claude's actual mapping:
// every non-ASCII-alphanumeric cwd character becomes '-', not only path separators. Dots,
// spaces and underscores occur in ordinary worktree paths on this VM.
func TestResumeHistory_ClaudeUsesProviderProjectDirectoryEncoding(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/nathan/My Repo/.claude/work_trees/x"
	wantDirName := "-home-nathan-My-Repo--claude-work-trees-x"
	if got := filepath.Base(claudeProjectDir(home, cwd)); got != wantDirName {
		t.Fatalf("Claude project encoding = %q, want %q", got, wantDirName)
	}
	writeClaudeHistory(t, home, legacyClaudeID, cwd, legacyCreatedAt.Add(time.Second), "", "")
	got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("claude", cwd, legacyCreatedAt))
	requireHistoryResult(t, got, resumeHistoryFound, legacyClaudeID)
}

func TestResumeHistory_ClaudeRejectsAmbiguityWrongCWDAndWrongTime(t *testing.T) {
	t.Run("ambiguity", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		writeClaudeHistory(t, home, legacyClaudeID, cwd, legacyCreatedAt.Add(time.Second), "", "")
		writeClaudeHistory(t, home, legacyClaudeOtherID, cwd, legacyCreatedAt.Add(2*time.Second), "", "")
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("claude", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryAmbiguous, "")
	})
	t.Run("wrong cwd", func(t *testing.T) {
		home := t.TempDir()
		metaCWD := filepath.Join(home, "work")
		// Put the file in the requested project's directory while keeping a mismatched cwd in
		// the record; directory selection alone must never be treated as identity evidence.
		p := filepath.Join(claudeProjectDir(home, metaCWD), legacyClaudeID+".jsonl")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"timestamp":%q}`+"\n",
			legacyClaudeID, filepath.Join(home, "other"), legacyCreatedAt.Add(time.Second).Format(time.RFC3339Nano))
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("claude", metaCWD, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryNoMatch, "")
	})
	t.Run("wrong time", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		writeClaudeHistory(t, home, legacyClaudeID, cwd, legacyCreatedAt.Add(31*time.Second), "", "")
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("claude", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryNoMatch, "")
	})
}

func TestResumeHistory_ClaudeRequiresAbsoluteCWDAndComparesCleanedValues(t *testing.T) {
	for _, tc := range []struct {
		name       string
		metaCWD    string
		historyCWD string
		want       resumeHistoryOutcome
	}{
		{"relative", "work/project", "work/project", resumeHistoryNoMatch},
		{"syntactic equivalent", "/work/project", "/work/../work/project", resumeHistoryFound},
		{"stored trailing slash", "/work/project/", "/work/project", resumeHistoryFound},
		{"wrong cleaned cwd", "/work/project", "/work/other", resumeHistoryNoMatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			// The directory is derived from the CLEAN trusted session cwd; history cwd itself is
			// still read from the record and compared after cleaning both absolute values.
			projectCWD := filepath.Clean(tc.metaCWD)
			p := filepath.Join(claudeProjectDir(home, projectCWD), legacyClaudeID+".jsonl")
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"timestamp":%q}`+"\n",
				legacyClaudeID, tc.historyCWD, legacyCreatedAt.Add(time.Second).Format(time.RFC3339Nano))
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("claude", tc.metaCWD, legacyCreatedAt))
			wantID := ""
			if tc.want == resumeHistoryFound {
				wantID = legacyClaudeID
			}
			requireHistoryResult(t, got, tc.want, wantID)
		})
	}
}

func TestResumeHistory_ClaudeRejectsMalformedCriticalRecords(t *testing.T) {
	cwd := "/work/project"
	ts := legacyCreatedAt.Add(time.Second).Format(time.RFC3339Nano)
	cases := []struct {
		name string
		line string
	}{
		{"duplicate id", fmt.Sprintf(`{"sessionId":%q,"sessionId":%q,"cwd":%q,"timestamp":%q}`, legacyClaudeID, legacyClaudeID, cwd, ts)},
		{"duplicate cwd", fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"cwd":%q,"timestamp":%q}`, legacyClaudeID, cwd, cwd, ts)},
		{"duplicate timestamp", fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"timestamp":%q,"timestamp":%q}`, legacyClaudeID, cwd, ts, ts)},
		{"trailing JSON", fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"timestamp":%q}{}`, legacyClaudeID, cwd, ts)},
		{"null id", fmt.Sprintf(`{"sessionId":null,"cwd":%q,"timestamp":%q}`, cwd, ts)},
		{"malformed earlier record", `{not json}` + "\n" + fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"timestamp":%q}`, legacyClaudeID, cwd, ts)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeRawClaudeHistory(t, home, legacyClaudeID, cwd, tc.line+"\n")
			got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("claude", cwd, legacyCreatedAt))
			requireHistoryResult(t, got, resumeHistoryUnsafe, "")
		})
	}
}

func TestResumeHistory_ClaudeRequiresCanonicalFilenameIdentityAndFirstBearingRecord(t *testing.T) {
	cwd := "/work/project"
	ts := legacyCreatedAt.Add(time.Second).Format(time.RFC3339Nano)
	t.Run("filename session id mismatch", func(t *testing.T) {
		home := t.TempDir()
		body := fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"timestamp":%q}`+"\n", legacyClaudeOtherID, cwd, ts)
		writeRawClaudeHistory(t, home, legacyClaudeID, cwd, body)
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("claude", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
	t.Run("uppercase UUID", func(t *testing.T) {
		home := t.TempDir()
		upper := strings.ToUpper(legacyClaudeID)
		body := fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"timestamp":%q}`+"\n", upper, cwd, ts)
		writeRawClaudeHistory(t, home, upper, cwd, body)
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("claude", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
	t.Run("first identity-bearing record conflicts", func(t *testing.T) {
		home := t.TempDir()
		body := fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"timestamp":%q}`+"\n", legacyClaudeOtherID, cwd, ts) +
			fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"timestamp":%q}`+"\n", legacyClaudeID, cwd, ts)
		writeRawClaudeHistory(t, home, legacyClaudeID, cwd, body)
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("claude", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
	t.Run("trailing whitespace before newline accepted", func(t *testing.T) {
		home := t.TempDir()
		body := fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"timestamp":%q}   `+"\n", legacyClaudeID, cwd, ts)
		writeRawClaudeHistory(t, home, legacyClaudeID, cwd, body)
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("claude", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryFound, legacyClaudeID)
	})
	t.Run("missing newline is incomplete", func(t *testing.T) {
		home := t.TempDir()
		body := fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"timestamp":%q}`, legacyClaudeID, cwd, ts)
		writeRawClaudeHistory(t, home, legacyClaudeID, cwd, body)
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("claude", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
}

func TestResumeHistory_RejectsSymlinkedRootsDirectoriesAndFiles(t *testing.T) {
	t.Run("Codex provider root", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		cwd := filepath.Join(home, "work")
		writeCodexHistory(t, outside, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		if err := os.Symlink(filepath.Join(outside, ".codex"), filepath.Join(home, ".codex")); err != nil {
			t.Fatal(err)
		}
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
	t.Run("Codex sessions directory", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		cwd := filepath.Join(home, "work")
		writeCodexHistory(t, outside, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, ".codex", "sessions"), filepath.Join(home, ".codex", "sessions")); err != nil {
			t.Fatal(err)
		}
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
	t.Run("Codex date directory", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		cwd := filepath.Join(home, "work")
		outsideFile := writeCodexHistory(t, outside, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		dayRel, err := filepath.Rel(filepath.Join(outside, ".codex", "sessions"), filepath.Dir(outsideFile))
		if err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(home, ".codex", "sessions", dayRel)
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Dir(outsideFile), link); err != nil {
			t.Fatal(err)
		}
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
	t.Run("Codex rollout file", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		cwd := filepath.Join(home, "work")
		outsideFile := writeCodexHistory(t, outside, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		link := codexHistoryPath(home, legacyCreatedAt.Add(time.Second), legacyCodexRootID)
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideFile, link); err != nil {
			t.Fatal(err)
		}
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
	t.Run("Claude project root", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		cwd := "/work/project"
		writeClaudeHistory(t, outside, legacyClaudeID, cwd, legacyCreatedAt.Add(time.Second), "", "")
		if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, ".claude", "projects"), filepath.Join(home, ".claude", "projects")); err != nil {
			t.Fatal(err)
		}
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("claude", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
	t.Run("Claude transcript file", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		cwd := "/work/project"
		outsideFile := writeClaudeHistory(t, outside, legacyClaudeID, cwd, legacyCreatedAt.Add(time.Second), "", "")
		link := filepath.Join(claudeProjectDir(home, cwd), legacyClaudeID+".jsonl")
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideFile, link); err != nil {
			t.Fatal(err)
		}
		got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource("claude", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
}

// TestResumeHistory_PathReplacementBetweenInspectionAndOpenFailsClosed deterministically
// exercises the TOCTOU boundary. beforeOpen is an unexported, test-only limit hook; production
// leaves it nil. Replacing a previously-inspected ancestor, directory or file with a symlink at
// that exact point must be rejected by rooted/no-follow opens plus pre/post inode verification.
func TestResumeHistory_PathReplacementBetweenInspectionAndOpenFailsClosed(t *testing.T) {
	t.Run("provider ancestor swap", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		cwd := filepath.Join(home, "work")
		writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		writeCodexHistory(t, outside, legacyCodexOtherID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		providerRoot := filepath.Join(home, ".codex")
		hold := filepath.Join(home, ".codex-inspected")
		swapped := false
		limits := generousResumeHistoryLimits()
		beforeOpen := func(path string) {
			if swapped || filepath.Base(filepath.Clean(path)) != ".codex" {
				return
			}
			swapped = true
			if err := os.Rename(providerRoot, hold); err != nil {
				t.Fatalf("swap provider root: %v", err)
			}
			if err := os.Symlink(filepath.Join(outside, ".codex"), providerRoot); err != nil {
				t.Fatalf("install provider-root symlink: %v", err)
			}
		}
		got := resolveHistoryWithBeforeOpen(t, home, limits, legacySource("codex", cwd, legacyCreatedAt), beforeOpen)
		if !swapped {
			t.Fatal("resolver never invoked beforeOpen at the provider-root boundary")
		}
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})

	t.Run("date directory swap", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		cwd := filepath.Join(home, "work")
		safeFile := writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		evilFile := writeCodexHistory(t, outside, legacyCodexOtherID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		safeDay := filepath.Dir(safeFile)
		hold := safeDay + "-inspected"
		swapped := false
		limits := generousResumeHistoryLimits()
		beforeOpen := func(path string) {
			if swapped || filepath.Base(filepath.Clean(path)) != filepath.Base(safeDay) {
				return
			}
			swapped = true
			if err := os.Rename(safeDay, hold); err != nil {
				t.Fatalf("swap date directory: %v", err)
			}
			if err := os.Symlink(filepath.Dir(evilFile), safeDay); err != nil {
				t.Fatalf("install date-directory symlink: %v", err)
			}
		}
		got := resolveHistoryWithBeforeOpen(t, home, limits, legacySource("codex", cwd, legacyCreatedAt), beforeOpen)
		if !swapped {
			t.Fatal("resolver never invoked beforeOpen at the date-directory boundary")
		}
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})

	t.Run("rollout file swap", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		cwd := filepath.Join(home, "work")
		safeFile := writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		evilFile := writeCodexHistory(t, outside, legacyCodexOtherID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		hold := safeFile + ".inspected"
		swapped := false
		limits := generousResumeHistoryLimits()
		beforeOpen := func(path string) {
			if swapped || filepath.Base(filepath.Clean(path)) != filepath.Base(safeFile) {
				return
			}
			swapped = true
			if err := os.Rename(safeFile, hold); err != nil {
				t.Fatalf("swap rollout file: %v", err)
			}
			if err := os.Symlink(evilFile, safeFile); err != nil {
				t.Fatalf("install rollout-file symlink: %v", err)
			}
		}
		got := resolveHistoryWithBeforeOpen(t, home, limits, legacySource("codex", cwd, legacyCreatedAt), beforeOpen)
		if !swapped {
			t.Fatal("resolver never invoked beforeOpen at the rollout-file boundary")
		}
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
}

// TestResumeHistory_RegularInodeReplacementBetweenInspectionAndOpenFailsClosed is distinct
// from the symlink cases above: O_NOFOLLOW alone cannot detect these swaps. The resolver must
// compare the inspected and opened inode (os.SameFile or equivalent) while traversing through
// its anchored root, or it will silently accept the replacement's conversation id.
func TestResumeHistory_RegularInodeReplacementBetweenInspectionAndOpenFailsClosed(t *testing.T) {
	t.Run("provider directory inode", func(t *testing.T) {
		home := t.TempDir()
		replacementHome := t.TempDir()
		cwd := filepath.Join(home, "work")
		writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		writeCodexHistory(t, replacementHome, legacyCodexOtherID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		safe := filepath.Join(home, ".codex")
		hold := filepath.Join(home, ".codex-inspected")
		replacement := filepath.Join(replacementHome, ".codex")
		swapped := false
		beforeOpen := func(path string) {
			if swapped || filepath.Base(filepath.Clean(path)) != ".codex" {
				return
			}
			swapped = true
			if err := os.Rename(safe, hold); err != nil {
				t.Fatalf("move inspected provider directory: %v", err)
			}
			if err := os.Rename(replacement, safe); err != nil {
				t.Fatalf("replace provider directory inode: %v", err)
			}
		}
		got := resolveHistoryWithBeforeOpen(t, home, generousResumeHistoryLimits(),
			legacySource("codex", cwd, legacyCreatedAt), beforeOpen)
		if !swapped {
			t.Fatal("resolver never exposed the provider-directory open boundary")
		}
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})

	t.Run("date directory inode", func(t *testing.T) {
		home := t.TempDir()
		replacementHome := t.TempDir()
		cwd := filepath.Join(home, "work")
		safeFile := writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		replacementFile := writeCodexHistory(t, replacementHome, legacyCodexOtherID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		safe := filepath.Dir(safeFile)
		hold := safe + "-inspected"
		replacement := filepath.Dir(replacementFile)
		swapped := false
		beforeOpen := func(path string) {
			if swapped || filepath.Base(filepath.Clean(path)) != filepath.Base(safe) {
				return
			}
			swapped = true
			if err := os.Rename(safe, hold); err != nil {
				t.Fatalf("move inspected date directory: %v", err)
			}
			if err := os.Rename(replacement, safe); err != nil {
				t.Fatalf("replace date directory inode: %v", err)
			}
		}
		got := resolveHistoryWithBeforeOpen(t, home, generousResumeHistoryLimits(),
			legacySource("codex", cwd, legacyCreatedAt), beforeOpen)
		if !swapped {
			t.Fatal("resolver never exposed the date-directory open boundary")
		}
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})

	t.Run("rollout file inode", func(t *testing.T) {
		home := t.TempDir()
		replacementHome := t.TempDir()
		cwd := filepath.Join(home, "work")
		safe := writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		replacement := writeCodexHistory(t, replacementHome, legacyCodexOtherID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		hold := safe + ".inspected"
		swapped := false
		beforeOpen := func(path string) {
			if swapped || filepath.Base(filepath.Clean(path)) != filepath.Base(safe) {
				return
			}
			swapped = true
			if err := os.Rename(safe, hold); err != nil {
				t.Fatalf("move inspected rollout: %v", err)
			}
			if err := os.Rename(replacement, safe); err != nil {
				t.Fatalf("replace rollout inode: %v", err)
			}
		}
		got := resolveHistoryWithBeforeOpen(t, home, generousResumeHistoryLimits(),
			legacySource("codex", cwd, legacyCreatedAt), beforeOpen)
		if !swapped {
			t.Fatal("resolver never exposed the rollout-file open boundary")
		}
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
}

func TestResumeHistory_ResourceBudgetsFailClosed(t *testing.T) {
	t.Run("directory entries max plus one", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		p := writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		if err := os.WriteFile(filepath.Join(filepath.Dir(p), "unrelated.jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		limits := generousResumeHistoryLimits()
		limits.MaxEntries = 1
		got := resolveHistory(t, home, limits, legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
	t.Run("open files", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		writeCodexHistory(t, home, legacyCodexOtherID, cwd, legacyCreatedAt.Add(2*time.Second), "", "cli", "")
		limits := generousResumeHistoryLimits()
		limits.MaxOpenFiles = 1
		got := resolveHistory(t, home, limits, legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
	t.Run("per record bytes", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		limits := generousResumeHistoryLimits()
		limits.MaxRecordBytes = 64
		got := resolveHistory(t, home, limits, legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
	t.Run("cumulative metadata bytes", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		writeCodexHistory(t, home, legacyCodexOtherID, cwd, legacyCreatedAt.Add(2*time.Second), "", "cli", "")
		limits := generousResumeHistoryLimits()
		limits.MaxRecordBytes = 1024
		limits.MaxTotalBytes = 400
		got := resolveHistory(t, home, limits, legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
	})
}

func TestResumeHistory_MissingRootsAreNoMatchAndInspectedDisappearanceIsUnreadable(t *testing.T) {
	t.Run("missing provider roots", func(t *testing.T) {
		home := t.TempDir()
		for _, provider := range []string{"codex", "claude"} {
			got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource(provider, "/work", legacyCreatedAt))
			requireHistoryResult(t, got, resumeHistoryNoMatch, "")
		}
	})

	t.Run("inspected rollout disappears before open", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		rollout := writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		hold := rollout + ".vanished"
		removed := false
		beforeOpen := func(path string) {
			if removed || filepath.Base(filepath.Clean(path)) != filepath.Base(rollout) {
				return
			}
			removed = true
			if err := os.Rename(rollout, hold); err != nil {
				t.Fatalf("remove inspected rollout before open: %v", err)
			}
		}
		got := resolveHistoryWithBeforeOpen(t, home, generousResumeHistoryLimits(),
			legacySource("codex", cwd, legacyCreatedAt), beforeOpen)
		if !removed {
			t.Fatal("resolver never exposed the rollout open boundary")
		}
		requireHistoryResult(t, got, resumeHistoryUnreadable, "")
	})
}

func TestResumeHistory_BudgetBoundariesAndNonCandidates(t *testing.T) {
	t.Run("exact maxima are inclusive", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		rollout := writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		data, err := os.ReadFile(rollout)
		if err != nil {
			t.Fatal(err)
		}
		lineBytes := len(data)
		limits := generousResumeHistoryLimits()
		limits.MaxEntries = 1
		limits.MaxOpenFiles = 1
		limits.MaxRecordBytes = int64(lineBytes)
		limits.MaxTotalBytes = int64(lineBytes)
		got := resolveHistory(t, home, limits, legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryFound, legacyCodexRootID)
	})

	t.Run("malformed non-candidate counts entry but is not opened", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		rollout := writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		if err := os.WriteFile(filepath.Join(filepath.Dir(rollout), "notes.jsonl"), []byte("not json and must not be opened\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		limits := generousResumeHistoryLimits()
		limits.MaxEntries = 2
		limits.MaxOpenFiles = 1
		got := resolveHistory(t, home, limits, legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryFound, legacyCodexRootID)
	})

	t.Run("entry budget is cumulative across all three date directories", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		rollout := writeCodexHistory(t, home, legacyCodexRootID, cwd, legacyCreatedAt.Add(time.Second), "", "cli", "")
		for _, day := range []time.Time{legacyCreatedAt.AddDate(0, 0, -1), legacyCreatedAt.AddDate(0, 0, 1)} {
			dir := filepath.Dir(codexHistoryPath(home, day, legacyCodexOtherID))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "notes.jsonl"), []byte("ignored\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		_ = rollout
		limits := generousResumeHistoryLimits()
		limits.MaxEntries = 2
		got := resolveHistory(t, home, limits, legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryUnsafe, "")
		limits.MaxEntries = 3
		got = resolveHistory(t, home, limits, legacySource("codex", cwd, legacyCreatedAt))
		requireHistoryResult(t, got, resumeHistoryFound, legacyCodexRootID)
	})
}

// TestResumeHistory_UnknownProvidersDoNotAcquireSpeculativeScanners preserves OpenCode and AGY
// behavior. Until their durable formats are characterized, the only supported source is their
// existing terminal marker; a missing old id remains a typed no-match without touching HOME.
func TestResumeHistory_UnknownProvidersDoNotAcquireSpeculativeScanners(t *testing.T) {
	home := t.TempDir()
	for _, provider := range []string{"opencode", "agy", "future-provider"} {
		t.Run(provider, func(t *testing.T) {
			got := resolveHistory(t, home, generousResumeHistoryLimits(), legacySource(provider, "/work", legacyCreatedAt))
			requireHistoryResult(t, got, resumeHistoryUnsupported, "")
		})
	}
}
