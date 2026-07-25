package transport

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// §6.0's per-hop inbound drain budget, exported so the arithmetic is legible and
// cannot drift silently. mailbox_read/mailbox_wait and mailbox_ack DO meter
// against the relay's OpsPerMin window (mailbox_append does not), so at the
// §6.0 input rate of 8 frames/s a wait that returned on the first item and acked
// it inline would cost 8 reads/s + 8 acks/s = 960/min against a 600/min window:
// the live tail dies with codeQuotaExceeded partway through the first minute.
//
// The same numbers bind BOTH hops -- the phone's live tail and the gateway's
// command-IN loop -- so remotegw uses these constants and the two types below
// rather than restating them.
const (
	// MaxDrainReadsPerSec is the sustained inbound read ceiling per routing id.
	MaxDrainReadsPerSec = 3
	// MaxDrainAcksPerSec is the batched-ack ceiling per routing id.
	MaxDrainAcksPerSec = 1
)

// followRetry is how long a live tail waits before retrying after a lost
// connection or a failed wait. It deliberately does NOT go through
// Options.Sleep: that seam exists so a test can collapse the reconnect backoff,
// and collapsing a §6.0 rate budget would silently defeat it.
const followRetry = 250 * time.Millisecond

// regimeEvidence is how many CONSECUTIVE spaced reads must come back without a
// batch before the drain concludes the spacing is unproductive and drops it. One
// is a fluctuation -- a single slow append widens one gather window past the
// spacing -- and acting on one would let one hiccup strand the drain in the
// interactive regime. Two in a row is the regime.
const regimeEvidence = 2

// DrainPacer paces one hop's inbound drain to §6.0's ceiling.
//
// The ceiling is a SUSTAINED-REGIME average, not an every-instant rule. Read as a
// flat rate it contradicts the p50 input budget: an un-batched wait returns one
// read per item, so at the sustained 8 frames/s input rate a flat 3 reads/s means
// reading every 333 ms, which adds ~167 ms of mean queueing -- more than the
// whole 150 ms budget, before any network or fsync cost (§6.0, 2026-07-25
// amendment).
//
// So there are two regimes, and the pacer switches between them on EVIDENCE
// rather than on a rate estimate -- because a rate estimate cannot tell them
// apart, both regimes being one item arriving at a time:
//
//   - BATCHING spaces reads at 1/MaxDrainReadsPerSec so each one carries a batch.
//     It is the default: assuming the ceiling binds is the safe assumption, since
//     the failure it prevents (the tail dying quota-refused mid-session) is worse
//     than the latency it costs.
//   - INTERACTIVE issues the next read as soon as the previous one returns. It is
//     entered when a SPACED read comes back with a single item: the spacing
//     produced no batch, so it bought nothing but latency -- the producer, not the
//     drain, is the limit, and reads/s can only equal arrivals/s. It is left again
//     the moment any read returns more than one item, which is what a backlog
//     actually looks like.
//
// A token bucket sized to the relay's own one-minute rate window backs both, so
// the SUSTAINED average can never exceed the ceiling however the regime flaps --
// which is what "the ceiling governs the sustained average over a 1-minute relay
// window" means operationally.
type DrainPacer struct {
	rate     float64       // reads per second
	interval time.Duration // batching-regime spacing
	capacity float64       // the one-minute window's budget
	tokens   float64
	refilled time.Time
	lastRead time.Time
	batching bool
	spaced   bool // the last admitted read was delayed by the batching spacing
	idle     int  // consecutive spaced reads that produced no batch
}

// NewDrainPacer returns a pacer at §6.0's ceiling, starting in the batching
// regime with a full one-minute window.
func NewDrainPacer() *DrainPacer {
	return &DrainPacer{
		rate:     MaxDrainReadsPerSec,
		interval: time.Second / MaxDrainReadsPerSec,
		capacity: MaxDrainReadsPerSec * 60,
		tokens:   MaxDrainReadsPerSec * 60,
		batching: true,
	}
}

// Pace blocks until the next inbound read may be issued, or ctx is done.
func (p *DrainPacer) Pace(ctx context.Context) error {
	now := time.Now()
	p.refillTo(now)

	var spacing time.Duration
	if p.batching && !p.lastRead.IsZero() {
		spacing = p.interval - now.Sub(p.lastRead)
	}
	delay := spacing
	if p.tokens < 1 {
		if need := time.Duration((1 - p.tokens) / p.rate * float64(time.Second)); need > delay {
			delay = need
		}
	}
	if delay > 0 {
		if err := sleepCtx(ctx, delay); err != nil {
			return err
		}
		p.refillTo(time.Now())
	}
	p.tokens--
	p.lastRead = time.Now()
	p.spaced = spacing > 0
	return nil
}

// Observe records how many items the read Pace just admitted came back with, and
// is what decides the regime. It must be called once per admitted read.
func (p *DrainPacer) Observe(items int) {
	if items > 1 {
		// A real backlog: the spacing is productive and the ceiling binds.
		p.batching, p.idle = true, 0
		return
	}
	if !p.spaced {
		// An unspaced read says nothing about whether spacing would have helped.
		return
	}
	p.idle++
	if p.idle >= regimeEvidence {
		p.batching = false
	}
}

