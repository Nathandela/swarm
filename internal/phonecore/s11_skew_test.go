package phonecore

// Slice S11 -- FAILING-FIRST (TDD RED, GG-5) tests for PB-TIME-1 (skew is detected,
// bounded at +/-30 s, and surfaced DISTINCTLY) and PB-TIME-3 (the skew-detection protocol
// itself: an authenticated machine-time exchange, an RTT allowance, a stated
// monotonic-vs-wall split, and defined offline behaviour).
//
// THE CONTRACT these tests freeze (undefined symbols -> compile-level RED):
//
//	const MaxClockSkew = 30 * time.Second   // §6.0
//	var ErrClockSkew error                  // distinct, user-legible
//
//	type Skew struct{ Offset time.Duration; RTT time.Duration; Known bool }
//
//	type SkewMonitor struct{ ... }
//	func NewSkewMonitor(now func() time.Time) *SkewMonitor
//	func (*SkewMonitor) Sent(operationID string)
//	func (*SkewMonitor) Observe(operationID string, machineTime time.Time) (Skew, error)
//	func (*SkewMonitor) Skew() Skew
//	func (*SkewMonitor) Check() error
//
//	func (*Core) SkewMonitor() *SkewMonitor // fed from the AUTHENTICATED inbound path
//
// THE PROTOCOL, and why it is this one. PB-TIME-3 forbids both available shortcuts:
// the relay may not be the authority (it is untrusted), and an unauthenticated wall clock
// may not be either. What is left is already in the tree: every machine -> phone envelope
// carries an AAD-covered IssuedAt (relaysink.go:432 stamps it; PB-TIME-2 makes
// SealControlReply stamp it too). A relay that alters that field breaks the AEAD, so an
// OPENED frame's IssuedAt is machine-authenticated by construction -- no new verb, no new
// exchange, no new signature.
//
// The RTT allowance is the classic one-way-delay bracket. With the phone's send at T1, the
// machine's stamp Tm and the phone's receive at T2, and both one-way delays non-negative:
//
//	Tm - T2  <=  offset  <=  Tm - T1        (width = RTT)
//
// so the best estimate is Tm - (T1+T2)/2 and the uncertainty is +/-RTT/2. Skew is reported
// ONLY when the WHOLE bracket lies outside +/-MaxClockSkew. That is what stops a slow relay
// from being mistaken for a wrong clock: an untrusted relay can add delay, and delay
// widens the bracket -- it does not move it far enough to manufacture a verdict.
//
// MONOTONIC vs WALL, stated: the RTT is a DURATION between two readings of the same clock
// source and is measured monotonically (time.Time carries a monotonic reading and Sub uses
// it); the OFFSET is a difference between two WALL clocks and cannot be. A wall-clock step
// between T1 and T2 -- an NTP correction, the exact event this whole requirement is about
// -- must therefore not be mistaken for RTT, so a sample whose RTT is NEGATIVE is DISCARDED
// rather than folded in. (Zero is not: an injected clock produces it, and it is the
// tightest possible bracket, not a broken one.)
//
// ONE-SIDEDNESS DOES NOT TRANSFER FROM PB-GW-2, and this is the point the S11 brief asked
// to be checked explicitly. The gateway's bounded-age check is one-sided (`now.Sub(issued)
// > maxAge`, never trips on a future stamp) for two reasons that are specific to it: it is
// an ANTI-REPLAY backstop, and the only adversary it models -- a retaining relay -- can
// make frames older but never newer. Neither reason holds here. This check is not an
// anti-replay device, it is a MEASUREMENT, and a phone whose clock is 45 seconds FAST is
// exactly as broken as one 45 seconds slow: it signs ExpiresAt = now + 1 min, the daemon
// refuses anything beyond now + maxCommandValidity, and the user gets the same opaque "not
// authorized". So the skew bound here is SYMMETRIC, deliberately, and the tests below
// assert both signs.
//
// This file contains NO implementation.

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// s11SkewClock is the phone's injected clock. skewFromMachine is how far the phone's wall
// clock sits from the machine's: positive means the PHONE IS AHEAD.
type s11SkewClock struct {
	machine time.Time     // the machine's true wall clock
	skew    time.Duration // phone = machine + skew
}

