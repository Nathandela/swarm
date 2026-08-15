package remotegw

// Bead agents-tracker-hggx.4 (Wave R3, machine side) -- FAILING-FIRST (TDD RED, GG-5)
// tests for the wake-obligation machine (ADR-015 P9, docs/specifications/push-gateway-api.md
// §6). READ ADR-015 P9/P12 and the spec's §6 before touching anything below; they bind
// exactly, per the task.
//
// THE SEAM these tests pin, so GREEN has one shape to build against:
//
//	type ObligationState string
//	const (
//		ObligationPending   ObligationState = "pending"
//		ObligationInFlight  ObligationState = "in_flight"
//		ObligationDelivered ObligationState = "delivered"
//		ObligationExpired   ObligationState = "expired"
//		ObligationAbandoned ObligationState = "abandoned"
//	)
//
//	// WakeObligation is the durable record keyed (push_address, wake_seq) -- PG-OBL-1.
//	// It persists the SEALED bytes (sealed ONCE, PG-WAKE-12), never re-sealed by a retry.
//	type WakeObligation struct {
//		Address     PushAddress
//		WakeSeq     uint64
//		Envelope    []byte // the exact 74-byte WakeV1
//		IssuedAt    time.Time
//		ExpiresAt   time.Time
//		State       ObligationState
//		Attempts    int
//		Coalesced   int    // PG-OBL-5/6: triggers that landed on this obligation while live
//		LastOutcome string
//	}
//
//	// ObligationStore is durable custody, mirroring Outbox's reserve-before-effect
//	// discipline (outbox.go): Put must be durable before it returns.
//	type ObligationStore interface {
//		Put(ob WakeObligation) error
//		Get(addr PushAddress) (WakeObligation, bool, error)
//		Pending() ([]WakeObligation, error) // non-terminal, oldest first, for restart re-drive
//	}
//	func OpenObligationStore(path string) (ObligationStore, error) // "" => in-memory
//
//	// WakeSubmitError is the gateway's typed refusal (spec §4); Retryable -- not the
//	// status code -- is the ONLY field the state machine transitions on (PG-ERR-3).
//	type WakeSubmitError struct {
//		Code      string
//		Retryable bool
//	}
//	func (e *WakeSubmitError) Error() string
//
//	// WakeSubmitter is the gateway HTTP seam (spec §3.5, wakesubmitter_test.go's
//	// HTTPWakeSubmitter is the production implementation). A nil error is 200
//	// provider_accepted; a *WakeSubmitError is a parsed gateway refusal; any other error
//	// is a transport failure/timeout, which P9 makes unconditionally retryable
//	// (PG-ERR-1's status-based fallback covers the same case on the wire).
//	type WakeSubmitter interface {
//		SubmitWake(ctx context.Context, envelope []byte) error
//	}
//
//	type WakeObligationConfig struct {
//		Store     ObligationStore
//		Submitter WakeSubmitter
//		WakeKey   crypto.WakeKey
//		Address   PushAddress
//		Seq       SeqSource // durable wake_seq (PG-WAKE-16): starts at 1, never reused after restart
//		Now       func() time.Time
//	}
//	func NewWakeObligationMachine(cfg WakeObligationConfig) *WakeObligationMachine
//
//	// Trigger coalesces into the address's live (non-terminal) obligation or mints and
//	// DURABLY persists a fresh one (PG-OBL-2/4/5) -- this call IS "the obligation append".
//	// The caller publishes the mailbox record AFTER Trigger returns nil, matching
//	// PG-OBL-2's "before or atomically with": obligation-first is the safe ordering,
//	// because the alternative (append first, obligation only in memory) is the specific
//	// crash window PG-OBL-2 forbids.
//	func (m *WakeObligationMachine) Trigger() error
//
//	// Drive attempts delivery of the address's live obligation, if any, exactly once and
//	// applies the resulting §6.4 transition. It durably marks in_flight BEFORE calling
//	// the submitter (so a crash mid-call recovers as in_flight, not as never-attempted),
//	// and durably marks the terminal/back-to-pending outcome after. An obligation whose
//	// expiry has passed is marked expired without a submit attempt; PG-OBL-6 re-mints
//	// immediately if triggers were coalesced into it. Restart calls Drive (after loading
//	// Pending()) to re-drive every persisted non-terminal obligation byte-identically
//	// (PG-OBL-8, PG-WAKE-12).
//	func (m *WakeObligationMachine) Drive(ctx context.Context) error
//
// NOTE ON §6.4's "abandoned" states: the header's Precedence clause records that this is
// the spec's own open question (§14.1 row 7) and, absent a ruling, defers to ADR-015 P9's
// literal (everything-retryable) text. This task's own instructions are explicit that the
// RED suite must pin the FULL three-way mapping the spec's §6.4 table already states
// (retryable / abandoned / terminal) -- see wakesubmitter_test.go -- so this file follows
// that instruction rather than P9's literal fallback: abandoned is a real terminal state
// here, not a divergence this suite silently resolves.
//
// This file contains NO implementation.

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/status"
)

// --- fakes -------------------------------------------------------------------------

// errObligationCrash marks an injected crash boundary: the process died here, exactly
// as the inbound/outbox crash-matrix tests model it (errGatewayCrashed,
// inbound_crash_matrix_test.go; errReplyLost, outbox_restart_test.go).
var errObligationCrash = errors.New("remotegw test: process died at this obligation boundary")

