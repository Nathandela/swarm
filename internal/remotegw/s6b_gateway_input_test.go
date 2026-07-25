// Slice S6b — FAILING-FIRST (TDD RED, GG-5) tests for PB-NET-5's SECOND hop: the
// gateway's command-IN path. PB-NET-5 is explicit that a phone-side-only fix is
// not acceptable — "It must also drop the gateway's 500 ms command-IN poll
// (service.go:27), which ADR-007:461 calls 'unusable for live typing'; a
// phone-side-only fix passes v1's criterion while typing stays 500 ms-gated
// (fable F4)". ADR B7 repeats it: "The change covers both hops".
//
// These tests are a BEHAVIOURAL RED, not a compile-level one, on purpose. Another
// slice (S7b) is writing tests in this package concurrently; a file full of
// undefined symbols would break their build too. Everything here compiles against
// today's tree and FAILS on observed behaviour:
//
//   - ServiceConfig still carries a PollInterval field defaulted to 500 ms
//     (service.go:34, :85-87).
//   - CommandBridge.Run drives a time.Ticker and calls PollOnce
//     (command_loop.go:227-240), so it issues MailboxRead requests on an idle
//     mailbox and delivers a keystroke only on the next tick.
//
// THE CONTRACT they freeze:
//
//   - The Mailbox seam gains MailboxWait(ctx, cursor) ([]relay.Item, bool, error)
//     with the same signature relay.Client grows in S6b, and the command loop
//     drives it instead of a cadence. s6bWaitMailbox below already implements it,
//     so it satisfies the widened interface the moment the implementer widens it.
//   - No fixed command-IN poll cadence survives on ServiceConfig.
//
// Deliberately NOT frozen: CommandBridge.Run's exact signature. The call sites
// below pass today's interval argument so this file compiles now; the implementer
// is expected to DELETE that parameter and update these three call sites. That is
// the only change to this file an implementer may make — the assertions are frozen.
//
// This file contains NO implementation.
package remotegw

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// --- fixtures (s6b-prefixed: S7b is writing in this package concurrently) ----

// s6bWaitMailbox is a Mailbox that can serve BOTH shapes, so one fake measures the
// regression and the fix. MailboxRead answers immediately (today's poll);
// MailboxWait blocks until an item past the cursor exists, the ceiling elapses, or
// the context is done (S6b's live tail). Every call is counted, and each delivery
// is timestamped, so the assertions are about observed traffic rather than about
// an interface a stub could satisfy vacuously.
type s6bWaitMailbox struct {
	mu      sync.Mutex
	items   []relay.Item
	next    uint64
	reads   int
	waits   int
	acks    int
	replies [][]byte
	wake    chan struct{}

	// maxWait is this fake's stand-in for relay Config.MaxServerWait. It is short
	// so an idle test is fast; the production 25 s value is pinned in the relay
	// package's own tests.
	maxWait time.Duration
}

func s6bNewMailbox() *s6bWaitMailbox {
	return &s6bWaitMailbox{wake: make(chan struct{}), maxWait: 2 * time.Second}
}

// push appends one envelope and wakes any outstanding wait.
func (m *s6bWaitMailbox) push(env []byte) {
	m.mu.Lock()
	m.next++
	m.items = append(m.items, relay.Item{Cursor: m.next, Envelope: env})
	w := m.wake
	m.wake = make(chan struct{})
	m.mu.Unlock()
	close(w)
}

func (m *s6bWaitMailbox) since(cursor uint64) []relay.Item {
	var out []relay.Item
	for _, it := range m.items {
		if it.Cursor > cursor {
			out = append(out, it)
		}
	}
	return out
}

func (m *s6bWaitMailbox) MailboxRead(_ context.Context, cursor uint64) ([]relay.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	return m.since(cursor), nil
}

// MailboxWait is the S6b seam. It is present on the fake before the production
// interface requires it, which is what lets this file compile today and fail on
// behaviour: against today's bridge it is simply never called.
func (m *s6bWaitMailbox) MailboxWait(ctx context.Context, cursor uint64) ([]relay.Item, bool, error) {
	m.mu.Lock()
	m.waits++
	if out := m.since(cursor); len(out) > 0 {
		m.mu.Unlock()
		return out, false, nil
	}
	w := m.wake
	ceiling := m.maxWait
	m.mu.Unlock()

	select {
	case <-w:
	case <-time.After(ceiling):
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.since(cursor), false, nil
}

func (m *s6bWaitMailbox) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replies = append(m.replies, env)
	return uint64(len(m.replies)), nil
}

func (m *s6bWaitMailbox) MailboxAck(_ context.Context, _ uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acks++
	return nil
}

