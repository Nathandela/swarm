package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func writeCodexReattachFixture(t *testing.T, home, folder, id, cwd string) string {
	t.Helper()
	dir := filepath.Join(home, folder)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-11-17T10-00-00-"+id+".jsonl")
	record, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": cwd, "source": "cli"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(record, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexReattachDiscoversNativeMetadataWithoutLaunching(t *testing.T) {
	home, cwd := filepath.Join(t.TempDir(), ".codex"), t.TempDir()
	t.Setenv("HOME", filepath.Dir(home))
	t.Setenv("CODEX_HOME", "")
	path := writeCodexReattachFixture(t, home, "sessions/2026/11/17", testClaudeLiveID, cwd)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	client := newFakeReattachClient()
	var stdout, stderr bytes.Buffer
	code := runReattach([]string{"--cli", "codex", "--dry-run"}, client, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), testClaudeLiveID) || !strings.Contains(stdout.String(), cwd) {
		t.Fatalf("discovery missing identity/cwd: %s", &stdout)
	}
	if len(client.launches) != 0 {
		t.Fatal("dry-run launched a session")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatal("native history changed")
	}
}

func TestCodexReattachRequiresTakeoverConfirmation(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")
	t.Setenv("HOME", filepath.Dir(home))
	t.Setenv("CODEX_HOME", "")
	writeCodexReattachFixture(t, home, "sessions/2026/11/17", testClaudeLiveID, t.TempDir())
	client := newFakeReattachClient()
	var stdout, stderr bytes.Buffer
	if code := runReattach([]string{"--cli", "codex"}, client, &stdout, &stderr); code != 1 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if len(client.launches) != 0 || !strings.Contains(stderr.String(), "--take-over") || !strings.Contains(stderr.String(), "stop") {
		t.Fatalf("unsafe import: launches=%d stderr=%q", len(client.launches), stderr.String())
	}
}

func TestCodexReattachSelectsOneSessionAndPreservesEnvironment(t *testing.T) {
	home, cwd := filepath.Join(t.TempDir(), ".codex"), t.TempDir()
	t.Setenv("HOME", filepath.Dir(home))
	t.Setenv("CODEX_HOME", "")
	writeCodexReattachFixture(t, home, "sessions/2026/11/17", testClaudeLiveID, cwd)
	writeCodexReattachFixture(t, home, "sessions/2026/11/18", testClaudeSettledID, t.TempDir())
	client := newFakeReattachClient()
	var stdout, stderr bytes.Buffer
	if code := runReattach([]string{"--cli", "codex", "--session", testClaudeLiveID, "--take-over"}, client, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if len(client.launches) != 1 {
		t.Fatalf("launches=%d", len(client.launches))
	}
	req := client.launches[0]
	if req.Agent != "codex" || req.Cwd != cwd || req.Options[protocol.OptionResumeConversationID] != testClaudeLiveID || req.InitialPrompt != "" {
		t.Fatalf("bad external resume: %#v", req)
	}
	if !strings.Contains(strings.Join(req.Env, "\n"), "HOME="+filepath.Dir(home)) {
		t.Fatal("HOME missing from launch environment")
	}
}

func TestCodexReattachAllIncludesArchivedAndDeduplicates(t *testing.T) {
	home, cwd := filepath.Join(t.TempDir(), ".codex"), t.TempDir()
	t.Setenv("HOME", filepath.Dir(home))
	t.Setenv("CODEX_HOME", "")
	writeCodexReattachFixture(t, home, "sessions/2026/11/17", testClaudeLiveID, cwd)
	writeCodexReattachFixture(t, home, "sessions/2026/11/18", testClaudeLiveID, cwd)
	writeCodexReattachFixture(t, home, "archived_sessions", testClaudeSettledID, t.TempDir())
	for _, all := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "all"}[all], func(t *testing.T) {
			client := newFakeReattachClient()
			var stdout, stderr bytes.Buffer
			args := []string{"--cli", "codex", "--take-over"}
			if all {
				args = append(args, "--all", "--dry-run")
			}
			if code := runReattach(args, client, &stdout, &stderr); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
			want := 1
			if all {
				want = 0
				if !strings.Contains(stdout.String(), testClaudeSettledID) || !strings.Contains(stdout.String(), "2 Codex session(s)") {
					t.Fatalf("archived discovery = %q", stdout.String())
				}
			}
			if len(client.launches) != want {
				t.Fatalf("launches=%d want %d", len(client.launches), want)
			}
		})
	}
}