// fakeObligationStore is an in-memory ObligationStore whose Put can be made to fail
// for a specific State, modelling a crash between "the write was attempted" and "the
// write is durable" at exactly the boundary a test cares about. A failed Put does NOT
// adopt the new record -- mirroring fileOutbox.adoptLocked's persist-then-adopt order
// (outbox.go:169-177) -- so the store left behind is exactly what a crashed real file
// would leave: the last SUCCESSFUL write, nothing torn.
type fakeObligationStore struct {
	mu       sync.Mutex
	byAddr   map[PushAddress]WakeObligation
	failOn   map[ObligationState]error
	putCalls []WakeObligation
}

func newFakeObligationStore() *fakeObligationStore {
	return &fakeObligationStore{byAddr: map[PushAddress]WakeObligation{}, failOn: map[ObligationState]error{}}
}

func (s *fakeObligationStore) Put(ob WakeObligation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putCalls = append(s.putCalls, ob)
	if err := s.failOn[ob.State]; err != nil {
		return err
	}
	s.byAddr[ob.Address] = ob
	return nil
}

func (s *fakeObligationStore) Get(addr PushAddress) (WakeObligation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ob, ok := s.byAddr[addr]
	return ob, ok, nil
}

func (s *fakeObligationStore) Pending() ([]WakeObligation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []WakeObligation
	for _, ob := range s.byAddr {
		if ob.State == ObligationPending || ob.State == ObligationInFlight {
			out = append(out, ob)
		}
	}
	return out, nil
}

// fakeSubmitter is a WakeSubmitter that records every envelope it was asked to send
// (byte for byte, so a retry's identity is checkable) and returns canned outcomes in
// call order; the last outcome repeats once the list is exhausted, so a test needs only
// as many entries as it cares to distinguish.
type fakeSubmitter struct {
	mu       sync.Mutex
	sent     [][]byte
	outcomes []error
}

func (s *fakeSubmitter) SubmitWake(_ context.Context, env []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, append([]byte(nil), env...))
	if len(s.outcomes) == 0 {
		return nil
	}
	idx := len(s.sent) - 1
	if idx >= len(s.outcomes) {
		idx = len(s.outcomes) - 1
	}
	return s.outcomes[idx]
}

func (s *fakeSubmitter) all() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.sent...)
}

// submitterFunc adapts a plain function to WakeSubmitter (http.HandlerFunc's pattern),
// for the one test that needs to observe the machine's OWN state mid-call.
type submitterFunc func(ctx context.Context, env []byte) error

func (f submitterFunc) SubmitWake(ctx context.Context, env []byte) error { return f(ctx, env) }

// countingSeq wraps a SeqSource and counts Next() calls, which is how the coalescing
// tests prove that a coalesced trigger consumes NO wake_seq (PG-OBL-5) without needing
// to inspect the machine's internals.
type countingSeq struct {
	mu    sync.Mutex
	inner SeqSource
	calls int
}

func (c *countingSeq) Next() (uint64, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner.Next()
}
func (c *countingSeq) Issued() uint64 { return c.inner.Issued() }
func (c *countingSeq) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// obligationHarness bundles one address's machine with its fakes, over a virtual clock
// so PG-WAKE-7's five-minute expiry is exercised without a real sleep.
type obligationHarness struct {
	addr  PushAddress
	store *fakeObligationStore
	sub   *fakeSubmitter
	seq   *countingSeq
	clk   *testClock
	m     *WakeObligationMachine
}

func newObligationHarness(t *testing.T) *obligationHarness {
	t.Helper()
	store := newFakeObligationStore()
	sub := &fakeSubmitter{}
	inner, err := OpenSeqSource("")
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	seq := &countingSeq{inner: inner}
	clk := newTestClock()
	addr := testPushAddress(0x30)
	m := NewWakeObligationMachine(WakeObligationConfig{
		Store: store, Submitter: sub, WakeKey: testWakeKey(), Address: addr, Seq: seq, Now: clk.Now,
	})
	return &obligationHarness{addr: addr, store: store, sub: sub, seq: seq, clk: clk, m: m}
}

// --- PG-OBL-1/2: the record and its durability ---------------------------------------

// TestObligation_TriggerDurablyPersistsTheSealedWakeBeforeReturning pins PG-OBL-1: the
// keyed record (push_address, wake_seq), the sealed 74-byte envelope, issued_at/expires_at
// and the initial state all exist BEFORE Trigger returns.
func TestObligation_TriggerDurablyPersistsTheSealedWakeBeforeReturning(t *testing.T) {
	h := newObligationHarness(t)
	if err := h.m.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	ob, ok, err := h.store.Get(h.addr)
	if err != nil || !ok {
		t.Fatalf("store.Get after Trigger: ok=%v err=%v, want a durably persisted obligation", ok, err)
	}
	if ob.WakeSeq != 1 {
		t.Fatalf("WakeSeq = %d, want 1 (PG-WAKE-16: starts at 1)", ob.WakeSeq)
	}
	if len(ob.Envelope) != WakeV1Size {
		t.Fatalf("Envelope is %d bytes, want the pinned WakeV1Size %d", len(ob.Envelope), WakeV1Size)
	}
	if ob.State != ObligationPending {
		t.Fatalf("State = %q, want %q", ob.State, ObligationPending)
	}
	if !ob.ExpiresAt.Equal(ob.IssuedAt.Add(WakeV1Expiry)) {
		t.Fatalf("ExpiresAt = %v, want IssuedAt + %s = %v", ob.ExpiresAt, WakeV1Expiry, ob.IssuedAt.Add(WakeV1Expiry))
	}
}