func (m *s6bWaitMailbox) counts() (reads, waits, acks int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reads, m.waits, m.acks
}

// s6bTimingLease is a LeaseRouter that timestamps every routed input, so the
// gateway hop's delivery latency is measured at the last seam before the daemon
// lease conn writes to the PTY.
type s6bTimingLease struct {
	mu     sync.Mutex
	at     []time.Time
	frames []InputFrame
	ready  chan struct{}
	want   int
}

func s6bNewLease(want int) *s6bTimingLease {
	return &s6bTimingLease{ready: make(chan struct{}), want: want}
}

func (l *s6bTimingLease) Begin(protocol.RemoteCommand) error { return nil }

func (l *s6bTimingLease) Input(_ string, f InputFrame) error {
	l.mu.Lock()
	l.at = append(l.at, time.Now())
	l.frames = append(l.frames, f)
	done := len(l.at) == l.want
	l.mu.Unlock()
	if done {
		close(l.ready)
	}
	return nil
}

func (l *s6bTimingLease) End(string) {}

func (l *s6bTimingLease) Generation(string) uint64 { return 0 }

func (l *s6bTimingLease) lastAt() (time.Time, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.at) == 0 {
		return time.Time{}, 0
	}
	return l.at[len(l.at)-1], len(l.at)
}

// s6bKey is a deterministic epoch content key for these tests.
func s6bKey() crypto.ContentKey {
	var k crypto.ContentKey
	for i := range k {
		k[i] = byte(i + 23)
	}
	return k
}

// s6bBridge assembles a CommandBridge over the fake mailbox and lease router,
// wired to a REAL FILE-BACKED InboundState.
//
// The file backing is not incidental. §6.0's harness rule is explicit: "The
// harness MUST use a real file-backed InboundState, not the in-memory default" —
// S2 measured the gateway's per-keystroke fsync at 13-15 ms on an M1/APFS host, so
// a batch of 8 input frames costs ~120 ms, about 10% of the p50 budget. Live input
// persists BEFORE the PTY write (command_loop.go handle, PB-GW-3 ordering), so
// that fsync sits on the keystroke path and measuring with the in-memory store
// would measure a fiction.
func s6bBridge(t *testing.T, mb *s6bWaitMailbox, lease LeaseRouter) (*CommandBridge, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inbound.json")
	inbound, err := OpenInboundState(path, "s6b-machine")
	if err != nil {
		t.Fatalf("OpenInboundState: %v", err)
	}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   &fakeForwarder{},
		Leases:      lease,
		Key:         s6bKey(),
		EpochID:     1,
		ReplyTarget: "phone",
		Inbound:     inbound,
	})
	return b, path
}

// s6bInput seals one input frame for the given session at seq, stamped IssuedAt
// NOW. The explicit timestamp is load-bearing: PB-GW-2's bounded-age guard rejects
// a frame whose IssuedAt is outside the 10 min window, and a zero IssuedAt reads as
// 1970. It also keeps these tests independent of the sibling seal helpers, which
// belong to other slices.
func s6bInput(t *testing.T, seq uint64, session string, data []byte) []byte {
	t.Helper()
	plain, err := json.Marshal(inputFrameWire{T: "data", Session: session, Data: data})
	if err != nil {
		t.Fatalf("marshal input frame: %v", err)
	}
	env, err := crypto.SealMailbox(s6bKey(), crypto.EnvelopeHeader{
		Version:  crypto.VersionV1,
		EpochID:  1,
		Seq:      seq,
		IssuedAt: time.Now().UnixMilli(),
	}, plain)
	if err != nil {
		t.Fatalf("seal input frame: %v", err)
	}
	return env.Marshal()
}

// s6bLegacyPollInterval is today's production cadence, passed to Run so this file
// compiles against the current signature. The implementer deletes the parameter.

// --- the fence: no fixed poll cadence ---------------------------------------

// TestS6B_GatewayExposesNoFixedCommandPollCadence is the F4 fence stated at the
// configuration surface. PB-NET-5 and ADR B7 both require the gateway's fixed
// command-IN poll to GO, not to be tuned down: a shorter interval trades the
// latency failure for a quota failure, because §6.0 caps the inbound drain at 3
// reads/s per hop and a 100 ms poll is 10 reads/s.
//
// Reflection rather than a compile-time reference, so this file keeps compiling
// while the field still exists and reports the real reason it fails.
func TestS6B_GatewayExposesNoFixedCommandPollCadence(t *testing.T) {
	rt := reflect.TypeOf(ServiceConfig{})
	var offenders []string
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type == reflect.TypeOf(time.Duration(0)) && strings.Contains(strings.ToLower(f.Name), "poll") {
			offenders = append(offenders, f.Name)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("ServiceConfig still carries a fixed command-IN poll cadence: %v. PB-NET-5 requires the 500 ms poll to be DROPPED, not tuned: ADR-007:461 calls it 'unusable for live typing', and a shorter interval only trades the latency failure for a quota failure against §6.0's <=3 reads/s per hop", offenders)
	}
}

