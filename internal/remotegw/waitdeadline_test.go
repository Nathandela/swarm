package remotegw

// The command-IN wait, bounded FROM THE CALLER'S SIDE.
//
// relay.Client.MailboxWait is unbounded BY CONTRACT, and that contract is pinned:
// internal/remote/relay's TestCallDeadline_TheLongPollIsNotBoundedByIt asserts the long poll
// ends on the CALLER's deadline rather than on the connection's exchange bound, because a poll
// cut by the generic call timeout would turn PB-NET-5's low-latency inbound seam into a timeout
// loop -- a worse bug than the wedge that bound exists to fix.
//
// THE COROLLARY IS THAT SOMEONE MUST BE THE CALLER WITH A DEADLINE, and nobody was.
// CommandBridge.Run handed MailboxWait the bridge's own lifetime context, which cmd/swarm-remote
// cancels only on a signal.
//
// The argument for why that was safe: the relay bounds every wait at MaxServerWait (25 s,
// §6.0). THE RELAY IS THIS DESIGN'S DECLARED ADVERSARY and that ceiling is enforced entirely at
// ITS end -- a relay that completes the websocket handshake and then answers nothing enforces
// nothing. Measured against exactly that relay, a wait was STILL PARKED AFTER 70 s: 2.8x the
// ceiling it was assumed to inherit. That is ADR-007 B94(1)'s error verbatim -- relying on the
// far end to end your call -- one hop over, and B109's claim that the wait "bypasses [the bound]
// by construction under the relay's own 25 s ceiling" is the sentence this file refutes.
//
// WHAT PARKS IS THE COMMAND-IN LOOP, so the machine stops processing keystrokes, take_control
// and kill -- with no error, no state change and no reconnect. The phone's appends still succeed
// (the relay stores them), so the UI still reads online. It is also reachable with no adversary
// at all: a half-open TCP after a WiFi -> cellular handoff answers nothing in the same way.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// specServerWaitCeiling is §6.0's "Server-side wait (long-poll) maximum | 25 s", bound to
// PB-NET-5 and transcribed from the requirements table rather than read from any constant under
// test. A test that read the code's own value would accept whatever the code happened to hold,
// which is the failure the relay package's committeeNonWaitTimeout pin was written to avoid.
const specServerWaitCeiling = 25 * time.Second

// specNonWaitRequestTimeout is §6.0's "Non-wait request timeout | 10 s", bound to PB-NET-7 and
// transcribed for the same reason.
const specNonWaitRequestTimeout = 10 * time.Second

// silentWaitCall is one observed MailboxWait: when it was issued, and the deadline (if any) the
// caller declared on it.
type silentWaitCall struct {
	issued   time.Time
	deadline time.Time
	bounded  bool
}

// silentWaitMailbox is THE RELAY THIS BOUND EXISTS FOR: the handshake completed, every frame is
// accepted, and mailbox_wait is answered never. Nothing but the caller's own context can end a
// wait against it, which is what makes the caller's context the whole of the fence.
//
// Reads and appends answer normally, so a test that fails here fails on the wait and not on a
// fake that refuses everything.
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

// TestInboundWait_CarriesItsOwnDeadline is the fence: the command-IN loop must declare a
// deadline on every MailboxWait it issues.
//
// It asserts the DEADLINE, not how long the wait took. The wall clock says nothing here -- a
// wait that returns quickly proves nothing about a relay that answers nothing -- and a duration
// assertion on this loop would be a flake on a loaded host. What is decisive is the context the
// caller handed down: whether it carries a deadline at all, and whether that deadline leaves the
// relay room to honour its own ceiling.
//
// Both halves matter and they pull in opposite directions:
//
//   - NO DEADLINE is the defect: the wait can only end when the process is signalled.
//   - A DEADLINE UNDER THE CEILING is the other bug, and the worse one -- it would cut a
//     well-behaved relay's long poll short on every cycle, converting the inbound seam back into
//     the timeout loop the relay package's boundary test forbids. So the bound must sit ABOVE
//     §6.0's 25 s with room for the frames that carry the wait and its reply.
func TestInboundWait_CarriesItsOwnDeadline(t *testing.T) {
	mb := &silentWaitMailbox{}
	b := NewCommandBridge(CommandBridgeConfig{Mailbox: mb})
	stop := runBridge(t, b)
	defer stop()

	calls := awaitWaits(t, mb, 1, 10*time.Second)
	first := calls[0]

	if !first.bounded {
		t.Fatalf("CommandBridge.Run issued MailboxWait with a context carrying NO DEADLINE.\n"+
			"The relay is the declared adversary and MaxServerWait (%v) is enforced at ITS end: a "+
			"relay that answers nothing parks this loop for the life of the process, and the "+
			"machine silently stops processing keystrokes, take_control and kill while the phone "+
			"still reads online (ADR-007 B94(1), one hop over).\n"+
			"The fix belongs HERE and not in relay.mailboxWait: the long poll ending on the "+
			"CALLER's terms is a pinned contract "+
			"(relay.TestCallDeadline_TheLongPollIsNotBoundedByIt), so this loop must BE a caller "+
			"with a deadline.", specServerWaitCeiling)
	}
	if margin := first.deadline.Sub(first.issued); margin < specServerWaitCeiling {
		t.Fatalf("MailboxWait was issued with a %v deadline, which is INSIDE §6.0's %v "+
			"server-side wait ceiling.\n"+
			"A relay that honours the ceiling would then be cut off on every cycle and the "+
			"low-latency inbound seam would become a timeout loop -- the failure "+
			"relay.TestCallDeadline_TheLongPollIsNotBoundedByIt exists to forbid. The bound is "+
			"there to end a wait the relay is NOT honouring, so it must sit above the ceiling, "+
			"not under it.", margin, specServerWaitCeiling)
	}
}

