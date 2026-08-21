package remotegw

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/status"
)

// DefaultPushWindow is §6.0's coalescing window: at most one wake PER SESSION per 30 s.
// Per session, not global -- two agents both stopping for input inside one window are two
// separate hand-offs, and a single shared timestamp would silently drop the second, which
// is invisible until the owner is waiting on a session the phone never mentioned.
const DefaultPushWindow = 30 * time.Second

// PushWakeEnvelopeSize is the exact, INVARIANT size of every wake that leaves this
// machine: crypto's 62-byte cleartext header plus a 16-byte AEAD tag over an EMPTY
// plaintext.
//
// It is pinned as a constant because size is the one property PB-PUSH-3 concedes the push
// provider observes, and a conceded disclosure is benign only while it is CONSTANT. A wake
// that grew with the session name, or with how many transitions were coalesced into it,
// would be a covert channel -- and the ADR's honesty claim would quietly stop being true
// with nothing failing anywhere (ADR-007 B20).
const PushWakeEnvelopeSize = 78

// defaultPushTimeout bounds one push_trigger round trip. The trigger is issued from the
// gateway's journal read loop, so an unbounded call against a hung relay would stall the
// journal the wake exists to announce.
const defaultPushTimeout = 5 * time.Second

// PushTriggerer is the relay seam a wake goes out through: the relay looks up the target's
// registered push token and hands the opaque envelope to its PushSink. *relay.Client
// satisfies it, which is why the gateway needs no second connection for push.
type PushTriggerer interface {
	PushTrigger(ctx context.Context, target string, env []byte) error
}

// obligationPreAppender is an OPTIONAL capability of PushConfig.Pusher (ADR-015 P9,
// PG-OBL-2): a Pusher that must durably record its intent to wake BEFORE the mailbox
// record announcing it is published implements it. *TransportRouter is the only
// implementation (pushtransport.go), and only its gateway leg does real work --
// legacy_relay's ordering guarantee is already "publish then push" (unaffected: see
// Event below), and foreground_only has nothing durable to record.
type obligationPreAppender interface {
	PreAppendObligation() error
}

// provisionalObligationAppender is the deferred-wake refinement of obligationPreAppender
// (bd agents-tracker-hggx.4.4): the pre-append that also reports WHICH wake_seq the
// durable trigger landed on (ok=false when none was recorded), so the deferral timer's
// later cancellation can be scoped to exactly the provisional record this deferral
// created -- and never to whatever obligation happens to be live at fire time, which
// PG-OBL-5's coalescing routinely makes a record carrying other sessions' hand-offs.
// *TransportRouter is the only implementation.
type provisionalObligationAppender interface {
	PreAppendProvisionalObligation() (seq uint64, ok bool, err error)
}

// obligationSuperseder is the OPTIONAL cancellation counterpart of obligationPreAppender
// (bd agents-tracker-hggx.4.4, ruled option (a)): the deferred-wake timer's at-send
// preference re-read (ADR-010 §4(b), PB-PUSH-8) may decide NOT to send the wake whose
// provisional obligation preAppendObligation durably recorded at trigger time, and the
// record must then be superseded -- durably, in place, honestly (never claiming a
// send, never left non-terminal for a redrive to resurrect), and ONLY that record
// (wakeSeq names it; a record carrying a coalesced trigger this deferral cycle did not
// itself make is refused downstream). ownAppends is how many pre-appends the cycle being
// cancelled made onto that record, which is what tells the machine a coalesce apart from
// a foreign one -- see WakeObligationMachine.Supersede.
// *TransportRouter is the only implementation, and only its gateway leg does real work.
type obligationSuperseder interface {
	SupersedeObligation(wakeSeq uint64, ownAppends int, reason string) error
}

// wakeOutcomePreferenceSuppressed is the LastOutcome word stamped on a provisional
// deferred-wake obligation whose send was suppressed at the timer's at-send preference
// re-read -- the preference had flipped off, was unreadable, or no preference custody
// was configured at all; every one of those suppresses at the sender (PB-PUSH-8,
// categoryEnabled), and every one means the wake was deliberately not sent.
const wakeOutcomePreferenceSuppressed = "preference_suppressed"

