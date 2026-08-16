package skeleton

// R6 REVIEW FIX-PACK ROUND 1 (FAILING-FIRST, TDD RED, GG-5). One function per reviewer
// finding, written before the fix; the failing runs are captured in
// docs/verification/r6-red/fixpack-red.txt.
//
// THE SEAMS THIS FILE PINS (undefined symbols -> compile-fail RED):
//
//	// hookdrainloop.go (new), HIGH 3: nothing constructed a HookDrainer outside tests,
//	// so EmitStructuredGap could never fire in production and `swarm hook` always took
//	// the daemon fallback. The assembly now starts one drain loop per session carrying a
//	// hook channel, off the same OnSessionStart seam that registers the session with the
//	// engine (fresh launch AND reconcile-after-restart).
//
//	// internal/daemon, HIGH 3/4: the launch path mints the channel and persists it beside
//	// the session's other launch-time secrets (the 0600 shim-launch.json reconcile
//	// already re-reads for the hook POST token).
//	type daemon.HookChannel struct{ SocketPath, DrainToken, CursorPath string }
//	func (d *daemon.Daemon) SessionHookChannel(id string) (HookChannel, bool)

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

// ---------------------------------------------------------------------------
// MEDIUM 8: a SECOND, genuinely DIFFERENT boundary must still be emitted and still
// degrade. The persisted-boundary check is the correct dedupe; ANDing it with a
// process-local "have I ever reported one" latch strictly loses information.
// ---------------------------------------------------------------------------

