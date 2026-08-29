package skeleton

// R6 (bd agents-tracker-hggx.7) FAILING-FIRST (TDD RED, GG-5) tests for the DAEMON-side half
// of playbook §6.1's structured-capture survival boundary: "the daemon drains the spool
// idempotently ... daemon unavailability neither fails a provider hook nor loses an accepted
// item ... an unrecoverable spool/cursor gap emits an exact structured_gap boundary, disables
// structured_chat for that session instance, and forbids a fabricated completion."
//
// WHY THIS LIVES HERE. Exactly interaction.go's own reason (its header comment, verbatim):
// internal/remotegw already depends on internal/daemon, so a daemon package that dialed the
// shim's hook socket and imported the adapter/journal producer chain would risk the same
// cycle the interaction producer sidesteps by living in the assembly layer. skeleton is the
// one package that already imports internal/daemon, internal/adapter, internal/protocol AND
// (new, this file) internal/shim, so the drain is assembled here, exactly beside the producer
// it feeds.
//
// SCOPE NOTE, stated so the gap between this test's harness and production is not mistaken
// for an oversight. internal/daemon/launch.go's spawnShim/shimSpawnConfig -- the code that
// would thread HookSocketPath through `sk.Core().Launch()` into a REAL production-spawned
// shim -- is out of this slice's owned files (only structured_gap emission is). These tests
// therefore start their OWN shim (shim.Run, in-process, exactly r6_hooksocket_test.go's
// runShimAsync pattern) and register its session directly on the engine (sk.eng.
// RegisterSession) rather than going through Launch — proving the drain/ingest machinery
// this package owns, independent of the launch-time wiring a different lane owns. The two
// meet only at HookSocketPath/HookSpoolFile, both frozen in internal/shim already.
//
// THE SEAMS THIS FILE PINS (undefined symbols -> compile-fail RED):
//
//	// hookdrain.go
//	func (d *Daemon) ingestHookBytes(raw []byte) error
//	    // serveHook's (conn.go) shared second half, factored out: hookclient.Decode(raw) ->
//	    // d.eng.HandleCallback(cb) -> (only if that returned nil) d.serveHookInteractions(cb).
//	    // Both the live hook-socket path and the drain path below apply a record through
//	    // this ONE function, so they can never diverge in behavior.
//
//	type HookDrainer struct{ ... }
//	func NewHookDrainer(d *Daemon, sessionID, hookSocketPath, cursorPath string) *HookDrainer
//	func (hd *HookDrainer) DrainOnce() (applied int, err error)
//	    // one dial+drain+apply+persist cycle against hookSocketPath (shim.HookDrainTag wire,
//	    // internal/shim/r6_hooksocket_test.go): read the persisted cursor (0 if cursorPath
//	    // does not exist yet), request records with seq>cursor (and fold=cursor, so the shim
//	    // may compact what was durably applied and persisted on the PRIOR call -- never on
//	    // this one, so the cursor written by THIS call cannot be compacted out from under a
//	    // reader that has not yet observed it), ingestHookBytes each record IN ORDER,
//	    // persisting (fsync) the advanced cursor after each one so a crash mid-batch loses at
//	    // most the redelivery of the record in flight -- which ingestHookBytes's own
//	    // HandleCallback replay check (cb.Sequence) then makes a safe no-op, not a duplicate
//	    // journal item. On a reported gap, DrainOnce applies every pre-gap record exactly as
//	    // above, then calls d.Core().EmitStructuredGap and degrades the session's stored
//	    // capability record (see below), and returns applied (the pre-gap count) with
//	    // ErrHookDrainGap -- it never advances past the boundary and never calls DrainOnce
//	    // again on its own.
//	func (hd *HookDrainer) cursor() uint64  // the persisted, applied-and-folded cursor
//
//	var ErrHookDrainGap = errors.New("skeleton: hook drain observed a spool gap")
//
//	// capability.go's store half, sibling of the existing pure deriveSessionCapabilities:
//	func (d *Daemon) sessionCapabilities(sessionID string) (protocol.SessionCapabilities, bool)
//	func (d *Daemon) registerSessionCapabilities(sessionID string, c protocol.SessionCapabilities)
//	    // re-registering an id that already has a record MERGES via SetStructuredChat's
//	    // degrade-only rule rather than overwriting it outright, so a reconcile-time
//	    // re-registration after a restart can never resurrect a session a prior incarnation
//	    // degraded (T2 rule 2's "one-way", carried across the daemon's own restart).

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/transcript"
	"github.com/Nathandela/swarm/internal/wire"
)

