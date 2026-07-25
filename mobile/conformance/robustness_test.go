package conformance_test

// FAILING-FIRST (TDD RED, GG-5) runtime guards for PB-BIND-5 (no Go panic crosses the
// boundary), PB-BIND-6 (the threading/lifecycle contract) and the S7 residuals that land
// on this surface.

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	swarmmobile "github.com/Nathandela/swarm/mobile"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// TestPBBIND5_NoEntryPointPanicsAcrossTheBoundary injects a panic at EVERY entry point,
// by the two routes a real Android app produces them, and asserts each returns an error
// instead of taking the process down.
//
// A panic through JNI is not catchable in Java: the runtime aborts the process. So the
// interesting cases are the ones the app can actually reach without any cooperation from
// the Go side: a proxy whose Go object is gone (nil receiver, e.g. after the reference is
// dropped or the object was never constructed), and a zero-valued object that never went
// through NewApp. Reflection is used deliberately so the guard covers methods added
// LATER, without anyone remembering to extend a hand-written list.
func TestPBBIND5_NoEntryPointPanicsAcrossTheBoundary(t *testing.T) {
	receivers := []struct {
		name string
		v    reflect.Value
	}{
		{"(*App)(nil)", reflect.ValueOf((*swarmmobile.App)(nil))},
		{"&App{}", reflect.ValueOf(&swarmmobile.App{})},
		{"(*Pairing)(nil)", reflect.ValueOf((*swarmmobile.Pairing)(nil))},
		{"&Pairing{}", reflect.ValueOf(&swarmmobile.Pairing{})},
		{"(*SessionList)(nil)", reflect.ValueOf((*swarmmobile.SessionList)(nil))},
		{"(*JournalPage)(nil)", reflect.ValueOf((*swarmmobile.JournalPage)(nil))},
	}

	for _, r := range receivers {
		rt := r.v.Type()
		for i := 0; i < rt.NumMethod(); i++ {
			m := rt.Method(i)
			// Stop/Close on a zero value are the teardown paths; they must recover too.
			t.Run(r.name+"."+m.Name, func(t *testing.T) {
				args := make([]reflect.Value, 0, m.Type.NumIn()-1)
				for a := 1; a < m.Type.NumIn(); a++ {
					args = append(args, reflect.Zero(m.Type.In(a)))
				}

				var out []reflect.Value
				panicked := func() (p any) {
					defer func() { p = recover() }()
					out = r.v.Method(i).Call(args)
					return nil
				}()
				if panicked != nil {
					t.Fatalf("PB-BIND-5: %s.%s PANICKED (%v). A panic crossing JNI kills the app "+
						"process; every entry point must recover into an error", r.name, m.Name, panicked)
				}
				if len(out) == 0 {
					t.Fatalf("PB-BIND-5: %s.%s returns nothing, so a recovered panic is invisible "+
						"to the app", r.name, m.Name)
					return
				}
				last := out[len(out)-1]
				if last.Type().String() != "error" {
					t.Fatalf("PB-BIND-5: %s.%s does not return an error last", r.name, m.Name)
				}
				if last.IsNil() {
					t.Errorf("PB-BIND-5: %s.%s returned a nil error on an unusable receiver; the "+
						"app cannot distinguish success from a swallowed panic", r.name, m.Name)
				}
			})
		}
	}
}

// TestPBBIND5_APanickingListenerDoesNotKillTheCore. The callback runs Java code on a Go
// goroutine. If that code throws, gomobile turns it into a Go panic on a goroutine the
// core owns -- and an unrecovered panic on ANY goroutine takes the process down, not just
// the one that called in.
func TestPBBIND5_APanickingListenerDoesNotKillTheCore(t *testing.T) {
	h := newHarness(t)

	if err := h.App.SetEventListener(panicListener{}); err != nil {
		t.Fatalf("SetEventListener: %v", err)
	}
	h.PushReconcile()
	h.PushRoster(schema.JournalRecord{Cursor: 1, SessionID: testSession, Type: "roster", Group: "working"})

	eventually(t, "the core stopped making progress after a listener panicked", func() bool {
		list, err := h.App.Roster()
		if err != nil {
			return false
		}
		n, err := list.Count()
		return err == nil && n > 0
	})
}

type panicListener struct{}

func (panicListener) OnEvent(e *swarmmobile.Event) { panic("listener exploded: " + e.Kind) }

