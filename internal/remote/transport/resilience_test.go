// PB-NET-4 (FAILING FIRST): resilience.
//
//	Automatic reconnect, bounded exponential backoff with the §6.0 numbers
//	(initial 500 ms, factor 2, ceiling 30 s, jitter +/-20%), re-authentication
//	after reconnect, connection state surfaced -- and, the property that actually
//	protects the user, INPUT AND RESIZE ARE NEVER QUEUED OR REPLAYED
//	(ADR-007 D7: live-only, a queued keystroke on disconnect resolves to an
//	explicit "delivery unknown / not sent"). Only high-level idempotent ops may
//	queue, bounded at 64 with a reject-new refusal that is never a silent drop.
//
// A replayed keystroke is not a lost-frame bug: it is a command the user typed
// once being executed twice, minutes later, against a different terminal state.
// That is why the assertion here is on the WIRE (the bytes never appear at all),
// not on a return value.
package transport_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/transport"
)

// TestBackoffBudgetIsTheCommitteeBudget pins the §6.0 numbers as constants, so a
// later "tuning" commit that changes them is a visible test failure rather than a
// silent drift from an approved budget.
func TestBackoffBudgetIsTheCommitteeBudget(t *testing.T) {
	if transport.InitialBackoff != 500*time.Millisecond {
		t.Errorf("InitialBackoff = %v, want 500ms (§6.0)", transport.InitialBackoff)
	}
	if transport.BackoffFactor != 2 {
		t.Errorf("BackoffFactor = %v, want 2 (§6.0)", transport.BackoffFactor)
	}
	if transport.MaxBackoff != 30*time.Second {
		t.Errorf("MaxBackoff = %v, want 30s (§6.0)", transport.MaxBackoff)
	}
	if transport.BackoffJitter != 0.20 {
		t.Errorf("BackoffJitter = %v, want 0.20 (§6.0)", transport.BackoffJitter)
	}
	if transport.OpQueueLimit != 64 {
		t.Errorf("OpQueueLimit = %d, want 64 (§6.0)", transport.OpQueueLimit)
	}
}

// TestBackoffScheduleDoublesToACeiling asserts the un-jittered schedule: 500 ms
// doubling to a hard 30 s ceiling that it never exceeds, however long the relay
// stays down.
func TestBackoffScheduleDoublesToACeiling(t *testing.T) {
	want := []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	for i, w := range want {
		if got := transport.BackoffDelay(i + 1); got != w {
			t.Errorf("BackoffDelay(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := transport.BackoffDelay(64); got != transport.MaxBackoff {
		t.Errorf("BackoffDelay(64) = %v, want the %v ceiling (no overflow past it)", got, transport.MaxBackoff)
	}
}

// TestReconnectDelaysStayWithinTheJitterBand drives a relay that stays down and
// asserts every delay the session actually waits sits inside its attempt's +/-20%
// band -- and that jitter is really applied, so a fleet of phones reconnecting
// after a relay restart does not arrive as one synchronised thundering herd.
func TestReconnectDelaysStayWithinTheJitterBand(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	tap := newWireTap(t, srv.URL())
	sleep := &recordingSleep{}
	s := devSession(t, tap.URL(), p, func(o *transport.Options) { o.Sleep = sleep.fn })

	tap.Refuse(true)
	tap.Cut()

	deadline := time.Now().Add(5 * time.Second)
	for len(sleep.all()) < 8 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	delays := sleep.all()
	if len(delays) < 8 {
		t.Fatalf("session made %d reconnect attempts in 5s; automatic reconnect is not running", len(delays))
	}

	jittered := false
	for i, d := range delays {
		base := transport.BackoffDelay(i + 1)
		lo := time.Duration(float64(base) * (1 - transport.BackoffJitter))
		hi := time.Duration(float64(base) * (1 + transport.BackoffJitter))
		if d < lo || d > hi {
			t.Fatalf("reconnect attempt %d waited %v, outside the [%v, %v] band for a %v base delay", i+1, d, lo, hi, base)
		}
		if d != base {
			jittered = true
		}
		if d > time.Duration(float64(transport.MaxBackoff)*(1+transport.BackoffJitter)) {
			t.Fatalf("reconnect attempt %d waited %v, past the jittered ceiling", i+1, d)
		}
	}
	if !jittered {
		t.Fatalf("every reconnect delay equalled its base exactly: no jitter is being applied")
	}

	tap.Refuse(false)
	waitState(t, s, transport.StateConnected)
}

// TestConnectionStateIsSurfaced asserts the state machine a UI binds to: the phone
// must be able to say "reconnecting" rather than silently pretending to be live.
func TestConnectionStateIsSurfaced(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	tap := newWireTap(t, srv.URL())

	var mu sync.Mutex
	var seen []transport.State
	record := func(st transport.State) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, st)
	}
	snapshot := func() []transport.State {
		mu.Lock()
		defer mu.Unlock()
		return append([]transport.State(nil), seen...)
	}

	sleep := &recordingSleep{}
	s := devSession(t, tap.URL(), p, func(o *transport.Options) {
		o.OnState = record
		o.Sleep = sleep.fn
	})
	if s.State() != transport.StateConnected {
		t.Fatalf("state after Dial = %q, want %q", s.State(), transport.StateConnected)
	}

	tap.Cut()
	waitState(t, s, transport.StateConnected) // it comes back on its own
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitState(t, s, transport.StateClosed)

	want := []transport.State{
		transport.StateConnecting,
		transport.StateConnected,
		transport.StateDisconnected,
		transport.StateConnecting,
		transport.StateConnected,
		transport.StateClosed,
	}
	if !isSubsequence(want, snapshot()) {
		t.Fatalf("observed states %v do not contain the transition sequence %v", snapshot(), want)
	}
}