func TestCodexReattachRejectsUnsafeMetadataBeforeAnyLaunch(t *testing.T) {
	for _, kind := range []string{"malformed", "oversize", "wrong-id", "relative-cwd", "missing-cwd", "symlink", "conflicting-duplicate", "mixed-archive"} {
		t.Run(kind, func(t *testing.T) {
			home, cwd := filepath.Join(t.TempDir(), ".codex"), t.TempDir()
			t.Setenv("HOME", filepath.Dir(home))
			t.Setenv("CODEX_HOME", "")
			writeCodexReattachFixture(t, home, "sessions/2026/11/17", testClaudeLiveID, cwd)
			path := writeCodexReattachFixture(t, home, "sessions/2026/11/18", testClaudeSettledID, cwd)
			var data []byte
			switch kind {
			case "malformed":
				data = []byte("{broken}\n")
			case "oversize":
				data = []byte(strings.Repeat("x", 65537) + "\n")
			case "wrong-id":
				data = []byte(`{"type":"session_meta","payload":{"id":"` + testClaudeLiveID + `","cwd":"` + cwd + `"}}` + "\n")
			case "relative-cwd":
				data = []byte(`{"type":"session_meta","payload":{"id":"` + testClaudeSettledID + `","cwd":"relative","source":"cli"}}` + "\n")
			case "missing-cwd":
				data = []byte(`{"type":"session_meta","payload":{"id":"` + testClaudeSettledID + `","cwd":"/this-swarmed-directory-does-not-exist","source":"cli"}}` + "\n")
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(home, "sessions/2026/11/17", filepath.Base(writeCodexReattachFixture(t, home, "outside", testClaudeSettledID, cwd))), path); err != nil {
					t.Fatal(err)
				}
			case "mixed-archive":
				writeCodexReattachFixture(t, home, "archived_sessions", testClaudeLiveID, cwd)
			case "conflicting-duplicate":
				writeCodexReattachFixture(t, home, "sessions/2026/11/19", testClaudeLiveID, t.TempDir())
			}
			if data != nil {
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			client := newFakeReattachClient()
			var stdout, stderr bytes.Buffer
			if code := runReattach([]string{"--cli", "codex", "--take-over", "--all"}, client, &stdout, &stderr); code != 1 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
			if len(client.launches) != 0 {
				t.Fatalf("invalid discovery launched %d session(s)", len(client.launches))
			}
		})
	}
}

func TestCodexReattachEmptyHomeAndUnknownSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", "")
	for _, selected := range []bool{false, true} {
		client := newFakeReattachClient()
		var stdout, stderr bytes.Buffer
		args := []string{"--cli", "codex", "--dry-run"}
		want := 0
		if selected {
			args = append(args, "--session", testClaudeLiveID)
			want = 1
		}
		if code := runReattach(args, client, &stdout, &stderr); code != want {
			t.Fatalf("exit=%d want=%d stderr=%q", code, want, stderr.String())
		}
	}
}

var errTestCodexLaunch = errors.New("codex resume refused")

func TestCodexReattachReportsLaunchFailure(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")
	t.Setenv("HOME", filepath.Dir(home))
	t.Setenv("CODEX_HOME", "")
	writeCodexReattachFixture(t, home, "sessions/2026/11/17", testClaudeLiveID, t.TempDir())
	client := newFakeReattachClient()
	client.launchErr = errTestCodexLaunch
	var stdout, stderr bytes.Buffer
	if code := runReattach([]string{"--cli", "codex", "--take-over"}, client, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), errTestCodexLaunch.Error()) {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}

