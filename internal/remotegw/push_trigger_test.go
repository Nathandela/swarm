package remotegw

// Failing-first tests for PB-PUSH-0 (the gateway-side push trigger), PB-PUSH-3 (the
// wake payload schema), PB-PUSH-5 (graceful degradation on the gateway side), and the
// SENDER half of PB-PUSH-8 / PB-PUSH-10 (a disabled preference sends no push at all,
// and still sends none after a restart).
//
// RED is undefined-only: nothing in this file has an implementation yet, so the package
// does not compile and every test below fails with "undefined: ...". That is deliberate
// -- a do-nothing stub is how the previous RED author in this project ended up with four
// tests passing vacuously.
//
// SCOPE HONESTY (hard constraint): nothing here models real FCM delivery, real Doze
// behaviour, or a real handset. These tests exercise the MACHINE-SIDE decision to send:
// which journal transition fires a wake, how often, under which key, and whether the
// user's preference suppresses it AT THE SENDER. PB-E2E-5 (physical handset) is
// DEFERRED and is not touched, weakened, or partially claimed by anything below.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/status"
)

// --- fakes -----------------------------------------------------------------

// pushCall is one trigger the notifier handed to the relay seam.
type pushCall struct {
	target string
	env    []byte
}

// fakePusher is the relay push seam. It is the SENDER: PB-PUSH-8 requires a disabled
// preference to produce ZERO calls here, because a call is exactly what makes the push
// provider observe token, timing and size.
type fakePusher struct {
	mu    sync.Mutex
	calls []pushCall
	err   error
}

func (f *fakePusher) PushTrigger(_ context.Context, target string, env []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, pushCall{target: target, env: append([]byte(nil), env...)})
	return nil
}

func (f *fakePusher) all() []pushCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pushCall(nil), f.calls...)
}

func (f *fakePusher) count() int { return len(f.all()) }

// recordingSink is the OutboundSink the notifier wraps: it records what still reached
// the sealing/appending layer, so a test can prove the push path never displaced the
// journal path.
type recordingSink struct {
	mu       sync.Mutex
	events   []protocol.JournalRecord
	rosters  int
	terminal int
	machine  string
	cursor   uint64
	err      error
}

func (r *recordingSink) Snapshot(roster []protocol.JournalRecord, _ uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rosters++
	if r.err != nil {
		return r.err
	}
	return nil
}

func (r *recordingSink) Event(rec protocol.JournalRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, rec)
	return nil
}

func (r *recordingSink) Terminal(_ string, _ []string, _, _ int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.terminal++
	return r.err
}

func (r *recordingSink) SetMachine(m string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.machine = m
}

func (r *recordingSink) DeliveredCursor() uint64 { return r.cursor }

func (r *recordingSink) eventCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func (r *recordingSink) terminalCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminal
}

func (r *recordingSink) rosterCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rosters
}

// stubPrefs is an in-memory PushPrefsSource whose Load can be made to fail, so the
// fail-closed direction is drivable (an unreadable preference must SUPPRESS, never
// silently fall back to enabled).
type stubPrefs struct {
	mu    sync.Mutex
	prefs PushPrefs
	err   error
}

func (s *stubPrefs) LoadPrefs() (PushPrefs, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return PushPrefs{}, s.err
	}
	return s.prefs, nil
}

func (s *stubPrefs) SavePrefs(p PushPrefs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prefs = p
	return nil
}

// --- harness ---------------------------------------------------------------

func testWakeKey() crypto.WakeKey {
	var k crypto.WakeKey
	for i := range k {
		k[i] = byte(0xA0 + i)
	}
	return k
}

func testContentKey() crypto.ContentKey {
	var k crypto.ContentKey
	for i := range k {
		k[i] = byte(0xC0 + i)
	}
	return k
}

// testClock is a manually advanced clock: the 30 s coalescing window must be tested by
// moving time, never by sleeping.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type pushHarness struct {
	notifier *PushNotifier
	pusher   *fakePusher
	inner    *recordingSink
	prefs    *stubPrefs
	clk      *testClock
}

// newPushHarness wires a notifier over a recording sink with both push categories
// ENABLED and the default 30 s window, which is the configuration every trigger-
// selection test starts from.
func newPushHarness(t *testing.T) *pushHarness {
	t.Helper()
	return newPushHarnessWith(t, PushPrefs{Version: 1, NeedsInput: true, Finished: true})
}

func newPushHarnessWith(t *testing.T, prefs PushPrefs) *pushHarness {
	t.Helper()
	pusher := &fakePusher{}
	inner := &recordingSink{}
	sp := &stubPrefs{prefs: prefs}
	clk := newTestClock()
	seq, err := OpenSeqSource("")
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	n := NewPushNotifier(inner, PushConfig{
		Pusher:  pusher,
		Target:  "phone-routing-id",
		WakeKey: testWakeKey(),
		EpochID: 7,
		Now:     clk.Now,
		Seq:     seq,
		Prefs:   sp,
	})
	return &pushHarness{notifier: n, pusher: pusher, inner: inner, prefs: sp, clk: clk}
}

// event delivers one live journal record for session s in group g.
func (h *pushHarness) event(t *testing.T, cursor uint64, s string, g status.Group) {
	t.Helper()
	if err := h.notifier.Event(protocol.JournalRecord{
		Cursor: cursor, SessionID: s, Type: "status", Group: g,
	}); err != nil {
		t.Fatalf("Event(%s, %s): %v", s, g, err)
	}
}

// --- PB-PUSH-0: trigger selection -------------------------------------------

