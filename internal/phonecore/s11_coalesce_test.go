package phonecore

// Slice S11 -- FAILING-FIRST (TDD RED, GG-5) tests for PB-INPUT-5 (coalescing to stay
// inside the relay's append quota) and PB-INPUT-6 (ordering + a flush at every boundary),
// plus PB-INPUT-1's half that only the buffer can hold: bytes the phone accepted from the
// user and could not deliver.
//
// THE CONTRACT these tests freeze (undefined symbols -> compile-level RED for the
// phonecore test binary):
//
//	const InputFrameInterval = 125 * time.Millisecond  // §6.0: <= 8 frames/s sustained
//	const MaxInputPayload    = 4096                    // §6.0: 4 KiB per frame
//
//	type Undelivered struct{ Session string; Bytes int; At time.Time; Reason string }
//
//	type InputCoalescer struct{ ... }
//	func NewInputCoalescer(now func() time.Time) *InputCoalescer
//	func (*InputCoalescer) Type(session string, data []byte) []InputFrame
//	func (*InputCoalescer) Insert(session string, data []byte) []InputFrame
//	func (*InputCoalescer) Resize(session string, cols, rows int) []InputFrame
//	func (*InputCoalescer) Due() []InputFrame
//	func (*InputCoalescer) Flush() []InputFrame
//	func (*InputCoalescer) Buffered(session string) int
//	func (*InputCoalescer) Abandon(reason string) []Undelivered
//	func (*InputCoalescer) Fail(f InputFrame, reason string)
//	func (*InputCoalescer) Undelivered() []Undelivered
//
// WHY THE FRAME TYPE IS THE EXISTING phonecore.InputFrame AND NOT A NEW ONE. The
// coalescer's output is sealed verbatim by SealInputData / SealInputResize, whose
// arguments are exactly (session, data) and (session, cols, rows). A separate "coalesced
// frame" type would have to be translated at the seal site, and a translation is where a
// field gets dropped silently. InputFrame.T is "data" or "resize" -- the same
// discriminator the gateway's OpenMailboxFrame dispatches on.
//
// WHY THE CLOCK IS INJECTED. Every assertion here is about WHEN a frame is emitted
// relative to a 125 ms window. On a real clock the sustained-typing test would take
// 60 seconds of wall time and would flake on a loaded host; §6.0's rate is a property of
// the algorithm, not of this machine, and it must be asserted as one. The host is an
// Apple M1 running an x86_64 toolchain under Rosetta, so wall-clock timings here are
// PESSIMISTIC and unstable in both directions -- another reason no assertion in this file
// reads the real clock.
//
// LEADING EDGE, NOT TRAILING. The first keystroke of a burst must go out immediately:
// S6b measured live typing at p50 31 ms phone->PTY (21% of the 150 ms budget, ADR-007 B7
// "AS BUILT"), and a purely trailing-edge coalescer would add a flat 125 ms to every
// keystroke a user types after a pause -- 156 ms, past the whole p50 budget, for the
// single most latency-visible keystroke there is. So: emit on the first byte, then hold
// the window. §6.0's "<= 8 frames/s sustained" is a SUSTAINED-REGIME average (the same
// reading §6.0 was amended to spell out for the drain ceiling), not a ban on the first
// frame of a burst.
//
// This file contains NO implementation.

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// s11Clock is the injected clock. Nothing here reads the wall clock.
type s11Clock struct{ t time.Time }

func s11NewClock() *s11Clock {
	// Millisecond-aligned and arbitrary; the value never appears in an assertion.
	return &s11Clock{t: time.UnixMilli(1_784_000_000_000)}
}

func (c *s11Clock) now() time.Time          { return c.t }
func (c *s11Clock) advance(d time.Duration) { c.t = c.t.Add(d) }

// s11Session is the namespaced session id every frame in this file targets.
const s11Session = "m1/s1"