// TestReAuthenticatesAfterReconnect asserts the reconnect is a full relay-auth
// signed-challenge handshake, not a resumed socket: the relay binds a connection to
// a routing id only through auth_init/auth_resp, so anything less leaves the session
// connected but unroutable.
func TestReAuthenticatesAfterReconnect(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	tap := newWireTap(t, srv.URL())
	sleep := &recordingSleep{}
	s := devSession(t, tap.URL(), p, func(o *transport.Options) { o.Sleep = sleep.fn })

	rid := s.RoutingID()
	if rid != p.deviceRID {
		t.Fatalf("routing id = %q, want %q", rid, p.deviceRID)
	}
	if n := strings.Count(string(tap.Sent()), "auth_init"); n != 1 {
		t.Fatalf("observed %d auth_init on the wire after the first connect, want 1", n)
	}

	tap.Cut()
	waitState(t, s, transport.StateConnected)

	if n := strings.Count(string(tap.Sent()), "auth_init"); n < 2 {
		t.Fatalf("observed %d auth_init on the wire after a reconnect, want at least 2: the session did not re-authenticate", n)
	}
	if got := s.RoutingID(); got != rid {
		t.Fatalf("routing id changed across reconnect: %q -> %q", rid, got)
	}

	// The re-authenticated session must actually be usable, not merely handshaken.
	if err := s.SendOp(testCtx(t), p.machineRID, []byte("post-reconnect-op")); err != nil {
		t.Fatalf("SendOp after reconnect: %v", err)
	}
}