// PushConfig configures a PushNotifier.
//
// It carries a WakeKey and NO content key, and that is PB-PUSH-0's "the content key is
// never used" expressed as a type rather than as a review note. Read literally the
// requirement said "the gateway holds the wake key only", which is unimplementable -- the
// gateway MUST hold the content key, since RelaySink seals every journal frame with it and
// CommandBridge opens every phone command with it. What is enforceable, and what the
// criterion protects, is that the PUSH PATH holds the wake key only, so no content key is
// even in scope where a wake is built (ADR-007 B19). Do not widen this struct to take an
// EpochKeys "for convenience": a test reads its fields reflectively and will fail.
type PushConfig struct {
	Pusher  PushTriggerer    // relay push seam (nil => the gateway simply does not push)
	Target  string           // the phone's relay routing id
	WakeKey crypto.WakeKey   // K_wake (PB-KEY-2, A15): content-free by construction
	EpochID uint32           // the epoch the wake key belongs to
	Now     func() time.Time // wake issued-at clock (nil => time.Now)
	Seq     SeqSource        // DURABLE wake replay coordinate (nil => in-memory, non-durable)
	Prefs   PushPrefsSource  // the user's push preference (nil => fail closed, no wake)
	Window  time.Duration    // per-session coalescing window (0 => DefaultPushWindow)
	// After schedules f to run after d: the DEFERRED-WAKE timer seam of ADR-010 §4(b)
	// (nil => time.AfterFunc). It is a seam because the deferral is the one push-path
	// behaviour that happens with NO journal record to drive it, so a test holding only a
	// virtual clock could not observe it at all.
	After func(d time.Duration, f func())
}

// recordTypeInteraction is journal.TypeInteraction as it appears on the wire. It is a
// literal rather than an import because this package must not link the daemon, and because
// the value is frozen by interaction-schema.md IS-LAYER-1.
const recordTypeInteraction = "interaction"

// PushNotifier is the gateway-side push trigger (PB-PUSH-0): an OutboundSink that passes
// the journal through unchanged and, on the transitions an owner is waiting on, ADDITIONALLY
// wakes the phone with a content-free envelope sealed under the wake key.
//
// It sits BETWEEN the coalescing sink and the RelaySink -- CoalescingSink{Inner:
// PushNotifier{inner: RelaySink}} -- and the position matters twice. Outside the coalescer
// it would hide CoalescingSink.Flush from RunTerminal's idle wake, and PB-GW-7's trailing
// terminal flush would die silently, leaving an idle peek on a stale grid forever. Inside
// it, it must forward SetMachine and DeliveredCursor, because the coalescer type-asserts
// for both THROUGH it: a swallowed SetMachine leaves the reconcile record unattributable
// and bricks every mutating op on the phone (PB-SYNC-7), and a lost DeliveredCursor makes
// every restart re-read the journal from 0 (PB-GW-8).
//
// The push is strictly ADDITIVE. It never displaces the journal frame the phone reads on
// reconnect, it never fails a journal record, and it happens only AFTER the record it
// announces was durably appended.
type PushNotifier struct {
	inner  OutboundSink
	cfg    PushConfig
	now    func() time.Time
	window time.Duration
	after  func(d time.Duration, f func())

	mu sync.Mutex
	// lastGroup is the group each session was last SEEN in. It is what distinguishes
	// "entered a push-worthy group" from "is in one": a journal re-reporting needs_input
	// for a session already in needs_input must not re-wake the phone.
	lastGroup map[string]status.Group
	// lastWake is when each session last woke the phone -- the per-session coalescing
	// state.
	lastWake map[string]time.Time
	// deferred holds the sessions whose interaction wake the window suppressed, and
	// deferArmed whether the single timer that serves them is already scheduled
	// (ADR-010 §4(b)). One wake serves every session in the set: the envelope is a
	// constant-size empty plaintext (PushWakeEnvelopeSize), so coalescing wakes discloses
	// nothing and loses nothing.
	deferred   map[string]struct{}
	deferArmed bool
	// deferredSeq is the wake_seq of the PROVISIONAL obligation the deferred-wake
	// pre-append recorded (deferredSeqSet false when none was), remembered so the
	// deferral timer's preference-suppressed supersede names exactly that record.
	// One slot suffices: deferrals inside one armed window coalesce into one record
	// (each pre-append re-reports the same live seq), and the timer consumes the slot
	// whether it sends or supersedes.
	//
	// deferredAppends counts how many of THIS cycle's pre-appends landed on that record
	// -- the mint plus one per further deferred interaction that coalesced into it. It is
	// what lets the supersede tell its own coalesces from a foreign session's
	// (WakeObligationMachine.Supersede's ownAppends); noteDeferredProvisional restarts the
	// count whenever the reported seq changes, so it is always "how many of mine are on
	// THIS record", never a running total across records.
	deferredSeq     uint64
	deferredSeqSet  bool
	deferredAppends int
	lastErr         error
}

