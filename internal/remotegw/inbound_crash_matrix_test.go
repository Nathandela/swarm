// FAILING-FIRST (TDD RED, GG-5) tests for PB-GW-3 (§6.0b): a PER-FRAME-CLASS crash
// matrix. A local transaction cannot atomically span the persisted inbound high-water,
// the persisted read cursor, an external PTY/daemon side effect, and the relay ack, so
// there is no single "atomic commit" rule -- each class states its own allowed-loss /
// duplicate-prevention rule and is crash-injected at each boundary.
//
// THE RULES THESE TESTS STATE AND PIN:
//
//	LIVE INPUT (ADR-007 D7: live-only, never replayed history)
//	  order:     persist consumption BEFORE the PTY write
//	  crash after persist, before the write -> the keystroke is LOST. Allowed.
//	  crash after the write, before the relay ack -> it must NOT be typed a second time.
//	  A duplicated keystroke is a corrupted command line; a dropped one is a keystroke
//	  the user retypes. Loss is the cheaper failure, so input persists first.
//
//	HIGH-LEVEL MUTATION (kill / delete / launch)
//	  order:     forward to the daemon BEFORE persisting
//	  crash after the forward, before the persist -> the command is re-forwarded exactly
//	  ONCE, carrying the SAME operation_id, and the daemon's durable two-phase
//	  idempotency suppresses the duplicate. The window then closes: a further restart
//	  does not forward again. Loss is NOT allowed -- a dropped kill is a live session the
//	  owner believes is dead.
//	  crash after the persist, before the relay ack -> no re-forward at all.
//
//	TERMINAL WATCH / UNWATCH (unsigned reads)
//	  order:     dispatch BEFORE persisting; convergence rather than exactly-once
//	  crash after the dispatch, before the persist -> the watch is re-dispatched and
//	  converges (TerminalWatcher is idempotent per session), and the replay must never
//	  synthesise the opposite transition (a spurious Unwatch tearing down a live peek).
//	  crash after the persist, before the relay ack -> no re-dispatch.
//
// Crash injection: "the process died before anything hit disk" is memInboundState.failSave
// (nothing durable is written even though the side effect already happened); "the process
// died between the persist and the side effect" is crashBeforePTYRouter; "the process died
// before the relay ack" is retainingRelay, which never deletes what it acked. Every
// post-restart assertion is made at the replay guard: the retained envelope is re-served at
// a FRESH storage cursor, so a seeded read cursor cannot mask a missing high-water.
//
// These tests share the seam pinned by inbound_state_test.go (InboundState /
// InboundCheckpoint / InboundStream / CommandBridgeConfig.Inbound).
package remotegw

