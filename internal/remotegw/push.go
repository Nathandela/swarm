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
}

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

	mu sync.Mutex
	// lastGroup is the group each session was last SEEN in. It is what distinguishes
	// "entered a push-worthy group" from "is in one": a journal re-reporting needs_input
	// for a session already in needs_input must not re-wake the phone.
	lastGroup map[string]status.Group
	// lastWake is when each session last woke the phone -- the per-session coalescing
	// state.
	lastWake map[string]time.Time
	lastErr  error
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
	return &PushNotifier{
		inner:     inner,
		cfg:       cfg,
		now:       now,
		window:    window,
		lastGroup: map[string]status.Group{},
		lastWake:  map[string]time.Time{},
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
func (n *PushNotifier) Event(rec protocol.JournalRecord) error {
	if err := n.inner.Event(rec); err != nil {
		return err
	}
	n.maybeWake(rec)
	return nil
}

// Terminal forwards a rendered terminal snapshot untouched. A snapshot is something the
// phone asked to watch while it is awake, so it wakes nobody.
func (n *PushNotifier) Terminal(session string, lines []string, cols, rows int) error {
	return n.inner.Terminal(session, lines, cols, rows)
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

// maybeWake decides whether this record is a hand-off worth waking the phone for and, if
// so, sends the wake. It never returns an error: see Event.
func (n *PushNotifier) maybeWake(rec protocol.JournalRecord) {
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
	// No transport configured is not a failure: "the system works without push"
	// (PB-PUSH-5). Checked before the preference so a gateway that never pushes at all
	// does not report a preference problem it does not have.
	if n.cfg.Pusher == nil {
		return
	}
	if !n.categoryEnabled(rec.Group) {
		return
	}
	if !n.claimWindow(rec.SessionID) {
		return
	}
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
func (n *PushNotifier) claimWindow(session string) bool {
	now := n.now()
	n.mu.Lock()
	defer n.mu.Unlock()
	if last, ok := n.lastWake[session]; ok && now.Sub(last) < n.window {
		return false
	}
	n.lastWake[session] = now
	return true
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

// categoryEnabled reports whether the user's preference permits waking for this group, and
// suppresses on ANY doubt.
//
// Suppression at the SENDER is the requirement, not local filtering on the phone
// (PB-PUSH-8): a push that is sent and then ignored still lets the provider observe token,
// timing and size, so only zero calls satisfy "disabled". An unreadable preference
// suppresses for the same reason -- the user is looking at a settings screen, and sending
// anyway contradicts a setting that may well say "off" while leaking exactly what the
// setting exists to withhold.
func (n *PushNotifier) categoryEnabled(g status.Group) bool {
	if n.cfg.Prefs == nil {
		n.setErr(errNoPushPrefs)
		return false
	}
	prefs, err := n.cfg.Prefs.LoadPrefs()
	if err != nil {
		n.setErr(err)
		return false
	}
	if g == status.GroupNeedsInput {
		return prefs.NeedsInput
	}
	return prefs.Finished
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
