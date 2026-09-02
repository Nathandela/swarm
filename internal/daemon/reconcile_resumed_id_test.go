package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// TestReconcile_BackfillsResumedCodexConversationID pins ADR-010 Amendment 7 H1: a codex
// session launched as `codex resume <id>` before v0.13.16 seeded ids continues the thread
// named in its launch record and nowhere else, so reconcile latches that id, once, through
// the write-once SetConversationID and persists it. Everything else is left alone: a captured
// id, a fresh launch, a claude session (its id is hook-captured and authoritative), an argv
// that names no id, and a session whose launch record is gone.
func TestReconcile_BackfillsResumedCodexConversationID(t *testing.T) {
	const resumed = "01a056e8-6352-7901-9290-e8a09b37dc2e"
	const captured = "01a05c5a-5d67-7a12-acac-ca46996ca363"
	cases := []struct {
		name, agent, resumedFrom, existing string
		argv                               []string
		want                               string
	}{
		{"codex resume without an id", "codex", "h6nwx67bov4iwhpy", "", []string{"/usr/bin/codex", "resume", resumed, "-c", "k=v"}, resumed},
		{"captured id is kept", "codex", "h6nwx67bov4iwhpy", captured, []string{"/usr/bin/codex", "resume", resumed}, captured},
		{"fresh launch is not a resume", "codex", "", "", []string{"/usr/bin/codex", "--model", "gpt-5.6-sol"}, ""},
		{"claude is hook-captured", "claude", "abcdefghijklmnop", "", []string{"/usr/bin/claude", "--resume", resumed}, ""},
		{"argv names no id", "codex", "h6nwx67bov4iwhpy", "", []string{"/usr/bin/codex", "resume", "--last"}, ""},
		{"launch record missing", "codex", "h6nwx67bov4iwhpy", "", nil, ""},
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
			if err := store.Save(m); err != nil {
				t.Fatalf("seed meta: %v", err)
			}
			if tc.argv != nil {
				data, _ := json.Marshal(map[string]any{"argv": tc.argv})
				if err := os.WriteFile(filepath.Join(cfg.StateDir, m.ID, shimLaunchConfigFile), data, 0o600); err != nil {
					t.Fatalf("seed launch record: %v", err)
				}
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
