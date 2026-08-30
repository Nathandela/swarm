package skeleton

// These tests pin the recoverable half of a hook-spool gap. A missing range is a
// permanent history fact, not permission to stop capturing everything that happens
// after it. Recovery is ordered deliberately:
//
//   1. append the explicit structured_gap boundary;
//   2. durably reset the fold cursor;
//   3. only on a later read fold the retained/future side of the spool.
//
// That order makes the splice visible in the journal and lets a daemon restart between
// any two steps resume without either losing the boundary or applying post-gap records
// ahead of it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
)

func TestHookDrainer_CrashAfterGapJournalBeforeCheckpointDoesNotDuplicateBoundary(t *testing.T) {
	stateDir := t.TempDir()
	sessionDir := t.TempDir()
	const sessionID = "s-gap-crash-window"
	spoolPath := filepath.Join(sessionDir, shim.HookSpoolFile)
	spool, err := shim.OpenHookSpool(spoolPath, 0)
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	if _, err := spool.Append([]byte(`{"n":1}`)); err != nil {
		t.Fatalf("append clean record: %v", err)
	}
	info, err := os.Stat(spoolPath)
	if err != nil {
		t.Fatalf("stat before torn append: %v", err)
	}
	if _, err := spool.Append([]byte(`{"n":2,"padding":"tear this record"}`)); err != nil {
		t.Fatalf("append record to tear: %v", err)
	}
	end, err := os.Stat(spoolPath)
	if err != nil {
		t.Fatalf("stat after torn append: %v", err)
	}
	incarnation := spool.IncarnationID()
	if err := spool.Close(); err != nil {
		t.Fatalf("close spool: %v", err)
	}
	if err := os.Truncate(spoolPath, info.Size()+(end.Size()-info.Size())/2); err != nil {
		t.Fatalf("tear record: %v", err)
	}

	sk := assembleAt(t, stateDir)
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, "")
	instance := mintSessionInstance()
	sk.registerSessionCapabilities(sessionID, protocol.SessionCapabilities{SessionInstance: instance})
	key := hookGapDedupeKey(sessionID, instance, incarnation, 2)
	if err := sk.Core().EmitStructuredGapOnce(sessionID, instance, "hook spool gap at seq 2", key); err != nil {
		t.Fatalf("plant crash-window journal record: %v", err)
	}

	cursorPath := filepath.Join(sessionDir, "hook.fold")
	if err := persistHookCursor(cursorPath, 1); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	hd := NewHookDrainer(sk, sessionID, "", cursorPath)
	hd.SetSpoolPath(spoolPath)
	if _, _, err := hd.drainFromSpoolFile(); err != ErrHookDrainGap {
		t.Fatalf("resume drain = %v, want ErrHookDrainGap", err)
	}
	if got := countJournalStructuredGaps(t, sk, sessionID); got != 1 {
		t.Fatalf("crash-window replay journalled %d boundaries, want exactly 1", got)
	}
}

func TestHookDrainer_GapDedupeUsesPersistedInstanceWithoutCapabilityRecord(t *testing.T) {
	stateDir := t.TempDir()
	sessionDir := t.TempDir()
	const sessionID = "s-gap-instance-without-capability"
	spoolPath := filepath.Join(sessionDir, shim.HookSpoolFile)
	spool, err := shim.OpenHookSpool(spoolPath, 0)
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	if _, err := spool.Append([]byte(`{"n":1}`)); err != nil {
		t.Fatalf("append clean record: %v", err)
	}
	before, err := os.Stat(spoolPath)
	if err != nil {
		t.Fatalf("stat before torn append: %v", err)
	}
	if _, err := spool.Append([]byte(`{"n":2,"padding":"tear this record"}`)); err != nil {
		t.Fatalf("append record to tear: %v", err)
	}
	after, err := os.Stat(spoolPath)
	if err != nil {
		t.Fatalf("stat after torn append: %v", err)
	}
	incarnation := spool.IncarnationID()
	if err := spool.Close(); err != nil {
		t.Fatalf("close spool: %v", err)
	}
	if err := os.Truncate(spoolPath, before.Size()+(after.Size()-before.Size())/2); err != nil {
		t.Fatalf("tear record: %v", err)
	}

	sk := assembleAt(t, stateDir)
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, "")
	instance := mintSessionInstance()
	if err := sk.recordSessionInstance(sessionID, instance, 101); err != nil {
		t.Fatalf("persist session instance: %v", err)
	}
	if _, ok := sk.sessionCapabilities(sessionID); ok {
		t.Fatal("test requires an instance with no authored capability record")
	}

	// Model a crash after the gap journal fsync but before its checkpoint. The
	// durable session-instance sidecar exists independently of capabilities, so
	// the replay must derive the same dedupe key and find this record.
	key := hookGapDedupeKey(sessionID, instance, incarnation, 2)
	if err := sk.Core().EmitStructuredGapOnce(sessionID, instance, "hook spool gap at seq 2", key); err != nil {
		t.Fatalf("plant crash-window journal record: %v", err)
	}
	cursorPath := filepath.Join(sessionDir, "hook.fold")
	if err := persistHookCursor(cursorPath, 1); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	hd := NewHookDrainer(sk, sessionID, "", cursorPath)
	hd.SetSpoolPath(spoolPath)
	if _, _, err := hd.drainFromSpoolFile(); err != ErrHookDrainGap {
		t.Fatalf("resume drain = %v, want ErrHookDrainGap", err)
	}
	if got := countJournalStructuredGaps(t, sk, sessionID); got != 1 {
		t.Fatalf("gap replay without capability journalled %d boundaries, want exactly 1", got)
	}
}

