package transport

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// The §6.0 budget. These are approved numbers, not tuning knobs: a change here
// is a change to the committee's budget.
const (
	// InitialBackoff is the first reconnect delay.
	InitialBackoff = 500 * time.Millisecond
	// BackoffFactor multiplies the delay after each failed attempt.
	BackoffFactor = 2
	// MaxBackoff is the hard ceiling on a reconnect delay.
	MaxBackoff = 30 * time.Second
	// BackoffJitter is the fraction of the delay applied as +/- randomness, so a
	// fleet reconnecting after a relay restart does not arrive as one herd.
	BackoffJitter = 0.20
	// RequestTimeout is the default bound on any non-wait request.
	RequestTimeout = 10 * time.Second
	// OpQueueLimit is how many idempotent ops may be held while disconnected.
	// The limit is reject-new: the caller is refused, never silently evicted.
	OpQueueLimit = 64
	// FlushPacing is the gap between two ops of a reconnect drain. §6.0 requires
	// that the OpQueueLimit drain "must not be issued as one burst", because the
	// relay's limiter is a TUMBLING one-minute window rather than a smooth rate:
	// a burst exhausts a window early and the tail of the drain -- plus every
	// legitimate frame sharing that window -- comes back quota-refused. It is the
	// same 8/s the §6.0 input budget paces to, which keeps a full 64-op drain
	// (~8 s) far inside one MailboxAppendPerMin window.
	FlushPacing = 125 * time.Millisecond
)

// Sentinels a caller distinguishes.
var (
	// ErrNotDelivered reports that a call was not performed, or was performed
	// with an unknown outcome, because there was no live authenticated
	// connection. It is what a live-only frame resolves to when the link is
	// down: explicitly "delivery unknown / not sent", never a silent queue
	// (ADR-007 D7).
	ErrNotDelivered = errors.New("transport: not delivered; no live connection")
	// ErrOpQueueFull refuses an idempotent op past OpQueueLimit.
	ErrOpQueueFull = errors.New("transport: idempotent op queue is full")
	// ErrClosed refuses any call on a closed session.
	ErrClosed = errors.New("transport: session is closed")
	// ErrStuckPage refuses a relay page that claims more items remain without
	// advancing the cursor: following it is an infinite scan.
	ErrStuckPage = errors.New("transport: relay page claims more items without advancing the cursor")
)

// State is the session's connection state, surfaced so a UI can say
// "reconnecting" instead of pretending to be live.
type State string

const (
	// StateConnecting means a connect attempt is in flight.
	StateConnecting State = "connecting"
	// StateConnected means a live, re-authenticated connection is bound.
	StateConnected State = "connected"
	// StateDisconnected means there is no connection and one is being retried.
	StateDisconnected State = "disconnected"
	// StateClosed is terminal.
	StateClosed State = "closed"
)

// BackoffDelay is the un-jittered delay before reconnect attempt n (1-based):
// InitialBackoff doubling to the MaxBackoff ceiling, which it never exceeds.
func BackoffDelay(attempt int) time.Duration {
	d := InitialBackoff
	for i := 1; i < attempt; i++ {
		d *= BackoffFactor
		if d >= MaxBackoff {
			return MaxBackoff
		}
	}
	return d
}

// jittered spreads base over its +/-BackoffJitter band.
func jittered(base time.Duration) time.Duration {
	lo := time.Duration(float64(base) * (1 - BackoffJitter))
	hi := time.Duration(float64(base) * (1 + BackoffJitter))
	d := base + time.Duration((rand.Float64()*2-1)*BackoffJitter*float64(base))
	if d < lo {
		return lo
	}
	if d > hi {
		return hi
	}
	return d
}

// sleepCtx is the default reconnect delay: a plain wait that a cancelled
// context cuts short.
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

// Options configures a Session. Every optional field's zero value selects the
// §6.0 default.
type Options struct {
	// URL is the relay endpoint (wss://, or a loopback ws:// under the
	// Security carve-out).
	URL string
	// Auth is the device's relay-auth identity: a public key and a signer.
	Auth relay.ClientAuth
	// Security is the transport-security policy applied to every dial,
	// including reconnects.
	Security relay.Security
	// Store holds the durable coordinates. Nil selects an in-process store,
	// which does NOT survive a restart.
	Store Store
	// OnState is called on every state transition, off the caller's goroutine.
	OnState func(State)
	// Sleep is the delay seam, used for the reconnect backoff and for the
	// FlushPacing gap between drained ops. Nil selects a real wait.
	Sleep func(ctx context.Context, d time.Duration) error
	// RequestTimeout bounds one request. Zero selects RequestTimeout.
	RequestTimeout time.Duration
}

// queuedOp is one idempotent op held while disconnected.
type queuedOp struct {
	target string
	env    []byte
}