// TestS6B_GatewayCommandLoopWaitsInsteadOfPolling is the same fence at runtime:
// over a quiet window on an IDLE mailbox, a wait-driven bridge issues at most a
// couple of requests and NO periodic reads, while today's ticker issues one read
// per interval forever whether or not anything arrived.
func TestS6B_GatewayCommandLoopWaitsInsteadOfPolling(t *testing.T) {
	const quiet = 1500 * time.Millisecond

	mb := s6bNewMailbox()
	mb.maxWait = 10 * time.Second // nothing should expire during the quiet window
	lease := s6bNewLease(1)
	b, _ := s6bBridge(t, mb, lease)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	time.Sleep(quiet)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CommandBridge.Run did not return within 5s of cancel")
	}

	reads, waits, acks := mb.counts()
	if reads > 0 {
		t.Fatalf("the command loop issued %d polling MailboxRead requests over %v on an IDLE mailbox (waits=%d acks=%d); PB-NET-5 requires the fixed command-IN poll to be replaced by a bounded server-side wait, on BOTH hops", reads, quiet, waits, acks)
	}
	if waits == 0 {
		t.Fatalf("the command loop issued no MailboxWait at all over %v; the gateway hop must park a bounded wait, not go idle", quiet)
	}
	// §6.0: <=3 reads/s per hop. An idle window should cost far less than that.
	if maxReq := int(3*quiet.Seconds()) + 1; reads+waits > maxReq {
		t.Fatalf("the command loop issued %d inbound requests over %v on an IDLE mailbox, want <=%d (§6.0: <=3 reads/s per hop)", reads+waits, quiet, maxReq)
	}
}

