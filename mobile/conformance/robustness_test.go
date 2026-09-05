package conformance_test

// FAILING-FIRST (TDD RED, GG-5) runtime guards for PB-BIND-5 (no Go panic crosses the
// boundary), PB-BIND-6 (the threading/lifecycle contract) and the S7 residuals that land
// on this surface.

import (
	"os"
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
	h.PushRoster(schema.JournalRecord{SessionID: testSession, Type: "roster", Group: "working"})

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

	// The flood below is a journal-family record, and onJournal (relay.go) only forwards
	// journal records to the dispatcher once the app has subscribed. Subscribe explicitly
	// so this exercises that gate, rather than riding NewApp's subscribed-by-default value.
	if err := h.App.SubscribeJournal(); err != nil {
		t.Fatalf("SubscribeJournal: %v", err)
	}

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

	// Wait for onJournal to have applied every flood record before unblocking the listener.
	// onJournal appends to a.journal and releases a.mu BEFORE calling a.events.emit, so
	// NextCursor == emitted only proves emit() ran for cursors 1..emitted-1 and that
	// emit(emitted) is at most in flight -- but records are applied in order by this one
	// goroutine, so at most that single emit can still be outstanding and the queue is
	// already at cap by the time this returns. Skipping this wait would let delivery below
	// race the tail of the flood still arriving, making the survivor bound meaningless.
	// The ceiling is load-tolerant, NOT the assertion (Opus round-4 F7): applying
	// CallbackQueueSize*2 journal records is real work that a CPU-contended host can
	// legitimately stretch past the shared 5 s helper, and this wait sits on the
	// REQUIRED-context path of every assertion below -- a deadline flake here fails
	// the suite on scheduling, not on the queue bound under test. The flood must
	// still FULLY arrive (the predicate is unchanged, and exact); only the wall-clock
	// allowance scales, and the early exit on success keeps a healthy run at the same
	// speed as before.
	eventuallyFor(t, 60*time.Second, "the flood never fully reached the app's journal log", func() bool {
		page, err := h.App.ReadJournal(0, 0)
		if err != nil {
			return false
		}
		next, err := page.NextCursor()
		return err == nil && next == emitted
	})

	close(l.release)

	eventually(t, "no overflow was ever surfaced despite queueing twice the stated bound", func() bool {
		return l.dropped() > 0
	})
	// The dispatcher delivers strictly in queue order, so the newest emitted cursor is
	// necessarily the LAST thing it can ever deliver. Waiting for it here means every
	// survivor assertion below reads a settled, fully-drained queue rather than a partial one.
	eventually(t, "the newest emitted cursor was never delivered", func() bool {
		cs := l.journalCursors()
		return len(cs) > 0 && cs[len(cs)-1] == emitted
	})

	cursors := l.journalCursors()
	if got := len(cursors); got > swarmmobile.CallbackQueueSize+1 {
		t.Errorf("the listener saw %d journal events for a %d-item queue (plus at most one "+
			"delivered before the callback blocked); drop-oldest is not bounding delivery", got,
			swarmmobile.CallbackQueueSize)
	}

	// drop-oldest must retain the NEWEST half of the flood and evict the oldest half -- not
	// the reverse, and not some other subset. The single event delivered before the callback
	// blocked is the only cursor allowed to predate the midpoint. (The newest half surviving
	// is already pinned above: cursors' last entry == emitted, which is > midpoint.)
	const midpoint = emitted / 2
	var oldHalf int
	for _, c := range cursors {
		if c <= midpoint {
			oldHalf++
		}
	}
	if oldHalf > 1 {
		t.Errorf("PB-BIND-6: %d cursors <= %d survived the flood; drop-oldest must evict the "+
			"OLDEST events first", oldHalf, midpoint)
	}
}

type blockingListener struct {
	release chan struct{}
	once    sync.Once

	mu      sync.Mutex
	drops   int
	cursors []int64
}

func (l *blockingListener) OnEvent(e *swarmmobile.Event) {
	l.once.Do(func() { <-l.release })
	l.mu.Lock()
	if e.Kind == "overflow" || e.Dropped > 0 {
		l.drops += e.Dropped
		if e.Dropped == 0 {
			l.drops++
		}
	}
	if e.Kind == "journal" {
		l.cursors = append(l.cursors, e.Cursor)
	}
	l.mu.Unlock()
}

func (l *blockingListener) dropped() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.drops
}

// journalCursors returns the Cursor of every "journal" event delivered so far, in delivery
// order (which is queue order: FIFO except for the single event delivered before the
// listener blocked).
func (l *blockingListener) journalCursors() []int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]int64, len(l.cursors))
	copy(out, l.cursors)
	return out
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

