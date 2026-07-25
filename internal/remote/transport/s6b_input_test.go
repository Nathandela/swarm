// Slice S6b — FAILING-FIRST (TDD RED, GG-5) tests for PB-NET-5's CLIENT half: the
// live tail that turns the relay's bounded server-side wait into low-latency
// inbound delivery WITHOUT head-of-line-blocking the outbound keystroke path, and
// without spending the relay's OpsPerMin window (ADR-007 B7).
//
// THE CONTRACT these tests freeze (undefined symbols -> compile-level RED for the
// transport test binary):
//
//   - (*Session).Follow(ctx, fn func(relay.Item) error) error — the live tail.
//     It parks a bounded server-side wait, hands each delivered item to fn
//     unchanged, batch-acks, and repeats until ctx is done, returning ctx.Err().
//     It is NOT bounded by Options.RequestTimeout: §6.0 sets the non-wait request
//     timeout at 10 s and the server-side wait ceiling at 25 s, so a wait bounded
//     by the request timeout would be re-issued 2.5x more often than the protocol
//     intends and would blow the drain budget below.
//   - MaxDrainReadsPerSec = 3, MaxDrainAcksPerSec = 1 — §6.0's per-hop inbound
//     drain budget, exported so the arithmetic is legible and cannot drift
//     silently.
//
// What is deliberately NOT frozen: how Follow paces (token bucket, timer, adaptive
// gather window), whether it acks inline or off the delivery path, and how it
// interacts with Drain. Those are ADR B7's to describe.
//
// Measured on this host through the real in-process relay: MailboxAppend p50
// 12.0 ms / p95 22.5 ms / p99 28.9 ms / max 30.9 ms (n=300); MailboxAck p50
// 30.8 ms / max 129.2 ms (n=50). Every relay append and ack is one synchronous
// bolt db.Update, i.e. one fsync.
//
// The host is an Apple M1 (uname -m = arm64) running an x86_64 Go toolchain
// (GOARCH=amd64), so those numbers were taken through ROSETTA 2 TRANSLATION and
// are PESSIMISTIC relative to native arm64 — safe for a budget assertion, but a
// reason to distrust a marginal result in either direction.
//
// Only ONE assertion in this file is timing-bound (the 50 ms median in
// TestS6B_KeystrokeCompletesWhileFollowing). Every other assertion here is a RATE
// or a STRUCTURAL property — requests per second, items delivered exactly once,
// a keystroke absent from the relay after a reconnect — none of which depend on
// how fast the host is or on whether it is translated. That split is deliberate:
// the always-run gate leans on the assertions that cannot flake.
//
// This file contains NO implementation.
package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/transport"
)

// s6bWireOps decodes the tapped client->relay byte stream back into the exact
// sequence of relay FRAMES the session sent, and counts them by op.
//
// It parses frames rather than grepping for an op name on purpose: the drain
// budget is a budget on REQUESTS, and counting frames stays exact whichever way
// ADR B7 encodes the wait (a new MsgRelay op, a new frame tag, a request id
// alongside either). total is every request the session issued; ops keys the
// MsgRelay control frames by their "op" and everything else by its tag.
func s6bWireOps(sent []byte) (total int, ops map[string]int) {
	ops = map[string]int{}
	r := bytes.NewReader(sent)
	for {
		tag, payload, err := relay.ReadFrame(r)
		if err != nil {
			return total, ops
		}
		total++
		if tag == relay.MsgRelay {
			var env struct {
				Op string `json:"op"`
			}
			_ = json.Unmarshal(payload, &env)
			ops["op:"+env.Op]++
			continue
		}
		ops[fmt.Sprintf("tag:%d", tag)]++
	}
}

// s6bHandshakeFrames is how many requests transport.Dial issues before any
// application traffic: authenticate sends auth_init then auth_resp (client.go
// authenticate). Subtracted from the frame total to isolate the drain.
const s6bHandshakeFrames = 2

func s6bMedian(ds []time.Duration) time.Duration {
	c := append([]time.Duration(nil), ds...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c[len(c)/2]
}

// s6bCollector is a Follow sink that records every item it is handed.
type s6bCollector struct {
	mu    sync.Mutex
	items []relay.Item
	ready chan struct{}
	want  int
}

func s6bNewCollector(want int) *s6bCollector {
	return &s6bCollector{ready: make(chan struct{}), want: want}
}

func (c *s6bCollector) fn(it relay.Item) error {
	c.mu.Lock()
	c.items = append(c.items, it)
	done := len(c.items) == c.want
	c.mu.Unlock()
	if done {
		close(c.ready)
	}
	return nil
}

func (c *s6bCollector) all() []relay.Item {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]relay.Item(nil), c.items...)
}