// s11DataBytes concatenates the payloads of the "data" frames for session, in the order
// they were emitted, and fails if any frame targets another session or carries a payload
// over the §6.0 cap. It is the reassembly every ordering and loss assertion is built on.
func s11DataBytes(t *testing.T, frames []InputFrame, session string) []byte {
	t.Helper()
	var out []byte
	for i, f := range frames {
		if f.T != "data" {
			continue
		}
		if f.Session != session {
			t.Fatalf("frame %d targets session %q, want %q -- a keystroke on another session's lease is the A7 cross-session misroute", i, f.Session, session)
		}
		if len(f.Data) > MaxInputPayload {
			t.Fatalf("frame %d carries %d bytes, over §6.0's %d-byte coalesced-payload cap", i, len(f.Data), MaxInputPayload)
		}
		out = append(out, f.Data...)
	}
	return out
}

// s11Kinds renders the frame kinds in order, for the boundary-ordering assertions.
func s11Kinds(frames []InputFrame) string {
	kinds := make([]string, 0, len(frames))
	for _, f := range frames {
		kinds = append(kinds, f.T)
	}
	return strings.Join(kinds, ",")
}

// ---------------------------------------------------------------------------
// §6.0's numbers
// ---------------------------------------------------------------------------

// TestS11Budget_CoalescingConstantsAreTheBudgetedValues pins §6.0 so the numbers cannot
// drift into implementer discretion -- the objection round 2 raised against "a stated
// bound". 125 ms is 8 frames/s, which is the deliberate 20% headroom under the relay's
// MailboxAppendPerMin: 600 (= 10/s), the ONLY cap that applies to mailbox_append.
func TestS11Budget_CoalescingConstantsAreTheBudgetedValues(t *testing.T) {
	if InputFrameInterval != 125*time.Millisecond {
		t.Errorf("InputFrameInterval = %v, want 125ms (§6.0: <= 8 frames/s sustained)", InputFrameInterval)
	}
	if MaxInputPayload != 4096 {
		t.Errorf("MaxInputPayload = %d, want 4096 (§6.0: 4 KiB per frame, flush early if exceeded)", MaxInputPayload)
	}
	// The headroom is the point of the number, so state it as an assertion rather than
	// as a comment: a 100 ms interval would be 10/s, exactly the relay's cap, and the
	// first jitter would trip codeQuotaExceeded mid-lease.
	if perSec := float64(time.Second) / float64(InputFrameInterval); perSec > 8 {
		t.Errorf("InputFrameInterval %v is %.1f frames/s, over §6.0's 8/s ceiling", InputFrameInterval, perSec)
	}
}

// ---------------------------------------------------------------------------
// PB-INPUT-5 -- sustained typing inside the quota, losing nothing
// ---------------------------------------------------------------------------