// The notifier is a full outbound sink and forwards both optional contracts. Pinned so a
// dropped method is a compile error, not a silent regression in an unrelated requirement.
var (
	_ OutboundSink = (*PushNotifier)(nil)
	_ CursorSource = (*PushNotifier)(nil)
	_ machineNamer = (*PushNotifier)(nil)
)

// NewPushNotifier wraps inner with the push trigger described by cfg.
func NewPushNotifier(inner OutboundSink, cfg PushConfig) *PushNotifier {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	window := cfg.Window
	if window <= 0 {
		window = DefaultPushWindow
	}
	if cfg.Seq == nil {
		cfg.Seq, _ = OpenSeqSource("") // in-memory, cannot error
	}
	after := cfg.After
	if after == nil {
		// ponytail: the timer is never cancelled and never held. At most one is armed at a
		// time and it fires a content-free wake at most one window later, so a gateway
		// shutting down leaves one pending 78-byte send -- cheaper than a lifecycle this
		// type does not otherwise have.
		after = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	}
	return &PushNotifier{
		inner:     inner,
		cfg:       cfg,
		now:       now,
		window:    window,
		after:     after,
		lastGroup: map[string]status.Group{},
		lastWake:  map[string]time.Time{},
		deferred:  map[string]struct{}{},
	}
}

// Snapshot forwards the reconnect roster and SEEDS the remembered group of every session
// in it WITHOUT waking anyone.
//
// Seeding rather than ignoring is the whole point. Snapshot is the per-(re)connection
// roster of CURRENT state, so every idle session in it looks like a fresh transition to a
// notifier that treats roster records as live: a gateway restart with n sessions parked in
// needs_input would fire n pushes at once for events that happened hours ago, and would do
// it again on every restart of a crash loop. Seeded, the same sessions are silent until
// something actually changes.
func (n *PushNotifier) Snapshot(roster []protocol.JournalRecord, cursor uint64) error {
	if err := n.inner.Snapshot(roster, cursor); err != nil {
		return err
	}
	n.mu.Lock()
	for _, rec := range roster {
		if rec.Group == "" {
			continue
		}
		n.lastGroup[rec.SessionID] = rec.Group
	}
	n.mu.Unlock()
	return nil
}

// Event forwards one live journal record and then, if it is a hand-off the owner is
// waiting on, wakes the phone.
//
// The ORDER is load-bearing. A wake tells the phone "reconnect, there is something to
// read"; if the record that motivated it never reached the mailbox, the phone reconnects,
// finds nothing, and the hand-off is lost with the owner believing they were notified. So
// the wake follows a SUCCESSFUL append, and a failed append suppresses it entirely.
//
// The converse is equally deliberate: a failed WAKE never fails the record. Gateway.deliver
// gates its durable cursor on this error, so returning a push failure would stall the
// journal on a relay push outage -- turning a lost convenience into a stalled bridge. The
// failure is surfaced through Err() instead, which is what keeps it from being silent.
//
// preAppendObligation runs FIRST, ahead of the publish, and is the one exception to
// "wake follows publish": it is not the wake itself (no network call, no phone-visible
// effect), only the gateway transport's durable local record of intent to send one
// (PG-OBL-2). See preAppendObligation's doc for why this ordering is safe against a
// failed append.
//
// GATEWAY-LEG QUALIFICATION: for a gateway-transport pairing specifically, "a failed
// append suppresses it entirely" is true of the WAKE (send() is still gated on inner.Event
// succeeding) but no longer true of the DURABLE OBLIGATION -- preAppendObligation already
// wrote one before inner.Event ran. A failed append therefore leaves a durable pending
// obligation for a record that was never published; a later trigger on the same address or
// RedrivePendingWakeObligations will still submit it, so the phone is woken to reconcile a
// record that does not exist. This is harmless by construction (the wake carries an empty,
// locator-free plaintext -- crypto.SealWake -- so the phone's reconcile simply finds
// nothing new) and is the forced consequence of PG-OBL-2's "obligation before publish"
// ordering, not an oversight.
func (n *PushNotifier) Event(rec protocol.JournalRecord) error {
	// One prefsCache for this call: peekWakeDisposition's peek and maybeWake's immediate-path
	// commit gate on the SAME preference read for the SAME record, so they share it rather
	// than each paying a separate LoadPrefs() (and, on failure, a separate setErr). See
	// prefsCache's doc for why the deferred-wake timer below does NOT get this cache.
	cache := &prefsCache{}
	n.preAppendObligation(rec, cache)
	if err := n.inner.Event(rec); err != nil {
		return err
	}
	n.maybeWake(rec, cache)
	return nil
}