func TestCodexReattachRejectsUnsupportedNativeHomeAndProfile(t *testing.T) {
	for _, selector := range []string{"CODEX_HOME", "CODEX_PROFILE"} {
		t.Run(selector, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), ".codex")
			t.Setenv("HOME", filepath.Dir(home))
			t.Setenv("CODEX_HOME", "")
			t.Setenv("CODEX_PROFILE", "")
			discovery := home
			if selector == "CODEX_HOME" {
				discovery = t.TempDir()
				t.Setenv(selector, discovery)
			} else {
				t.Setenv(selector, "other-profile")
			}
			writeCodexReattachFixture(t, discovery, "sessions/2026/11/17", testClaudeLiveID, t.TempDir())
			client := newFakeReattachClient()
			var stdout, stderr bytes.Buffer
			if code := runReattach([]string{"--cli", "codex", "--take-over"}, client, &stdout, &stderr); code != 1 || len(client.launches) != 0 || !strings.Contains(stderr.String(), selector) {
				t.Fatalf("exit=%d launches=%d stderr=%q", code, len(client.launches), stderr.String())
			}
			stdout.Reset()
			stderr.Reset()
			if code := runReattach([]string{"--cli", "codex", "--dry-run"}, client, &stdout, &stderr); code != 0 {
				t.Fatalf("inspection exit=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestCodexReattachAllowsDefaultHomeAlias(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")
	t.Setenv("HOME", filepath.Dir(home))
	t.Setenv("CODEX_HOME", home)
	writeCodexReattachFixture(t, home, "sessions/2026/11/17", testClaudeLiveID, t.TempDir())
	client := newFakeReattachClient()
	var stdout, stderr bytes.Buffer
	if code := runReattach([]string{"--cli", "codex", "--take-over"}, client, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}

func TestCodexReattachFindsPhysicalRolloutAndSkipsSubagents(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")
	cwd := t.TempDir()
	t.Setenv("HOME", filepath.Dir(home))
	t.Setenv("CODEX_HOME", "")
	path := writeCodexReattachFixture(t, home, "sessions/2026/11/17", testClaudeLiveID, cwd)
	if err := os.Rename(path, strings.TrimSuffix(path, ".jsonl")+"_"+testClaudeSettledID+".jsonl"); err != nil {
		t.Fatal(err)
	}
	sub := writeCodexReattachFixture(t, home, "sessions/2026/11/18", testClaudeSettledID, cwd)
	raw, err := os.ReadFile(sub)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"source":"cli"`), []byte(`"source":{"subagent":{"thread_spawn":{"parent_thread_id":"`+testClaudeLiveID+`"}}}`), 1)
	if err := os.WriteFile(sub, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []bool{false, true} {
		client := newFakeReattachClient()
		var stdout, stderr bytes.Buffer
		args := []string{"--cli", "codex", "--take-over"}
		if selected {
			args = append(args, "--session", testClaudeLiveID)
		}
		if code := runReattach(args, client, &stdout, &stderr); code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
		if len(client.launches) != 1 || client.launches[0].Options[protocol.OptionResumeConversationID] != testClaudeLiveID {
			t.Fatalf("launches=%#v", client.launches)
		}
	}
}

func TestCodexReattachRefusesArchivedBeforeLaunching(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")
	t.Setenv("HOME", filepath.Dir(home))
	t.Setenv("CODEX_HOME", "")
	writeCodexReattachFixture(t, home, "sessions/2026/11/17", testClaudeLiveID, t.TempDir())
	writeCodexReattachFixture(t, home, "archived_sessions", testClaudeSettledID, t.TempDir())
	for _, selected := range []bool{false, true} {
		client := newFakeReattachClient()
		var stdout, stderr bytes.Buffer
		args := []string{"--cli", "codex", "--all", "--take-over"}
		if selected {
			args = append(args, "--session", testClaudeSettledID)
		}
		if code := runReattach(args, client, &stdout, &stderr); code != 1 || len(client.launches) != 0 || !strings.Contains(stderr.String(), "codex unarchive "+testClaudeSettledID) {
			t.Fatalf("exit=%d launches=%d stderr=%q", code, len(client.launches), stderr.String())
		}
	}
}

func TestCodexReattachSkipsSubagentWithDeletedWorktree(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")
	t.Setenv("HOME", filepath.Dir(home))
	t.Setenv("CODEX_HOME", "")
	path := writeCodexReattachFixture(t, home, "sessions/2026/11/17", testClaudeLiveID, t.TempDir())
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	payload := meta["payload"].(map[string]any)
	payload["source"] = map[string]any{"subagent": "review"}
	payload["cwd"] = "/missing-codex-subagent-worktree"
	raw, err = json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	client := newFakeReattachClient()
	var stdout, stderr bytes.Buffer
	if code := runReattach([]string{"--cli", "codex", "--dry-run"}, client, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), testClaudeLiveID) {
		t.Fatal("subagent was eligible")
	}
}