func s11NewSkewClock(skew time.Duration) *s11SkewClock {
	return &s11SkewClock{machine: time.UnixMilli(1_784_000_000_000), skew: skew}
}

// now is the PHONE's reading.
func (c *s11SkewClock) now() time.Time { return c.machine.Add(c.skew) }

// advance moves real time forward for both clocks (the skew is unchanged).
func (c *s11SkewClock) advance(d time.Duration) { c.machine = c.machine.Add(d) }

// machineStamp is what the machine seals into IssuedAt right now.
func (c *s11SkewClock) machineStamp() time.Time { return c.machine }

// s11Measure runs one round trip through the monitor: the phone sends, rtt of real time
// passes, the machine stamps at the midpoint, and the reply lands.
func s11Measure(m *SkewMonitor, clk *s11SkewClock, op string, rtt time.Duration) (Skew, error) {
	m.Sent(op)
	clk.advance(rtt / 2)
	stamp := clk.machineStamp()
	clk.advance(rtt - rtt/2)
	return m.Observe(op, stamp)
}

// ---------------------------------------------------------------------------
// §6.0's number, and the sentinel PB-TIME-1 is about
// ---------------------------------------------------------------------------

// TestS11Skew_BoundIsTheBudgetedThirtySeconds pins §6.0.
func TestS11Skew_BoundIsTheBudgetedThirtySeconds(t *testing.T) {
	if MaxClockSkew != 30*time.Second {
		t.Fatalf("MaxClockSkew = %v, want 30s (§6.0, PB-TIME-1)", MaxClockSkew)
	}
}

// TestS11Skew_TheErrorIsDistinctAndLegible is PB-TIME-1's acceptance criterion in full:
// "a distinct, user-legible error (not the generic authorization failure)". Today a
// two-minute-slow handset fails EVERY command with the daemon's opaque "not authorized"
// (skeleton/deviceauth.go:74-76), which tells the user nothing they can act on -- and the
// action is not "re-pair", it is "fix the clock".
func TestS11Skew_TheErrorIsDistinctAndLegible(t *testing.T) {
	if ErrClockSkew == nil {
		t.Fatal("ErrClockSkew is nil; PB-TIME-1 requires a DISTINCT sentinel, not a generic authorization failure")
	}
	msg := strings.ToLower(ErrClockSkew.Error())
	if !strings.Contains(msg, "clock") {
		t.Errorf("ErrClockSkew reads %q; it is shown to a user who has to fix their handset's clock, so it must say so", ErrClockSkew.Error())
	}
	if strings.Contains(msg, "not authorized") || strings.Contains(msg, "unauthorized") {
		t.Errorf("ErrClockSkew reads %q -- it must not be mistakable for the authorization failure it exists to replace", ErrClockSkew.Error())
	}

	// The measured offset must reach the user too: "your clock is wrong" without a
	// direction or a magnitude is not actionable.
	clk := s11NewSkewClock(90 * time.Second)
	m := NewSkewMonitor(clk.now)
	_, err := s11Measure(m, clk, "op-1", 20*time.Millisecond)
	if !errors.Is(err, ErrClockSkew) {
		t.Fatalf("a 90s-fast phone: err = %v, want ErrClockSkew", err)
	}
	if !strings.Contains(err.Error(), "1m30s") && !strings.Contains(err.Error(), "90") {
		t.Errorf("the refusal reads %q and never names the measured offset (~90s); the user cannot tell how far out they are", err.Error())
	}
}

// ---------------------------------------------------------------------------
// PB-TIME-3 -- the boundary, in both directions
// ---------------------------------------------------------------------------

