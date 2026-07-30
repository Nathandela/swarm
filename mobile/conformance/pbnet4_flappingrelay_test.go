// PB-NET-4's reconnect backoff, observed against App.run itself (round-7 re-audit).
//
// THE GAP THIS CLOSES. mobile/pbnet4_backoff_test.go's four tests all construct a
// *reconnectBackoff directly or call its pure functions -- newReconnectBackoff(),
// &reconnectBackoff{...}, reconnectBackoffBase(n). None of them ever call App.run, so
// reverting App.run's `case <-time.After(rb.next()):` to the pre-fix
// `case <-time.After(250 * time.Millisecond):` -- touching nothing else -- leaves all four
// green, the whole mobile package green, and conformance identical to baseline. The
// adjudicator found this by making exactly that revert. This file drives the real facade
// (NewApp + Start, exactly what PhoneRuntime does) against a relay it does not control the
// timing of, and asserts on when connections actually arrive, so a revert of the WIRING
// alone -- not just the constants -- is what it is written to catch.
//
// It also closes PB-NET-4's other unfenced clause: "re-auth after reconnect" was true only
// by construction (App.dial always calls relay.DialSecure, which always authenticates, and
// there is no resume path anywhere) and named by no test in the tree -- the only auth_init
// assertions that exist are b37_cleartext_test.go's, whose property runs the other
// direction (that NO auth_init crosses a cleartext hop). auth_init carries the phone's
// relay-auth signature, so counting it per connection is a direct observation of
// re-authentication, not an inference from the code that is supposed to cause it.
package conformance_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/remote/relay"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// flapTap is a websocket proxy in front of the real relay, built on dialTap's shape
// (b37_cleartext_test.go) with two additions this fence needs: it can be told to REFUSE
// every new connection outright, before any websocket upgrade -- so a refused dial never
// gets to send a frame -- and it can SEVER the one connection currently live without
// refusing what follows, so a test can force a reconnect against an otherwise healthy
// relay.
//
// Every connection ATTEMPT is timestamped as it arrives, refused or not. That is the one
// observable that can tell a growing backoff apart from a fixed one over the real App.run
// loop: the connection state (`ConnectionState()`) reads "reconnecting" in both cases.
type flapTap struct {
	srv      *httptest.Server
	upstream string

	mu         sync.Mutex
	refuse     bool
	arrivals   []time.Time
	sent       bytes.Buffer
	liveCancel context.CancelFunc
}

func newFlapTap(t *testing.T, upstreamWS string) *flapTap {
	t.Helper()
	tap := &flapTap{upstream: upstreamWS}
	tap.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tap.mu.Lock()
		tap.arrivals = append(tap.arrivals, time.Now())
		refuse := tap.refuse
		tap.mu.Unlock()
		if refuse {
			http.Error(w, "relay refused (flapTap)", http.StatusServiceUnavailable)
			return
		}

		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(relay.MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, tap.upstream, nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(relay.MaxFrame + 64)

		tap.mu.Lock()
		tap.liveCancel = cancel
		tap.mu.Unlock()

		done := make(chan struct{}, 2)
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := up.Read(ctx)
				if err != nil || down.Write(ctx, mt, data) != nil {
					return
				}
			}
		}()
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := down.Read(ctx)
				if err != nil {
					return
				}
				tap.mu.Lock()
				tap.sent.Write(data)
				tap.mu.Unlock()
				if up.Write(ctx, mt, data) != nil {
					return
				}
			}
		}()
		<-done
	}))
	t.Cleanup(tap.srv.Close)
	return tap
}

func (d *flapTap) URL() string { return "ws" + strings.TrimPrefix(d.srv.URL, "http") }

func (d *flapTap) setRefuse(v bool) {
	d.mu.Lock()
	d.refuse = v
	d.mu.Unlock()
}

// arrivalTimes returns a copy of every connection attempt's arrival time so far, refused or
// not.
func (d *flapTap) arrivalTimes() []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]time.Time(nil), d.arrivals...)
}

// authCount reports how many auth_init frames have crossed this tap in total.
func (d *flapTap) authCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return bytes.Count(d.sent.Bytes(), []byte("auth_init"))
}

