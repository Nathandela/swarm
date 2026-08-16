package skeleton

// R6 REVIEW FIX-PACK ROUND 2 regression tests. Each function here pins one finding the
// round-2 reviewer PROVED with a throwaway probe against the round-1 implementation,
// as a permanent test rather than a script. See hookdrain.go / hookdrainloop.go /
// capability.go for the designs these hold in place.
//
//	R2-BLOCKER 1  the per-session dedupe was a MONOTONE high-water gate, so a
//	              legitimately distinct event arriving after a higher-sequenced sibling
//	              was dropped whole. engine.go's own doc (agents-tracker-707) states the
//	              premise that makes this wrong: "a sequence carries no causal order".
//	R2-BLOCKER 2  an item the shim durably ACKED was permanently lost at ORDINARY
//	              session end -- the shim's hookServer (the only reader of hooks.spool)
//	              shuts down with the agent, and stopHookDrain performed no final drain.
//	R2-MEDIUM 3   the durable structured-degraded marker bound the WRITE path only, so a
//	              capabilities.json left saying structured_chat=true (a write that failed
//	              between the marker and the record) resurrected structured chat on read.
//	R2-LOW 4      an attacker-controlled SessionID reached the filesystem (an arbitrary
//	              os.ReadFile via filepath.Join) and an unbounded map BEFORE any
//	              authentication had run.

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
)

// postRawEvent posts one authenticated capture=raw-shaped hook event (an UNMAPPED
// event: no turn/interaction payload key, so engine.HandleCallback accepts it as a
// benign no-op before its own dimension-scoped replay guard ever runs) straight to the
// shim's hook socket, failing the test unless it is durably acked.
func postRawEvent(t *testing.T, hookSocketPath, sessionID, token string, seq uint64) {
	t.Helper()
	cb := engine.Callback{SessionID: sessionID, Token: token, Sequence: seq, Event: "PostToolUse"}
	acked, err := hookclient.PostToShim(hookSocketPath, cb)
	if err != nil {
		t.Fatalf("post hook event seq=%d: %v", seq, err)
	}
	if !acked {
		t.Fatalf("hook event seq=%d was not acked by the shim's hook socket", seq)
	}
}

// ---------------------------------------------------------------------------
// R2-BLOCKER 1: two DISTINCT, durably-acked events whose sequences arrive out of order
// must BOTH be applied. internal/engine's own header states the premise: sequences are
// handed out by racing `swarm hook` processes contending on a flock and released BEFORE
// the POST, so arrival order and sequence order are independent -- which is exactly why
// the engine's own anti-replay guard is DIMENSION-scoped rather than session-scoped.
// A per-session monotone high-water gate drops the lower-sequenced sibling whole, and a
// capture=raw body is the only place tool_input/tool_response ever exist.
// ---------------------------------------------------------------------------