// TestLiveFramesAreNeverQueuedAndNeverReplayed is ADR-007 D7's guard. A keystroke
// typed while the link is down must fail loudly and must NEVER be delivered later;
// an idempotent op typed at the same moment must be held and delivered exactly once.
func TestLiveFramesAreNeverQueuedAndNeverReplayed(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	tap := newWireTap(t, srv.URL())
	sleep := &recordingSleep{}
	s := devSession(t, tap.URL(), p, func(o *transport.Options) { o.Sleep = sleep.fn })

	tap.Refuse(true)
	tap.Cut()
	waitState(t, s, transport.StateDisconnected)

	keystroke := []byte("KEYSTROKE-MUST-NEVER-BE-REPLAYED")
	resize := []byte("RESIZE-MUST-NEVER-BE-REPLAYED")
	op := []byte("IDEMPOTENT-OP-MUST-BE-DELIVERED-ONCE")

	for _, live := range [][]byte{keystroke, resize} {
		err := s.SendLive(testCtx(t), p.machineRID, live)
		if err == nil {
			t.Fatalf("SendLive succeeded with no live connection; a keystroke was silently queued")
		}
		if !errors.Is(err, transport.ErrNotDelivered) {
			t.Fatalf("SendLive offline: got %v, want ErrNotDelivered (explicit delivery unknown / not sent)", err)
		}
	}
	if n := s.Queued(); n != 0 {
		t.Fatalf("%d item(s) queued after two live sends; live frames must never enter the queue", n)
	}

	if err := s.SendOp(testCtx(t), p.machineRID, op); err != nil {
		t.Fatalf("SendOp offline: %v (an idempotent op may queue)", err)
	}
	if n := s.Queued(); n != 1 {
		t.Fatalf("queued = %d after one idempotent op, want 1", n)
	}

	tap.Refuse(false)
	waitState(t, s, transport.StateConnected)
	waitQueueDrained(t, s)

	on := string(tap.Sent())
	if n := strings.Count(on, b64(keystroke)); n != 0 {
		t.Fatalf("the keystroke reached the relay %d time(s) after reconnect: input was replayed (ADR-007 D7)", n)
	}
	if n := strings.Count(on, b64(resize)); n != 0 {
		t.Fatalf("the resize reached the relay %d time(s) after reconnect: resize was replayed (ADR-007 D7)", n)
	}
	if n := strings.Count(on, b64(op)); n != 1 {
		t.Fatalf("the idempotent op reached the relay %d time(s), want exactly 1", n)
	}
}

// TestIdempotentOpQueueIsBoundedAndRejectsNew asserts the §6.0 bound: 64 ops held,
// the 65th REFUSED with an error, and the refused op never delivered. "Reject-new
// with an error, never a silent drop" is the whole point -- a dropped kill or
// approve that the user believes was accepted is worse than a refusal.
func TestIdempotentOpQueueIsBoundedAndRejectsNew(t *testing.T) {
	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	tap := newWireTap(t, srv.URL())
	sleep := &recordingSleep{}
	s := devSession(t, tap.URL(), p, func(o *transport.Options) { o.Sleep = sleep.fn })

	tap.Refuse(true)
	tap.Cut()
	waitState(t, s, transport.StateDisconnected)

	ops := make([][]byte, transport.OpQueueLimit)
	for i := range ops {
		ops[i] = []byte(fmt.Sprintf("QUEUED-OP-%03d-XXXXXXXX", i))
		if err := s.SendOp(testCtx(t), p.machineRID, ops[i]); err != nil {
			t.Fatalf("SendOp #%d of %d: %v", i+1, transport.OpQueueLimit, err)
		}
	}
	if n := s.Queued(); n != transport.OpQueueLimit {
		t.Fatalf("queued = %d, want %d", n, transport.OpQueueLimit)
	}

	overflow := []byte("OVERFLOW-OP-MUST-BE-REFUSED")
	err := s.SendOp(testCtx(t), p.machineRID, overflow)
	if err == nil {
		t.Fatalf("the %dth op was accepted: the queue is unbounded or silently dropping", transport.OpQueueLimit+1)
	}
	if !errors.Is(err, transport.ErrOpQueueFull) {
		t.Fatalf("overflow error: got %v, want ErrOpQueueFull", err)
	}
	if n := s.Queued(); n != transport.OpQueueLimit {
		t.Fatalf("queued = %d after the refusal, want %d (a refusal must not evict)", n, transport.OpQueueLimit)
	}

	tap.Refuse(false)
	waitState(t, s, transport.StateConnected)
	waitQueueDrained(t, s)

	on := string(tap.Sent())
	for i, o := range ops {
		if n := strings.Count(on, b64(o)); n != 1 {
			t.Fatalf("queued op #%d reached the relay %d time(s), want exactly 1", i, n)
		}
	}
	if n := strings.Count(on, b64(overflow)); n != 0 {
		t.Fatalf("the refused op was delivered anyway (%d time(s)); a refusal must be final", n)
	}
}

// pacingGate is the flush-pacing seam. It HOLDS every pacing wait until the test
// releases it, while letting the reconnect backoff through, so "the drain stopped at
// the pacer" is an observed behaviour rather than a timing guess. The two waits are
// told apart by duration and cannot be confused: a reconnect wait is a jittered
// BackoffDelay, whose smallest possible value is 0.8 * InitialBackoff = 400 ms, while
// a pacing wait is a fraction of a second.
type pacingGate struct {
	release chan struct{}

	mu    sync.Mutex
	waits []time.Duration
}

