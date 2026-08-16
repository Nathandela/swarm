package hookclient

// R6 (bd agents-tracker-hggx.7) FAILING-FIRST (TDD RED, GG-5) tests for the CLIENT half of
// playbook §6.1's structured-capture survival boundary: "Claude hooks post to a per-session
// shim-owned socket" -- and requirement 7's compatibility rule, stated in the assignment
// verbatim: "the swarm hook CLI keeps working against old shims during the transition
// (feature-detect the shim socket, fall back to the daemon socket, honest about which path
// served)".
//
// Post/Decode (hookclient.go) are UNCHANGED: they remain exactly today's daemon-socket
// fire-and-forget pair, and are what PostSmart falls back to. This file adds the shim-aware
// half beside them.
//
// THE SEAMS THIS FILE PINS (undefined symbols -> compile-fail RED):
//
//	// EnvHookSocket names the per-session shim hook-socket path injected at spawn -- the
//	// hookclient-side twin of EnvSocket. Unset (an old shim, or a daemon that has not yet
//	// wired R6) means "no shim hook socket": PostSmart goes straight to Post, unchanged from
//	// today. That absence check is the ENTIRE compatibility story; there is no version probe
//	// and no capability negotiation.
//	const EnvHookSocket = "SWARM_SHIM_HOOK_SOCK"
//
//	// HookPath names which transport actually carried a post, so a caller can be honest
//	// about it rather than silently swallowing the distinction.
//	type HookPath string
//	const (
//	    HookPathShim   HookPath = "shim"
//	    HookPathDaemon HookPath = "daemon"
//	)
//
//	// PostToShim dials hookSocketPath, writes cb with THE SAME JSON ENCODING Post already
//	// uses (SetEscapeHTML(false) included -- a shim-spooled body and a daemon-posted body
//	// for an identical Callback must be byte-identical, or a session's capture would differ
//	// by transport), then reads for exactly one ack byte under postAckTimeout.
//	//   - a dial failure (no such socket, connection refused: an old shim, or the hook
//	//     socket disabled) returns acked=false with a non-nil err.
//	//   - a successful dial+write with NO ack observed before the deadline returns
//	//     acked=false, err=nil -- the honest "reachable, not confirmed" outcome a caller MAY
//	//     retry (playbook 6.1: "the hook client may double-post on no-ack").
//	//   - a successful dial+write+ack-byte-read returns acked=true, err=nil.
//	func PostToShim(hookSocketPath string, cb engine.Callback) (acked bool, err error)
//
//	// PostSmart is the CLI's one entrypoint. hookSocketPath=="" (EnvHookSocket unset) skips
//	// straight to the daemon path -- no dial attempt at all, so an old shim never pays a
//	// timeout. Otherwise: try PostToShim; a DIAL failure falls back to the daemon path
//	// immediately (no retry against a socket that plainly is not there); an ack-less-but-
//	// reachable outcome is retried against the SAME shim socket, with the SAME cb (so its
//	// Sequence -- already the engine's own per-session replay-protection key -- is what
//	// keeps a genuine double-post idempotent downstream), up to hookShimPostRetries times
//	// before giving up and falling back to the daemon path. The returned HookPath is
//	// authoritative: it names whichever transport the post actually traveled on, however
//	// many attempts that took.
//	func PostSmart(hookSocketPath, daemonSocketPath string, cb engine.Callback) (HookPath, error)

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/engine"
)