func TestHookDrainer_TwoDistinctEventsSpooledOutOfSequenceOrder_BothShapeJournalItems(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskr2a")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sessionDir, err := os.MkdirTemp("/tmp", "swskr2as")
	if err != nil {
		t.Fatalf("session dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sessionDir) })

	const sessionID, token = "s-r2-a", "tok-r2-a"
	h := startHookShim(t, sessionDir, sessionID)
	t.Cleanup(func() { h.kill(t) })

	// The engine's documented race, materialized: the HIGHER sequence lands first.
	postRawEvent(t, h.cfg.HookSocketPath, sessionID, token, 2)
	postRawEvent(t, h.cfg.HookSocketPath, sessionID, token, 1)

	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, token)
	scriptedCapture(sk, "r2a-first", "r2a-second")

	hd := NewHookDrainer(sk, sessionID, h.cfg.HookSocketPath, filepath.Join(sessionDir, "hook.fold"))
	applied, skipped, err := hd.DrainOnce()
	if err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if applied != 2 || skipped != 0 {
		t.Fatalf("DrainOnce applied=%d skipped=%d over two DISTINCT acked events whose sequences arrived out of order, want applied=2 skipped=0 -- a sequence carries no causal order (internal/engine, agents-tracker-707), so a per-session monotone high-water gate silently drops the lower-sequenced sibling", applied, skipped)
	}
	if texts := awaitJournalTexts(t, sk, sessionID, 2); len(texts) != 2 {
		t.Fatalf("journal holds %v for %s, want two items -- every durably-acked event shapes its own item (playbook 6.1: neither fails a provider hook nor loses an accepted item)", texts, sessionID)
	}
}

func TestServeHook_LowerSequencedSiblingAfterAStop_IsStillIngestedOnTheLivePath(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskr2b")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	const sessionID, token = "s-r2-b", "tok-r2-b"
	registerTestSession(sk, sessionID, token)
	scriptedCapture(sk, "from-stop", "from-posttooluse")

	sock := filepath.Join(dir, "d.sock")
	// The exact shape internal/engine documents: a Stop closes the turn at seq=2 while
	// the PostToolUse of a tool that finished around the same instant took seq=1 and
	// arrives afterwards.
	postCallbackToDaemon(t, sock, engine.Callback{
		SessionID: sessionID, Token: token, Sequence: 2, Event: "Stop",
		Payload: map[string]string{engine.PayloadKeyTurn: "idle"},
	})
	awaitJournalTexts(t, sk, sessionID, 1)
	postCallbackToDaemon(t, sock, engine.Callback{
		SessionID: sessionID, Token: token, Sequence: 1, Event: "PostToolUse",
	})

	texts := awaitJournalTexts(t, sk, sessionID, 2)
	if len(texts) != 2 || texts[0] != "from-stop" || texts[1] != "from-posttooluse" {
		t.Fatalf("journal texts = %v, want [from-stop from-posttooluse] -- the live hook path must not drop a lower-sequenced capture=raw event that arrived after a higher-sequenced sibling", texts)
	}
}

// postCallbackToDaemon writes one raw hook callback to the daemon's own socket and
// closes, exactly like hookclient.Post does.
func postCallbackToDaemon(t *testing.T, sock string, cb engine.Callback) {
	t.Helper()
	body, err := json.Marshal(cb)
	if err != nil {
		t.Fatalf("marshal callback: %v", err)
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial daemon socket: %v", err)
	}
	if _, err := conn.Write(body); err != nil {
		t.Fatalf("write callback: %v", err)
	}
	_ = conn.Close()
}

// ---------------------------------------------------------------------------
// R2-BLOCKER 2: an item the shim durably ACKED must not be lost when the session ends.
// The shim's hookServer is the ONLY reader of hooks.spool over the socket, and it is
// shut down when the agent is reaped -- so every hook acked inside the last drain
// interval was unreachable forever while its bytes sat on disk. The spool file lives in
// the session's own 0700 dir and its format is self-describing, so the daemon reads it
// directly rather than staying hostage to a live socket.
// ---------------------------------------------------------------------------

func TestHookDrainer_AppliesAckedRecordsFromTheSpoolFileAfterTheShimIsGone(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskr2c")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sessionDir, err := os.MkdirTemp("/tmp", "swskr2cs")
	if err != nil {
		t.Fatalf("session dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sessionDir) })

	const sessionID, token = "s-r2-c", "tok-r2-c"
	h := startHookShim(t, sessionDir, sessionID)
	postRawEvent(t, h.cfg.HookSocketPath, sessionID, token, 1)
	h.kill(t) // the agent is reaped: hookServer shuts down, the socket is gone

	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, token)
	scriptedCapture(sk, "r2c-recovered")

	cursorPath := filepath.Join(sessionDir, "hook.fold")
	hd := NewHookDrainer(sk, sessionID, h.cfg.HookSocketPath, cursorPath)
	hd.SetSpoolPath(filepath.Join(sessionDir, shim.HookSpoolFile))

	if _, _, err := hd.DrainOnce(); err == nil {
		t.Fatalf("DrainOnce succeeded against a dead shim's socket; the premise of this test is that the socket is gone")
	}
	applied, skipped, err := hd.drainFromSpoolFile()
	if err != nil {
		t.Fatalf("drainFromSpoolFile: %v", err)
	}
	if applied != 1 || skipped != 0 {
		t.Fatalf("drainFromSpoolFile applied=%d skipped=%d, want applied=1 skipped=0 -- a durably acked record must be recoverable straight from hooks.spool once its shim is gone", applied, skipped)
	}
	if got := awaitJournalTexts(t, sk, sessionID, 1); got[0] != "r2c-recovered" {
		t.Fatalf("journal texts = %v, want [r2c-recovered]", got)
	}
	if hd.cursor() != 1 {
		t.Fatalf("Cursor() = %d after a disk drain, want 1 -- the disk path must persist the cursor exactly like the socket path", hd.cursor())
	}
	// Idempotent: a second disk drain over the same cursor applies nothing.
	applied2, _, err := hd.drainFromSpoolFile()
	if err != nil {
		t.Fatalf("second drainFromSpoolFile: %v", err)
	}
	if applied2 != 0 {
		t.Fatalf("second drainFromSpoolFile applied=%d, want 0", applied2)
	}
}

func TestStopHookDrain_FinalDrainRecoversAnItemAckedInTheLastInterval(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskr2d")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	const sessionID, token = "s-r2-d", "tok-r2-d"
	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })

	// The session's own state dir, exactly where the production launch path puts it --
	// SessionHookChannel recovers the channel from the 0600 shim-launch.json here.
	sessionDir := filepath.Join(dir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("session dir: %v", err)
	}
	h := startHookShim(t, sessionDir, sessionID)
	lc := map[string]any{"session_id": sessionID, "hook_socket_path": h.cfg.HookSocketPath}
	data, err := json.Marshal(lc)
	if err != nil {
		t.Fatalf("marshal launch config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "shim-launch.json"), data, 0o600); err != nil {
		t.Fatalf("write launch config: %v", err)
	}

	registerTestSession(sk, sessionID, token)
	scriptedCapture(sk, "r2d-last-interval")

	// The event is acked, and then the agent is reaped BEFORE any drain tick could have
	// carried it -- the ordinary end of a headless `claude -p` run, or a Ctrl-C right
	// after a turn. The loop below therefore has no live socket at all, so the item can
	// only reach the journal through the final drain stopHookDrain performs.
	postRawEvent(t, h.cfg.HookSocketPath, sessionID, token, 1)
	h.kill(t)

	sk.startHookDrain(sessionID)
	sk.stopHookDrain(sessionID)

	texts := awaitJournalTexts(t, sk, sessionID, 1)
	if texts[0] != "r2d-last-interval" {
		t.Fatalf("journal texts = %v, want [r2d-last-interval] -- an item the shim durably ACKED must survive ordinary session end", texts)
	}
}

// TestLaunchedSession_HookAckedJustBeforeTheAgentIsReaped_SurvivesSessionEnd is the same
// finding through the PRODUCTION path the reviewer's probe used -- a real Launch, a real
// spawned shim, the daemon's OWN drain loop, and a real Kill -- rather than a
// test-constructed drainer over a hand-written launch config. It is what proves the fix
// where the loss was actually measured (5/5 trials lost the final event): that endSession
// fires stopHookDrain at all, that SessionHookChannel's SpoolPath names the file the shim
// really wrote, and that the record still authenticates against a live engine session
// because the final drain runs BEFORE d.eng.EndSession.
func TestLaunchedSession_HookAckedJustBeforeTheAgentIsReaped_SurvivesSessionEnd(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskr2i")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	scriptedCapture(sk, "r2i-warm", "r2i-final")
	m := launchFake(t, sk, "idle 600s\n")

	ch, ok := sk.Core().SessionHookChannel(m.ID)
	if !ok || ch.SocketPath == "" {
		t.Fatalf("SessionHookChannel(%s) = (%+v, %v); a launched session must carry a shim-owned hook socket", m.ID, ch, ok)
	}
	waitForSocket(t, ch.SocketPath)
	token := envValue(launchEnv(t, dir, m.ID), hookclient.EnvToken)

	// Warm the channel and CONFIRM the daemon's own loop is carrying records, so the
	// second event below is measured against a channel that demonstrably worked.
	postRawEvent(t, ch.SocketPath, m.ID, token, 1)
	awaitJournalTexts(t, sk, m.ID, 1)

	// The last hook of the session: durably acked by the shim, and then the agent is
	// reaped before any 250ms tick could have carried it. This is the ordinary end of a
	// headless run, not a narrow race.
	postRawEvent(t, ch.SocketPath, m.ID, token, 2)
	if err := sk.Core().Kill(m.ID); err != nil {
		t.Fatalf("kill the session: %v", err)
	}

	texts := awaitJournalTexts(t, sk, m.ID, 2)
	if len(texts) != 2 || texts[1] != "r2i-final" {
		t.Fatalf("journal texts = %v, want the final acked hook to have survived session end -- its bytes are durable in hooks.spool, and playbook 6.1 forbids losing an accepted item", texts)
	}
}

// ---------------------------------------------------------------------------
// R2-MEDIUM 3: the durable structured-degraded marker is the PROOF of the gap, and
// nothing may read past it. markSessionDegraded writes the marker first and the
// degraded record second, logging rather than failing if the second write fails
// (ENOSPC -- precisely the disk-full case playbook 6.1 names), so a marker beside a
// capabilities.json still claiming structured_chat=true is reachable.
// ---------------------------------------------------------------------------

func TestSessionCapabilities_NeverReadsPastTheDurableDegradedMarker(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskr2e")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	const sessionID = "s-r2-e"
	sessionDir := filepath.Join(dir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("session dir: %v", err)
	}
	rec, err := json.Marshal(protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.0.0", AdapterRevision: "rev",
		StructuredChat: true, TerminalFallback: false,
	})
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, sessionCapabilityFile), rec, 0o600); err != nil {
		t.Fatalf("write capability record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, sessionDegradedFile), nil, 0o600); err != nil {
		t.Fatalf("write degraded marker: %v", err)
	}

	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })

	caps, ok := sk.sessionCapabilities(sessionID)
	if !ok {
		t.Fatalf("SessionCapabilities(%s) not found", sessionID)
	}
	if caps.StructuredChat {
		t.Fatalf("StructuredChat = true for a session carrying the durable structured-degraded marker -- ADR-017 T2 rule 2 inverted on the READ path: the marker is the proof, and nothing may read past it")
	}
	if !caps.TerminalFallback {
		t.Fatalf("TerminalFallback = false for a degraded session, want true")
	}
	// The same must hold on the SECOND call, which is served from the in-memory cache
	// the first one populated.
	caps2, _ := sk.sessionCapabilities(sessionID)
	if caps2.StructuredChat || !caps2.TerminalFallback {
		t.Fatalf("cached read: StructuredChat=%v TerminalFallback=%v, want false/true", caps2.StructuredChat, caps2.TerminalFallback)
	}
}