// TestS11Skew_BoundaryAtTwentyNineThirtyAndThirtyOne is PB-TIME-3's acceptance criterion
// verbatim ("boundary tests at exactly +/-29, 30 and 31 s"), run in BOTH directions
// because the bound here is symmetric -- see the file header on why PB-GW-2's
// one-sidedness does not transfer.
//
// The RTT is zero so the bracket collapses to a point and the boundary is exact; the RTT
// allowance is exercised separately below. Exactly AT the bound is accepted, matching the
// strict-inequality semantics S7b pinned for the age bound, so the two comparisons in the
// tree agree at their edges.
func TestS11Skew_BoundaryAtTwentyNineThirtyAndThirtyOne(t *testing.T) {
	cases := []struct {
		skew    time.Duration
		wantErr bool
	}{
		{29 * time.Second, false},
		{30 * time.Second, false},
		{31 * time.Second, true},
		{-29 * time.Second, false},
		{-30 * time.Second, false},
		{-31 * time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.skew.String(), func(t *testing.T) {
			clk := s11NewSkewClock(tc.skew)
			m := NewSkewMonitor(clk.now)

			got, err := s11Measure(m, clk, "op-boundary", 0)
			if tc.wantErr {
				if !errors.Is(err, ErrClockSkew) {
					t.Fatalf("skew %v: err = %v, want ErrClockSkew (§6.0 bounds skew at +/-%v)", tc.skew, err, MaxClockSkew)
				}
			} else if err != nil {
				t.Fatalf("skew %v: err = %v, want accepted -- refusing inside the budget refuses a phone the machine would have served", tc.skew, err)
			}

			if !got.Known {
				t.Fatalf("skew %v: Skew.Known = false after a completed round trip; the measurement must be reported whether or not it is refused", tc.skew)
			}
			// The MEASUREMENT is reported for the machine, so the sign convention is
			// "machine minus phone": a phone that is AHEAD yields a negative offset.
			if want := -tc.skew; got.Offset != want {
				t.Fatalf("skew %v: Skew.Offset = %v, want %v (machine clock minus phone clock)", tc.skew, got.Offset, want)
			}
			if got != m.Skew() {
				t.Fatalf("Skew() = %+v, does not match the value Observe returned (%+v)", m.Skew(), got)
			}
		})
	}
}

// TestS11Skew_CheckMirrorsTheLastMeasurement. Check is the gate a command-authoring path
// consults; it must agree with Observe, or a command is refused on one rule and admitted
// on another.
func TestS11Skew_CheckMirrorsTheLastMeasurement(t *testing.T) {
	clk := s11NewSkewClock(45 * time.Second)
	m := NewSkewMonitor(clk.now)
	if _, err := s11Measure(m, clk, "op-a", 0); !errors.Is(err, ErrClockSkew) {
		t.Fatalf("setup: a 45s skew was not refused: %v", err)
	}
	if err := m.Check(); !errors.Is(err, ErrClockSkew) {
		t.Fatalf("Check after a 45s measurement = %v, want ErrClockSkew", err)
	}

	// MUTATION CONTROL: the user fixes the clock; the gate must reopen. A latched
	// refusal is the permanent brick PB-STATE-10 forbids elsewhere and would be no
	// better here.
	clk.skew = 2 * time.Second
	if _, err := s11Measure(m, clk, "op-b", 0); err != nil {
		t.Fatalf("after the clock was corrected: err = %v, want nil", err)
	}
	if err := m.Check(); err != nil {
		t.Fatalf("Check after the clock was corrected = %v, want nil -- a latched skew refusal cannot be recovered from", err)
	}
}

// ---------------------------------------------------------------------------
// PB-TIME-3 -- the RTT allowance
// ---------------------------------------------------------------------------

// TestS11Skew_RelayDelayIsNotMistakenForSkew is the RTT allowance doing its job. A phone
// with a PERFECT clock behind a slow relay must not be told its clock is wrong: the reply
// it reads is stamped at some point inside the round trip, so a naive
// "machineStamp - now" reads as up to a full RTT of skew.
//
// Without the bracket a 70-second round trip -- an ordinary thing on a bad mobile link,
// and trivially arrangeable by an untrusted relay -- would refuse every command on a
// correct clock. That is a denial of service the relay gets for free.
func TestS11Skew_RelayDelayIsNotMistakenForSkew(t *testing.T) {
	for _, rtt := range []time.Duration{time.Second, 40 * time.Second, 70 * time.Second, 5 * time.Minute} {
		t.Run(rtt.String(), func(t *testing.T) {
			clk := s11NewSkewClock(0) // a PERFECT phone clock
			m := NewSkewMonitor(clk.now)

			got, err := s11Measure(m, clk, "op-rtt", rtt)
			if err != nil {
				t.Fatalf("rtt %v on a perfectly-set clock: err = %v, want nil -- the relay's delay was mistaken for skew, so a hostile relay can refuse every command by delaying replies", rtt, err)
			}
			if got.RTT != rtt {
				t.Fatalf("rtt %v: Skew.RTT = %v; the uncertainty must be reported, or a caller cannot tell a tight measurement from a useless one", rtt, got.RTT)
			}
		})
	}
}