// TestPBPUSH0_TransitionIntoNeedsInputFiresExactlyOnePush pins the primary trigger:
// the agent has stopped and is blocked on its owner. ADR-007 B16 makes this push the
// SOLE way a backgrounded phone learns about it, so a missed one is a missed hand-off
// with no fallback.
func TestPBPUSH0_TransitionIntoNeedsInputFiresExactlyOnePush(t *testing.T) {
	h := newPushHarness(t)
	h.event(t, 1, "m/s1", status.GroupWorking)
	h.event(t, 2, "m/s1", status.GroupNeedsInput)

	if got := h.pusher.count(); got != 1 {
		t.Fatalf("push count after working -> needs_input: got %d, want 1", got)
	}
	if got := h.pusher.all()[0].target; got != "phone-routing-id" {
		t.Fatalf("push target: got %q, want the phone routing id", got)
	}
	// The journal record itself must still have been delivered: the push is an
	// ADDITIONAL wake, never a substitute for the frame the phone reads on reconnect.
	if got := h.inner.eventCount(); got != 2 {
		t.Fatalf("journal events delivered to the inner sink: got %d, want 2", got)
	}
}

// TestPBPUSH0_TransitionIntoFinishedFiresAPush pins the second trigger class: the agent
// finished its turn (ready_for_review) or the session ended (completed). Both are
// hand-offs the owner is waiting on.
func TestPBPUSH0_TransitionIntoFinishedFiresAPush(t *testing.T) {
	for _, g := range []status.Group{status.GroupReadyForReview, status.GroupCompleted} {
		t.Run(string(g), func(t *testing.T) {
			h := newPushHarness(t)
			h.event(t, 1, "m/s1", status.GroupWorking)
			h.event(t, 2, "m/s1", g)
			if got := h.pusher.count(); got != 1 {
				t.Fatalf("push count after working -> %s: got %d, want 1", g, got)
			}
		})
	}
}

// TestPBPUSH0_TransitionIntoWorkingNeverPushes is the negative half of selection. A
// session going back to work is the one transition the owner does NOT need to be woken
// for, and it is by far the most frequent one -- pushing on it would burn the
// unpublished per-app FCM quota ADR-007 B16 flagged as the cost of dropping the socket.
func TestPBPUSH0_TransitionIntoWorkingNeverPushes(t *testing.T) {
	// POSITIVE CONTROL FIRST. A pure negative assertion is satisfied by a notifier that
	// never pushes at all, which is the do-nothing shape this project has shipped four
	// vacuous tests against before. Establish that this harness DOES push, then show that
	// working does not.
	h := newPushHarness(t)
	h.event(t, 1, "m/s1", status.GroupWorking)
	h.event(t, 2, "m/s1", status.GroupNeedsInput)
	if got := h.pusher.count(); got != 1 {
		t.Fatalf("control: push count after working -> needs_input = %d, want 1; the negative assertion below would be meaningless", got)
	}

	h.clk.advance(10 * DefaultPushWindow)
	h.event(t, 3, "m/s1", status.GroupWorking)
	h.event(t, 4, "m/s2", status.GroupWorking)
	if got := h.pusher.count(); got != 1 {
		t.Fatalf("push count after two transitions into working: got %d, want the control's 1", got)
	}
}

// TestPBPUSH0_RepeatedSameGroupIsNotATransition guards the difference between "is in a
// push-worthy group" and "just entered one". A journal that re-reports needs_input for a
// session already in needs_input must not re-wake the phone -- and this must hold even
// once the coalescing window has fully elapsed, or the coalescer would be hiding a
// selection bug rather than the selection being correct.
func TestPBPUSH0_RepeatedSameGroupIsNotATransition(t *testing.T) {
	h := newPushHarness(t)
	h.event(t, 1, "m/s1", status.GroupWorking)
	h.event(t, 2, "m/s1", status.GroupNeedsInput)
	if got := h.pusher.count(); got != 1 {
		t.Fatalf("control: the first transition into needs_input produced %d pushes, want 1", got)
	}

	h.clk.advance(10 * DefaultPushWindow)
	h.event(t, 3, "m/s1", status.GroupNeedsInput)
	h.clk.advance(10 * DefaultPushWindow)
	h.event(t, 4, "m/s1", status.GroupNeedsInput)

	if got := h.pusher.count(); got != 1 {
		t.Fatalf("push count after two same-group repeats well outside the window: got %d, want 1", got)
	}
}

// TestPBPUSH0_ReconnectRosterSeedsWithoutPushing is the restart-storm guard. Snapshot is
// the per-(re)connection roster of CURRENT state (gateway.go / relaysink.go), so every
// session in it looks like a fresh transition to a notifier that treats roster records as
// live. A gateway restart with n idle sessions would then fire n pushes at once for
// events that happened hours ago -- and, under a restart loop, repeatedly.
func TestPBPUSH0_ReconnectRosterSeedsWithoutPushing(t *testing.T) {
	h := newPushHarness(t)
	roster := []protocol.JournalRecord{
		{Cursor: 10, SessionID: "m/s1", Type: "roster", Group: status.GroupNeedsInput},
		{Cursor: 11, SessionID: "m/s2", Type: "roster", Group: status.GroupReadyForReview},
		{Cursor: 12, SessionID: "m/s3", Type: "roster", Group: status.GroupCompleted},
	}
	if err := h.notifier.Snapshot(roster, 12); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := h.pusher.count(); got != 0 {
		t.Fatalf("push count for a reconnect roster: got %d, want 0", got)
	}

	// Seeded, not ignored: a session already reported as needs_input in the roster must
	// not fire when the next live event repeats that group.
	h.clk.advance(10 * DefaultPushWindow)
	h.event(t, 13, "m/s1", status.GroupNeedsInput)
	if got := h.pusher.count(); got != 0 {
		t.Fatalf("push count after a live repeat of a roster-seeded group: got %d, want 0", got)
	}

	// But a genuine transition after the roster still fires.
	h.event(t, 14, "m/s2", status.GroupNeedsInput)
	if got := h.pusher.count(); got != 1 {
		t.Fatalf("push count after a genuine post-roster transition: got %d, want 1", got)
	}
}