// ---------------------------------------------------------------------------
// R2-LOW 4: the dedupe runs on a body decoded off an explicitly UNAUTHENTICATED spool
// POST, ahead of engine.HandleCallback. Its session id must therefore never reach
// filepath.Join (an arbitrary os.ReadFile outside the state dir), and must never grow
// an unbounded in-memory map.
// ---------------------------------------------------------------------------

func TestIngestHookBytes_UnauthenticatedSessionIDNeverReachesTheFilesystemOrTheSeenSet(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskr2f")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })

	for _, id := range []string{"../../../etc/passwd", "..", ".", "a/b", "", string([]byte{'x', 0x00, 'y'})} {
		if p := sk.hookSeqPath(id); p != "" {
			t.Fatalf("hookSeqPath(%q) = %q, want \"\" -- an unauthenticated session id must never reach filepath.Join", id, p)
		}
	}

	for i, id := range []string{"../../../etc/passwd", "ghost-1", "ghost-2", "ghost-3"} {
		raw, err := json.Marshal(engine.Callback{SessionID: id, Token: "not-a-real-token", Sequence: uint64(i + 1), Event: "PostToolUse"})
		if err != nil {
			t.Fatalf("marshal callback: %v", err)
		}
		if err := sk.ingestHookBytes(raw); err == nil {
			t.Fatalf("ingestHookBytes accepted an unauthenticated callback for session %q", id)
		}
	}

	sk.itemMu.Lock()
	n := len(sk.hookSeq)
	sk.itemMu.Unlock()
	if n != 0 {
		t.Fatalf("the ingest seen-set holds %d entry/entries after 4 UNAUTHENTICATED posts, want 0 -- an attacker-controlled session id must not grow an unbounded map before authentication", n)
	}
}

