package phonecore

// WAVE R8 -- THE ROUTER, AND THE CONTROL GENERATION'S PHONE-SIDE STATE (ADR-017).
//
// THE ROUTER. T1 gives a session exactly ONE phone surface, chosen by the daemon: chat,
// the capability-routed sanitized terminal fallback, or the honest status card -- "three
// destinations, nothing in between". T2 rule 3 makes the choice a READ of the record and
// never an inference ("it never infers support from whether a transcript happens to be
// empty"), and amendments T2-a, T2-b and T5-a make absence, inconsistency and a
// zero-valued profile all resolve to the SAME destination: the status card, with both
// verbs refused. That is ONE predicate in ONE place, so there is one thing to get right
// rather than one per screen.
//
// THE CONTROL GENERATION. T8's severance list is a LIST, not a timeout, and amendment
// T8-b makes backgrounding a trigger in its own right rather than by consequence of the
// disconnect it forces. T6-f adds the rule at the buffer that actually exists: on ANY
// severance trigger the held bytes are DROPPED, never flushed, because the natural
// implementation of "release control" flushes -- and a flush converts live-only input into
// a short offline queue at the one place a queue can form.

import (
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// SessionDestination is the one phone surface a session gets.
type SessionDestination int

const (
	// DestinationStatusCard is T1's third destination AND the fail-closed default. It is
	// the answer for a session that is neither structured nor fallback, for an absent
	// record, for an inconsistent one, for one that binds no instance, and for a machine
	// whose profile declares no TerminalView or no capability-record version.
	DestinationStatusCard SessionDestination = iota
	// DestinationChat is ADR-009 exactly, unchanged, for a structured_chat session. Such a
	// session never sees a terminal.
	DestinationChat
	// DestinationTerminalFallback is the machine-sanitized, read-only-by-default terminal
	// view, and the ONLY destination ADR-009 decision 1 is re-scoped for.
	DestinationTerminalFallback
)

// String makes a failing routing table legible.
func (d SessionDestination) String() string {
	switch d {
	case DestinationChat:
		return "chat"
	case DestinationTerminalFallback:
		return "terminal_fallback"
	default:
		return "status_card"
	}
}

// RouteSession is the whole routing rule, and every fail-closed arm below names a state a
// user is protected FROM rather than a case that was tidy to add:
//
//   - a zero terminal_view_version means NO FALLBACK EXISTS on this machine -- which is
//     literally what every machine deployed before Wave R8 sends -- so rendering one would
//     be rendering a view the machine never produces (T5-a);
//   - a zero capability_record_version means the record is UNTRUSTED, which composes with
//     T2-a into the same predicate;
//   - an absent record is the state of every pre-R8 session, so "unknown therefore chat"
//     shows a composer whose every send is refused, and "unknown therefore terminal" opens
//     the peek on all of them (T2-a);
//   - an INCONSISTENT record is rejected rather than resolved, because resolving it means
//     choosing which boolean to believe and either choice is a routing decision taken by
//     the reader rather than by the daemon that authored it (T2-b);
//   - a record that binds no session instance can bind neither a watch nor a generation to
//     an incarnation (T8-a).
func RouteSession(rec *schema.SessionCapabilities, profile schema.RemoteProfileV1) SessionDestination {
	if !profile.TrustsCapabilityRecord() {
		return DestinationStatusCard
	}
	if rec == nil || rec.Validate() != nil {
		return DestinationStatusCard
	}
	if rec.StructuredChat {
		return DestinationChat
	}
	// Written over BOTH booleans via the record's own accessor, every time: a gate that
	// tests terminal_fallback alone enforces T2 rule 4 only for as long as the daemon's
	// derivation stays right.
	if rec.AllowsTerminalWatch() && profile.OffersTerminalView() {
		return DestinationTerminalFallback
	}
	return DestinationStatusCard
}

// ComposerAvailable answers T2 rule 3 at the place the phone currently decides it. The
// Kotlin detail panel derived it from `transcript.structureTorn` -- a fact about the
// TRANSCRIPT, which is exactly the inference the rule forbids. A torn transcript and a
// provider that never had structured chat are different states with different
// explanations, and only the record can tell them apart.
func ComposerAvailable(rec *schema.SessionCapabilities, profile schema.RemoteProfileV1) bool {
	return RouteSession(rec, profile) == DestinationChat
}

// TerminalControlAvailable is ComposerAvailable's opposite number on the other destination,
// and it exists for the reason ComposerAvailable's own comment gives one line up: THE RAW
// RECORD FIELD IS NOT THE PREDICATE.
//
// mobile/app.go read `out.TerminalControl = rec.TerminalControl` verbatim, three lines under
// that paragraph, and it cost two things at once (round-3 major 4):
//
//   - with NO mutation and a perfectly VALID record, an opencode session on a machine whose
//     profile carries terminal_view_version == 0 -- which is every machine deployed before
//     this wave -- routes to the STATUS CARD while the facade hands Kotlin
//     `terminalControl = true`. A screen offering a keyboard over a session the router
//     refused is the same defect as a composer over a refused record, one destination over;
//   - against an inconsistent record the only guard left was the phone's decode seam, so a
//     single seam was carrying a ruling that amendment T2-b deliberately states at three.
//
// It is written as ROUTE FIRST, then the record's own nil-safe accessor, so control can never
// outrun the destination: a session that is not on the fallback cannot be typed into, whatever
// its record says, and a fallback session degraded into that destination may watch and may not
// control (T6-b).
func TerminalControlAvailable(rec *schema.SessionCapabilities, profile schema.RemoteProfileV1) bool {
	return RouteSession(rec, profile) == DestinationTerminalFallback && rec.AllowsTerminalControl()
}

// TerminalControlTTL is the control generation's horizon (ADR-017 T7). It ADOPTS the
// number already implemented rather than inventing one, so the system has ONE fifteen-
// minute wall rather than two nearly-equal ones that will drift apart, and it stays half
// MaxControlSessionTTL so a generation can never outlive the control-session cap on the
// strength of its horizon alone.
const TerminalControlTTL = TakeControlTTL

// terminalGeneration is one session's live control generation on the phone.
type terminalGeneration struct {
	instance   string
	generation string
	expires    time.Time
}

// TerminalControlState is the phone's own view of which sessions it may currently type raw
// bytes into. It is deliberately NOT LeaseState (OPEN-C4): a generation is not a lease,
// they have different lifetimes and different ceremonies, and sharing the plane would make
// a fallback session compete for the shim's single interactive subscriber slot the owner
// already holds.
type TerminalControlState struct {
	coalescer *InputCoalescer

	mu   sync.Mutex
	live map[string]*terminalGeneration
}

// NewTerminalControlState returns a state bound to the coalescer whose held bytes it must
// drop on every severance trigger.
func NewTerminalControlState(c *InputCoalescer) *TerminalControlState {
	return &TerminalControlState{coalescer: c, live: map[string]*terminalGeneration{}}
}

// Begin records a generation the machine confirmed.
func (t *TerminalControlState) Begin(session, instance, generation string, expires time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.live[session] = &terminalGeneration{instance: instance, generation: generation, expires: expires}
}

// Apply is the phone's ingestion of a command reply on the terminal-control plane, and it
// is what makes Begin reachable at all: without it the phone never learned the generation
// the machine minted, so TerminalInput could not name one and the whole control half was
// unreachable from the app whatever the wire allowed.
//
// It lands beside LeaseState.Apply, on the SAME frame and for the same reason: a command
// reply is the only machine -> phone frame the phone can correlate to a send it made, and
// it has been through the AEAD, so the generation is adopted from an authenticated frame
// rather than from an unauthenticated header the untrusted relay could author.
//
// A reply carrying no generation is not a begin, and nothing happens.
//
// THE HORIZON IS RECOMPUTED HERE FROM THE PHONE'S OWN CLOCK, deliberately: the machine's
// wall is the one that decides, and this is only the banner's countdown. It is never longer
// than the machine's, because it starts later -- the reply arrives after the begin was
// stamped -- so the screen can say control ended early but never late.
func (t *TerminalControlState) Apply(ctrl schema.Control, instance string, now time.Time) {
	if ctrl.ControlGeneration == "" || ctrl.SessionID == "" {
		return
	}
	t.Begin(ctrl.SessionID, instance, ctrl.ControlGeneration, now.Add(TerminalControlTTL))
}

// Live reports whether session has a live, unexpired generation at now.
func (t *TerminalControlState) Live(session string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	g, ok := t.live[session]
	return ok && now.Before(g.expires)
}

// Generation returns session's live generation id and the instance it was minted against,
// so every input frame can name both and the machine can re-evaluate both per frame
// (T6-e). It reports false once the generation is severed, which is what keeps a refused
// byte from being authored at all.
func (t *TerminalControlState) Generation(session string) (generation, instance string, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g, found := t.live[session]
	if !found {
		return "", "", false
	}
	return g.generation, g.instance, true
}

// Sever ends ONE session's generation and DROPS its held bytes (ADR-017 T6-f).
//
// The bytes are dropped, never flushed, and each is recorded on the undelivered ledger.
// The two wrong answers are symmetrical and both ship a lie: a flush delivers bytes whose
// authority has just been withdrawn (an offline queue, one window long), and a silent drop
// leaves the user believing they typed something that never landed.
func (t *TerminalControlState) Sever(session, reason string) {
	t.mu.Lock()
	_, had := t.live[session]
	delete(t.live, session)
	t.mu.Unlock()
	_ = had
	t.dropHeld(session, reason)
}

// SeverAll ends EVERY session's generation and drops every held byte. It is the
// whole-device boundary: transport loss, the kill switch, or this device being revoked.
func (t *TerminalControlState) SeverAll(reason string) {
	t.mu.Lock()
	sessions := make([]string, 0, len(t.live))
	for s := range t.live {
		sessions = append(sessions, s)
	}
	t.live = map[string]*terminalGeneration{}
	t.mu.Unlock()
	for _, s := range sessions {
		t.dropHeld(s, reason)
	}
	if c := t.buffer(); c != nil {
		// And anything held for a session with no generation at all: a byte the user typed
		// on the way out must not survive the boundary either.
		c.Abandon(reason)
	}
}

// Background is amendment T8-b: BACKGROUNDING SEVERS DIRECTLY, in its own right and
// independent of transport.
//
// The phone core recorded the opposite -- that a backgrounded app loses its authority
// because backgrounding DISCONNECTS the phone and the transport loss is what severs. That
// answer is BY CONSEQUENCE and rests on a connectivity choice a later wave could revisit,
// at which point a generation would quietly outlive the screen that owns it. T6 makes
// "only the active foreground screen may send input" a ROUTING RULE; a generation with no
// screen displaying that it is live is the state the persistent control banner exists to
// make impossible.
func (t *TerminalControlState) Background(reason string) {
	t.SeverAll(reason)
}

// dropHeld discards one session's paced-but-unsent bytes and records them as undelivered.
func (t *TerminalControlState) dropHeld(session, reason string) {
	if c := t.buffer(); c != nil {
		c.AbandonSession(session, reason)
	}
}

// buffer reads the bound coalescer under the lock.
func (t *TerminalControlState) buffer() *InputCoalescer {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.coalescer
}

// BindCoalescer attaches the input coalescer whose held bytes every severance trigger must
// drop (ADR-017 T6-f). It is set by the facade that owns both, because the Core is
// constructed before the coalescer exists; a state with none still severs the generation,
// it just has no buffer to empty.
func (t *TerminalControlState) BindCoalescer(c *InputCoalescer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.coalescer = c
}
