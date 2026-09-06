// PB-NET-5 gateway input checks: relay-v2 push delivery, durable checkpointing,
// low latency, and batched acknowledgements.
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
)

// --- fixtures (s6b-prefixed: S7b is writing in this package concurrently) ----

// s6bWaitMailbox models relay-v2's pushed delivery queue. MailboxWait blocks locally
// until an item is pushed, its receive deadline expires, or the context ends.
type s6bWaitMailbox struct {
	mu      sync.Mutex
	items   []mailboxItem
	next    uint64
	reads   int
	waits   int
	acks    int
	replies [][]byte
	wake    chan struct{}

	// maxWait keeps an idle fake fast.
	maxWait time.Duration
}

func s6bNewMailbox() *s6bWaitMailbox {
	return &s6bWaitMailbox{wake: make(chan struct{}), maxWait: 2 * time.Second}
}

// push appends one envelope and wakes any outstanding wait.
func (m *s6bWaitMailbox) push(env []byte) {
	m.mu.Lock()
	m.next++
	m.items = append(m.items, mailboxItem{Cursor: m.next, Envelope: env})
	w := m.wake
	m.wake = make(chan struct{})
	m.mu.Unlock()
	close(w)
}

func (m *s6bWaitMailbox) since(cursor uint64) []mailboxItem {
	var out []mailboxItem
	for _, it := range m.items {
		if it.Cursor > cursor {
			out = append(out, it)
		}
	}
	return out
}

func (m *s6bWaitMailbox) MailboxRead(_ context.Context, cursor uint64) ([]mailboxItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	return m.since(cursor), nil
}

// MailboxWait models a blocking local delivery receive.
func (m *s6bWaitMailbox) MailboxWait(ctx context.Context, cursor uint64) ([]mailboxItem, bool, error) {
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

// OnSever satisfies the LeaseRouter seam's lease-death method (PB-INPUT-2). This fake holds
// no conn, so no lease can die on it and the sink is never fired.
func (l *s6bTimingLease) OnSever(func(SeveredLease)) {}

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
		t.Fatalf("ServiceConfig still carries a fixed command-IN poll cadence: %v; relay-v2 pushes deliveries and needs no poll", offenders)
	}
}

// TestS6B_GatewayCommandLoopWaitsInsteadOfPolling is the same fence at runtime:
// over a quiet window on an IDLE mailbox, a wait-driven bridge issues at most a
// one blocking receive and NO periodic reads.
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
		t.Fatalf("the command loop issued %d polling MailboxRead requests over %v on an IDLE mailbox (waits=%d acks=%d); relay-v2 must block on its pushed-delivery channel", reads, quiet, waits, acks)
	}
	if waits == 0 {
		t.Fatalf("the command loop issued no MailboxWait over %v; the gateway must park a local subscription receive", quiet)
	}
}