// TestS11Coalescer_SustainedAutorepeatStaysUnderTheRelayQuota is PB-INPUT-5's acceptance
// criterion verbatim: "continuous input for >= 60 s at autorepeat rate stays within quota
// and loses no keystrokes". It is a SUSTAINED test, not a burst -- the failure it exists
// to catch is a lease that dies with codeQuotaExceeded after ~20 s while every short-burst
// latency test still passes.
//
// The premise is asserted first so the test cannot be quietly satisfied by a slow host:
// 30 Hz for 60 s is 1800 keystrokes, and 1800 un-coalesced appends is three times the
// relay's 600-per-minute window.
//
// The loss check is what makes the rate check honest. A coalescer that emitted NOTHING
// would pass every rate assertion below; only reassembling the exact byte stream refuses
// it.
func TestS11Coalescer_SustainedAutorepeatStaysUnderTheRelayQuota(t *testing.T) {
	const (
		hz      = 30
		seconds = 60
		// mailboxAppendPerMin is relay/config.go's MailboxAppendPerMin. It is written
		// as a literal because internal/remote/relay is outside phonecore's bound
		// dependency closure (PB-BIND-0, deps_allowlist.txt) and must not be imported
		// here; mobile/conformance pins the two against each other.
		mailboxAppendPerMin = 600
	)
	keystrokes := hz * seconds

	// PREMISE: the un-coalesced shape this requirement exists to prevent.
	if keystrokes <= mailboxAppendPerMin {
		t.Fatalf("premise broken: %d un-coalesced appends is inside the %d/min window, so this test proves nothing about coalescing", keystrokes, mailboxAppendPerMin)
	}

	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	var typed, sent []byte
	var frames []InputFrame
	// emitted[i] is the wall instant of the i-th emitted frame, for the rate windows.
	var emitted []time.Time

	record := func(fs []InputFrame) {
		for _, f := range fs {
			frames = append(frames, f)
			emitted = append(emitted, clk.now())
		}
	}

	tick := time.Second / hz
	for i := 0; i < keystrokes; i++ {
		b := byte('a' + i%26)
		typed = append(typed, b)
		record(c.Type(s11Session, []byte{b}))
		clk.advance(tick)
		record(c.Due()) // the caller's cadence; the coalescer decides what is due
	}
	// End of the typing session: the trailing window's bytes must come out, or the last
	// keystrokes are lost the moment the user stops -- PB-INPUT-6's "a sustained-rate test
	// alone would pass while the last buffered keystrokes are lost".
	record(c.Flush())

	sent = s11DataBytes(t, frames, s11Session)
	if !bytes.Equal(sent, typed) {
		t.Fatalf("coalescing lost or reordered keystrokes: sent %d bytes, typed %d bytes (first divergence at %d)", len(sent), len(typed), s11FirstDiff(sent, typed))
	}

	// The relay's limiter is a TUMBLING one-minute window (§6.0), so the binding check is
	// per window, not an average. Every 60 s window over the run must fit.
	if worst := s11WorstWindow(emitted, time.Minute); worst > mailboxAppendPerMin {
		t.Fatalf("worst 60s window issued %d appends, over the relay's MailboxAppendPerMin of %d -- the lease dies with codeQuotaExceeded mid-session", worst, mailboxAppendPerMin)
	}
	// And the §6.0 budget itself, which is tighter than the relay cap by the deliberate
	// 20% headroom.
	if budget := int(float64(seconds) * float64(time.Second) / float64(InputFrameInterval)); len(frames) > budget+1 {
		t.Fatalf("emitted %d frames over %ds, over §6.0's %d (8/s sustained, +1 for the leading edge of the first burst)", len(frames), seconds, budget)
	}
	if len(frames) == 0 {
		t.Fatal("emitted no frames at all; the rate assertions above are vacuous")
	}
}

// s11FirstDiff reports the first index at which two byte slices differ (-1 when equal but
// for length), so a loss failure names where the stream broke.
func s11FirstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}

// s11WorstWindow is the largest number of instants falling inside any window-length span.
func s11WorstWindow(at []time.Time, window time.Duration) int {
	worst := 0
	for i := range at {
		n := 0
		for j := i; j < len(at) && at[j].Sub(at[i]) < window; j++ {
			n++
		}
		if n > worst {
			worst = n
		}
	}
	return worst
}

