package main

// Failing-first contract for the first-class, CLI-only supervised handoff.
// `swarm handoff` is deliberately a wrapper over the existing LaunchReq and
// protected handoff-copy mechanics: no MCP server, skill, slash command, or new
// daemon operation is part of the path.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func handoffSource(id, cwd string, process status.Process) protocol.SessionView {
	return protocol.SessionView{
		EndpointID: "local",
		ID:         protocol.NamespacedID("local", id),
		Agent:      "codex",
		Name:       "source-work",
		Cwd:        cwd,
		Status: status.Status{
			Process:     process,
			Turn:        status.TurnIdle,
			Interaction: status.InteractionNone,
		},
	}
}

func writeHandoffContext(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "handoff.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunHandoff_BuildsLinkedLaunchFromCurrentSession(t *testing.T) {
	stateDir := useTempStateDir(t)
	sourceDir := t.TempDir()
	contextPath := writeHandoffContext(t, "# Goal\n\nFinish the review.\n")
	t.Setenv(hookclient.EnvSessionID, "source-1")

	c := newFakeSpawnClient()
	c.sessions = []protocol.SessionView{handoffSource("source-1", sourceDir, status.ProcessRunning)}
	var stdout, stderr bytes.Buffer
	exit := runHandoff([]string{
		"--cli", "claude",
		"--model", "opus",
		"--name", "yuanhui-review",
		"--context-file", contextPath,
	}, c, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("runHandoff exit = %d, want 0; stderr=%q", exit, stderr.String())
	}

	req := onlyLaunch(t, c)
	if req.Agent != "claude" || req.Options["model"] != "opus" {
		t.Errorf("target = %q model=%q, want claude/opus", req.Agent, req.Options["model"])
	}
	if req.Name != "yuanhui-review" {
		t.Errorf("Name = %q, want yuanhui-review", req.Name)
	}
	if req.Cwd != sourceDir {
		t.Errorf("Cwd = %q, want source session cwd %q", req.Cwd, sourceDir)
	}
	if req.SpawnedFrom != "source-1" || req.SpawnIntent != protocol.SpawnIntentHandoff {
		t.Errorf("lineage = (%q,%q), want (source-1,handoff)", req.SpawnedFrom, req.SpawnIntent)
	}
	if req.Worktree {
		t.Error("handoff unexpectedly requested a worktree")
	}
	if req.Cols != spawnCols || req.Rows != spawnRows {
		t.Errorf("grid = %dx%d, want %dx%d", req.Cols, req.Rows, spawnCols, spawnRows)
	}
	if !strings.Contains(req.InitialPrompt, "supervised Swarm handoff") ||
		!strings.Contains(req.InitialPrompt, "Read and follow the context in ") {
		t.Errorf("InitialPrompt = %q, want the short supervised child pointer", req.InitialPrompt)
	}
	if strings.Contains(req.InitialPrompt, "Finish the review") {
		t.Errorf("context body travelled in argv prompt: %q", req.InitialPrompt)
	}

	const wantID = "local/new1\n"
	if stdout.String() != wantID {
		t.Errorf("stdout = %q, want child id alone %q", stdout.String(), wantID)
	}
	if !strings.Contains(stderr.String(), "handed off") || !strings.Contains(stderr.String(), "local/new1") {
		t.Errorf("stderr = %q, want human confirmation naming child", stderr.String())
	}

	entries, err := os.ReadDir(filepath.Join(stateDir, "handoffs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("protected handoff copies = %d, want 1", len(entries))
	}
	copyPath := filepath.Join(stateDir, "handoffs", entries[0].Name())
	info, err := os.Stat(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("context copy mode = %04o, want 0600", got)
	}
	got, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# Goal\n\nFinish the review.\n" {
		t.Errorf("context copy = %q, want source verbatim", string(got))
	}
}