// Terminal forwards a rendered terminal snapshot untouched. A snapshot is something the
// phone asked to watch while it is awake, so it wakes nobody.
func (n *PushNotifier) Terminal(view protocol.TerminalViewV1) error {
	return n.inner.Terminal(view)
}

// SetMachine passes the daemon-assigned endpoint id down to the sealing sink. A no-op here
// would leave the reconcile record naming no machine, which a paired phone REFUSES --
// bricking every mutating op permanently (PB-SYNC-7) for a wrapper that merely forgot to
// forward one call.
func (n *PushNotifier) SetMachine(machine string) {
	if m, ok := n.inner.(machineNamer); ok {
		m.SetMachine(machine)
	}
}

// Reseed forwards the journal repair frame down to the sealing sink. A no-op here would
// leave every stale phone with a resync that reaches the machine, is answered, and delivers
// nothing -- the flag then never clears and the roster stays a hole presented as live.
// It raises no push: the phone asked for this frame and is awake waiting for it.
func (n *PushNotifier) Reseed(rs protocol.JournalReseed) error {
	if rr, ok := n.inner.(ReseedSink); ok {
		return rr.Reseed(rs)
	}
	return errNoReseedSink
}

// DeliveredCursor forwards the inner sink's durable PB-GW-8 resume point.
func (n *PushNotifier) DeliveredCursor() uint64 {
	if cs, ok := n.inner.(CursorSource); ok {
		return cs.DeliveredCursor()
	}
	return 0
}

// Err returns the FIRST push-path error since construction, or nil.
//
// It is a separate channel from the journal's precisely because a push failure is not
// allowed to fail a record: without it a gateway whose every wake is refused, or whose
// preference file is corrupt, is indistinguishable from one that is working -- the phone
// simply never rings, and nothing anywhere reports why.
func (n *PushNotifier) Err() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastErr
}

func (n *PushNotifier) setErr(err error) {
	n.mu.Lock()
	if n.lastErr == nil {
		n.lastErr = err
	}
	n.mu.Unlock()
}

// errNoPushPrefs refuses to wake anyone from a gateway assembled without preference
// custody. Fail-closed-ABSENT, matching the protocol tier's refusal when its
// DeviceAuthenticator is missing: a misassembled gateway must not be the one configuration
// in which every push goes out unfiltered.
var errNoPushPrefs = errors.New("remotegw: push suppressed: no preference custody configured")

// preAppendObligation durably records (TransportRouter.PreAppendObligation ->
// WakeObligationMachine.Trigger) the wake this Event call is about to publish, BEFORE
// the mailbox append -- PG-OBL-2, for a gateway-transport pairing only. A Trigger
// failure is reported through Err() and otherwise swallowed: PG-OBL-3 forbids a
// push-path failure from ever blocking or failing the mailbox record, and maybeWake's
// own send() (moments later, after the publish) makes its own Trigger+Drive attempt
// regardless -- harmlessly coalescing into whatever this call managed to append, or
// minting fresh if it appended nothing at all.
func (n *PushNotifier) preAppendObligation(rec protocol.JournalRecord, cache *prefsCache) {
	pre, ok := n.cfg.Pusher.(obligationPreAppender)
	if !ok {
		return
	}
	disp := n.peekWakeDisposition(rec, cache)
	if disp == wakeNone {
		return
	}
	if disp == wakeDeferred {
		// The DEFERRED case remembers the provisional record's identity, so the timer's
		// preference-suppressed supersede cancels that record and nothing else.
		if pp, capable := n.cfg.Pusher.(provisionalObligationAppender); capable {
			seq, recorded, err := pp.PreAppendProvisionalObligation()
			if err != nil {
				n.setErr(err)
			} else if recorded {
				n.noteDeferredProvisional(seq)
			}
			return
		}
	}
	if err := pre.PreAppendObligation(); err != nil {
		n.setErr(err)
	}
}

// wakeDisposition is peekWakeDisposition's answer: what maybeWake would do with rec once
// the publish it gates on succeeds.
type wakeDisposition int

const (
	wakeNone      wakeDisposition = iota // no wake: suppressed, disabled, or not wake-worthy
	wakeImmediate                        // send() fires now
	wakeDeferred                         // window-suppressed interaction: deferred to the window's end (ADR-010 §4(b))
)