import (
	"context"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// errGatewayCrashed marks the injected crash boundary.
var errGatewayCrashed = errors.New("remotegw test: gateway process died at this boundary")

// crashBeforePTYRouter models the process dying AFTER the bridge persisted its
// consumption of an input frame and BEFORE the frame reached the PTY: the keystroke is
// never recorded, exactly as a dead process would never have written it.
type crashBeforePTYRouter struct {
	fakeLeaseRouter
}

func (c *crashBeforePTYRouter) Input(string, InputFrame) error { return errGatewayCrashed }

// TestCrashMatrix_InputLostNotDuplicatedWhenCrashBeforePTYWrite pins the input class's
// ordering rule: consumption is persisted BEFORE the PTY write, so a crash in between
// LOSES the keystroke and no restart re-types it. An implementation that persists after
// the write fails here, because the retained frame is re-delivered post-restart.
func TestCrashMatrix_InputLostNotDuplicatedWhenCrashBeforePTYWrite(t *testing.T) {
	key := inboundKey(61)
	const epoch uint32 = 7
	st := &memInboundState{}
	rl := &retainingRelay{}
	env := sealAt(t, key, epoch, 1, inputFrameWire{T: "data", Session: "m/s1", Data: []byte("rm -rf /\r")})
	rl.add(1, env)

	crashing := &crashBeforePTYRouter{}
	b1 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: &fakeForwarder{}, Leases: crashing, Key: key, EpochID: epoch,
		ReplyTarget: "phone", Inbound: st,
	})
	if _, err := b1.PollOnce(context.Background()); !errors.Is(err, errGatewayCrashed) {
		t.Fatalf("run 1 PollOnce err = %v, want the injected crash", err)
	}

	// RESTART. The relay retained the frame and re-serves it at a fresh cursor.
	rl.add(5, env)
	mgr2 := &fakeLeaseRouter{}
	b2 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: &fakeForwarder{}, Leases: mgr2, Key: key, EpochID: epoch,
		ReplyTarget: "phone", Inbound: st,
	})
	_, err := b2.PollOnce(context.Background())

	mgr2.mu.Lock()
	defer mgr2.mu.Unlock()
	if len(mgr2.inputs) != 0 {
		t.Fatalf("the keystroke was re-delivered %d time(s) after the crash, want 0: live input must "+
			"persist its consumption BEFORE the PTY write and accept the loss (ADR-007 D7). A gateway "+
			"that persists nothing -- or persists after the write -- re-types a retained keystroke into "+
			"the session on every restart", len(mgr2.inputs))
	}
	if !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("post-restart PollOnce err = %v, want it to wrap crypto.ErrStaleSeq", err)
	}
}

// TestCrashMatrix_InputNotRetypedWhenCrashBeforeRelayAck pins the other input boundary:
// the write happened and the persist happened, only the relay ack was lost. The retained
// frame must be refused at the guard, so the keystroke lands exactly once in total.
func TestCrashMatrix_InputNotRetypedWhenCrashBeforeRelayAck(t *testing.T) {
	key := inboundKey(67)
	const epoch uint32 = 7
	st := &memInboundState{}
	rl := &retainingRelay{}
	env := sealAt(t, key, epoch, 1, inputFrameWire{T: "data", Session: "m/s1", Data: []byte("y\r")})
	rl.add(1, env)

	mgr1 := &fakeLeaseRouter{}
	b1 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: &fakeForwarder{}, Leases: mgr1, Key: key, EpochID: epoch,
		ReplyTarget: "phone", Inbound: st,
	})
	if _, err := b1.PollOnce(context.Background()); err != nil {
		t.Fatalf("run 1 PollOnce: %v", err)
	}
	mgr1.mu.Lock()
	run1 := len(mgr1.inputs)
	mgr1.mu.Unlock()
	if run1 != 1 {
		t.Fatalf("run 1 routed %d keystrokes, want 1", run1)
	}

	// RESTART with the ack never honoured; the frame comes back at a fresh cursor.
	rl.add(5, env)
	mgr2 := &fakeLeaseRouter{}
	b2 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: &fakeForwarder{}, Leases: mgr2, Key: key, EpochID: epoch,
		ReplyTarget: "phone", Inbound: st,
	})
	_, err := b2.PollOnce(context.Background())
	mgr2.mu.Lock()
	defer mgr2.mu.Unlock()
	if len(mgr2.inputs) != 0 {
		t.Fatalf("the keystroke was typed %d extra time(s) after a crash before the relay ack, want 0: "+
			"the durable inbound high-water -- not the relay's ack-deletion -- is what must make a "+
			"consumed keystroke unrepeatable", len(mgr2.inputs))
	}
	if !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("post-restart PollOnce err = %v, want it to wrap crypto.ErrStaleSeq", err)
	}
}