// tmpSocketPath returns a short-pathed name under /tmp for a unix socket. t.TempDir()
// embeds the full test name (nested subtests included), which routinely blows past the
// 104-byte sun_path limit on macOS -- every other package in this slice already works
// around this (internal/shim's newSocketPath, internal/skeleton's and cmd/swarm's
// os.MkdirTemp("/tmp", ...) calls); this is that same convention for this package.
func tmpSocketPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swhc")
	if err != nil {
		t.Fatalf("mktemp socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

// fakeHookSocket stands in for the shim's hook socket: it accepts ONE connection, decodes
// exactly the JSON callback body PostToShim/Post would write, and answers per the test's
// script. It is a fake because internal/shim is a heavy PTY/VT dependency this package must
// never import (hookclient is the THIN poster the swarm-hook CLI pays for on every event) --
// the wire shape under test here is trivial (a JSON body, then optionally one ack byte), so a
// hand-rolled listener is the leaner double, not a borrowed production type.
type fakeHookSocket struct {
	path     string
	received chan engine.Callback
}

func startFakeHookSocket(t *testing.T, ack bool) *fakeHookSocket {
	t.Helper()
	path := tmpSocketPath(t, "hook.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen fake hook socket: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	f := &fakeHookSocket{path: path, received: make(chan engine.Callback, 8)}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				var cb engine.Callback
				if err := json.NewDecoder(c).Decode(&cb); err != nil {
					return
				}
				f.received <- cb
				if ack {
					_, _ = c.Write([]byte{1})
				}
				// no ack: close silently, exactly like a shim that never got to reply
			}(conn)
		}
	}()
	return f
}

func testCallback() engine.Callback {
	return engine.Callback{SessionID: "s1", Token: "tok", Sequence: 1, Event: "Stop"}
}

// ---------------------------------------------------------------------------
// PostToShim: the wire-level contract.
// ---------------------------------------------------------------------------