// TestPBPUSH0_RecordWithNoGroupIsNotATransition pins that a group-less record (a
// session-neutral record, or one whose type carries no status) is IGNORED rather than
// read as a transition into the empty group -- which would otherwise let the next real
// needs_input record look like a change from "" and, worse, let a stream of group-less
// records reset every session's remembered state.
func TestPBPUSH0_RecordWithNoGroupIsNotATransition(t *testing.T) {
	h := newPushHarness(t)
	h.event(t, 1, "m/s1", status.GroupWorking)
	h.event(t, 2, "m/s1", status.GroupNeedsInput)
	if got := h.pusher.count(); got != 1 {
		t.Fatalf("control: the first transition into needs_input produced %d pushes, want 1", got)
	}

	h.clk.advance(10 * DefaultPushWindow)
	h.event(t, 3, "m/s1", "") // no group: must neither push nor clear the remembered group
	if got := h.pusher.count(); got != 1 {
		t.Fatalf("push count after a group-less record: got %d, want 1", got)
	}

	h.clk.advance(10 * DefaultPushWindow)
	h.event(t, 4, "m/s1", status.GroupNeedsInput)
	if got := h.pusher.count(); got != 1 {
		t.Fatalf("push count after needs_input following a group-less record: got %d, want 1 (the group-less record must not have cleared the remembered state)", got)
	}
}

// --- PB-PUSH-0: coalescing (§6.0: 30 s per session) -------------------------

// TestPBPUSH0_CoalescesRepeatTransitionsWithinTheWindow pins §6.0's 30 s window: a
// session that flaps between push-worthy groups inside one window wakes the phone once.
func TestPBPUSH0_CoalescesRepeatTransitionsWithinTheWindow(t *testing.T) {
	h := newPushHarness(t)
	h.event(t, 1, "m/s1", status.GroupNeedsInput) // fires
	h.clk.advance(DefaultPushWindow - time.Millisecond)
	h.event(t, 2, "m/s1", status.GroupWorking)        // not push-worthy
	h.event(t, 3, "m/s1", status.GroupReadyForReview) // push-worthy, but inside the window
	h.event(t, 4, "m/s1", status.GroupWorking)        //
	h.event(t, 5, "m/s1", status.GroupNeedsInput)     // push-worthy, still inside the window

	if got := h.pusher.count(); got != 1 {
		t.Fatalf("push count inside one %s window: got %d, want 1", DefaultPushWindow, got)
	}

	h.clk.advance(2 * time.Millisecond) // now past the window
	h.event(t, 6, "m/s1", status.GroupWorking)
	h.event(t, 7, "m/s1", status.GroupNeedsInput)
	if got := h.pusher.count(); got != 2 {
		t.Fatalf("push count after the window elapsed: got %d, want 2", got)
	}
}

// TestPBPUSH0_CoalescingWindowIsPerSessionNotGlobal is the mutation that a single shared
// timestamp would pass everything else with. §6.0 says "30 s PER SESSION": two different
// agents both stopping for input within the same window are two separate hand-offs, and
// a global window silently drops the second one -- the failure that is invisible until
// the owner is waiting on a session the phone never mentioned.
func TestPBPUSH0_CoalescingWindowIsPerSessionNotGlobal(t *testing.T) {
	h := newPushHarness(t)
	h.event(t, 1, "m/s1", status.GroupNeedsInput)
	h.event(t, 2, "m/s2", status.GroupNeedsInput)
	h.event(t, 3, "m/s3", status.GroupReadyForReview)

	if got := h.pusher.count(); got != 3 {
		t.Fatalf("push count for three distinct sessions inside one window: got %d, want 3 (the window is per session)", got)
	}
}

// --- PB-PUSH-0: key separation ----------------------------------------------

// TestPBPUSH0_WakeIsSealedUnderTheWakeKeyAndOpaqueToTheContentKey is PB-PUSH-0's
// "the content key is never used" criterion, driven both ways: the wake OPENS under the
// wake key and is REFUSED under the content key. crypto's typed keys (A15/F10) make the
// wrong-key direction a hard ErrWrongKeyType rather than a silent AEAD failure.
func TestPBPUSH0_WakeIsSealedUnderTheWakeKeyAndOpaqueToTheContentKey(t *testing.T) {
	h := newPushHarness(t)
	h.event(t, 1, "m/s1", status.GroupNeedsInput)

	calls := h.pusher.all()
	if len(calls) != 1 {
		t.Fatalf("push count: got %d, want 1", len(calls))
	}
	env, err := crypto.ParseEnvelope(calls[0].env)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.Header.Type != crypto.TypePushWake {
		t.Fatalf("wake envelope type = %#x, want TypePushWake (%#x)", env.Header.Type, crypto.TypePushWake)
	}
	if _, err := crypto.OpenWake(testWakeKey(), env); err != nil {
		t.Fatalf("OpenWake with the wake key: %v", err)
	}
	if _, err := crypto.OpenMailbox(testContentKey(), env); !errors.Is(err, crypto.ErrWrongKeyType) {
		t.Fatalf("OpenMailbox(content key) on a wake = %v, want ErrWrongKeyType", err)
	}
}