// s6bFollow starts Follow in the background and returns its cancel plus a channel
// carrying its single return value.
func s6bFollow(s *transport.Session, fn func(relay.Item) error) (context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Follow(ctx, fn) }()
	return cancel, errc
}

// --- §6.0 arithmetic --------------------------------------------------------

// TestS6B_DrainBudgetArithmeticFitsTheRelayOpsWindow is the arithmetic §6.0 spells
// out, encoded so it cannot drift. mailbox_read and mailbox_ack DO meter against
// OpsPerMin (relay/server.go:766 and :798) while mailbox_append does not
// (relay/config.go:39-44). §6.0 budgeted both APPEND legs and neither DRAIN leg in
// its earlier revision, and the consequence is the regression this whole slice
// exists to prevent: at the §6.0 input rate of 8 frames/s a wait that returns on
// the FIRST item costs 8 reads/s + 8 acks/s = 960/min against a 600/min window, so
// the live tail dies with codeQuotaExceeded partway through the first minute —
// mid-demonstration.
func TestS6B_DrainBudgetArithmeticFitsTheRelayOpsWindow(t *testing.T) {
	if transport.MaxDrainReadsPerSec != 3 {
		t.Fatalf("MaxDrainReadsPerSec = %d, want 3 (§6.0 inbound drain rate, each hop)", transport.MaxDrainReadsPerSec)
	}
	if transport.MaxDrainAcksPerSec != 1 {
		t.Fatalf("MaxDrainAcksPerSec = %d, want 1 (§6.0: batched acks <= 1/s per routing id)", transport.MaxDrainAcksPerSec)
	}

	budgetPerMin := (transport.MaxDrainReadsPerSec + transport.MaxDrainAcksPerSec) * 60
	if budgetPerMin != 240 {
		t.Fatalf("drain budget = %d/min, want 240/min (§6.0)", budgetPerMin)
	}

	opsPerMin := relay.DefaultConfig().Quotas.OpsPerMin
	if budgetPerMin > opsPerMin {
		t.Fatalf("the drain budget (%d/min) does not fit the relay's OpsPerMin window (%d/min)", budgetPerMin, opsPerMin)
	}
	// The budget is set with deliberate headroom, not at the edge: the same window
	// also carries presence, token and reconnect ops on a real client.
	if budgetPerMin*2 > opsPerMin {
		t.Fatalf("the drain budget (%d/min) leaves under 2x headroom in OpsPerMin (%d/min); §6.0 sets it low precisely so the drain is not the whole window", budgetPerMin, opsPerMin)
	}

	// The regression, stated as arithmetic. §6.0's input frame rate is 8/s.
	const inputFramesPerSec = 8
	naivePerMin := (inputFramesPerSec + inputFramesPerSec) * 60
	if naivePerMin <= opsPerMin {
		t.Fatalf("a naive un-batched drain costs %d/min which now FITS OpsPerMin=%d; the regression this budget prevents is no longer expressible, so either the input rate or the quota moved and §6.0 must be re-derived", naivePerMin, opsPerMin)
	}
	if secondsToDeath := float64(opsPerMin) / float64(inputFramesPerSec*2); secondsToDeath > 60 {
		t.Fatalf("a naive drain would survive %.0fs, i.e. past the whole tumbling window; the arithmetic §6.0 records (~37s) no longer holds", secondsToDeath)
	}
}

// --- the drain budget, on the wire ------------------------------------------

