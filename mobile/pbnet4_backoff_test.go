package swarmmobile

// PB-NET-4's reconnect backoff (ADR-007 section 6.0's numeric budget, table row "Reconnect
// backoff"): initial 500 ms, factor 2, ceiling 30 s, jitter +/-20%, reset to initial on a
// successful connection.
//
// THE DEFECT THIS IS WRITTEN AGAINST (ADR-007 B94(3)). Those numbers existed only in
// internal/remote/transport, which had zero production callers and has since been deleted --
// so the specified behaviour existed nowhere the shipped phone could reach. The shipped
// reconnect loop (mobile/relay.go's App.run) waited a FIXED `250 * time.Millisecond` between
// every dial attempt: no growth, no ceiling, no jitter. Setting that fixed delay to three
// hours left every PB-NET-4-named test passing, because nothing asserted the SHAPE of the
// delay -- only that a delay of some kind existed.
//
// So every value below is TRANSCRIBED from section 6.0's table, not read from the constant it
// is checking: a mutation to reconnectInitialDelay, reconnectFactor, reconnectCeiling or
// reconnectJitter changes what these tests expect versus what the code returns, and the test
// fails. Nothing here sleeps or measures wall-clock time -- the delay computation is a pure
// function of the attempt count (and, for jitter, an injected fraction), so the sequence is
// asserted directly.

import (
	"testing"
	"time"
)

// TestPBNET4_BackoffBaseDoublesFromFiveHundredMillisecondsToAThirtySecondCeiling pins the
// un-jittered growth curve: 500ms, 1s, 2s, 4s, 8s, 16s, then capped at 30s forever after,
// because 500ms * 2^6 = 32s overshoots the stated ceiling.
func TestPBNET4_BackoffBaseDoublesFromFiveHundredMillisecondsToAThirtySecondCeiling(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 500 * time.Millisecond},
		{2, 1 * time.Second},
		{3, 2 * time.Second},
		{4, 4 * time.Second},
		{5, 8 * time.Second},
		{6, 16 * time.Second},
		{7, 30 * time.Second}, // 32s uncapped; the ceiling must bite here
		{8, 30 * time.Second}, // stays at the ceiling, does not keep doubling
		{20, 30 * time.Second},
	}
	for _, c := range cases {
		if got := reconnectBackoffBase(c.attempt); got != c.want {
			t.Errorf("reconnectBackoffBase(%d) = %v, want %v (section 6.0: initial 500ms, "+
				"factor 2, ceiling 30s)", c.attempt, got, c.want)
		}
	}
}

// TestPBNET4_JitterIsPlusMinusTwentyPercentOfBase pins the jitter band's edges and midpoint
// against literal values, so a change to the 20% figure or to the direction of the spread is
// caught without trusting the constant that would also have changed.
func TestPBNET4_JitterIsPlusMinusTwentyPercentOfBase(t *testing.T) {
	cases := []struct {
		base time.Duration
		frac float64
		want time.Duration
	}{
		{500 * time.Millisecond, 0, 500 * time.Millisecond},
		{500 * time.Millisecond, 1, 600 * time.Millisecond},  // +20%
		{500 * time.Millisecond, -1, 400 * time.Millisecond}, // -20%
		{30 * time.Second, 1, 36 * time.Second},              // +20% at the ceiling
		{30 * time.Second, -1, 24 * time.Second},             // -20% at the ceiling
		{1 * time.Second, 0.5, 1100 * time.Millisecond},      // +10%
	}
	for _, c := range cases {
		if got := reconnectJittered(c.base, c.frac); got != c.want {
			t.Errorf("reconnectJittered(%v, %v) = %v, want %v (section 6.0: jitter +/-20%%)",
				c.base, c.frac, got, c.want)
		}
	}
}

// TestPBNET4_DefaultJitterFractionStaysWithinTheStatedBand exercises the RANDOM source
// production actually uses (newReconnectBackoff's default frac func), not just the pure
// jitter math, so a mutation that wires in an unbounded or differently-scaled random source
// is caught too. No sleeping: this calls next() many times back to back.
func TestPBNET4_DefaultJitterFractionStaysWithinTheStatedBand(t *testing.T) {
	rb := newReconnectBackoff()
	for i := 0; i < 500; i++ {
		base := reconnectBackoffBase(rb.attempt + 1)
		lo := time.Duration(float64(base) * 0.8)
		hi := time.Duration(float64(base) * 1.2)
		got := rb.next()
		if got < lo || got > hi {
			t.Fatalf("reconnectBackoff.next() attempt %d = %v, want within [%v, %v] "+
				"(base %v +/-20%%)", i, got, lo, hi, base)
		}
	}
}

// TestPBNET4_BackoffResetsToInitialOnReset pins reset()'s contract: the very next delay after
// a reset is the same as the very first delay a fresh backoff produces, not a continuation of
// the growth that came before it. Jitter is pinned to zero (frac always returns 0) so the
// sequence can be asserted exactly.
func TestPBNET4_BackoffResetsToInitialOnReset(t *testing.T) {
	rb := &reconnectBackoff{frac: func() float64 { return 0 }}

	got := []time.Duration{rb.next(), rb.next(), rb.next()}
	want := []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("before reset, next() call %d = %v, want %v", i+1, got[i], want[i])
		}
	}

	rb.reset()

	if got := rb.next(); got != 500*time.Millisecond {
		t.Errorf("after reset(), next() = %v, want %v -- reset must return the backoff to the "+
			"INITIAL delay, not merely stop it growing", got, 500*time.Millisecond)
	}
}