// TestObligation_MarksInFlightDurablyBeforeCallingTheSubmitter pins the ordering §6.4's
// "pending -> in_flight" row requires: the durable write happens BEFORE the network
// call, not after, so a crash mid-call recovers as in_flight (retry-safe) rather than as
// "never attempted" (which risks a second obligation for the same trigger).
func TestObligation_MarksInFlightDurablyBeforeCallingTheSubmitter(t *testing.T) {
	h := newObligationHarness(t)
	if err := h.m.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	var sawStateAtCallTime ObligationState
	h.m = NewWakeObligationMachine(WakeObligationConfig{
		Store: h.store, WakeKey: testWakeKey(), Address: h.addr, Seq: h.seq, Now: h.clk.Now,
		Submitter: submitterFunc(func(context.Context, []byte) error {
			ob, _, _ := h.store.Get(h.addr)
			sawStateAtCallTime = ob.State
			return nil
		}),
	})
	if err := h.m.Drive(context.Background()); err != nil {
		t.Fatalf("Drive: %v", err)
	}
	if sawStateAtCallTime != ObligationInFlight {
		t.Fatalf("obligation state AT SUBMIT TIME = %q, want %q: the in_flight transition must be "+
			"durable before the gateway call (spec §6.4)", sawStateAtCallTime, ObligationInFlight)
	}
}

// --- PG-OBL-5/6: coalescing ----------------------------------------------------------

// TestObligation_CoalescesIntoALiveObligationWithoutMintingANewSeq pins PG-OBL-5: while
// an obligation for an address is non-terminal, a second Trigger consumes NO wake_seq.
func TestObligation_CoalescesIntoALiveObligationWithoutMintingANewSeq(t *testing.T) {
	h := newObligationHarness(t)
	if err := h.m.Trigger(); err != nil {
		t.Fatalf("Trigger #1: %v", err)
	}
	first, _, _ := h.store.Get(h.addr)

	if err := h.m.Trigger(); err != nil {
		t.Fatalf("Trigger #2 (coalescing): %v", err)
	}
	if got := h.seq.count(); got != 1 {
		t.Fatalf("SeqSource.Next() called %d times after two triggers on one live obligation, want 1: "+
			"a coalesced trigger must not mint a wake_seq (PG-OBL-5)", got)
	}
	second, _, _ := h.store.Get(h.addr)
	if second.WakeSeq != first.WakeSeq {
		t.Fatalf("WakeSeq changed on coalesce: %d -> %d, want unchanged", first.WakeSeq, second.WakeSeq)
	}
	if !bytes.Equal(second.Envelope, first.Envelope) {
		t.Fatal("Envelope bytes changed on coalesce -- a coalesced trigger must not reseal (PG-WAKE-12)")
	}
	if second.Coalesced != 1 {
		t.Fatalf("Coalesced = %d, want 1 after one coalesced trigger", second.Coalesced)
	}

	// Once the obligation reaches a TERMINAL state, the next trigger mints fresh.
	h.sub.outcomes = []error{nil}
	if err := h.m.Drive(context.Background()); err != nil {
		t.Fatalf("Drive: %v", err)
	}
	if err := h.m.Trigger(); err != nil {
		t.Fatalf("Trigger #3 (post-terminal): %v", err)
	}
	if got := h.seq.count(); got != 2 {
		t.Fatalf("SeqSource.Next() called %d times after a fresh trigger post-delivery, want 2", got)
	}
	third, _, _ := h.store.Get(h.addr)
	if third.WakeSeq != first.WakeSeq+1 {
		t.Fatalf("WakeSeq after a fresh trigger = %d, want %d", third.WakeSeq, first.WakeSeq+1)
	}
}

// TestObligation_ExpiredWithCoalescedTriggersReMintsImmediately pins PG-OBL-6's first
// clause: reaching `expired` with triggers coalesced into it mints a fresh obligation
// with a NEW wake_seq and issued_at, so the owner is not left unwoken for work that is
// still waiting.
func TestObligation_ExpiredWithCoalescedTriggersReMintsImmediately(t *testing.T) {
	h := newObligationHarness(t)
	if err := h.m.Trigger(); err != nil {
		t.Fatalf("Trigger #1: %v", err)
	}
	if err := h.m.Trigger(); err != nil { // coalesces
		t.Fatalf("Trigger #2: %v", err)
	}
	before, _, _ := h.store.Get(h.addr)
	if before.Coalesced != 1 {
		t.Fatalf("control: Coalesced = %d, want 1", before.Coalesced)
	}

	h.clk.advance(WakeV1Expiry + time.Second)
	if err := h.m.Drive(context.Background()); err != nil {
		t.Fatalf("Drive past expiry: %v", err)
	}
	if len(h.sub.all()) != 0 {
		t.Fatalf("the submitter was called %d times for an already-expired obligation, want 0: "+
			"expiry is checked BEFORE a submit is attempted (spec §6.3)", len(h.sub.all()))
	}
	after, ok, _ := h.store.Get(h.addr)
	if !ok {
		t.Fatal("no obligation for the address after expiry+re-mint, want the FRESH one")
	}
	if after.State != ObligationPending {
		t.Fatalf("re-minted obligation state = %q, want %q", after.State, ObligationPending)
	}
	if after.WakeSeq != before.WakeSeq+1 {
		t.Fatalf("re-minted WakeSeq = %d, want %d (a fresh seq, PG-OBL-6)", after.WakeSeq, before.WakeSeq+1)
	}
	if !after.IssuedAt.Equal(h.clk.Now()) {
		t.Fatalf("re-minted IssuedAt = %v, want the current clock %v", after.IssuedAt, h.clk.Now())
	}
	if got := h.seq.count(); got != 2 {
		t.Fatalf("SeqSource.Next() called %d times total, want 2 (one original mint, one re-mint)", got)
	}
}