// TestS6B_SustainedTypingStaysInsideTheDrainBudget is the regression test for the
// "dies with codeQuotaExceeded partway through the first minute" failure. It runs
// the live tail against a REAL relay through the recording wireTap and counts the
// requests the session actually put on the wire, so the assertion is on observed
// traffic rather than on an interface a fake could get wrong.
//
// A wait that returns on the first item and acks it costs one read + one ack per
// keystroke: 2 requests per item, 48 requests here. §6.0's budget allows 3 reads/s
// and 1 ack/s. The separation is ~4x on a 3 s window, and it grows linearly with
// the run, so the assertion does not depend on host speed.
func TestS6B_SustainedTypingStaysInsideTheDrainBudget(t *testing.T) {
	// §6.0: input frame rate <= 8 frames/s sustained, i.e. one frame per 125 ms.
	const (
		frames = 24
		pacing = 125 * time.Millisecond
	)
	srv, url := startRelay(t, nil)
	tap := newWireTap(t, url)
	p := newPeers(t, srv)
	sess := devSession(t, tap.URL(), p, nil)

	coll := s6bNewCollector(frames)
	cancel, errc := s6bFollow(sess, coll.fn)
	defer func() {
		cancel()
		<-errc
	}()

	start := time.Now()
	for i := 0; i < frames; i++ {
		env := p.seal(t, uint64(i+1), []byte("k"))
		if _, err := p.machine.MailboxAppend(testCtx(t), p.deviceRID, env); err != nil {
			t.Fatalf("machine append %d: %v", i, err)
		}
		time.Sleep(pacing)
	}
	select {
	case <-coll.ready:
	case <-time.After(30 * time.Second):
		t.Fatalf("Follow delivered %d of %d frames; the live tail is not draining", len(coll.all()), frames)
	}
	elapsed := time.Since(start)

	// Every frame exactly once: batching must not lose or duplicate a keystroke.
	seen := map[uint64]int{}
	for _, it := range coll.all() {
		seen[it.Cursor]++
	}
	if len(seen) != frames {
		t.Fatalf("Follow delivered %d distinct cursors over %d appended frames; batching must lose nothing", len(seen), frames)
	}
	for c, n := range seen {
		if n != 1 {
			t.Fatalf("Follow delivered cursor %d %d times; the live tail must not duplicate", c, n)
		}
	}

	total, ops := s6bWireOps(tap.Sent())
	drainRequests := total - s6bHandshakeFrames
	secs := elapsed.Seconds()

	// +2 on each bound absorbs the window boundaries: one request may be in flight
	// when the run starts and one more is needed to flush the tail.
	maxReads := int(float64(transport.MaxDrainReadsPerSec)*secs) + 2
	maxAcks := int(float64(transport.MaxDrainAcksPerSec)*secs) + 2
	if drainRequests > maxReads+maxAcks {
		t.Fatalf("the live tail issued %d requests over %.1fs draining %d frames (ops=%v); §6.0 allows <=%d reads + <=%d acks = %d. An un-batched wait costs one read AND one ack per keystroke, which is %d/min at 8 frames/s against OpsPerMin=%d — the tail dies with codeQuotaExceeded mid-demonstration",
			drainRequests, secs, frames, ops, maxReads, maxAcks, maxReads+maxAcks, 16*60, relay.DefaultConfig().Quotas.OpsPerMin)
	}
	if acks := ops["op:mailbox_ack"]; acks > maxAcks {
		t.Fatalf("the live tail issued %d acks over %.1fs; §6.0 requires BATCHED acks at <=%d/s (<=%d here). Acks are not free: a relay ack is one bolt fsync, measured p50 30.8ms / max 129.2ms on this host, so an un-batched ack also sits on the keystroke path",
			acks, secs, transport.MaxDrainAcksPerSec, maxAcks)
	}
}