// TestPBPUSH0_PushConfigCarriesNoContentKey is the checkable form of PB-PUSH-0's "the
// gateway holds the wake key only".
//
// Read literally that criterion is UNIMPLEMENTABLE: the gateway MUST hold the content
// key -- RelaySink seals every journal frame with it and CommandBridge opens every phone
// command with it. What IS checkable, and what the criterion is actually protecting, is
// that the PUSH PATH holds the wake key only, so no content key is even in scope at the
// point a push is built. Go's typed keys turn that into a compile-time property; this
// test pins the configuration surface so a later "just pass the whole EpochKeys through"
// refactor is a test failure and not a quiet widening.
//
// NOT A RED TEST beyond the compile step: it is a FENCE and passes as soon as PushConfig
// exists in the right shape. Its value is prospective.
func TestPBPUSH0_PushConfigCarriesNoContentKey(t *testing.T) {
	// Positive control: an EMPTY struct would satisfy the two "must not carry" assertions
	// below for free, so first prove the configuration is the real one by requiring the
	// key the push path DOES need.
	if !hasFieldOfType(PushConfig{}, func(v any) bool { _, ok := v.(crypto.WakeKey); return ok }) {
		t.Fatal("control: PushConfig carries no crypto.WakeKey, so the assertions below prove nothing")
	}
	assertNoFieldOfType(t, PushConfig{}, "crypto.ContentKey", func(v any) bool {
		_, ok := v.(crypto.ContentKey)
		return ok
	})
	assertNoFieldOfType(t, PushConfig{}, "crypto.EpochKeys", func(v any) bool {
		_, ok := v.(crypto.EpochKeys)
		return ok
	})
}

// --- PB-PUSH-3: the payload schema ------------------------------------------

// TestPBPUSH3_WakeEnvelopeIsExactlyTheFixedContentFreeSize pins the schema by SIZE,
// which is the one property the push provider is conceded to observe (token, timing,
// size). A fixed size is therefore not cosmetic: a variable-length wake would make the
// size itself a covert channel -- longer for a longer session name, or for more
// transitions coalesced -- and PB-PUSH-3's disclosure statement would become false
// without a single test failing.
func TestPBPUSH3_WakeEnvelopeIsExactlyTheFixedContentFreeSize(t *testing.T) {
	h := newPushHarness(t)
	// Deliberately loud identifiers: a leak has something recognisable to leak.
	h.event(t, 1, "build-box-17.local/refactor-the-auth-middleware", status.GroupNeedsInput)
	h.clk.advance(2 * DefaultPushWindow)
	h.event(t, 2, "m/x", status.GroupWorking)
	h.event(t, 3, "m/x", status.GroupNeedsInput)

	calls := h.pusher.all()
	if len(calls) != 2 {
		t.Fatalf("push count: got %d, want 2", len(calls))
	}
	for i, c := range calls {
		if len(c.env) != PushWakeEnvelopeSize {
			t.Fatalf("push %d envelope size = %d, want the fixed PushWakeEnvelopeSize %d", i, len(c.env), PushWakeEnvelopeSize)
		}
	}
	if len(calls[0].env) != len(calls[1].env) {
		t.Fatalf("wake size varies with the session it describes (%d vs %d): size is disclosed to the provider", len(calls[0].env), len(calls[1].env))
	}
}

// TestPBPUSH3_WakePlaintextIsEmptyAndNamesNothing pins the plaintext half: opened under
// the wake key the payload is EMPTY, and no session id, hostname, agent name or Group
// label appears anywhere in the bytes that leave the machine.
func TestPBPUSH3_WakePlaintextIsEmptyAndNamesNothing(t *testing.T) {
	const session = "build-box-17.local/refactor-the-auth-middleware"
	h := newPushHarness(t)
	h.event(t, 1, session, status.GroupNeedsInput)

	calls := h.pusher.all()
	if len(calls) != 1 {
		t.Fatalf("push count: got %d, want 1", len(calls))
	}
	env, err := crypto.ParseEnvelope(calls[0].env)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	plain, err := crypto.OpenWake(testWakeKey(), env)
	if err != nil {
		t.Fatalf("OpenWake: %v", err)
	}
	if len(plain) != 0 {
		t.Fatalf("wake plaintext = %q (%d bytes), want empty: a content-free wake carries no fields at all", plain, len(plain))
	}
	for _, secret := range []string{
		session, "build-box-17.local", "refactor-the-auth-middleware",
		string(status.GroupNeedsInput), "needs_input", "phone-routing-id",
	} {
		if strings.Contains(string(calls[0].env), secret) {
			t.Fatalf("wake envelope contains %q -- the provider must learn only token, timing and size", secret)
		}
	}
}

// TestPBPUSH3_WakeHeaderCarriesNoStableEndpointIdentifiers covers the part of the
// envelope that is NOT encrypted. crypto.Envelope.Marshal writes a 62-byte CLEARTEXT
// header, so recipient_key_id and sender_key_id would reach the push provider in the
// clear if the mailbox header were reused verbatim -- stable pseudonymous identifiers
// that let the provider link every wake to one machine/device pair for the life of the
// epoch, which is strictly more than "token, timing, size".
func TestPBPUSH3_WakeHeaderCarriesNoStableEndpointIdentifiers(t *testing.T) {
	h := newPushHarness(t)
	h.event(t, 1, "m/s1", status.GroupNeedsInput)

	calls := h.pusher.all()
	if len(calls) != 1 {
		t.Fatalf("push count: got %d, want 1", len(calls))
	}
	env, err := crypto.ParseEnvelope(calls[0].env)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.Header.RecipientKeyID != ([8]byte{}) {
		t.Fatalf("wake header recipient_key_id = %x, want zero (it is cleartext to the push provider and the wake needs no routing id)", env.Header.RecipientKeyID)
	}
	if env.Header.SenderKeyID != ([8]byte{}) {
		t.Fatalf("wake header sender_key_id = %x, want zero (cleartext, and a stable machine identifier across the epoch)", env.Header.SenderKeyID)
	}
}