// peekWakeDisposition reports, WITHOUT mutating any bookkeeping (lastGroup/lastWake/
// deferred), whether rec would cause maybeWake to send an IMMEDIATE wake, a DEFERRED
// one, or none at all. It exists only to drive preAppendObligation, and it must be
// EXACT: a false positive would durably record an obligation for a wake PG-OBL-7
// requires never exist ("a suppressed trigger creates NO obligation"), and a false
// negative reopens the crash gap PG-OBL-2 exists to close.
//
// It deliberately mirrors maybeWake's gating rather than sharing code with it, because
// maybeWake's version COMMITS (isTransition/claimWindow mutate state) and this one must
// not: gateway.go's deliver forwards a FAILED record for redelivery with the identical
// rec ("forward first, advance the cursor only after the sink acks", deliver's own
// comment), and committing transition/window state before knowing the publish actually
// succeeded would silently suppress the retry's wake.
//
// WHAT ACTUALLY SERIALISES, STATED PRECISELY: Event calls themselves are serialised by
// RunJournal's single read loop, so no OTHER Event call can land between this peek and
// maybeWake's own commit moments later. That does NOT cover the deferred-wake timer
// goroutine (maybeWake's send() closure, ADR-010 §4(b)): it runs on its own goroutine and
// mutates lastWake via claimDeferred independently of the read loop, so it CAN interleave
// between this peek and the commit. The exposure is narrow and self-healing rather than
// absent: at worst this peek sees claimWindow as available (predicting an immediate wake)
// when the timer's claimDeferred races in first and the commit defers instead -- which
// durably records an obligation for a trigger PG-OBL-7 says must create none. The timer's
// OWN send(), moments later, Triggers+Drives the same address, so the extra obligation
// coalesces into a real wake rather than sitting unresolved: it costs one redundant local
// write, not a leaked or duplicated wake.
//
// A DEFERRED wake (window-suppressed interaction) reports wakeDeferred, and
// preAppendObligation records a durable PROVISIONAL obligation for it exactly as for an
// immediate wake (bd agents-tracker-hggx.4.4, ruled option (a)). This closes the
// narrower PG-OBL-2 crash gap the earlier design accepted as a residual: the deferral
// timer fires up to one window AFTER the mailbox record is published, so a crash inside
// that window used to publish an event with no durable obligation announcing it. The
// cost the old design feared -- durably recording intent for a send the at-send
// preference re-read (PB-PUSH-8) may yet forbid -- is paid honestly instead: the timer
// SUPERSEDES the provisional record in place when the re-read suppresses the send
// (maybeWake's deferral closure, WakeObligationMachine.Supersede).
func (n *PushNotifier) peekWakeDisposition(rec protocol.JournalRecord, cache *prefsCache) wakeDisposition {
	if n.cfg.Pusher == nil {
		return wakeNone
	}
	category := rec.Group
	deferrable := false
	if rec.Type == recordTypeInteraction {
		category, deferrable = status.GroupNeedsInput, true
	} else {
		if rec.Group == "" || !isWakeWorthy(rec.Group) {
			return wakeNone
		}
		n.mu.Lock()
		prev, seen := n.lastGroup[rec.SessionID]
		n.mu.Unlock()
		if seen && prev == rec.Group {
			return wakeNone
		}
	}
	if !n.categoryEnabled(category, cache) {
		return wakeNone
	}
	n.mu.Lock()
	last, seen := n.lastWake[rec.SessionID]
	n.mu.Unlock()
	if !seen || n.now().Sub(last) >= n.window {
		return wakeImmediate
	}
	if deferrable {
		return wakeDeferred
	}
	// A window-suppressed GROUP transition is dropped, not deferred (maybeWake's own
	// deferral comment), so it creates no obligation either (PG-OBL-7).
	return wakeNone
}

