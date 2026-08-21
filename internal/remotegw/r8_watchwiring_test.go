package remotegw

// WAVE R8 / ROUND 2 -- THE WATCH HORIZON, WIRED (round-2 MAJOR 4).
//
// r8_watchlease_test.go proves `Renew`, `WatchLive`, `Reap` and `UnwatchAll` BEHAVE. Round 2
// measured that three of the four had ZERO PRODUCTION CALLERS: only `Renew` was reached
// (`command_loop.go`), so an unrenewed watch never expired (nothing called `Reap`), transport
// loss did not unwatch (nothing called `UnwatchAll`), and the emission path asked nothing
// about watch liveness. The horizon was a field that was written on every Watch and read by
// nobody -- so the defect T4-b was written to close was still open, and round 1's mutation
// mutated the HELPER and therefore passed while the production property did not exist.
//
// This file fences the WIRING, which is a different claim from the behaviour:
//
//	Gateway.watchStillLive  <- TerminalWatcher.WatchLive, bound in NewService
//	Service.Run's reaper    <- TerminalWatcher.Reap on a ticker
//	Service.Run's ctx exit  <- TerminalWatcher.UnwatchAll on transport loss

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// readSource reads one file of this package for the two source fences at the bottom. They are
// source fences because "the supervised loop treats a horizon breach as terminal" is a
// statement about a code path a fake runner cannot reach: the fake never returns that error,
// and a fake that did would be asserting about itself.
func goFuncBody(t *testing.T, src, decl string) string {
	t.Helper()
	i := strings.Index(src, decl)
	if i < 0 {
		t.Fatalf("the source declares no %s", decl)
	}
	rest := src[i:]
	j := strings.Index(rest, "\n}\n")
	if j < 0 {
		t.Fatalf("could not find the end of %s", decl)
	}
	return rest[:j]
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestR8Wiring_TheEmissionPathAsksTheWatchHorizon is the per-emission half of T4-b. It drives
// Gateway.watchStillLive -- the predicate RunTerminal consults before handing a snapshot to
// the sink -- rather than TerminalWatcher.WatchLive directly, because the round-1 defect was
// exactly that the two were never connected.
func TestR8Wiring_TheEmissionPathAsksTheWatchHorizon(t *testing.T) {
	clk := &r8Clock{at: time.Unix(1755600000, 0)}
	runner := newR8FakeRunner()
	const horizon = 30 * time.Second
	w := newTerminalWatcher(runner, time.Millisecond, horizon, clk.now)
	t.Cleanup(func() { _ = w.Close() })

	gw := New("/tmp/does-not-need-to-exist.sock", nil)

	// UNBOUND is permissive, and deliberately: a Gateway with no watcher owns no horizons, so
	// its absence cannot be its violation. This is the state every unit test that builds a
	// bare Gateway is in, and it must not be the state the sidecar ships in.
	if !gw.watchStillLive("m/sess1") {
		t.Fatalf("an UNBOUND gateway refused an emission; a peek nobody owns has no horizon to breach")
	}

	gw.bindWatchLiveness(w.WatchLive)
	if gw.watchStillLive("m/sess1") {
		t.Fatalf("the bound gateway allowed an emission for a session with NO WATCH AT ALL. The " +
			"emission path must ask the watcher, or the horizon is a field nothing reads.")
	}

	w.Watch("m/sess1")
	if !gw.watchStillLive("m/sess1") {
		t.Fatalf("the bound gateway refused an emission for a LIVE watch")
	}

	clk.advance(horizon + time.Second)
	if gw.watchStillLive("m/sess1") {
		t.Fatalf("the bound gateway allowed an emission %s past the watch horizon. ADR-017 T4-b: "+
			"the machine stops rendering, sealing and appending -- a phone that stopped renewing "+
			"is a phone that provably is not looking, and every screen sealed for it is spent from "+
			"an append budget shared with every other session's transcript.", horizon)
	}
	w.Unwatch("m/sess1")
}

// TestR8Wiring_TheServiceBindsTheWatcherToTheGateway is the constructor half: the binding
// must happen in NewService, because a predicate bound only in a test is a predicate the
// shipped sidecar does not have.
func TestR8Wiring_TheServiceBindsTheWatcherToTheGateway(t *testing.T) {
	s := newWiringService(t)
	if s.gw.watchLive == nil {
		t.Fatalf("NewService left Gateway.watchLive nil, so the shipped sidecar's snapshot path " +
			"asks nothing about the watch horizon. TerminalWatcher.WatchLive is the predicate and " +
			"NewService is where it is installed.")
	}
	// It must be THIS service's watcher, not some other predicate: a watch opened on the
	// service's watcher must be visible through the gateway's predicate.
	if s.gw.watchStillLive("m/none") {
		t.Fatalf("the bound predicate allows an unwatched session; it is not this watcher's")
	}
	s.watchers.Watch("m/sess1")
	t.Cleanup(func() { s.watchers.Unwatch("m/sess1") })
	if !s.gw.watchStillLive("m/sess1") {
		t.Fatalf("the bound predicate does not see this service's own watch")
	}
}

// TestR8Wiring_RunReapsExpiredWatchesAndUnwatchesOnTransportLoss drives the REAL Service.Run
// loop: it starts the run, opens a watch, lets the reaper tick, then cancels the context the
// way a dead relay link does and asserts every watch is gone.
//
// THE RIG REAPS WHAT IT SPAWNS (bd agents-tracker-ev0w): Run is cancelled and joined in
// t.Cleanup, and the watcher is closed with it.
func TestR8Wiring_RunReapsExpiredWatchesAndUnwatchesOnTransportLoss(t *testing.T) {
	s := newWiringService(t)
	// A short horizon so the production reap interval (a quarter of the horizon) fires inside
	// the test's own patience, without the test reaching into the reaper.
	s.watchers.horizon = 40 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	s.watchers.Watch("m/sess1")
	if !s.watchers.WatchLive("m/sess1") {
		t.Fatalf("the watch did not open")
	}

	// NOBODY RENEWS. The reaper must end it without any further input.
	deadline := time.Now().Add(5 * time.Second)
	for s.watchers.watchCount() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("an unrenewed watch survived its horizon indefinitely: nothing in the running "+
				"Service calls Reap. That is round-2 MAJOR 4 -- the deadline was written on every "+
				"Watch and read by nobody, so a phone that went offline mid-watch left the machine "+
				"rendering, sealing and appending full screens against the shared %s budget forever.",
				"8-appends/s")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// TRANSPORT LOSS UNWATCHES, and it does not wait for a horizon: re-open a watch, cancel
	// the run the way a dead relay link does, and assert it is gone.
	s.watchers.horizon = time.Hour
	s.watchers.Watch("m/sess2")
	if s.watchers.watchCount() != 1 {
		t.Fatalf("the second watch did not open")
	}
	cancel()
	<-done
	if n := s.watchers.watchCount(); n != 0 {
		t.Fatalf("%d watch(es) survived the Service's exit with an HOUR left on the horizon. "+
			"ADR-017 T4-b: transport loss unwatches -- waiting for each watch to time out on its "+
			"own horizon leaves the machine rendering and appending for a phone that provably is "+
			"not there.", n)
	}
}

// TestR8Wiring_TheCancelPathUnwatchesRatherThanWaitingForRunToReturn is the SOURCE half of
// the transport-loss obligation, and it is stated as a source fence with its limitation
// disclosed rather than dressed up as behaviour.
//
// WHAT THE BEHAVIOURAL TEST ABOVE CANNOT SEPARATE. `Service.Run`'s deferred
// `watchers.Close()` also ends every watch, so "no watch survives Run" is true with or
// without the explicit unwatch. The difference is WHEN: `UnwatchAll` fires the instant the
// context is cancelled, while `Close` fires only after `wg.Wait()` has joined the journal
// loop, the command bridge and the link watcher -- each of which can take its own timeout to
// notice. During that window the daemon is still rendering and the sink still sealing for a
// phone whose link is already dead. That window is real and is not reproducible in a unit
// rig without stalling one of those loops artificially, which would be a test about the
// stall. So the fence is over the code path, scoped to Run's own body so it cannot be
// satisfied by UnwatchAll's declaration in another file.
func TestR8Wiring_TheCancelPathUnwatchesRatherThanWaitingForRunToReturn(t *testing.T) {
	body := goFuncBody(t, readSource(t, "service.go"), "func (s *Service) Run(")
	if !strings.Contains(body, "UnwatchAll(") {
		t.Fatalf("Service.Run never calls UnwatchAll, so a dead relay link leaves every watch " +
			"running until wg.Wait joins the journal loop, the bridge and the link watcher. " +
			"ADR-017 T4-b: transport loss unwatches, and it does not wait.")
	}
	if !strings.Contains(body, "ctx.Done()") {
		t.Fatalf("the extracted body is not Service.Run; the assertion above is vacuous")
	}
	// It must be on the CANCEL path and not on the tick: an UnwatchAll on every reap would end
	// every watch a second after it opened.
	cancelArm := body[strings.Index(body, "case <-ctx.Done():"):]
	if !strings.Contains(cancelArm[:min(len(cancelArm), 800)], "UnwatchAll(") {
		t.Fatalf("UnwatchAll is not on the context-cancel arm of the reaper")
	}
}

// watchCount is the live-watch count, for the wiring assertions above.
func (w *TerminalWatcher) watchCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.watches)
}