// TestPBPUSH3_WakeCarriesAMonotonicReplayCoordinate pins the replay/expiry gating half
// of PB-PUSH-3. §6.0 gives the wake a 10-minute TTL "with the replay coordinate
// persisted per PB-STATE-1", so each wake must carry a strictly increasing seq and a
// real issued_at. A zero issued_at is the specific failure SealControlReply already hit
// once: the field is AAD-covered, so leaving it unset AUTHENTICATES a zero and every
// receiver computes an age of decades.
func TestPBPUSH3_WakeCarriesAMonotonicReplayCoordinate(t *testing.T) {
	h := newPushHarness(t)
	h.event(t, 1, "m/s1", status.GroupNeedsInput)
	h.clk.advance(2 * DefaultPushWindow)
	h.event(t, 2, "m/s1", status.GroupWorking)
	h.event(t, 3, "m/s1", status.GroupNeedsInput)

	calls := h.pusher.all()
	if len(calls) != 2 {
		t.Fatalf("push count: got %d, want 2", len(calls))
	}
	var seqs []uint64
	for i, c := range calls {
		env, err := crypto.ParseEnvelope(c.env)
		if err != nil {
			t.Fatalf("ParseEnvelope(%d): %v", i, err)
		}
		if env.Header.IssuedAt == 0 {
			t.Fatalf("wake %d issued_at is zero: the 10-minute TTL is uncheckable and the value is AAD-authenticated as a zero", i)
		}
		if got, want := env.Header.IssuedAt, h.clk.Now().UnixMilli(); i == 1 && got != want {
			t.Fatalf("wake %d issued_at = %d, want the configured clock %d", i, got, want)
		}
		if env.Header.EpochID != 7 {
			t.Fatalf("wake %d epoch = %d, want 7", i, env.Header.EpochID)
		}
		seqs = append(seqs, env.Header.Seq)
	}
	if seqs[0] == 0 {
		t.Fatalf("first wake seq is 0: a receiver cannot distinguish it from an unset field")
	}
	if seqs[1] <= seqs[0] {
		t.Fatalf("wake seqs are not strictly increasing: %d then %d -- a replayed wake is indistinguishable from a fresh one", seqs[0], seqs[1])
	}
}

// TestPBPUSH3_WakeSeqDoesNotRestartAfterAGatewayRestart pins that the wake seq comes
// from a DURABLE source. A per-process counter restarts at 1 on every gateway restart,
// so the phone's persisted replay coordinate (PB-STATE-1) would reject every wake after
// a restart -- push silently dies exactly the way PB-PUSH-10 warns about, and only on a
// real device.
func TestPBPUSH3_WakeSeqDoesNotRestartAfterAGatewayRestart(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/wake.seq"

	firstSeq := func() uint64 {
		seq, err := OpenSeqSource(path)
		if err != nil {
			t.Fatalf("OpenSeqSource: %v", err)
		}
		pusher := &fakePusher{}
		clk := newTestClock()
		n := NewPushNotifier(&recordingSink{}, PushConfig{
			Pusher:  pusher,
			Target:  "phone-routing-id",
			WakeKey: testWakeKey(),
			EpochID: 7,
			Now:     clk.Now,
			Seq:     seq,
			Prefs:   &stubPrefs{prefs: PushPrefs{Version: 1, NeedsInput: true, Finished: true}},
		})
		if err := n.Event(protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "status", Group: status.GroupNeedsInput}); err != nil {
			t.Fatalf("Event: %v", err)
		}
		calls := pusher.all()
		if len(calls) != 1 {
			t.Fatalf("push count: got %d, want 1", len(calls))
		}
		env, err := crypto.ParseEnvelope(calls[0].env)
		if err != nil {
			t.Fatalf("ParseEnvelope: %v", err)
		}
		return env.Header.Seq
	}

	before := firstSeq()
	after := firstSeq() // a whole new notifier + a whole new SeqSource over the same file
	if after <= before {
		t.Fatalf("wake seq after a restart = %d, want strictly above the pre-restart %d", after, before)
	}
}

// --- PB-PUSH-5: the gateway degrades gracefully and loudly ------------------

// TestPBPUSH5_PushFailureNeverFailsTheJournalRecord pins that the push path is strictly
// additive. Gateway.deliver gates its durable cursor on the sink's error, so returning a
// push failure from Event would stall the journal cursor on a relay push outage and turn
// a lost convenience into a stalled bridge. The error must still be LOUD via Err().
func TestPBPUSH5_PushFailureNeverFailsTheJournalRecord(t *testing.T) {
	h := newPushHarness(t)
	h.pusher.err = errors.New("relay refused push_trigger")

	if err := h.notifier.Event(protocol.JournalRecord{
		Cursor: 1, SessionID: "m/s1", Type: "status", Group: status.GroupNeedsInput,
	}); err != nil {
		t.Fatalf("Event returned %v; a failing push must not fail the journal record", err)
	}
	if got := h.inner.eventCount(); got != 1 {
		t.Fatalf("inner sink events: got %d, want 1 (the record must still be sealed and appended)", got)
	}
	if h.notifier.Err() == nil {
		t.Fatal("Err() is nil after a failed push: the degradation must be loud, not silent")
	}
}

// TestPBPUSH5_NoPusherConfiguredLeavesTheCorePathsUntouched pins "the system works
// without push": a gateway assembled with no push transport at all still bridges the
// journal, returns no error, and does not panic.
func TestPBPUSH5_NoPusherConfiguredLeavesTheCorePathsUntouched(t *testing.T) {
	inner := &recordingSink{}
	seq, err := OpenSeqSource("")
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	n := NewPushNotifier(inner, PushConfig{
		Target:  "phone-routing-id",
		WakeKey: testWakeKey(),
		EpochID: 7,
		Seq:     seq,
		Prefs:   &stubPrefs{prefs: PushPrefs{Version: 1, NeedsInput: true, Finished: true}},
	}) // Pusher deliberately nil

	if err := n.Snapshot([]protocol.JournalRecord{{Cursor: 1, SessionID: "m/s1", Group: status.GroupWorking}}, 1); err != nil {
		t.Fatalf("Snapshot with no pusher: %v", err)
	}
	if err := n.Event(protocol.JournalRecord{Cursor: 2, SessionID: "m/s1", Type: "status", Group: status.GroupNeedsInput}); err != nil {
		t.Fatalf("Event with no pusher: %v", err)
	}
	if err := n.Terminal("m/s1", []string{"x"}, 80, 24); err != nil {
		t.Fatalf("Terminal with no pusher: %v", err)
	}
	if got := inner.eventCount(); got != 1 {
		t.Fatalf("inner sink events: got %d, want 1", got)
	}
	if got := inner.terminalCount(); got != 1 {
		t.Fatalf("inner sink terminal snapshots: got %d, want 1", got)
	}
	if got := inner.rosterCount(); got != 1 {
		t.Fatalf("inner sink roster snapshots: got %d, want 1", got)
	}
	// POSITIVE CONTROL for the whole no-pusher configuration: the identical harness WITH
	// a pusher wakes the phone, so "no pushes" above is attributable to the missing
	// transport and not to a notifier that never pushes at all.
	ctl := newPushHarness(t)
	ctl.event(t, 1, "m/s1", status.GroupWorking)
	ctl.event(t, 2, "m/s1", status.GroupNeedsInput)
	if got := ctl.pusher.count(); got != 1 {
		t.Fatalf("control: the same transition WITH a pusher produced %d pushes, want 1", got)
	}
}