func TestHookDrainer_SameNumericBoundaryInFreshSpoolIncarnationEmitsAgain(t *testing.T) {
	stateDir := t.TempDir()
	sessionDir := t.TempDir()
	const sessionID = "s-gap-fresh-spool"
	spoolPath := filepath.Join(sessionDir, shim.HookSpoolFile)
	cursorPath := filepath.Join(sessionDir, "hook.fold")
	sk := assembleAt(t, stateDir)
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, "")

	makeTornAtTwo := func() string {
		spool, err := shim.OpenHookSpool(spoolPath, 0)
		if err != nil {
			t.Fatalf("open spool: %v", err)
		}
		if _, err := spool.Append([]byte(`{"n":1}`)); err != nil {
			t.Fatalf("append clean record: %v", err)
		}
		before, _ := os.Stat(spoolPath)
		if _, err := spool.Append([]byte(`{"n":2,"padding":"tear this record"}`)); err != nil {
			t.Fatalf("append record to tear: %v", err)
		}
		after, _ := os.Stat(spoolPath)
		id := spool.IncarnationID()
		if err := spool.Close(); err != nil {
			t.Fatalf("close spool: %v", err)
		}
		if err := os.Truncate(spoolPath, before.Size()+(after.Size()-before.Size())/2); err != nil {
			t.Fatalf("tear record: %v", err)
		}
		return id
	}

	firstID := makeTornAtTwo()
	if err := persistHookCursor(cursorPath, 1); err != nil {
		t.Fatalf("seed first cursor: %v", err)
	}
	hd := NewHookDrainer(sk, sessionID, "", cursorPath)
	hd.SetSpoolPath(spoolPath)
	if _, _, err := hd.drainFromSpoolFile(); err != ErrHookDrainGap {
		t.Fatalf("first drain = %v, want ErrHookDrainGap", err)
	}

	if err := os.Remove(spoolPath); err != nil {
		t.Fatalf("remove first spool: %v", err)
	}
	secondID := makeTornAtTwo()
	if secondID == firstID {
		t.Fatalf("fresh spool reused incarnation %q", firstID)
	}
	if err := persistHookCursor(cursorPath, 1); err != nil {
		t.Fatalf("seed second cursor: %v", err)
	}
	hd = NewHookDrainer(sk, sessionID, "", cursorPath)
	hd.SetSpoolPath(spoolPath)
	if _, _, err := hd.drainFromSpoolFile(); err != ErrHookDrainGap {
		t.Fatalf("second drain = %v, want ErrHookDrainGap", err)
	}
	if got := countJournalStructuredGaps(t, sk, sessionID); got != 2 {
		t.Fatalf("two spool incarnations with boundary 2 journalled %d gaps, want 2", got)
	}
}