// sever force-closes the one live connection, if any, WITHOUT refusing the dial that
// follows -- so a test can observe a reconnect (its re-auth, and its reset backoff) against
// a relay that is otherwise healthy.
func (d *flapTap) sever() {
	d.mu.Lock()
	cancel := d.liveCancel
	d.liveCancel = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// awaitArrivals polls until at least n connection attempts have reached the tap. The
// deadline is generous on purpose -- see the test's own note on timing.
func awaitArrivals(t *testing.T, tap *flapTap, n int, within time.Duration) []time.Time {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := tap.arrivalTimes()
		if len(got) >= n {
			return got
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("timed out after %s waiting for %d dial attempts at the tap; saw %d",
				within, n, len(got))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// awaitConnStateLeaves polls until App.ConnectionState reads something other than from.
//
// THIS IS THE HALF awaitConnState CANNOT DO, and phase 3 needs it: severing a connection
// that was already reporting "online" and then immediately polling for "online" again would
// pass on its very FIRST read, before the severance has had any chance to propagate --
// proving nothing about a reconnect actually having happened. Confirming the state LEFT the
// target first makes the later wait for it to RETURN a real observation of a round trip,
// not a stale read of the state that was already true.
func awaitConnStateLeaves(t *testing.T, app *swarmmobile.App, from string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got, err := app.ConnectionState()
		if err != nil {
			t.Fatalf("App.ConnectionState: %v", err)
		}
		if got != from {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("timed out after %s waiting for the connection state to leave %q", within, from)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestPBNET4_TheRealRunLoopGrowsReAuthenticatesAndResetsItsBackoff is PB-NET-4's fence over
// App.run, not over the backoff type in isolation.
//
// THE SEQUENCE, one relay, three phases:
//  1. The tap REFUSES every dial, before any websocket upgrade. Four attempts arrive; the
//     three gaps between them must fall in section 6.0's stated bands (500ms, 1s, 2s, each
//     +/-20%) -- and because the bands do not overlap, three individually-correct gaps
//     already prove growth without computing a ratio.
//  2. The tap starts ALLOWING connections. The dial already in flight (on the backoff
//     clock) reaches the real relay, the phone comes online, and exactly one auth_init has
//     been observed -- the only way it could have, since App.dial always re-authenticates.
//  3. The tap SEVERS that one live connection without refusing what follows. The phone
//     must reconnect, send a SECOND auth_init, and do so on a gap close to the INITIAL
//     500ms delay rather than a continuation of phase 1's growth -- which is what reset()
//     (called in App.run right after setConn(connOnline)) exists for.
//
// No tight wall-clock upper bound is asserted anywhere below. Every wait uses a generous
// deadline, and the one comparison that matters for reset() (phase 3's reconnect gap) is
// checked RELATIVE to phase 1's own measured third gap, not against an absolute duration
// this host's load could blow through under a slow run.
func TestPBNET4_TheRealRunLoopGrowsReAuthenticatesAndResetsItsBackoff(t *testing.T) {
	h := newHarness(t)
	tap := newFlapTap(t, h.RelayURL)
	tap.setRefuse(true)

	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.AppRelayURL = tap.URL()
	h.App = h.openApp()

	// ---- phase 1: every dial refused, so the only observable is arrival timing --------
	arrivals := awaitArrivals(t, tap, 4, 15*time.Second)
	gaps := make([]time.Duration, 3)
	for i := range gaps {
		gaps[i] = arrivals[i+1].Sub(arrivals[i])
	}
	bands := []struct{ lo, hi time.Duration }{
		{400 * time.Millisecond, 600 * time.Millisecond},   // 500ms +/-20%
		{800 * time.Millisecond, 1200 * time.Millisecond},  // 1s +/-20%
		{1600 * time.Millisecond, 2400 * time.Millisecond}, // 2s +/-20%
	}
	for i, b := range bands {
		if gaps[i] < b.lo || gaps[i] > b.hi {
			t.Fatalf("PB-NET-4: gap #%d between dial attempts at the tap was %v, want within "+
				"[%v, %v] (section 6.0: initial 500ms, factor 2, jitter +/-20%%), measured "+
				"against the REAL App.run loop over a relay that refuses every dial. A fixed, "+
				"non-growing delay (the pre-fix 250ms, or any other constant) fails here",
				i, gaps[i], b.lo, b.hi)
		}
	}
	if n := tap.authCount(); n != 0 {
		t.Fatalf("PB-NET-4: %d auth_init frame(s) arrived while every dial was refused before "+
			"the websocket upgrade; the growth measurement above would not be trustworthy", n)
	}

	// ---- phase 2: allowed. Re-auth is the only way the pending dial reaches online. ----
	tap.setRefuse(false)
	if _, ok := awaitConnState(t, h.App, "online", 15*time.Second); !ok {
		t.Fatalf("PB-NET-4: the phone never came online once the tap stopped refusing dials")
	}
	if n := tap.authCount(); n != 1 {
		t.Fatalf("PB-NET-4: %d auth_init frame(s) observed on the first successful connection, "+
			"want exactly 1", n)
	}

	// ---- phase 3: sever the live connection; the relay stays otherwise healthy. -------
	severedAt := time.Now()
	tap.sever()
	// The state must be OBSERVED leaving "online" before waiting for it to return -- see
	// awaitConnStateLeaves's own note. Without this, the wait below could pass on its first
	// poll, against the connection severed() just tore down, before App.run had any chance
	// to notice.
	awaitConnStateLeaves(t, h.App, "online", 5*time.Second)
	if _, ok := awaitConnState(t, h.App, "online", 15*time.Second); !ok {
		t.Fatalf("PB-NET-4: the phone never reconnected after its one live connection was severed")
	}
	if n := tap.authCount(); n != 2 {
		t.Errorf("PB-NET-4: %d auth_init frame(s) observed after the reconnect, want exactly 2 "+
			"-- re-auth after reconnect must happen on EVERY reconnect, not just the first", n)
	}
	after := tap.arrivalTimes()
	reconnectGap := after[len(after)-1].Sub(severedAt)
	if reconnectGap >= gaps[2] {
		t.Errorf("PB-NET-4: the reconnect after a live severance waited %v, which is not less "+
			"than phase 1's own third gap (%v). reset() must return the backoff to its INITIAL "+
			"delay after a successful connection; a backoff that kept growing across a healthy "+
			"connection would be indistinguishable from one that never reset", reconnectGap, gaps[2])
	}
}