// TestPBPUSH5_JournalDeliveryFailureSuppressesTheWake pins the ORDER. A wake tells the
// phone "reconnect, there is something to read"; if the record that motivated it never
// reached the mailbox, the phone reconnects, finds nothing, and the hand-off is lost
// with the owner believing they were notified. The push must follow a SUCCESSFUL append.
func TestPBPUSH5_JournalDeliveryFailureSuppressesTheWake(t *testing.T) {
	rec := protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "status", Group: status.GroupNeedsInput}

	// POSITIVE CONTROL: the same record through a HEALTHY sink wakes the phone.
	ctl := newPushHarness(t)
	if err := ctl.notifier.Event(rec); err != nil {
		t.Fatalf("control: Event through a healthy sink: %v", err)
	}
	if got := ctl.pusher.count(); got != 1 {
		t.Fatalf("control: a healthy append produced %d pushes, want 1", got)
	}

	h := newPushHarness(t)
	h.inner.err = errors.New("relay refused mailbox_append")

	if err := h.notifier.Event(rec); err == nil {
		t.Fatal("Event returned nil although the inner sink failed: the gateway would advance its cursor past an undelivered record")
	}
	if got := h.pusher.count(); got != 0 {
		t.Fatalf("push count after a failed journal append: got %d, want 0", got)
	}
}

// --- PB-PUSH-8 (sender half): a disabled toggle sends NOTHING ---------------

// TestPBPUSH8_DisabledCategorySendsNoPushAtAll is the requirement's own wording: local
// filtering is NOT sufficient, because a push that is sent and then ignored still lets
// the provider observe token, timing and size. Zero calls at the sender is the only
// assertion that distinguishes the two.
func TestPBPUSH8_DisabledCategorySendsNoPushAtAll(t *testing.T) {
	// The sequence both halves are driven with.
	drive := func(h *pushHarness) {
		h.event(t, 1, "m/s1", status.GroupWorking)
		h.event(t, 2, "m/s1", status.GroupNeedsInput)
		h.clk.advance(2 * DefaultPushWindow)
		h.event(t, 3, "m/s1", status.GroupReadyForReview)
		h.clk.advance(2 * DefaultPushWindow)
		h.event(t, 4, "m/s2", status.GroupCompleted)
	}

	// POSITIVE CONTROL. "Zero pushes" is the single most important assertion in this
	// slice and also the easiest to satisfy accidentally -- a notifier that pushes for
	// nobody passes it perfectly. Show the identical sequence DOES wake the phone when
	// the preference permits it.
	enabled := newPushHarnessWith(t, PushPrefs{Version: 2, NeedsInput: true, Finished: true})
	drive(enabled)
	if got := enabled.pusher.count(); got != 3 {
		t.Fatalf("control: the same sequence with both categories ENABLED produced %d pushes, want 3; the suppression assertion below would be meaningless", got)
	}

	h := newPushHarnessWith(t, PushPrefs{Version: 2, NeedsInput: false, Finished: false})
	drive(h)
	if got := h.pusher.count(); got != 0 {
		t.Fatalf("push count with both categories disabled: got %d, want 0 -- the provider must observe no token, timing or size at all", got)
	}
	// Suppressing the wake must not suppress the journal.
	if got := h.inner.eventCount(); got != 4 {
		t.Fatalf("inner sink events: got %d, want 4", got)
	}
}

// TestPBPUSH8_CategoriesAreIndependent pins that the two coarse toggles PB-APP-7 exposes
// are actually two, not one switch wired twice.
func TestPBPUSH8_CategoriesAreIndependent(t *testing.T) {
	h := newPushHarnessWith(t, PushPrefs{Version: 3, NeedsInput: false, Finished: true})
	h.event(t, 1, "m/s1", status.GroupWorking)
	h.event(t, 2, "m/s1", status.GroupNeedsInput)
	if got := h.pusher.count(); got != 0 {
		t.Fatalf("needs_input disabled: got %d pushes, want 0", got)
	}
	h.clk.advance(2 * DefaultPushWindow)
	h.event(t, 3, "m/s1", status.GroupReadyForReview)
	if got := h.pusher.count(); got != 1 {
		t.Fatalf("finished enabled: got %d pushes, want 1", got)
	}

	h2 := newPushHarnessWith(t, PushPrefs{Version: 3, NeedsInput: true, Finished: false})
	h2.event(t, 1, "m/s1", status.GroupWorking)
	h2.event(t, 2, "m/s1", status.GroupReadyForReview)
	if got := h2.pusher.count(); got != 0 {
		t.Fatalf("finished disabled: got %d pushes, want 0", got)
	}
	h2.clk.advance(2 * DefaultPushWindow)
	h2.event(t, 3, "m/s1", status.GroupNeedsInput)
	if got := h2.pusher.count(); got != 1 {
		t.Fatalf("needs_input enabled: got %d pushes, want 1", got)
	}
}