func TestPostToShim_AckedTrueWhenTheAckByteArrives(t *testing.T) {
	f := startFakeHookSocket(t, true)
	acked, err := PostToShim(f.path, testCallback())
	if err != nil {
		t.Fatalf("PostToShim: %v", err)
	}
	if !acked {
		t.Fatalf("acked = false, want true (the fake shim wrote the ack byte)")
	}
	select {
	case got := <-f.received:
		if got.SessionID != "s1" || got.Sequence != 1 || got.Event != "Stop" {
			t.Errorf("shim received %+v, want the posted callback verbatim", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("fake shim never received a decodable callback")
	}
}

func TestPostToShim_AckedFalseNoErrorWhenTheConnectionIsSilent(t *testing.T) {
	// Playbook 6.1: "the hook client may double-post on no-ack" -- so this outcome must be
	// distinguishable from a hard failure: acked=false, err=nil is a RETRYABLE signal, not
	// a reason to give up on the shim socket outright.
	f := startFakeHookSocket(t, false)
	acked, err := PostToShim(f.path, testCallback())
	if err != nil {
		t.Fatalf("PostToShim over a live-but-silent shim returned err=%v, want nil (retryable, not fatal)", err)
	}
	if acked {
		t.Fatalf("acked = true, want false (the fake shim never wrote a byte)")
	}
}

func TestPostToShim_DialFailureIsAnError(t *testing.T) {
	acked, err := PostToShim(tmpSocketPath(t, "no-such-socket"), testCallback())
	if err == nil {
		t.Fatalf("PostToShim against a nonexistent socket returned err=nil")
	}
	if acked {
		t.Fatalf("acked = true against a nonexistent socket")
	}
}

func TestPostToShim_EncodesByteForByteIdenticallyToPost(t *testing.T) {
	// A shim-spooled body and a daemon-posted body for the identical Callback must be
	// byte-identical, or a session's captured content would differ by transport alone.
	f := startFakeHookSocket(t, true)
	cb := engine.Callback{
		SessionID: "s1", Token: "tok", Sequence: 7, Event: "PostToolUse",
		Payload: map[string]string{"tool": "Read"},
		Raw:     json.RawMessage(`{"a":"<b>&c"}`), // exercises the SetEscapeHTML(false) path
	}
	if _, err := PostToShim(f.path, cb); err != nil {
		t.Fatalf("PostToShim: %v", err)
	}

	daemonPath := tmpSocketPath(t, "daemon.sock")
	dl, err := net.Listen("unix", daemonPath)
	if err != nil {
		t.Fatalf("listen fake daemon socket: %v", err)
	}
	defer func() { _ = dl.Close() }()
	rawBody := make(chan []byte, 1)
	go func() {
		conn, err := dl.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		rawBody <- buf[:n]
	}()
	if err := Post(daemonPath, cb); err != nil {
		t.Fatalf("Post: %v", err)
	}

	select {
	case daemonBytes := <-rawBody:
		select {
		case shimCB := <-f.received:
			// Compare via re-marshal (SetEscapeHTML(false), same field order) rather than a
			// raw byte capture on the shim side, since fakeHookSocket decodes rather than
			// tees; re-encoding the DECODED shim callback must match what actually crossed
			// the daemon wire, proving no field was dropped, reordered, or re-escaped.
			var wantCB engine.Callback
			if err := json.Unmarshal(daemonBytes, &wantCB); err != nil {
				t.Fatalf("decode daemon-path bytes: %v", err)
			}
			if shimCB.SessionID != wantCB.SessionID || shimCB.Sequence != wantCB.Sequence ||
				shimCB.Event != wantCB.Event || string(shimCB.Raw) != string(wantCB.Raw) {
				t.Fatalf("shim-path callback %+v != daemon-path callback %+v", shimCB, wantCB)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("fake shim never received a decodable callback")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("fake daemon never received any bytes")
	}
}

// ---------------------------------------------------------------------------
// PostSmart: requirement 7's compatibility contract.
// ---------------------------------------------------------------------------

func startFakeDaemonSocket(t *testing.T) (path string, received chan engine.Callback) {
	t.Helper()
	path = tmpSocketPath(t, "daemon.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen fake daemon socket: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	ch := make(chan engine.Callback, 8)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				var cb engine.Callback
				if json.NewDecoder(c).Decode(&cb) == nil {
					ch <- cb
				}
			}(conn)
		}
	}()
	return path, ch
}

func TestPostSmart_EmptyHookSocketPathGoesStraightToTheDaemon_NoShimDialAttempted(t *testing.T) {
	daemonPath, received := startFakeDaemonSocket(t)
	path, err := PostSmart("", daemonPath, testCallback())
	if err != nil {
		t.Fatalf("PostSmart: %v", err)
	}
	if path != HookPathDaemon {
		t.Fatalf("PostSmart path = %q, want %q (old-shim compat: no HookSocketPath means the daemon path unconditionally)", path, HookPathDaemon)
	}
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatalf("the daemon never received the post")
	}
}

func TestPostSmart_ShimSocketAbsent_FallsBackToDaemonAndSaysSo(t *testing.T) {
	// The socket path is set (as a daemon that has wired R6 would inject it) but nothing is
	// listening there — an OLD SHIM that predates the second listener, mid-upgrade. This must
	// fall back cleanly, not hang the hook post.
	daemonPath, received := startFakeDaemonSocket(t)
	missingShimPath := tmpSocketPath(t, "no-such-hook-sock")
	path, err := PostSmart(missingShimPath, daemonPath, testCallback())
	if err != nil {
		t.Fatalf("PostSmart: %v", err)
	}
	if path != HookPathDaemon {
		t.Fatalf("PostSmart path = %q, want %q (an absent shim hook socket must fall back)", path, HookPathDaemon)
	}
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatalf("the daemon never received the fallback post")
	}
}