// TestPBKEY7_TheRevokePurgeIsNotRecoverableInPlaceAndIsNotABrick is PB-KEY-7 asked of the
// trigger the requirement actually has (ADR-007 B133).
//
// WHAT THIS TEST USED TO ASSERT AND WHY IT NO LONGER CAN. Its subject was a SCREEN LOCK: the
// lock zeroized live custody, the sealed blobs were deliberately left at rest (ADR-007 B35),
// and App.UnlockContent re-opened them through the Keystore KEK -- a local, immediate recovery
// that made the first lock survivable rather than PB-STATE-10's brick. B133 removes every
// phone-side user authentication, so there is no lock, no unlock, and no screen-lock event for
// any of that to hang from. The trigger is now REVOKE / UNPAIR, and internal/phonecore's
// Store.PurgeKeys carries the whole argument: BOTH tiers go, in memory and at rest, because a
// revoked handset that keeps a resident key -- or that restores itself with one local unwrap --
// has not been revoked in any sense the owner would recognise.
//
// SO THE RECOVERY INVERTS, AND "NOT A BRICK" SURVIVES THE INVERSION. Not-recoverable-in-place
// is not the same as bricked: re-pairing mints a fresh epoch and fresh keys and is the intended
// and only way back, which is a thing the owner can actually do. What this test now fences is
// the difference between those two, in both directions -- the in-place recovery must be GONE
// (or a revoke is cosmetic), and what is left must be a state a re-pair resolves rather than a
// dead end (or the revoke bricks the handset it was meant to protect).
//
// WHAT IT DOES NOT COVER, said plainly rather than implied by a name: it does not drive a
// re-pair. This harness seeds a paired phone by writing durable state and holds no machine-side
// pairing responder (that rig is s16MachinePairing / s10Machine). What is asserted here is that
// the purged phone lands in the state a re-pair RESOLVES -- keyless and waiting, its durable
// blob still loadable -- and not in one it cannot.
func TestPBKEY7_TheRevokePurgeIsNotRecoverableInPlaceAndIsNotABrick(t *testing.T) {
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
		t.Errorf("PB-KEY-7: decrypted session content survived the purge. Destroying the keys is " +
			"not enough while the process still holds content it already decrypted -- that is what " +
			"a revoked handset would still be showing its finder")
	}

	// KEYLESS AND WAITING, not grant-LOST. The two are one field apart and have opposite
	// remedies: PB-KEY-3's terminal state is one the user cannot act on at all, while this one
	// is cleared by the machine issuing a grant -- which is exactly what a re-pair produces.
	// Read through the classifier because that is what the screen routes on (PB-APP-9).
	werr := h.App.TerminalWatch(testSession)
	if werr == nil {
		t.Fatal("PB-KEY-7: a content operation succeeded after the purge, so the keys it needs " +
			"outlived the revoke they were destroyed by")
	}
	class, cerr := h.App.ErrorClass(werr.Error())
	if cerr != nil {
		t.Fatalf("App.ErrorClass: %v", cerr)
	}
	if class != swarmmobile.ErrClassAwaitingKey {
		t.Errorf("PB-KEY-7/PB-STATE-10: after the purge a content operation classified as %q, "+
			"want %q (error: %v). A purged phone is keyless and WAITING: the state a re-pair "+
			"clears. Classified as %q it would be the terminal grant loss instead, whose remedy "+
			"the user cannot perform -- the brick expressed as a screen.",
			class, swarmmobile.ErrClassAwaitingKey, werr, swarmmobile.ErrClassGrantLost)
	}

	// THE IN-PLACE RECOVERY IS GONE, and this assertion is the old one with its sign reversed.
	// UnlockContent re-opens a sealed content tier; the purge destroyed the blob it would open,
	// so it has nothing to do and says so by succeeding (phonecore's fileStore.UnsealContent
	// returns early when every container is already "opened, holding nothing"). What must NOT
	// happen is content operations coming back: a revoke undone by one local call is a revoke
	// that never happened.
	if err := h.App.UnlockContent(); err != nil {
		t.Fatalf("UnlockContent after a purge returned %v. It is not the recovery any more, but "+
			"it is still a verb the app calls on the wake path and it must not fail here", err)
	}
	if err := h.App.TerminalWatch(testSession); err == nil {
		t.Error("PB-KEY-7: content operations resumed after UnlockContent alone. The revoke purge " +
			"destroys both tiers precisely so that no local unwrap can undo it -- re-pairing is " +
			"the way back, and a phone that restores itself has not been revoked")
	}

	// AND IT IS NOT A BRICK. The durable blob must still LOAD: a purge that left it unreadable
	// would fail Resume, and PB-STATE-4's fail-closed refusal is the one state with no App to
	// offer the pairing flow from (ErrClassStateCorrupt), so the re-pair this test calls the
	// recovery could not even be started from the handset.
	fresh := h.openApp()
	sum, err := fresh.StateSummary()
	if err != nil {
		t.Fatalf("PB-KEY-7/PB-STATE-10: a fresh App over the purged directory will not open (%v). "+
			"The purge left the durable state unloadable, which turns a revoke into the brick "+
			"PB-STATE-10 names -- there is no App left to start a re-pair from", err)
	}
	if !sum.Restored {
		t.Errorf("PB-KEY-7: the purged phone came up with no restored state (%+v). The purge "+
			"destroys KEY MATERIAL, not the record of which machine this handset belongs to",
			sum)
	}
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
	// PB-INPUT-2 gates every keystroke on a CONFIRMED lease generation.
	h.AwaitLease(testSession)
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
	// The restarted phone holds no lease either -- one cannot survive a process death
	// (PB-INPUT-2) -- so the re-lease must be confirmed before it can type again.
	h.AwaitLease(testSession)
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

