package remotegw

// Bead agents-tracker-hggx.4.3 -- FAILING-FIRST (TDD RED, GG-5) tests for PG-OBL-9
// (docs/specifications/push-gateway-api.md section 6.4): "Retry SHALL be bounded by
// expiry, not by an attempt count, and SHALL back off." Today the machine has no
// scheduler of its own: a retryable failure is retried only when a new trigger or a
// relay redial happens to call Drive again, and the gateway's Retry-After is parsed
// nowhere (wakesubmitter.go discards the header). The explicit accepted residual on the
// bead -- one transient blip on an idle machine loses the wake -- is what this suite
// closes.
//
// THE SEAM these tests pin (nothing below exists yet; GREEN supplies it; this is the
// same frozen-contract convention as docs/verification/r3-red/obligations-red.txt's
// original run):
//
//	// WakeRetryConfig configures one address's wake-obligation retry scheduler
//	// (PG-OBL-9): the timer-driven driver that re-Drives a live obligation after a
//	// retryable failure, independent of triggers and redials.
//	type WakeRetryConfig struct {
//		Machine   *WakeObligationMachine
//		Store     ObligationStore
//		Address   PushAddress
//		Now       func() time.Time                // nil => time.Now
//		After     func(d time.Duration, f func()) // nil => time.AfterFunc; the same
//		                                          // deterministic timer seam as
//		                                          // PushConfig.After
//		BaseDelay time.Duration                   // first-retry backoff (0 => default)
//	}
//	func NewWakeRetryScheduler(cfg WakeRetryConfig) *WakeRetryScheduler
//	func (s *WakeRetryScheduler) Kick(ctx context.Context)
//
// Kick's pinned semantics: drive the address's live obligation NOW (synchronously, via
// Machine.Drive); after a RETRYABLE outcome arm exactly one timer for the next attempt;
// the delay doubles per consecutive retryable failure starting from BaseDelay
// (exponential backoff); a gateway refusal carrying Retry-After is honoured -- the next
// attempt is scheduled no earlier than it, whatever the backoff says; a terminal state
// stops the scheduling; and the whole retry is bounded by the obligation's OWN expiry
// (Drive's now > expires_at branch), never by how many attempts have been spent.
// This file contains NO implementation.

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

// stampedSubmit is one submit attempt with the virtual-clock time it happened at, so
// the expiry bound is checkable against the clock rather than inferred from counts.
type stampedSubmit struct {
	at  time.Time
	env []byte
}

// clockStampingSubmitter is a WakeSubmitter that stamps every attempt with the test
// clock and returns canned outcomes in call order (last repeats), mirroring
// fakeSubmitter's outcome-list shape.
type clockStampingSubmitter struct {
	mu       sync.Mutex
	clk      *testClock
	stamps   []stampedSubmit
	outcomes []error
}

func (s *clockStampingSubmitter) SubmitWake(_ context.Context, env []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stamps = append(s.stamps, stampedSubmit{at: s.clk.Now(), env: append([]byte(nil), env...)})
	if len(s.outcomes) == 0 {
		return nil
	}
	idx := len(s.stamps) - 1
	if idx >= len(s.outcomes) {
		idx = len(s.outcomes) - 1
	}
	return s.outcomes[idx]
}

func (s *clockStampingSubmitter) all() []stampedSubmit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stampedSubmit(nil), s.stamps...)
}

// retryableRefusal is a gateway refusal the obligation machine sends back to pending
// (spec section 4: 502 upstream_unavailable, retryable=true).
func retryableRefusal() error {
	return &WakeSubmitError{Code: "upstream_unavailable", Retryable: true, Message: "test outage"}
}

// retryHarness bundles one address's machine with a scheduler over the deterministic
// clock and timer seams the package already uses (testClock, fakeTimer).
type retryHarness struct {
	addr  PushAddress
	clk   *testClock
	ft    *fakeTimer
	store *fakeObligationStore
	sub   *clockStampingSubmitter
	m     *WakeObligationMachine
	sched *WakeRetryScheduler
}

