// Slice S6b — FAILING-FIRST (TDD RED, GG-5) tests for PB-NET-5's RELAY half: the
// bounded server-side wait plus the request-id correlation / concurrent dispatch
// that makes it usable (ADR-007 B7, docs/adr/ADR-007-remote-access.md:760).
//
// Why the relay needs a protocol change at all (requirements §4.5): today
// Conn.roundtrip holds c.mu across write-then-blocking-read (client.go:214-233)
// and serveConn is strictly readFrame -> dispatch -> readFrame (server.go:382-390),
// so a blocking handler stalls the whole connection in BOTH directions. A naive
// long-poll therefore head-of-line-blocks the very keystrokes it exists to
// accelerate, and a second connection is not available because registerSession
// binds one conn per routing id with newest-wins takeover (server.go:675-691),
// which revoke and presence severance depend on.
//
// THE CONTRACT these tests freeze (undefined symbols -> compile-level RED; Go
// builds one test binary per package, so the whole relay test build fails until
// the implementer supplies them — the same convention bounds_test.go used):
//
//   - Config.MaxServerWait time.Duration — the server-side wait ceiling, §6.0
//     default 25 s. A Config field rather than a bare const so a test can shorten
//     it, exactly as HandshakeTimeout/RendezvousTTL are configured.
//   - (*Client).MailboxWait(ctx, cursor uint64) ([]Item, bool, error) — blocks
//     SERVER-side until at least one item past cursor exists in the caller's own
//     mailbox and returns that bounded page (same {items, has_more} shape as
//     MailboxReadPage); returns an empty page and a nil error at the ceiling.
//     It returns the ITEMS, not a bare signal: a wait that only signalled would
//     cost a wait + a read per batch and double the metered drain, which §6.0's
//     <=240/min drain budget cannot absorb.
//   - ErrWaitInProgress — the clean refusal of a SECOND concurrent wait on one
//     client (§6.0: max concurrent pending waits per client = 1, refused, never
//     queued).
//
// What is deliberately NOT frozen here, so ADR B7 keeps room to describe its own
// mechanism: the wire encoding of the request id, whether the correlation id is a
// new JSON field or a new frame tag, the server's internal wakeup mechanism, and
// whether a wait is served from a per-mailbox condition variable or an internal
// poll. Every assertion below is on observable behaviour at the exported client
// surface, with one deliberate exception noted in the unauthenticated-wait test.
//
// Timing bounds and what they were measured against.
//
// ENVIRONMENT, stated precisely because "M1" alone would be misleading: the host
// is an Apple M1 (uname -m = arm64) but the Go toolchain is x86_64
// (/usr/local/bin/go is Mach-O x86_64; `go version` reports darwin/amd64;
// GOARCH=GOHOSTARCH=amd64), so these numbers were taken through ROSETTA 2
// TRANSLATION. Translation is a cost, so they are PESSIMISTIC relative to a native
// arm64 build: a bound met here is met natively with margin. That is never a
// reason to loosen a bound; it is a reason to distrust a MARGINAL result. The
// clause (b) harness (internal/skeleton/s6b_input_latency_test.go) records this
// pair and the derived translation flag on every run, per §6.0's "CI records the
// environment".
//
// On that host, otherwise idle, a full loopback relay round-trip through the
// real bbolt store measures: MailboxAppend p50 12.0 ms / p95 22.5 ms / p99 28.9 ms
// / max 30.9 ms over n=300; MailboxAck p50 30.8 ms / max 129.2 ms over n=50. Every
// append and ack is a synchronous bolt db.Update (store.go:58-78), i.e. one fsync.
// So §6.0's "<= 50 ms for the append call to complete" has only ~1.7x headroom over
// the observed p99 on an IDLE machine. Each timing assertion below is therefore
// paired with a STRUCTURAL assertion that cannot flake: a head-of-line-blocked
// append does not take 60 ms, it takes the whole MaxServerWait (25 s) — a ~1000x
// separation, not a 2x one.
//
// This file contains NO implementation.
package relay

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