// TestObligation_ExpiredWithNoCoalescedTriggerStaysExpired is the counterweight: PG-OBL-6
// re-mints ONLY when something was coalesced. An obligation nothing landed on since
// simply expires -- there is nothing still waiting to wake the phone for.
func TestObligation_ExpiredWithNoCoalescedTriggerStaysExpired(t *testing.T) {
	h := newObligationHarness(t)
	if err := h.m.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	h.clk.advance(WakeV1Expiry + time.Second)
	if err := h.m.Drive(context.Background()); err != nil {
		t.Fatalf("Drive past expiry: %v", err)
	}
	ob, ok, _ := h.store.Get(h.addr)
	if !ok {
		t.Fatal("obligation vanished; PG-OBL-3 requires it end in a terminal state, not disappear")
	}
	if ob.State != ObligationExpired {
		t.Fatalf("State = %q, want %q (nothing was coalesced, so no re-mint)", ob.State, ObligationExpired)
	}
	if got := h.seq.count(); got != 1 {
		t.Fatalf("SeqSource.Next() called %d times, want 1: no re-mint without a coalesced trigger", got)
	}
}

// --- concurrency: a coalesced trigger during Drive's submit must survive -------------

// TestObligation_TriggerDuringDriveSubmitIsNotClobbered is the regression test for the
// BLOCKING finding of the R3 GREEN review: a Trigger landing WHILE Drive's network call
// is outstanding is a REAL interleaving -- push.go's deferred-wake timer fires on its own
// goroutine (push.go:343-356), so a trigger for the same address can land mid-submit --
// not a hypothetical. Before the fix, Drive read the record once, called the submitter
// with no lock held, and then wrote back its OWN stale pre-call copy on return, silently
// erasing the Coalesced increment (and any Attempts bump) a concurrent Trigger had
// durably recorded in between. That is PG-OBL-5's coalesced trigger being dropped and
// PG-OBL-6 never firing: the phone is never woken for work that is still waiting, which
// is exactly the failure this whole machine exists to prevent.
func TestObligation_TriggerDuringDriveSubmitIsNotClobbered(t *testing.T) {
	h := newObligationHarness(t)
	if err := h.m.Trigger(); err != nil {
		t.Fatalf("Trigger #1: %v", err)
	}

	triggered := make(chan struct{})
	h.m = NewWakeObligationMachine(WakeObligationConfig{
		Store: h.store, WakeKey: testWakeKey(), Address: h.addr, Seq: h.seq, Now: h.clk.Now,
		Submitter: submitterFunc(func(context.Context, []byte) error {
			// A second trigger lands WHILE the submit is outstanding, on a goroutine
			// standing in for push.go's deferred-wake timer.
			if err := h.m.Trigger(); err != nil {
				t.Errorf("concurrent Trigger during submit: %v", err)
			}
			close(triggered)
			return &WakeSubmitError{Code: "service_unavailable", Retryable: true}
		}),
	})
	if err := h.m.Drive(context.Background()); err == nil {
		t.Fatal("Drive: want the retryable submit error surfaced")
	}
	<-triggered

	ob, ok, _ := h.store.Get(h.addr)
	if !ok {
		t.Fatal("obligation vanished")
	}
	if ob.State != ObligationPending {
		t.Fatalf("state after the retryable failure = %q, want %q", ob.State, ObligationPending)
	}
	if ob.Coalesced != 1 {
		t.Fatalf("Coalesced = %d, want 1: the trigger that landed during the submit must not be lost", ob.Coalesced)
	}

	// PG-OBL-6 must still fire on expiry, proving the surviving Coalesced count is
	// actually load-bearing for the re-mint decision, not merely present in the struct.
	h.clk.advance(WakeV1Expiry + time.Second)
	if err := h.m.Drive(context.Background()); err != nil {
		t.Fatalf("Drive past expiry: %v", err)
	}
	after, ok, _ := h.store.Get(h.addr)
	if !ok || after.State != ObligationPending || after.WakeSeq != ob.WakeSeq+1 {
		t.Fatalf("after expiry: ok=%v state=%q wakeSeq=%d, want a FRESH re-minted pending obligation "+
			"(PG-OBL-6) -- the clobber bug would leave nothing coalesced to re-mint for", ok, after.State, after.WakeSeq)
	}
}