// TestCrashMatrix_MutationDuplicateBoundedToOneRedelivery pins the mutation class: the
// command is forwarded BEFORE the persist, so a crash in between re-forwards it exactly
// once (same operation_id -> the daemon's durable two-phase idempotency dedups it) and the
// duplicate window then CLOSES. Without persisted state the window never closes: every
// restart re-forwards, for as long as the relay retains the frame.
func TestCrashMatrix_MutationDuplicateBoundedToOneRedelivery(t *testing.T) {
	key := inboundKey(71)
	const epoch uint32 = 7
	st := &memInboundState{failSave: errGatewayCrashed}
	rl := &retainingRelay{}
	env := sealAt(t, key, epoch, 1, killCmd("m/s1", "op-kill-1"))
	rl.add(1, env)

	poll := func(cursor uint64) *fakeForwarder {
		if cursor != 0 {
			rl.add(cursor, env)
		}
		fwd := &fakeForwarder{}
		b := NewCommandBridge(CommandBridgeConfig{
			Mailbox: rl, Forwarder: fwd, Key: key, EpochID: epoch, ReplyTarget: "phone", Inbound: st,
		})
		_, err := b.PollOnce(context.Background())
		t.Logf("poll err = %v", err)
		return fwd
	}

	// Run 1: the daemon executes the kill, then the process dies before anything is durable.
	f1 := poll(0)
	if len(f1.seen) != 1 {
		t.Fatalf("run 1 forwarded %d, want 1", len(f1.seen))
	}

	// Run 2: state persists normally now. The retained frame is re-forwarded ONCE -- allowed,
	// and deduped downstream because the operation_id is unchanged.
	st.mu.Lock()
	st.failSave = nil
	st.mu.Unlock()
	f2 := poll(5)
	if len(f2.seen) != 1 {
		t.Fatalf("run 2 forwarded %d, want exactly 1 re-forward: a mutation must not be LOST by a crash "+
			"between the daemon forward and the persist", len(f2.seen))
	}
	if f2.seen[0].OperationID != f1.seen[0].OperationID {
		t.Fatalf("re-forwarded operation_id = %q, want the original %q: the daemon's durable two-phase "+
			"idempotency is what suppresses this duplicate, and it keys on the operation_id",
			f2.seen[0].OperationID, f1.seen[0].OperationID)
	}

	// Run 3: the window is closed.
	f3 := poll(9)
	if len(f3.seen) != 0 {
		t.Fatalf("run 3 forwarded the retained kill %d more time(s), want 0: with nothing persisted the "+
			"duplicate window never closes -- every restart re-forwards for as long as the relay retains "+
			"the frame, and the daemon's idempotency store is the ONLY thing standing between a replayed "+
			"kill and a dead session", len(f3.seen))
	}
}

// TestCrashMatrix_MutationNotReforwardedWhenCrashBeforeRelayAck pins the mutation class's
// other boundary: forward done, persist done, only the ack lost -> no re-forward at all.
func TestCrashMatrix_MutationNotReforwardedWhenCrashBeforeRelayAck(t *testing.T) {
	key := inboundKey(73)
	const epoch uint32 = 7
	st := &memInboundState{}
	rl := &retainingRelay{}
	env := sealAt(t, key, epoch, 1, killCmd("m/s1", "op-kill-2"))
	rl.add(1, env)

	fwd1 := &fakeForwarder{}
	b1 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: fwd1, Key: key, EpochID: epoch, ReplyTarget: "phone", Inbound: st,
	})
	if _, err := b1.PollOnce(context.Background()); err != nil {
		t.Fatalf("run 1 PollOnce: %v", err)
	}
	if len(fwd1.seen) != 1 {
		t.Fatalf("run 1 forwarded %d, want 1", len(fwd1.seen))
	}

	rl.add(5, env)
	fwd2 := &fakeForwarder{}
	b2 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: fwd2, Key: key, EpochID: epoch, ReplyTarget: "phone", Inbound: st,
	})
	_, err := b2.PollOnce(context.Background())
	if len(fwd2.seen) != 0 {
		t.Fatalf("the retained kill was re-forwarded %d time(s) after a crash before the relay ack, want "+
			"0: once the consumption is durable the gateway itself must refuse the replay rather than "+
			"leaning on the daemon to absorb it", len(fwd2.seen))
	}
	if !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("post-restart PollOnce err = %v, want it to wrap crypto.ErrStaleSeq", err)
	}
}

