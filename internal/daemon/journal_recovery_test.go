package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func latchJournalRotationFailure(t *testing.T, d *Daemon, journalDir string) {
	t.Helper()
	if err := os.Chmod(journalDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(journalDir, 0o700) })

	// Force active segment past journalMaxBytes (64 KiB) so rotateLocked fails with EACCES
	// and latches storageErr on d.journal.
	bigName := strings.Repeat("x", 4096)
	var failed bool
	for i := range 64 {
		id := "fill-" + string(rune('a'+i%26)) + "-" + time.Now().Format("150405.000000")
		err := d.saveMeta(persist.Meta{
			ID:        persistIDFromIndex(i),
			AgentType: "claude",
			Name:      bigName,
			Cwd:       t.TempDir(),
			Status:    status.Status{Process: status.ProcessExited},
		})
		_ = id
		if err != nil {
			failed = true
			break
		}
	}
	if !failed {
		t.Fatal("expected journal rotation failure while journal directory is 0500")
	}
}

func persistIDFromIndex(i int) string {
	const digits = "abcdefghijklmnop"
	out := make([]byte, 16)
	for pos := range out {
		out[pos] = digits[(i>>(pos*2))&0xf]
	}
	return string(out)
}

func TestDaemonRecoversJournalAfterTransientStorageFailure(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	journalDir := filepath.Join(cfg.StateDir, "journal")

	latchJournalRotationFailure(t, d, journalDir)

	// While journal storage is still unwritable, launches (and handoffs) fail closed.
	script := writeRecoveryScript(t, "idle 60s\n")
	if _, err := d.Launch(LaunchSpec{
		AgentType:   "claude",
		Argv:        []string{fakeAgentBin, script},
		Cwd:         t.TempDir(),
		Cols:        80,
		Rows:        24,
		SpawnedFrom: "aaaaaaaaaaaaaaa0",
		SpawnIntent: "handoff",
	}); err == nil {
		t.Fatal("Launch succeeded while journal directory remained unwritable")
	}

	// Restore storage without restarting the daemon: Launch/handoff, SetStatus,
	// RecordInteraction, and Delete must automatically recover the journal and succeed.
	if err := os.Chmod(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}

	launched, err := d.Launch(LaunchSpec{
		AgentType:   "claude",
		Argv:        []string{fakeAgentBin, script},
		Cwd:         t.TempDir(),
		Cols:        80,
		Rows:        24,
		SpawnedFrom: "aaaaaaaaaaaaaaa0",
		SpawnIntent: "handoff",
	})
	if err != nil {
		t.Fatalf("handoff Launch after storage recovery failed: %v", err)
	}
	t.Cleanup(func() { _ = d.Kill(launched.ID) })

	if err := d.SetStatus(launched.ID, status.Status{
		Process:     status.ProcessRunning,
		Turn:        status.TurnIdle,
		Interaction: status.InteractionPermission,
	}); err != nil {
		t.Fatalf("SetStatus after storage recovery failed: %v", err)
	}

	if err := d.RecordInteraction(launched.ID, InteractionItem{
		V:      InteractionSchemaVersion,
		ItemID: "post-recovery-msg",
		Kind:   "agent_message",
		Status: "completed",
		Fields: json.RawMessage(`{"text":"recovered"}`),
	}); err != nil {
		t.Fatalf("RecordInteraction after storage recovery failed: %v", err)
	}

	if err := d.Kill(launched.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	waitProcessNotRunning(t, d, launched.ID)
	if err := d.Delete(launched.ID); err != nil {
		t.Fatalf("Delete after storage recovery failed: %v", err)
	}
}

func TestMonitorRetriesTerminalFinalizeAfterStorageFailure(t *testing.T) {
	cfg := daemonConfig(t)
	var ended atomic.Int32
	cfg.OnSessionEnd = func(string) { ended.Add(1) }
	d := openDaemon(t, cfg)
	journalDir := filepath.Join(cfg.StateDir, "journal")

	script := writeRecoveryScript(t, "idle 60s\n")
	m, err := d.Launch(LaunchSpec{
		AgentType: "claude",
		Argv:      []string{fakeAgentBin, script},
		Cwd:       t.TempDir(),
		Cols:      80,
		Rows:      24,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	latchJournalRotationFailure(t, d, journalDir)

	if err := d.Kill(m.ID); err != nil {
		t.Fatalf("Kill during storage failure: %v", err)
	}
	waitLogContains(t, cfg.LogPath, "finalize: persist terminal meta for "+m.ID, pollTimeout)

	// Restore journal storage: superviseLaunched must retry handleShimExit automatically
	// instead of stranding the dead session as running.
	if err := os.Chmod(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}

	waitProcessNotRunning(t, d, m.ID)
	cur, ok := d.Get(m.ID)
	if !ok || cur.Status.Process != status.ProcessExited {
		t.Fatalf("session after storage recovery = (%+v, %v), want exited", cur.Status, ok)
	}
	if ended.Load() == 0 {
		t.Fatal("OnSessionEnd never fired")
	}
}

func waitProcessNotRunning(t *testing.T, d *Daemon, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m, ok := d.Get(id); ok && m.Status.Process != status.ProcessRunning {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	m, _ := d.Get(id)
	t.Fatalf("session %s stuck in ProcessRunning (%+v)", id, m.Status)
}

func writeRecoveryScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.script")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
