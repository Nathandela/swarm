package swarmmobile

// Bead agents-tracker-xtj -- the machine's presence as something a screen can read.
//
// App.Presence is a relay round-trip. The facade sets no deadline on it and the transport's
// DefaultCallTimeout is 10 s, so a screen that called it per render would put a 10-second
// blocking RPC on Android's main thread once per redraw -- which is why
// android/unbound-verbs.tsv ledgered the verb unbound rather than letting a surface reach it:
// "It needs a screen that polls it on its own cadence, off the main thread."
//
// THE CADENCE IS OWNED HERE, NOT IN KOTLIN. The phone already keeps three facts this way --
// clockVerdict, killSwitch, connState -- each learned on the relay goroutine, held under
// a.mu, read O(1) by the UI thread, and raised as an event on transition. Presence is the
// fourth, and putting the timer in Go rather than in a Kotlin poller is what keeps the rule
// ("never per render") enforceable from the side that can enforce it.
//
// WHAT IS DIFFERENT ABOUT THIS ONE: it can go stale. The other three are facts about the
// phone's own connection, known the instant they change. Presence is the RELAY'S OPINION,
// fetched on a timer, so a reading is only as good as its age -- and the moment the link
// drops the phone cannot ask at all. Both are in the cache: every reading carries its
// instant, and losing the link resets the state to unknown rather than leaving "online"
// standing on evidence the phone can no longer refresh. That is PB-APP-11's lesson (silence
// is not liveness) applied to a value somebody else owns.

import (
	"context"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

const (
	// presenceUnknown is the state of a machine this phone cannot currently speak for. It is
	// the relay's own vocabulary (relay.PresenceUnknown), used here for the phone's own "I
	// have not asked" and "I can no longer ask" so a screen has one word to render and no
	// fourth state to invent.
	presenceUnknown = string(relay.PresenceUnknown)

	// presenceInterval is how often the poll runs while a connection is up.
	//
	// It is DERIVED, not chosen. The relay declares a party offline PresenceTimeout after a
	// gateway drop -- 30 s by default -- so its answer cannot change faster than its own
	// sweep, and sampling at half that period observes a transition within one window.
	// Polling faster buys nothing the relay could answer differently; polling slower misses a
	// transition for a whole window.
	//
	// It is a literal rather than a read of relay.DefaultConfig() because that is the SERVER's
	// default and an operator may tune the deployed one, which a handset cannot see -- the
	// same convention phonecore's coalescer uses for MailboxAppendPerMin.
	//
	// The budget it draws on is OpsPerMin, which meters `presence` at 600/min shared with
	// mailbox_read and mailbox_ack. Four polls a minute is noise against the drain.
	presenceInterval = 15 * time.Second
)

// presenceCache is the phone's latest reading of the machine's presence, and when it was
// taken. Safe for concurrent use: the relay goroutine writes while the UI thread reads.
type presenceCache struct {
	now func() time.Time

	mu    sync.Mutex
	state string
	at    time.Time
}

// newPresenceCache returns a cache reading now for every observation. The clock is injected
// because an age is a property of the algorithm, not of the host.
func newPresenceCache(now func() time.Time) *presenceCache {
	if now == nil {
		now = time.Now
	}
	return &presenceCache{now: now, state: presenceUnknown}
}

// observe records what the relay just said, reporting whether that is a CHANGE. The instant
// advances either way: an unchanged machine that looked staler every tick would eventually be
// indistinguishable from one that had stopped answering. Only the change is worth an event --
// the callback queue is bounded and drops oldest, so a notification every cadence tick would
// push real events off it.
func (p *presenceCache) observe(state string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	changed := p.state != state
	p.state, p.at = state, p.now()
	return changed
}

// forget resets the reading to unknown because the phone can no longer ask -- the connection
// is gone. It is not the same as the relay REPORTING unknown, and it does not need to be: to
// a screen both mean "nobody can currently vouch for this machine", which is the only honest
// thing to render. Holding the last "online" would be the phone speaking for a party it has
// lost contact with.
func (p *presenceCache) forget() bool {
	return p.observe(presenceUnknown)
}

// read is the O(1) locked read a screen makes. The zero instant means nothing has been
// observed yet, which the caller renders as an absent reading rather than an ancient one.
func (p *presenceCache) read() (string, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state, p.at
}

// pollPresence runs the cadence for the life of ONE connection: ask, cache, raise an event on
// transition, until the context ends. The caller cancels it when the drain returns, so the
// poll can never outlive the client it polls through.
//
// A failed poll is NOT recorded as unknown. The call can fail because the link is going away,
// and the disconnect path already resets the cache; treating a transient refusal as a presence
// answer would flap the screen between online and unknown on every hiccup. The reading simply
// ages until the next tick succeeds or the connection ends.
func (a *App) pollPresence(ctx context.Context, cl *relay.Client) {
	tick := time.NewTicker(presenceInterval)
	defer tick.Stop()
	for {
		a.probePresence(ctx, cl)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// probePresence is one reading. It is the same relay op App.Presence performs, taken on the
// relay goroutine where a 10-second timeout costs a screen nothing.
func (a *App) probePresence(ctx context.Context, cl *relay.Client) {
	target, _ := a.destination()
	if target == "" {
		return
	}
	info, err := cl.Presence(ctx, target)
	if err != nil {
		return
	}
	if a.presence.observe(string(info.State)) {
		a.events.emit(&Event{Kind: "presence", State: string(info.State)})
	}
}

// MachinePresence is the machine's reachability as the relay last told this phone, with the
// instant that reading was taken (PB-APP-5). It is a CACHED read and never a round-trip: this
// is the verb a screen may call per render, and App.Presence is the one that asks.
//
// THE AGE IS PART OF THE ANSWER. A cached opinion that cannot say how old it is renders
// staleness as liveness, and this particular opinion belongs to the relay -- the party
// PB-APP-11 declines to take on trust. ObservedUnixMs is 0 when nothing has been observed
// yet, which a screen shows as no reading rather than as an ancient one.
//
// IT IS NOT MachineFreshness AND A SCREEN MAY NOT SUBSTITUTE ONE FOR THE OTHER, for the reason
// that verb's own doc gives: presence is the relay's opinion, freshness is the phone's
// evidence. A withholding relay can keep answering "online" here forever; only the machine's
// own AAD-covered stamp can contradict it.
func (a *App) MachinePresence() (p *MachinePresence, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	state, at := a.presence.read()
	out := &MachinePresence{State: state}
	if !at.IsZero() {
		out.ObservedUnixMs = at.UnixMilli()
	}
	return out, nil
}