func (p *DrainPacer) refillTo(now time.Time) {
	if p.refilled.IsZero() {
		p.refilled = now
		return
	}
	p.tokens += p.rate * now.Sub(p.refilled).Seconds()
	if p.tokens > p.capacity {
		p.tokens = p.capacity
	}
	p.refilled = now
}

// AckBatcher acks the relay OFF the delivery path, at most MaxDrainAcksPerSec.
//
// Placement is a LATENCY requirement, not only a quota one (§6.0, 2026-07-25
// amendment): a relay ack is one synchronous bolt fsync -- measured p50 30.8 ms
// / max 129.2 ms on the reference host -- so a single ack taken inline between
// delivering an item and re-parking the wait can consume 86% of the entire
// 150 ms p50 input budget on its own.
//
// Dropping an ack is safe. An ack is an OPTIMISATION that purges the relay's
// copy; both hops advance a DURABLE cursor before recording one, so an un-acked
// item is never re-delivered to the caller whatever the relay does with it.
type AckBatcher struct {
	ack func(ctx context.Context, cursor uint64) error

	mu      sync.Mutex
	pending uint64
}

// NewAckBatcher returns a batcher over one hop's ack call.
func NewAckBatcher(ack func(ctx context.Context, cursor uint64) error) *AckBatcher {
	return &AckBatcher{ack: ack}
}

// Record notes the highest cursor consumed so far. It never blocks and never
// performs I/O, so it is safe to call from the delivery path.
func (a *AckBatcher) Record(cursor uint64) {
	a.mu.Lock()
	if cursor > a.pending {
		a.pending = cursor
	}
	a.mu.Unlock()
}

// Run flushes at most one ack per tick until ctx is done. It is meant to be the
// body of a goroutine the drain owns and joins.
func (a *AckBatcher) Run(ctx context.Context) {
	t := time.NewTicker(time.Second / MaxDrainAcksPerSec)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.mu.Lock()
			cursor := a.pending
			a.pending = 0
			a.mu.Unlock()
			if cursor == 0 {
				continue
			}
			if err := a.ack(ctx, cursor); err != nil {
				// Keep the coordinate for the next tick. Acks are monotonic and
				// idempotent, so re-acking the same cursor costs nothing, and a newer
				// one recorded meanwhile wins.
				a.Record(cursor)
			}
		}
	}
}

// Follow is the LIVE TAIL (PB-NET-5, ADR-007 B7): it parks a bounded server-side
// wait on this session's own mailbox, hands every delivered item to fn unchanged,
// batch-acks off the delivery path, and repeats until ctx is done, returning
// ctx.Err().
//
// The wait is deliberately NOT bounded by Options.RequestTimeout. §6.0 sets the
// non-wait request timeout at 10 s and the server-side wait ceiling at 25 s, so a
// wait truncated at the request timeout would be re-issued 2.5x more often than
// the protocol intends -- invisible in a latency test, fatal in the quota
// arithmetic.
//
// Nothing here buffers or replays: items go to fn as they arrive and the
// correlation state on the connection holds one wait, never a frame. Input stays
// live-only (ADR-007 D7), and a keystroke refused during an outage is refused,
// not held.
//
// Follow and Drain are two drains of ONE mailbox and ONE cursor, so they are
// mutually exclusive on drainMu: a Drain issued while Follow is parked waits for
// the current wait to return. A caller runs one or the other.
func (s *Session) Follow(ctx context.Context, fn func(relay.Item) error) error {
	acks := NewAckBatcher(func(actx context.Context, cursor uint64) error {
		cli, err := s.live()
		if err != nil {
			return err
		}
		rctx, cancel := context.WithTimeout(actx, s.opts.RequestTimeout)
		defer cancel()
		return cli.MailboxAck(rctx, cursor)
	})
	actx, stopAcks := context.WithCancel(ctx)
	acksDone := make(chan struct{})
	go func() { defer close(acksDone); acks.Run(actx) }()
	defer func() { stopAcks(); <-acksDone }()

	pacer := NewDrainPacer()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cli, err := s.live()
		if err != nil {
			if errors.Is(err, ErrClosed) {
				return err
			}
			if err := sleepCtx(ctx, followRetry); err != nil {
				return ctx.Err()
			}
			continue
		}
		if err := pacer.Pace(ctx); err != nil {
			return ctx.Err()
		}
		n, err := s.followOnce(ctx, cli, fn, acks)
		pacer.Observe(n)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := sleepCtx(ctx, followRetry); err != nil {
				return ctx.Err()
			}
		}
	}
}

// followOnce parks one wait and delivers whatever it returns.
func (s *Session) followOnce(ctx context.Context, cli *relay.Client, fn func(relay.Item) error, acks *AckBatcher) (int, error) {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()

	from, err := s.opts.Store.Cursor()
	if err != nil {
		return 0, err
	}
	items, hasMore, err := cli.MailboxWait(ctx, from)
	if err != nil {
		return 0, err
	}
	high := from
	for _, it := range items {
		if it.Cursor > high {
			high = it.Cursor
		}
	}
	if hasMore && high <= from {
		return 0, ErrStuckPage // a page that claims more without advancing is an infinite scan
	}
	for _, it := range items {
		if err := fn(it); err != nil {
			return len(items), err
		}
	}
	if high > from {
		if err := s.opts.Store.SetCursor(high); err != nil {
			return len(items), err
		}
		acks.Record(high)
	}
	return len(items), nil
}