// maybeWake decides whether this record is a hand-off worth waking the phone for and, if
// so, sends the wake -- now, or at the end of the window that suppressed it. It never
// returns an error: see Event.
//
// send is a closure rather than a method for one deliberate reason: relay's PB-PUSH-3
// producer ledger (internal/remote/relay/pbpush3_producers_test.go) enumerates every
// FUNCTION that hands a payload to the push provider, so that a new producer fails by name.
// The deferred wake is not a new producer -- same seal, same empty plaintext, same constant
// 78 bytes -- and keeping its one PushTrigger call inside this function keeps that ledger
// accurate rather than merely quiet.
//
// NOTE, NOT A DISCLOSURE: send always seals the LEGACY 78-byte envelope (sealWake,
// consuming cfg.Seq) before calling Pusher.PushTrigger, even for a `gateway` or
// `foreground_only` pairing whose TransportRouter discards that envelope outright
// (pushtransport.go: only legacy_relay's leg ever forwards it). The sealed bytes never
// leave this process, so nothing is exposed -- but it does mean cfg.Seq (PushSeq)
// advances, and a seal is spent, for every wake a migrated pairing never actually sends
// over legacy_relay. Do not read PushSeq's value as a count of legacy wakes sent.
func (n *PushNotifier) maybeWake(rec protocol.JournalRecord, cache *prefsCache) {
	send := func() {
		env, err := n.sealWake()
		if err != nil {
			n.setErr(err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), defaultPushTimeout)
		defer cancel()
		if err := n.cfg.Pusher.PushTrigger(ctx, n.cfg.Target, env); err != nil {
			n.setErr(err)
		}
	}

	category := rec.Group
	deferrable := false
	if rec.Type == recordTypeInteraction {
		// ADR-010 §4(a): an interaction append is wake-eligible ON ITS OWN, independent of
		// Group and of isTransition. It carries no Group (an item is not a roster
		// transition), so the gate below would drop it and IS-LIFE-1's "the daemon SHALL
		// send a push wake" would be unimplementable -- and a second approval inside one
		// turn, with the session already in the permission group, would wake nothing at
		// all even if it did carry one.
		//
		// It fires for EVERY interaction record, not only approval_request, and that is
		// forced rather than chosen: IS-LAYER-1 gives every item the coarse wire type
		// `interaction` and interaction-schema.md §10 forbids the gateway parsing an item,
		// so the kind is only readable inside the AEAD-covered payload. This is the
		// superset that needs no parse; the per-session window below is what bounds it.
		//
		// The category is needs_input because that is what an approval IS -- the agent
		// blocked on its owner. Charging it to `finished` would put the one wake the owner
		// is waiting on behind the preference for the one they are not.
		category, deferrable = status.GroupNeedsInput, true
	} else {
		// A group-less record (session-neutral, or a type carrying no status) is IGNORED
		// rather than read as a transition into the empty group. Read as one it would both
		// fire on the next real needs_input as though it were a change from "", and let a
		// stream of group-less records reset every session's remembered state.
		if rec.Group == "" {
			return
		}
		if !n.isTransition(rec) {
			return
		}
		if !isWakeWorthy(rec.Group) {
			return
		}
	}
	// No transport configured is not a failure: "the system works without push"
	// (PB-PUSH-5). Checked before the preference so a gateway that never pushes at all
	// does not report a preference problem it does not have.
	if n.cfg.Pusher == nil {
		return
	}
	// cache carries peekWakeDisposition's already-loaded preference forward for this SAME record
	// (Event's peek and this commit, moments later): reused here rather than reloaded.
	if !n.categoryEnabled(category, cache) {
		return
	}
	remaining, ok := n.claimWindow(rec.SessionID)
	if !ok {
		// ADR-010 §4(b): a suppressed interaction wake is DEFERRED to the end of the window
		// that suppressed it, never dropped, and one timer serves every session pending at
		// that moment. The arithmetic: <= 30 s against Codex's 120 s measured expiry leaves
		// >= 90 s, above spike-SC's 60 s one-tap floor, and is not close against Claude
		// Code's >= 300 s.
		//
		// A suppressed GROUP transition is still dropped, deliberately. That wake is
		// redundant -- same session, same roster state, re-read whole on the next connect --
		// and PB-PUSH-0's own fence
		// (TestPBPUSH0_CoalescesRepeatTransitionsWithinTheWindow) pins that it fires once
		// per window. An approval is the opposite: a distinct request with an expiry, which
		// nothing later re-announces.
		if deferrable && n.armDeferral(rec.SessionID) {
			n.after(remaining, func() {
				// The preference is re-read AT SEND. This is the only push this type emits
				// with no record driving it, so it is the only one that can outlive the
				// preference it was authorized under -- and PB-PUSH-8 wants a disabled toggle
				// to mean no push sent, verified at the sender. claimDeferred runs first
				// either way: a dropped wake must still release the single timer arm, or one
				// preference flip wedges the deferral path shut for every session after it.
				owed := n.claimDeferred()
				if !owed {
					return
				}
				// The provisional identity, and this cycle's pre-append count against it,
				// are consumed on EVERY fire that owed a wake -- sent or suppressed -- so
				// neither can leak into a later cycle's supersede.
				provSeq, provAppends, haveProv := n.takeDeferredSeq()
				// nil cache: this runs up to n.window later, on its own goroutine, and MUST
				// re-read the preference fresh rather than reuse Event's cache from send time
				// (the whole point of "re-read AT SEND" above).
				if n.categoryEnabled(status.GroupNeedsInput, nil) {
					send()
					return
				}
				// The re-read suppressed the send. The PROVISIONAL obligation
				// preAppendObligation durably recorded for this deferral (PG-OBL-2,
				// bd agents-tracker-hggx.4.4) must now be superseded -- durably, honestly,
				// and scoped to exactly that record by its remembered wake_seq -- or a
				// later redrive would submit a wake the preference forbids. A no-op for
				// any Pusher without the capability (the legacy paths, which pre-appended
				// nothing), and skipped entirely when no identity was recorded: cancelling
				// "whatever is live" would destroy other sessions' coalesced hand-offs.
				// provAppends is how many pre-appends THIS cycle made onto that record --
				// two window-suppressed interactions in one window make two, the second
				// coalescing into the first's record -- so the machine can cancel a record
				// carrying only this cycle's own needs_input triggers while still refusing
				// one a foreign trigger joined (Supersede's ownAppends).
				if sup, ok := n.cfg.Pusher.(obligationSuperseder); ok && haveProv {
					if err := sup.SupersedeObligation(provSeq, provAppends, wakeOutcomePreferenceSuppressed); err != nil {
						n.setErr(err)
					}
				}
			})
		}
		return
	}
	send()
}

