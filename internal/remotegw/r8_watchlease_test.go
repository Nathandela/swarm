package remotegw

// WAVE R8 / SLICE S4 -- A WATCH IS A LEASE WITH A HORIZON, AND ONE PRODUCER PER SESSION.
// Failing-first (TDD RED, GG-5).
//
// THE LEAK, MEASURED RATHER THAN SUSPECTED. `TerminalWatcher.Close()` IS wired to gateway
// close, so a clean shutdown reaps every peek -- that half is fine and stays fine. What
// nothing reaps is a phone that simply STOPS READING: `Watch` / `Unwatch` / `Close`
// (terminal_watcher.go:100-160) are the whole lifecycle, there is no timer and no presence
// input anywhere in the type, and the render loop's own liveness poll asks only the KILL
// SWITCH (`stillAllowed = cc.peekGateOpen`, server.go:2230). "Is anyone still watching" is a
// question no part of this machinery asks.
//
// The consequence is not a goroutine leak in the abstract. A phone that goes offline
// mid-watch leaves the machine rendering full screens, sealing them and appending them
// against the SHARED 8-appends/s budget the journal also spends from (ADR-009:156-165) --
// so the transcript of every other session yields to snapshots nobody is reading -- and it
// builds a backlog the phone then replays on reconnect.
//
// Amendment T4-b: the watch is renewed on the same discipline as the control keepalive and
// expires without renewal; transport loss unwatches; the predicate the render loop ALREADY
// polls every tick widens from the kill switch alone to kill switch AND capability AND watch
// liveness -- one predicate, on the tick that already exists, so the horizon costs no new
// loop.
//
// THE SEAMS (undefined symbols -> compile-fail RED):
//
//	func newTerminalWatcher(runner terminalRunner, backoff, horizon time.Duration, now func() time.Time) *TerminalWatcher
//	func (w *TerminalWatcher) Renew(session string)
//	func (w *TerminalWatcher) WatchLive(session string) bool
//	func (w *TerminalWatcher) Reap()   // expires unrenewed watches; called from the existing tick

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// r8FakeRunner is a terminalRunner that blocks until its ctx is cancelled and counts how many
// times it was (re)started, so a test can watch the supervised loop rather than a daemon.
//
// IT REAPS WHAT IT SPAWNS (bd agents-tracker-ev0w): every run returns on ctx cancel and the
// test joins through Unwatch/Close, so no goroutine and no fd outlives the test.
type r8FakeRunner struct {
	mu     sync.Mutex
	starts map[string]int
	live   map[string]int
}

func newR8FakeRunner() *r8FakeRunner {
	return &r8FakeRunner{starts: map[string]int{}, live: map[string]int{}}
}

func (r *r8FakeRunner) RunTerminal(ctx context.Context, session string) error {
	r.mu.Lock()
	r.starts[session]++
	r.live[session]++
	r.mu.Unlock()
	<-ctx.Done()
	r.mu.Lock()
	r.live[session]--
	r.mu.Unlock()
	return ctx.Err()
}

func (r *r8FakeRunner) startCount(session string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts[session]
}

func (r *r8FakeRunner) liveCount(session string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live[session]
}

// r8Clock is a test-driven clock: the horizon must be provable without sleeping through it,
// and a horizon test that sleeps is a horizon test that will be shortened later.
type r8Clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *r8Clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *r8Clock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// TestR8Watch_AnUnrenewedWatchExpires is amendment T4-b's core assertion and the mutation
// fence D-WATCHLEASE names: make the liveness predicate ignore the watch lease and this fails.
func TestR8Watch_AnUnrenewedWatchExpires(t *testing.T) {
	clk := &r8Clock{at: time.Unix(1755600000, 0)}
	runner := newR8FakeRunner()
	const horizon = 30 * time.Second
	w := newTerminalWatcher(runner, 5*time.Millisecond, horizon, clk.now)
	t.Cleanup(func() { _ = w.Close() })

	w.Watch("sess1")
	if !w.WatchLive("sess1") {
		t.Fatalf("a freshly opened watch must be live")
	}

	// Renewed inside the horizon: still live. This is the half that stops the fix from being
	// "expire everything quickly".
	clk.advance(horizon / 2)
	w.Renew("sess1")
	w.Reap()
	if !w.WatchLive("sess1") {
		t.Errorf("a watch renewed inside its horizon must stay live; a horizon that outruns a renewing " +
			"phone turns a working screen off while the user is reading it")
	}

	// Not renewed across the horizon: expired, and the supervised loop is joined.
	clk.advance(horizon + time.Second)
	w.Reap()
	if w.WatchLive("sess1") {
		t.Errorf("ADR-017 T4-b: an UNRENEWED watch is still live after its horizon. A phone that goes " +
			"offline mid-watch leaves the machine rendering, sealing and appending full screens against " +
			"the shared 8-appends/s budget indefinitely -- the transcript of every other session yields " +
			"to snapshots nobody is reading.")
	}
	deadline := time.Now().Add(2 * time.Second)
	for runner.liveCount("sess1") != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := runner.liveCount("sess1"); n != 0 {
		t.Errorf("an expired watch left %d supervised RunTerminal goroutine(s) running. Expiry must "+
			"cancel the peek and join it, exactly as Unwatch does.", n)
	}
}