func newRetryHarness(t *testing.T, base time.Duration, outcomes ...error) *retryHarness {
	t.Helper()
	h := &retryHarness{
		addr:  testPushAddress(0x9A),
		clk:   newTestClock(),
		ft:    &fakeTimer{},
		store: newFakeObligationStore(),
	}
	h.sub = &clockStampingSubmitter{clk: h.clk, outcomes: outcomes}
	seq, err := OpenSeqSource("")
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	h.m = NewWakeObligationMachine(WakeObligationConfig{
		Store: h.store, Submitter: h.sub, WakeKey: testWakeKey(), Address: h.addr, Seq: seq, Now: h.clk.Now,
	})
	h.sched = NewWakeRetryScheduler(WakeRetryConfig{
		Machine: h.m, Store: h.store, Address: h.addr,
		Now: h.clk.Now, After: h.ft.after, BaseDelay: base,
	})
	return h
}

// pump fires scheduled retry timers in order -- advancing the virtual clock by each
// timer's own delay first, exactly as the runtime would -- until the scheduler stops
// arming new ones or max firings have happened. It returns how many timers fired.
func (h *retryHarness) pump(t *testing.T, max int) int {
	t.Helper()
	fired := 0
	for fired < max {
		sched := h.ft.scheduled()
		if fired == len(sched) {
			return fired // scheduling stopped: the retry horizon is over
		}
		if len(sched) > fired+1 {
			t.Fatalf("%d retry timers outstanding at once, want at most 1: the scheduler double-armed", len(sched)-fired)
		}
		h.clk.advance(sched[fired])
		h.ft.fire(t, fired)
		fired++
	}
	return fired
}

// TestOBL9_RetryBackoffDoublesAcrossConsecutiveRetryableFailures pins the backoff
// curve: with BaseDelay 1s and a gateway that refuses retryably every time, the
// scheduler's successive delays are 1s, 2s, 4s, 8s -- exponential, driven by its own
// timer, with no trigger or redial anywhere in the test.
func TestOBL9_RetryBackoffDoublesAcrossConsecutiveRetryableFailures(t *testing.T) {
	h := newRetryHarness(t, time.Second, retryableRefusal())
	if err := h.m.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	h.sched.Kick(context.Background())

	if got := len(h.sub.all()); got != 1 {
		t.Fatalf("Kick made %d submit attempts, want 1 (drive now, then back off)", got)
	}
	for i := 0; i < 3; i++ {
		sched := h.ft.scheduled()
		if len(sched) != i+1 {
			t.Fatalf("after attempt %d: %d retry timers scheduled, want %d -- a retryable failure must arm "+
				"the scheduler's OWN timer (PG-OBL-9), not wait for a trigger or redial", i+1, len(sched), i+1)
		}
		h.clk.advance(sched[i])
		h.ft.fire(t, i)
	}
	got := h.ft.scheduled()
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if len(got) != len(want) {
		t.Fatalf("scheduled retry delays = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("retry delay %d = %s, want %s: backoff must DOUBLE per consecutive retryable failure "+
				"from BaseDelay", i, got[i], want[i])
		}
	}
	if got := len(h.sub.all()); got != 4 {
		t.Fatalf("submit attempts after three fired retries = %d, want 4", got)
	}
}

