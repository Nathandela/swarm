// PB-NET-4's reconnect backoff, observed against App.run itself (round-7 re-audit).
//
// THREE GAPS, ONE SPECIES: a property that is TRUE, measured by NOTHING that names it, and
// none of the three a live defect.
//
//  1. Nothing ties App.run's delay to the backoff. mobile/pbnet4_backoff_test.go's four
//     tests all construct a *reconnectBackoff directly or call its pure functions --
//     newReconnectBackoff(), &reconnectBackoff{...}, reconnectBackoffBase(n). None of them
//     ever call App.run, so reverting App.run's `case <-time.After(rb.next()):` to the
//     pre-fix `case <-time.After(250 * time.Millisecond):` -- touching nothing else --
//     leaves all four green, the whole mobile package green, and conformance identical to
//     baseline. The adjudicator found this by making exactly that revert. This file drives
//     the real facade (NewApp + Start, exactly what PhoneRuntime does) against a relay it
//     does not control the timing of, and asserts on when connections actually arrive, so a
//     revert of the WIRING alone -- not just the constants -- is what it is written to
//     catch.
//  2. Nothing names re-auth after reconnect: true only by construction (App.dial always
//     calls relay.DialSecure, which always authenticates, and there is no resume path
//     anywhere), asserted by no test in the tree -- the only auth_init assertions that
//     exist are b37_cleartext_test.go's, whose property runs the other direction (that NO
//     auth_init crosses a cleartext hop). auth_init carries the phone's relay-auth
//     signature, so counting it per connection is a direct observation of
//     re-authentication, not an inference from the code that is supposed to cause it.
//  3. Nothing names the connection EVENT plane (ADR-007 B114). App.setConn does two things
//     from one write: it updates a.connState (what ConnectionState() polls) and it emits a
//     "connection" Event (the plane the Android UI actually subscribes to). Suppressing
//     JUST the emit leaves ConnectionState() -- and therefore every state-polling fence,
//     including this file's own first two phases before this gap was closed -- reading the
//     truth while the UI is never told. Exactly one existing test would have caught it
//     (s18b_gracewindow_test.go's grace-window fence, which holds the whole plane by
//     accident through s18bConnLog), and that test's subject is a different requirement
//     entirely. This file now asserts the event SEQUENCE too, reusing s18bConnLog rather
//     than polling ConnectionState -- the same instrument, applied on purpose this time.
//
// The row's own evidence column asks for exactly this: "tests against a flapping relay
// assert the retry ceiling, state transitions, re-auth, that no keystroke is ever
// replayed." Retry ceiling by dial gaps (phase 1), state transitions by the event sequence
// (all three phases), re-auth by the second auth_init (phase 2 vs phase 3).
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