// TestS11Coalescer_FirstKeystrokeOfABurstIsNotDelayed is the latency half of the regime
// split, and the mutation that a pure trailing-edge coalescer fails. S6b measured p50
// 31 ms phone->PTY; holding the first byte for a full InputFrameInterval would add 125 ms
// to it and push the most latency-visible keystroke there is past §6.0's whole p50 budget.
func TestS11Coalescer_FirstKeystrokeOfABurstIsNotDelayed(t *testing.T) {
	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	frames := c.Type(s11Session, []byte("l"))
	if len(frames) != 1 {
		t.Fatalf("first keystroke emitted %d frames, want 1 -- a trailing-edge coalescer adds %v to every keystroke after a pause, over §6.0's 150ms p50 budget on its own", len(frames), InputFrameInterval)
	}
	if got := string(frames[0].Data); got != "l" {
		t.Fatalf("first frame data = %q, want %q", got, "l")
	}

	// ... and the SECOND keystroke inside the same window is held, or there is no
	// coalescing at all and PB-INPUT-5's quota assertion above is unreachable.
	clk.advance(InputFrameInterval / 5)
	if held := c.Type(s11Session, []byte("s")); len(held) != 0 {
		t.Fatalf("a keystroke %v into the %v window emitted %d frames, want 0 (buffered) -- without coalescing a 30Hz autorepeat is 30 appends/s", InputFrameInterval/5, InputFrameInterval, len(held))
	}
	if c.Buffered(s11Session) != 1 {
		t.Fatalf("Buffered = %d, want 1 -- the held keystroke must be accounted for, or the 'never silently dropped' ledger has nothing to report", c.Buffered(s11Session))
	}

	// ... and it comes out once the window elapses.
	clk.advance(InputFrameInterval)
	due := c.Due()
	if got := string(s11DataBytes(t, due, s11Session)); got != "s" {
		t.Fatalf("after the window elapsed, Due() yielded %q, want %q", got, "s")
	}
}

// ---------------------------------------------------------------------------
// PB-INPUT-6 -- ordering and a flush at EVERY boundary
// ---------------------------------------------------------------------------

// TestS11Coalescer_PreservesByteOrderAcrossFrames is PB-INPUT-6's first clause. Order is
// the property a coalescer is most likely to break, because the natural implementation
// (a per-session map plus a timer) reorders the moment a flush races a write.
func TestS11Coalescer_PreservesByteOrderAcrossFrames(t *testing.T) {
	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	// Uneven arrival: some inside a window, some spanning several.
	writes := []struct {
		data  string
		after time.Duration
	}{
		{"git ", 0},
		{"sta", 7 * time.Millisecond},
		{"tus", 11 * time.Millisecond},
		{" --", 200 * time.Millisecond},
		{"sho", 3 * time.Millisecond},
		{"rt\r", 400 * time.Millisecond},
	}
	var typed []byte
	var frames []InputFrame
	for _, w := range writes {
		clk.advance(w.after)
		frames = append(frames, c.Due()...)
		typed = append(typed, w.data...)
		frames = append(frames, c.Type(s11Session, []byte(w.data))...)
	}
	clk.advance(InputFrameInterval)
	frames = append(frames, c.Due()...)
	frames = append(frames, c.Flush()...)

	if got := s11DataBytes(t, frames, s11Session); !bytes.Equal(got, typed) {
		t.Fatalf("byte stream reordered or lost: got %q, want %q", got, typed)
	}
}

// TestS11Coalescer_FlushesBeforeResize is PB-INPUT-6's "flush before resize". A resize
// that overtakes buffered keystrokes is not a cosmetic reordering: the shell re-wraps the
// line, and the bytes then land against a grid the user was not looking at when they typed
// them.
func TestS11Coalescer_FlushesBeforeResize(t *testing.T) {
	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	c.Type(s11Session, []byte("e")) // leading edge, emitted
	clk.advance(InputFrameInterval / 4)
	if held := c.Type(s11Session, []byte("cho hi")); len(held) != 0 {
		t.Fatalf("expected the burst to be buffered, got %d frames", len(held))
	}

	frames := c.Resize(s11Session, 120, 40)
	if got := s11Kinds(frames); got != "data,resize" {
		t.Fatalf("Resize emitted %q, want \"data,resize\" -- the buffered keystrokes must be flushed BEFORE the resize, never after it and never dropped", got)
	}
	if got := string(s11DataBytes(t, frames, s11Session)); got != "cho hi" {
		t.Fatalf("flushed-before-resize data = %q, want %q", got, "cho hi")
	}
	if frames[1].Cols != 120 || frames[1].Rows != 40 {
		t.Fatalf("resize frame = %+v, want cols 120 rows 40", frames[1])
	}
	if c.Buffered(s11Session) != 0 {
		t.Fatalf("Buffered = %d after a resize flush, want 0", c.Buffered(s11Session))
	}
}