// ---------------------------------------------------------------------------
// The bounded seen-set's own contract, unit-level: a sequence below the mark that was
// never actually ingested is applied rather than assumed stale, and one that WAS
// ingested is refused -- durably, across a genuinely new Daemon incarnation.
// ---------------------------------------------------------------------------

func TestHookSeenSet_IsAMembershipTestNotAHighWaterGate_AndSurvivesARestart(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskr2g")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	const sessionID = "s-r2-g"
	sk := assembleAt(t, dir)
	sk.markHookSeqIngested(sessionID, 7)

	if !sk.hookSeqDuplicate(sessionID, 7) {
		t.Fatalf("seq 7 is not reported as already ingested right after it was marked")
	}
	if sk.hookSeqDuplicate(sessionID, 3) {
		t.Fatalf("seq 3 reported as already ingested merely because 7 was -- the guard must be a MEMBERSHIP test, not a monotone high-water gate: a sequence carries no causal order")
	}
	sk.markHookSeqIngested(sessionID, 3)
	if !sk.hookSeqDuplicate(sessionID, 3) {
		t.Fatalf("seq 3 is not reported as already ingested after it was marked")
	}
	if err := sk.Close(); err != nil {
		t.Fatalf("close first incarnation: %v", err)
	}

	sk2 := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk2.Close() })
	if !sk2.hookSeqDuplicate(sessionID, 7) || !sk2.hookSeqDuplicate(sessionID, 3) {
		t.Fatalf("a genuinely NEW daemon incarnation forgot which sequences were ingested; the seen-set must be durable")
	}
	if sk2.hookSeqDuplicate(sessionID, 5) {
		t.Fatalf("seq 5 -- never ingested, and BELOW the highest mark -- reported as a duplicate on a fresh incarnation")
	}
	if sk2.hookSeqDuplicate(sessionID, 8) {
		t.Fatalf("seq 8 -- never ingested -- reported as a duplicate on a fresh incarnation")
	}
}

