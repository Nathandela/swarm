package remotegw

// The command-IN receive uses a bounded local context to periodically recheck its parent. The
// local expiry is benign idle, not a relay health or heartbeat verdict.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// silentWaitCall is one observed MailboxWait: when it was issued, and the deadline (if any) the
// caller declared on it.
type silentWaitCall struct {
	issued   time.Time
	deadline time.Time
	bounded  bool
}

// silentWaitMailbox remains idle until its receive context ends.
type silentWaitMailbox struct {
	mu    sync.Mutex
	calls []silentWaitCall
}

func (m *silentWaitMailbox) MailboxRead(context.Context, uint64) ([]relay.Item, error) {
	return nil, nil
}

func (m *silentWaitMailbox) MailboxWait(ctx context.Context, _ uint64) ([]relay.Item, bool, error) {
	dl, ok := ctx.Deadline()
	m.mu.Lock()
	m.calls = append(m.calls, silentWaitCall{issued: time.Now(), deadline: dl, bounded: ok})
	m.mu.Unlock()

	<-ctx.Done() // the silent relay: the caller's context is the ONLY thing that ends this
	return nil, false, ctx.Err()
}

func (m *silentWaitMailbox) MailboxAppend(context.Context, string, []byte) (uint64, error) {
	return 1, nil
}

func (m *silentWaitMailbox) MailboxAck(context.Context, uint64) error { return nil }

func (m *silentWaitMailbox) waits() []silentWaitCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]silentWaitCall(nil), m.calls...)
}

// awaitWaits blocks until the mailbox has seen n waits, or fails.
//
// within is an OBSERVATION WINDOW, not a latency assertion: it is deliberately far larger than
// anything this loop should need, so a loaded CI host cannot fail it and only a loop that never
// issues the wait can.
func awaitWaits(t *testing.T, m *silentWaitMailbox, n int, within time.Duration) []silentWaitCall {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if calls := m.waits(); len(calls) >= n {
			return calls
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d mailbox waits were issued in %v, want %d", len(m.waits()), within, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// runBridge starts b.Run under a cancellable context and returns a stop func that cancels it and
// asserts the loop actually unwound.
func runBridge(t *testing.T, b *CommandBridge) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = b.Run(ctx) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("CommandBridge.Run did not return after its context was cancelled")
		}
	}
}

// TestInboundWait_CarriesItsOwnDeadline proves each receive has the local periodic-recheck
// deadline. Parent cancellation remains authoritative; this is not a server health assertion.
func TestInboundWait_CarriesItsOwnDeadline(t *testing.T) {
	mb := &silentWaitMailbox{}
	b := NewCommandBridge(CommandBridgeConfig{Mailbox: mb})
	stop := runBridge(t, b)
	defer stop()

	calls := awaitWaits(t, mb, 1, 10*time.Second)
	first := calls[0]

	if !first.bounded {
		t.Fatal("CommandBridge.Run issued MailboxWait with no local recheck deadline")
	}
	margin := first.deadline.Sub(first.issued)
	if margin > idleRecheckInterval || margin < idleRecheckInterval-waitObservationSlack {
		t.Fatalf("MailboxWait was issued with a %v deadline; the bridge's own bound is %v.\n"+
			"The loop is not using idleRecheckInterval.", margin, idleRecheckInterval)
	}
}

// waitObservationSlack absorbs the gap between context.WithTimeout computing a deadline and the
// mailbox timestamping the call it arrives on. That gap is a function call -- microseconds -- so
// this is roughly six orders of magnitude of headroom against a loaded host, and still tight
// enough to separate the configured local interval from any other value.
const waitObservationSlack = 2 * time.Second

// TestInboundWait_LocalDeadlineIsIdleRecheck keeps an ordinary idle stream healthy. A local
// receive timeout is not a relay refusal: it wakes this loop to recheck its parent context, then
// immediately starts the next receive without latching Err or applying error retry backoff.
func TestInboundWait_LocalDeadlineIsIdleRecheck(t *testing.T) {
	mb := &silentWaitMailbox{}
	// Leave room above DrainPacer's idle spacing so this measures only the error-retry
	// backoff, not the normal read-rate ceiling.
	b := NewCommandBridge(CommandBridgeConfig{Mailbox: mb, WaitTimeout: 400 * time.Millisecond})
	stop := runBridge(t, b)
	defer stop()

	calls := awaitWaits(t, mb, 2, 10*time.Second)
	for i, c := range calls {
		if !c.bounded {
			t.Fatalf("wait %d carried no deadline", i)
		}
	}
	if got := calls[1].issued.Sub(calls[0].deadline); got > commandRetryDelay/2 {
		t.Fatalf("second idle receive began %v after the first deadline, want no %v error-retry backoff", got, commandRetryDelay)
	}
	if err := b.Err(); err != nil {
		t.Fatalf("CommandBridge.Err() = %v after an idle local deadline, want nil", err)
	}
}
