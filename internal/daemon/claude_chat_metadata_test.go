package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func claudeChatDaemonFixture(t *testing.T) (*Daemon, persist.Meta) {
	t.Helper()
	d, m := retainedClaudeDaemonFixture(t, nil)
	if _, err := d.store.Load(m.ID); err != nil {
		t.Fatalf("ordinary owned fixture invalid: %v", err)
	}
	// Metadata-only retained reservation; no native process or grant is claimed.
	m.SchemaVersion, m.RequiredRuntime = 6, "claude-chat"
	m.Status.Process = status.ProcessRunning
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.cfg.StateDir, m.ID, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	d.putMem(m)
	return d, m
}

func assertClaudeChatDaemonSix(t *testing.T, d *Daemon, id string) persist.Meta {
	t.Helper()
	memory, ok := d.Get(id)
	disk, err := d.store.Load(id)
	if !ok || err != nil || memory.SchemaVersion != 6 || disk.SchemaVersion != 6 || memory.RequiredRuntime != "claude-chat" || disk.RequiredRuntime != "claude-chat" {
		t.Fatalf("chat version diverged/downgraded: memory=%d disk=%d found=%t err=%v", memory.SchemaVersion, disk.SchemaVersion, ok, err)
	}
	return memory
}

func TestClaudeChatDaemonRealWritersKeepMemoryAndDiskSix(t *testing.T) {
	for _, operation := range []string{"save", "locked-save", "status", "rename", "tag", "hidden", "conversation", "exit", "reconcile-lost"} {
		t.Run(operation, func(t *testing.T) {
			if operation == "hidden" {
				ordinary, ended := retainedClaudeDaemonFixture(t, nil)
				for _, hidden := range []bool{true, false} {
					if err := ordinary.SetRosterHidden(ended.ID, hidden); err != nil {
						t.Fatalf("ordinary5 ended visibility precondition: %v", err)
					}
					memory, ok := ordinary.Get(ended.ID)
					disk, err := ordinary.store.Load(ended.ID)
					if !ok || err != nil || memory.SchemaVersion != 5 || disk.SchemaVersion != 5 || memory.RosterHidden != hidden || disk.RosterHidden != hidden {
						t.Fatalf("ordinary5 ended visibility did not persist %t: memory=%+v disk=%+v err=%v", hidden, memory, disk, err)
					}
				}
			}
			d, m := claudeChatDaemonFixture(t)
			if operation == "hidden" {
				// Exercise the supported ended-attempt transition, not live hiding.
				m.Status.Process = status.ProcessExited
				raw, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(d.cfg.StateDir, m.ID, "meta.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Model the common writer's incoming baseline reset, not a disk downgrade.
			m.SchemaVersion = 1
			d.putMem(m)
			var err error
			switch operation {
			case "save":
				m.Name = "chat save"
				err = d.saveMeta(m)
			case "locked-save":
				m.Tag = "locked"
				d.writeMu.Lock()
				_, err = d.saveMetaLocked(&m)
				d.writeMu.Unlock()
			case "status":
				next := m.Status
				next.Turn = status.TurnActive
				err = d.SetStatus(m.ID, next)
			case "rename":
				err = d.RenameAt(m.ID, "renamed chat", time.Now())
			case "tag":
				err = d.SetTag(m.ID, "chat-tag")
			case "hidden":
				err = d.SetRosterHidden(m.ID, true)
			case "conversation":
				err = d.SetConversationID(m.ID, "4a7a2465-d8f0-4c05-a7a9-c44d8077b22b")
			case "exit":
				d.finalizeTerminal(m.ID, func(cur persist.Meta) persist.Meta { cur.Status.Process = status.ProcessExited; return cur })
			case "reconcile-lost":
				d.reconcileRunning(m)
			}
			if err != nil {
				t.Fatalf("real %s writer refused managed chat update: %v", operation, err)
			}
			got := assertClaudeChatDaemonSix(t, d, m.ID)
			switch operation {
			case "status":
				if got.Status.Turn != status.TurnActive {
					t.Fatal("status writer did not apply")
				}
			case "rename":
				if got.Name != "renamed chat" {
					t.Fatal("rename writer did not apply")
				}
			case "tag":
				if got.Tag != "chat-tag" {
					t.Fatal("tag writer did not apply")
				}
			case "hidden":
				if !got.RosterHidden {
					t.Fatal("hidden writer did not apply")
				}
				if err := d.SetRosterHidden(m.ID, false); err != nil {
					t.Fatalf("ended chat unhide refused: %v", err)
				}
				if unhidden := assertClaudeChatDaemonSix(t, d, m.ID); unhidden.RosterHidden {
					t.Fatal("unhide writer did not apply")
				}
			case "conversation":
				if got.ConversationID == "" {
					t.Fatal("conversation writer did not apply")
				}
			case "exit":
				if got.Status.Process != status.ProcessExited {
					t.Fatal("exit writer did not apply")
				}
			case "reconcile-lost":
				if got.Status.Process != status.ProcessLost {
					t.Fatal("reconcile writer did not apply")
				}
			}
		})
	}
}

func TestClaudeChatDaemonImmutableIdentityRefusesWithoutDowngrade(t *testing.T) {
	d, m := claudeChatDaemonFixture(t)
	path := filepath.Join(d.cfg.StateDir, m.ID, "meta.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ClearCLIIdentity(m.ID); err == nil {
		t.Fatal("owned chat lost its required immutable CLI identity")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected identity clear changed disk: %v", err)
	}
	got, ok := d.Get(m.ID)
	if !ok || got.SchemaVersion != 6 || got.CLIIdentity == nil || *got.CLIIdentity != *m.CLIIdentity {
		t.Fatal("identity refusal changed memory")
	}
}

func TestClaudeChatDaemonRetirementAndRetryKeepSix(t *testing.T) {
	d, m := claudeChatDaemonFixture(t)
	retired, err := d.retireClaudeReservation(m.ID, func() error { return nil })
	if err != nil || retired.SchemaVersion != 6 || retired.ClaudeRetirement == nil {
		t.Fatalf("chat retirement: %+v %v", retired, err)
	}
	assertClaudeChatDaemonSix(t, d, m.ID)
	again, err := d.retireClaudeReservation(m.ID, nil)
	if err != nil || again.SchemaVersion != 6 || again.ClaudeRetirement == nil || *again.ClaudeRetirement != *retired.ClaudeRetirement {
		t.Fatalf("chat retirement retry: %+v %v", again, err)
	}
	assertClaudeChatDaemonSix(t, d, m.ID)
}

func TestClaudeChatDaemonOrdinaryFiveWriterControl(t *testing.T) {
	d, m := retainedClaudeDaemonFixture(t, nil)
	m.SchemaVersion, m.Name = 1, "ordinary owned"
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	got, ok := d.Get(m.ID)
	disk, err := d.store.Load(m.ID)
	if !ok || err != nil || got.SchemaVersion != 5 || disk.SchemaVersion != 5 || got.RequiredRuntime != "" {
		t.Fatalf("ordinary5 changed: memory=%d disk=%d err=%v", got.SchemaVersion, disk.SchemaVersion, err)
	}
}

func TestClaudeChatDaemonMixedOpenCodeSchemaRefusesBeforePublication(t *testing.T) {
	cfg := daemonConfig(t)
	publications := 0
	cfg.onMetaSave = func(persist.Meta) { publications++ }
	d := openDaemon(t, cfg)
	ordinary := openCodeMeta()
	if err := d.saveMeta(ordinary); err != nil {
		t.Fatalf("valid OpenCode precondition refused: %v", err)
	}
	control, ok := d.Get(ordinary.ID)
	disk, err := d.store.Load(ordinary.ID)
	if !ok || err != nil || control.SchemaVersion != persist.OpenCodeSchemaVersion || disk.SchemaVersion != persist.OpenCodeSchemaVersion || publications != 1 {
		t.Fatalf("valid OpenCode precondition not published: memory=%+v disk=%+v publications=%d err=%v", control, disk, publications, err)
	}
	publications = 0
	mixed := openCodeMeta()
	mixed.ID, mixed.SchemaVersion = "mixed-chat-opencode", 6
	if err := d.saveMeta(mixed); err == nil {
		t.Error("schema6 with otherwise valid OpenCode shape was normalized into an ordinary OpenCode row")
	}
	if got, ok := d.Get(mixed.ID); ok {
		t.Errorf("refused mixed runtime published memory: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, mixed.ID, "meta.json")); !os.IsNotExist(err) {
		t.Errorf("refused mixed runtime published disk: %v", err)
	}
	if publications != 0 {
		t.Errorf("refused mixed runtime notified publication %d times", publications)
	}
}