// TestS11Skew_RealSkewIsStillCaughtThroughASlowRelay is the mutation that keeps the
// allowance from swallowing the requirement. If the bracket alone decided, a relay could
// hide any skew by being slow enough -- so a genuinely wrong clock must still be caught
// when the bracket is entirely outside the bound.
func TestS11Skew_RealSkewIsStillCaughtThroughASlowRelay(t *testing.T) {
	clk := s11NewSkewClock(-5 * time.Minute) // the handset is five minutes SLOW
	m := NewSkewMonitor(clk.now)

	_, err := s11Measure(m, clk, "op-slow", 2*time.Second)
	if !errors.Is(err, ErrClockSkew) {
		t.Fatalf("a 5-minute-slow phone behind a 2s relay: err = %v, want ErrClockSkew -- this is the handset PB-TIME-1 describes, whose every command fails with an opaque \"not authorized\"", err)
	}
}

// TestS11Skew_ABackwardClockStepIsDiscardedNotMeasured is the monotonic-vs-wall split,
// asserted through its only observable consequence. If T2 lands before T1 -- an NTP
// correction between the send and the reply, which is precisely the event a
// skew-detection feature provokes -- the RTT is not a duration and the bracket is
// meaningless. The sample must be dropped, not folded in.
func TestS11Skew_ABackwardClockStepIsDiscardedNotMeasured(t *testing.T) {
	clk := s11NewSkewClock(0)
	m := NewSkewMonitor(clk.now)

	// A good measurement first, so there is a known-good value to protect.
	if _, err := s11Measure(m, clk, "op-good", 20*time.Millisecond); err != nil {
		t.Fatalf("setup: %v", err)
	}
	good := m.Skew()
	if !good.Known {
		t.Fatal("setup: no measurement was recorded, so \"the bad sample did not overwrite it\" is vacuously true")
	}

	// Now the handset's wall clock jumps BACKWARD by a minute between send and reply.
	m.Sent("op-step")
	stamp := clk.machineStamp()
	clk.skew -= time.Minute
	got, err := m.Observe("op-step", stamp)
	if err != nil {
		t.Fatalf("a backward clock step produced err = %v; the sample is unusable and must be DISCARDED, not turned into a skew verdict", err)
	}
	if got != good || m.Skew() != good {
		t.Fatalf("a backward clock step overwrote the last good measurement (%+v -> %+v); a negative RTT is not a measurement", good, m.Skew())
	}
}

// ---------------------------------------------------------------------------
// PB-TIME-3 -- the relay is not an authority, and offline behaviour
// ---------------------------------------------------------------------------

// TestS11Skew_AnUncorrelatedStampIsIgnored is half of "the relay cannot influence the
// phone's notion of machine time". A stamp the phone cannot tie to a send IT made has no
// T1, so it has no bracket -- and folding it in with an assumed delay is exactly how a
// relay steers the estimate: retain a frame for ten minutes, deliver it, and the phone
// concludes its clock is ten minutes fast.
func TestS11Skew_AnUncorrelatedStampIsIgnored(t *testing.T) {
	clk := s11NewSkewClock(0)
	m := NewSkewMonitor(clk.now)
	if _, err := s11Measure(m, clk, "op-good", 20*time.Millisecond); err != nil {
		t.Fatalf("setup: %v", err)
	}
	good := m.Skew()
	if !good.Known {
		t.Fatal("setup: no measurement was recorded, so \"the uncorrelated stamp did not move it\" is vacuously true")
	}

	// A retained frame, stamped ten minutes ago, correlated with nothing.
	got, err := m.Observe("op-never-sent", clk.machineStamp().Add(-10*time.Minute))
	if err != nil {
		t.Fatalf("an uncorrelated stamp returned err = %v; it must simply be ignored", err)
	}
	if got != good || m.Skew() != good {
		t.Fatalf("an uncorrelated retained stamp moved the estimate (%+v -> %+v) -- a retaining relay can then steer the phone's notion of machine time at will", good, m.Skew())
	}
}