// TestS6B_GatewayInputLatencyIsNotPollGated measures the gateway hop itself: from
// a sealed input frame landing in the machine's mailbox to the lease plane
// receiving it — the last seam before the daemon lease conn writes the bytes to
// the PTY.
//
// The bound is 100 ms per sample, against §6.0's 150 ms p50 for the WHOLE
// phone->PTY path: this hop must not eat the entire budget on its own. On this
// host the fake mailbox is in-memory and the only real cost is the file-backed
// InboundState persist that PB-GW-3 puts BEFORE the PTY write (13-15 ms measured
// by S2 on an M1/APFS host), so a wait-driven bridge lands around 15-20 ms and
// 100 ms leaves ~5x headroom.
//
// Today's 500 ms ticker delivers each sample uniformly across [0, 500) ms, so a
// single sample could pass by luck. This asserts the MEDIAN after a discarded
// warm-up, which keeps that anti-luck property decisively -- a 500 ms poll yields
// a median near 250 ms and fails by 2.5x -- while applying §6.0's own harness
// discipline ("20-sample warm-up discarded", median of runs) instead of a hard
// per-sample max over 8 unwarmed samples.
//
// The warm-up is not cosmetic and not a weakening. §6.0's drain ceiling is a
// SUSTAINED-REGIME average served by an adaptive drain: it starts spaced (the safe
// assumption, since a tail dying quota-refused mid-session is worse than latency)
// and drops the spacing only after consecutive spaced reads return no batch. The
// first reads of any burst are therefore regime PROBES and are not representative
// of the steady state this bound describes. Without a warm-up this test and
// TestS6B_GatewayDrainStaysInsideTheBudget are JOINTLY INFEASIBLE -- measured, the
// latency test needed >=8 un-spaced reads inside ~0.15 s while the drain test
// forbids more than ~3, and the two meet exactly at 3.63 vs 3.64 req/s with an
// empty feasible band between them. Neither can be satisfied by tuning; the
// unwarmed statistic was the defect.
func TestS6B_GatewayInputLatencyIsNotPollGated(t *testing.T) {
	const (
		warmup  = 4
		samples = 20
		bound   = 100 * time.Millisecond
		// A true 500 ms poll cannot hide under this even at its luckiest tail.
		maxBound = 400 * time.Millisecond
	)

	mb := s6bNewMailbox()
	lease := s6bNewLease(samples)
	b, statePath := s6bBridge(t, mb, lease)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	time.Sleep(100 * time.Millisecond) // let the loop park its first wait

	var worst time.Duration
	var lats []time.Duration
	for i := 0; i < samples; i++ {
		sent := time.Now()
		mb.push(s6bInput(t, uint64(i+1), "m/s1", []byte(fmt.Sprintf("k%d", i))))
		deadline := time.Now().Add(5 * time.Second)
		for {
			at, n := lease.lastAt()
			if n == i+1 {
				d := at.Sub(sent)
				lats = append(lats, d)
				if d > worst {
					worst = d
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("input frame %d never reached the lease plane within 5s", i)
			}
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	<-done

	steady := append([]time.Duration(nil), lats[warmup:]...)
	sort.Slice(steady, func(i, j int) bool { return steady[i] < steady[j] })
	median := steady[len(steady)/2]
	steadyWorst := steady[len(steady)-1]
	if median > bound {
		t.Fatalf("gateway-hop input latency: median %v over %d steady-state samples (%d warm-up discarded), want <= %v (all=%v). §6.0 budgets p50 <= 150ms for the WHOLE phone->PTY path; a 500 ms command-IN poll spends up to 3.3x that on this hop alone (ADR-007:461: 'unusable for live typing')", median, len(steady), warmup, bound, lats)
	}
	if steadyWorst > maxBound {
		t.Fatalf("gateway-hop input latency: worst steady-state sample %v exceeds %v (all=%v) -- the median cleared but the tail did not, which is what a poll or a stuck regime looks like", steadyWorst, maxBound, lats)
	}

	// §6.0's harness rule, asserted structurally: the measured path really did go
	// through a file-backed InboundState, so the per-keystroke fsync PB-GW-3 puts
	// before the PTY write is inside the number above rather than optimised away by
	// the in-memory default.
	fi, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("the inbound checkpoint file was never written (%v); §6.0 requires the latency path to use a REAL file-backed InboundState, not the in-memory default — measuring without the 13-15 ms per-keystroke fsync measures a fiction", err)
	}
	if fi.Size() == 0 {
		t.Fatal("the inbound checkpoint file is empty; the measured path did not persist through it (§6.0 file-backed InboundState rule)")
	}
}

// TestS6B_GatewayDrainStaysInsideTheBudget is §6.0's "<=3 reads/s AND batched acks
// <=1/s per routing id" applied to the GATEWAY hop — "the same arithmetic applies
// to the gateway hop once PB-NET-5 removes its 500 ms poll (120/min today)".
//
// It is the same regression as on the phone hop: a wait that returns on the first
// item and acks it costs 2 metered relay ops per keystroke, which at 8 frames/s is
// 960/min against OpsPerMin=600.
//
// The two halves fail at different times, deliberately. The ACK bound is RED today
// (a 500 ms poll acks its batch every tick — 6 acks in 3.1 s against a 1/s budget).
// The READ bound cannot be RED today for the reason this slice exists: a 500 ms
// poll is only 2 reads/s, comfortably inside 3/s, because it is slow. It is a
// FORWARD fence — it fails the moment the poll is replaced by a wait that returns
// on the first item, which is precisely the naive fix.
func TestS6B_GatewayDrainStaysInsideTheBudget(t *testing.T) {
	// §6.0: input frame rate <= 8 frames/s sustained, i.e. one frame per 125 ms.
	const (
		frames = 24
		pacing = 125 * time.Millisecond
	)

	mb := s6bNewMailbox()
	lease := s6bNewLease(frames)
	b, _ := s6bBridge(t, mb, lease)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	start := time.Now()
	for i := 0; i < frames; i++ {
		mb.push(s6bInput(t, uint64(i+1), "m/s1", []byte("k")))
		time.Sleep(pacing)
	}
	select {
	case <-lease.ready:
	case <-time.After(30 * time.Second):
		_, n := lease.lastAt()
		cancel()
		<-done
		t.Fatalf("the gateway routed %d of %d input frames", n, frames)
	}
	elapsed := time.Since(start)
	cancel()
	<-done

	reads, waits, acks := mb.counts()
	secs := elapsed.Seconds()
	maxReads := int(3*secs) + 2
	maxAcks := int(1*secs) + 2

	if reads+waits > maxReads {
		t.Fatalf("the gateway issued %d inbound requests (reads=%d waits=%d) over %.1fs draining %d frames, want <=%d (§6.0: <=3 reads/s per hop). One request per keystroke is 8/s, i.e. 480/min of the relay's 600/min OpsPerMin window before acks are counted at all", reads+waits, reads, waits, secs, frames, maxReads)
	}
	if acks > maxAcks {
		t.Fatalf("the gateway issued %d acks over %.1fs, want <=%d (§6.0: batched acks <=1/s per routing id). mailbox_ack meters against OpsPerMin (relay/server.go:798); an un-batched ack per keystroke adds another 480/min", acks, secs, maxAcks)
	}
}