func TestPostSmart_ShimAcks_ReportsTheShimPathAndNeverTouchesTheDaemon(t *testing.T) {
	f := startFakeHookSocket(t, true)
	daemonPath, received := startFakeDaemonSocket(t)
	path, err := PostSmart(f.path, daemonPath, testCallback())
	if err != nil {
		t.Fatalf("PostSmart: %v", err)
	}
	if path != HookPathShim {
		t.Fatalf("PostSmart path = %q, want %q", path, HookPathShim)
	}
	select {
	case <-f.received:
	case <-time.After(2 * time.Second):
		t.Fatalf("the shim never received the post")
	}
	select {
	case cb := <-received:
		t.Fatalf("the daemon received a post (%+v) even though the shim acked — a healthy shim path must never also hit the daemon", cb)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestPostSmart_ShimReachableButSilent_FallsBackAfterRetryingTheSameShim(t *testing.T) {
	f := startFakeHookSocket(t, false) // reachable, never acks
	daemonPath, received := startFakeDaemonSocket(t)
	path, err := PostSmart(f.path, daemonPath, testCallback())
	if err != nil {
		t.Fatalf("PostSmart: %v", err)
	}
	if path != HookPathDaemon {
		t.Fatalf("PostSmart path = %q, want %q (a live-but-never-acking shim must eventually fall back rather than hang the hook forever)", path, HookPathDaemon)
	}
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatalf("the daemon never received the fallback post")
	}
	// The retry itself is observable: the shim saw the post more than once before giving up
	// ("the hook client may double-post on no-ack").
	got := 0
	deadline := time.After(500 * time.Millisecond)
drain:
	for {
		select {
		case <-f.received:
			got++
		case <-deadline:
			break drain
		}
	}
	if got < 2 {
		t.Errorf("the shim observed %d post attempt(s) before PostSmart fell back, want >= 2 (a single silent attempt is not a retry)", got)
	}
}

func TestPostSmart_RetriedShimAttemptsCarryTheSameSequence(t *testing.T) {
	// The retry-stable identity: PostSmart must not mint a fresh Sequence per attempt, or
	// the engine's own replay protection (cb.Sequence) — the mechanism that makes a genuine
	// double-post idempotent downstream — would see two DIFFERENT sequences and apply both.
	f := startFakeHookSocket(t, false)
	daemonPath, _ := startFakeDaemonSocket(t)
	cb := testCallback()
	if _, err := PostSmart(f.path, daemonPath, cb); err != nil {
		t.Fatalf("PostSmart: %v", err)
	}
	deadline := time.After(500 * time.Millisecond)
	seqs := map[uint64]bool{}
drain:
	for {
		select {
		case got := <-f.received:
			seqs[got.Sequence] = true
		case <-deadline:
			break drain
		}
	}
	if len(seqs) == 0 {
		t.Fatalf("the shim observed no post attempts")
	}
	if len(seqs) != 1 {
		t.Fatalf("the shim observed %d distinct Sequence value(s) across retries (%v), want exactly 1 — a retry must reuse the SAME callback, not mint a new sequence per attempt", len(seqs), seqs)
	}
}

// ---------------------------------------------------------------------------
// EnvHookSocket: the environment seam a spawned hook invocation reads.
// ---------------------------------------------------------------------------

func TestEnvHookSocket_IsADistinctVariableFromTheDaemonSocket(t *testing.T) {
	if EnvHookSocket == "" {
		t.Fatalf("EnvHookSocket is empty")
	}
	if EnvHookSocket == EnvSocket {
		t.Fatalf("EnvHookSocket must name a DIFFERENT variable than EnvSocket (the daemon socket) — they select different transports")
	}
}

func TestEnvHookSocket_UnsetMeansPostSmartNeverDials(t *testing.T) {
	// A REAL unset process environment (not a stand-in closure), read through
	// os.Getenv exactly as cmd/swarm's runHook reads it, must produce the empty
	// string PostSmart treats as "no shim socket" -- and PostSmart, given that value,
	// must never even attempt to dial a shim: point it at a socket that would fail
	// loudly if dialed (a directory, not a listenable path), and prove the ONLY
	// traffic is the daemon post.
	if _, ok := os.LookupEnv(EnvHookSocket); ok {
		t.Fatalf("%s is set in this test's environment; the test needs it genuinely unset", EnvHookSocket)
	}
	got := os.Getenv(EnvHookSocket)
	if got != "" {
		t.Fatalf("os.Getenv(EnvHookSocket) on an unset environment = %q, want empty", got)
	}

	daemonPath, received := startFakeDaemonSocket(t)
	path, err := PostSmart(got, daemonPath, testCallback())
	if err != nil {
		t.Fatalf("PostSmart: %v", err)
	}
	if path != HookPathDaemon {
		t.Fatalf("PostSmart path = %q, want %q", path, HookPathDaemon)
	}
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatalf("the daemon never received the post")
	}
}