func TestHookDrainer_DecimalCheckpointMigratesWithAnAdoptedLegacySpool(t *testing.T) {
	stateDir := t.TempDir()
	sessionDir := t.TempDir()
	const sessionID = "s-gap-legacy-migrate"
	spoolPath := filepath.Join(sessionDir, shim.HookSpoolFile)
	cursorPath := filepath.Join(sessionDir, "hook.fold")

	spool, err := shim.OpenHookSpool(spoolPath, 0)
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	if _, err := spool.Append([]byte(`{"n":1}`)); err != nil {
		t.Fatalf("append clean record: %v", err)
	}
	before, _ := os.Stat(spoolPath)
	if _, err := spool.Append([]byte(`{"n":2,"padding":"tear this record"}`)); err != nil {
		t.Fatalf("append record to tear: %v", err)
	}
	after, _ := os.Stat(spoolPath)
	if err := spool.Close(); err != nil {
		t.Fatalf("close spool: %v", err)
	}
	if err := os.Truncate(spoolPath, before.Size()+(after.Size()-before.Size())/2); err != nil {
		t.Fatalf("tear record: %v", err)
	}
	// Remove only the new identity sidecar: this is the on-disk shape an upgrade
	// encounters when hooks.spool and hook.fold.gap were authored by the old build.
	if err := os.Remove(spoolPath + ".incarnation"); err != nil {
		t.Fatalf("remove identity to simulate legacy spool: %v", err)
	}
	adopted, err := shim.OpenHookSpool(spoolPath, 0)
	if err != nil {
		t.Fatalf("open legacy spool with new shim: %v", err)
	}
	adoptedID := adopted.IncarnationID()
	if !adopted.AdoptedLegacySequence() {
		t.Fatal("new shim did not mark the existing pre-identity spool as adopted legacy")
	}
	if err := adopted.Close(); err != nil {
		t.Fatalf("close adopted spool: %v", err)
	}

	if err := persistHookCursor(cursorPath, 1); err != nil {
		t.Fatalf("seed fold cursor: %v", err)
	}
	if err := persistHookCursor(hookGapCursorPath(cursorPath), 2); err != nil {
		t.Fatalf("seed decimal legacy gap checkpoint: %v", err)
	}
	sk := assembleAt(t, stateDir)
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, "")
	if err := sk.Core().EmitStructuredGap(sessionID, "hook spool gap at seq 2"); err != nil {
		t.Fatalf("seed legacy structured gap: %v", err)
	}

	hd := NewHookDrainer(sk, sessionID, "", cursorPath)
	hd.SetSpoolPath(spoolPath)
	if _, _, err := hd.drainFromSpoolFile(); err != ErrHookDrainGap {
		t.Fatalf("legacy migration drain = %v, want ErrHookDrainGap", err)
	}
	if got := countJournalStructuredGaps(t, sk, sessionID); got != 1 {
		t.Fatalf("legacy checkpoint migration appended a duplicate: got %d gaps, want 1", got)
	}
	checkpoint := readHookGapCheckpoint(hookGapCursorPath(cursorPath))
	if checkpoint.SpoolIncarnation != adoptedID || checkpoint.Boundary != 2 {
		t.Fatalf("migrated checkpoint = %+v, want incarnation %q boundary 2", checkpoint, adoptedID)
	}
}