func newPacingGate() *pacingGate { return &pacingGate{release: make(chan struct{})} }

func (g *pacingGate) fn(ctx context.Context, d time.Duration) error {
	if d >= time.Duration(float64(transport.InitialBackoff)*(1-transport.BackoffJitter)) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Millisecond):
			return nil
		}
	}
	g.mu.Lock()
	g.waits = append(g.waits, d)
	g.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.release:
		return nil
	}
}

func (g *pacingGate) paced() []time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]time.Duration(nil), g.waits...)
}

func (g *pacingGate) releaseAll() { close(g.release) }

// TestReconnectDrainIsPacedNotABurst is §6.0's anti-burst clause: "PB-NET-4's 64-op
// reconnect drain must not be issued as one burst". It exists because the relay's
// limiter is a TUMBLING one-minute window (relay/server.go:105-115 resets the window
// once a minute has elapsed since its start) rather than a smooth rate, so a burst
// exhausts a window early and the tail of the drain -- plus every legitimate frame
// sharing that window -- is refused with a quota error.
//
// The assertion is that the drain STOPS at the pacer with work still held, which no
// tight loop can satisfy, and that pacing does not cost exactly-once delivery.
func TestReconnectDrainIsPacedNotABurst(t *testing.T) {
	const queued = 8

	srv, _ := startRelay(t, nil)
	p := newPeers(t, srv)
	tap := newWireTap(t, srv.URL())
	gate := newPacingGate()
	s := devSession(t, tap.URL(), p, func(o *transport.Options) { o.Sleep = gate.fn })

	tap.Refuse(true)
	tap.Cut()
	waitState(t, s, transport.StateDisconnected)

	ops := make([][]byte, queued)
	for i := range ops {
		ops[i] = []byte(fmt.Sprintf("DRAIN-OP-%03d-XXXXXXXX", i))
		if err := s.SendOp(testCtx(t), p.machineRID, ops[i]); err != nil {
			t.Fatalf("SendOp #%d of %d: %v", i+1, queued, err)
		}
	}

	tap.Refuse(false)
	waitState(t, s, transport.StateConnected)

	deadline := time.Now().Add(5 * time.Second)
	for len(gate.paced()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(gate.paced()) == 0 {
		t.Fatalf("the reconnect drain never paced: %d held ops were issued back to back as one burst (§6.0)", queued)
	}
	if n := countOnWire(tap.Sent(), ops); n > 1 {
		t.Fatalf("%d of %d held ops reached the relay before the drain paced once: the drain is a burst", n, queued)
	}
	for i, d := range gate.paced() {
		if min := time.Second / 8; d < min {
			t.Fatalf("pacing wait %d was %v: §6.0 budgets <= 8 appends/s, so the gap between drained ops must be at least %v", i+1, d, min)
		}
	}

	gate.releaseAll()
	waitQueueDrained(t, s)

	on := string(tap.Sent())
	for i, o := range ops {
		if n := strings.Count(on, b64(o)); n != 1 {
			t.Fatalf("held op #%d reached the relay %d time(s) after the paced drain, want exactly 1", i, n)
		}
	}
	if got := len(gate.paced()); got < queued-1 {
		t.Fatalf("the drain paced %d time(s) for %d held ops, want at least %d: ops were still issued back to back", got, queued, queued-1)
	}
}

// countOnWire counts how many of ops appear in the bytes the client sent.
func countOnWire(on []byte, ops [][]byte) int {
	n := 0
	for _, o := range ops {
		n += strings.Count(string(on), b64(o))
	}
	return n
}

// waitQueueDrained blocks until the session has flushed its held ops.
func waitQueueDrained(t *testing.T, s *transport.Session) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.Queued() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("queue still holds %d op(s) after reconnect; it never drained", s.Queued())
}

// b64 renders a payload the way the relay's JSON control frames carry it, so a wire
// assertion looks for what is actually on the wire.
func b64(p []byte) string { return base64.StdEncoding.EncodeToString(p) }

// isSubsequence reports whether want appears in order (not necessarily adjacently)
// inside got. Reconnect may emit extra attempts; the ORDER is what is pinned.
func isSubsequence(want, got []transport.State) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}