// TestR8Watch_ExactlyOneProducerPerSession is T4-a's last clause, and it is about the
// coalescer rather than about goroutines.
//
// `CoalescingSink.Terminal` holds ONE newest snapshot per session (coalesce.go:167-180),
// latest-wins. Two producers publishing into that slot with independent revision counters
// make the released stream zigzag -- revision 5 from producer A, then 3 from producer B --
// and the phone's "strictly greater" rule then discards an arbitrary half of the stream. The
// watcher is already idempotent per session; this pins that idempotence as the WIRE
// GUARANTEE it now has to be, not merely as a tidiness property of Watch.
func TestR8Watch_ExactlyOneProducerPerSession(t *testing.T) {
	clk := &r8Clock{at: time.Unix(1755600000, 0)}
	runner := newR8FakeRunner()
	w := newTerminalWatcher(runner, 5*time.Millisecond, 30*time.Second, clk.now)
	t.Cleanup(func() { _ = w.Close() })

	for i := 0; i < 5; i++ {
		w.Watch("sess1")
	}
	deadline := time.Now().Add(time.Second)
	for runner.startCount("sess1") == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := runner.liveCount("sess1"); n != 1 {
		t.Errorf("ADR-017 T4-a: %d producers are live for one session. Exactly one may publish into the "+
			"coalescer's per-session slot (coalesce.go:172-180); two independent revision counters make "+
			"the released stream zigzag and the phone discards an arbitrary half of it.", n)
	}
	// A repeated watch must also RENEW, or a phone that re-asserts its watch instead of
	// renewing (the natural thing to do after a reconnect) is expired out from under itself.
	clk.advance(20 * time.Second)
	w.Watch("sess1")
	clk.advance(20 * time.Second)
	w.Reap()
	if !w.WatchLive("sess1") {
		t.Errorf("a repeated Watch must renew the lease: a phone that re-asserts its watch after a " +
			"reconnect would otherwise be expired while it is actively asking")
	}
}

// TestR8Watch_TransportLossUnwatches is T4-b's second clause. `bindParent` already re-roots
// every peek at the Service ctx so a revoke stops them structurally; the missing half is that
// losing the PHONE (not the service) also ends the watch, and it ends it without waiting for
// the horizon.
func TestR8Watch_TransportLossUnwatches(t *testing.T) {
	clk := &r8Clock{at: time.Unix(1755600000, 0)}
	runner := newR8FakeRunner()
	w := newTerminalWatcher(runner, 5*time.Millisecond, 30*time.Second, clk.now)
	t.Cleanup(func() { _ = w.Close() })

	w.Watch("sess1")
	w.Watch("sess2")
	w.UnwatchAll("transport lost")
	if w.WatchLive("sess1") || w.WatchLive("sess2") {
		t.Errorf("ADR-017 T4-b: transport loss must unwatch every session immediately rather than " +
			"leaving each to time out on its own horizon. Until the horizon passes, the machine is " +
			"rendering and appending for a phone that provably is not there.")
	}
	deadline := time.Now().Add(2 * time.Second)
	for (runner.liveCount("sess1")+runner.liveCount("sess2")) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := runner.liveCount("sess1") + runner.liveCount("sess2"); n != 0 {
		t.Errorf("UnwatchAll left %d peek goroutine(s) behind; it must join them as Unwatch does", n)
	}
}

// TestR8Watch_TheCommandLoopGateIsNotTheWatchersOwn is amendment T2-c on the GATEWAY side of
// the same hole the daemon-side test covers.
//
// `routeCommand` sends `ActionTerminalWatch` straight to `Watchers.Watch(rc.Session)`
// (command_loop.go:612-621) with a comment saying the DAEMON gates it -- which was true of
// the kill switch and the negotiated capability, and was never true of the SESSION. This is
// a source-level obligation because it is about which call sites exist: the gateway must not
// start a peek for a session whose record does not permit one, so that a refusal costs no
// daemon dial and no backoff loop.
func TestR8Watch_TheCommandLoopGateIsNotTheWatchersOwn(t *testing.T) {
	src := readGatewaySource(t, "command_loop.go")
	idx := indexOf(src, "case protocol.ActionTerminalWatch:")
	if idx < 0 {
		t.Fatalf("command_loop.go no longer routes ActionTerminalWatch; the fixture is stale")
	}
	block := src[idx:min(idx+1200, len(src))]
	if !containsAny(block, "Capabilit", "capabilit") {
		t.Errorf("ADR-017 T2-c: the gateway's terminal_watch route never consults the session capability " +
			"record. Its comment says \"the daemon gates the peek itself (capability + kill switch)\", " +
			"and that is the REMOTE-GATEWAY capability and the kill switch -- neither of which is about " +
			"the SESSION. A downlevel or compromised app that merely asks gets a supervised, " +
			"reconnecting peek started on its behalf onto a healthy Claude session.")
	}
}

// readGatewaySource reads one file of this package as text, for the obligations that are
// about which call sites exist rather than about what one call returns.
func readGatewaySource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func indexOf(s, sub string) int { return strings.Index(s, sub) }

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