// TestPBPUSH8_UnreadablePreferenceFailsClosed pins the direction of failure. The user is
// looking at a settings screen; if the machine cannot read what it says, sending anyway
// contradicts a setting that may well be "off" -- and leaks token/timing/size while
// doing it. Suppress, and say so via Err().
func TestPBPUSH8_UnreadablePreferenceFailsClosed(t *testing.T) {
	h := newPushHarness(t)
	h.prefs.err = errors.New("push-prefs.json is corrupt")

	if err := h.notifier.Event(protocol.JournalRecord{
		Cursor: 1, SessionID: "m/s1", Type: "status", Group: status.GroupNeedsInput,
	}); err != nil {
		t.Fatalf("Event: %v (an unreadable preference must not fail the journal path)", err)
	}
	if got := h.pusher.count(); got != 0 {
		t.Fatalf("push count with an unreadable preference: got %d, want 0 (fail closed)", got)
	}
	if h.notifier.Err() == nil {
		t.Fatal("Err() is nil after an unreadable preference: a silently push-less gateway is indistinguishable from a working one")
	}
}

// TestPBPUSH8_AbsentPreferenceSourceFailsClosed is the fail-closed-ABSENT case, the same
// shape as the protocol tier's refusal when its DeviceAuthenticator is missing: a
// misassembled gateway must not be the one configuration in which every push goes out
// unfiltered.
func TestPBPUSH8_AbsentPreferenceSourceFailsClosed(t *testing.T) {
	build := func(prefs PushPrefsSource) *fakePusher {
		t.Helper()
		pusher := &fakePusher{}
		seq, err := OpenSeqSource("")
		if err != nil {
			t.Fatalf("OpenSeqSource: %v", err)
		}
		n := NewPushNotifier(&recordingSink{}, PushConfig{
			Pusher:  pusher,
			Target:  "phone-routing-id",
			WakeKey: testWakeKey(),
			EpochID: 7,
			Seq:     seq,
			Prefs:   prefs,
		})
		if err := n.Event(protocol.JournalRecord{Cursor: 1, SessionID: "m/s1", Type: "status", Group: status.GroupNeedsInput}); err != nil {
			t.Fatalf("Event: %v", err)
		}
		return pusher
	}

	// POSITIVE CONTROL: the same assembly WITH a permissive source pushes.
	if got := build(&stubPrefs{prefs: PushPrefs{Version: 1, NeedsInput: true, Finished: true}}).count(); got != 1 {
		t.Fatalf("control: an identical notifier WITH a preference source produced %d pushes, want 1", got)
	}

	if got := build(nil).count(); got != 0 {
		t.Fatalf("push count with no preference source: got %d, want 0 (fail closed on a misassembled gateway)", got)
	}
}

// --- PB-PUSH-10 (sender half): the preference survives a restart ------------

// TestPBPUSH10_DisabledPreferenceStillSuppressesAtTheSenderAfterARestart is the
// requirement's own restart test, and it is deliberately asserted at the SENDER. A test
// that asserted at the receiver would pass while the machine kept emitting triggers the
// provider observes -- the exact "requirement satisfiable while the defect ships" shape
// PB-PUSH-10 was written to close.
//
// Everything in the second half is rebuilt from the on-disk path: a new prefs handle, a
// new seq source, a new notifier. Nothing is carried over in memory.
func TestPBPUSH10_DisabledPreferenceStillSuppressesAtTheSenderAfterARestart(t *testing.T) {
	dir := t.TempDir()
	prefsPath := dir + "/push-prefs.json"
	seqPath := dir + "/wake.seq"

	// Run 1: the user turns both toggles off.
	prefs, err := OpenPushPrefs(prefsPath)
	if err != nil {
		t.Fatalf("OpenPushPrefs: %v", err)
	}
	if err := prefs.SavePrefs(PushPrefs{Version: 5, NeedsInput: false, Finished: false}); err != nil {
		t.Fatalf("SavePrefs: %v", err)
	}

	// Run 2: a whole new gateway process reads the same state dir.
	if got := restartAndDrive(t, prefsPath, seqPath); got != 0 {
		t.Fatalf("push count after a gateway restart with a disabled preference: got %d, want 0 at the SENDER", got)
	}

	// POSITIVE CONTROL, in the same shape. Without it a notifier that pushes for nobody
	// -- or one whose durable prefs handle always fails closed -- passes the assertion
	// above and the requirement is "satisfied" by a push transport that never fires.
	prefsOn, err := OpenPushPrefs(prefsPath)
	if err != nil {
		t.Fatalf("OpenPushPrefs(enable): %v", err)
	}
	if err := prefsOn.SavePrefs(PushPrefs{Version: 6, NeedsInput: true, Finished: true}); err != nil {
		t.Fatalf("SavePrefs(enable): %v", err)
	}
	if got := restartAndDrive(t, prefsPath, seqPath); got != 3 {
		t.Fatalf("control: after a restart with an ENABLED preference the sender produced %d pushes, want 3", got)
	}
}

// restartAndDrive builds an entirely fresh notifier over the given durable paths --
// nothing carried over in memory, which is what "after a gateway restart" has to mean --
// drives three push-worthy transitions well apart in time, and reports how many triggers
// reached the sender.
func restartAndDrive(t *testing.T, prefsPath, seqPath string) int {
	t.Helper()
	prefs, err := OpenPushPrefs(prefsPath)
	if err != nil {
		t.Fatalf("OpenPushPrefs(restart): %v", err)
	}
	seq, err := OpenSeqSource(seqPath)
	if err != nil {
		t.Fatalf("OpenSeqSource(restart): %v", err)
	}
	pusher := &fakePusher{}
	clk := newTestClock()
	n := NewPushNotifier(&recordingSink{}, PushConfig{
		Pusher:  pusher,
		Target:  "phone-routing-id",
		WakeKey: testWakeKey(),
		EpochID: 7,
		Now:     clk.Now,
		Seq:     seq,
		Prefs:   prefs,
	})
	for i, g := range []status.Group{
		status.GroupNeedsInput, status.GroupReadyForReview, status.GroupCompleted,
	} {
		clk.advance(2 * DefaultPushWindow)
		if err := n.Event(protocol.JournalRecord{
			Cursor: uint64(i + 1), SessionID: "m/s1", Type: "status", Group: g,
		}); err != nil {
			t.Fatalf("Event(%s): %v", g, err)
		}
	}
	return pusher.count()
}

