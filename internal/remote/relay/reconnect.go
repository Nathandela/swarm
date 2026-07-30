package relay

import (
	"math/rand/v2"
	"time"
)

// The reconnect backoff between relay dial attempts: ADR-007 section 6.0's numeric budget
// (initial delay, growth factor, ceiling, jitter fraction), and the D9 clause that binds
// it -- "Client reconnect backoff + jitter on BOTH hops".
//
// A client that reconnects hard exhausts the relay's per-target quota, which is shared
// with the traffic it is trying to carry; the exponential growth is what keeps a stuck
// relay from being redialled at a fixed high rate for the life of the process, and the
// jitter keeps a fleet reconnecting after a relay restart from arriving as one herd.
// These are the committee's approved numbers, not tuning knobs: a change here is a change
// to the budget.
//
// IT LIVES IN THIS PACKAGE BECAUSE BOTH HOPS DIAL THROUGH IT AND NOTHING ELSE IS COMMON TO
// THEM. The schedule shipped first as unexported constants inside mobile/relay.go, which
// made it the PHONE's schedule -- and when the gateway hop finally got a reconnect there
// was nowhere to take the numbers from but a second copy. A budget that exists twice is
// two budgets. relay is the one package both the handset facade and the machine sidecar
// already import (it is on mobile's PB-BIND-0 allowlist), so putting the schedule here
// costs the bound handset no new dependency edge and gives the clause one home.
//
// Section 6.0's transcription of these values is asserted directly, against literals,
// in mobile/pbnet4_backoff_test.go.
const (
	ReconnectInitialDelay = 500 * time.Millisecond
	ReconnectFactor       = 2
	ReconnectCeiling      = 30 * time.Second
	ReconnectJitter       = 0.20
)

// ReconnectBackoffBase returns the un-jittered delay before dial attempt n, 1-based:
// ReconnectInitialDelay doubling on every failed attempt, never exceeding ReconnectCeiling.
func ReconnectBackoffBase(attempt int) time.Duration {
	d := ReconnectInitialDelay
	for i := 1; i < attempt; i++ {
		d *= ReconnectFactor
		if d >= ReconnectCeiling {
			return ReconnectCeiling
		}
	}
	return d
}

// ReconnectJittered spreads base by +/-ReconnectJitter. frac is a value in [-1, 1] -- taken
// as a parameter, rather than drawn here, so the spread itself is testable without a random
// source.
func ReconnectJittered(base time.Duration, frac float64) time.Duration {
	return base + time.Duration(frac*ReconnectJitter*float64(base))
}

// ReconnectBackoff tracks consecutive failed dial attempts across one reconnect generation
// and computes each retry delay.
//
// RESET IS THE CALLER'S DECISION, AND IT IS NOT "the dial succeeded". A relay that
// completes the websocket handshake and then answers nothing keeps the socket up while
// every call reaches its deadline, so a backoff reset by connection alone turns that into
// a fixed-rate redial cycle the adversary drives for free. Reset on evidence of PROGRESS
// -- traffic that actually crossed the link -- which is why this type has no opinion about
// when that happened.
type ReconnectBackoff struct {
	attempt int
	frac    func() float64 // returns a value in [-1, 1]; overridden by tests
}

// NewReconnectBackoff returns a backoff at its initial delay, jittered from the default
// random source.
func NewReconnectBackoff() *ReconnectBackoff {
	return &ReconnectBackoff{frac: func() float64 { return rand.Float64()*2 - 1 }}
}

// Next returns the delay before the next dial attempt and advances the backoff state.
func (b *ReconnectBackoff) Next() time.Duration {
	b.attempt++
	return ReconnectJittered(ReconnectBackoffBase(b.attempt), b.frac())
}

// Reset returns the backoff to its initial state, as if no attempt had yet failed.
func (b *ReconnectBackoff) Reset() {
	b.attempt = 0
}

// Attempt is how many delays have been drawn since the last Reset. It exists for the
// tests that assert the SEQUENCE rather than a single delay.
func (b *ReconnectBackoff) Attempt() int { return b.attempt }