// ---------------------------------------------------------------------------
// Harness: a real shim (in-process, bypassing daemon.spawnShim -- see the scope note
// above) plus a daemon whose engine registers that same session directly.
// ---------------------------------------------------------------------------

// assembleAt is assemble (serve_test.go) parameterized on an explicit, reusable state dir,
// so a test can Close and re-Serve the SAME on-disk state to model a daemon restart.
func assembleAt(t *testing.T, dir string) *Daemon {
	t.Helper()
	buildBinaries(t)
	sk, err := Serve(Config{
		StateDir:           dir,
		SocketPath:         filepath.Join(dir, "d.sock"),
		LockPath:           filepath.Join(dir, "d.lock"),
		LogPath:            filepath.Join(dir, "d.log"),
		ShimBinary:         swarmBin,
		MaxSessions:        16,
		PollInterval:       50 * time.Millisecond,
		StalenessThreshold: 2 * time.Second,
		FakeAgentBin:       fakeAgentBin,
	})
	if err != nil {
		t.Fatalf("skeleton.Serve: %v", err)
	}
	return sk
}

// hookShim is one in-process shim instance with its hook socket wired, standing in for a
// production-spawned shim until daemon.spawnShim (a different lane's file) threads
// HookSocketPath through Launch.
type hookShim struct {
	cfg  shim.Config
	done <-chan struct{}
}

func startHookShim(t *testing.T, sessionDir, sessionID string) hookShim {
	t.Helper()
	// buildBinaries populates the package-level fakeAgentBin this function uses below
	// via sync.Once, so it is safe (and cheap after the first call) to call it here
	// too rather than relying on assembleAt having already run first in test-file
	// order -- a focused or sharded `-run` that never reaches assembleAt before this
	// function must not find fakeAgentBin still empty.
	buildBinaries(t)
	scriptPath := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(scriptPath, []byte("idle 600s\n"), 0o600); err != nil {
		t.Fatalf("write agent script: %v", err)
	}
	cfg := shim.Config{
		SessionID:      sessionID,
		Argv:           []string{fakeAgentBin, scriptPath},
		Cwd:            t.TempDir(),
		Env:            []string{"PATH=" + os.Getenv("PATH")},
		SocketPath:     filepath.Join(sessionDir, "c.sock"),
		HookSocketPath: filepath.Join(sessionDir, "h.sock"),
		SessionDir:     sessionDir,
		Cols:           80,
		Rows:           24,
		TranscriptCfg:  transcript.Config{MaxBytes: 8 << 20, MaxFiles: 3},
		GraceTimeout:   5 * time.Second,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = shim.Run(cfg)
	}()
	waitForSocket(t, cfg.SocketPath)
	waitForSocket(t, cfg.HookSocketPath)
	return hookShim{cfg: cfg, done: done}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket %s never appeared", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// kill sends the shim's control-socket SigKill directly over the wire (internal/wire +
// internal/shimwire only -- neither pulls in the pty/vt weight r6_hookdrain_test.go's own
// package doc explains staying clear of), then waits for shim.Run to return.
func (h hookShim) kill(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("unix", h.cfg.SocketPath, 3*time.Second)
	if err != nil {
		t.Fatalf("dial control socket to kill shim: %v", err)
	}
	defer func() { _ = conn.Close() }()
	hello, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
	if err := wire.WriteFrame(conn, wire.TControl, hello); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	if _, _, err := wire.ReadFrame(conn); err != nil {
		t.Fatalf("read hello reply: %v", err)
	}
	kill, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	if err := wire.WriteFrame(conn, wire.TControl, kill); err != nil {
		t.Fatalf("write kill signal: %v", err)
	}
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("shim did not exit within 10s of SigKill")
	}
}

// registerTestSession installs sessionID directly on sk's engine (bypassing Launch — see the
// package doc's scope note) with a live token, so hook posts bearing it authenticate exactly
// as a Launch-registered session's would (S6/G5).
func registerTestSession(sk *Daemon, sessionID, token string) {
	sk.eng.RegisterSession(sessionID, token, os.Getpid(), nil)
}

// scriptedCapture wires sk.adapterFor to a captureAdapter whose interactionScript hands out
// one distinctly-labelled agent_message per captured hook event, so drained records are
// verifiable by exact content and order, not merely by count.
func scriptedCapture(sk *Daemon, refs ...string) {
	batches := make([][]adapter.Interaction, len(refs))
	for i, ref := range refs {
		batches[i] = []adapter.Interaction{{
			Kind: adapter.KindAgentMessage, Status: adapter.StatusCompleted,
			Ref: ref, Text: ref, StopReason: "end_turn",
		}}
	}
	script := &interactionScript{items: batches}
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) {
		return &captureAdapter{Adapter: newPlainAdapter(), script: script}, true
	})
}