// TestObligation_DriveDoesNotStampASubmitOutcomeOntoASupersededObligation is the
// regression test for the BLOCKING finding of the R3 GREEN review: Drive used to
// re-read the obligation by ADDRESS ONLY after its network call and apply the submit
// outcome to whatever record was durable NOW, without checking it was still the SAME
// obligation it submitted. A Trigger that lands after the in-flight obligation's expiry
// passes correctly refuses to coalesce (PG-OBL-6 mints a FRESH one instead) -- and the
// bug then stamped the ORIGINAL submit's outcome onto that fresh, never-submitted
// wake_seq: delivered on success, abandoned on a non-retryable refusal, either way
// losing the wake the re-mint exists to protect (no re-mint, since Coalesced==0 on the
// fresh record; no re-drive, since both outcomes are terminal). Reproduced here without
// a lock hack: the fake submitter itself advances the virtual clock past expiry and
// re-triggers mid-call, exactly the shape a restart/redial re-drive of an obligation near
// its five-minute bound against a hanging gateway would hit for real.
func TestObligation_DriveDoesNotStampASubmitOutcomeOntoASupersededObligation(t *testing.T) {
	h := newObligationHarness(t)
	if err := h.m.Trigger(); err != nil {
		t.Fatalf("Trigger #1: %v", err)
	}
	first, _, _ := h.store.Get(h.addr)

	h.m = NewWakeObligationMachine(WakeObligationConfig{
		Store: h.store, WakeKey: testWakeKey(), Address: h.addr, Seq: h.seq, Now: h.clk.Now,
		Submitter: submitterFunc(func(context.Context, []byte) error {
			// The submit for wake_seq=1 is still outstanding when its five minutes
			// elapse and something else re-triggers for the same address -- Trigger
			// correctly refuses to coalesce into the now-expired record and mints a
			// FRESH one (wake_seq=2).
			h.clk.advance(WakeV1Expiry + time.Second)
			if err := h.m.Trigger(); err != nil {
				t.Errorf("re-trigger past expiry: %v", err)
			}
			return nil // FCM accepts the ORIGINAL (now-superseded) submit for wake_seq=1
		}),
	})
	if err := h.m.Drive(context.Background()); err != nil {
		t.Fatalf("Drive: %v", err)
	}

	fresh, ok, _ := h.store.Get(h.addr)
	if !ok {
		t.Fatal("obligation vanished")
	}
	if fresh.WakeSeq == first.WakeSeq {
		t.Fatalf("control: re-trigger past expiry did not mint a fresh wake_seq (still %d)", fresh.WakeSeq)
	}
	if fresh.State != ObligationPending {
		t.Fatalf("fresh obligation (wake_seq=%d) state = %q, want %q -- the submit outcome for the "+
			"SUPERSEDED wake_seq=%d must never be stamped onto it: it was never submitted",
			fresh.WakeSeq, fresh.State, ObligationPending, first.WakeSeq)
	}
	if fresh.Attempts != 0 {
		t.Fatalf("fresh obligation Attempts = %d, want 0: it has never been driven", fresh.Attempts)
	}
	if fresh.LastOutcome == "provider_accepted" {
		t.Fatal("fresh obligation carries the SUPERSEDED submit's outcome")
	}
}

// TestObligation_TriggerDoesNotCoalesceIntoAnAlreadyExpiredObligation pins that Trigger
// checks expiry itself rather than depending on an immediately-following Drive to
// self-heal: coalescing into a live-looking (pending/in_flight) record whose five minutes
// have already elapsed would durably record a trigger that nothing may ever re-mint for,
// if this Trigger call is not always immediately followed by a Drive -- a property only
// TransportRouter's own calling convention happens to guarantee today, not one this
// method's contract may assume of every future caller.
func TestObligation_TriggerDoesNotCoalesceIntoAnAlreadyExpiredObligation(t *testing.T) {
	h := newObligationHarness(t)
	if err := h.m.Trigger(); err != nil {
		t.Fatalf("Trigger #1: %v", err)
	}
	first, _, _ := h.store.Get(h.addr)

	h.clk.advance(WakeV1Expiry + time.Second) // the live obligation is now expired; Drive never ran
	if err := h.m.Trigger(); err != nil {
		t.Fatalf("Trigger #2 (past expiry, no Drive in between): %v", err)
	}
	second, _, _ := h.store.Get(h.addr)
	if second.WakeSeq == first.WakeSeq {
		t.Fatal("Trigger coalesced into an already-expired obligation instead of minting a fresh one")
	}
	if second.State != ObligationPending {
		t.Fatalf("state = %q, want %q", second.State, ObligationPending)
	}
	if !second.IssuedAt.Equal(h.clk.Now()) {
		t.Fatalf("IssuedAt = %v, want the current clock %v", second.IssuedAt, h.clk.Now())
	}
	if got := h.seq.count(); got != 2 {
		t.Fatalf("SeqSource.Next() called %d times, want 2 (one original mint, one fresh mint)", got)
	}
}

// TestObligation_NilSeqFailsClosedRatherThanPanicking pins that a misassembled machine
// (no durable Seq configured) refuses to mint rather than nil-interface-panicking the
// process that owns every other live session's journal bridge.
func TestObligation_NilSeqFailsClosedRatherThanPanicking(t *testing.T) {
	store := newFakeObligationStore()
	addr := testPushAddress(0xE0)
	m := NewWakeObligationMachine(WakeObligationConfig{
		Store: store, Submitter: &fakeSubmitter{}, WakeKey: testWakeKey(), Address: addr,
		// Seq deliberately left nil.
	})
	if err := m.Trigger(); err == nil {
		t.Fatal("Trigger with no Seq configured returned nil, want a fail-closed error")
	}
	if _, ok, _ := store.Get(addr); ok {
		t.Fatal("a failed mint must not leave a partial obligation behind")
	}
}

// TestObligation_WakeSeqIsNotReusedAcrossRestart is the durable half of PG-WAKE-16 that
// every OTHER test in this file cannot exercise: they all build their harness over
// OpenSeqSource(""), the silently NON-durable in-memory source. This test uses a REAL
// file-backed SeqSource and proves wake_seq resumes STRICTLY above what a previous
// process instance already issued. Reusing a seq after restart is exactly the bug the
// phone's persisted high-water is built to reject -- push.go:481-500 documents the
// mirror-image failure for the legacy path, and PG-WAKE-16 states the identical property
// for this machine's own coordinate.
func TestObligation_WakeSeqIsNotReusedAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	seqPath := filepath.Join(dir, "wake.seq")
	obPath := filepath.Join(dir, "wake-obligations")
	addr := testPushAddress(0xE1)

	seq1, err := OpenSeqSource(seqPath)
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	store1, err := OpenObligationStore(obPath)
	if err != nil {
		t.Fatalf("OpenObligationStore: %v", err)
	}
	m1 := NewWakeObligationMachine(WakeObligationConfig{
		Store: store1, Submitter: &fakeSubmitter{}, WakeKey: testWakeKey(), Address: addr, Seq: seq1, Now: time.Now,
	})
	if err := m1.Trigger(); err != nil {
		t.Fatalf("Trigger #1: %v", err)
	}
	first, _, _ := store1.Get(addr)
	if first.WakeSeq != 1 {
		t.Fatalf("first WakeSeq = %d, want 1", first.WakeSeq)
	}
	if err := m1.Drive(context.Background()); err != nil {
		t.Fatalf("Drive #1 (delivers immediately): %v", err)
	}

	// RESTART: a fresh SeqSource AND a fresh ObligationStore over the SAME durable paths
	// -- not the same in-memory instances, so this crosses the real durability boundary.
	seq2, err := OpenSeqSource(seqPath)
	if err != nil {
		t.Fatalf("reopen OpenSeqSource: %v", err)
	}
	store2, err := OpenObligationStore(obPath)
	if err != nil {
		t.Fatalf("reopen OpenObligationStore: %v", err)
	}
	m2 := NewWakeObligationMachine(WakeObligationConfig{
		Store: store2, Submitter: &fakeSubmitter{}, WakeKey: testWakeKey(), Address: addr, Seq: seq2, Now: time.Now,
	})
	if err := m2.Trigger(); err != nil {
		t.Fatalf("Trigger after restart: %v", err)
	}
	second, _, _ := store2.Get(addr)
	if second.WakeSeq <= first.WakeSeq {
		t.Fatalf("WakeSeq after restart = %d, want STRICTLY greater than the pre-restart %d (PG-WAKE-16)",
			second.WakeSeq, first.WakeSeq)
	}
}