// TestInboundWait_ATimedOutWaitIsRecoverable is the other half of the fix, and without it the
// bound would only convert a silent wedge into a silent stop.
//
// A deadline that ended the loop would be no better than the wedge: the process holds ONE
// relay.Client for its lifetime (cmd/swarm-remote dials once, with no redial loop), so a
// command-IN loop that returned on the first unanswered wait would leave that process alive,
// connected, and permanently deaf -- the same observable failure by a different route. The
// timeout is therefore treated as any other wait error: recorded for Err(), backed off, and
// followed by the next wait at the same cursor, so a link that comes back is picked up on the
// following cycle with no reconnect and no lost inbound state.
//
// The relay here NEVER answers, so every wait this observes is one the bound ended.
func TestInboundWait_ATimedOutWaitIsRecoverable(t *testing.T) {
	mb := &silentWaitMailbox{}
	b := NewCommandBridge(CommandBridgeConfig{Mailbox: mb, WaitTimeout: 50 * time.Millisecond})
	stop := runBridge(t, b)
	defer stop()

	// Three waits, not two: the second could be a loop that unwinds one cycle late, the third
	// cannot. The window is an observation window and not a rate assertion -- it is 200x the
	// bound under test, so only a loop that has STOPPED can fail it.
	calls := awaitWaits(t, mb, 3, 10*time.Second)
	for i, c := range calls {
		if !c.bounded {
			t.Fatalf("wait %d carried no deadline; the loop recovered but the bound did not "+
				"survive the retry path", i)
		}
	}

	// And it is OBSERVABLE. Err() is the only channel through which an operator learns that
	// the machine is talking to a relay that answers nothing, so a recovered loop must not be
	// a silent one.
	err := b.Err()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CommandBridge.Err() = %v, want an error carrying context.DeadlineExceeded.\n"+
			"A loop that keeps retrying a relay that never answers, and reports nothing, is "+
			"indistinguishable from a healthy idle loop.", err)
	}
	if bound := b.waitTimeout().String(); !strings.Contains(err.Error(), bound) {
		t.Errorf("CommandBridge.Err() = %q, which does not name the bound (%s) that fired.\n"+
			"relay.mailboxWait reports every context ending as \"mailbox wait cancelled\", which "+
			"reads as an orderly shutdown; the loop wraps it precisely so the operator sees "+
			"WHICH deadline elapsed and how long it was.", err, bound)
	}
}

// TestInboundWait_TheBoundIsComposedFromTheBudget pins the VALUE, and pins it as a COMPOSITION.
//
// §6.0's table is prefaced "Changing any value requires committee agreement, not implementer
// discretion", and PB-NET-7 was left unmet for a round precisely because an implementer picked a
// defensible local number instead of the table's (ADR-007 B99). §6.0 binds no client-side wait
// bound, so rather than invent one this constant is composed from two values it does bind: the
// relay's own 25 s wait ceiling, plus PB-NET-7's 10 s request timeout for the frames that carry
// the wait out and its reply back.
//
// Both terms are transcribed from the table here rather than read from the code, so the test
// fails on a drift in EITHER: a re-tuned bound, or a serverWaitCeiling quietly re-pointed at
// whatever a relay claims its ceiling is.
func TestInboundWait_TheBoundIsComposedFromTheBudget(t *testing.T) {
	if serverWaitCeiling != specServerWaitCeiling {
		t.Errorf("serverWaitCeiling = %v, want §6.0's %v (\"Server-side wait (long-poll) "+
			"maximum\", PB-NET-5). This is the RELAY's ceiling and the gateway's bound must "+
			"clear it; taking it from anywhere but the table hands the adversary the number.",
			serverWaitCeiling, specServerWaitCeiling)
	}
	want := specServerWaitCeiling + specNonWaitRequestTimeout
	if defaultWaitTimeout != want {
		t.Errorf("defaultWaitTimeout = %v, want %v (§6.0's %v wait ceiling + %v non-wait request "+
			"timeout).\nUnder the ceiling, a relay that HONOURS its ceiling is cut off on every "+
			"cycle and the inbound seam becomes a timeout loop. Far above it, the loop stays deaf "+
			"for longer than it has to. If a different bound is right, the composition belongs in "+
			"§6.0's table, not in a local re-derivation -- that is exactly what left PB-NET-7 "+
			"unmet in round 6 (ADR-007 B99).",
			defaultWaitTimeout, want, specServerWaitCeiling, specNonWaitRequestTimeout)
	}
}