// TestCrashMatrix_WatchConvergesAndDuplicateWindowCloses pins the watch/unwatch class:
// a crash between the dispatch and the persist re-dispatches the watch (idempotent, so it
// converges on one live peek) and NEVER synthesises the opposite transition; the window
// then closes on the next restart.
func TestCrashMatrix_WatchConvergesAndDuplicateWindowCloses(t *testing.T) {
	key := inboundKey(79)
	const epoch uint32 = 7
	st := &memInboundState{failSave: errGatewayCrashed}
	rl := &retainingRelay{}
	env := sealAt(t, key, epoch, 1, protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionTerminalWatch, Session: "m/s1",
	}})
	rl.add(1, env)

	poll := func(cursor uint64) *fakeTerminalWatchRouter {
		if cursor != 0 {
			rl.add(cursor, env)
		}
		w := &fakeTerminalWatchRouter{}
		b := NewCommandBridge(CommandBridgeConfig{
			Mailbox: rl, Forwarder: &fakeForwarder{}, Watchers: w, Key: key, EpochID: epoch,
			ReplyTarget: "phone", Inbound: st,
		})
		_, err := b.PollOnce(context.Background())
		t.Logf("poll err = %v", err)
		return w
	}

	w1 := poll(0)
	if len(w1.watched) != 1 {
		t.Fatalf("run 1 dispatched %d watches, want 1", len(w1.watched))
	}

	st.mu.Lock()
	st.failSave = nil
	st.mu.Unlock()
	w2 := poll(5)
	if len(w2.watched) != 1 {
		t.Fatalf("run 2 dispatched %d watches, want exactly 1: a watch lost to a crash before the persist "+
			"must converge by being re-dispatched, not be dropped", len(w2.watched))
	}
	if len(w2.unwatched) != 0 {
		t.Fatalf("run 2 dispatched %d unwatches, want 0: a replay must never synthesise the opposite "+
			"transition and tear down a live peek", len(w2.unwatched))
	}

	w3 := poll(9)
	if len(w3.watched) != 0 || len(w3.unwatched) != 0 {
		t.Fatalf("run 3 dispatched watch=%d unwatch=%d, want 0/0: once the consumption is durable the "+
			"replay must be refused at the guard, so the peek plane stops re-rendering the same retained "+
			"watch on every restart", len(w3.watched), len(w3.unwatched))
	}
}

// TestCrashMatrix_WatchNotRedispatchedWhenCrashBeforeRelayAck pins the watch class's other
// boundary: dispatch done, persist done, only the ack lost -> no re-dispatch.
func TestCrashMatrix_WatchNotRedispatchedWhenCrashBeforeRelayAck(t *testing.T) {
	key := inboundKey(83)
	const epoch uint32 = 7
	st := &memInboundState{}
	rl := &retainingRelay{}
	env := sealAt(t, key, epoch, 1, protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionTerminalWatch, Session: "m/s1",
	}})
	rl.add(1, env)

	w1 := &fakeTerminalWatchRouter{}
	b1 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: &fakeForwarder{}, Watchers: w1, Key: key, EpochID: epoch,
		ReplyTarget: "phone", Inbound: st,
	})
	if _, err := b1.PollOnce(context.Background()); err != nil {
		t.Fatalf("run 1 PollOnce: %v", err)
	}
	if len(w1.watched) != 1 {
		t.Fatalf("run 1 dispatched %d watches, want 1", len(w1.watched))
	}

	rl.add(5, env)
	w2 := &fakeTerminalWatchRouter{}
	b2 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: &fakeForwarder{}, Watchers: w2, Key: key, EpochID: epoch,
		ReplyTarget: "phone", Inbound: st,
	})
	_, err := b2.PollOnce(context.Background())
	if len(w2.watched) != 0 {
		t.Fatalf("the retained watch was re-dispatched %d time(s) after a crash before the relay ack, "+
			"want 0", len(w2.watched))
	}
	if !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("post-restart PollOnce err = %v, want it to wrap crypto.ErrStaleSeq", err)
	}
}
