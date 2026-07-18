package daemon

// FAILING-FIRST daemon-integration tests for the durable journal (plan R-JRN,
// ADR-007 D6, amendment D.0-A7). They prove the journal is written at the
// saveMetaLocked CHOKE POINT — covering SetStatus, finalizeTerminal, launch, the
// reconcile.go restart transitions, AND the Delete tombstone path — never a named
// caller list. RED is undefined-only: `go test ./internal/daemon/` fails to compile
// because these production seams do not exist yet:
//
//	func (d *Daemon) JournalReadFrom(from uint64) (journal.Resume, error) // reads the daemon-wide journal (also backs R-PROT.3); Resume.Events each carry SessionID, Type, Group, Cursor
//	func (d *Daemon) RecordGatewayPresence(online bool) error             // gateway connect/disconnect appends a `presence` record (R-JRN.7)
//
// The journal itself lives under <cfg.StateDir>/journal/ (ONE daemon-wide journal,
// R-JRN.7). These tests reach it ONLY through d.JournalReadFrom so they never import
// the not-yet-built journal package (keeping the RED a clean single undefined seam).
//
// SEAM the source does not yet provide (flagged for the implementer): daemon.go's
// saveMetaLocked has NO journal hook and no crash-injection boundary between the
// meta write and a journal append; reconcile.go/lifecycle.go call saveMeta/Delete
// with no journal awareness. A7 requires hooking the choke point + a separate Delete
// hook, WAL-consistent with the meta write.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// journalRecord is the minimal shape each test asserts on. It mirrors the fields
// JournalReadFrom's Resume.Events expose; the loop below binds them by inference so
// this file never names the journal package.
type journalPred func(sessionID, recordType string, group status.Group) bool

// waitJournal polls d.JournalReadFrom(0) until pred matches a record or timeout.
func waitJournal(t *testing.T, d *Daemon, timeout time.Duration, pred journalPred) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, err := d.JournalReadFrom(0)
		if err != nil {
			t.Fatalf("JournalReadFrom(0): %v", err)
		}
		for _, ev := range res.Events {
			if pred(ev.SessionID, string(ev.Type), ev.Group) {
				return
			}
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("no journal record matched within %s", timeout)
}

// TestDaemon_StatusChangeAppendsGroupTransition (R-JRN.2): a status change that
// crosses a Group boundary appends exactly one group_transition record with the
// server-derived Group — never derived on the phone.
func TestDaemon_StatusChangeAppendsGroupTransition(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	m, _ := launchAnnounce(t, d)

	// Launch lands at Working (Turn unknown). Drive it to NeedsInput (idle +
	// permission), which crosses status.Derive's Group boundary.
	if err := d.SetStatus(m.ID, status.Status{Turn: status.TurnIdle, Interaction: status.InteractionPermission}); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	waitJournal(t, d, pollTimeout, func(sess, typ string, g status.Group) bool {
		return sess == m.ID && typ == "group_transition" && g == status.GroupNeedsInput
	})
}

