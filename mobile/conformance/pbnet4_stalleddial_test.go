package conformance_test

// FAILING-FIRST (TDD RED, GG-5) for the USER-VISIBLE half of the round-7 dial blocker, over
// the real facade. This file contains NO implementation.
//
// THE DEFECT, restated where it is felt. relay.dialConn applies no deadline and mobile/app.go
// hands it `context.WithCancel(context.Background())` -- cancellation, no deadline. So a relay
// that accepts the TCP connection and then STALLS parks App.dial indefinitely, and every
// consequence follows from one fact: A DIAL THAT NEVER RETURNS IS A DIAL THAT NEVER FAILS.
//
// PB-NET-4's reconnect schedule runs BETWEEN dial attempts -- App.run loops
// `time.After(rb.next())` then `a.dial(ctx)` -- so a parked dial means the backoff never
// starts, the retry that would grow and jitter and eventually reach a working relay is never
// scheduled, and the handset sits on "connecting" with no error and nothing to act on. The
// backoff is implemented, fenced and mutation-proven (pbnet4_flappingrelay_test.go), and NONE
// OF IT RUNS, because every one of those fences reaches the loop through a dial that returns.
//
// WHY A REFUSING RELAY IS NOT THIS TEST. pbnet4_flappingrelay_test.go's tap answers 503, which
// ENDS the dial -- the schedule then runs and that test measures it. This tap answers nothing
// at all, which is strictly cheaper for the adversary and is also what a half-open TCP after a
// WiFi -> cellular handoff looks like from the handset. The two taps differ in one behaviour
// and the difference is the whole defect.
//
// THE OBSERVABLE IS ARRIVALS AT THE TAP, not the connection state. A SECOND dial attempt can
// only exist if the first one returned, so counting arrivals measures the thing directly; the
// connection event plane is asserted alongside it because ADR-007 B114 found setConn's emit
// suppressed while its state write stayed correct, and a phone that recovers silently has
// still told its user nothing.
//
// WHAT THIS FENCE MEASURES, stated because it changed once. A caller-side bound was written at
// App.dial during the same round and then REVERTED when the committee ruled the dial's missing
// bound an OMISSION rather than a contract, fixable only at the boundary. While both existed
// this test passed under either, and it says so here rather than leaving a reader to assume it
// was always discriminating. App.dial now declares no deadline -- it hands relay.DialSecure the
// generation's context, Start to Stop -- so what ends the dial below is relay.DefaultDialTimeout
// inside dialConn, and unwiring that bound puts this test back to one arrival in 90 s.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// stalledDialBound is how long this test waits for behaviour that must happen. It is far above
// the dial bound it is written to accept and far below "forever", and it transcribes no
// production constant (ADR-007 B113).
const stalledDialBound = 90 * time.Second

// stallTap accepts every connection and answers NOTHING -- no upgrade, no error, no close. It
// timestamps each arrival, which is the observable: the tap is the only party that can see a
// retry the handset never got far enough to log.
type stallTap struct {
	srv  *httptest.Server
	done chan struct{}

	mu       sync.Mutex
	arrivals []time.Time
}

func newStallTap(t *testing.T) *stallTap {
	t.Helper()
	tap := &stallTap{done: make(chan struct{})}
	tap.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tap.mu.Lock()
		tap.arrivals = append(tap.arrivals, time.Now())
		tap.mu.Unlock()
		// The upgrade request is held, unanswered, until the test ends. Nothing is written,
		// so the client is parked reading a response head that never arrives.
		select {
		case <-tap.done:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(tap.done)
		tap.srv.Close()
	})
	return tap
}

func (s *stallTap) URL() string { return "ws" + strings.TrimPrefix(s.srv.URL, "http") }

func (s *stallTap) arrivalCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.arrivals)
}

// TestPBNET4_AStalledDialStillReachesTheReconnectSchedule.
//
// TWO ASSERTIONS, both halves of "the phone recovers":
//  1. A SECOND dial attempt reaches the tap. It cannot, unless the first one returned, so this
//     is a direct measurement of the bound rather than an inference from a state variable.
//  2. The connection event plane reports "reconnecting". A dial that fails silently leaves the
//     user watching a spinner that means "connecting" while the app has in fact given up on
//     this attempt and scheduled another -- the state PB-NET-4's own row names.
//
// NO GAP IS MEASURED between the arrivals. The backoff SHAPE is pbnet4_flappingrelay_test.go's
// subject and is fenced there against a relay whose refusal is instant; here every gap also
// contains a whole dial bound, so a shape assertion would measure the bound, not the schedule.
func TestPBNET4_AStalledDialStillReachesTheReconnectSchedule(t *testing.T) {
	h := newHarness(t)
	tap := newStallTap(t)

	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.AppRelayURL = tap.URL()
	h.App = h.openApp()

	log := &s18bConnLog{}
	if err := h.App.SetEventListener(log); err != nil {
		t.Fatalf("SetEventListener: %v", err)
	}

	deadline := time.Now().Add(stalledDialBound)
	for tap.arrivalCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := tap.arrivalCount(); n < 2 {
		t.Fatalf("only %d dial attempt(s) reached a relay that accepts connections and answers "+
			"nothing, after %v.\n"+
			"The first dial is still parked, so App.run never reached `time.After(rb.next())` "+
			"and PB-NET-4's reconnect schedule -- implemented, fenced and mutation-proven -- "+
			"has not run once. The handset retries never and shows the user nothing",
			n, stalledDialBound)
	}

	sawReconnecting := false
	for time.Now().Before(deadline) {
		for _, s := range log.snapshot() {
			if s == "reconnecting" {
				sawReconnecting = true
			}
		}
		if sawReconnecting {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawReconnecting {
		t.Fatalf("the connection event plane never reported \"reconnecting\" against a relay "+
			"that answers nothing; states seen, in order: %v.\n"+
			"A dial that ends without saying so leaves the user on a spinner that means "+
			"\"connecting\" while the app has already moved on", log.snapshot())
	}
}