// TestS11Coalescer_FlushEmptiesEveryBoundary covers PB-INPUT-6's remaining boundaries with
// ONE mechanism, because they are one mechanism: release / take_control_end, app
// backgrounding, and biometric-freshness expiry all end the ability to type and must leave
// nothing buffered. §6.0 is explicit that a freshness lapse "must pause input and
// re-authorize, not silently continue or silently drop".
//
// The mutation control is the second half: after the flush the SAME bytes must not be
// emitted again by a later Due(). A coalescer that copies rather than consumes its buffer
// passes the first assertion and double-types the user's line.
func TestS11Coalescer_FlushEmptiesEveryBoundary(t *testing.T) {
	boundaries := []string{"release / take_control_end", "app backgrounding", "biometric freshness expiry"}
	for _, name := range boundaries {
		t.Run(name, func(t *testing.T) {
			clk := s11NewClock()
			c := NewInputCoalescer(clk.now)

			c.Type(s11Session, []byte("r")) // leading edge
			clk.advance(InputFrameInterval / 4)
			c.Type(s11Session, []byte("m -rf ."))

			flushed := c.Flush()
			if got := string(s11DataBytes(t, flushed, s11Session)); got != "m -rf ." {
				t.Fatalf("Flush at %s yielded %q, want %q -- buffered input at a boundary is flushed or reported, never silently dropped", name, got, "m -rf .")
			}
			if c.Buffered(s11Session) != 0 {
				t.Fatalf("Buffered = %d after Flush, want 0", c.Buffered(s11Session))
			}
			clk.advance(InputFrameInterval * 2)
			if again := c.Due(); len(again) != 0 {
				t.Fatalf("Due() after Flush yielded %d frames -- the flushed bytes were copied, not consumed, so the user's line is typed twice", len(again))
			}
		})
	}
}

// TestS11Coalescer_OversizePayloadFlushesEarlyInOrder is §6.0's 4 KiB cap. Without it a
// single window can accumulate an unbounded buffer (a held paste, a wedged tail) and the
// resulting frame is refused by the relay's message-size limit -- which loses the whole
// burst at once rather than pacing it.
func TestS11Coalescer_OversizePayloadFlushesEarlyInOrder(t *testing.T) {
	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	// One write, 2.5 x the cap, entirely inside one window.
	big := make([]byte, MaxInputPayload*5/2)
	for i := range big {
		big[i] = byte('0' + i%10)
	}
	frames := c.Type(s11Session, big)
	frames = append(frames, c.Flush()...)

	if len(frames) < 3 {
		t.Fatalf("a %d-byte write emitted %d frames; the %d-byte cap forces at least 3", len(big), len(frames), MaxInputPayload)
	}
	if got := s11DataBytes(t, frames, s11Session); !bytes.Equal(got, big) {
		t.Fatalf("oversize write lost or reordered bytes: got %d, want %d (first divergence at %d)", len(got), len(big), s11FirstDiff(got, big))
	}
	// s11DataBytes already refuses any frame over the cap; assert it saw them, so a
	// coalescer that emitted one giant frame cannot pass by emitting nothing.
	if len(frames) == 0 {
		t.Fatal("no frames emitted for an oversize write")
	}
}

