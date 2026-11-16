package skeleton

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const agyTranscriptConvID = "fb5e3e02-e5ef-4d25-b398-aead20366441"

func agySource(local, cwd, convID string, proc status.Process) persist.Meta {
	return persist.Meta{
		ID:             local,
		AgentType:      "agy",
		ConversationID: convID,
		Cwd:            cwd,
		CreatedAt:      legacyCreatedAt,
		LastActivity:   legacyCreatedAt,
		Status:         status.Status{Process: proc},
	}
}

func writeAgyTranscript(t *testing.T, root, convID, body string) string {
	t.Helper()
	path := filepath.Join(root, ".gemini", "antigravity-cli", "brain", convID, ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validAgyStepLine = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-16T18:39:25Z","content":"hi"}` + "\n"

func TestLocateTranscript_AgyFindsBrainTranscript(t *testing.T) {
	t.Run("complete step record", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		want := writeAgyTranscript(t, home, agyTranscriptConvID, validAgyStepLine)
		src := agySource("srclocal", cwd, agyTranscriptConvID, status.ProcessRunning)

		got, outcome := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()).LocateTranscript(src, agyTranscriptConvID)
		if outcome != resumeHistoryFound || got != want {
			t.Fatalf("LocateTranscript = (%q, %v), want (%q, found)", got, outcome, want)
		}
	})

	t.Run("live transcript with trailing partial line", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		want := writeAgyTranscript(t, home, agyTranscriptConvID, validAgyStepLine+`{"step_index":1,"type":"PLANNER`)
		src := agySource("srclocal", cwd, agyTranscriptConvID, status.ProcessRunning)

		got, outcome := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()).LocateTranscript(src, agyTranscriptConvID)
		if outcome != resumeHistoryFound || got != want {
			t.Fatalf("LocateTranscript = (%q, %v), want (%q, found)", got, outcome, want)
		}
	})

	t.Run("accepts clean strict-descendant .gemini alias inside home", func(t *testing.T) {
		home := t.TempDir()
		cwd := filepath.Join(home, "work")
		realRoot := filepath.Join(home, "xdg", "gemini-home")
		if err := os.MkdirAll(realRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realRoot, filepath.Join(home, ".gemini")); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(realRoot, "antigravity-cli", "brain", agyTranscriptConvID, ".system_generated", "logs", "transcript.jsonl")
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(validAgyStepLine), 0o600); err != nil {
			t.Fatal(err)
		}
		src := agySource("srclocal", cwd, agyTranscriptConvID, status.ProcessRunning)

		got, outcome := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()).LocateTranscript(src, agyTranscriptConvID)
		if outcome != resumeHistoryFound || got != target {
			t.Fatalf("LocateTranscript(alias) = (%q, %v), want (%q, found)", got, outcome, target)
		}
	})
}

func TestLocateTranscript_AgyRejectsInvalidOrUnsafe(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(t *testing.T, home string)
		want    resumeHistoryOutcome
	}{
		{
			name:    "missing transcript",
			arrange: func(*testing.T, string) {},
			want:    resumeHistoryNoMatch,
		},
		{
			name: "zero-byte transcript",
			arrange: func(t *testing.T, home string) {
				writeAgyTranscript(t, home, agyTranscriptConvID, "")
			},
			want: resumeHistoryNoMatch,
		},
		{
			name: "unrelated json without step fields",
			arrange: func(t *testing.T, home string) {
				writeAgyTranscript(t, home, agyTranscriptConvID, `{"hello":"world"}`+"\n")
			},
			want: resumeHistoryNoMatch,
		},
		{
			name: "symlink inside brain directory",
			arrange: func(t *testing.T, home string) {
				logsDir := filepath.Join(home, ".gemini", "antigravity-cli", "brain", agyTranscriptConvID, ".system_generated", "logs")
				if err := os.MkdirAll(logsDir, 0o700); err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(home, "outside.jsonl")
				if err := os.WriteFile(outside, []byte(validAgyStepLine), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(logsDir, "transcript.jsonl")); err != nil {
					t.Fatal(err)
				}
			},
			want: resumeHistoryUnsafe,
		},
		{
			name: "outside-home .gemini symlink",
			arrange: func(t *testing.T, home string) {
				outside := t.TempDir()
				writeAgyTranscript(t, outside, agyTranscriptConvID, validAgyStepLine)
				if err := os.Symlink(filepath.Join(outside, ".gemini"), filepath.Join(home, ".gemini")); err != nil {
					t.Fatal(err)
				}
			},
			want: resumeHistoryUnsafe,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cwd := filepath.Join(home, "work")
			tc.arrange(t, home)
			src := agySource("srclocal", cwd, agyTranscriptConvID, status.ProcessRunning)

			got, outcome := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits()).LocateTranscript(src, agyTranscriptConvID)
			if outcome != tc.want || got != "" {
				t.Fatalf("LocateTranscript = (%q, %v), want (\"\", %v)", got, outcome, tc.want)
			}
		})
	}
}

func TestHandsOff_ComposesForAgySourceRunningEndedAndWorktree(t *testing.T) {
	for _, tc := range []struct {
		name     string
		proc     status.Process
		worktree bool
	}{
		{"running", status.ProcessRunning, false},
		{"ended", status.ProcessExited, false},
		{"worktree", status.ProcessRunning, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			const local = "srclocal"
			repoCwd := filepath.Join(home, "repo")
			agentCwd := repoCwd
			if tc.worktree {
				agentCwd = filepath.Join(home, "repo", ".swarm", "worktrees", "wt-agy")
			}
			if err := os.MkdirAll(agentCwd, 0o700); err != nil {
				t.Fatal(err)
			}
			src := agySource(local, repoCwd, agyTranscriptConvID, tc.proc)
			if tc.worktree {
				src.AgentCwd = agentCwd
				src.LaunchOptions = map[string]string{protocol.OptionWorktree: "true"}
			}
			src.CreatedAt = time.Date(2026, 11, 16, 18, 0, 0, 0, time.UTC)
			transcript := writeAgyTranscript(t, home, agyTranscriptConvID, validAgyStepLine)
			resolver := newFilesystemResumeHistoryResolver(home, generousResumeHistoryLimits())

			got, err := composeHandsOffLaunch(handsOffSpec(testEndpoint, local, "/ignored-new-work"), testEndpoint, srcGetter(local, src), resolver)
			if err != nil {
				t.Fatalf("composeHandsOffLaunch(agy %s) failed: %v", tc.name, err)
			}
			for _, want := range []string{agyTranscriptConvID, transcript, agentCwd, "agy", local} {
				if !strings.Contains(got.InitialPrompt, want) {
					t.Errorf("composed prompt missing %q:\n%s", want, got.InitialPrompt)
				}
			}
			if got.Cwd != agentCwd {
				t.Errorf("Cwd = %q, want %q", got.Cwd, agentCwd)
			}
			if got.SpawnedFrom != local || got.SpawnIntent != protocol.SpawnIntentHandoff || got.Supervision != "" {
				t.Errorf("lineage = (%q, %q, supervision %q)", got.SpawnedFrom, got.SpawnIntent, got.Supervision)
			}
			if got.Options["model"] != "opus" {
				t.Errorf("Options[model] = %q, want opus", got.Options["model"])
			}
		})
	}
}