func TestRunHandoff_DefaultsOptionalTargetFields(t *testing.T) {
	useTempStateDir(t)
	sourceDir := t.TempDir()
	contextPath := writeHandoffContext(t, "context\n")
	t.Setenv(hookclient.EnvSessionID, "source-1")

	c := newFakeSpawnClient()
	c.sessions = []protocol.SessionView{handoffSource("source-1", sourceDir, status.ProcessRunning)}
	if exit := runHandoff([]string{"--cli", "claude", "--context-file", contextPath}, c, io.Discard, io.Discard); exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	req := onlyLaunch(t, c)
	if req.Name != "" {
		t.Errorf("Name = %q without --name, want daemon default", req.Name)
	}
	if len(req.Options) != 0 {
		t.Errorf("Options = %v without --model, want empty", req.Options)
	}
}

func TestRunHandoff_RequiresLiveManagedSource(t *testing.T) {
	contextPath := writeHandoffContext(t, "context\n")
	cases := []struct {
		name     string
		env      string
		sessions []protocol.SessionView
		want     string
	}{
		{name: "outside swarm", want: hookclient.EnvSessionID},
		{name: "source missing", env: "missing", want: "missing"},
		{name: "source completed", env: "source-1", sessions: []protocol.SessionView{handoffSource("source-1", t.TempDir(), status.ProcessExited)}, want: "running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempStateDir(t)
			t.Setenv(hookclient.EnvSessionID, tc.env)
			c := newFakeSpawnClient()
			c.sessions = tc.sessions
			var stderr bytes.Buffer
			exit := runHandoff([]string{"--cli", "claude", "--context-file", contextPath}, c, io.Discard, &stderr)
			if exit != 1 {
				t.Fatalf("exit = %d, want 1; stderr=%q", exit, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.want)
			}
			if got := len(c.reqs()); got != 0 {
				t.Errorf("Launch called %d times after source refusal, want 0", got)
			}
		})
	}
}

func TestRunHandoff_FlagAndContextRefusals(t *testing.T) {
	goodContext := writeHandoffContext(t, "context\n")
	missingContext := filepath.Join(t.TempDir(), "missing.md")
	t.Setenv(hookclient.EnvSessionID, "source-1")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing cli", args: []string{"--context-file", goodContext}, want: "--cli"},
		{name: "missing context", args: []string{"--cli", "claude"}, want: "--context-file"},
		{name: "unexpected arg", args: []string{"--cli", "claude", "--context-file", goodContext, "extra"}, want: "unexpected"},
		{name: "unknown flag", args: []string{"--bogus"}, want: "flag"},
		{name: "unreadable context", args: []string{"--cli", "claude", "--context-file", missingContext}, want: missingContext},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempStateDir(t)
			c := newFakeSpawnClient()
			c.sessions = []protocol.SessionView{handoffSource("source-1", t.TempDir(), status.ProcessRunning)}
			var stderr bytes.Buffer
			exit := runHandoff(tc.args, c, io.Discard, &stderr)
			wantExit := misuseExit
			if tc.name == "unreadable context" {
				wantExit = 1
			}
			if exit != wantExit {
				t.Fatalf("exit = %d, want %d; stderr=%q", exit, wantExit, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.want)
			}
			if got := len(c.reqs()); got != 0 {
				t.Errorf("Launch called %d times after refusal, want 0", got)
			}
		})
	}
}

func TestRunHandoff_HelpUsesTheFirstClassCommandContract(t *testing.T) {
	c := newFakeSpawnClient()
	var stderr bytes.Buffer
	if exit := runHandoff([]string{"--help"}, c, io.Discard, &stderr); exit != 0 {
		t.Fatalf("--help exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	for _, want := range []string{"usage: swarm handoff", "--cli", "--context-file", "swarm watch", "swarm peek", "swarm send"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("help missing %q:\n%s", want, stderr.String())
		}
	}
	if got := len(c.reqs()); got != 0 {
		t.Errorf("help launched %d sessions, want 0", got)
	}
}

func TestRunHandoff_LaunchFailureCleansProtectedCopy(t *testing.T) {
	stateDir := useTempStateDir(t)
	contextPath := writeHandoffContext(t, "context\n")
	t.Setenv(hookclient.EnvSessionID, "source-1")
	c := newFakeSpawnClient()
	c.sessions = []protocol.SessionView{handoffSource("source-1", t.TempDir(), status.ProcessRunning)}
	c.err = errFakeDaemon

	var stderr bytes.Buffer
	if exit := runHandoff([]string{"--cli", "claude", "--context-file", contextPath}, c, io.Discard, &stderr); exit != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%q", exit, stderr.String())
	}
	left, err := os.ReadDir(filepath.Join(stateDir, "handoffs"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("handoffs dir holds %d orphaned copies, want 0", len(left))
	}
}