// The seen-set stays bounded: a session that never contiguously fills its gaps must not
// grow an unbounded on-disk record. Beyond the bound the floor advances (documented in
// hookdrain.go as the one place the honest membership test degrades back to a
// high-water assumption) rather than the file growing without limit.
func TestHookSeenSet_StaysBounded(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskr2h")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	const sessionID = "s-r2-h"
	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })

	// Only even sequences: nothing is ever contiguous, so the floor can only advance
	// through the bound itself.
	const last = uint64(800)
	for seq := uint64(2); seq <= last; seq += 2 {
		sk.markHookSeqIngested(sessionID, seq)
	}
	info, err := os.Stat(sk.hookSeqPath(sessionID))
	if err != nil {
		t.Fatalf("stat seen-set: %v", err)
	}
	if info.Size() > 1<<20 {
		t.Fatalf("the durable seen-set is %d bytes, want a bounded file", info.Size())
	}
	// The most recent marks are still remembered exactly.
	if !sk.hookSeqDuplicate(sessionID, last) {
		t.Fatalf("the most recent mark (%d) was evicted from the seen-set", last)
	}
	if sk.hookSeqDuplicate(sessionID, last-1) {
		t.Fatalf("an odd sequence just below the most recent mark reads as ingested; eviction must not swallow the live window")
	}
}
