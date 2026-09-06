package transport

import (
	"context"
	"sync"
	"time"
)

// MaxDrainAcksPerSec is the batched-ack ceiling per routing id. Relay-v2
// subscriptions push deliveries, so draining the client's local channel spends no
// relay operation and needs no read pacer; ACK remains an explicit metered message.
const MaxDrainAcksPerSec = 1

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
	// mu: RecordGeneration remains non-blocking, while Reset waits for an already-started
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
// to a late page.
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