func TestHookDrainer_ASecondDifferentBoundaryIsStillReported(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskfp1")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sessionDir, err := os.MkdirTemp("/tmp", "swskfps1")
	if err != nil {
		t.Fatalf("session dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sessionDir) })

	const sessionID, token = "s-fp-1", "tok-fp-1"
	spoolPath := filepath.Join(sessionDir, shim.HookSpoolFile)
	cursorPath := filepath.Join(sessionDir, "hook.fold")

	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	registerTestSession(sk, sessionID, token)
	scriptedCapture(sk, "fp1-a", "fp1-b")
	sk.registerSessionCapabilities(sessionID, protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.0.0", AdapterRevision: "rev",
		StructuredChat: true, TerminalFallback: false,
	})

	// FIRST boundary: a spool whose very first record was torn -- boundary 1.
	tearSpoolTail(t, spoolPath)
	h1 := startHookShim(t, sessionDir, sessionID)
	hd := NewHookDrainer(sk, sessionID, h1.cfg.HookSocketPath, cursorPath)
	if _, _, err := hd.DrainOnce(); err != ErrHookDrainGap {
		t.Fatalf("first drain: err=%v, want ErrHookDrainGap", err)
	}
	if n := countJournalStructuredGaps(t, sk, sessionID); n != 1 {
		t.Fatalf("journal holds %d structured_gap record(s) after the first proven boundary, want 1", n)
	}
	h1.kill(t)

	// The shim restarts over a WIPED spool (the corrupt-spool recovery playbook §6.1
	// itself contemplates): a genuinely FRESH sequence space, so the next tear names a
	// different, never-reported boundary -- 3, not 1.
	if err := os.Remove(spoolPath); err != nil {
		t.Fatalf("wipe spool: %v", err)
	}
	_ = os.Remove(spoolPath + ".floor")

	h2 := startHookShim(t, sessionDir, sessionID)
	postTurnEvent(t, h2.cfg.HookSocketPath, sessionID, token, 1)
	postTurnEvent(t, h2.cfg.HookSocketPath, sessionID, token, 2)
	h2.kill(t)
	tearSpoolTail(t, spoolPath)

	h3 := startHookShim(t, sessionDir, sessionID)
	t.Cleanup(func() { h3.kill(t) })

	// The SAME drainer instance in the SAME process (startHookShim rebinds the same
	// per-session socket path, so hd needs no re-pointing): exactly the process-local
	// latch under test.
	applied, _, err := hd.DrainOnce()
	if err != ErrHookDrainGap {
		t.Fatalf("second drain: err=%v, want ErrHookDrainGap", err)
	}
	if applied != 2 {
		t.Fatalf("second drain applied %d record(s) below the new boundary, want 2", applied)
	}
	if n := countJournalStructuredGaps(t, sk, sessionID); n != 2 {
		t.Fatalf("journal holds %d structured_gap record(s) after TWO genuinely different proven boundaries, want 2 -- a process-local first-report latch swallows every boundary after the first", n)
	}
}

// tearSpoolTail appends one record to the spool at path and truncates the file so the
// record's own bytes are cut in half: a torn tail, exactly what a crash mid-Append
// leaves. The spool is opened and closed inside, so no live writer is involved.
func tearSpoolTail(t *testing.T, path string) {
	t.Helper()
	spool, err := shim.OpenHookSpool(path, 0)
	if err != nil {
		t.Fatalf("open spool to seed a tear: %v", err)
	}
	var before int64
	if fi, serr := os.Stat(path); serr == nil {
		before = fi.Size()
	}
	if _, err := spool.Append([]byte(`{"session_id":"x","token":"y","sequence":99,"event":"Stop","payload":{"turn":"active"}}`)); err != nil {
		t.Fatalf("Append the record to tear: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after the torn append: %v", err)
	}
	_ = spool.Close()
	if err := os.Truncate(path, before+(after.Size()-before)/2); err != nil {
		t.Fatalf("truncate to tear the last record: %v", err)
	}
}

// ---------------------------------------------------------------------------
// LOW 10: the daemon-side fold cursor and its gap-boundary sidecar carry a session's
// drain position; both land 0600 today only because os.CreateTemp does, and nothing
// pinned it.
// ---------------------------------------------------------------------------

func TestPersistHookCursor_IsOwnerOnly0600(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"hook.fold", "hook.fold.gap"} {
		path := filepath.Join(dir, name)
		if err := persistHookCursor(path, 7); err != nil {
			t.Fatalf("persistHookCursor(%s): %v", name, err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, want 0600", name, fi.Mode().Perm())
		}
	}
}

// ---------------------------------------------------------------------------
// HIGH 3 + HIGH 4: the channel, END TO END, through the PRODUCTION launch path. No
// test-constructed HookDrainer and no in-process shim: a real Launch, a real spawned
// shim, a hook posted to that shim's own socket, and the daemon's OWN drain loop
// carrying it into the engine.
// ---------------------------------------------------------------------------

func TestLaunchedSession_HookPostedToTheShimSocketReachesTheDaemon(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskfp2")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	m := launchFake(t, sk, "idle 600s\n")

	ch, ok := sk.Core().SessionHookChannel(m.ID)
	if !ok || ch.SocketPath == "" {
		t.Fatalf("SessionHookChannel(%s) = (%+v, %v); a launched session must carry a shim-owned hook socket", m.ID, ch, ok)
	}
	if ch.DrainToken == "" {
		t.Fatalf("SessionHookChannel(%s).DrainToken is empty: the DRAIN gate never runs, so a same-user process can fold away or read the whole spool", m.ID)
	}
	waitForSocket(t, ch.SocketPath)

	env := launchEnv(t, dir, m.ID)
	if got := envValue(env, hookclient.EnvHookSocket); got != ch.SocketPath {
		t.Fatalf("agent env %s = %q, want the session's hook socket %q -- without it `swarm hook` keeps taking the daemon fallback and the spool boundary is bypassed in production", hookclient.EnvHookSocket, got, ch.SocketPath)
	}
	for _, kv := range env {
		if strings.Contains(kv, ch.DrainToken) {
			t.Fatalf("the DRAIN token reached the agent's own environment (%q): the agent is the least-trusted party here, and DRAIN is destructive and read-everything", kv)
		}
	}

	postTurnEvent(t, ch.SocketPath, m.ID, envValue(env, hookclient.EnvToken), 1)

	// Nothing in this test drains. If the assembly runs no drain loop of its own, the
	// event stays in the spool forever and the session's turn never moves.
	deadline := time.Now().Add(15 * time.Second)
	for {
		got, ok := sk.Core().Get(m.ID)
		if ok && got.Status.Turn == status.TurnActive {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a hook durably accepted by the session's shim never reached the daemon: the production assembly constructs no HookDrainer, so the structured channel is inert (playbook 6.1)")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestLaunchedSession_DrainWithoutTheMintedTokenIsRefused(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "swskfp3")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sk := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk.Close() })
	m := launchFake(t, sk, "idle 600s\n")

	ch, ok := sk.Core().SessionHookChannel(m.ID)
	if !ok || ch.SocketPath == "" {
		t.Fatalf("SessionHookChannel(%s) = (%+v, %v)", m.ID, ch, ok)
	}
	waitForSocket(t, ch.SocketPath)

	// A same-user process that can reach the socket but does not hold the daemon's
	// per-session drain secret. DRAIN is read-everything (every spooled hook body,
	// including the session's own POST token) and destructive (FoldSeq compacts on the
	// caller's say-so), so it must answer nothing at all.
	conn, err := net.DialTimeout("unix", ch.SocketPath, 3*time.Second)
	if err != nil {
		t.Fatalf("dial the session's hook socket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(`D{"from_seq":0,"fold_seq":0}`)); err != nil {
		t.Fatalf("write an unauthenticated DRAIN: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var buf [1]byte
	if n, rerr := conn.Read(buf[:]); rerr == nil && n > 0 {
		t.Fatalf("an unauthenticated DRAIN got %d byte(s) of response; want none -- the minted per-session drain token must gate the verb", n)
	}
}

// launchEnv reads back the agent environment the daemon injected at spawn, out of the
// 0600 shim-launch.json (the same file reconcile re-reads the hook token from).
func launchEnv(t *testing.T, stateDir, id string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, id, "shim-launch.json"))
	if err != nil {
		t.Fatalf("read the session's launch config: %v", err)
	}
	var lc struct {
		Env []string `json:"env"`
	}
	if err := json.Unmarshal(data, &lc); err != nil {
		t.Fatalf("decode the session's launch config: %v", err)
	}
	return lc.Env
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}