// TestOBL9_RetryIsBoundedByExpiryNotAttemptCount pins the bound: the scheduler keeps
// retrying past any plausible attempt cap while the obligation lives, never submits
// after expires_at, marks the obligation expired once its five minutes are spent, and
// then stops arming timers entirely.
func TestOBL9_RetryIsBoundedByExpiryNotAttemptCount(t *testing.T) {
	h := newRetryHarness(t, time.Second, retryableRefusal())
	if err := h.m.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	ob, ok, err := h.store.Get(h.addr)
	if err != nil || !ok {
		t.Fatalf("store.Get after Trigger: ok=%v err=%v", ok, err)
	}
	expiresAt := ob.ExpiresAt

	h.sched.Kick(context.Background())
	fired := h.pump(t, 100)
	if fired >= 100 {
		t.Fatal("the scheduler never stopped: retry must be bounded by the obligation's expiry")
	}

	stamps := h.sub.all()
	if len(stamps) < 8 {
		t.Fatalf("only %d submit attempts across the five-minute obligation, want at least 8: an attempt "+
			"COUNT is not the bound (PG-OBL-9) -- doubling from 1s inside WakeV1Expiry leaves room for at "+
			"least eight", len(stamps))
	}
	for i, s := range stamps {
		if s.at.After(expiresAt) {
			t.Fatalf("attempt %d was submitted at %v, after the obligation's expiry %v: an expired wake is "+
				"past the FCM TTL and may never be submitted", i, s.at, expiresAt)
		}
	}
	cur, ok, err := h.store.Get(h.addr)
	if err != nil || !ok {
		t.Fatalf("store.Get after the retry horizon: ok=%v err=%v", ok, err)
	}
	if cur.State != ObligationExpired {
		t.Fatalf("obligation state after the retry horizon = %q, want %q: the expiry, not an attempt "+
			"count, is what ends the retry", cur.State, ObligationExpired)
	}
}

// writeWakeThrottled writes the spec section 4 quota refusal (429 quota_exceeded,
// retryable) carrying a Retry-After header of the given seconds -- the Throttled
// response shape of spec section 3.6.
func writeWakeThrottled(retryAfterSeconds int) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":"quota_exceeded","message":"test throttle","retryable":true}`))
	}
}

// TestOBL9_HonoursGatewayRetryAfterOverItsOwnBackoff pins Retry-After end to end
// through the REAL HTTP submitter: a 429 carrying Retry-After: 45 must push the next
// attempt out to no earlier than 45s -- BaseDelay is 1s, so backoff alone cannot
// explain the delay -- while still landing it inside the obligation's own expiry, and
// the honoured retry must then actually reach the gateway.
func TestOBL9_HonoursGatewayRetryAfterOverItsOwnBackoff(t *testing.T) {
	g := newWakeGatewayServer(t, writeWakeThrottled(45), writeWakeAccepted)

	addr := testPushAddress(0x9B)
	clk := newTestClock()
	ft := &fakeTimer{}
	store := newFakeObligationStore()
	seq, err := OpenSeqSource("")
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	m := NewWakeObligationMachine(WakeObligationConfig{
		Store: store, Submitter: newTestHTTPWakeSubmitter(g), WakeKey: testWakeKey(),
		Address: addr, Seq: seq, Now: clk.Now,
	})
	sched := NewWakeRetryScheduler(WakeRetryConfig{
		Machine: m, Store: store, Address: addr, Now: clk.Now, After: ft.after, BaseDelay: time.Second,
	})

	if err := m.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	sched.Kick(context.Background())

	if got := len(g.reqs); got != 1 {
		t.Fatalf("gateway saw %d requests after Kick, want 1", got)
	}
	sched2 := ft.scheduled()
	if len(sched2) != 1 {
		t.Fatalf("%d retry timers scheduled after a retryable 429, want 1", len(sched2))
	}
	if sched2[0] < 45*time.Second {
		t.Fatalf("retry scheduled after %s, want no earlier than the gateway's Retry-After of 45s: the "+
			"1s base backoff must not override a throttle the gateway declared (PG-OBL-9, spec section "+
			"6.4 'honour Retry-After')", sched2[0])
	}
	if sched2[0] >= WakeV1Expiry {
		t.Fatalf("retry scheduled after %s, at or beyond the obligation's own %s expiry: honouring "+
			"Retry-After must not schedule an attempt the expiry bound forbids", sched2[0], WakeV1Expiry)
	}

	clk.advance(sched2[0])
	ft.fire(t, 0)
	if got := len(g.reqs); got != 2 {
		t.Fatalf("gateway saw %d requests after the honoured delay elapsed, want 2: the throttled wake "+
			"must actually be retried", got)
	}
	cur, ok, err := store.Get(addr)
	if err != nil || !ok {
		t.Fatalf("store.Get after the honoured retry: ok=%v err=%v", ok, err)
	}
	if cur.State != ObligationDelivered {
		t.Fatalf("obligation state after the honoured retry = %q, want %q", cur.State, ObligationDelivered)
	}
}