func TestUsage_ListsFirstClassHandoffWithoutSkills(t *testing.T) {
	if !strings.Contains(usage, "swarm handoff") {
		t.Fatalf("usage does not list swarm handoff:\n%s", usage)
	}
	if strings.Contains(usage, "swarm agents") || strings.Contains(usage, "/swarm-handoff") {
		t.Fatalf("usage still advertises the retired skill/slash installer:\n%s", usage)
	}
}

// ---------------------------------------------------------------------------
// ADR-010 Amendment 3 C1: `swarm handoff --supervision passive|manual|none`.
// The mode is validated locally against the closed vocabulary, defaults to
// passive, and travels in LaunchReq.Supervision beside the handoff intent.
// ---------------------------------------------------------------------------

func TestRunHandoff_SupervisionModeTravelsInLaunchReq(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "default is passive", args: nil, want: protocol.SupervisionPassive},
		{name: "passive", args: []string{"--supervision", "passive"}, want: protocol.SupervisionPassive},
		{name: "manual", args: []string{"--supervision", "manual"}, want: protocol.SupervisionManual},
		{name: "none", args: []string{"--supervision", "none"}, want: protocol.SupervisionNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempStateDir(t)
			contextPath := writeHandoffContext(t, "context\n")
			t.Setenv(hookclient.EnvSessionID, "source-1")
			c := newFakeSpawnClient()
			c.sessions = []protocol.SessionView{handoffSource("source-1", t.TempDir(), status.ProcessRunning)}
			args := append([]string{"--cli", "claude", "--context-file", contextPath}, tc.args...)
			var stderr bytes.Buffer
			if exit := runHandoff(args, c, io.Discard, &stderr); exit != 0 {
				t.Fatalf("exit = %d, want 0; stderr=%q", exit, stderr.String())
			}
			req := onlyLaunch(t, c)
			if req.Supervision != tc.want {
				t.Errorf("LaunchReq.Supervision = %q, want %q", req.Supervision, tc.want)
			}
			if req.SpawnIntent != protocol.SpawnIntentHandoff {
				t.Errorf("SpawnIntent = %q, want handoff (the daemon admits supervision only with it)", req.SpawnIntent)
			}
		})
	}
}

func TestRunHandoff_RefusesUnknownSupervisionMode(t *testing.T) {
	contextPath := writeHandoffContext(t, "context\n")
	t.Setenv(hookclient.EnvSessionID, "source-1")
	for _, mode := range []string{"eager", "watch", ""} {
		t.Run(mode, func(t *testing.T) {
			useTempStateDir(t)
			c := newFakeSpawnClient()
			c.sessions = []protocol.SessionView{handoffSource("source-1", t.TempDir(), status.ProcessRunning)}
			var stderr bytes.Buffer
			exit := runHandoff([]string{"--cli", "claude", "--context-file", contextPath, "--supervision", mode}, c, io.Discard, &stderr)
			if exit != misuseExit {
				t.Fatalf("exit = %d, want %d (misuse); stderr=%q", exit, misuseExit, stderr.String())
			}
			if !strings.Contains(stderr.String(), "--supervision") {
				t.Errorf("stderr = %q, want it to name --supervision", stderr.String())
			}
			if got := len(c.reqs()); got != 0 {
				t.Errorf("Launch called %d times after an unknown supervision mode, want 0", got)
			}
		})
	}
}

func TestRunHandoff_UsageNamesSupervisionModes(t *testing.T) {
	const want = "[--supervision passive|manual|none]"
	c := newFakeSpawnClient()
	var stderr bytes.Buffer
	if exit := runHandoff([]string{"--help"}, c, io.Discard, &stderr); exit != 0 {
		t.Fatalf("--help exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("handoff help missing %q:\n%s", want, stderr.String())
	}
	if !strings.Contains(usage, want) {
		t.Errorf("top-level usage missing %q:\n%s", want, usage)
	}
}