// --- wiring: production must actually take this seam ------------------------

// TestPBPUSH0_ServiceWiresTheNotifierIntoTheLiveJournalPath is the class-(v) guard. A
// notifier that is constructed, configured and unit-tested but never inserted into the
// sink chain the Gateway delivers to would pass every test above while no push is ever
// produced in production -- which is precisely the state the tree is in today (zero
// non-test callers of PushTrigger anywhere).
//
// It reaches into the composed chain on purpose: the assertion is about STRUCTURE, and
// only the structure distinguishes "wired" from "built and dropped".
func TestPBPUSH0_ServiceWiresTheNotifierIntoTheLiveJournalPath(t *testing.T) {
	mb := &pushCapableMailbox{}
	svc := NewService(ServiceConfig{
		DaemonSocket: "/nonexistent.sock",
		Relay:        mb,
		PhoneTarget:  "phone-routing-id",
		Key:          testContentKey(),
		WakeKey:      testWakeKey(),
		EpochID:      7,
		PushPrefs:    &stubPrefs{prefs: PushPrefs{Version: 1, NeedsInput: true, Finished: true}},
	})

	notifier := svc.PushNotifier()
	if notifier == nil {
		t.Fatal("Service.PushNotifier() is nil: the gateway has no push producer")
	}

	// Unwrap the sink the Gateway actually delivers into.
	coalescer, ok := svc.gw.sink.(*CoalescingSink)
	if !ok {
		t.Fatalf("gateway sink is %T, want the coalescing wrapper", svc.gw.sink)
	}
	if coalescer.inner != OutboundSink(notifier) {
		t.Fatalf("the coalescing sink wraps %T, not the service's push notifier: journal events never reach the trigger", coalescer.inner)
	}

	// And the notifier must still hand everything down to the sealing sink, or the push
	// wiring would have severed the journal.
	if _, ok := notifier.inner.(*RelaySink); !ok {
		t.Fatalf("the push notifier wraps %T, want the *RelaySink that seals and appends", notifier.inner)
	}

	// The relay client is BOTH the mailbox and the push transport: a Service that
	// silently left Pusher nil would degrade to no push with nothing failing.
	if notifier.cfg.Pusher == nil {
		t.Fatal("the notifier has no push transport: NewService did not wire cfg.Relay as the pusher")
	}
	if notifier.cfg.Target != "phone-routing-id" {
		t.Fatalf("notifier target = %q, want the phone routing id", notifier.cfg.Target)
	}
	if notifier.cfg.WakeKey != testWakeKey() {
		t.Fatal("the notifier was not given the configured wake key")
	}
}

// TestPBPUSH0_NotifierPassesThroughTheWrappedSinkContracts pins the two optional
// interfaces the surrounding code type-asserts THROUGH this new wrapper. A wrapper that
// answers neither is not a compile error and breaks two unrelated guarantees silently:
// SetMachine leaves the reconcile record unattributable (the phone then refuses every
// mutating op forever, PB-SYNC-7), and a lost DeliveredCursor makes every restart
// re-read the journal from 0 and re-flood the mailbox (PB-GW-8).
func TestPBPUSH0_NotifierPassesThroughTheWrappedSinkContracts(t *testing.T) {
	inner := &recordingSink{cursor: 4242}
	seq, err := OpenSeqSource("")
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	n := NewPushNotifier(inner, PushConfig{Target: "t", WakeKey: testWakeKey(), EpochID: 7, Seq: seq})

	var namer machineNamer = n
	namer.SetMachine("endpoint-9")
	if inner.machine != "endpoint-9" {
		t.Fatalf("inner sink machine = %q, want %q: SetMachine did not reach the sealing sink", inner.machine, "endpoint-9")
	}

	var cs CursorSource = n
	if got := cs.DeliveredCursor(); got != 4242 {
		t.Fatalf("DeliveredCursor through the notifier = %d, want the inner sink's 4242", got)
	}
}

// pushCapableMailbox is what the production relay client is: a Mailbox AND a push
// transport on one connection.
type pushCapableMailbox struct {
	fakeMailbox
	pushes int
}

func (p *pushCapableMailbox) PushTrigger(_ context.Context, _ string, _ []byte) error {
	p.pushes++
	return nil
}

// assertNoFieldOfType fails when any exported or unexported field of v satisfies match.
// It is how "this configuration cannot carry that key" is expressed as a test rather
// than as a comment: a later widening of the struct is then a failure, not a review miss.
func assertNoFieldOfType(t *testing.T, v any, typeName string, match func(any) bool) {
	t.Helper()
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Struct {
		t.Fatalf("assertNoFieldOfType: %T is not a struct", v)
	}
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Type().Field(i)
		if match(reflect.Zero(f.Type).Interface()) {
			t.Fatalf("%T carries field %q of type %s: the push path must not be able to reach a content key",
				v, f.Name, typeName)
		}
	}
}

// hasFieldOfType is assertNoFieldOfType's positive control.
func hasFieldOfType(v any, match func(any) bool) bool {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < rv.NumField(); i++ {
		if match(reflect.Zero(rv.Type().Field(i).Type).Interface()) {
			return true
		}
	}
	return false
}