// TestPBBIND6_ConcurrentCallsAndRepeatedStartStop is PB-BIND-6's -race criterion: the
// surface is safe to call from any thread, and Start/Stop are idempotent under
// concurrency. Android will call Stop from a lifecycle callback while a UI thread is
// mid-Peek; that must not corrupt state or race.
func TestPBBIND6_ConcurrentCallsAndRepeatedStartStop(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				switch (n + j) % 6 {
				case 0:
					_ = mustNotPanic(t, func() error { return h.App.Start() })
				case 1:
					_ = mustNotPanic(t, func() error { return h.App.Stop() })
				case 2:
					_ = mustNotPanic(t, func() error { _, err := h.App.Roster(); return err })
				case 3:
					_ = mustNotPanic(t, func() error { _, err := h.App.ConnectionState(); return err })
				case 4:
					_ = mustNotPanic(t, func() error { _, err := h.App.Peek(testSession); return err })
				case 5:
					_ = mustNotPanic(t, func() error { _, err := h.App.StateSummary(); return err })
				}
			}
		}(i)
	}
	wg.Wait()

	if err := h.App.Start(); err != nil {
		t.Fatalf("the app is unusable after concurrent Start/Stop churn: %v", err)
	}
	if running, err := h.App.IsRunning(); err != nil || !running {
		t.Fatalf("IsRunning = %v, %v after a final Start; Start/Stop must be idempotent, not a counter",
			running, err)
	}
}

func mustNotPanic(t *testing.T, fn func() error) error {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Errorf("PB-BIND-6: a facade call panicked under concurrency: %v", p)
		}
	}()
	return fn()
}

// TestPBBIND6_SlowCallbackDoesNotStallTheCoreAndOverflowIsObservable pins the queue bound
// from 6.0: 256 items, drop-oldest, with a SURFACED overflow signal. A UI thread that
// blocks in OnEvent must not stop the core from draining the relay -- otherwise one slow
// frame on the main thread stalls the keystroke path.
func TestPBBIND6_SlowCallbackDoesNotStallTheCoreAndOverflowIsObservable(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()

	l := &blockingListener{release: make(chan struct{})}
	if err := h.App.SetEventListener(l); err != nil {
		t.Fatalf("SetEventListener: %v", err)
	}

	// Far more than the stated bound, so drop-oldest must engage.
	const emitted = swarmmobile.CallbackQueueSize * 2
	for i := 0; i < emitted; i++ {
		h.PushEvent(schema.JournalRecord{
			Cursor: uint64(i + 1), SessionID: testSession, Type: "group_transition", Group: "working",
		})
	}

	// The CORE must keep working while the listener is wedged.
	if err := h.App.TerminalWatch(testSession); err != nil {
		t.Fatalf("PB-BIND-6: a blocked callback stalled the core: %v", err)
	}
	h.AwaitCommand(protocol.ActionTerminalWatch)

	close(l.release)

	eventually(t, "no overflow was ever surfaced despite queueing twice the stated bound", func() bool {
		return l.dropped() > 0
	})
	if got := l.seen(); got > swarmmobile.CallbackQueueSize+emitted {
		t.Errorf("the listener saw %d events for %d emitted with a %d-item queue; the bound is not "+
			"being enforced", got, emitted, swarmmobile.CallbackQueueSize)
	}
}

type blockingListener struct {
	release chan struct{}
	once    sync.Once

	mu    sync.Mutex
	n     int
	drops int
}

func (l *blockingListener) OnEvent(e *swarmmobile.Event) {
	l.once.Do(func() { <-l.release })
	l.mu.Lock()
	l.n++
	if e.Kind == "overflow" || e.Dropped > 0 {
		l.drops += e.Dropped
		if e.Dropped == 0 {
			l.drops++
		}
	}
	l.mu.Unlock()
}

func (l *blockingListener) seen() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}

func (l *blockingListener) dropped() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.drops
}

// ---- S7 residuals ------------------------------------------------------------

