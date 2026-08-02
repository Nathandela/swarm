package phonecore

// PB-TIME-1 / PB-TIME-3 -- clock-skew detection, bounded at +/-30 s and surfaced as its own
// error.
//
// A handset two minutes slow signs ExpiresAt = now + TTL, the daemon refuses anything past
// its own horizon, and EVERY command fails with the daemon's opaque "not authorized". The
// action the user needs is "fix the clock", not "re-pair", so the refusal has to say so.
//
// THE PROTOCOL, and why no new verb. The relay may not be the authority (it is untrusted)
// and an unauthenticated wall clock may not be either. What is left is already on the wire:
// every machine -> phone envelope carries an AAD-covered IssuedAt, so an OPENED frame's
// stamp is machine-authenticated by construction. Sent/Observe bracket it against the
// phone's own send and receive instants.
//
// THE RTT ALLOWANCE. With the phone's send at T1, the machine's stamp Tm and the phone's
// receive at T2, and both one-way delays non-negative:
//
//	Tm - T2  <=  offset  <=  Tm - T1        (width = RTT)
//
// so the estimate is Tm - (T1+T2)/2 with uncertainty +/-RTT/2. Skew is reported ONLY when
// the WHOLE bracket lies outside the bound. That is what stops an untrusted relay from
// refusing every command by being slow: delay WIDENS the bracket, it does not move it.
//
// MONOTONIC vs WALL. The RTT is a duration between two readings of one clock and is
// measured monotonically; the OFFSET is a difference between two WALL clocks and cannot be.
// A wall-clock step between T1 and T2 -- an NTP correction, the very event this feature
// provokes -- yields a NEGATIVE RTT, which is not a measurement, so the sample is discarded
// rather than folded in. Zero RTT is kept: an injected clock produces it, and it is the
// tightest bracket there is, not a broken one.
//
// THE BOUND IS SYMMETRIC, unlike the bounded-age check next door. That one is an
// anti-replay backstop against a relay that can only make frames OLDER; this is a
// MEASUREMENT, and a phone 45 s fast is exactly as broken as one 45 s slow.

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// MaxClockSkew is §6.0's budget for the offset between the phone's wall clock and the
// machine's.
const MaxClockSkew = 30 * time.Second

// ErrClockSkew is PB-TIME-1's distinct, user-legible refusal. It deliberately does not read
// like the authorization failure it exists to replace: the user has to fix a clock, and
// "not authorized" tells them to do the wrong thing.
var ErrClockSkew = errors.New("phonecore: this device's clock is out of sync with the machine")

// Skew is one measurement. Offset is MACHINE MINUS PHONE, so a phone that runs AHEAD yields
// a negative offset; RTT is the round trip the measurement rode, i.e. the width of the
// uncertainty bracket. Known is false until a round trip has completed.
type Skew struct {
	Offset time.Duration
	RTT    time.Duration
	Known  bool
}

// SkewMonitor brackets the machine's authenticated timestamps against the phone's own send
// and receive instants. Safe for concurrent use: commands are minted on one thread and
// replies land on the drain.
type SkewMonitor struct {
	now func() time.Time

	mu      sync.Mutex
	sent    map[string]time.Time // operation id -> T1
	last    Skew
	lastErr error
}

// NewSkewMonitor returns a monitor with no measurement yet.
func NewSkewMonitor(now func() time.Time) *SkewMonitor {
	if now == nil {
		now = time.Now
	}
	return &SkewMonitor{now: now, sent: map[string]time.Time{}}
}

// Sent records T1 for an operation, at the moment the phone hands it to the relay. Without
// it every machine stamp arrives uncorrelated and is ignored by design, so the phone can
// never measure skew at all.
func (m *SkewMonitor) Sent(operationID string) {
	if operationID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	// An op whose reply never lands would otherwise pin its T1 forever. A reply older than
	// InboundMaxAge is refused by the receiver anyway, so its bracket can never complete.
	for op, at := range m.sent {
		if now.Sub(at) > InboundMaxAge {
			delete(m.sent, op)
		}
	}
	m.sent[operationID] = now
}

// Observe closes the bracket with the machine's AUTHENTICATED timestamp from the reply to
// operationID, and returns the current measurement plus the verdict.
//
// A stamp the phone cannot tie to a send it made has no T1, so it has no bracket: it is
// IGNORED, never folded in with an assumed delay. That is the half that keeps a retaining
// relay from steering the estimate -- hold a frame for ten minutes, deliver it, and an
// assuming phone concludes its clock is ten minutes fast.
func (m *SkewMonitor) Observe(operationID string, machineTime time.Time) (Skew, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	t1, ok := m.sent[operationID]
	if !ok {
		return m.last, m.lastErr
	}
	delete(m.sent, operationID)

	t2 := m.now()
	rtt := t2.Sub(t1)
	if rtt < 0 {
		// The phone's wall clock stepped backwards between the send and the reply. The
		// "round trip" is not a duration and the bracket is meaningless.
		return m.last, m.lastErr
	}

	upper := machineTime.Sub(t1) // Tm - T1
	lower := machineTime.Sub(t2) // Tm - T2
	m.last = Skew{Offset: upper - rtt/2, RTT: rtt, Known: true}
	m.lastErr = nil
	if lower > MaxClockSkew || upper < -MaxClockSkew {
		m.lastErr = fmt.Errorf("%w: measured %v off (machine minus phone), outside the +/-%v budget",
			ErrClockSkew, m.last.Offset, MaxClockSkew)
	}
	return m.last, m.lastErr
}

// Skew is the last completed measurement.
func (m *SkewMonitor) Skew() Skew {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

// Check is the last measurement's VERDICT, for reporting. It mirrors the last Observe
// exactly, so the user is never told one thing while the phone believes another.
//
// IT IS NOT A GATE, and no command-authoring path may make it one. The only authenticated
// machine time the phone can get rides a reply, and a reply only exists in answer to a
// command -- so refusing commands on a bad verdict refuses the command that would have
// re-measured it, and the refusal outlives the broken clock it was reporting: the user
// corrects their clock exactly as the error said and every op stays refused until the
// process restarts. The daemon's own ExpiresAt check is the enforcement; this is the
// explanation. Its one production caller is the phone's reply handler, which reports the
// verdict on the event plane (mobile/relay.go reportSkew), and
// mobile/s11r_livesend_test.go fences the command path against calling it at all.
//
// UNKNOWN IS LIKEWISE NEVER A VERDICT: a phone that has never measured reports nothing
// rather than a fault it cannot substantiate, and a bad verdict is never latched -- the
// next good measurement clears it.
func (m *SkewMonitor) Check() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr
}
