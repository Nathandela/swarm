package managerproof

import (
	"context"
	"sync"
	"sync/atomic"
)

const custodyJobs = 128
const custodyBytes = 256 << 10

type custodyJob struct {
	size    int
	fn      func(context.Context) error
	request context.Context
	result  chan error
	fatal   bool
}

// Custody is one bounded native-observation writer. Callbacks only enqueue;
// synchronous pre-admission callers may wait outside provider/registry locks.
// The durable replay owner remains dirty on close; this worker never renews it.
type Custody struct {
	mu        sync.Mutex
	queue     chan custodyJob
	bytes     int
	closed    bool
	pending   atomic.Int64
	failed    atomic.Bool
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	onFailure func(error)
	wake      func()
	closeOnce sync.Once
}

func NewCustody(parent context.Context, onFailure func(error), wake func()) *Custody {
	ctx, cancel := context.WithCancel(parent)
	c := &Custody{queue: make(chan custodyJob, custodyJobs), ctx: ctx, cancel: cancel, done: make(chan struct{}), onFailure: onFailure, wake: wake}
	go c.run()
	return c
}
func (c *Custody) fail(err error) {
	if c.failed.CompareAndSwap(false, true) {
		c.reportFailure(err)
	}
}

func (c *Custody) reportFailure(err error) {
	c.cancel()
	if c.onFailure != nil {
		c.onFailure(err)
	}
	c.notify()
}

func (c *Custody) notify() {
	if c.wake != nil {
		c.wake()
	}
}
func (c *Custody) submit(j custodyJob) error {
	c.mu.Lock()
	if c.closed || c.failed.Load() || c.ctx.Err() != nil {
		c.mu.Unlock()
		return ErrQuarantined
	}
	if j.fn == nil || j.size < 0 || j.size > custodyBytes || c.bytes+j.size > custodyBytes {
		firstFailure := c.failed.CompareAndSwap(false, true)
		c.mu.Unlock()
		if firstFailure {
			c.reportFailure(ErrCapacity)
		}
		return ErrCapacity
	}
	c.pending.Add(1)
	c.bytes += j.size
	select {
	case c.queue <- j:
		c.mu.Unlock()
		c.notify()
		return nil
	default:
		firstFailure := c.failed.CompareAndSwap(false, true)
		c.pending.Add(-1)
		c.bytes -= j.size
		c.mu.Unlock()
		if firstFailure {
			c.reportFailure(ErrCapacity)
		}
		return ErrCapacity
	}
}
func (c *Custody) Enqueue(size int, fn func(context.Context) error) error {
	return c.submit(custodyJob{size: size, fn: fn, fatal: true})
}
func (c *Custody) Do(ctx context.Context, size int, fn func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result := make(chan error, 1)
	if err := c.submit(custodyJob{size: size, fn: fn, request: ctx, result: result}); err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return ErrQuarantined
	}
}
func (c *Custody) Pending() bool { return c != nil && (c.pending.Load() != 0 || c.Err() != nil) }
func (c *Custody) Err() error {
	if c == nil || c.failed.Load() || c.ctx.Err() != nil {
		return ErrQuarantined
	}
	return nil
}
func (c *Custody) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() { c.mu.Lock(); c.closed = true; c.mu.Unlock(); c.fail(ErrQuarantined); <-c.done })
}
func (c *Custody) run() {
	defer close(c.done)
	for {
		select {
		case <-c.ctx.Done():
			c.fail(ErrQuarantined)
			return
		case job := <-c.queue:
			var err error
			if c.ctx.Err() != nil {
				err = ErrQuarantined
			} else if job.request != nil && job.request.Err() != nil {
				err = job.request.Err()
			} else {
				err = job.fn(c.ctx)
			}
			if err != nil && job.fatal {
				c.fail(err)
			}
			c.mu.Lock()
			c.bytes -= job.size
			c.mu.Unlock()
			c.pending.Add(-1)
			c.notify()
			if job.result != nil {
				job.result <- err
			}
			if err != nil && job.fatal {
				return
			}
		}
	}
}