// TestS7Residual_ReconcileAdoptionSurvivesProcessDeath. Recorded residual: "Reconcile
// adoption is not persisted, so every phone process death re-arms the fail-closed refusal
// of mutating ops, clearable only by a gateway reconnect the phone cannot trigger."
//
// On Android, process death is ROUTINE. A phone that must wait for a gateway-initiated
// reconnect after every kill is a phone whose Stop button does not work when the user
// most needs it -- the fail-closed path turning into the brick PB-STATE-10 forbids.
func TestS7Residual_ReconcileAdoptionSurvivesProcessDeath(t *testing.T) {
	h := newHarness(t)

	h.PushReconcile()
	eventually(t, "reconcile never adopted before the restart", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})
	if _, err := h.App.Kill(testSession); err != nil {
		t.Fatalf("pre-restart Kill: %v", err)
	}

	// Process death: drop the App and reopen over the same state directory. No new
	// reconcile record is published -- the machine has no idea the phone died.
	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.App = h.openApp()

	sum, err := h.App.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary after restart: %v", err)
	}
	if !sum.Reconciled {
		t.Errorf("S7 residual: reconcile adoption did not survive the restart, so every mutating " +
			"op is refused until the machine happens to republish. PB-STATE-10: fail-closed must " +
			"not mean bricked")
	}
	if _, err := h.App.Kill(testSession); err != nil {
		t.Fatalf("S7 residual: Kill after a restart is refused (%v). This is the fail-closed "+
			"refusal re-armed by process death, with no phone-triggerable exit", err)
	}
}

// TestS7Residual_PersistedReconcileStillRefusesAForeignAuthority is the second standing
// question applied to the fix above: does making adoption durable make a WRONG adoption
// permanent? Core.Reconcile refuses a record naming another machine or epoch, and
// Sequencer.SeedFrom is MONOTONIC -- so an adopted foreign authority would be
// unrewindable. Persisting adoption is only safe while that refusal survives the restart
// too; a persisted "reconciled" flag that skips revalidation would turn a relay's retained
// pre-rotation record into a permanent, unrewindable send-seq jump.
func TestS7Residual_PersistedReconcileStillRefusesAForeignAuthority(t *testing.T) {
	h := newHarness(t)

	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})
	before, err := h.App.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary: %v", err)
	}

	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.App = h.openApp()

	// A relay retaining a record from a DIFFERENT machine re-serves it. It must change
	// nothing: an adopted foreign InboundHighWater walks the send-seq into a range this
	// epoch's gateway stream has never seen, and monotonicity makes that permanent.
	h.sink.SetMachine("some-other-machine")
	h.PushReconcile()
	h.sink.SetMachine(h.Machine)

	time.Sleep(300 * time.Millisecond)
	after, err := h.App.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary after the foreign record: %v", err)
	}
	if after.SendSeq != before.SendSeq {
		t.Errorf("a reconcile record naming another machine moved the send-seq from %d to %d. "+
			"SeedFrom is monotonic, so this is UNREWINDABLE: every frame the phone seals from now "+
			"on is stale-dropped and no re-pair can lower the counter", before.SendSeq, after.SendSeq)
	}
	if _, err := h.App.Kill(testSession); err != nil {
		t.Errorf("the foreign record also re-armed the fail-closed refusal (%v); a rejected "+
			"authority must be a no-op, not a regression", err)
	}
}

// TestPBKEY7_PurgeKeysIsRecoverableNotABrick is the same question asked of the lock purge.
// PB-KEY-7 requires lock to zeroize custody and drop every decrypted cache. If that state
// cannot be restored by re-installing the tier key, the first screen lock bricks the app
// until a re-pair -- fail-closed turning into PB-STATE-10's brick.
func TestPBKEY7_PurgeKeysIsRecoverableNotABrick(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()
	h.PushTerminal(testSession, []string{"SECRET"}, 80, 24)
	eventually(t, "the snapshot never arrived", func() bool {
		snap, err := h.App.Peek(testSession)
		return err == nil && strings.Contains(snap.Text, "SECRET")
	})

	if err := h.App.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
	if snap, err := h.App.Peek(testSession); err == nil && strings.Contains(snap.Text, "SECRET") {
		t.Errorf("PB-KEY-7: decrypted session content survived the lock purge. Invalidating the " +
			"biometric gate is not enough while the process still holds already-decrypted content")
	}

	if err := h.App.InstallContentKey(h.Keys.ContentKey[:]); err != nil {
		t.Fatalf("PB-KEY-7/PB-STATE-10: content operations cannot be restored after a lock purge "+
			"(%v). The first screen lock would brick the app", err)
	}
	h.PushTerminal(testSession, []string{"AFTER-UNLOCK"}, 80, 24)
	eventually(t, "content operations never resumed after re-installing the content key", func() bool {
		snap, err := h.App.Peek(testSession)
		return err == nil && strings.Contains(snap.Text, "AFTER-UNLOCK")
	})
}