// --- PG-OBL-8: the five-boundary crash-injection bill --------------------------------
//
// Boundaries, named as the task and playbook :741-742 name them: before obligation
// append, after append before mailbox publish, before gateway submit, after gateway
// commit before local ack, and on restart. Each must recover to AT-MOST-ONCE mailbox
// publish and AT-LEAST-ONCE byte-identical wake submission.

// TestCrashMatrix_Obligation_BeforeAppend_NothingDurableRecoversCleanly is boundary 1:
// the process dies before Trigger's own durable write lands, so nothing exists at all.
// This is the SAFE side of PG-OBL-2's ordering rule -- the alternative order (append the
// mailbox record first) is what turns this same crash into a silently lost wake, which
// is exactly what "obligation before or atomically with the append" forbids. Recovery
// here is simply: a fresh Trigger (as the upstream mailbox replay would re-drive, per
// the SAME idempotent redelivery outbox_restart_test.go already proves for the journal
// side) succeeds normally, with nothing left over from the crashed attempt.
//
// Over the REAL file-backed ObligationStore, reopened after the write, in the same
// reopen-the-same-path shape as outbox_restart_test.go/inbound_crash_matrix_test.go --
// not the in-memory fakeObligationStore other tests in this file use, so a
// persistence-format bug in loadObligations/persistObligations could not hide here.
func TestCrashMatrix_Obligation_BeforeAppend_NothingDurableRecoversCleanly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wake-obligations")
	addr := testPushAddress(0x91)

	store, err := OpenObligationStore(path)
	if err != nil {
		t.Fatalf("OpenObligationStore: %v", err)
	}
	if _, ok, _ := store.Get(addr); ok {
		t.Fatal("nothing should be durable before the first Trigger ever runs")
	}
	inner, err := OpenSeqSource("")
	if err != nil {
		t.Fatal(err)
	}
	sub := &fakeSubmitter{}
	m := NewWakeObligationMachine(WakeObligationConfig{
		Store: store, Submitter: sub, WakeKey: testWakeKey(), Address: addr, Seq: inner, Now: time.Now,
	})
	if err := m.Trigger(); err != nil {
		t.Fatalf("Trigger after the crash-before-append window: %v", err)
	}
	ob, ok, _ := store.Get(addr)
	if !ok || ob.State != ObligationPending {
		t.Fatalf("recovered obligation ok=%v state=%q, want a fresh pending record", ok, ob.State)
	}

	// REOPEN the same durable path: the write above must have actually crossed the file
	// boundary, not merely lived in the in-memory map fronting it.
	reopened, err := OpenObligationStore(path)
	if err != nil {
		t.Fatalf("reopen OpenObligationStore: %v", err)
	}
	got, ok, _ := reopened.Get(addr)
	if !ok || got.WakeSeq != ob.WakeSeq || !bytes.Equal(got.Envelope, ob.Envelope) {
		t.Fatalf("reopened store disagrees with the live one: got=%+v ok=%v, want %+v", got, ok, ob)
	}
}

// TestCrashMatrix_Obligation_AfterAppendBeforeMailboxPublish_StillDeliverable is
// boundary 2: Trigger durably succeeded, then the process died before the mailbox
// append (inner.Event) ever ran. The obligation must still exist and still be
// deliverable on restart -- at worst a content-free wake fires for a record the phone
// never received, which the empty, locator-free plaintext makes harmless (the phone
// simply reconciles and finds nothing new). What must NOT happen is the obligation
// vanishing along with the crash.
//
// Over the REAL file-backed ObligationStore: m1/store1 are abandoned rather than reused
// (modelling the crashed process exiting), and m2/store2 are built over a GENUINE reopen
// of the same path, so this actually crosses the durability boundary rather than sharing
// one in-memory map across the "before" and "after" halves.
func TestCrashMatrix_Obligation_AfterAppendBeforeMailboxPublish_StillDeliverable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wake-obligations")
	addr := testPushAddress(0x92)
	inner, err := OpenSeqSource("")
	if err != nil {
		t.Fatal(err)
	}
	store1, err := OpenObligationStore(path)
	if err != nil {
		t.Fatalf("OpenObligationStore: %v", err)
	}
	m1 := NewWakeObligationMachine(WakeObligationConfig{
		Store: store1, Submitter: &fakeSubmitter{}, WakeKey: testWakeKey(), Address: addr, Seq: inner, Now: time.Now,
	})
	if err := m1.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	// CRASH HERE. The mailbox append never ran -- nothing in this test calls it, and
	// m1/store1 are abandoned below rather than reused.
	if ob, ok, _ := store1.Get(addr); !ok || ob.State != ObligationPending {
		t.Fatal("the obligation itself must have survived the crash")
	}

	// RESTART: a genuinely reopened file store at the same path, plus a fresh machine.
	store2, err := OpenObligationStore(path)
	if err != nil {
		t.Fatalf("reopen OpenObligationStore: %v", err)
	}
	sub := &fakeSubmitter{}
	m2 := NewWakeObligationMachine(WakeObligationConfig{
		Store: store2, Submitter: sub, WakeKey: testWakeKey(), Address: addr, Seq: inner, Now: time.Now,
	})
	if err := m2.Drive(context.Background()); err != nil {
		t.Fatalf("Drive after restart: %v", err)
	}
	if got := len(sub.all()); got != 1 {
		t.Fatalf("wake submissions after restart = %d, want 1: at-least-once delivery must not be lost "+
			"because the mailbox publish it announces never happened", got)
	}
}