// --- fixtures ---------------------------------------------------------------

// s6bPeers is an authenticated machine plus an authorized device. It exists
// alongside mailboxFixture because these tests also need the device's ClientAuth:
// the newest-wins fence opens a SECOND connection for the SAME routing id, which
// mailboxFixture's return values cannot express.
type s6bPeers struct {
	machine *Client
	device  *Client
	devAuth ClientAuth
	devRID  string
	machRID string
	sp      sealParty
}

func s6bFixture(t *testing.T, srv *Server) s6bPeers {
	t.Helper()
	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	devAuth := authFor(dPub, dPriv)
	device := dialAuthed(t, srv.URL(), devAuth)
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub), consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	return s6bPeers{
		machine: machine,
		device:  device,
		devAuth: devAuth,
		devRID:  RoutingID(dPub),
		machRID: RoutingID(mPub),
		sp:      newSealParty(t, []byte("machine-sender-pub-000000000000x"), []byte("device-recipient-pub-0000000000x")),
	}
}

// s6bWaitOutcome is one MailboxWait return, carried off the goroutine that made it.
type s6bWaitOutcome struct {
	items   []Item
	hasMore bool
	err     error
}

// s6bParkWait issues a MailboxWait on cli in the background and returns a channel
// carrying its single outcome, plus the cancel that releases it. It blocks until
// the wait has plausibly reached the server (settle) and fails the test if the
// wait returned during that window, so every caller downstream may rely on "a wait
// is outstanding right now".
func s6bParkWait(t *testing.T, cli *Client, cursor uint64, settle time.Duration) (<-chan s6bWaitOutcome, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan s6bWaitOutcome, 1)
	go func() {
		items, hasMore, err := cli.MailboxWait(ctx, cursor)
		out <- s6bWaitOutcome{items: items, hasMore: hasMore, err: err}
	}()
	time.Sleep(settle)
	select {
	case r := <-out:
		cancel()
		t.Fatalf("MailboxWait on an EMPTY mailbox returned after %v (items=%d err=%v); a wait must stay parked until an item arrives or Config.MaxServerWait elapses", settle, len(r.items), r.err)
	default:
	}
	return out, cancel
}

// s6bStillParked fails unless the wait is still outstanding. It is the
// head-of-line-blocking assertion in its non-flaky form: if the relay had served
// the concurrent work by draining the wait first, this channel would be ready.
func s6bStillParked(t *testing.T, out <-chan s6bWaitOutcome, what string) {
	t.Helper()
	select {
	case r := <-out:
		t.Fatalf("%s: the outstanding wait resolved (items=%d err=%v); the wait and the concurrent request must be independent in-flight exchanges, not one serialised queue", what, len(r.items), r.err)
	default:
	}
}

// s6bAppendSamples issues n appends from cli to target and returns each call's
// wall-clock duration, in issue order.
func s6bAppendSamples(t *testing.T, cli *Client, target string, sp sealParty, clk *fakeClock, n int, seqBase uint64) []time.Duration {
	t.Helper()
	out := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		env := sp.sealMailbox(t, seqBase+uint64(i), []byte("k"), clk)
		start := time.Now()
		if _, err := cli.MailboxAppend(testCtx(t), target, env); err != nil {
			t.Fatalf("MailboxAppend sample %d: %v", i, err)
		}
		out = append(out, time.Since(start))
	}
	return out
}

