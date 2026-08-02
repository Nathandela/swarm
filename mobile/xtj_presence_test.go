package swarmmobile

// Bead agents-tracker-xtj -- FAILING-FIRST (TDD RED, GG-5) for the presence half of the
// Machines tab. Compile-level RED: presenceCache and its constants do not exist yet.
//
// THE DEFECT THE BEAD NAMES. MachinePane.presence is App.Presence, which is a relay
// round-trip -- cl.Presence(context.Background(), target), app.go:906. The facade sets no
// deadline of its own; the transport's DefaultCallTimeout does, at 10 s (relay/client.go:148).
// Ten seconds on the main thread is an ANR, and android/unbound-verbs.tsv:52 ledgered the verb
// unbound in advance for exactly this: "calling it per redraw would issue a relay RPC per
// journal record. It needs a screen that polls it on its own cadence, off the main thread."
//
// THE CADENCE GOES IN GO, NOT KOTLIN, and this file pins the state it keeps. The phone
// already owns three facts learned on the relay goroutine and read O(1) from the UI thread --
// clockVerdict (app.go:145), killSwitch (app.go:127), connState (relay.go:232) -- and every
// one of them is a locked field plus an event on transition. Presence is the fourth. What is
// new here is that presence can go STALE in a way those cannot: it is somebody else's opinion,
// fetched on a timer, so a reading has an AGE and the screen has to be able to see it.
//
// WHY App.Presence ITSELF IS NOT BEING TURNED INTO THIS. mobile/conformance/pbnet7_leak_test.go
// calls Presence() precisely because it is a real exchange -- "so the cycle retires a used
// connection rather than an idle one: this is the request/reply path, the one that holds the
// exchange lock and now carries a deadline of its own." A cached Presence() leaves that test
// green while it stops exercising what it exists to exercise. The live verb stays; the screen
// reads the cache.
//
// This file contains NO implementation.

import (
	"testing"
	"time"
)

// xtjClock is the injected clock. No assertion here reads the wall clock: an age is a
// property of the algorithm, not of this machine.
type xtjClock struct{ t time.Time }