// TestDaemon_LifecycleAppendsRecords (R-JRN.2): a fresh launch appends a `launched`
// record and a shim exit appends an `exited` record — the lifecycle transitions the
// choke point owns.
func TestDaemon_LifecycleAppendsRecords(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	m, agentPID := launchAnnounce(t, d)

	waitJournal(t, d, pollTimeout, func(sess, typ string, _ status.Group) bool {
		return sess == m.ID && typ == "launched"
	})

	if err := d.Kill(m.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	waitProcessGone(t, agentPID, pollTimeout)
	waitStatus(t, d, m.ID, status.ProcessExited, pollTimeout)

	waitJournal(t, d, pollTimeout, func(sess, typ string, _ status.Group) bool {
		return sess == m.ID && typ == "exited"
	})
}

// TestJournal_ReconcileRestartTransitionRecorded (R-JRN.2/A7) is the choke-point
// proof: a daemon-restart Lost transition that flows through reconcile.go's saveMeta
// (NOT SetStatus, NOT a named caller) IS journaled. A running meta whose recorded
// PID was reaped reconciles to lost on Open, and the journal records it.
func TestJournal_ReconcileRestartTransitionRecorded(t *testing.T) {
	cfg := daemonConfig(t)
	id := "reaped-jrnl"

	// A reaped PID: the meta says running, but the process is gone (mirrors
	// TestReconcile_LostOnReapedPID).
	child := exec.Command(selfExe(t), markerCatchTerm, filepath.Join(t.TempDir(), "unused"))
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := child.Process.Pid
	start, err := processStartTime(pid)
	if err != nil {
		t.Fatalf("processStartTime: %v", err)
	}
	_ = child.Process.Kill()
	_, _ = child.Process.Wait()
	writeRunningMeta(t, cfg.StateDir, id, pid, start)

	d := openDaemon(t, cfg)
	waitStatus(t, d, id, status.ProcessLost, pollTimeout)

	// The reconcile-driven lost transition must be in the journal — proving the hook
	// is on saveMetaLocked (which reconcile.go calls), not on SetStatus/finalizeTerminal.
	waitJournal(t, d, pollTimeout, func(sess, typ string, _ status.Group) bool {
		return sess == id && typ == "lost"
	})
}

// TestJournal_DeletedRecordOutlivesSessionDir (R-JRN.7): the `deleted` record must
// outlive the session directory that Store.Delete removes. The journal is ONE
// daemon-wide log under <stateDir>/journal, distinct from the per-session dir, so a
// deleted session's tombstone remains readable after its dir is gone.
func TestJournal_DeletedRecordOutlivesSessionDir(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m, _ := launchAnnounce(t, d)

	if err := d.Delete(m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// The session directory is gone.
	if _, err := os.Stat(filepath.Join(cfg.StateDir, m.ID)); !os.IsNotExist(err) {
		t.Fatalf("session dir still present after Delete: %v", err)
	}
	// The daemon-wide journal still holds the `deleted` tombstone.
	waitJournal(t, d, pollTimeout, func(sess, typ string, _ status.Group) bool {
		return sess == m.ID && typ == "deleted"
	})
}

// TestDaemon_GatewayPresenceRecorded (R-JRN.7): gateway connect/disconnect appends a
// `presence` record (a daemon-side liveness proxy; true online/asleep/offline is
// relay-derived). The append seam is R-JRN.7; the connect/disconnect TRIGGER is
// wired by the remote-tier gateway slice (R-GW.8) — see the test-writer report.
func TestDaemon_GatewayPresenceRecorded(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	if err := d.RecordGatewayPresence(true); err != nil {
		t.Fatalf("RecordGatewayPresence(true): %v", err)
	}
	if err := d.RecordGatewayPresence(false); err != nil {
		t.Fatalf("RecordGatewayPresence(false): %v", err)
	}
	waitJournal(t, d, pollTimeout, func(_, typ string, _ status.Group) bool {
		return typ == "presence"
	})
}

// TestJournal_MetaAndJournalConsistentAcrossCrash (R-JRN.5/A7): the journal append
// is a WAL-style step in the same recoverable commit as the meta write, so a crash
// leaves neither meta-without-journal nor journal-without-meta. Model a kill -9 with
// abandon(), reopen, and assert every session the reopened registry still holds has
// at least one journal record (its lifecycle is not lost from the journal).
func TestJournal_MetaAndJournalConsistentAcrossCrash(t *testing.T) {
	const n = 2
	cfg := daemonConfig(t)
	d1, err := Open(cfg)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		m, _ := launchAnnounce(t, d1)
		ids = append(ids, m.ID)
	}
	// Each launch must already be journaled before the crash.
	for _, id := range ids {
		waitJournal(t, d1, pollTimeout, func(sess, typ string, _ status.Group) bool {
			return sess == id && typ == "launched"
		})
	}

	d1.abandon() // kill -9 model: no clean shutdown, no shim signalling

	d2 := openDaemon(t, cfg)
	// Every session in the reopened meta registry must have a journal record: no
	// meta-without-journal survived the crash.
	res, err := d2.JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom after restart: %v", err)
	}
	haveJournal := map[string]bool{}
	for _, ev := range res.Events {
		haveJournal[ev.SessionID] = true
	}
	for _, m := range d2.List() {
		if !haveJournal[m.ID] {
			t.Fatalf("session %s present in meta after crash but absent from the journal (meta-without-journal)", m.ID)
		}
	}
}