// TestCrashMatrix_Obligation_BeforeGatewaySubmit_RestartSubmitsTheStoredBytesVerbatim is
// boundary 3: obligation persisted, mailbox published, then the process died before
// Drive was ever called. Restart must submit the EXACT bytes Trigger sealed, never a
// re-sealed copy (PG-WAKE-12).
//
// Over the REAL file-backed ObligationStore with a genuine reopen, as boundary 2 above.
func TestCrashMatrix_Obligation_BeforeGatewaySubmit_RestartSubmitsTheStoredBytesVerbatim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wake-obligations")
	addr := testPushAddress(0x93)
	inner, err := OpenSeqSource("")
	if err != nil {
		t.Fatal(err)
	}
	store1, err := OpenObligationStore(path)
	if err != nil {
		t.Fatalf("OpenObligationStore: %v", err)
	}
	m1 := NewWakeObligationMachine(WakeObligationConfig{
		Store: store1, Submitter: &fakeSubmitter{}, WakeKey: testWakeKey(), Address: addr, Seq: inner, Now: time.Now,
	})
	if err := m1.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	mailbox := &recordingSink{}
	if err := mailbox.Event(protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "status", Group: status.GroupNeedsInput}); err != nil {
		t.Fatalf("mailbox publish: %v", err)
	}
	// CRASH HERE, before Drive ever ran. store1/m1 are abandoned below.
	want, _, _ := store1.Get(addr)

	store2, err := OpenObligationStore(path)
	if err != nil {
		t.Fatalf("reopen OpenObligationStore: %v", err)
	}
	sub := &fakeSubmitter{}
	m2 := NewWakeObligationMachine(WakeObligationConfig{
		Store: store2, Submitter: sub, WakeKey: testWakeKey(), Address: addr, Seq: inner, Now: time.Now,
	})
	if err := m2.Drive(context.Background()); err != nil {
		t.Fatalf("Drive after restart: %v", err)
	}
	sent := sub.all()
	if len(sent) != 1 {
		t.Fatalf("submissions after restart = %d, want 1", len(sent))
	}
	if !bytes.Equal(sent[0], want.Envelope) {
		t.Fatal("the post-restart submission differs from the durably sealed bytes (PG-WAKE-12)")
	}
}

// TestCrashMatrix_Obligation_AfterGatewayCommitBeforeLocalAck_RetriesHarmlessly is
// boundary 4: FCM/the gateway already accepted the request, but the LOCAL write that
// would record "delivered" never lands before the process dies. Restart must retry --
// FCM sees the byte-identical wake a second time, which is exactly the harmless
// duplicate P9 accepts ("the authenticator and high-water reject it harmlessly").
func TestCrashMatrix_Obligation_AfterGatewayCommitBeforeLocalAck_RetriesHarmlessly(t *testing.T) {
	store := newFakeObligationStore()
	store.failOn[ObligationDelivered] = errObligationCrash // the local ack write never lands
	addr := testPushAddress(0x94)
	inner, err := OpenSeqSource("")
	if err != nil {
		t.Fatal(err)
	}
	sub := &fakeSubmitter{} // always reports FCM acceptance
	m1 := NewWakeObligationMachine(WakeObligationConfig{
		Store: store, Submitter: sub, WakeKey: testWakeKey(), Address: addr, Seq: inner, Now: time.Now,
	})
	if err := m1.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if err := m1.Drive(context.Background()); err == nil {
		t.Fatal("Drive succeeded despite the injected crash on the delivered write -- the local ack must be durable, and its failure must surface")
	}
	if got := len(sub.all()); got != 1 {
		t.Fatalf("submissions before the crash = %d, want 1", got)
	}
	ob, _, _ := store.Get(addr)
	if ob.State != ObligationInFlight {
		t.Fatalf("state after the crash = %q, want %q: the delivered write never landed", ob.State, ObligationInFlight)
	}

	// RESTART: the crash is gone (a real restart does not carry a poisoned store).
	store.failOn = map[ObligationState]error{}
	pending, err := store.Pending()
	if err != nil || len(pending) != 1 {
		t.Fatalf("Pending() after restart = %v (err=%v), want exactly the in_flight obligation", pending, err)
	}
	m2 := NewWakeObligationMachine(WakeObligationConfig{
		Store: store, Submitter: sub, WakeKey: testWakeKey(), Address: addr, Seq: inner, Now: time.Now,
	})
	if err := m2.Drive(context.Background()); err != nil {
		t.Fatalf("Drive after restart: %v", err)
	}
	sent := sub.all()
	if len(sent) != 2 {
		t.Fatalf("total submissions = %d, want 2: at-least-once means FCM sees the wake again", len(sent))
	}
	if !bytes.Equal(sent[0], sent[1]) {
		t.Fatal("the retried submission was not byte-identical to the first (PG-WAKE-12)")
	}
	ob2, _, _ := store.Get(addr)
	if ob2.State != ObligationDelivered {
		t.Fatalf("final state = %q, want %q", ob2.State, ObligationDelivered)
	}
}