func newXTJClock() *xtjClock                { return &xtjClock{t: time.UnixMilli(1_784_000_000_000)} }
func (c *xtjClock) now() time.Time          { return c.t }
func (c *xtjClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// TestXTJPresence_ACacheThatHasObservedNothingIsUnknownAndNotOnline is the first thing a
// screen sees: the app has just started, the prober has not run, and the pane must render
// "unknown" rather than an empty string it will style as offline or, worse, a zero value some
// mapping turns into online. There is also no observation to be aged, so the instant is zero.
func TestXTJPresence_ACacheThatHasObservedNothingIsUnknownAndNotOnline(t *testing.T) {
	clk := newXTJClock()
	p := newPresenceCache(clk.now)

	state, at := p.read()
	if state != presenceUnknown {
		t.Fatalf("a cache that has observed nothing reads %q, want %q -- a screen must not be handed a blank to interpret", state, presenceUnknown)
	}
	if !at.IsZero() {
		t.Fatalf("the observation instant is %v, want the zero time -- nothing has been observed, and dating that reading now would make an absence look fresh", at)
	}
}

// TestXTJPresence_AnObservationIsReadBackWithItsInstant is the cache doing its job: what the
// prober learned on the relay goroutine is what the UI thread reads, with the age that makes
// it judgeable.
func TestXTJPresence_AnObservationIsReadBackWithItsInstant(t *testing.T) {
	clk := newXTJClock()
	p := newPresenceCache(clk.now)

	if changed := p.observe("online"); !changed {
		t.Fatal("the first observation reported no change; a screen opened before it must be told, and the transition is what tells it")
	}
	state, at := p.read()
	if state != "online" {
		t.Fatalf("read %q, want %q", state, "online")
	}
	if !at.Equal(clk.now()) {
		t.Fatalf("the observation is dated %v, want %v", at, clk.now())
	}
}

// TestXTJPresence_ARepeatObservationIsNotATransitionButIsStillFresher is the pair of
// properties that make the age worth carrying.
//
// The state did not change, so nothing may be emitted -- the event plane is bounded at 256 and
// drops OLDEST (events.go), so a presence event every 15 s forever would push real events off
// the queue. But the READING is newer, and a cache that kept the old instant would make a
// live machine look progressively staler until it was indistinguishable from one that had
// stopped answering.
func TestXTJPresence_ARepeatObservationIsNotATransitionButIsStillFresher(t *testing.T) {
	clk := newXTJClock()
	p := newPresenceCache(clk.now)
	p.observe("online")
	first := clk.now()

	clk.advance(presenceInterval)
	if changed := p.observe("online"); changed {
		t.Fatal("observing the SAME state reported a change; an event every cadence tick would push real events off the bounded callback queue")
	}
	_, at := p.read()
	if !at.After(first) {
		t.Fatalf("the observation is still dated %v after a second reading at %v -- an unchanged machine would look staler every tick", at, clk.now())
	}
}

// TestXTJPresence_LosingTheLinkGoesUnknownRatherThanHoldingOnline is the honesty clause, and
// it is PB-APP-11's lesson applied to a cache: the phone stops being able to ASK the moment
// the connection drops, and a value it can no longer refresh must not go on saying "online".
// The transition is reported, because a screen already open is the one that would otherwise
// keep rendering the stale answer.
func TestXTJPresence_LosingTheLinkGoesUnknownRatherThanHoldingOnline(t *testing.T) {
	clk := newXTJClock()
	p := newPresenceCache(clk.now)
	p.observe("online")

	clk.advance(2 * presenceInterval)
	if changed := p.forget(); !changed {
		t.Fatal("losing the link reported no change while the cache said \"online\"; the screen that is already open never learns the answer went unknowable")
	}
	state, at := p.read()
	if state != presenceUnknown {
		t.Fatalf("after the link dropped the cache reads %q, want %q -- the phone cannot ask, so it cannot answer", state, presenceUnknown)
	}
	if !at.Equal(clk.now()) {
		t.Fatalf("the unknown reading is dated %v, want %v -- when the answer became unknowable is itself the freshest thing known about it", at, clk.now())
	}

	if changed := p.forget(); changed {
		t.Fatal("a second link loss reported a change from unknown to unknown; a reconnect loop would emit one event per attempt")
	}
}

// TestXTJPresence_TheCadenceIsDerivedFromTheRelaysOwnPresenceWindow pins the interval to its
// derivation rather than to an implementer's taste. The relay declares a party offline
// PresenceTimeout after a gateway drop -- 30 s by default (internal/remote/relay/config.go) --
// so its answer cannot change faster than its own sweep, and sampling at half that period
// observes a transition within one window. It is written as a literal here because the
// DEPLOYED relay's config is not readable from a handset; the same convention the input
// coalescer uses for MailboxAppendPerMin.
//
// The budget it has to fit is OpsPerMin, which meters `presence` (relay/config.go:39-44) at
// 600/min shared with mailbox_read/ack: four polls a minute is noise against the drain.
func TestXTJPresence_TheCadenceIsDerivedFromTheRelaysOwnPresenceWindow(t *testing.T) {
	const relayPresenceTimeout = 30 * time.Second // relay.DefaultConfig().PresenceTimeout

	if presenceInterval != relayPresenceTimeout/2 {
		t.Fatalf("presenceInterval = %v, want %v (half the relay's %v presence window) -- polling faster buys nothing the relay can answer differently, and slower misses a transition for a whole window", presenceInterval, relayPresenceTimeout/2, relayPresenceTimeout)
	}
	if perMinute := int(time.Minute / presenceInterval); perMinute > 10 {
		t.Fatalf("the cadence issues %d presence ops/min, which is a real share of the relay's 600/min OpsPerMin window that mailbox_read and mailbox_ack also draw from", perMinute)
	}
}