// Session is a relay connection that survives the network dropping underneath
// it: it reconnects with a bounded, jittered backoff, re-runs the full
// signed-challenge handshake each time, and surfaces its state.
//
// It moves opaque sealed frames only. Nothing here holds a content key, so
// nothing here can open, inspect or repair what it carries (PB-NET-3).
type Session struct {
	opts Options
	rid  string

	ctx     context.Context // cancelled by Close; scopes the supervisor
	stop    context.CancelFunc
	stopped chan struct{}

	// drainMu serialises Drain. Its Cursor() -> SetCursor() is a read-modify-write
	// spanning a relay round-trip, so two concurrent drains would both read the
	// old cursor and both deliver the same page -- and on a handset a foreground
	// drain racing a push-wake drain is exactly that shape. It is taken before mu,
	// never after.
	drainMu sync.Mutex

	mu     sync.Mutex
	state  State
	cli    *relay.Client
	queue  []queuedOp
	closed bool
}

// Dial opens a session and completes the relay-auth handshake. A failed connect
// returns an error and no Session; a connect that succeeds hands back a session
// that will keep itself connected until Close.
func Dial(ctx context.Context, opts Options) (*Session, error) {
	if opts.Sleep == nil {
		opts.Sleep = sleepCtx
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = RequestTimeout
	}
	if opts.Store == nil {
		opts.Store = newMemStore()
	}
	s := &Session{opts: opts, stopped: make(chan struct{})}
	s.ctx, s.stop = context.WithCancel(context.Background())

	s.setState(StateConnecting)
	cli, err := s.connect(ctx)
	if err != nil {
		s.stop()
		return nil, err
	}
	s.mu.Lock()
	s.cli = cli
	s.rid = cli.RoutingID()
	s.mu.Unlock()
	s.setState(StateConnected)

	go s.supervise()
	return s, nil
}

// connect performs one bounded dial under the session's security policy.
func (s *Session) connect(ctx context.Context) (*relay.Client, error) {
	dctx, cancel := context.WithTimeout(ctx, s.opts.RequestTimeout)
	defer cancel()
	return relay.DialSecure(dctx, s.opts.URL, s.opts.Auth, s.opts.Security)
}

// supervise watches the live connection and rebuilds it when it dies.
func (s *Session) supervise() {
	defer close(s.stopped)
	for {
		s.mu.Lock()
		cli := s.cli
		s.mu.Unlock()
		if cli == nil {
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case <-cli.Done():
		}
		if s.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		s.cli = nil
		s.mu.Unlock()
		s.setState(StateDisconnected)
		_ = cli.Close()
		if !s.reconnect() {
			return
		}
	}
}

// reconnect retries on the §6.0 schedule until it succeeds, or until Close.
func (s *Session) reconnect() bool {
	for attempt := 1; ; attempt++ {
		if err := s.opts.Sleep(s.ctx, jittered(BackoffDelay(attempt))); err != nil {
			return false
		}
		if s.ctx.Err() != nil {
			return false
		}
		s.setState(StateConnecting)
		cli, err := s.connect(s.ctx)
		if err != nil {
			s.setState(StateDisconnected)
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = cli.Close()
			return false
		}
		s.cli = cli
		s.mu.Unlock()
		s.setState(StateConnected)
		s.flush()
		return true
	}
}

// flush delivers the held idempotent ops, oldest first, PACED at FlushPacing so
// the drain never lands on the relay as one burst (§6.0). A send that fails
// leaves its op at the head for the next reconnect: an op is dropped only once
// the relay has accepted it.
func (s *Session) flush() {
	for {
		s.mu.Lock()
		if s.closed || len(s.queue) == 0 || s.cli == nil {
			s.mu.Unlock()
			return
		}
		op, cli := s.queue[0], s.cli
		s.mu.Unlock()

		ctx, cancel := context.WithTimeout(s.ctx, s.opts.RequestTimeout)
		_, err := cli.MailboxAppend(ctx, op.target, op.env)
		cancel()
		if err != nil {
			return
		}
		s.mu.Lock()
		if len(s.queue) > 0 {
			s.queue = s.queue[1:]
		}
		remaining := len(s.queue)
		s.mu.Unlock()
		if remaining == 0 {
			return
		}
		if err := s.opts.Sleep(s.ctx, FlushPacing); err != nil {
			return // Close, or a cancelled session: the rest stays queued.
		}
	}
}

// setState records a transition and notifies the observer off the lock.
func (s *Session) setState(st State) {
	s.mu.Lock()
	if s.state == st {
		s.mu.Unlock()
		return
	}
	s.state = st
	cb := s.opts.OnState
	s.mu.Unlock()
	if cb != nil {
		cb(st)
	}
}