// TestS11Coalescer_PasteIsAtomicAndNeverInterleaved is PB-INPUT-6's "stated treatment of
// paste and IME composition, which are not keystroke streams".
//
// THE STATED TREATMENT, which this test pins:
//   - A paste, and an IME COMMIT, arrive through Insert as one unit. They are not
//     keystrokes and are not subject to the 125 ms leading-edge window, which exists to
//     coalesce autorepeat; holding a paste for a window buys nothing (it is already one
//     event) and costs 125 ms of visible latency.
//   - Insert flushes any buffered keystrokes FIRST, so a paste can never overtake
//     characters the user typed before it.
//   - An oversize unit is split at MaxInputPayload and at no other point, in order.
//   - An IME PREEDIT is never sent. There is no entry point for one here, and that is the
//     decision: a preedit is local until the IME commits, at which point it arrives as an
//     Insert. Sending preedit text would type-then-correct against a live shell.
func TestS11Coalescer_PasteIsAtomicAndNeverInterleaved(t *testing.T) {
	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	c.Type(s11Session, []byte("#")) // leading edge, emitted
	clk.advance(InputFrameInterval / 4)
	c.Type(s11Session, []byte(" prefix ")) // buffered

	paste := []byte(strings.Repeat("PASTE", 2000)) // 10000 bytes, over 2 x the cap
	frames := c.Insert(s11Session, paste)

	got := s11DataBytes(t, frames, s11Session)
	want := append([]byte(" prefix "), paste...)
	if !bytes.Equal(got, want) {
		t.Fatalf("paste interleaved with or overtook buffered keystrokes (first divergence at %d): got %d bytes, want %d", s11FirstDiff(got, want), len(got), len(want))
	}
	if c.Buffered(s11Session) != 0 {
		t.Fatalf("Buffered = %d after Insert, want 0 -- Insert must flush the keystroke buffer, not sit beside it", c.Buffered(s11Session))
	}
	// Atomicity: the paste is emitted by THIS call, not left for a later Due(). A paste
	// the user watches disappear for 125 ms reads as a dropped paste.
	clk.advance(InputFrameInterval * 2)
	if late := c.Due(); len(late) != 0 {
		t.Fatalf("Due() after Insert yielded %d frames; the paste must be emitted atomically by Insert", len(late))
	}
}

// ---------------------------------------------------------------------------
// PB-INPUT-1 -- live-only: never queued, never replayed, always reported
// ---------------------------------------------------------------------------

// TestS11Coalescer_AbandonReportsDeliveryUnknownAndNeverReplays is PB-INPUT-1 at the one
// place ADR-007 D7 does not already cover. transport.SendLive refuses an in-flight
// keystroke with ErrNotDelivered and S6b proved it never survives a disconnect
// (TestS6B_KeystrokeNeverSurvivesADisconnectWhileFollowing). What no layer covers is the
// COALESCER'S BUFFER: bytes the phone took from the user, acknowledged on screen, and
// never handed to the transport at all. Those have no ErrNotDelivered to carry them.
//
// Both failure modes are fenced in one test, because each alone is satisfiable by the
// other's bug:
//
//	(a) silently dropped  -> the ledger is empty, and the user believes they typed it;
//	(b) held and replayed -> the keystroke lands minutes later against a different
//	    terminal state, which is exactly the hazard ADR-007 D7 makes structural.
func TestS11Coalescer_AbandonReportsDeliveryUnknownAndNeverReplays(t *testing.T) {
	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	c.Type(s11Session, []byte("d")) // leading edge, emitted
	clk.advance(InputFrameInterval / 4)
	c.Type(s11Session, []byte("eploy prod\r")) // buffered when the link drops

	lost := c.Abandon("no live connection")
	if len(lost) != 1 {
		t.Fatalf("Abandon reported %d undelivered entries, want 1 -- buffered input at a disconnect must resolve as an explicit \"delivery unknown / not sent\", never a silent drop (PB-INPUT-1)", len(lost))
	}
	if lost[0].Session != s11Session || lost[0].Bytes != len("eploy prod\r") {
		t.Fatalf("undelivered entry = %+v, want session %q and %d bytes", lost[0], s11Session, len("eploy prod\r"))
	}
	if lost[0].Reason == "" {
		t.Fatal("undelivered entry carries no reason; PB-INPUT-1 requires the state be SURFACED to the user, and an empty reason surfaces nothing")
	}
	if !lost[0].At.Equal(clk.now()) {
		t.Fatalf("undelivered At = %v, want the injected clock's %v -- a wall-clock reading here is a second clock authority (PB-TIME-2)", lost[0].At, clk.now())
	}
	if c.Buffered(s11Session) != 0 {
		t.Fatalf("Buffered = %d after Abandon, want 0", c.Buffered(s11Session))
	}

	// (b) NEVER REPLAYED. The link comes back; nothing may follow it out.
	clk.advance(10 * time.Minute)
	if replayed := c.Due(); len(replayed) != 0 {
		t.Fatalf("Due() after Abandon yielded %d frames -- an abandoned keystroke was queued and replayed %v later, which is the disconnect hazard ADR-007 D7 forbids structurally", len(replayed), 10*time.Minute)
	}
	if replayed := c.Flush(); len(replayed) != 0 {
		t.Fatalf("Flush() after Abandon yielded %d frames -- the abandoned bytes were retained", len(replayed))
	}

	// The ledger survives for the UI to read; it is not consumed by Abandon's return.
	if got := c.Undelivered(); len(got) != 1 {
		t.Fatalf("Undelivered() = %d entries, want 1 -- the UX state must remain readable after Abandon returns", len(got))
	}
}