func s6bMedian(ds []time.Duration) time.Duration {
	c := append([]time.Duration(nil), ds...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c[len(c)/2]
}

func s6bMax(ds []time.Duration) time.Duration {
	var m time.Duration
	for _, d := range ds {
		if d > m {
			m = d
		}
	}
	return m
}

// --- §6.0 numbers -----------------------------------------------------------

// TestS6B_ServerWaitCeilingIsTheCommitteeBudget pins §6.0's "Server-side wait
// (long-poll) maximum: 25 s". The value is chosen to sit under the common 30-60 s
// idle-proxy timeout, so a ceiling at or above 30 s would let an intermediary kill
// the connection before the relay ever answers — asserted here so a later tuning
// pass cannot quietly cross that line. RED: Config has no MaxServerWait field.
func TestS6B_ServerWaitCeilingIsTheCommitteeBudget(t *testing.T) {
	got := DefaultConfig().MaxServerWait
	if got != 25*time.Second {
		t.Fatalf("DefaultConfig().MaxServerWait = %v, want 25s (§6.0 binding budget)", got)
	}
	if got >= 30*time.Second {
		t.Fatalf("MaxServerWait = %v must stay strictly under the 30 s low end of common idle-proxy timeouts (§6.0)", got)
	}
}

// TestS6B_AppendCompletesWhileAWaitIsOutstanding is acceptance clause (a) and the
// heart of the slice: with a wait outstanding, a keystroke append FROM THE SAME
// CLIENT completes within §6.0's 50 ms.
//
// Two assertions, deliberately of different kinds:
//
//   - STRUCTURAL (cannot flake): every append completes while the wait is STILL
//     parked. Under today's serialised roundtrip the append cannot even be written
//     before the wait's reply is read, so this fails by ~25 s, not by ~20 ms.
//   - BUDGET (§6.0's number): the median append issued while a wait is outstanding
//     is <= 50 ms, and is not materially worse than the same client's appends with
//     NO wait outstanding — proving the 50 ms is met because the paths are
//     independent, not because the host happens to be fast.
//
// The median rather than the max is asserted against 50 ms on purpose: measured
// append cost on this idle host is p50 12 ms / p99 28.9 ms / max 30.9 ms (one bolt
// fsync each), so a max-based assertion at 50 ms would sit 1.6x from the observed
// worst case and would be the first thing to flake on a loaded CI box. The
// structural assertion above is what actually guards the property.
func TestS6B_AppendCompletesWhileAWaitIsOutstanding(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil) // default MaxServerWait: a real 25 s ceiling
	p := s6bFixture(t, srv)

	// Baseline: the same client, the same target, NO wait outstanding.
	baseline := s6bAppendSamples(t, p.device, p.machRID, p.sp, clk, 32, 1_000)

	// Park a wait on the device's own (empty) mailbox.
	waited, cancelWait := s6bParkWait(t, p.device, 0, 200*time.Millisecond)
	defer cancelWait()

	// 32 keystroke-sized appends from THE SAME client while that wait is parked.
	withWait := s6bAppendSamples(t, p.device, p.machRID, p.sp, clk, 32, 2_000)
	s6bStillParked(t, waited, "after 32 concurrent appends")

	if med := s6bMedian(withWait); med > 50*time.Millisecond {
		t.Fatalf("median append with a wait outstanding = %v, want <= 50ms (§6.0, PB-NET-5(a)); baseline median without a wait = %v, max with wait = %v", med, s6bMedian(baseline), s6bMax(withWait))
	}
	// Independence, not luck: a path that serialised behind the wait would be
	// orders of magnitude slower, so a generous 4x band still catches it while
	// tolerating ordinary fsync jitter.
	if med, base := s6bMedian(withWait), s6bMedian(baseline); med > 4*base+10*time.Millisecond {
		t.Fatalf("median append with a wait outstanding = %v vs %v without; the wait is delaying the append path", med, base)
	}

	// The parked wait still works afterwards: an item appended to the device's
	// mailbox resolves it, carrying the ITEM (not a bare signal).
	env := p.sp.sealMailbox(t, 9_001, []byte("journal"), clk)
	if _, err := p.machine.MailboxAppend(testCtx(t), p.devRID, env); err != nil {
		t.Fatalf("machine append to the waiting device: %v", err)
	}
	select {
	case r := <-waited:
		if r.err != nil {
			t.Fatalf("parked MailboxWait resolved with error %v, want the appended item", r.err)
		}
		if len(r.items) != 1 {
			t.Fatalf("parked MailboxWait returned %d items, want exactly the 1 appended", len(r.items))
		}
		if string(r.items[0].Envelope) != string(env) {
			t.Fatalf("MailboxWait returned a different envelope than was appended")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MailboxWait never resolved after an item was appended to the waiting client's mailbox")
	}
}

// TestS6B_AWaitOnOneClientDoesNotBlockAnotherConnection is the second half of the
// head-of-line-blocking property, at the SERVER: a wait parked by one connection
// must not hold the relay's shared state (s.mu, or a bolt transaction) while it
// sits there, or one waiting phone freezes every other party on the relay.
// Structural: a blocked op would take the whole 25 s ceiling.
func TestS6B_AWaitOnOneClientDoesNotBlockAnotherConnection(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	p := s6bFixture(t, srv)

	waited, cancelWait := s6bParkWait(t, p.device, 0, 200*time.Millisecond)
	defer cancelWait()

	start := time.Now()
	if _, err := p.machine.Presence(testCtx(t), p.devRID); err != nil {
		t.Fatalf("Presence on a second connection while another client's wait is parked: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("an unrelated op on a SECOND connection took %v while another client's wait was parked; the parked wait is holding shared relay state", d)
	}

	// An append to a THIRD-party mailbox must also be served: appends take s.mu for
	// the rate window and then a bolt write transaction, both of which a badly
	// implemented wait could be sitting on.
	env := p.sp.sealMailbox(t, 3_001, []byte("k"), clk)
	start = time.Now()
	if _, err := p.machine.MailboxAppend(testCtx(t), p.devRID, env); err != nil {
		t.Fatalf("append while another client's wait is parked: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("an append took %v while a wait was parked; the wait is holding the store or s.mu", d)
	}
	// That append targeted the waiting mailbox, so the wait is now expected to
	// resolve — assert it does, rather than asserting it is still parked.
	select {
	case r := <-waited:
		if r.err != nil || len(r.items) != 1 {
			t.Fatalf("parked wait after the third-party append: items=%d err=%v, want 1 item", len(r.items), r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the parked wait never observed an item appended by another connection")
	}
}

// TestS6B_SecondConcurrentWaitIsRefusedNotQueued pins §6.0's "Max concurrent
// pending waits per client: 1 (a second wait is refused, not queued)". Queueing
// the second wait would let a client pin unbounded server-side wait state on one
// connection, and would make cancellation ambiguous.
func TestS6B_SecondConcurrentWaitIsRefusedNotQueued(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	p := s6bFixture(t, srv)

	waited, cancelWait := s6bParkWait(t, p.device, 0, 200*time.Millisecond)
	defer cancelWait()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err := p.device.MailboxWait(ctx, 0)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrWaitInProgress) {
		t.Fatalf("second concurrent MailboxWait returned %v after %v, want ErrWaitInProgress — §6.0 caps pending waits per client at 1 and REFUSES the second rather than queueing it", err, elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("second concurrent MailboxWait was refused only after %v; a refusal must be immediate, not a queued wait that eventually errors", elapsed)
	}
	s6bStillParked(t, waited, "after the second wait was refused")
}

// TestS6B_WaitCancellationFreesThePendingSlot is acceptance clause (c),
// cancellation. Cancelling the caller's context must (1) return promptly, (2)
// leave the CONNECTION usable, and (3) free the single pending-wait slot — the
// third is what proves the server-side wait was really released rather than
// orphaned until its 25 s ceiling, which would make the client's own cancellation
// a lie and strand the slot for the whole ceiling after every reconnect.
func TestS6B_WaitCancellationFreesThePendingSlot(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	p := s6bFixture(t, srv)

	waited, cancelWait := s6bParkWait(t, p.device, 0, 200*time.Millisecond)
	cancelWait()

	select {
	case r := <-waited:
		if r.err == nil {
			t.Fatalf("cancelled MailboxWait returned err=nil (items=%d); a cancelled wait must surface the cancellation", len(r.items))
		}
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("cancelled MailboxWait returned %v, want a context.Canceled-wrapping error", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled MailboxWait did not return within 2s; cancellation is not honoured")
	}

	// (2) the connection is still usable for an ordinary op.
	if _, err := p.device.Presence(testCtx(t), p.devRID); err != nil {
		t.Fatalf("ordinary op after a cancelled wait: %v; cancellation must not poison the connection", err)
	}

	// (3) the pending-wait slot is free: a fresh wait is ACCEPTED, not refused by
	// the ghost of the cancelled one.
	second, cancelSecond := s6bParkWait(t, p.device, 0, 200*time.Millisecond)
	defer cancelSecond()
	env := p.sp.sealMailbox(t, 5_001, []byte("after-cancel"), clk)
	if _, err := p.machine.MailboxAppend(testCtx(t), p.devRID, env); err != nil {
		t.Fatalf("append after re-parking: %v", err)
	}
	select {
	case r := <-second:
		if r.err != nil || len(r.items) != 1 {
			t.Fatalf("re-parked MailboxWait after a cancellation: items=%d err=%v, want 1 item and no error", len(r.items), r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("re-parked MailboxWait never resolved; the cancelled wait's server-side slot was never released")
	}
}

// TestS6B_WaitReturnsAnEmptyPageAtTheCeiling asserts the wait is BOUNDED: with
// nothing to deliver it returns cleanly at Config.MaxServerWait rather than
// hanging until an intermediary kills the socket. The ceiling is shortened here so
// the assertion costs milliseconds; the 25 s production value is pinned separately
// by TestS6B_ServerWaitCeilingIsTheCommitteeBudget.
func TestS6B_WaitReturnsAnEmptyPageAtTheCeiling(t *testing.T) {
	const ceiling = 250 * time.Millisecond
	srv, _, _, _ := startTestRelay(t, func(c *Config) { c.MaxServerWait = ceiling })
	p := s6bFixture(t, srv)

	start := time.Now()
	items, hasMore, err := p.device.MailboxWait(testCtx(t), 0)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("MailboxWait at the ceiling returned %v, want a clean empty page", err)
	}
	if len(items) != 0 || hasMore {
		t.Fatalf("MailboxWait on an empty mailbox returned items=%d has_more=%v, want an empty page", len(items), hasMore)
	}
	// Lower bound: it really waited. A wait that returns instantly is a poll under
	// another name, and §6.0's <=3 reads/s drain budget cannot absorb one.
	if elapsed < ceiling/2 {
		t.Fatalf("MailboxWait returned after %v with MaxServerWait=%v; it did not wait at all", elapsed, ceiling)
	}
	if elapsed > ceiling+3*time.Second {
		t.Fatalf("MailboxWait returned after %v with MaxServerWait=%v; the ceiling is not enforced", elapsed, ceiling)
	}
}

// TestS6B_WaitMetersExactlyOneOpPerCallNotPerItem is acceptance clause (c), quota
// accounting, and it is the arithmetic §6.0 spells out. mailbox_read and
// mailbox_ack DO meter against OpsPerMin (server.go:766 and :798); mailbox_append
// does not (config.go:39-44). A wait must therefore meter — an unmetered
// state-touching op is the one abuse hole the relay does not otherwise have — and
// it must meter ONCE per call regardless of how many items it hands back, or the
// batching that keeps the drain inside §6.0's 240/min buys nothing.
//
// The probe: freeze the clock so the whole test lands in ONE tumbling window
// (server.go:105-115), set a tiny OpsPerMin, and count how many waits succeed
// before the clean ErrQuotaExceeded. handleAuthResp charges one op at handshake
// (server.go:648) against this device's own window; mailboxFixture's
// AuthorizeDevice is charged to the MACHINE's window, not the device's.
func TestS6B_WaitMetersExactlyOneOpPerCallNotPerItem(t *testing.T) {
	const opsPerMin = 6
	srv, _, _, clk := startTestRelay(t, func(c *Config) {
		c.Quotas.OpsPerMin = opsPerMin
		c.MaxServerWait = 100 * time.Millisecond // an empty wait costs a tenth of a second
	})
	p := s6bFixture(t, srv)

	// A batch of 12 items, so the FIRST wait returns many at once. If a wait were
	// metered per delivered item, or if it needed a follow-up mailbox_read, the
	// budget would be gone before the loop below finishes.
	for i := 0; i < 12; i++ {
		env := p.sp.sealMailbox(t, uint64(i+1), []byte("batch"), clk)
		if _, err := p.machine.MailboxAppend(testCtx(t), p.devRID, env); err != nil {
			t.Fatalf("seed append %d: %v", i, err)
		}
	}

	// auth_resp charges NOTHING against the device's routing-id window. meterOp() runs at
	// the top of handleAuthResp (server.go:659), BEFORE registerSession sets sc.rid
	// (server.go:680), so opSource() falls back to sc.sourceKey -- the transport source --
	// and the charge lands on "127.0.0.1", not on "rid:<device>". Verified by probing the
	// relay's own opsRate map: both auth_resps charge the transport key, and the device's
	// rid key is absent entirely.
	//
	// This is deliberate and must not be "fixed" by metering against pendingRID. That is
	// the UNPROVEN, client-presented pubkey, and the 2026-07-20 amendment forbids it
	// ("BEFORE any signature verifies... keyed by TRANSPORT SOURCE -- never by the unproven
	// presented pubkey") precisely because it would let an attacker exhaust a victim
	// identity's budget by presenting the victim's key. The S6b implementer refused to make
	// that change and was right to; this constant was the defect.
	const authRespCharge = 0
	wantSucceeded := opsPerMin - authRespCharge

	succeeded := 0
	var lastErr error
	for i := 0; i < opsPerMin+4; i++ {
		items, _, err := p.device.MailboxWait(testCtx(t), 0)
		if err != nil {
			lastErr = err
			break
		}
		if i == 0 && len(items) != 12 {
			t.Fatalf("first MailboxWait returned %d items, want the whole batch of 12 in ONE metered call", len(items))
		}
		succeeded++
	}

	if !errors.Is(lastErr, ErrQuotaExceeded) {
		t.Fatalf("waits past the OpsPerMin budget ended with %v, want a clean ErrQuotaExceeded — a wait that never meters is an unmetered state-touching op", lastErr)
	}
	if succeeded != wantSucceeded {
		t.Fatalf("%d waits succeeded under OpsPerMin=%d, want exactly %d (one op per CALL, not per delivered item; auth_resp charges %d)", succeeded, opsPerMin, wantSucceeded, authRespCharge)
	}
}

// --- clause (d): the Phase A regression fence -------------------------------

// TestS6B_ConcurrentDialsDoNotBypassThePerSourceConnCap re-proves the CR-1
// per-source concurrent-connection cap under CONCURRENCY. bounds_test.go's
// TestRelay_PerSourceConcurrentConnCapEnforced fills the cap with sequential,
// blocking dials, which is exactly the shape that stops being representative once
// dispatch is concurrent: a cap enforced by a check-then-insert no longer
// serialised by the connection's own request loop can be raced past by N
// simultaneous dials, and PB-NET-5(d) requires the property to still hold.
//
// The cap is enforced in serveConn under s.mu (server.go:351-367), so this passes
// against today's tree and is a FENCE, not a RED assertion: it fails if S6b's
// concurrency work moves admission out from under that lock.
func TestS6B_ConcurrentDialsDoNotBypassThePerSourceConnCap(t *testing.T) {
	const capN = 3
	const racers = 12

	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.Quotas.MaxConcurrentConnections = 0 // isolate the per-source cap
		c.Quotas.MaxConcurrentConnectionsPerSource = capN
	})

	var wg sync.WaitGroup
	var mu sync.Mutex
	served := 0
	conns := make([]*Conn, 0, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), testDeadline)
			defer cancel()
			c, err := DialRaw(ctx, srv.URL())
			if err != nil {
				return
			}
			if _, _, err := c.Hello(ctx, ProtocolVersion, nil); err != nil {
				_ = c.Close()
				return
			}
			mu.Lock()
			served++
			conns = append(conns, c)
			mu.Unlock()
		}()
	}
	wg.Wait()
	t.Cleanup(func() {
		for _, c := range conns {
			_ = c.Close()
		}
	})

	mu.Lock()
	got := served
	mu.Unlock()
	if got > capN {
		t.Fatalf("%d of %d SIMULTANEOUS same-source connections were served with MaxConcurrentConnectionsPerSource=%d; concurrent dispatch must not race past CR-1 admission control (PB-NET-5(d))", got, racers, capN)
	}
	if got == 0 {
		t.Fatalf("no connection at all was served under the per-source cap of %d; the cap must admit capN, not zero", capN)
	}
}

// TestS6B_UnauthenticatedConnCannotParkInAWaitToEvadeTheHandshakeDeadline is the
// other half of clause (d). readFrame bounds the CUMULATIVE time-to-authenticate
// only while `!sc.authed && sc.rdvID == ""` (server.go:443). A wait is the first
// operation in the protocol that legitimately parks a connection for tens of
// seconds, so if it were servable before authentication — or if parking moved the
// connection out of the handshake-timed regime — an unauthenticated slowloris
// would get a free 25 s per attempt and the 2026-07-20 amendment's deadline would
// be dead.
//
// The one place this file names a wire op ("mailbox_wait") rather than going
// through the exported client: an unauthenticated Conn has no *Client to call
// MailboxWait on. If the implementer names the op differently the relay answers
// codeBadRequest, which still satisfies "refused, promptly" — the test degrades to
// a weaker but still-valid assertion rather than a false pass.
func TestS6B_UnauthenticatedConnCannotParkInAWaitToEvadeTheHandshakeDeadline(t *testing.T) {
	const (
		handshakeTimeout = 400 * time.Millisecond
		dripInterval     = 80 * time.Millisecond // comfortably under handshakeTimeout
		guard            = 4 * time.Second
	)
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.HandshakeTimeout = handshakeTimeout
		c.Quotas.MaxConcurrentConnections = 0
		c.MaxServerWait = 25 * time.Second // the production ceiling: what an evader would win
	})

	conn := dialRaw(t, srv.URL())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err := conn.control(ctx, "mailbox_wait", map[string]any{"cursor": uint64(0)})
	// ErrNotAuthorized specifically, not merely "some error". Reviewed finding: asserting
	// err != nil made this fence VACUOUS -- deleting the `if !sc.authed` guard at wait.go:162
	// so a pre-auth wait really does park still PASSED, because HandshakeTimeout (400ms here)
	// tears the connection down regardless and the client's blocked read returns a websocket
	// close well inside the 2s window. Both assertions passed while the property in this
	// test's own name -- refused INLINE, never parked -- was unfenced, and a refactor moving
	// the auth check after registerWait would have shipped green. Under that mutation
	// registerWait(sc, "", ...) also keys s.waits[""], one shared slot across every
	// unauthenticated connection, never reaped by removeConn (guarded on sc.rid != "").
	// Requiring the specific refusal is what makes the mutation fail: a close is not it.
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("an UNAUTHENTICATED mailbox_wait returned %v, want ErrNotAuthorized; a wait must be refused INLINE like every other mailbox op, never parked", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("an unauthenticated mailbox_wait was refused only after %v; it was PARKED, which hands a pre-auth connection a free MaxServerWait per attempt (PB-NET-5(d))", d)
	}

	// And the cumulative deadline still closes it despite a harmless drip.
	deadline := time.Now().Add(guard)
	for time.Now().Before(deadline) {
		time.Sleep(dripInterval)
		if _, _, err := conn.Hello(testCtx(t), ProtocolVersion, nil); err != nil {
			return // closed on the cumulative deadline: the property holds
		}
	}
	t.Fatalf("an unauthenticated connection that attempted a wait survived a %v drip (interval %v, HandshakeTimeout %v); the cumulative handshake deadline was weakened (PB-NET-5(d))", guard, dripInterval, handshakeTimeout)
}