// armDeferral records that session needs a wake its window suppressed, and reports whether
// the caller must schedule the timer. Only the first suppressed session arms it: a timer
// already pending will fire within one window and serve everyone in the set, so arming a
// second would spend a second wake on the same information.
func (n *PushNotifier) armDeferral(session string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.deferred[session] = struct{}{}
	if n.deferArmed {
		return false
	}
	n.deferArmed = true
	return true
}

// claimDeferred consumes the deferred set and reports whether a wake is still owed. It
// stamps every deferred session's coalescing window at the moment the wake goes out, so the
// one wake counts for all of them and none of them re-fires immediately after.
func (n *PushNotifier) claimDeferred() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.deferArmed = false
	if len(n.deferred) == 0 {
		return false
	}
	now := n.now()
	for s := range n.deferred {
		n.lastWake[s] = now
		delete(n.deferred, s)
	}
	return true
}

// noteDeferredProvisional remembers the provisional record a deferred pre-append landed
// on and counts this cycle's pre-appends onto it. A reported seq that differs from the
// remembered one is a DIFFERENT record (the previous cycle's was delivered, superseded or
// re-minted over), so the count restarts at 1 rather than carrying a stale cycle's
// allowance onto a record it never touched.
func (n *PushNotifier) noteDeferredProvisional(seq uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.deferredSeqSet && n.deferredSeq == seq {
		n.deferredAppends++
		return
	}
	n.deferredSeq, n.deferredSeqSet, n.deferredAppends = seq, true, 1
}

// takeDeferredSeq consumes the remembered provisional obligation identity and this
// cycle's pre-append count against it, if any.
func (n *PushNotifier) takeDeferredSeq() (seq uint64, ownAppends int, ok bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	seq, ownAppends, ok = n.deferredSeq, n.deferredAppends, n.deferredSeqSet
	n.deferredSeq, n.deferredSeqSet, n.deferredAppends = 0, false, 0
	return seq, ownAppends, ok
}

// isTransition records the record's group and reports whether it CHANGED the session's
// remembered one. A session seen for the first time is a transition: the gateway has no
// prior state for it, and the roster path (Snapshot) is what seeds the sessions that
// already existed.
func (n *PushNotifier) isTransition(rec protocol.JournalRecord) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	prev, seen := n.lastGroup[rec.SessionID]
	n.lastGroup[rec.SessionID] = rec.Group
	return !seen || prev != rec.Group
}

// claimWindow reports whether session may wake the phone now, consuming its coalescing
// window if so. A suppressed wake (wrong group, disabled category) never reaches here, so
// it does not consume the window either.
//
// On refusal it also returns how long is LEFT of the window, which is the deferral's
// deadline: ADR-010 §4(b) defers to the end of the window that suppressed the wake, not to a
// fresh one.
func (n *PushNotifier) claimWindow(session string) (remaining time.Duration, ok bool) {
	now := n.now()
	n.mu.Lock()
	defer n.mu.Unlock()
	if last, seen := n.lastWake[session]; seen {
		if elapsed := now.Sub(last); elapsed < n.window {
			return n.window - elapsed, false
		}
	}
	n.lastWake[session] = now
	return 0, true
}