// TestCrashMatrix_Obligation_OnRestart_RedrivesAnInFlightObligationByteIdentically is
// boundary 5: a machine starting up finds an obligation LEFT in_flight by a previous
// crash (seeded directly, modelling exactly what boundary 4 leaves behind before its own
// recovery runs) and must redrive it through Pending(), submitting the SAME bytes.
func TestCrashMatrix_Obligation_OnRestart_RedrivesAnInFlightObligationByteIdentically(t *testing.T) {
	store := newFakeObligationStore()
	addr := testPushAddress(0x95)
	issuedAt := time.Unix(1_700_000_000, 0)
	env, err := SealWakeV1(testWakeKey(), addr, 3, issuedAt)
	if err != nil {
		t.Fatalf("SealWakeV1: %v", err)
	}
	seeded := WakeObligation{
		Address: addr, WakeSeq: 3, Envelope: env,
		IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(WakeV1Expiry), State: ObligationInFlight,
	}
	if err := store.Put(seeded); err != nil {
		t.Fatalf("seeding the pre-crash state: %v", err)
	}

	pending, err := store.Pending()
	if err != nil || len(pending) != 1 {
		t.Fatalf("a restarted machine must discover the in_flight obligation via Pending(): got %v (err=%v)", pending, err)
	}

	inner, err := OpenSeqSource("")
	if err != nil {
		t.Fatal(err)
	}
	sub := &fakeSubmitter{}
	m := NewWakeObligationMachine(WakeObligationConfig{
		Store: store, Submitter: sub, WakeKey: testWakeKey(), Address: addr, Seq: inner,
		Now: func() time.Time { return issuedAt.Add(time.Minute) }, // still inside the 5-minute expiry
	})
	if err := m.Drive(context.Background()); err != nil {
		t.Fatalf("Drive: %v", err)
	}
	sent := sub.all()
	if len(sent) != 1 {
		t.Fatalf("submissions = %d, want 1", len(sent))
	}
	if !bytes.Equal(sent[0], env) {
		t.Fatal("the re-driven submission was not byte-identical to the seeded envelope")
	}
	ob, _, _ := store.Get(addr)
	if ob.State != ObligationDelivered {
		t.Fatalf("final state = %q, want %q", ob.State, ObligationDelivered)
	}
}

// --- ObligationStore: the REAL file-backed store, not the fake -----------------------

// TestObligationStore_PersistsAcrossReopen is the durability half at the file layer
// itself, in the same shape as TestRelaySink_OutboxCommitsDeliveredCursorsAcrossRestart
// (outbox_restart_test.go): write, reopen the SAME path, expect the write to survive.
func TestObligationStore_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wake-obligations")
	addr := testPushAddress(0xA0)

	st1, err := OpenObligationStore(path)
	if err != nil {
		t.Fatalf("OpenObligationStore: %v", err)
	}
	issuedAt := time.Unix(1_700_000_100, 0)
	env, err := SealWakeV1(testWakeKey(), addr, 1, issuedAt)
	if err != nil {
		t.Fatalf("SealWakeV1: %v", err)
	}
	want := WakeObligation{
		Address: addr, WakeSeq: 1, Envelope: env,
		IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(WakeV1Expiry), State: ObligationInFlight, Attempts: 1,
	}
	if err := st1.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	st2, err := OpenObligationStore(path)
	if err != nil {
		t.Fatalf("reopen OpenObligationStore: %v", err)
	}
	got, ok, err := st2.Get(addr)
	if err != nil || !ok {
		t.Fatalf("Get after reopen: ok=%v err=%v, want the persisted obligation", ok, err)
	}
	if got.WakeSeq != want.WakeSeq || got.State != want.State || !bytes.Equal(got.Envelope, want.Envelope) {
		t.Fatalf("reopened obligation = %+v, want %+v", got, want)
	}
	pending, err := st2.Pending()
	if err != nil || len(pending) != 1 {
		t.Fatalf("Pending after reopen = %v (err=%v), want exactly the in_flight obligation", pending, err)
	}
}

// --- content-key fence, extended to the gateway path --------------------------------

// TestWakeObligationConfig_CarriesNoContentKey extends
// TestPBPUSH0_PushConfigCarriesNoContentKey's fence (push_trigger_test.go) to this
// wave's new push-path configuration surface: PB-PUSH-0/ADR-007 B19 requires the push
// path hold the wake key ONLY, and that requirement did not stop meaning anything just
// because a second configuration struct now also sits on the push path.
func TestWakeObligationConfig_CarriesNoContentKey(t *testing.T) {
	if !hasFieldOfType(WakeObligationConfig{}, func(v any) bool { _, ok := v.(crypto.WakeKey); return ok }) {
		t.Fatal("control: WakeObligationConfig carries no crypto.WakeKey, so the assertions below prove nothing")
	}
	assertNoFieldOfType(t, WakeObligationConfig{}, "crypto.ContentKey", func(v any) bool {
		_, ok := v.(crypto.ContentKey)
		return ok
	})
	assertNoFieldOfType(t, WakeObligationConfig{}, "crypto.EpochKeys", func(v any) bool {
		_, ok := v.(crypto.EpochKeys)
		return ok
	})
}