func TestHookDrainer_GapBoundaryPrecedesRetainedAndFutureEvents(t *testing.T) {
	stateDir := t.TempDir()
	sessionDir := t.TempDir()
	const sessionID, token = "s-gap-recover", "tok-gap-recover"

	h := startHookShim(t, sessionDir, sessionID)
	postTurnEvent(t, h.cfg.HookSocketPath, sessionID, token, 1)

	sk := assembleAt(t, stateDir)
	// sk is reassigned across the restart below; this cleanup therefore closes the
	// final incarnation while the first one is closed explicitly at the crash seam.
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, token)
	scriptedCapture(sk, "before-gap", "retained-4", "retained-5", "future-6")

	cursorPath := filepath.Join(sessionDir, "hook.fold")
	hd := NewHookDrainer(sk, sessionID, h.cfg.HookSocketPath, cursorPath)
	if applied, _, err := hd.DrainOnce(); err != nil || applied != 1 {
		t.Fatalf("initial DrainOnce = applied %d err %v, want applied 1", applied, err)
	}
	awaitJournalTexts(t, sk, sessionID, 1)

	for seq := uint64(2); seq <= 5; seq++ {
		postTurnEvent(t, h.cfg.HookSocketPath, sessionID, token, seq)
	}
	h.kill(t)

	// Retire 2 and 3 while this drainer is durably at 1. Records 4 and 5 are
	// retained, but they must not be applied until an explicit gap is journalled.
	spoolPath := filepath.Join(sessionDir, shim.HookSpoolFile)
	spool, err := shim.OpenHookSpool(spoolPath, 0)
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	if err := spool.Compact(3); err != nil {
		_ = spool.Close()
		t.Fatalf("compact through missing range: %v", err)
	}
	if err := spool.Close(); err != nil {
		t.Fatalf("close compacted spool: %v", err)
	}

	h2 := startHookShim(t, sessionDir, sessionID)
	t.Cleanup(func() { h2.kill(t) })

	if applied, _, err := hd.DrainOnce(); err != ErrHookDrainGap || applied != 0 {
		t.Fatalf("gap DrainOnce = applied %d err %v, want applied 0 and recoverable gap", applied, err)
	}
	if !hd.recoveringGap() {
		t.Fatal("gap DrainOnce did not leave the drainer in recoverable-reset state")
	}
	if got := hd.cursor(); got != 0 {
		t.Fatalf("cursor after explicit gap = %d, want durable recovery reset 0", got)
	}
	if got := countJournalStructuredGaps(t, sk, sessionID); got != 1 {
		t.Fatalf("structured gaps after recovery reset = %d, want 1", got)
	}
	if got := countJournalInteractions(t, sk, sessionID); got != 1 {
		t.Fatalf("post-gap records were applied before their boundary: interactions=%d, want only the pre-gap item", got)
	}

	// Crash/restart at the narrowest durability seam: the boundary, boundary sidecar,
	// and reset cursor exist, but no post-gap record has been folded. A fresh drainer
	// has no process-local recovery latch; the durable cursor alone must make it adopt
	// the retained sequence space without emitting the same boundary again.
	if err := sk.Close(); err != nil {
		t.Fatalf("close daemon after durable gap reset: %v", err)
	}
	sk = assembleAt(t, stateDir)
	registerTestSession(sk, sessionID, token)
	scriptedCapture(sk, "retained-4", "retained-5", "future-6")
	hd = NewHookDrainer(sk, sessionID, h2.cfg.HookSocketPath, cursorPath)
	if hd.recoveringGap() {
		t.Fatal("fresh drainer inherited a process-local recovery latch")
	}
	if applied, _, err := hd.DrainOnce(); err != nil || applied != 2 {
		t.Fatalf("post-restart retained DrainOnce = applied %d err %v, want applied 2", applied, err)
	}
	if got := hd.cursor(); got != 5 {
		t.Fatalf("cursor after retained side = %d, want 5", got)
	}
	if got := countJournalStructuredGaps(t, sk, sessionID); got != 1 {
		t.Fatalf("structured gaps after retained side = %d, want exactly 1", got)
	}

	postTurnEvent(t, h2.cfg.HookSocketPath, sessionID, token, 6)
	if applied, _, err := hd.DrainOnce(); err != nil || applied != 1 {
		t.Fatalf("future DrainOnce = applied %d err %v, want applied 1", applied, err)
	}
	awaitJournalTexts(t, sk, sessionID, 4)

	wantOrder := []string{"interaction:before-gap", "gap", "interaction:retained-4", "interaction:retained-5", "interaction:future-6"}
	if got := hookJournalOrder(t, sk, sessionID); !equalStrings(got, wantOrder) {
		t.Fatalf("journal chronology = %q, want %q", got, wantOrder)
	}
}

