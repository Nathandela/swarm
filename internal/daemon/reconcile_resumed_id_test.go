package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

// TestReconcile_BackfillsResumedCodexConversationID pins ADR-010 Amendment 7 H1: a codex
// session launched as `codex resume <id>` before v0.13.16 seeded ids continues the thread
// named in its launch record and nowhere else, so reconcile latches that id, once, through
// the write-once SetConversationID and persists it, on every reconcile branch that needs no
// live process (terminal, lost, exit side-file). Everything else is left alone: a captured
// id, a fresh launch, a claude session (its id is hook-captured and authoritative), an argv
// that names no id, and a session whose launch record is gone.
func TestReconcile_BackfillsResumedCodexConversationID(t *testing.T) {
	const resumed = "01a056e8-6352-7901-9290-e8a09b37dc2e"
	const captured = "01a05c5a-5d67-7a12-acac-ca46996ca363"
	resumeArgv := []string{"/usr/bin/codex", "resume", resumed, "-c", "k=v"}
	cases := []struct {
		name, agent, resumedFrom, existing string
		argv                               []string
		running, exited                    bool // persisted as running; exit side-file present
		want                               string
	}{
		{name: "terminal codex resume without an id", agent: "codex", resumedFrom: "h6nwx67bov4iwhpy", argv: resumeArgv, want: resumed},
		{name: "lost shim", agent: "codex", resumedFrom: "h6nwx67bov4iwhpy", argv: resumeArgv, running: true, want: resumed},
		{name: "exit side-file", agent: "codex", resumedFrom: "h6nwx67bov4iwhpy", argv: resumeArgv, running: true, exited: true, want: resumed},
		{name: "captured id is kept", agent: "codex", resumedFrom: "h6nwx67bov4iwhpy", existing: captured, argv: resumeArgv, want: captured},
		{name: "fresh launch is not a resume", agent: "codex", argv: []string{"/usr/bin/codex", "--model", "gpt-5.6-sol"}},
		{name: "claude is hook-captured", agent: "claude", resumedFrom: "abcdefghijklmnop", argv: []string{"/usr/bin/claude", "--resume", resumed}},
		{name: "argv names no id", agent: "codex", resumedFrom: "h6nwx67bov4iwhpy", argv: []string{"/usr/bin/codex", "resume", "--last"}},
		{name: "launch record missing", agent: "codex", resumedFrom: "h6nwx67bov4iwhpy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := daemonConfig(t)
			store, err := persist.NewStore(cfg.StateDir)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			m := persist.Meta{
				ID:             "legacy-resume",
				AgentType:      tc.agent,
				Cwd:            "/work",
				CreatedAt:      time.Now().Add(-time.Hour),
				Status:         status.Status{Process: status.ProcessExited},
				ResumedFrom:    tc.resumedFrom,
				ConversationID: tc.existing,
			}
			if tc.running {
				m.Status.Process = status.ProcessRunning // ShimPID 0: nothing to reconnect
			}
			if err := store.Save(m); err != nil {
				t.Fatalf("seed meta: %v", err)
			}
			dir := filepath.Join(cfg.StateDir, m.ID)
			if tc.argv != nil {
				data, _ := json.Marshal(map[string]any{"argv": tc.argv})
				if err := os.WriteFile(filepath.Join(dir, shimLaunchConfigFile), data, 0o600); err != nil {
					t.Fatalf("seed launch record: %v", err)
				}
			}
			if tc.exited {
				writeJSON(t, filepath.Join(dir, shim.ExitFile), shim.ExitInfo{ExitCode: 0, FinishedAt: time.Now()})
			}
			d := openDaemon(t, cfg)
			got, ok := d.Get(m.ID)
			if !ok {
				t.Fatalf("session %s not reconciled", m.ID)
			}
			if got.ConversationID != tc.want {
				t.Fatalf("conversation id after reconcile = %q, want %q", got.ConversationID, tc.want)
			}
			onDisk, err := store.Load(m.ID)
			if err != nil {
				t.Fatalf("reload meta: %v", err)
			}
			if onDisk.ConversationID != tc.want {
				t.Fatalf("persisted conversation id = %q, want %q", onDisk.ConversationID, tc.want)
			}
		})
	}
}

// TestReconcile_BackfillsResumedCodexConversationIDForALiveShim is H1 on the branch the
// amendment was written for: the legacy session's shim is still alive across the daemon
// restart, so reconcile reconnects it (E5.2/L2) and only then latches the id.
func TestReconcile_BackfillsResumedCodexConversationIDForALiveShim(t *testing.T) {
	const resumed = "01a056e8-6352-7901-9290-e8a09b37dc2e"
	cfg := daemonConfig(t)
	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	spec.AgentType = "codex"
	spec.ResumedFrom = "src"
	spec.Argv = append(spec.Argv, resumed) // the announce agent ignores argv beyond its pidfile
	m, err := d1.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if before, _ := d1.Get(m.ID); before.ConversationID != "" {
		t.Fatalf("launch latched %q; the legacy shape has no id", before.ConversationID)
	}
	d1.abandon()

	d2 := openDaemon(t, cfg)
	got := waitStatus(t, d2, m.ID, status.ProcessRunning, pollTimeout)
	if got.ConversationID != resumed {
		t.Fatalf("conversation id after reconnect = %q, want %q", got.ConversationID, resumed)
	}
}