// TestS6B_TakeoverSeversAnOutstandingWait is the newest-wins fence.
// registerSession binds one conn per routing id and supersedes the older one
// (server.go:675-691); revoke and presence severance depend on it. A parked wait
// is precisely the state that could outlive a takeover unnoticed: the superseded
// connection is not issuing requests, so nothing else would ever tell it. It must
// be released with ErrDuplicateConnection rather than left holding the single wait
// slot for the remaining ceiling — otherwise the NEW connection cannot park its
// own wait and live typing is dead for up to 25 s after every reconnect.
func TestS6B_TakeoverSeversAnOutstandingWait(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	p := s6bFixture(t, srv)

	waited, cancelWait := s6bParkWait(t, p.device, 0, 200*time.Millisecond)
	defer cancelWait()

	// A NEWER connection for the SAME routing id takes over.
	second := dialAuthed(t, srv.URL(), p.devAuth)

	select {
	case r := <-waited:
		if !errors.Is(r.err, ErrDuplicateConnection) {
			t.Fatalf("the superseded connection's outstanding wait resolved with %v, want ErrDuplicateConnection; a takeover must release the old wait, not leave it parked", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a takeover left the superseded connection's wait parked; it must be severed (PB-NET-5(d): newest-wins must not be weakened)")
	}

	// The NEW connection owns the single wait slot and works.
	fresh, cancelFresh := s6bParkWait(t, second, 0, 200*time.Millisecond)
	defer cancelFresh()
	env := p.sp.sealMailbox(t, 7_001, []byte("after-takeover"), clk)
	if _, err := p.machine.MailboxAppend(testCtx(t), p.devRID, env); err != nil {
		t.Fatalf("append to the taken-over routing id: %v", err)
	}
	select {
	case r := <-fresh:
		if r.err != nil || len(r.items) != 1 {
			t.Fatalf("the takeover connection's wait: items=%d err=%v, want 1 item and no error", len(r.items), r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the takeover connection could not park a wait; the superseded connection's slot was never released")
	}
}

// TestS6B_RevokeSeversAnOutstandingWait: device_revoke severs the target's live
// socket (server.go:938-944, ME-1). A revoked device parked in a 25 s wait must be
// cut immediately like any other, not left connected until its ceiling elapses —
// that window is exactly the post-revocation reachability the Phase A property
// exists to close.
func TestS6B_RevokeSeversAnOutstandingWait(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	p := s6bFixture(t, srv)

	waited, cancelWait := s6bParkWait(t, p.device, 0, 200*time.Millisecond)
	defer cancelWait()

	if err := p.machine.DeviceRevoke(testCtx(t), p.devRID); err != nil {
		t.Fatalf("DeviceRevoke: %v", err)
	}
	select {
	case r := <-waited:
		if r.err == nil {
			t.Fatalf("a revoked device's parked wait resolved with err=nil (items=%d); revocation must sever it", len(r.items))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("device_revoke left the revoked device parked in a wait; the severance must reach an outstanding wait (PB-NET-5(d))")
	}
}
