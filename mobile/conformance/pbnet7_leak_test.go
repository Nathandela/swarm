package conformance_test

// PB-NET-7's leak clause, ON THE PATH THE PHONE ACTUALLY TAKES.
//
// The requirement's evidence column asks for "-race + goroutine-leak assertion over repeated
// Start/Stop", and that assertion EXISTS -- in internal/remote/transport
// (hygiene_test.go's TestNoGoroutineLeakAcrossConnectDisconnectCycles), a package the shipped
// phone does not use. This is the same shape as the wedge that preceded it: the phone calls
// relay.Client directly from App.run, so a fence written against transport.Session guards a
// path nothing takes. It is also the shape PB-NET-4's backoff and PB-NET-5's wait are in.
//
// WHAT LEAKS HERE AND WHY IT IS INVISIBLE. Each Start..Stop generation owns a goroutine
// (App.run), and each dial inside it owns another (relay.Conn.pump), plus whatever the
// websocket library keeps per connection. A handset roams between cells all day: the reconnect
// loop runs on every flap, so one goroutine retained per cycle is not a slow leak, it is a
// counter tied to how much the user moved. Nothing surfaces it -- the app reports "online",
// memory grows, and the OS kills the process mid-session hours later.
//
// THE FLAP IS PART OF THE CYCLE, not decoration. Start/Stop alone exercises the loop's clean
// exit; cutting the link mid-session makes the reconnect machinery start, fail, back off and
// start again, which is where a retained connection would actually accumulate.

import (
	"runtime"
	"testing"
	"time"
)

// pbnet7Cycles is how many connect/disconnect cycles the assertion runs. One retained
// goroutine per cycle has to be visible above the runtime's own noise, and it is: the
// baseline is taken after a warm-up cycle, so anything the relay or the websocket library
// keeps per connection is already counted.
const pbnet7Cycles = 12

// settledGoroutines waits for the runtime's goroutine count to stop moving, so a baseline is
// not taken mid-teardown. It mirrors internal/remote/transport's helper of the same name --
// deliberately, so the live-path assertion and the dead-path one are the same measurement.
func settledGoroutines() int {
	prev := -1
	for i := 0; i < 100; i++ {
		runtime.Gosched()
		n := runtime.NumGoroutine()
		if n == prev {
			return n
		}
		prev = n
		time.Sleep(10 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

// assertNoLeak fails with the full goroutine dump if the count does not come back to the
// baseline within a bound. The dump is the point: a bare count tells a reader that something
// leaked, and the stacks tell them which cycle stage retained it.
func assertNoLeak(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= baseline {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("goroutine leak across %d connect/disconnect cycles: %d live, baseline %d "+
				"(PB-NET-7).\nA handset reconnects on every cell flap, so a goroutine retained "+
				"per cycle grows with how much the user moved and is invisible until the OS "+
				"kills the process.\n%s", pbnet7Cycles, n, baseline, buf)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestPBNET7_NoGoroutineLeakAcrossConnectDisconnectCycles is the acceptance criterion, driven
// through the FACADE's own lifecycle verbs rather than through a transport this app never
// constructs.
func TestPBNET7_NoGoroutineLeakAcrossConnectDisconnectCycles(t *testing.T) {
	h := newHarness(t)
	proxy := newCuttableRelay(t, h.RelayURL)

	// The phone dials through a proxy whose link the cycle can cut, so the reconnect loop is
	// exercised rather than only the clean shutdown.
	_ = h.App.Close()
	h.AppRelayURL = proxy.URL()
	h.App = h.openApp()

	awaitConnState := func(want string, wantIt bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			s, err := h.App.ConnectionState()
			if err == nil && (s == want) == wantIt {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		s, _ := h.App.ConnectionState()
		t.Fatalf("connection state %q never became %s%q", s, map[bool]string{true: "", false: "not "}[wantIt], want)
	}

	cycle := func() {
		t.Helper()
		if err := h.App.Start(); err != nil {
			t.Fatalf("App.Start: %v", err)
		}
		awaitConnState(connOnlineState, true)

		// A REAL EXCHANGE, so the cycle retires a used connection rather than an idle one:
		// this is the request/reply path, the one that holds the exchange lock and now carries
		// a deadline of its own.
		if _, err := h.App.Presence(); err != nil {
			t.Fatalf("Presence: %v", err)
		}

		// The flap: cut, let the reconnect loop run, restore, come back.
		proxy.Cut()
		awaitConnState(connOnlineState, false)
		proxy.Restore()
		awaitConnState(connOnlineState, true)

		if err := h.App.Stop(); err != nil {
			t.Fatalf("App.Stop: %v", err)
		}
	}

	// openApp already started this app; reach a known stopped state first.
	if err := h.App.Stop(); err != nil {
		t.Fatalf("App.Stop (pre-roll): %v", err)
	}

	// One warm-up cycle, so the baseline includes whatever the relay, the proxy and the
	// websocket library keep alive per connection. Without it the assertion measures
	// first-connection setup and fails on a phone that leaks nothing.
	cycle()
	baseline := settledGoroutines()

	for i := 0; i < pbnet7Cycles; i++ {
		cycle()
	}
	// The measurement is logged, not just asserted: a reader of this evidence needs to see
	// that the count MOVED during the cycles and came back, or the assertion is
	// indistinguishable from one that measured nothing.
	t.Logf("goroutines: baseline %d after the warm-up cycle, %d immediately after %d cycles",
		baseline, runtime.NumGoroutine(), pbnet7Cycles)
	assertNoLeak(t, baseline)
}

// connOnlineState is the transport state the facade reports while connected. It is a literal
// here because mobile/relay.go's constant is unexported and this is an external test package;
// s16 already pins the same strings against the Android side.
const connOnlineState = "online"