// newWiringService builds a Service through the REAL NewService with the minimum config that
// constructor needs, so the assertions above are about the shipped assembly and not about a
// hand-built struct.
func newWiringService(t *testing.T) *Service {
	t.Helper()
	s := NewService(ServiceConfig{
		DaemonSocket:   "/nonexistent/remote.sock", // no daemon: the journal loop just retries
		Relay:          &scriptedMailbox{},
		PhoneTarget:    "phone",
		ReconnectDelay: time.Millisecond,
	})
	if s == nil {
		t.Fatalf("NewService returned nil")
	}
	t.Cleanup(func() { _ = s.watchers.Close() })
	return s
}

// TestR8Wiring_AHorizonBreachEndsTheWatchRatherThanReconnecting pins the supervised loop's
// treatment of the new terminal error: like a capability refusal it must END the watch, not
// back off and dial again. A reconnect loop for a phone that is not looking is the same cost
// the horizon exists to stop, one indirection later.
func TestR8Wiring_AHorizonBreachEndsTheWatchRatherThanReconnecting(t *testing.T) {
	src := readSource(t, "gateway.go")
	if !strings.Contains(src, "errWatchHorizonPassed") {
		t.Fatalf("gateway.go declares no errWatchHorizonPassed, so the emission path has no way to " +
			"report a breached horizon distinctly from a dropped connection")
	}
	loop := readSource(t, "terminal_watcher.go")
	if !strings.Contains(loop, "errors.Is(err, errWatchHorizonPassed)") {
		t.Fatalf("terminal_watcher.go's supervised loop does not recognise a breached horizon, so it " +
			"backs off and RECONNECTS -- re-opening the peek it just closed, for the same absent phone")
	}
}