// isWakeWorthy selects the transitions an owner is actually waiting on.
//
// needs_input is the agent blocked on its owner; ready_for_review and completed are the
// agent handing work back. working is deliberately absent and is the negative half of the
// selection: a session going back to work is the one transition the owner does NOT need to
// be woken for, and it is by far the most frequent one -- pushing on it would burn the
// per-app FCM quota ADR-007 B16 named as the cost of dropping the socket.
func isWakeWorthy(g status.Group) bool {
	switch g {
	case status.GroupNeedsInput, status.GroupReadyForReview, status.GroupCompleted:
		return true
	default:
		return false
	}
}

// prefsCache memoizes a single LoadPrefs() call across preAppendObligation's peek
// (peekWakeDisposition) and maybeWake's immediate-path commit for the SAME Event call, so the
// two no longer pay two separate reads -- and, on failure, two separate setErr calls --
// for what is gating the identical record. Both calls compute the same category for the
// same rec (peekWakeDisposition's peek does not mutate the state maybeWake's commit reads), so
// the cached PREFERENCE is valid to reuse regardless of which category each call checks.
//
// It must NOT be threaded into the deferred-wake timer's own categoryEnabled call
// (maybeWake's send() closure): that read happens up to n.window later, on a separate
// goroutine, and ADR-010 §4(b) requires it be a FRESH read at send time, not this call's.
// A nil *prefsCache always loads fresh and caches nothing, which is what that call passes.
type prefsCache struct {
	prefs  PushPrefs
	err    error
	loaded bool
}

// categoryEnabled reports whether the user's preference permits waking for this group, and
// suppresses on ANY doubt.
//
// Suppression at the SENDER is the requirement, not local filtering on the phone
// (PB-PUSH-8): a push that is sent and then ignored still lets the provider observe token,
// timing and size, so only zero calls satisfy "disabled". An unreadable preference
// suppresses for the same reason -- the user is looking at a settings screen, and sending
// anyway contradicts a setting that may well say "off" while leaking exactly what the
// setting exists to withhold.
func (n *PushNotifier) categoryEnabled(g status.Group, cache *prefsCache) bool {
	if n.cfg.Prefs == nil {
		n.setErr(errNoPushPrefs)
		return false
	}
	prefs, err := n.loadPrefs(cache)
	if err != nil {
		return false
	}
	if g == status.GroupNeedsInput {
		return prefs.NeedsInput
	}
	return prefs.Finished
}

// loadPrefs returns the current preference, via cache when one is given and already
// loaded. A load failure is recorded through setErr exactly once -- at the point it
// actually happened -- regardless of how many callers subsequently share the cache.
func (n *PushNotifier) loadPrefs(cache *prefsCache) (PushPrefs, error) {
	if cache != nil && cache.loaded {
		return cache.prefs, cache.err
	}
	prefs, err := n.cfg.Prefs.LoadPrefs()
	if err != nil {
		n.setErr(err)
	}
	if cache != nil {
		cache.prefs, cache.err, cache.loaded = prefs, err, true
	}
	return prefs, err
}

// sealWake builds the content-free wake: an EMPTY plaintext under the wake key, carrying a
// durable monotonic seq and a real issued_at, with BOTH key ids left at zero.
//
// The zero key ids are the part that is easy to get wrong and impossible to notice.
// crypto.Envelope.Marshal emits a 62-byte CLEARTEXT header, so reusing the mailbox header
// shape verbatim would hand the push provider recipient_key_id and sender_key_id in the
// clear -- two stable identifiers linking every wake to one machine/device pair for the
// life of the epoch, which is strictly more than the "token, timing, size" PB-PUSH-3
// promises (ADR-007 B20). The wake needs neither: the relay routes it by the push_trigger
// target, and the phone opens it with the one wake key it holds.
//
// issued_at is stamped here, at the producer. The field is AAD-covered, so leaving it unset
// AUTHENTICATES a zero and every receiver computes an age of decades -- the exact failure
// SealControlReply hit once already. The seq comes from a DURABLE source for the mirror-image
// reason: a per-process counter restarts at 1 on every gateway restart, and the phone's
// persisted replay coordinate (PB-STATE-1) would then reject every wake after one.
func (n *PushNotifier) sealWake() ([]byte, error) {
	seq, err := n.cfg.Seq.Next()
	if err != nil {
		return nil, err
	}
	env, err := crypto.SealWake(n.cfg.WakeKey, crypto.EnvelopeHeader{
		Version:  crypto.VersionV1,
		EpochID:  n.cfg.EpochID,
		Seq:      seq,
		IssuedAt: n.now().UnixMilli(),
		// RecipientKeyID and SenderKeyID stay ZERO. See above.
	}, nil)
	if err != nil {
		return nil, err
	}
	return env.Marshal(), nil
}
