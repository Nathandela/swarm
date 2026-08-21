// R9 (remote-control-product-playbook.md section 10): "Backoff includes production
// jitter and is exercised with the production random source enabled." Every other
// backoff test injects frac; these are the only tests that construct
// NewReconnectBackoff and draw from the real random source, so a regression in the
// production draw (a degenerate source, a frac outside [-1, 1], a lost jitter term,
// a broken growth schedule) fails here and nowhere else.
//
// The assertions state the documented contract from reconnect.go / ADR-007 section
// 6.0 -- delays spread by at most +/-ReconnectJitter around ReconnectBackoffBase,
// the base doubling from ReconnectInitialDelay and holding at ReconnectCeiling --
// not a reimplementation of the jitter formula.
package relay

import (
	"testing"
	"time"
)

// jitterEnvelope returns the documented spread bounds around the un-jittered base:
// base +/- ReconnectJitter of base.
func jitterEnvelope(base time.Duration) (lo, hi time.Duration) {
	spread := time.Duration(ReconnectJitter * float64(base))
	return base - spread, base + spread
}

// TestR9ProductionJitterWithinSpreadBounds draws many delays from the production
// random source and asserts every one lands inside the documented +/-ReconnectJitter
// envelope around the attempt's base -- first many draws at a fixed attempt level,
// then one draw per attempt level up into the ceiling region.
func TestR9ProductionJitterWithinSpreadBounds(t *testing.T) {
	b := NewReconnectBackoff()
	base := ReconnectBackoffBase(1)
	lo, hi := jitterEnvelope(base)
	const draws = 1000
	for i := 0; i < draws; i++ {
		b.Reset()
		if d := b.Next(); d < lo || d > hi {
			t.Fatalf("attempt 1 draw %d: delay %v outside documented envelope [%v, %v] around base %v", i, d, lo, hi, base)
		}
	}

	// Across attempt levels, past the point where the base reaches the ceiling
	// (500ms doubling reaches 30s within 8 attempts).
	b = NewReconnectBackoff()
	for attempt := 1; attempt <= 12; attempt++ {
		base := ReconnectBackoffBase(attempt)
		lo, hi := jitterEnvelope(base)
		if d := b.Next(); d < lo || d > hi {
			t.Fatalf("attempt %d: delay %v outside documented envelope [%v, %v] around base %v", attempt, d, lo, hi, base)
		}
	}
}

// TestR9ProductionJitterSpreadIsNotDegenerate asserts the production source
// actually spreads the delays: a source that always returns the same frac (or one
// that was silently disconnected from the delay) collapses every attempt-1 delay
// to a single value.
//
// False-failure probability: each attempt-1 delay is 500ms plus a jitter term
// uniform over roughly 2*10^8 distinct nanosecond values (+/-100ms). Over 1000
// draws the expected number of colliding pairs is ~1000^2/(2*2*10^8) ~= 0.0025, so
// the chance of at most draws/2 distinct values is far below 1e-100 -- a failure
// here means the source is degenerate, not unlucky.
func TestR9ProductionJitterSpreadIsNotDegenerate(t *testing.T) {
	b := NewReconnectBackoff()
	const draws = 1000
	seen := make(map[time.Duration]struct{}, draws)
	for i := 0; i < draws; i++ {
		b.Reset()
		seen[b.Next()] = struct{}{}
	}
	if len(seen) <= draws/2 {
		t.Fatalf("production jitter is degenerate: %d draws produced only %d distinct delays", draws, len(seen))
	}
}

// TestR9ReconnectBaseGrowsMonotonicallyToCeiling asserts the documented growth
// schedule: the base starts at ReconnectInitialDelay, grows strictly on every
// failed attempt until it reaches ReconnectCeiling, then holds there and never
// exceeds it.
func TestR9ReconnectBaseGrowsMonotonicallyToCeiling(t *testing.T) {
	if got := ReconnectBackoffBase(1); got != ReconnectInitialDelay {
		t.Fatalf("base at attempt 1 = %v, want ReconnectInitialDelay %v", got, ReconnectInitialDelay)
	}
	prev := ReconnectBackoffBase(1)
	reached := prev == ReconnectCeiling
	for attempt := 2; attempt <= 20; attempt++ {
		cur := ReconnectBackoffBase(attempt)
		if cur > ReconnectCeiling {
			t.Fatalf("base at attempt %d = %v exceeds ReconnectCeiling %v", attempt, cur, ReconnectCeiling)
		}
		if reached && cur != ReconnectCeiling {
			t.Fatalf("base at attempt %d = %v fell below the ceiling after reaching it", attempt, cur)
		}
		if !reached && cur <= prev {
			t.Fatalf("base at attempt %d = %v did not grow from %v before reaching the ceiling", attempt, cur, prev)
		}
		if cur == ReconnectCeiling {
			reached = true
		}
		prev = cur
	}
	if !reached {
		t.Fatalf("base never reached ReconnectCeiling %v within 20 attempts", ReconnectCeiling)
	}
}