// TestS6B_FollowIsNotTruncatedByTheNonWaitRequestTimeout: §6.0 sets the non-wait
// request timeout at 10 s and the server-side wait ceiling at 25 s. Applying the
// former to the latter would silently re-issue every wait 2.5x more often than the
// protocol intends, which is invisible in a latency test and fatal in the quota
// arithmetic above. Asserted on an IDLE mailbox with a deliberately tiny
// RequestTimeout, so a truncating implementation shows up as a burst of requests
// in a window where a correct one is silent.
func TestS6B_FollowIsNotTruncatedByTheNonWaitRequestTimeout(t *testing.T) {
	const (
		requestTimeout = 100 * time.Millisecond
		quiet          = 1200 * time.Millisecond
	)
	srv, url := startRelay(t, nil)
	tap := newWireTap(t, url)
	p := newPeers(t, srv)
	sess := devSession(t, tap.URL(), p, func(o *transport.Options) {
		o.RequestTimeout = requestTimeout
	})

	coll := s6bNewCollector(1)
	cancel, errc := s6bFollow(sess, coll.fn)
	defer func() {
		cancel()
		<-errc
	}()

	time.Sleep(quiet)
	total, ops := s6bWireOps(tap.Sent())
	idleRequests := total - s6bHandshakeFrames

	// A wait truncated at RequestTimeout would have been re-issued ~12 times here.
	// The §6.0 drain budget allows 3/s -> at most 3 in this window, +1 in flight.
	maxIdle := int(float64(transport.MaxDrainReadsPerSec)*quiet.Seconds()) + 1
	if idleRequests > maxIdle {
		t.Fatalf("the live tail issued %d requests over %v on an IDLE mailbox (ops=%v), want <=%d; a wait bounded by Options.RequestTimeout=%v is re-issued far more often than §6.0's 25 s ceiling intends",
			idleRequests, quiet, ops, maxIdle, requestTimeout)
	}

	// And it is a live tail, not a stalled one: an item arriving after that quiet
	// period is still delivered, promptly.
	env := p.seal(t, 1, []byte("after-idle"))
	appended := time.Now()
	if _, err := p.machine.MailboxAppend(testCtx(t), p.deviceRID, env); err != nil {
		t.Fatalf("append after the quiet window: %v", err)
	}
	select {
	case <-coll.ready:
		if d := time.Since(appended); d > 2*time.Second {
			t.Fatalf("an item appended after a quiet window took %v to reach Follow", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an item appended after a quiet window never reached Follow; the wait went idle instead of staying live")
	}
}

// --- acceptance clause (a), at the transport ---------------------------------

// TestS6B_KeystrokeCompletesWhileFollowing is clause (a) at the layer where
// today's head-of-line block actually lives: Conn.roundtrip holds c.mu across
// write-then-blocking-read (relay/client.go:214-233), so with a wait outstanding
// on one connection a SendLive on the SAME session cannot even be written until
// the wait's reply is read.
//
// STRUCTURAL (cannot flake): every SendLive returns without error while Follow's
// wait is outstanding. Under today's client each one blocks until its own
// Options.RequestTimeout (10 s default) and fails — a ~200x separation from the
// 50 ms budget, not a 2x one.
//
// BUDGET (§6.0's number): the median of 32 such sends is <= 50 ms. The median
// rather than the max, because the measured cost of a single relay append on this
// idle host is p50 12 ms / p99 28.9 ms / max 30.9 ms (one bolt fsync each) — a max
// assertion at 50 ms would sit 1.6x from the observed worst case and would be the
// first thing to flake on a loaded CI box.
func TestS6B_KeystrokeCompletesWhileFollowing(t *testing.T) {
	srv, url := startRelay(t, nil)
	p := newPeers(t, srv)
	sess := devSession(t, url, p, nil)

	// Baseline: the same sends with NO wait outstanding.
	baseline := make([]time.Duration, 0, 32)
	for i := 0; i < 32; i++ {
		start := time.Now()
		if err := sess.SendLive(testCtx(t), p.machineRID, []byte("k")); err != nil {
			t.Fatalf("baseline SendLive %d: %v", i, err)
		}
		baseline = append(baseline, time.Since(start))
	}

	coll := s6bNewCollector(1 << 30) // never "ready": this test only needs the wait parked
	cancel, errc := s6bFollow(sess, coll.fn)
	defer func() {
		cancel()
		<-errc
	}()
	time.Sleep(300 * time.Millisecond) // let the wait reach the relay

	withWait := make([]time.Duration, 0, 32)
	for i := 0; i < 32; i++ {
		start := time.Now()
		if err := sess.SendLive(testCtx(t), p.machineRID, []byte("k")); err != nil {
			t.Fatalf("SendLive %d with a wait outstanding: %v — a keystroke must not queue behind the live tail's wait (PB-NET-5(a))", i, err)
		}
		withWait = append(withWait, time.Since(start))
	}

	if med := s6bMedian(withWait); med > 50*time.Millisecond {
		t.Fatalf("median SendLive with the live tail's wait outstanding = %v, want <= 50ms (§6.0, PB-NET-5(a)); baseline median without = %v", med, s6bMedian(baseline))
	}
	if med, base := s6bMedian(withWait), s6bMedian(baseline); med > 4*base+10*time.Millisecond {
		t.Fatalf("median SendLive with a wait outstanding = %v vs %v without; the keystroke path is being delayed by the wait rather than running independently of it", med, base)
	}
}

// --- ADR-007 D7: input is live-only, never queued or replayed ---------------

// TestS6B_KeystrokeNeverSurvivesADisconnectWhileFollowing is the D7 fence around
// the new mechanism. Correlating requests means holding per-request state on the
// client — a pending map keyed by request id — and that is exactly the structure
// into which a keystroke could quietly be buffered and re-sent to make latency
// look good. D7 (ADR-007:60-62) forbids it: a keystroke replayed minutes later is
// a command the user typed once being executed twice against a different terminal
// state.
//
// Asserted at the RELAY, not at the client's own bookkeeping: the machine's
// mailbox depth must still be zero after the reconnect. resilience_test.go's
// TestLiveFramesAreNeverQueuedAndNeverReplayed proves this for a plain session;
// this proves it survives the concurrent-dispatch model, with a wait outstanding
// at the moment the link dies.
func TestS6B_KeystrokeNeverSurvivesADisconnectWhileFollowing(t *testing.T) {
	srv, url := startRelay(t, nil)
	tap := newWireTap(t, url)
	p := newPeers(t, srv)
	sleeps := &recordingSleep{}
	sess := devSession(t, tap.URL(), p, func(o *transport.Options) { o.Sleep = sleeps.fn })

	coll := s6bNewCollector(1)
	cancel, errc := s6bFollow(sess, coll.fn)
	defer func() {
		cancel()
		<-errc
	}()
	time.Sleep(300 * time.Millisecond)

	if d := srv.MailboxDepth(p.machineRID); d != 0 {
		t.Fatalf("machine mailbox depth = %d before the test sent anything", d)
	}

	// Kill the link with the wait outstanding. Refuse(true) FIRST: without it the session
	// reconnects immediately (recordingSleep collapses the backoff to ~2ms) while waitState
	// sleeps 5ms before its first check, so StateDisconnected is unobservable and the outage
	// this test needs never exists. Every other disconnect test in this package does the same
	// (resilience_test.go:216-218); this one omitted it. Verified independent of the S6b
	// implementation: the identical Cut-then-await sequence with no Follow at all fails the
	// same way.
	tap.Refuse(true)
	tap.Cut()
	waitState(t, sess, transport.StateDisconnected)

	// A keystroke attempted with no live connection is REFUSED, never held.
	err := sess.SendLive(testCtx(t), p.machineRID, []byte("never-replay-me"))
	if !errors.Is(err, transport.ErrNotDelivered) {
		t.Fatalf("SendLive while disconnected returned %v, want ErrNotDelivered (ADR-007 D7: live-only, delivery unknown / not sent)", err)
	}
	if q := sess.Queued(); q != 0 {
		t.Fatalf("Queued() = %d after a refused live send; a keystroke must never enter the idempotent-op queue (D7)", q)
	}

	tap.Refuse(false)
	waitState(t, sess, transport.StateConnected)
	// Give any (forbidden) replay every chance to land before asserting absence.
	time.Sleep(500 * time.Millisecond)
	if d := srv.MailboxDepth(p.machineRID); d != 0 {
		t.Fatalf("machine mailbox depth = %d after reconnect; the keystroke refused during the outage was replayed. Input is live-only and is NEVER queued or replayed (ADR-007 D7) — a correlation/pending map must not become a keystroke buffer", d)
	}

	// PB-NET-5(c) reconnect behaviour: the live tail resumes after the drop.
	env := p.seal(t, 1, []byte("after-reconnect"))
	if _, err := p.machine.MailboxAppend(testCtx(t), p.deviceRID, env); err != nil {
		t.Fatalf("append after reconnect: %v", err)
	}
	select {
	case <-coll.ready:
	case <-time.After(10 * time.Second):
		t.Fatal("Follow never delivered an item appended after the reconnect; the live tail did not resume (PB-NET-5(c))")
	}
}

// TestS6B_FollowStopsCleanlyAndLeaksNoGoroutine is PB-NET-7 hygiene for the new
// path: a wait parks a goroutine on both sides for up to 25 s, so a Follow that
// does not unwind on cancel leaks one per reconnect cycle on a handset that
// reconnects all day.
func TestS6B_FollowStopsCleanlyAndLeaksNoGoroutine(t *testing.T) {
	srv, url := startRelay(t, nil)
	p := newPeers(t, srv)
	baseline := settledGoroutines()

	for i := 0; i < 5; i++ {
		sess := devSession(t, url, p, nil)
		cancel, errc := s6bFollow(sess, func(relay.Item) error { return nil })
		time.Sleep(150 * time.Millisecond) // park the wait
		cancel()
		select {
		case err := <-errc:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Follow returned %v on cancel, want context.Canceled", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("Follow did not return within 5s of cancel on cycle %d; a parked wait must unwind on cancellation, not sit out its 25 s ceiling", i)
		}
		if err := sess.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	assertNoLeak(t, baseline)
}