// TestRelayV2GatewayDoesNotPaceAlreadyPushedDeliveries protects the relay-v2 receive
// shape: SUBSCRIBE pushes deliveries over one websocket, so MailboxWait drains a local
// channel and does not spend a metered relay operation. Delaying that drain by the retired
// relay-v1 3-reads/s budget adds a fixed 333 ms to interactive input for no quota saving.
func TestRelayV2GatewayDoesNotPaceAlreadyPushedDeliveries(t *testing.T) {
	mb := &pushedDeliveryMailbox{
		items: []mailboxItem{
			{Cursor: 1, Envelope: s6bInput(t, 1, "m/s1", []byte("a"))},
			{Cursor: 2, Envelope: s6bInput(t, 2, "m/s1", []byte("b"))},
		},
		waited: make(chan time.Time, 2),
	}
	b := NewCommandBridge(CommandBridgeConfig{Mailbox: mb, Key: s6bKey(), EpochID: 1})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	defer func() { cancel(); <-done }()

	first := <-mb.waited
	select {
	case second := <-mb.waited:
		if delay := second.Sub(first); delay >= 250*time.Millisecond {
			t.Fatalf("second already-pushed delivery was held for %v; relay-v2 receives are local channel drains, not metered relay reads", delay)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("second already-pushed delivery was held behind the retired 333 ms relay-read pacer")
	}
}

type pushedDeliveryMailbox struct {
	mu     sync.Mutex
	items  []mailboxItem
	waited chan time.Time
	next   int
}

func (m *pushedDeliveryMailbox) MailboxRead(context.Context, uint64) ([]mailboxItem, error) {
	return nil, nil
}

func (m *pushedDeliveryMailbox) MailboxWait(ctx context.Context, _ uint64) ([]mailboxItem, bool, error) {
	m.mu.Lock()
	if m.next < len(m.items) {
		item := m.items[m.next]
		m.next++
		m.mu.Unlock()
		m.waited <- time.Now()
		return []mailboxItem{item}, false, nil
	}
	m.mu.Unlock()
	<-ctx.Done()
	return nil, false, ctx.Err()
}

func (*pushedDeliveryMailbox) MailboxAppend(context.Context, string, []byte) (uint64, error) {
	return 1, nil
}

func (*pushedDeliveryMailbox) MailboxAck(context.Context, uint64) error { return nil }

// Removing the obsolete receive pacer must not turn a hostile retained tail into a
// CPU spin. The malformed item cannot advance the authenticated cursor, so the bridge
// backs off that zero-progress page while healthy pushed deliveries remain unpaced.
func TestRelayV2GatewayBacksOffAnUnconsumablePushedTail(t *testing.T) {
	mb := &poisonTailMailbox{}
	stalled := make(chan int, 1)
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb,
		StalledRetryWait: func(ctx context.Context, attempt int) error {
			select {
			case stalled <- attempt:
			case <-ctx.Done():
				return ctx.Err()
			}
			<-ctx.Done()
			return ctx.Err()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	select {
	case attempt := <-stalled:
		if attempt != 1 {
			t.Fatalf("first zero-progress backoff attempt = %d, want 1", attempt)
		}
	case <-time.After(time.Second):
		t.Fatal("unconsumable pushed tail never entered zero-progress backoff")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop from zero-progress backoff")
	}

	mb.mu.Lock()
	waits := mb.waits
	mb.mu.Unlock()
	if waits != 1 {
		t.Fatalf("an unconsumable pushed tail was received %d times before its first backoff, want 1", waits)
	}
}

type poisonTailMailbox struct {
	mu    sync.Mutex
	waits int
}

func (*poisonTailMailbox) MailboxRead(context.Context, uint64) ([]mailboxItem, error) {
	return nil, nil
}

func (m *poisonTailMailbox) MailboxWait(context.Context, uint64) ([]mailboxItem, bool, error) {
	m.mu.Lock()
	m.waits++
	m.mu.Unlock()
	return []mailboxItem{{Cursor: 1, Envelope: []byte("malformed")}}, false, nil
}

func (*poisonTailMailbox) MailboxAppend(context.Context, string, []byte) (uint64, error) {
	return 1, nil
}

func (*poisonTailMailbox) MailboxAck(context.Context, uint64) error { return nil }

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
// single sample could pass by luck. This asserts the MEDIAN over a burst, which keeps
// that anti-luck property decisively -- a 500 ms poll yields
// a median near 250 ms and fails by 2.5x -- while applying §6.0's own harness
// discipline instead of a hard per-sample maximum.
func TestS6B_GatewayInputLatencyIsNotPollGated(t *testing.T) {
	const (
		bound = 100 * time.Millisecond
		// A true 500 ms poll cannot hide under this even at its luckiest tail.
		maxBound = 400 * time.Millisecond
		// attempts is the load defence, and it is NOT a widened threshold: both bounds
		// above are untouched and every attempt is judged against them.
		//
		// A wall-clock assertion measures the host as much as the code, and under a
		// saturated `go test ./...` this one failed at a median of 141 ms while passing
		// in isolation. That is a FALSE RED, which is worse here than a missing fence:
		// it teaches the reader to re-run a red gate rather than read it, and every
		// green claim in this phase rests on someone believing a red result.
		//
		// Re-measuring costs a genuine regression almost nothing, and that is arithmetic
		// rather than hope. A 500 ms ticker delivers each sample uniformly over
		// [0, 500) ms, so P(sample <= 100 ms) = 0.2, and the median of the 16 steady
		// samples clears 100 ms only if at least 9 of them do:
		// P(Binom(16, 0.2) >= 9) ~= 6e-4 per attempt, ~2e-3 across three. A poll-gated
		// bridge still fails essentially every time; a host that is merely busy for a
		// second gets another look.
		attempts = 3
	)

	var lats []time.Duration
	var median, steadyWorst time.Duration
	var statePath string
	for attempt := 1; attempt <= attempts; attempt++ {
		lats, statePath = s6bMeasureHopLatency(t)
		median, steadyWorst = s6bMedianAndWorst(lats)
		if median <= bound && steadyWorst <= maxBound {
			break
		}
		if attempt < attempts {
			t.Logf("attempt %d/%d: median %v, worst %v (all=%v) -- re-measuring; a poll-gated "+
				"bridge fails every attempt, a loaded host does not",
				attempt, attempts, median, steadyWorst, lats)
		}
	}

	if median > bound {
		t.Fatalf("gateway-hop input latency: median %v over the steady-state samples, in each of %d attempts, want <= %v (last=%v). §6.0 budgets p50 <= 150ms for the WHOLE phone->PTY path; a 500 ms command-IN poll spends up to 3.3x that on this hop alone (ADR-007:461: 'unusable for live typing')", median, attempts, bound, lats)
	}
	if steadyWorst > maxBound {
		t.Fatalf("gateway-hop input latency: worst steady-state sample %v exceeds %v (last=%v) -- the median cleared but the tail did not, which is what a poll or a stuck regime looks like", steadyWorst, maxBound, lats)
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

const s6bHopSamples = 20

// s6bMeasureHopLatency runs ONE measurement: a fresh bridge over a fresh mailbox, one
// sealed input frame at a time, timed from the push to the lease plane's own stamp. It
// returns every sample and the inbound-checkpoint path the run actually persisted through.
func s6bMeasureHopLatency(t *testing.T) ([]time.Duration, string) {
	t.Helper()
	mb := s6bNewMailbox()
	lease := s6bNewLease(s6bHopSamples)
	b, statePath := s6bBridge(t, mb, lease)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	time.Sleep(100 * time.Millisecond) // let the loop park its first wait

	lats := make([]time.Duration, 0, s6bHopSamples)
	for i := 0; i < s6bHopSamples; i++ {
		sent := time.Now()
		mb.push(s6bInput(t, uint64(i+1), "m/s1", []byte(fmt.Sprintf("k%d", i))))
		deadline := time.Now().Add(5 * time.Second)
		for {
			at, n := lease.lastAt()
			if n == i+1 {
				lats = append(lats, at.Sub(sent))
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
	return lats, statePath
}

// s6bMedianAndWorst reduces one measurement to the two statistics the bound is stated in,
// over all samples.
func s6bMedianAndWorst(lats []time.Duration) (median, worst time.Duration) {
	steady := append([]time.Duration(nil), lats...)
	sort.Slice(steady, func(i, j int) bool { return steady[i] < steady[j] })
	return steady[len(steady)/2], steady[len(steady)-1]
}

// TestS6B_GatewayAcksStayInsideTheBudget checks the one metered operation left on
// relay-v2's pushed receive path. Local delivery-channel drains need no rate limit.
func TestS6B_GatewayAcksStayInsideTheBudget(t *testing.T) {
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

	_, _, acks := mb.counts()
	secs := elapsed.Seconds()
	maxAcks := int(1*secs) + 2

	if acks > maxAcks {
		t.Fatalf("the gateway issued %d ACKs over %.1fs, want <=%d; relay-v2 meters ACK messages", acks, secs, maxAcks)
	}
}
