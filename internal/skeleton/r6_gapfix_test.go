package skeleton

// R6 REVIEW FIX-PACK regression tests: each function here pins one BLOCKER a probe
// confirmed against the original HookDrainer.DrainOnce/ingestHookBytes implementation,
// as a permanent test rather than a throwaway script -- mirrors internal/shim's own
// r6_gapfix_test.go convention. See hookdrain.go's DrainOnce/ingestHookBytes comments
// for the design these tests hold in place.

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
)

// countJournalInteractions returns how many journal `interaction` records sessionID
// holds right now (no polling -- callers that expect a positive count use
// awaitJournalTexts; this is for asserting an ABSENCE stays true across a grace
// window, where waiting for a count to reach a target would defeat the point).
func countJournalInteractions(t *testing.T, sk *Daemon, sessionID string) int {
	t.Helper()
	res, err := sk.Core().JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}
	n := 0
	for _, rec := range res.Events {
		if rec.Type == journal.TypeInteraction && rec.SessionID == sessionID {
			n++
		}
	}
	return n
}

// countJournalStructuredGaps returns how many journal structured_gap records
// sessionID holds right now.
func countJournalStructuredGaps(t *testing.T, sk *Daemon, sessionID string) int {
	t.Helper()
	res, err := sk.Core().JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}
	n := 0
	for _, rec := range res.Events {
		if rec.Type == journal.TypeStructuredGap && rec.SessionID == sessionID {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// BLOCKER 1: a redelivered record must never shape a second journal item.
//
// engine.HandleCallback's own cb.Sequence replay guard (applyTyped) only runs for a
// callback that derives a status dimension. A capture=raw event with none (the
// common case -- PostToolUse et al) short-circuits to a benign "accept" BEFORE that
// guard, so redelivering the identical spool-backed record (a crash between
// ingestHookBytes applying it and DrainOnce persisting the advanced cursor, or
// PostToShim's own ack-less retry landing a second spool Seq under the same
// cb.Sequence) used to shape a SECOND interaction item for one logical event.
// ---------------------------------------------------------------------------

func TestHookDrainer_RedeliveredUnmappedEventNeverDuplicatesTheJournalItem(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskgf1")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sessionDir, err := os.MkdirTemp("/tmp", "swskgfs1")
	if err != nil {
		t.Fatalf("session dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sessionDir) })

	const sessionID, token = "s-gf-1", "tok-gf-1"
	h := startHookShim(t, sessionDir, sessionID)
	t.Cleanup(func() { h.kill(t) })

	// A capture=raw event with NO turn/interaction payload key: registerTestSession
	// registers no SignalSources (nil), so this derives NO status dimension --
	// exactly the "unmapped event" path engine.HandleCallback accepts as a benign
	// no-op before its own cb.Sequence check ever runs.
	cb := engine.Callback{SessionID: sessionID, Token: token, Sequence: 1, Event: "PostToolUse"}
	acked, err := hookclient.PostToShim(h.cfg.HookSocketPath, cb)
	if err != nil || !acked {
		t.Fatalf("post seq=1: acked=%v err=%v", acked, err)
	}

	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, token)
	scriptedCapture(sk, "cap-1")

	cursorPath := filepath.Join(sessionDir, "hook.fold")
	hd := NewHookDrainer(sk, sessionID, h.cfg.HookSocketPath, cursorPath)

	applied, skipped, err := hd.DrainOnce()
	if err != nil {
		t.Fatalf("first DrainOnce: %v", err)
	}
	if applied != 1 || skipped != 0 {
		t.Fatalf("first DrainOnce applied=%d skipped=%d, want applied=1 skipped=0", applied, skipped)
	}
	awaitJournalTexts(t, sk, sessionID, 1)

	// Simulate the crash window DrainOnce's own doc names: the record was applied
	// (the journal item above already proves it), but the cursor persist that
	// should have followed it did not survive -- rewind the persisted cursor back
	// behind the record, exactly like a restart resuming mid-batch would find it.
	if err := persistHookCursor(cursorPath, 0); err != nil {
		t.Fatalf("rewind cursor: %v", err)
	}

	// R6 REVIEW FIX-PACK ROUND 1 (BLOCKER 2b): the ORIGINAL version of this test kept
	// the SAME live *Daemon and the SAME HookDrainer across both drains, so it
	// exercised precisely the in-memory guard that CANNOT survive the crash the test
	// is named for ("a crash between ingestHookBytes applying it and DrainOnce
	// persisting the advanced cursor" takes the map with it). The crash is now real:
	// the first daemon incarnation is CLOSED and a genuinely new one is opened over
	// the same state dir, which is what a restarted daemon is.
	if err := sk.Close(); err != nil {
		t.Fatalf("close the first daemon incarnation: %v", err)
	}

	sk2 := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk2.Close() })
	registerTestSession(sk2, sessionID, token)
	// A DISTINCT label, so a second shape would be unmistakable in the journal rather
	// than merely indistinguishable from the first.
	scriptedCapture(sk2, "cap-1-redelivered")

	hd2 := NewHookDrainer(sk2, sessionID, h.cfg.HookSocketPath, cursorPath)
	applied2, skipped2, err := hd2.DrainOnce()
	if err != nil {
		t.Fatalf("second (redelivery, fresh daemon) DrainOnce: %v", err)
	}
	if applied2 != 0 || skipped2 != 1 {
		t.Fatalf("redelivery DrainOnce against a FRESH daemon applied=%d skipped=%d, want applied=0 skipped=1 (a duplicate, rejected -- not a second apply); the per-session ingest high-water mark must be durable, not an in-memory map the crash takes with it", applied2, skipped2)
	}

	// Give any (incorrect) second shape a moment to land, then assert it never did.
	time.Sleep(200 * time.Millisecond)
	if n := countJournalInteractions(t, sk2, sessionID); n != 1 {
		t.Fatalf("journal holds %d interaction item(s) for %s after a redelivered record, want exactly 1 (no duplicate)", n, sessionID)
	}
}

// ---------------------------------------------------------------------------
// BLOCKER 2 (ADR-017 "never silently bridged"): a record returned ABOVE a
// reported gap boundary must never be applied in the boundary-producing drain.
// The explicit gap is journalled first; only then may the durable cursor reset so
// a later drain can adopt the retained side without hiding the hole.
// ---------------------------------------------------------------------------

func TestHookDrainer_OnAHoleAboveTheCursor_NeverAppliesPastTheBoundary(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskgf2")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sessionDir, err := os.MkdirTemp("/tmp", "swskgfs2")
	if err != nil {
		t.Fatalf("session dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sessionDir) })

	const sessionID, token = "s-gf-2", "tok-gf-2"
	h := startHookShim(t, sessionDir, sessionID)
	for i := uint64(1); i <= 5; i++ {
		postTurnEvent(t, h.cfg.HookSocketPath, sessionID, token, i)
	}
	h.kill(t) // stop before manipulating the spool directly -- no concurrent writer

	// Fold the spool through seq 3 out-of-band (a manual/administrative compaction,
	// or some other reader's own fold cursor racing ahead) -- retained records are
	// now 4 and 5, with nothing to prove what 2 and 3 ever were.
	spoolPath := filepath.Join(sessionDir, shim.HookSpoolFile)
	spool, err := shim.OpenHookSpool(spoolPath, 0)
	if err != nil {
		t.Fatalf("open spool to compact ahead of the reader: %v", err)
	}
	if err := spool.Compact(3); err != nil {
		t.Fatalf("Compact(3): %v", err)
	}
	_ = spool.Close()

	h2 := startHookShim(t, sessionDir, sessionID)
	t.Cleanup(func() { h2.kill(t) })

	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, token)
	scriptedCapture(sk, "must-never-apply-4", "must-never-apply-5")

	cursorPath := filepath.Join(sessionDir, "hook.fold")
	// Simulate this drainer's own last-observed cursor sitting at 1 -- behind the
	// hole the out-of-band compaction just opened between 1 and the retained window.
	if err := persistHookCursor(cursorPath, 1); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	hd := NewHookDrainer(sk, sessionID, h2.cfg.HookSocketPath, cursorPath)
	applied, _, err := hd.DrainOnce()
	if err != ErrHookDrainGap {
		t.Fatalf("DrainOnce over a hole above the cursor returned err=%v, want ErrHookDrainGap", err)
	}
	if applied != 0 {
		t.Fatalf("DrainOnce applied %d record(s) from ABOVE a reported gap boundary, want 0 -- ADR-017 forbids silently bridging the hole", applied)
	}
	if hd.cursor() != 0 {
		t.Fatalf("Cursor() = %d after a gap, want 0 -- only a durable reset after the explicit boundary may adopt the retained side", hd.cursor())
	}
	if n := countJournalInteractions(t, sk, sessionID); n != 0 {
		t.Fatalf("journal holds %d interaction item(s) for %s from past a reported gap, want 0", n, sessionID)
	}
}

// ---------------------------------------------------------------------------
// MEDIUM (ADR-017 "an exact structured_gap boundary", singular): a daemon RESTART
// must never re-emit a second structured_gap record for the IDENTICAL boundary a
// prior incarnation already reported. Because a proven tear never clears (BLOCKER 2
// pins the cursor at the boundary forever), every later incarnation's drain would
// otherwise rediscover the SAME gap and re-report it.
//
// R6 REVIEW FIX-PACK ROUND 1 (BLOCKER 1b): the test walked past its own kill. It set
// up the exact scenario -- restart, reconcile re-registering structured_chat=true --
// and then asserted ONLY the structured_gap COUNT, never reading the capability record
// back. A probe against that implementation found structured_chat RESURRECTED to true
// on the second incarnation, i.e. ADR-017 T2 rule 2's "it cannot upgrade a fallback
// session in place", inverted. The capability assertions below are the missing half.
// ---------------------------------------------------------------------------

func TestHookDrainer_SameGapAcrossADaemonRestart_ReportsOnceAndStaysDegraded(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskgf3")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sessionDir, err := os.MkdirTemp("/tmp", "swskgfs3")
	if err != nil {
		t.Fatalf("session dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sessionDir) })

	const sessionID, token = "s-gf-3", "tok-gf-3"
	h := startHookShim(t, sessionDir, sessionID)
	postTurnEvent(t, h.cfg.HookSocketPath, sessionID, token, 1)
	postTurnEvent(t, h.cfg.HookSocketPath, sessionID, token, 2)
	h.kill(t)

	// Tear record 3 (the same technique r6_hookdrain_test.go's own gap test uses):
	// a torn TAIL, so 1 and 2 stay cleanly readable and the boundary is fixed at 3
	// for as long as this spool file exists.
	spoolPath := filepath.Join(sessionDir, shim.HookSpoolFile)
	spool, err := shim.OpenHookSpool(spoolPath, 0)
	if err != nil {
		t.Fatalf("open spool to seed a tear: %v", err)
	}
	before, err := os.Stat(spoolPath)
	if err != nil {
		t.Fatalf("stat before torn append: %v", err)
	}
	if _, err := spool.Append([]byte(`{"session_id":"s-gf-3","token":"tok-gf-3","sequence":3,"event":"Stop","payload":{"turn":"active"}}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	after, err := os.Stat(spoolPath)
	if err != nil {
		t.Fatalf("stat after torn append: %v", err)
	}
	_ = spool.Close()
	if err := os.Truncate(spoolPath, before.Size()+(after.Size()-before.Size())/2); err != nil {
		t.Fatalf("truncate to tear the last record: %v", err)
	}

	h2 := startHookShim(t, sessionDir, sessionID)
	t.Cleanup(func() { h2.kill(t) })
	cursorPath := filepath.Join(sessionDir, "hook.fold")

	// FIRST daemon incarnation: discovers the gap, reports it.
	sk1 := assembleAt(t, dir)
	registerTestSession(sk1, sessionID, token)
	scriptedCapture(sk1, "gf3-1", "gf3-2")
	sk1.registerSessionCapabilities(sessionID, protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.0.0", AdapterRevision: "rev",
		StructuredChat: true, TerminalFallback: false,
	})
	hd1 := NewHookDrainer(sk1, sessionID, h2.cfg.HookSocketPath, cursorPath)
	if _, _, err := hd1.DrainOnce(); err != ErrHookDrainGap {
		t.Fatalf("first incarnation DrainOnce: err=%v, want ErrHookDrainGap", err)
	}
	if n := countJournalStructuredGaps(t, sk1, sessionID); n != 1 {
		t.Fatalf("journal holds %d structured_gap record(s) after the first incarnation, want 1", n)
	}
	caps1, ok := sk1.sessionCapabilities(sessionID)
	if !ok {
		t.Fatalf("SessionCapabilities(%s) not found after the first incarnation's degrade", sessionID)
	}
	if caps1.StructuredChat || caps1.TerminalFallback || caps1.TerminalControl {
		t.Fatalf("after incarnation 1's proven gap: %+v, want chat/fallback/control all false", caps1)
	}
	if err := sk1.Close(); err != nil {
		t.Fatalf("close first incarnation: %v", err)
	}

	// SECOND daemon incarnation, same on-disk state dir and the SAME cursorPath:
	// the identical tear is still there (nothing about the shim's spool changed),
	// so a fresh drain rediscovers the identical boundary.
	sk2 := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk2.Close() })
	registerTestSession(sk2, sessionID, token)
	scriptedCapture(sk2, "gf3-1", "gf3-2")
	// A restart's own reconcile re-registering the capability record, exactly like
	// r6_hookdrain_test.go's one-way-degrade test does. This is the resurrection
	// vector: the record the adapter authored at launch says structured_chat=true,
	// and the incarnation that degraded it is gone.
	sk2.registerSessionCapabilities(sessionID, protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.0.0", AdapterRevision: "rev",
		StructuredChat: true, TerminalFallback: false,
	})
	caps2, ok := sk2.sessionCapabilities(sessionID)
	if !ok {
		t.Fatalf("SessionCapabilities(%s) not found on the second incarnation", sessionID)
	}
	if caps2.StructuredChat {
		t.Fatalf("StructuredChat = true on a FRESH daemon incarnation whose session carries a still-proven, still-present spool gap -- ADR-017 T2 rule 2 ('it cannot upgrade a fallback session in place'), inverted: the degrade must be DURABLE, not die with the process that authored it")
	}
	if caps2.TerminalFallback || caps2.TerminalControl {
		t.Fatalf("second incarnation's history gap granted terminal authority: %+v", caps2)
	}

	hd2 := NewHookDrainer(sk2, sessionID, h2.cfg.HookSocketPath, cursorPath)
	if _, _, err := hd2.DrainOnce(); err != ErrHookDrainGap {
		t.Fatalf("second incarnation DrainOnce: err=%v, want ErrHookDrainGap", err)
	}

	if n := countJournalStructuredGaps(t, sk2, sessionID); n != 1 {
		t.Fatalf("journal holds %d structured_gap record(s) across two daemon incarnations draining the SAME boundary, want exactly 1", n)
	}
	caps3, ok := sk2.sessionCapabilities(sessionID)
	if !ok {
		t.Fatalf("SessionCapabilities(%s) not found after the second incarnation's drain", sessionID)
	}
	if caps3.StructuredChat || caps3.TerminalFallback || caps3.TerminalControl {
		t.Fatalf("after incarnation 2 re-drained the SAME proven gap: %+v, want chat/fallback/control all false -- emit dedupe gates only the journal append", caps3)
	}
}

// ---------------------------------------------------------------------------
// MEDIUM: serveHook's LIVE (non-shim) path must decode and ingest a callback the
// instant it is complete on the wire, not wait for the peer to close the
// connection. hookclient.Post always closes right after writing, so this never
// regressed against any real caller -- but the shared ingestHookBytes([]byte) seam
// needs its OWN byte-boundary to come from the JSON value being complete, exactly
// like the pre-R6 json.Decoder-based decodeHookCallback it replaced, not from
// io.ReadAll's EOF.
// ---------------------------------------------------------------------------

func TestServeHook_CallbackStillIngestedWhenThePeerHoldsTheConnectionOpen(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskgf5")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	const sessionID, token = "s-gf-5", "tok-gf-5"
	registerTestSession(sk, sessionID, token)
	scriptedCapture(sk, "held-open-1")

	body, err := json.Marshal(engine.Callback{SessionID: sessionID, Token: token, Sequence: 1, Event: "PostToolUse"})
	if err != nil {
		t.Fatalf("marshal callback: %v", err)
	}
	conn, err := net.Dial("unix", filepath.Join(dir, "d.sock"))
	if err != nil {
		t.Fatalf("dial daemon socket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write(body); err != nil {
		t.Fatalf("write callback: %v", err)
	}
	// Deliberately do NOT close conn: a peer that writes a complete, well-formed
	// callback and then just sits there must not delay ingest.

	deadline := time.Now().Add(1500 * time.Millisecond) // well under demuxReadTimeout (3s)
	for {
		if countJournalInteractions(t, sk, sessionID) >= 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("callback not ingested within %s of a still-open connection -- serveHook is blocking on EOF instead of decoding at value completion", 1500*time.Millisecond)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