// TestS7Residual_OutcomeIsNeverAStaleReplyForAnotherOp. Recorded residual: "ReplyCache is
// rebuilt from an unpruned OpOutcomes map; the unkeyed Take() can hand the app stale
// outcomes (TakeFor is safe)." On this surface that becomes: App.Outcome(X) reports the
// outcome of some earlier op Y, so a Stop the user pressed shows as succeeded.
func TestS7Residual_OutcomeIsNeverAStaleReplyForAnotherOp(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})

	first, err := h.App.Kill(testSession)
	if err != nil {
		t.Fatalf("first Kill: %v", err)
	}
	h.Reply(schema.Control{Op: "ok", OperationID: first.OperationID, EndpointID: h.Machine})
	eventually(t, "the first outcome never resolved", func() bool {
		out, err := h.App.Outcome(first.OperationID)
		return err == nil && out.Resolved
	})

	second, err := h.App.Kill(testSession)
	if err != nil {
		t.Fatalf("second Kill: %v", err)
	}
	if second.OperationID == first.OperationID {
		t.Fatalf("two ops shared one operation id; idempotency keys must be unique per op")
	}

	// No reply for the SECOND op has been sent. It must read as unresolved.
	out, err := h.App.Outcome(second.OperationID)
	if err != nil {
		t.Fatalf("Outcome(second): %v", err)
	}
	if out.Resolved {
		t.Errorf("S7 residual: App.Outcome(%q) reported resolved using the reply for %q. An "+
			"unkeyed FIFO cannot attribute outcomes, and the wrong verdict for a mutating op is "+
			"worse than none", second.OperationID, first.OperationID)
	}

	// An untagged reply must match NOTHING -- not the empty key, not the pending op.
	h.Reply(schema.Control{Op: "ok", EndpointID: h.Machine})
	time.Sleep(200 * time.Millisecond)
	if out, err := h.App.Outcome(second.OperationID); err == nil && out.Resolved {
		t.Errorf("S7 residual: an UNTAGGED reply resolved op %q by proximity", second.OperationID)
	}
}

// TestPBSTATE2_TypingSurvivesAProcessDeathThroughTheFacade is requirements 4.3's exit
// criterion, asserted at the surface the Android app actually uses: after a process
// death the phone's next keystroke must still be accepted by the machine's replay guard,
// which is only true if the send-seq resumed from its DURABLE reservation ceiling.
//
// It is also the guard against the Sequencer.Next() residual: a facade that used the
// non-durable allocator would restart at 1 here and the machine-side receiver would
// reject the frame as stale.
func TestPBSTATE2_TypingSurvivesAProcessDeathThroughTheFacade(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})

	if _, err := h.App.TakeControl(testSession); err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	h.AwaitCommand(protocol.ActionTakeControl)
	if err := h.App.SendInput(testSession, []byte("before\r")); err != nil {
		t.Fatalf("SendInput before the kill: %v", err)
	}
	h.AwaitInput("data")

	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.App = h.openApp()

	// PB-STATE-8: the burned reservation block is absorbed by a COMMAND frame. An input
	// frame carrying the Gap bit is dropped SILENTLY by the gateway, so the re-lease must
	// come first -- and the facade must enforce that, not the caller.
	if _, err := h.App.TakeControl(testSession); err != nil {
		t.Fatalf("post-restart TakeControl: %v", err)
	}
	if err := h.App.SendInput(testSession, []byte("after\r")); err != nil {
		t.Fatalf("post-restart SendInput: %v", err)
	}

	got := ""
	eventually(t, "the post-restart keystroke never reached the machine", func() bool {
		h.Drain()
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, in := range h.Inputs {
			if strings.Contains(string(in.Data), "after") {
				got = string(in.Data)
				return true
			}
		}
		return false
	})
	if got == "" {
		t.Fatalf("requirements 4.3: the post-restart keystroke was stale-dropped. The phone " +
			"restarted its send-seq under the same epoch, so every keystroke, take_control, launch " +
			"and kill is refused permanently -- the exit criterion failing on the second app launch")
	}

	sum, err := h.App.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary: %v", err)
	}
	if sum.SendSeq == 0 {
		t.Errorf("StateSummary.SendSeq is 0 after a restart that sent frames; the durable " +
			"reservation ceiling is not being surfaced, so PB-STATE-3 is unobservable")
	}
}