func TestRunHookDrain_ContinuesAfterRecoverableGap(t *testing.T) {
	stateDir := t.TempDir()
	sessionDir := t.TempDir()
	const sessionID, token = "s-gap-loop", "tok-gap-loop"

	h := startHookShim(t, sessionDir, sessionID)
	for seq := uint64(1); seq <= 5; seq++ {
		postTurnEvent(t, h.cfg.HookSocketPath, sessionID, token, seq)
	}
	h.kill(t)

	spoolPath := filepath.Join(sessionDir, shim.HookSpoolFile)
	spool, err := shim.OpenHookSpool(spoolPath, 0)
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	if err := spool.Compact(3); err != nil {
		_ = spool.Close()
		t.Fatalf("compact through missing range: %v", err)
	}
	if err := spool.Close(); err != nil {
		t.Fatalf("close compacted spool: %v", err)
	}

	h2 := startHookShim(t, sessionDir, sessionID)
	t.Cleanup(func() { h2.kill(t) })
	sk := assembleAt(t, stateDir)
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, token)
	scriptedCapture(sk, "loop-retained-4", "loop-retained-5", "loop-future-6")

	cursorPath := filepath.Join(sessionDir, "hook.fold")
	if err := persistHookCursor(cursorPath, 1); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	hd := NewHookDrainer(sk, sessionID, h2.cfg.HookSocketPath, cursorPath)
	stop := make(chan struct{})
	sk.drains.wg.Add(1)
	go sk.runHookDrain(sessionID, hd, stop)
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			close(stop)
		}
		sk.drains.wg.Wait()
	})

	// Two ticks are required: one journals the boundary and resets, the next
	// adopts records 4 and 5. If runHookDrain still treats every gap as terminal,
	// this wait times out with no interactions.
	awaitJournalTexts(t, sk, sessionID, 2)
	if got := countJournalStructuredGaps(t, sk, sessionID); got != 1 {
		t.Fatalf("structured gaps after loop recovery = %d, want 1", got)
	}

	postTurnEvent(t, h2.cfg.HookSocketPath, sessionID, token, 6)
	got := awaitJournalTexts(t, sk, sessionID, 3)
	want := []string{"loop-retained-4", "loop-retained-5", "loop-future-6"}
	if !equalStrings(got, want) {
		t.Fatalf("loop-folded texts = %q, want %q", got, want)
	}

	close(stop)
	stopped = true
	sk.drains.wg.Wait()
}

func TestHookDrainer_FinalDrainRecoversRetainedSideWithNoLiveSocket(t *testing.T) {
	stateDir := t.TempDir()
	sessionDir := t.TempDir()
	const sessionID, token = "s-gap-final", "tok-gap-final"

	h := startHookShim(t, sessionDir, sessionID)
	for seq := uint64(1); seq <= 5; seq++ {
		postTurnEvent(t, h.cfg.HookSocketPath, sessionID, token, seq)
	}
	h.kill(t) // FinalDrain must use the file after ordinary session teardown.

	spoolPath := filepath.Join(sessionDir, shim.HookSpoolFile)
	spool, err := shim.OpenHookSpool(spoolPath, 0)
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	if err := spool.Compact(3); err != nil {
		_ = spool.Close()
		t.Fatalf("compact through missing range: %v", err)
	}
	if err := spool.Close(); err != nil {
		t.Fatalf("close compacted spool: %v", err)
	}

	sk := assembleAt(t, stateDir)
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, token)
	scriptedCapture(sk, "final-retained-4", "final-retained-5")

	cursorPath := filepath.Join(sessionDir, "hook.fold")
	if err := persistHookCursor(cursorPath, 1); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	hd := NewHookDrainer(sk, sessionID, h.cfg.HookSocketPath, cursorPath)
	hd.SetSpoolPath(spoolPath)
	hd.FinalDrain()

	// There is no later poll after OnSessionEnd. FinalDrain itself must make the
	// boundary-producing disk read and the later retained-side disk read.
	got := awaitJournalTexts(t, sk, sessionID, 2)
	want := []string{"final-retained-4", "final-retained-5"}
	if !equalStrings(got, want) {
		t.Fatalf("final-drain texts = %q, want %q", got, want)
	}
	if got := hd.cursor(); got != 5 {
		t.Fatalf("final-drain cursor = %d, want 5", got)
	}
	if got := countJournalStructuredGaps(t, sk, sessionID); got != 1 {
		t.Fatalf("final-drain structured gaps = %d, want 1", got)
	}
}

func hookJournalOrder(t *testing.T, sk *Daemon, sessionID string) []string {
	t.Helper()
	res, err := sk.Core().JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}
	var out []string
	for _, rec := range res.Events {
		if rec.SessionID != sessionID {
			continue
		}
		switch rec.Type {
		case journal.TypeStructuredGap:
			out = append(out, "gap")
		case journal.TypeInteraction:
			var item map[string]any
			if err := json.Unmarshal(rec.Payload, &item); err != nil {
				t.Fatalf("decode interaction: %v", err)
			}
			if text, _ := item["text"].(string); text != "" {
				out = append(out, "interaction:"+text)
			}
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