// postTurnEvent posts one authenticated, sequence-distinct hook event straight to the shim's
// hook socket (PostToShim, r6_shimpost_test.go), failing the test unless it is durably acked.
// The explicit turn payload gives the event a non-empty status dimension, which is what
// engages the engine's OWN cb.Sequence replay guard (engine.go HandleCallback) -- the safety
// net a redelivered-after-restart record relies on in TestHookDrainer_RestartMidDrain below.
func postTurnEvent(t *testing.T, hookSocketPath, sessionID, token string, seq uint64) {
	t.Helper()
	cb := engine.Callback{
		SessionID: sessionID, Token: token, Sequence: seq, Event: "Stop",
		Payload: map[string]string{"turn": "active"},
	}
	acked, err := hookclient.PostToShim(hookSocketPath, cb)
	if err != nil {
		t.Fatalf("post hook event seq=%d: %v", seq, err)
	}
	if !acked {
		t.Fatalf("hook event seq=%d was not acked by the shim's hook socket", seq)
	}
}

// awaitJournalTexts polls the journal (ADR-010 §7's append floor holds an item for a window
// before it appends, exactly why interaction_capture_test.go's awaitItems polls rather than
// reading once) until sessionID has want interaction items, then returns their `text` fields
// in cursor order.
func awaitJournalTexts(t *testing.T, sk *Daemon, sessionID string, want int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err := sk.Core().JournalReadFrom(0)
		if err != nil {
			t.Fatalf("JournalReadFrom: %v", err)
		}
		var refs []string
		for _, rec := range res.Events {
			if rec.Type != journal.TypeInteraction || rec.SessionID != sessionID {
				continue
			}
			var item map[string]any
			if err := json.Unmarshal(rec.Payload, &item); err != nil {
				t.Fatalf("decode interaction payload: %v", err)
			}
			if text, ok := item["text"].(string); ok {
				refs = append(refs, text)
			}
		}
		if len(refs) >= want {
			return refs
		}
		if time.Now().After(deadline) {
			t.Fatalf("journal holds %d interaction item(s) for %s after 10s, want >= %d", len(refs), sessionID, want)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// (2)+(3): daemon down (never drained), then restarted: every accepted item survives
// via spool replay -- the R6 exit's core.
// ---------------------------------------------------------------------------

func TestHookDrainer_DrainsEverythingThatAccumulatedWhileNeverDrained(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskhd1")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sessionDir, err := os.MkdirTemp("/tmp", "swskhds1")
	if err != nil {
		t.Fatalf("session dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sessionDir) })

	const sessionID, token = "s-r6-1", "tok-r6-1"
	h := startHookShim(t, sessionDir, sessionID)
	t.Cleanup(func() { h.kill(t) })

	const n = 5
	for i := uint64(1); i <= n; i++ {
		postTurnEvent(t, h.cfg.HookSocketPath, sessionID, token, i)
	}
	// Note: no daemon has been opened at all yet -- these posts succeeded against the shim
	// alone, exactly playbook 6.1's "daemon unavailability neither fails a provider hook".

	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, token)
	refs := make([]string, n)
	for i := range refs {
		refs[i] = "evt-" + string(rune('A'+i))
	}
	scriptedCapture(sk, refs...)

	cursorPath := filepath.Join(sessionDir, "hook.fold")
	hd := NewHookDrainer(sk, sessionID, h.cfg.HookSocketPath, cursorPath)
	applied, _, err := hd.DrainOnce()
	if err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if applied != n {
		t.Fatalf("DrainOnce applied %d record(s), want %d", applied, n)
	}
	if hd.cursor() != n {
		t.Fatalf("Cursor() = %d after a full drain, want %d", hd.cursor(), n)
	}

	got := awaitJournalTexts(t, sk, sessionID, n)
	if len(got) != n {
		t.Fatalf("journal holds %d interaction item(s) for %s, want %d — every accepted item must survive to a daemon that never drained before now", len(got), sessionID, n)
	}
	for i, want := range refs {
		if got[i] != want {
			t.Errorf("item %d = %q, want %q (drain must apply in spool order)", i, got[i], want)
		}
	}
}

// ---------------------------------------------------------------------------
// (2): restart the daemon MID-drain -- no duplicate, no loss, idempotent fold cursor.
// ---------------------------------------------------------------------------

func TestHookDrainer_RestartMidDrain_ResumesExactlyOnceWithNoDuplicateOrLoss(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskhd2")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sessionDir, err := os.MkdirTemp("/tmp", "swskhds2")
	if err != nil {
		t.Fatalf("session dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sessionDir) })
	cursorPath := filepath.Join(sessionDir, "hook.fold")

	const sessionID, token = "s-r6-2", "tok-r6-2"
	h := startHookShim(t, sessionDir, sessionID)
	t.Cleanup(func() { h.kill(t) })

	// First batch: three events, fully drained by the FIRST daemon incarnation.
	for i := uint64(1); i <= 3; i++ {
		postTurnEvent(t, h.cfg.HookSocketPath, sessionID, token, i)
	}
	sk1 := assembleAt(t, dir)
	registerTestSession(sk1, sessionID, token)
	scriptedCapture(sk1, "first-1", "first-2", "first-3")
	hd1 := NewHookDrainer(sk1, sessionID, h.cfg.HookSocketPath, cursorPath)
	applied1, _, err := hd1.DrainOnce()
	if err != nil {
		t.Fatalf("first incarnation DrainOnce: %v", err)
	}
	if applied1 != 3 {
		t.Fatalf("first incarnation applied %d, want 3", applied1)
	}

	// Second batch posted BEFORE the restart -- the backlog the restarted daemon must
	// resume into, proving the drain was genuinely left mid-sequence, not merely re-run
	// from empty.
	for i := uint64(4); i <= 6; i++ {
		postTurnEvent(t, h.cfg.HookSocketPath, sessionID, token, i)
	}
	if err := sk1.Close(); err != nil {
		t.Fatalf("close first daemon incarnation: %v", err)
	}

	sk2 := assembleAt(t, dir)
	registerTestSession(sk2, sessionID, token)
	scriptedCapture(sk2, "second-4", "second-5", "second-6")
	hd2 := NewHookDrainer(sk2, sessionID, h.cfg.HookSocketPath, cursorPath) // SAME cursorPath
	applied2, _, err := hd2.DrainOnce()
	if err != nil {
		t.Fatalf("second incarnation DrainOnce: %v", err)
	}
	if applied2 != 3 {
		t.Fatalf("second incarnation applied %d, want exactly the 3 records left over from before the restart (no loss, no re-delivery of the first batch)", applied2)
	}
	// Wait for the append-floor window to durably flush BEFORE closing sk2, so the journal
	// on disk really does hold all 6 by the time the third incarnation reads it.
	awaitJournalTexts(t, sk2, sessionID, 6)
	if err := sk2.Close(); err != nil {
		t.Fatalf("close second daemon incarnation: %v", err)
	}

	// Third incarnation: nothing new to drain. Idempotent no-op.
	sk3 := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk3.Close() })
	registerTestSession(sk3, sessionID, token)
	hd3 := NewHookDrainer(sk3, sessionID, h.cfg.HookSocketPath, cursorPath)
	applied3, _, err := hd3.DrainOnce()
	if err != nil {
		t.Fatalf("third incarnation DrainOnce: %v", err)
	}
	if applied3 != 0 {
		t.Fatalf("third incarnation (nothing new posted) applied %d, want 0 — the fold cursor must survive two restarts without re-requesting what was already folded", applied3)
	}

	got := awaitJournalTexts(t, sk3, sessionID, 6)
	if len(got) != 6 {
		t.Fatalf("journal holds %d interaction item(s) across three daemon incarnations, want exactly 6 — no duplicate, no loss", len(got))
	}
	want := []string{"first-1", "first-2", "first-3", "second-4", "second-5", "second-6"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("item %d = %q, want %q", i, got[i], w)
		}
	}
}

// ---------------------------------------------------------------------------
// (4): gap honesty, daemon side -- structured_gap emission and a one-way capability
// degrade, wired from a real drain that hits a real torn record.
// ---------------------------------------------------------------------------

func TestHookDrainer_OnAGap_EmitsStructuredGapAndDegradesCapabilityOneWay(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskhd3")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sessionDir, err := os.MkdirTemp("/tmp", "swskhds3")
	if err != nil {
		t.Fatalf("session dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sessionDir) })

	const sessionID, token = "s-r6-3", "tok-r6-3"
	h := startHookShim(t, sessionDir, sessionID)

	postTurnEvent(t, h.cfg.HookSocketPath, sessionID, token, 1)
	postTurnEvent(t, h.cfg.HookSocketPath, sessionID, token, 2)

	// Corrupt the spool's tail directly (the same technique internal/shim's own gap tests
	// use): stop the shim first so there is no concurrent writer, tear the last record,
	// restart a second in-process shim over the SAME session dir.
	h.kill(t)
	spoolPath := filepath.Join(sessionDir, shim.HookSpoolFile)
	spool, err := shim.OpenHookSpool(spoolPath, 0)
	if err != nil {
		t.Fatalf("open spool to seed a tear: %v", err)
	}
	before, err := os.Stat(spoolPath)
	if err != nil {
		t.Fatalf("stat spool before the torn append: %v", err)
	}
	if _, err := spool.Append([]byte(`{"session_id":"s-r6-3","token":"tok-r6-3","sequence":3,"event":"Stop","payload":{"turn":"active"}}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	after, err := os.Stat(spoolPath)
	if err != nil {
		t.Fatalf("stat spool after the torn append: %v", err)
	}
	_ = spool.Close()
	if err := os.Truncate(spoolPath, before.Size()+(after.Size()-before.Size())/2); err != nil {
		t.Fatalf("truncate spool to tear the last record: %v", err)
	}

	h2 := startHookShim(t, sessionDir, sessionID)
	t.Cleanup(func() { h2.kill(t) })

	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, token)
	scriptedCapture(sk, "gap-1", "gap-2")
	sk.registerSessionCapabilities(sessionID, protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.0.0", AdapterRevision: "rev",
		StructuredChat: true, TerminalFallback: false,
	})

	cursorPath := filepath.Join(sessionDir, "hook.fold")
	hd := NewHookDrainer(sk, sessionID, h2.cfg.HookSocketPath, cursorPath)
	applied, _, err := hd.DrainOnce()
	if !errors.Is(err, ErrHookDrainGap) {
		t.Fatalf("DrainOnce over a torn spool returned err=%v, want ErrHookDrainGap", err)
	}
	if applied != 2 {
		t.Fatalf("DrainOnce applied %d record(s) before the gap, want 2", applied)
	}

	res, err := sk.Core().JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}
	found := false
	for _, rec := range res.Events {
		if rec.Type == journal.TypeStructuredGap && rec.SessionID == sessionID {
			found = true
		}
	}
	if !found {
		t.Fatalf("no structured_gap journal record for %s after a proven spool tear", sessionID)
	}

	caps, ok := sk.sessionCapabilities(sessionID)
	if !ok {
		t.Fatalf("SessionCapabilities(%s) not found after a gap degrade", sessionID)
	}
	if caps.StructuredChat {
		t.Errorf("StructuredChat still true after a proven gap — T2 rule 2's degrade never fired")
	}
	if !caps.TerminalFallback {
		t.Errorf("TerminalFallback still false after a structured_chat degrade — ADR-017 T2 rule 2: a degrade forces the fallback surface on")
	}

	// One-way: a later re-registration (what a reconcile would do) must never resurrect it.
	sk.registerSessionCapabilities(sessionID, protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.0.0", AdapterRevision: "rev",
		StructuredChat: true, TerminalFallback: false,
	})
	caps2, ok := sk.sessionCapabilities(sessionID)
	if !ok {
		t.Fatalf("SessionCapabilities(%s) not found after re-registration", sessionID)
	}
	if caps2.StructuredChat {
		t.Fatalf("StructuredChat = true after re-registering a degraded session — the degrade must be one-way, even across a re-registration a daemon restart's reconcile would perform")
	}

	// R6 REVIEW FIX-PACK ROUND 1 (BLOCKER 1b): the re-registration above happens on the
	// SAME live *Daemon, so on its own it proves only the in-memory merge -- while the
	// sentence it claims to prove ("a daemon restart's reconcile") needs the degrade to
	// outlive the PROCESS that authored it. Close this incarnation and re-register on a
	// genuinely new one over the same state dir, which is what a restart is.
	if err := sk.Close(); err != nil {
		t.Fatalf("close the degraded daemon incarnation: %v", err)
	}
	sk2 := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk2.Close() })
	sk2.registerSessionCapabilities(sessionID, protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.0.0", AdapterRevision: "rev",
		StructuredChat: true, TerminalFallback: false,
	})
	caps3, ok := sk2.sessionCapabilities(sessionID)
	if !ok {
		t.Fatalf("SessionCapabilities(%s) not found on a restarted daemon", sessionID)
	}
	if caps3.StructuredChat {
		t.Fatalf("StructuredChat = true after a daemon RESTART re-registered the launch-time record — the degrade must be durable, not die with the incarnation that authored it (ADR-017 T2 rule 2)")
	}
	if !caps3.TerminalFallback {
		t.Fatalf("TerminalFallback = false after a daemon restart of a degraded session, want true")
	}
}
