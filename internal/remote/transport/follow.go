package transport

import (
	"context"
	"sync"
	"time"
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

// connection or a failed wait. It deliberately does NOT go through
// Options.Sleep: that seam exists so a test can collapse the reconnect backoff,
// and collapsing a §6.0 rate budget would silently defeat it.

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

	// flushMu makes Reset a generation barrier without putting network I/O under
	// mu: Record remains non-blocking, while Reset waits for an already-started
	// old-generation ack to finish before its caller switches protocol incarnation.
	flushMu    sync.Mutex
	mu         sync.Mutex
	pending    uint64
	generation uint64
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

// Generation snapshots the mailbox generation a delivery started under. A caller that can
// reset concurrently with delivery must carry this token to RecordGeneration; otherwise a
// late old-store page could be recorded after Reset and inherit the replacement mailbox.
func (a *AckBatcher) Generation() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.generation
}

// RecordGeneration records cursor only if the delivery began in the still-current mailbox
// generation. The boolean lets a caller discard any separate coalescing state that belonged
// to a late page. Record remains the compatibility API for single-generation/serialized use.
func (a *AckBatcher) RecordGeneration(cursor, generation uint64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.generation != generation {
		return false
	}
	if cursor > a.pending {
		a.pending = cursor
	}
	return true
}

// Reset forgets every coordinate from the prior relay mailbox generation. A relay store
// may be restored or reinitialised while a consumer retains its durable cursor; once the
// consumer explicitly rewinds, an older numerically-larger ack must not delete items from
// the replacement mailbox or prevent its smaller cursors from being recorded.
func (a *AckBatcher) Reset() {
	a.flushMu.Lock()
	defer a.flushMu.Unlock()
	a.mu.Lock()
	a.pending = 0
	a.generation++
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
			a.flushMu.Lock()
			a.mu.Lock()
			cursor := a.pending
			generation := a.generation
			a.pending = 0
			a.mu.Unlock()
			if cursor == 0 {
				a.flushMu.Unlock()
				continue
			}
			if err := a.ack(ctx, cursor); err != nil {
				// Keep the coordinate for the next tick. Acks are monotonic and
				// idempotent INSIDE one mailbox generation. A Reset that raced this
				// call means the coordinate belongs to the retired generation and must
				// never be resurrected beside the replacement mailbox's smaller ones.
				a.mu.Lock()
				if a.generation == generation && cursor > a.pending {
					a.pending = cursor
				}
				a.mu.Unlock()
			}
			a.flushMu.Unlock()
		}
	}
}

// sleepCtx waits d, or returns early when ctx ends. It moved here from session.go when the
// dead Session was deleted (ADR-007 B98): the pacer is the only remaining caller.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