// live returns the connection to use, or the reason there is none.
func (s *Session) live() (*relay.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if s.cli == nil || s.state != StateConnected {
		return nil, ErrNotDelivered
	}
	return s.cli, nil
}

// State returns the current connection state. It is exactly the state last
// reported to OnState, so a poller and an observer never disagree.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// RoutingID returns the routing id the session is bound to. It is fixed at Dial
// and unchanged by a reconnect.
func (s *Session) RoutingID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rid
}

// Queued reports how many idempotent ops are held.
func (s *Session) Queued() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// SendLive delivers a live-only frame -- raw input, resize -- and NEVER queues
// or replays it. With no live connection it fails immediately with
// ErrNotDelivered, because a keystroke replayed minutes later is a command the
// user typed once being executed twice against a different terminal state
// (ADR-007 D7).
func (s *Session) SendLive(ctx context.Context, target string, frame []byte) error {
	cli, err := s.live()
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, s.opts.RequestTimeout)
	defer cancel()
	if _, err := cli.MailboxAppend(rctx, target, frame); err != nil {
		return fmt.Errorf("%w: %w", ErrNotDelivered, err)
	}
	return nil
}

// SendOp delivers a high-level idempotent op. Connected, it is sent now and the
// relay's answer -- including a definitive refusal such as a quota cap -- is
// returned unchanged. Disconnected, it is held until the link returns, bounded
// at OpQueueLimit; the op past the bound is refused with ErrOpQueueFull rather
// than dropped, because an approve or kill the user believes was accepted is
// worse than one they were told was refused.
//
// RECORDED, not fixed here: an op sent on a connection that dies mid-request is
// not re-queued -- the caller gets the error and decides. Re-queueing it blindly
// would be wrong, because the relay commits the item BEFORE it replies, so a lost
// reply is "delivery unknown", not "not delivered". The correct answer is
// PB-GW-7's split -- a definitive pre-commit refusal is retried, a
// delivery-unknown failure is retried only as the exact same sealed envelope (a
// duplicate the receiver stale-drops for free) or abandoned -- and it needs the
// caller's sealing context, which this layer deliberately does not have.
func (s *Session) SendOp(ctx context.Context, target string, env []byte) error {
	cli, err := s.live()
	switch {
	case errors.Is(err, ErrClosed):
		return err
	case err != nil:
		return s.enqueue(target, env)
	}
	rctx, cancel := context.WithTimeout(ctx, s.opts.RequestTimeout)
	defer cancel()
	_, err = cli.MailboxAppend(rctx, target, env)
	return err
}

func (s *Session) enqueue(target string, env []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if len(s.queue) >= OpQueueLimit {
		return ErrOpQueueFull
	}
	s.queue = append(s.queue, queuedOp{target: target, env: append([]byte(nil), env...)})
	return nil
}

// Drain reads ONE bounded page from the device's mailbox, hands each item to fn
// unchanged, and reports whether more remains. One page per call is structural:
// a relay cannot wedge the caller by paginating forever, and a relay that claims
// more remains without advancing the cursor is refused with ErrStuckPage rather
// than followed.
//
// Items are the relay's opaque envelope bytes. The transport holds no key, so it
// neither opens nor reorders nor deduplicates them: that is the caller's job,
// and it is why the relay-adversary properties survive this layer.
//
// Concurrent calls are serialised, so a page is delivered to exactly one of them.
func (s *Session) Drain(ctx context.Context, fn func(relay.Item) error) (bool, error) {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()

	cli, err := s.live()
	if err != nil {
		return false, err
	}
	rctx, cancel := context.WithTimeout(ctx, s.opts.RequestTimeout)
	defer cancel()

	from, err := s.opts.Store.Cursor()
	if err != nil {
		return false, err
	}
	items, hasMore, err := cli.MailboxReadPage(rctx, from, 0)
	if err != nil {
		return false, err
	}

	high := from
	for _, it := range items {
		if it.Cursor > high {
			high = it.Cursor
		}
	}
	if hasMore && high <= from {
		return false, ErrStuckPage
	}
	for _, it := range items {
		if err := fn(it); err != nil {
			return false, err
		}
	}
	if high > from {
		if err := s.opts.Store.SetCursor(high); err != nil {
			return false, err
		}
		if err := cli.MailboxAck(rctx, high); err != nil {
			return hasMore, err
		}
	}
	return hasMore, nil
}

// Close severs the session and stops reconnecting. It is idempotent, and every
// later call is a clean ErrClosed.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cli := s.cli
	s.cli = nil
	s.mu.Unlock()

	s.stop()
	if cli != nil {
		_ = cli.Close()
	}
	<-s.stopped
	s.setState(StateClosed)
	return nil
}