// TestS11Coalescer_AFailedFrameIsReportedNotResent covers the other half of PB-INPUT-1:
// a frame the coalescer already handed out, whose send then failed. transport.SendLive
// returns ErrNotDelivered for it; the phone must record it and must NOT hand it back.
//
// The mutation control is the resend check. An implementation that "helpfully" re-buffers
// a failed frame turns the live-only path into a queue, one frame at a time.
func TestS11Coalescer_AFailedFrameIsReportedNotResent(t *testing.T) {
	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	frames := c.Type(s11Session, []byte("q"))
	if len(frames) != 1 {
		t.Fatalf("expected 1 leading-edge frame, got %d", len(frames))
	}
	c.Fail(frames[0], "transport: not delivered; no live connection")

	led := c.Undelivered()
	if len(led) != 1 || led[0].Bytes != 1 || led[0].Session != s11Session {
		t.Fatalf("Undelivered() = %+v, want one 1-byte entry for %q", led, s11Session)
	}
	clk.advance(InputFrameInterval * 4)
	if again := c.Due(); len(again) != 0 {
		t.Fatalf("Due() after Fail yielded %d frames -- a failed live frame was re-buffered, turning the live-only path into a queue (ADR-007 D7)", len(again))
	}
	if again := c.Flush(); len(again) != 0 {
		t.Fatalf("Flush() after Fail yielded %d frames -- same defect", len(again))
	}
}

// TestS11Coalescer_ResizeIsLiveOnlyToo. PB-INPUT-1 names "input/resize", not just input.
// A resize that survived a disconnect would re-wrap a terminal the user is no longer
// looking at, and the daemon applies it to whatever session holds the lease then.
func TestS11Coalescer_ResizeIsLiveOnlyToo(t *testing.T) {
	clk := s11NewClock()
	c := NewInputCoalescer(clk.now)

	frames := c.Resize(s11Session, 80, 24)
	if got := s11Kinds(frames); got != "resize" {
		t.Fatalf("Resize with an empty buffer emitted %q, want \"resize\"", got)
	}
	c.Fail(frames[0], "transport: not delivered; no live connection")

	if got := c.Undelivered(); len(got) != 1 {
		t.Fatalf("Undelivered() = %d, want 1 -- an undelivered resize is reported like an undelivered keystroke", len(got))
	}
	clk.advance(time.Minute)
	if again := append(c.Due(), c.Flush()...); len(again) != 0 {
		t.Fatalf("a failed resize was replayed %d frames later", len(again))
	}
}