// TestPBNET4_TheRealRunLoopGrowsReAuthenticatesAndResetsItsBackoff is PB-NET-4's fence over
// App.run, not over the backoff type in isolation.
//
// THREE OBSERVABLES, one relay, three phases -- see the package doc for why each is its own
// instrument rather than a proxy for the others:
//  1. The tap REFUSES every dial, before any websocket upgrade. Four attempts arrive; the
//     three GAPS between them must fall in section 6.0's stated bands (500ms, 1s, 2s, each
//     +/-20%) -- and because the bands do not overlap, three individually-correct gaps
//     already prove growth without computing a ratio.
//  2. The tap starts ALLOWING connections. The dial already in flight (on the backoff
//     clock) reaches the real relay, the phone comes online, and exactly one AUTH_INIT has
//     been observed -- the only way it could have, since App.dial always re-authenticates.
//  3. The tap SEVERS that one live connection without refusing what follows. The phone
//     must reconnect, send a SECOND auth_init, and do so on a gap smaller than phase 1's
//     own third gap -- which is what reset() (called in App.run right after
//     setConn(connOnline)) exists for.
//
// Threaded through all three: the connection EVENT SEQUENCE (s18bConnLog, shared with
// s18b_gracewindow_test.go), asserted exactly at the end rather than merely "some events
// arrived" -- a state-polling fence would not notice ADR-007 B114's defect (setConn's
// emit suppressed while its state write stays correct), and a fence that only checked
// presence would not notice a change that emitted the right states in the wrong order.
//
// No tight wall-clock upper bound is asserted anywhere below. Every wait uses a generous
// deadline, and the one duration comparison that matters for reset() (phase 3's reconnect
// gap) is checked RELATIVE to phase 1's own measured third gap, not against an absolute
// duration this host's load could blow through under a slow run.
func TestPBNET4_TheRealRunLoopGrowsReAuthenticatesAndResetsItsBackoff(t *testing.T) {
	h := newHarness(t)
	tap := newFlapTap(t, h.RelayURL)
	tap.setRefuse(true)

	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.AppRelayURL = tap.URL()
	h.App = h.openApp()

	log := &s18bConnLog{}
	if err := h.App.SetEventListener(log); err != nil {
		t.Fatalf("SetEventListener: %v", err)
	}

	// ---- phase 1: every dial refused, so arrival timing is the retry-ceiling observable -
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
	// WHAT A WALL CLOCK OVER A NETWORK HOP CAN HONESTLY SAY (Wave R6 review round 3,
	// finding F5(a)). An arrival gap is r + d + L: the time the refusal took to reach the
	// phone, the delay the loop SCHEDULED, and the next connection's transit. r and L are
	// non-negative and unbounded above, so the gap is bounded BELOW by the scheduled delay
	// exactly, and ABOVE by nothing this rig controls.
	//
	// This loop used to assert both edges of section 6.0's band on that quantity, and the
	// band's top IS the largest legal schedule -- zero headroom. A correct run loop failed
	// it under load: `gap #0 ... 605.604042ms, want within [400ms, 600ms]` (round-2
	// reviewer), and `gap #1 ... 1.200393917s` reproduced at GOMAXPROCS=1 under load while
	// this fix was written.
	//
	// NOTHING WAS DROPPED. The exact band moved to where the quantity exists --
	// mobile/pbnet4_rundelay_test.go's TestPBNET4_TheRunLoopSchedulesSection60sBackoff,
	// which drives this same App.run through NewApp+Start and reads the delay the loop
	// itself chose, with no tolerance at all, plus a one-sided elapsed check so a delay
	// that is computed and not waited on fails there too. What stays here is the half a
	// tap can prove: the FLOOR, which is load-immune by the algebra above and is what a
	// fixed 250 ms delay violates, and a deliberately generous ceiling that catches a
	// schedule of the wrong ORDER without asserting the host's scheduler.
	const latencyAllowance = time.Second
	for i, b := range bands {
		if gaps[i] < b.lo {
			t.Fatalf("PB-NET-4: gap #%d between dial attempts at the tap was %v, BELOW the %v "+
				"floor of section 6.0's band (initial 500ms, factor 2, jitter +/-20%%), measured "+
				"against the REAL App.run loop over a relay that refuses every dial. The gap is "+
				"the scheduled delay plus non-negative transit, so it can only be short if the "+
				"schedule is: a fixed, non-growing delay (the pre-fix 250ms, or any other "+
				"constant) fails here", i, gaps[i], b.lo)
		}
		if gaps[i] > b.hi+latencyAllowance {
			t.Fatalf("PB-NET-4: gap #%d between dial attempts at the tap was %v, over the %v "+
				"band ceiling even allowing %v for transit and scheduling. A gap this long is a "+
				"schedule of the wrong order, not a loaded host", i, gaps[i], b.hi, latencyAllowance)
		}
	}
	if n := tap.authCount(); n != 0 {
		t.Fatalf("PB-NET-4: %d auth_init frame(s) arrived while every dial was refused before "+
			"the websocket upgrade; the growth measurement above would not be trustworthy", n)
	}

	// ---- phase 2: allowed. Re-auth is the only way the pending dial reaches online. ----
	tap.setRefuse(false)
	log.await(t, "PB-NET-4: the connection event plane never reported \"online\" once the tap "+
		"stopped refusing dials. ConnectionState() is deliberately NOT polled here -- ADR-007 "+
		"B114 found setConn's emit suppressed while its state write stayed correct, which a "+
		"poll-based wait cannot see", func(s []string) bool {
		return len(s) >= 3 && s[len(s)-1] == "online"
	})
	if n := tap.authCount(); n != 1 {
		t.Fatalf("PB-NET-4: %d auth_init frame(s) observed on the first successful connection, "+
			"want exactly 1", n)
	}

	// ---- phase 3: sever the live connection; the relay stays otherwise healthy. -------
	severedAt := time.Now()
	beforeSever := len(log.snapshot())
	tap.sever()
	// The event log, not a state poll, is what proves a reconnect actually happened rather
	// than a stale "online" read surviving the severance untouched: the predicate requires
	// TWO NEW entries (a "reconnecting" the severance caused, then a fresh "online"), which
	// a read that never left "online" could not satisfy.
	log.await(t, "PB-NET-4: the phone never reconnected (event sequence) after its one live "+
		"connection was severed", func(s []string) bool {
		return len(s) >= beforeSever+2 && s[len(s)-1] == "online"
	})
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

	// ---- the event SEQUENCE, asserted exactly (ADR-007 B114). -------------------------
	got := log.snapshot()
	want := []string{"connecting", "reconnecting", "online", "reconnecting", "online"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("PB-NET-4: the connection event sequence was %v, want exactly %v. Suppressing "+
			"setConn's emit (ADR-007 B114) while leaving its state write correct produces an "+
			"empty or truncated sequence here even though ConnectionState() would still read "+
			"right -- which is the whole reason this is asserted on the event plane and not by "+
			"polling", got, want)
	}
}