// TestS11Skew_OfflineIsNotSkewed is PB-TIME-3's "defined offline behaviour", and the
// deadlock it has to avoid is structural: the ONLY authenticated machine time the phone
// can get is stamped on a reply, and a reply only exists in answer to a command. So a gate
// that refused commands until the skew was known would refuse the command that would have
// measured it -- the phone would never send anything again, and the symptom would be
// indistinguishable from a dead relay.
//
// Unknown is therefore NOT a refusal. The daemon's own ExpiresAt check remains the
// backstop for a phone that has never measured.
func TestS11Skew_OfflineIsNotSkewed(t *testing.T) {
	clk := s11NewSkewClock(10 * time.Minute) // wildly wrong, and unmeasured
	m := NewSkewMonitor(clk.now)

	if s := m.Skew(); s.Known {
		t.Fatalf("Skew() = %+v before any measurement, want Known == false", s)
	}
	if err := m.Check(); err != nil {
		t.Fatalf("Check() with no measurement = %v, want nil -- the first machine timestamp can only ride a reply to a command, so refusing commands until the skew is known is a permanent deadlock", err)
	}

	// MUTATION CONTROL: once a measurement DOES land, the same gate refuses.
	if _, err := s11Measure(m, clk, "op-1", 0); !errors.Is(err, ErrClockSkew) {
		t.Fatalf("a 10-minute skew was not refused once measured: %v", err)
	}
	if err := m.Check(); !errors.Is(err, ErrClockSkew) {
		t.Fatalf("Check() after a 10-minute measurement = %v, want ErrClockSkew -- the permissive offline rule must not also make the online rule vacuous", err)
	}
}

// TestS11Skew_TheMonitorIsFedOnlyFromAuthenticatedFrames is the other half of "the relay
// cannot influence the phone's notion of machine time", asserted on the PRODUCTION path
// rather than on the monitor in isolation.
//
// IssuedAt is AAD-covered, so a relay that rewrites it cannot also produce a valid tag.
// The property that has to hold is therefore that the phone reads the stamp ONLY from a
// frame the AEAD has vouched for -- never from the parsed header of a frame that failed to
// open. This test tampers with the header of a real, correctly-sealed reply and asserts
// both that the frame is refused and that the estimate did not move.
func TestS11Skew_TheMonitorIsFedOnlyFromAuthenticatedFrames(t *testing.T) {
	c, key, epoch := s11ResumedCore(t)

	// A good round trip first, through the real inbound path.
	c.SkewMonitor().Sent(s11TakeOp)
	raw := s11SealReply(t, key, epoch, 1, s11Confirmation(nil))
	if _, err := c.Router().AcceptCommit(raw, 1); err != nil {
		t.Fatalf("AcceptCommit of a real machine-sealed reply: %v -- see the PB-TIME-2 tests: SealControlReply must stamp IssuedAt", err)
	}
	good := c.SkewMonitor().Skew()
	if !good.Known {
		t.Fatalf("SkewMonitor().Skew() = %+v after a correlated reply arrived on the real inbound path; the monitor is not wired to it, so the phone can never measure skew at all", good)
	}

	// A tampered IssuedAt: the relay shifts the machine's stamp an hour into the past.
	c.SkewMonitor().Sent("op-tampered")
	tampered := s11TamperIssuedAt(t, s11SealReply(t, key, epoch, 2, s11ReplyFor("op-tampered")), -time.Hour)
	if _, err := c.Router().AcceptCommit(tampered, 2); err == nil {
		t.Fatal("a frame whose AAD-covered IssuedAt was rewritten by the relay was ACCEPTED; the AEAD must refuse it")
	}
	if now := c.SkewMonitor().Skew(); now != good {
		t.Fatalf("the tampered frame moved the skew estimate (%+v -> %+v) -- the stamp was read before the AEAD vouched for it, so the untrusted relay is the authority on machine time", good, now)
	}
}