// TestPBKEY7_PurgeDropsTheAppCachesEvenWhenTheDurableWriteFails is the facade half of the
// same finding phonecore's TestS14A_R3_APurgeClearsMemoryEvenWhenTheDurableWriteFails
// fences. PB-KEY-7 requires lock to "purge decrypted session/snapshot/reply caches and
// sensitive UI state"; App.PurgeKeys returns before clearing the journal and the pending
// unlock-needs map whenever the durable write fails. A read-only data directory then leaves
// the app rendering already-decrypted session content with the screen locked.
//
// The durable failure must still be REPORTED: the sealed blobs at rest genuinely did survive
// and the caller has to know that.
//
// THE JOURNAL ASSERTION IS THE ONE THAT MEASURES THE FACADE. Peek and Roster read the CORE's
// caches, which Core.PurgeKeys clears unconditionally one layer down, so both of them pass
// with App.PurgeKeys reverted to its early return -- this test used to say nothing at all
// about the fix it is named for. a.journal and a.needs are the facade's OWN decrypted caches
// and nothing below the facade can clear them; ReadJournal is their only reader.
func TestPBKEY7_PurgeDropsTheAppCachesEvenWhenTheDurableWriteFails(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()
	h.PushTerminal(testSession, []string{"SECRET"}, 80, 24)
	eventually(t, "the snapshot never arrived", func() bool {
		snap, err := h.App.Peek(testSession)
		return err == nil && strings.Contains(snap.Text, "SECRET")
	})
	// An EVENT, not a roster record. The record has to be pageable by ReadJournal below, and
	// a roster record is not: the daemon leaves its Cursor deliberately unset (0), while
	// ReadJournal(0, ...) serves entries strictly AFTER cursor 0 -- so on the real wire a
	// roster record populates the roster and never the paged journal log. Stamping one with
	// an invented cursor is what used to make this read.
	h.PushEvent(schema.JournalRecord{Cursor: 1, SessionID: testSession, Type: "launched", Group: "working"})
	eventually(t, "the roster never arrived", func() bool {
		list, err := h.App.Roster()
		if err != nil {
			return false
		}
		n, err := list.Count()
		return err == nil && n > 0
	})
	// The facade's own journal log must actually hold something, or the assertion below
	// passes on an empty cache no matter what the purge does.
	eventually(t, "the journal read model never saw the record", func() bool {
		page, err := h.App.ReadJournal(0, 0)
		if err != nil {
			return false
		}
		n, err := page.Count()
		return err == nil && n > 0
	})

	// A full disk or a read-only app data directory, from in here.
	if err := os.Chmod(h.CoreDir, 0o500); err != nil {
		t.Fatalf("making the state dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(h.CoreDir, 0o700) })
	if f, err := os.CreateTemp(h.CoreDir, ".probe-*"); err == nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		t.Skip("this process can still write to a 0500 directory (root?); the failure this test " +
			"needs cannot be produced")
	}

	err := h.App.PurgeKeys()
	if err == nil {
		t.Fatal("fixture: the durable write was expected to fail against a read-only state dir")
	}
	if snap, perr := h.App.Peek(testSession); perr == nil && strings.Contains(snap.Text, "SECRET") {
		t.Errorf("PB-KEY-7: decrypted session content survived a lock purge whose durable write "+
			"failed (%v). Zeroizing memory cannot fail, so it must not be gated behind a write "+
			"that can", err)
	}
	if list, lerr := h.App.Roster(); lerr == nil {
		if n, cerr := list.Count(); cerr == nil && n > 0 {
			t.Errorf("PB-KEY-7: %d decrypted journal session(s) survived a lock purge whose durable "+
				"write failed", n)
		}
	}
	// The facade's OWN cache: a.journal, which only App.PurgeKeys can clear.
	page, perr := h.App.ReadJournal(0, 0)
	if perr != nil {
		t.Fatalf("ReadJournal after the purge: %v", perr)
	}
	if n, cerr := page.Count(); cerr != nil || n > 0 {
		t.Errorf("PB-KEY-7: %d decrypted journal entry(s) survived a lock purge whose durable write "+
			"failed (%v). App.PurgeKeys returned on the durable error before clearing a.journal, so "+
			"the app keeps rendering already-decrypted session content with the screen locked", n, err)
	}
}
