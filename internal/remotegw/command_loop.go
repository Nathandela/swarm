package remotegw

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/transport"
)

// The production relay client is a Mailbox (read + append). This assertion pins the
// seam so a relay-client signature change is caught at compile time.
var _ Mailbox = (*relay.Client)(nil)

// It is ALSO the push transport: one connection carries both the mailbox and the wake
// trigger, which is why NewService discovers the pusher by type-asserting cfg.Relay rather
// than taking a second seam. That assertion is the risk this pin covers -- a drift in the
// client's PushTrigger signature would make it fail silently at runtime, degrading the
// gateway to no push with nothing failing anywhere.
var _ PushTriggerer = (*relay.Client)(nil)

// The gateway is a CommandForwarder via ForwardCommand. Pinned at compile time.
var _ CommandForwarder = (*Gateway)(nil)

// Mailbox is the relay seam the command loop needs: read the machine's own inbox
// (commands the phone appended to the machine's routing id) and append sealed
// replies to the phone's mailbox. relay.Client satisfies it.
type Mailbox interface {
	MailboxRead(ctx context.Context, cursor uint64) ([]relay.Item, error)
	// MailboxWait is the low-latency inbound seam (PB-NET-5, ADR-007 B7): it
	// blocks SERVER-side until an item past cursor exists and returns that bounded
	// page, so the command loop is driven by arrivals instead of by a cadence. It
	// is on THIS interface rather than an optional side interface a call site
	// type-asserts, because an optional wait would silently fall back to polling —
	// which is exactly the phone-side-only fix PB-NET-5 forbids.
	MailboxWait(ctx context.Context, cursor uint64) ([]relay.Item, bool, error)
	MailboxAppend(ctx context.Context, target string, env []byte) (uint64, error)
	MailboxAck(ctx context.Context, cursor uint64) error
}

type mailboxIncarnationState interface {
	SetMailboxIncarnation(string)
	ResetMailboxIncarnation()
	MailboxIncarnation() string
}

// CommandForwarder forwards a device-signed command to the daemon and returns the
// reply. (*Gateway).ForwardCommand satisfies it.
//
// It takes the OPENED RemoteCommand rather than a tail of its parts, which is what
// LeaseRouter.Begin has always done. The parts were (sessionID, cmd, launch) and IS-LIFE-4
// adds an ApproveReq body, so the alternative was a fourth nil-by-default parameter that
// every existing call site passes nil for -- the shape mobile/commands.go's commandBody
// comment already warns about, where "a positional nil is exactly the kind of argument that
// gets passed in the wrong slot". One argument that is the frame the gateway opened cannot be
// mispaired with itself, and the next body needs no signature change at all.
type CommandForwarder interface {
	ForwardCommand(op string, rc protocol.RemoteCommand) (protocol.Control, error)
}

// LeaseRouter is the live-input seam the command loop routes take_control and input
// frames through (A7 input Slice 5): Begin opens+leases a session's persistent conn,
// Input rides a keystroke/resize on that conn, End tears it down. It is the routing
// subset of *LeaseManager (Close is the runtime's, not the router's), extracted so
// the loop's dispatch is unit-testable with a fake. *LeaseManager satisfies it.
type LeaseRouter interface {
	Begin(cmd protocol.RemoteCommand) error
	Input(session string, f InputFrame) error
	End(session string)
	// Generation reports the daemon-granted lease generation for a session (0 when it
	// holds none). It is on the INTERFACE, not merely on *LeaseManager, because a
	// call-site type assert would make the lease confirmation OPTIONAL -- a router
	// without it would silently revert to sealing nothing, and PB-INPUT-2 would again
	// have no confirmed generation to gate keystrokes on.
	Generation(session string) uint64
	// OnSever installs the sink a lease death is reported to, fired ONCE per death. It is
	// on the interface for the same reason Generation is: an optional hook is a hook a
	// refactor drops, and the failure is silent -- the gateway simply stops telling the
	// phone, PB-INPUT-2 regresses to typing into a void, and every test stays green.
	OnSever(fn func(SeveredLease))
}

// *LeaseManager is the production LeaseRouter. Pinned at compile time.
var _ LeaseRouter = (*LeaseManager)(nil)

// TerminalWatchRouter is the terminal-peek seam the command loop routes terminal_watch /
// terminal_unwatch through (A7 F2 wiring): Watch starts a server-rendered peek for a
// session, Unwatch stops it. It is the routing subset of *TerminalWatcher (Close is the
// runtime's, not the router's), extracted so the loop's dispatch is unit-testable with a
// fake. *TerminalWatcher satisfies it.
type TerminalWatchRouter interface {
	Watch(session string)
	Unwatch(session string)
	// Renew extends a live watch's horizon (ADR-017 amendment T4-b). It is part of the
	// ROUTING subset because the horizon is a wire-visible contract: without a renewal
	// verb the horizon would expire every watch and the fallback would go dark.
	Renew(session string)
}

// *TerminalWatcher is the production TerminalWatchRouter. Pinned at compile time.
var _ TerminalWatchRouter = (*TerminalWatcher)(nil)

// JournalResyncer serves PB-SYNC-2's journal repair and the inbox's roster-only refresh.
// Both read one atomic daemon snapshot; rosterOnly controls whether the backlog is included
// in the one reseed frame. discardedBacklog is the phone's sealed proof that its explicit
// self-mailbox recovery completed, allowing a bounded roster reseed at the daemon's final
// cursor. *Gateway is the production implementation (Resync).
type JournalResyncer interface {
	Resync(ctx context.Context, from uint64, rosterOnly, discardedBacklog bool, recoveryToken string) error
}

// *Gateway is the production JournalResyncer. Pinned at compile time.
var _ JournalResyncer = (*Gateway)(nil)

// CommandBridgeConfig configures a CommandBridge.
type CommandBridgeConfig struct {
	Mailbox     Mailbox             // the machine's own relay mailbox (read) + the phone's (append)
	Forwarder   CommandForwarder    // forwards opened mutating commands to the daemon remote.sock
	Leases      LeaseRouter         // routes take_control + input frames to per-session lease conns (nil => input plane disabled)
	Watchers    TerminalWatchRouter // routes terminal_watch/terminal_unwatch to per-session peeks (nil => peek plane disabled)
	Key         crypto.ContentKey   // K_epoch content key shared with the phone
	EpochID     uint32              // the epoch the content key belongs to
	ReplyTarget string              // the phone's relay routing id (where replies are appended)
	ReplySeq    SeqSource           // durable reply seq high-water (nil => in-memory, non-durable)
	Inbound     InboundState        // durable INBOUND checkpoint: read cursor + per-stream replay high-water (nil => in-memory, non-durable)
	// Prefs is the durable push-preference custody the push_prefs verb writes (PB-PUSH-8,
	// PB-PUSH-10). It is the SAME object the PushNotifier reads, so a change the daemon
	// authorizes takes effect on the next transition. Nil disables the verb -- and disables
	// it LOUDLY: a bridge with no custody refuses push_prefs rather than answering OK to a
	// preference it did not store, which would leave the phone showing a setting the
	// machine has never heard of.
	Prefs PushPrefsSource
	// Resync serves journal_resync (PB-SYNC-2). Nil disables the verb -- and the phone's
	// stale journal then never clears, because nothing else can clear it (PB-SYNC-3: only
	// that channel's own repair does). Production always wires the gateway.
	Resync JournalResyncer
	// WaitTimeout is the per-MailboxWait upper bound Run applies (0 => defaultWaitTimeout),
	// the inbound sibling of RelayConfig.AppendTimeout. Production takes the default; the
	// field exists so a test can reach the timed-out path without sitting out the real
	// budget, which is how the outbound half is already tested.
	WaitTimeout time.Duration
	// RetainedRetryWait is the cancellable no-progress backoff after an authenticated
	// command remains in relay custody (nil => exponential production backoff). It is a
	// seam for deterministic Run tests; production callers leave it nil.
	RetainedRetryWait func(context.Context, int) error
	// replyPublication is shared with the production RelaySink. It orders the complete
	// reply publication against any reconcile that publishes ReplySeq.Issued().
	replyPublication *replyPublicationFence
}

// CommandBridge is the command-IN + reply half of the gateway (R-GW.3/.7): it polls
// the machine's relay mailbox for phone-authored sealed command envelopes, opens
// each under the epoch content key, forwards the device-signed command to the daemon
// (a blind conduit -- the daemon verifies the signature independently, R-POL.9), and
// seals the daemon's reply back to the phone's mailbox. It complements RelaySink's
// journal-OUT with the command-IN direction.
//
// The read cursor advances ONLY through items the bridge actually HANDLED (see
// processBatch): the relay mints relay.Item.Cursor and nothing authenticates it, so a
// cursor read off an item that could not be opened is a value the bridge has no evidence
// for. A malformed item is still stepped over by the next one that opens, so a poisoned
// envelope can neither wedge the loop nor be retried forever; per-item failures are
// aggregated into the returned error while the good items still process. Both the read
// cursor and the per-(sender,epoch) replay high-water are DURABLE (PB-GW-1, see the Inbound
// seam), so a restart resumes where the previous run stopped and refuses anything the relay
// retained beyond it. The daemon's own two-phase idempotency (D6) covers only the one bounded
// re-delivery a crash between a daemon forward and its persist can produce (see handle).
type CommandBridge struct {
	cfg              CommandBridgeConfig
	recv             *crypto.MailboxReceiver // per-(sender,epoch) seq guard against relay replay/reorder
	replySeq         SeqSource               // OUTBOUND reply seq (durable across restart, C2b)
	inbound          InboundState            // INBOUND checkpoint custody (durable across restart, PB-GW-1)
	replyPublication *replyPublicationFence

	// replyMu serialises the whole seq-allocate -> seal -> append of the OUTBOUND reply
	// bucket. It is separate from mu because sealReply's failure path calls setErr, which
	// takes mu. See sealReply for why the three steps cannot be split.
	replyMu sync.Mutex
	// pendingReply is the exact sealed envelope whose append outcome is unknown. It stays
	// under replyMu and is replayed byte-for-byte before any later reply can allocate a seq.
	// deliveredRetries remembers a pending reply redriven by a concurrent reply producer,
	// so the retained inbound command can consume on retry without sealing it a second time.
	pendingReply     *pendingCommandReply
	deliveredRetries map[[32]byte]struct{}

	mu          sync.Mutex
	cursor      uint64
	incarnation string
	authority   RelayAuthority
	highest     map[InboundStream]uint64 // in-memory mirror of the persisted per-stream high-water
	// cursorRecovery is true only after this connection received the relay's explicit
	// continuity-reset verdict. It authorises authenticated stale-envelope compaction while
	// redraining from zero; ordinary restart replays keep their longstanding surfaced-error
	// behavior and are never silently adopted as relay-cursor progress.
	cursorRecovery bool
	pollErr        error  // first error the Run loop's polls hit (see Err)
	replies        uint64 // waits the RELAY answered (see RelayReplies)
}

// NewCommandBridge returns a bridge over cfg, SEEDED from its durable inbound checkpoint
// (PB-GW-1). Seeding restores both halves of the guard: every persisted (sender, epoch)
// high-water goes into the receiver via crypto.MailboxReceiver.SeedHighWater, and the
// persisted read cursor into the mailbox cursor. Without the high-water half a restarted
// bridge's fresh receiver has seen == false for every stream and SKIPS the staleness check
// entirely (crypto/envelope.go), so a relay that never honoured an ack has every frame it
// still retains RE-ACCEPTED AT THE GUARD -- keystrokes included. (Only at the guard: §4.6
// WITHDREW the claim that a replayed keystroke reaches a PTY, which additionally requires a
// seq-regressed phone; see inbound_replay_class_test.go's scope note.)
//
// A nil cfg.Inbound defaults to non-durable in-memory state (resets on restart), as does a
// nil cfg.ReplySeq; production wires durable files for both. Seeding cannot fail:
// OpenInboundState validated custody at open, so Load is infallible and this signature
// stays unchanged.
func NewCommandBridge(cfg CommandBridgeConfig) *CommandBridge {
	replySeq := cfg.ReplySeq
	if replySeq == nil {
		replySeq, _ = OpenSeqSource("") // in-memory, cannot error
	}
	inbound := cfg.Inbound
	if inbound == nil {
		inbound, _ = OpenInboundState("", "") // in-memory, cannot error
	}
	replyPublication := cfg.replyPublication
	if replyPublication == nil {
		replyPublication = &replyPublicationFence{}
	}
	b := &CommandBridge{
		cfg:              cfg,
		recv:             crypto.NewMailboxReceiver(),
		replySeq:         replySeq,
		inbound:          inbound,
		highest:          map[InboundStream]uint64{},
		deliveredRetries: map[[32]byte]struct{}{},
		replyPublication: replyPublication,
	}
	ck := inbound.Load()
	for st, seq := range ck.Highest {
		b.recv.SeedHighWater(st.Sender, st.Epoch, seq)
		b.highest[st] = seq
	}
	b.cursor = ck.Cursor
	b.incarnation = ck.Incarnation
	b.authority = ck.Relay
	if mailbox, ok := cfg.Mailbox.(mailboxIncarnationState); ok {
		mailbox.SetMailboxIncarnation(ck.Incarnation)
	}
	// PB-INPUT-2: a lease that dies under a live gateway must be sealed back to the phone
	// (lease_sever.go). cfg.Leases is nilable -- "nil => input plane disabled" -- and a
	// registration that dereferenced it would turn a supported configuration into a crash
	// at construction.
	if cfg.Leases != nil {
		cfg.Leases.OnSever(b.sealSevered)
	}
	return b
}

// Err returns the FIRST error the Run loop's polls hit, or nil -- the root cause, not the
// latest symptom, mirroring RelaySink.Err. Run's poll errors are non-fatal, but they are
// not nothing: a state dir that is full or read-only fails every checkpoint persist, and
// live input persists BEFORE the PTY write (handle), so the bridge then DROPS every
// keystroke for as long as the condition lasts. Without this an operator has no signal at
// all that it is happening.
func (b *CommandBridge) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pollErr
}

// RelayReplies counts the bounded waits the RELAY ANSWERED -- and, on the round-4
// compatibility poll arm, the bounded reads it answered -- not the ops issued, and not
// the frames that came back in them. An idle but healthy link produces one per
// server-side wait ceiling (or one per poll cadence); a relay that completes the
// handshake and then goes quiet produces none, however long its socket stays up.
//
// It is the gateway's evidence of PROGRESS, which is what the reconnect backoff resets on
// (Service.Progressed). A count is deliberately cheaper than a timestamp: nothing here
// needs to know WHEN the relay last answered, only whether this connection ever carried
// anything, and a monotonic count cannot be confused by a clock.
func (b *CommandBridge) RelayReplies() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.replies
}

// Cursor is the highest relay mailbox cursor the bridge has consumed (its durable
// resume point).
func (b *CommandBridge) Cursor() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cursor
}

// There is deliberately NO SetCursor. It existed, PB-GW-1's text names it, and its ONE caller
// was processBatch's advance-past-every-item-read -- the defect itself. The resume point now
// moves only inside consume, under the same lock that carries the replay high-water, and a
// public setter would be a second way to move a coordinate that must have exactly one.

// PollOnce reads every mailbox item past the current cursor, processes each (open ->
// forward -> seal reply), and returns how many were forwarded successfully. Discardable
// malformed/wrong-key failures are joined and skipped so hostile junk cannot pin the page.
// An authenticated command whose daemon/reply transaction is incomplete instead stops the
// page: a later cursor must never strand its retained operation. The cursor advances only
// past items that were fully HANDLED (processBatch).
func (b *CommandBridge) PollOnce(ctx context.Context) (int, error) {
	items, err := b.readMailboxPage(ctx)
	if err != nil {
		return 0, err
	}
	processed, maxCursor, errs := b.processBatch(ctx, items)
	if len(items) == 0 {
		b.completeCursorRecovery()
	}
	if maxCursor > 0 {
		// Ack durably purges consumed items from the relay's mailbox store, so a
		// restarted bridge never re-reads them. A failed ack surfaces as an error but
		// must not lose the cursor advance above -- the next poll will simply try to
		// ack forward again. The ack is an OPTIMISATION, never the guard: a relay that
		// does not honour it can still replay nothing, because the durable high-water
		// refuses every retained frame.
		if err := b.cfg.Mailbox.MailboxAck(ctx, maxCursor); err != nil {
			errs = append(errs, fmt.Errorf("ack cursor %d: %w", maxCursor, err))
		}
	}
	return processed, errors.Join(errs...)
}

// readMailboxPage performs one bounded continuity repair. A reset relay store can restart
// its per-mailbox cursors at one while this bridge durably resumes at a larger coordinate;
// the relay's explicit sentinel is the only honest evidence that lowering that coordinate
// is required. Retry exactly once from zero so a broken or hostile relay cannot turn the
// repair signal into an unbounded local loop.
func (b *CommandBridge) readMailboxPage(ctx context.Context) ([]relay.Item, error) {
	items, err := b.cfg.Mailbox.MailboxRead(ctx, b.Cursor())
	if !errors.Is(err, relay.ErrMailboxCursorResetRequired) {
		if err == nil {
			err = b.adoptMailboxIncarnation()
		}
		return items, err
	}
	if err := b.rewindMailboxCursor(); err != nil {
		return nil, err
	}
	items, err = b.cfg.Mailbox.MailboxRead(ctx, 0)
	if err == nil {
		err = b.adoptMailboxIncarnation()
	}
	return items, err
}

// rewindMailboxCursor persists the reset before publishing it in memory, preserving every
// authenticated (sender, epoch) high-water. Holding b.mu across custody prevents an ordinary
// checkpoint Save captured before the reset from landing afterwards and raising the cursor
// back to the retired relay generation.
func (b *CommandBridge) rewindMailboxCursor() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.inbound.RewindCursor(b.authority); err != nil {
		return fmt.Errorf("rewind relay mailbox cursor: %w", err)
	}
	b.cursor = 0
	b.incarnation = ""
	b.cursorRecovery = true
	if mailbox, ok := b.cfg.Mailbox.(mailboxIncarnationState); ok {
		mailbox.ResetMailboxIncarnation()
	}
	return nil
}

func (b *CommandBridge) adoptMailboxIncarnation() error {
	mailbox, ok := b.cfg.Mailbox.(mailboxIncarnationState)
	if !ok {
		return nil
	}
	incarnation := mailbox.MailboxIncarnation()
	if incarnation == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.incarnation == incarnation {
		return nil
	}
	previous := b.incarnation
	b.incarnation = incarnation
	ck := InboundCheckpoint{Cursor: b.cursor, Incarnation: b.incarnation, Highest: make(map[InboundStream]uint64, len(b.highest)), Relay: b.authority}
	for st, seq := range b.highest {
		ck.Highest[st] = seq
	}
	if err := b.inbound.Save(ck); err != nil {
		b.incarnation = previous
		return fmt.Errorf("persist relay mailbox incarnation: %w", err)
	}
	return nil
}

// processBatch handles one batch of mailbox items and returns how many forwarded
// successfully, the highest cursor CONSUMED (0 when the batch consumed none), and the
// per-item failures. It is shared by the wait-driven Run and by PollOnce, which
// differ only in how the batch was fetched and where the ack goes.
//
// THE CURSOR IS NOT READ OFF THE ITEMS HERE, and that is the fence.
// This used to take the batch maximum from every item BEFORE handle(), so a relay -- which
// MINTS relay.Item.Cursor and is the declared adversary -- needed no key at all: six bytes of
// garbage beside a cursor of its choosing moved the durable resume point past every real
// command, and the ack that followed ordered the relay to compact away the backlog it had
// just made undeliverable. The resume point now moves ONLY through consume, i.e. only for an
// item the bridge actually opened and handled, so an item that fails to open contributes
// nothing at all.
//
// The no-wedge property that motivated the old rule survives, because the advance is a
// MAXIMUM over handled items rather than a contiguous prefix: an item that can never open
// (garbage, or a frame sealed under a superseded epoch) is stepped over by the next item that
// does, and only one sitting at the mailbox TAIL is re-read -- the same bounded cost the
// phone's drain already accepts for the same reason, paced by transport.DrainPacer.
//
// What this does NOT fence is the VALUE a HANDLED item carries: that is the relay's own
// coordinate and nothing authenticates it, so a relay that rewrites the cursor of a genuine
// phone-sealed frame still moves the resume point. Bounding that needs a limit on how far a
// cursor may move per page, which no requirement states; it is recorded as a residual rather
// than invented here.
func (b *CommandBridge) processBatch(ctx context.Context, items []relay.Item) (int, uint64, []error) {
	processed := 0
	var errs []error
	before := b.Cursor()
	for _, it := range items {
		if err := b.handle(ctx, it); err != nil {
			var retained retainedCommandError
			if errors.As(err, &retained) {
				// This envelope authenticated and may already have produced a durable daemon
				// outcome, but its terminal reply is not in known relay custody. Nothing after
				// it may advance the durable cursor: doing so would permanently strand that
				// outcome and the phone's operation would remain pending forever.
				errs = append(errs, fmt.Errorf("cursor %d: %w", it.Cursor, err))
				break
			}
			if b.cursorRecoveryActive() {
				// An explicit relay-cursor rewind deliberately re-serves envelopes whose
				// authenticated seq is already durable. Authenticate one with a fresh
				// receiver before advancing only the relay coordinate: MailboxReceiver's
				// fast stale path checks the visible header before AEAD, so ErrStaleSeq
				// alone is never sufficient authority to compact an item.
				if replayErr := b.consumeDurableReplay(it); replayErr == nil {
					errs = append(errs, fmt.Errorf("cursor %d: %w", it.Cursor, err))
					continue
				} else {
					errs = append(errs, fmt.Errorf("cursor %d: %w", it.Cursor, errors.Join(err, replayErr)))
					continue
				}
			}
			errs = append(errs, fmt.Errorf("cursor %d: %w", it.Cursor, err))
			continue
		}
		processed++
	}
	// consume already committed each handled item's cursor durably, in the order that
	// item's action class requires. Nothing is persisted here: a batch that handled nothing
	// has nothing to record, and re-saving an unchanged checkpoint would cost one bolt
	// fsync per empty poll on the keystroke path.
	consumed := b.Cursor()
	if consumed <= before {
		return processed, 0, errs
	}
	return processed, consumed, errs
}

func (b *CommandBridge) cursorRecoveryActive() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cursorRecovery
}

func (b *CommandBridge) completeCursorRecovery() {
	b.mu.Lock()
	b.cursorRecovery = false
	b.mu.Unlock()
}

// consumeDurableReplay authenticates a stale envelope without dispatching it, then advances
// only the relay-owned storage cursor when the signed stream/seq is at or below the durable
// replay high-water. This is what lets a post-reset rewind compact a page made entirely of
// already-applied commands without repeating any daemon or PTY side effect.
func (b *CommandBridge) consumeDurableReplay(it relay.Item) error {
	env, err := crypto.ParseEnvelope(it.Envelope)
	if err != nil {
		return fmt.Errorf("parse replay: %w", err)
	}
	fresh := crypto.NewMailboxReceiver()
	frame, err := OpenMailboxFrameAt(fresh, b.cfg.Key, it.Envelope, time.UnixMilli(env.Header.IssuedAt))
	if err != nil {
		return fmt.Errorf("authenticate replay: %w", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	durable := b.inbound.Load()
	if durable.Relay != b.authority {
		return errRelayAuthorityChanged
	}
	if high, ok := durable.Highest[frame.Stream]; !ok || frame.Seq > high {
		return fmt.Errorf("replay seq %d is not covered by a durable high-water", frame.Seq)
	}
	if it.Cursor <= b.cursor {
		return nil
	}
	previous := b.cursor
	b.cursor = it.Cursor
	ck := cloneCheckpoint(durable)
	ck.Cursor = b.cursor
	if err := b.inbound.Save(ck); err != nil {
		b.cursor = previous
		return fmt.Errorf("persist replay cursor %d: %w", it.Cursor, err)
	}
	return nil
}

// gatewayHelloCaps is every capability the gateway sidecar's r_hello asks the relay
// for -- the machine-hop sibling of mobile's helloRequestCaps, kept as one var for the
// same reason (Opus round-3 nit 6): the cross-package fence
// TestCommitteeR4_GatewayHelloCapsAreServedByTheShippedRelay asserts the shipped relay
// grants every one of them, so the two sets cannot drift apart silently. The gateway
// deliberately omits "presence" (it never asks) and "rendezvous" (pairing's, spoken on a
// raw connection by the machine CLI, not by this sidecar).
var gatewayHelloCaps = []string{"mailbox", "push", "wait", relay.CapabilityMailboxRecovery}

// CapabilityHello is the optional per-connection negotiation seam (codex round-3
// blocker 1, bead agents-tracker-10ar): the r_hello exchange through which a relay
// advertises which optional ops it serves. *relay.Client satisfies it (pinned below);
// a Mailbox seam that does not -- every unit-test fake -- simply keeps the wait it
// implements, so the seam stays optional-to-OFFER while the wait op itself stays
// mandatory on Mailbox (an optional wait would silently fall back to polling against
// modern relays too, the phone-side-only fix PB-NET-5 forbids).
type CapabilityHello interface {
	Hello(ctx context.Context, version int, caps []string) (int, []string, error)
}

var _ CapabilityHello = (*relay.Client)(nil)

// negotiateWait derives THIS connection's wait verdict from its r_hello exchange,
// exactly as the phone's negotiateWaitSupport does: a hello that does not advertise
// "wait" -- a pre-wait relay -- selects the compatibility poll outright, because a
// blindly-probed mailbox_wait against such a relay is answered with an uncorrelated
// in-order MsgError the client's pump drops as unsolicited, so every wait ends as a
// swallowed timeout and commands stop flowing forever while the relay is perfectly
// usable through mailbox_read. A refused or failed hello reads as unsupported rather
// than an error, for the phone's reason: the poll works against every relay, and if the
// hello failed because the link is dying the poll's first bounded read discovers that.
//
// A Service is one relay generation (cmd/swarm-remote builds a fresh one per redial),
// so Run entry IS the connection's start and the verdict is per connection by
// construction: an upgraded relay is re-evaluated for free on the next redial.
func (b *CommandBridge) negotiateWait(ctx context.Context) bool {
	h, ok := b.cfg.Mailbox.(CapabilityHello)
	if !ok {
		return true // no hello to consult; the seam's own MailboxWait is the contract
	}
	hctx, cancel := context.WithTimeout(ctx, relay.DefaultCallTimeout)
	defer cancel()
	_, caps, err := h.Hello(hctx, relay.ProtocolVersion, gatewayHelloCaps)
	if err != nil {
		return false
	}
	for _, c := range caps {
		if c == "wait" {
			return true
		}
	}
	return false
}

// compatPollInterval is the compatibility fallback's cadence against a relay whose
// hello does not advertise "wait" -- the same 500 ms the phone's drainPoll uses
// (playbook section 10; internal/remote/transport doc.go names this as the one
// surviving poll). It is a fallback for OLD relays only: a modern relay's command-IN
// stays the bounded server-side wait.
const compatPollInterval = 500 * time.Millisecond

// runPoll is the compatibility arm: MailboxRead at compatPollInterval, exactly the
// phone drainPoll's shape -- an immediate next read only on PROGRESS (the durable
// cursor moved), never merely on a non-empty page, so one undecodable item at the
// mailbox tail cannot spin the loop at full speed against the relay's ops budget
// (PB-SYNC-6's argument, restated for this hop). PollOnce carries the shared batch
// handling and the inline ack; at this cadence the metered op rate is bounded by the
// interval itself.
func (b *CommandBridge) runPoll(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		before := b.Cursor()
		if _, err := b.PollOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Per-item failures and transport failures arrive joined; both are
			// stashed for Err(), and neither counts as link progress below (a
			// conservative under-count only delays a backoff reset, where crediting
			// a dead link would defeat what Progressed exists to prove).
			b.setErr(err)
		} else {
			// The relay ANSWERED a read cleanly: the same link-progress evidence a
			// completed wait is on the modern arm (Progressed resets the reconnect
			// backoff on it).
			b.mu.Lock()
			b.replies++
			b.mu.Unlock()
		}
		// PROGRESS is judged on the cursor alone, error or not: a poisoned item
		// beside good ones joins an error while the cursor still advances, and a
		// drain that slept on it would throttle a real backlog to the compatibility
		// cadence (the phone's drainPoll applies the same rule).
		if b.Cursor() > before {
			continue // a real backlog drains at full speed: it advances the cursor
		}
		t := time.NewTimer(compatPollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Run drives the command-IN path off the relay's server-side wait until ctx is cancelled,
// returning ctx.Err(). Each wait is bounded HERE, by this loop, at defaultWaitTimeout.
//
// There is NO poll cadence, and dropping it is half of PB-NET-5, not a detail: the fixed
// 500 ms command-IN poll this replaces is what ADR-007:461 calls "unusable for live
// typing", and a phone-side-only fix passes the letter of the acceptance criterion while
// typing stays 500 ms-gated. Tuning the interval down is not the fix either -- it trades
// the latency failure for a quota one, since a 100 ms poll is 10 reads/s against §6.0's
// 3 reads/s per hop.
//
// The same §6.0 budget and the same adaptive pacer bind BOTH hops, so this loop uses the
// transport package's DrainPacer and AckBatcher rather than restating either. Acks ride
// the batcher, off the delivery path: a relay ack is one synchronous bolt fsync (p50
// 30.8 ms / max 129.2 ms measured) and taking one between an item's arrival and the next
// wait would put most of the p50 input budget on the keystroke path.
//
// Wait errors are non-fatal (a transient relay error should not tear the bridge down) but
// they are not swallowed: the first is stashed for Err(), so a bridge that is dropping
// every inbound frame is observable rather than silent.
func (b *CommandBridge) Run(ctx context.Context) error {
	// Capabilities first, once per connection (Run entry is the connection's start --
	// a Service is one relay generation): a relay whose hello does not advertise
	// "wait" gets the documented MailboxRead compatibility poll, never a blind
	// mailbox_wait probe whose refusal it can only swallow (codex round-3 blocker 1,
	// bead agents-tracker-10ar).
	if !b.negotiateWait(ctx) {
		return b.runPoll(ctx)
	}
	acks := transport.NewAckBatcher(func(actx context.Context, cursor uint64) error {
		return b.cfg.Mailbox.MailboxAck(actx, cursor)
	})
	ackCtx, stopAcks := context.WithCancel(ctx)
	acksDone := make(chan struct{})
	go func() { defer close(acksDone); acks.Run(ackCtx) }()
	defer func() { stopAcks(); <-acksDone }()

	pacer := transport.NewDrainPacer()
	retainedAttempts := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := pacer.Pace(ctx); err != nil {
			return ctx.Err()
		}
		// ONE deadline per wait, and it is cancelled rather than deferred: a defer in this
		// loop would accumulate one live timer per cycle for the life of the bridge.
		waitCtx, cancelWait := context.WithTimeout(ctx, b.waitTimeout())
		ackGeneration := acks.Generation()
		items, _, err := b.cfg.Mailbox.MailboxWait(waitCtx, b.Cursor())
		cancelWait()
		pacer.Observe(len(items))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, relay.ErrMailboxCursorResetRequired) {
				// Cross the ack-generation barrier before the relay client forgets the
				// retired incarnation in rewindMailboxCursor.
				acks.Reset()
				if resetErr := b.rewindMailboxCursor(); resetErr != nil {
					b.setErr(resetErr)
				} else {
					continue
				}
			}
			if waitCtx.Err() != nil {
				// Name the condition. relay.mailboxWait reports every context ending as
				// "mailbox wait cancelled", which reads as an orderly shutdown; what this
				// actually is, is the relay not answering.
				//
				// RECORDED, NOT SURFACED, and the distinction is ADR-007 B114's. Err() is the
				// only channel this condition has and NOTHING IN PRODUCTION READS IT -- not this
				// bridge's Err, not RelaySink's, not PushNotifier's; the tree contains no
				// non-test caller of any of the three. An operator therefore learns nothing
				// today. That gap is older and wider than this bound, so it is named here rather
				// than closed here: a stored error is not a reported one.
				err = fmt.Errorf("relay answered no mailbox wait within %v: %w", b.waitTimeout(), err)
			}
			b.setErr(err)
			// Back off, or a relay that refuses every wait becomes a spin loop.
			t := time.NewTimer(commandRetryDelay)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
			continue
		}
		if err := b.adoptMailboxIncarnation(); err != nil {
			b.setErr(err)
			continue
		}
		// The relay ANSWERED. Recorded before the batch is handled, because what this
		// counts is the link carrying traffic, not the gateway liking what arrived.
		b.mu.Lock()
		b.replies++
		b.mu.Unlock()
		_, maxCursor, errs := b.processBatch(ctx, items)
		if len(items) == 0 {
			b.completeCursorRecovery()
		}
		if maxCursor > 0 {
			acks.RecordGeneration(maxCursor, ackGeneration)
		}
		batchErr := errors.Join(errs...)
		if batchErr != nil {
			b.setErr(batchErr)
		}
		var retained retainedCommandError
		if errors.As(batchErr, &retained) {
			retainedAttempts++
			// This page made no progress through the retained command. MailboxWait will
			// therefore return the same item immediately; without an explicit backoff it
			// spends DrainPacer's initially-full minute bucket in a burst and hammers both
			// the daemon idempotency path and the already-failing reply append. The wait is
			// outside replyMu, so lease-sever notices remain free to redrive the pending
			// exact envelope while this command sleeps.
			if err := b.waitRetainedRetry(ctx, retainedAttempts); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
			continue
		}
		retainedAttempts = 0
	}
}

func (b *CommandBridge) waitRetainedRetry(ctx context.Context, attempt int) error {
	if b.cfg.RetainedRetryWait != nil {
		return b.cfg.RetainedRetryWait(ctx, attempt)
	}
	exponent := attempt - 1
	if exponent < 0 {
		exponent = 0
	}
	if exponent > 5 {
		exponent = 5
	}
	delay := time.Second << exponent
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// commandRetryDelay bounds how fast the wait loop retries a failing relay. It is not a
// poll cadence: it applies only after an error, and a healthy loop never reaches it.
const commandRetryDelay = 250 * time.Millisecond

// serverWaitCeiling is §6.0's "Server-side wait (long-poll) maximum | 25 s" (PB-NET-5),
// transcribed because it is the number the gateway's own bound has to clear. It is the
// RELAY's ceiling, which is exactly why it cannot be the gateway's bound.
const serverWaitCeiling = 25 * time.Second

// defaultWaitTimeout bounds ONE MailboxWait from the caller's side.
//
// IT MUST EXIST HERE BECAUSE THE ONLY OTHER PARTY THAT COULD END THE WAIT IS THE ADVERSARY.
// relay.MailboxWait is unbounded by contract -- relay.TestCallDeadline_TheLongPollIsNotBoundedByIt
// pins that the long poll ends on the CALLER's deadline and not on the connection's exchange
// bound, because a poll cut by the generic call timeout would turn PB-NET-5's low-latency
// inbound seam into a timeout loop. The corollary is that some caller must declare a deadline,
// and this loop was not one: it handed MailboxWait the bridge's lifetime context, which
// cmd/swarm-remote cancels only on a signal. Against a relay that completes the websocket
// handshake and then answers nothing -- no ping and no read deadline on the client conn, so the
// connection never even looks dead -- a wait was measured STILL PARKED AFTER 70 s, 2.8x the
// ceiling it was assumed to inherit. What parks is the command-IN loop, so the machine stops
// processing keystrokes, take_control and kill with no error and no state change, while the
// phone's appends keep succeeding and the UI keeps reading online. ADR-007 B94(1)'s defect one
// hop over, and reachable with no adversary at all: a half-open TCP after a WiFi -> cellular
// handoff answers nothing in the same way.
//
// EVERY TERM IS §6.0'S, AND THAT IS DELIBERATE. B99's lesson is that a bound an implementer
// re-derives locally is not a budget, so this value is composed rather than chosen: the relay's
// own 25 s wait ceiling, plus PB-NET-7's 10 s non-wait request timeout for the two frames that
// carry the wait out and its reply back. A relay that honours the ceiling is therefore NEVER cut
// off early -- which is the property that keeps the seam a long poll -- and a relay that honours
// nothing is ended one request budget later.
//
// A LATER BOUND IS ALSO WHY THIS IS NOT A RECONNECT. The loop treats the deadline as any other
// wait error: it records it for Err() and issues the next wait after commandRetryDelay, so a
// link that comes back resumes on the following cycle at the same cursor, with no torn-down
// connection and no lost inbound state.
const defaultWaitTimeout = serverWaitCeiling + relay.DefaultCallTimeout

// waitTimeout is the configured per-wait bound, or defaultWaitTimeout.
func (b *CommandBridge) waitTimeout() time.Duration {
	if b.cfg.WaitTimeout > 0 {
		return b.cfg.WaitTimeout
	}
	return defaultWaitTimeout
}

// setErr records the first poll error; later ones are dropped so Err() keeps pointing at
// the root cause (RelaySink.setErrLocked's rule).
func (b *CommandBridge) setErr(err error) {
	b.mu.Lock()
	if b.pollErr == nil {
		b.pollErr = err
	}
	b.mu.Unlock()
}

// handle opens ONE mailbox envelope (a single Accept advancing the shared seq
// high-water) and routes it by kind: an input frame rides the lease conn of the
// session named INSIDE its sealed plaintext; a command dispatches on its action
// (take_control/take_control_end to the lease plane, kill/delete/launch to the
// daemon). A replayed seq is rejected by the single Accept before any dispatch, so it
// can neither double-lease nor double-forward nor reach Input.
//
// PERSIST ORDERING (PB-GW-3). No local transaction can atomically span the persisted
// high-water, the persisted cursor, an external PTY/daemon side effect and the relay ack,
// so the order of the persist relative to the dispatch is chosen PER CLASS by which
// failure is cheaper:
//
//   - LIVE INPUT persists BEFORE the PTY write. A crash in between LOSES the keystroke,
//     which the user retypes; the alternative -- a keystroke replayed into a live shell --
//     is a corrupted command line. Input is live-only by ADR-007 D7, so loss is allowed and
//     duplication is not. A failed persist therefore DROPS the frame rather than typing it.
//   - EVERY OTHER CLASS dispatches BEFORE persisting, because loss is not allowed there: a
//     dropped kill is a live session its owner believes is dead. A crash in between
//     re-delivers exactly once more (the retained frame is re-served, the high-water was
//     never raised), carrying the SAME operation_id so the daemon's durable two-phase
//     idempotency suppresses the duplicate; watch/unwatch is idempotent per session and
//     simply converges. Once the persist lands the window CLOSES -- the next restart
//     refuses the retained frame at the guard.
func (b *CommandBridge) handle(ctx context.Context, it relay.Item) error {
	frame, err := OpenMailboxFrame(b.recv, b.cfg.Key, it.Envelope)
	if err != nil {
		return fmt.Errorf("open frame: %w", err)
	}
	if frame.Kind == FrameInput {
		if err := b.consume(frame, it.Cursor); err != nil {
			return err
		}
		return b.routeInput(frame)
	}
	if redriven, err := b.redrivePendingReply(ctx, frame.Command.OperationID); redriven {
		if err != nil {
			return retainedCommandError{err: errors.Join(err, b.restoreReceiverFromCheckpoint())}
		}
		return b.consume(frame, it.Cursor)
	}
	if err := b.routeCommand(ctx, frame.Command); err != nil {
		// A SEALED REFUSAL IS FINAL, SO THE ITEM IS STILL CONSUMED (agents-tracker-2pnu F3).
		// Every other failure here is a fact about the world -- the daemon was down, the relay
		// refused the append -- and leaving it unconsumed is what lets the next drain deliver
		// it. A refusal is a fact about THIS BINARY: it has no arm for the action, or the arm's
		// body is not in the envelope, and neither changes on a retry. Left unconsumed the
		// cursor never passes it, the relay is never acked, and the phone -- which already
		// resolved the operation on the reply that went out -- gets a second copy on every
		// restart. Consumed, the reason still rides out in the poll error below.
		var refused refusedCommand
		if !errors.As(err, &refused) {
			// OpenMailboxFrame already advanced recv's in-memory high-water. Because the
			// command was deliberately not consumed, restore that guard from durable custody
			// before returning; otherwise the very retry promised here is rejected stale in
			// this gateway generation. processBatch recognises the wrapper and stops the page,
			// so no later command can commit a cursor beyond this retained item.
			return retainedCommandError{err: errors.Join(err, b.restoreReceiverFromCheckpoint())}
		}
		if consumeErr := b.consume(frame, it.Cursor); consumeErr != nil {
			return errors.Join(err, consumeErr)
		}
		return err
	}
	return b.consume(frame, it.Cursor)
}

// retainedCommandError marks an authenticated non-input command whose external work is not
// fully acknowledged. The command remains in relay custody and must form a page barrier until
// its daemon action and terminal reply converge; malformed unauthenticated frames deliberately
// do not carry this marker and retain the historical skip-past-poison behaviour.
type retainedCommandError struct{ err error }

func (r retainedCommandError) Error() string { return r.err.Error() }

func (r retainedCommandError) Unwrap() error { return r.err }

// restoreReceiverFromCheckpoint rolls the one-phase MailboxReceiver back to the last state
// durable consume() actually committed. OpenMailboxFrame authenticates before mutating this
// receiver, but routeCommand has external failure points after that mutation; reconstructing it
// is the local rollback that makes a retained envelope retryable in the same process.
func (b *CommandBridge) restoreReceiverFromCheckpoint() error {
	ck := b.inbound.Load()
	if ck.Relay != b.authority {
		return errRelayAuthorityChanged
	}
	recv := crypto.NewMailboxReceiver()
	highest := make(map[InboundStream]uint64, len(ck.Highest))
	for st, seq := range ck.Highest {
		recv.SeedHighWater(st.Sender, st.Epoch, seq)
		highest[st] = seq
	}
	b.mu.Lock()
	b.recv = recv
	b.cursor = ck.Cursor
	b.incarnation = ck.Incarnation
	b.highest = highest
	b.mu.Unlock()
	return nil
}

// refusedCommand marks a refusal this build can never take back: the reply IS sealed and the
// item must be consumed. It is a type rather than a sentinel because the reason it carries is
// the operator's only account of what was refused -- see handle.
type refusedCommand struct{ reason error }

func (r refusedCommand) Error() string { return r.reason.Error() }

func (r refusedCommand) Unwrap() error { return r.reason }

// consume records that frame's seq has been taken off its (sender, epoch) stream at
// mailbox cursor, and persists that fact. It is the ONLY writer of the inbound replay
// high-water: after it returns nil, a restarted bridge seeded from this checkpoint refuses
// that seq -- and every seq below it -- with crypto.ErrStaleSeq.
func (b *CommandBridge) consume(frame MailboxFrame, cursor uint64) error {
	b.mu.Lock()
	if frame.Seq > b.highest[frame.Stream] {
		b.highest[frame.Stream] = frame.Seq
	}
	if cursor > b.cursor {
		b.cursor = cursor
	}
	b.mu.Unlock()
	if err := b.saveCheckpoint(); err != nil {
		return fmt.Errorf("persist inbound seq %d: %w", frame.Seq, err)
	}
	return nil
}

// saveCheckpoint hands the current in-memory checkpoint to durable custody. The custody
// merges monotonically, so a concurrent poll can never lower what another already wrote.
func (b *CommandBridge) saveCheckpoint() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	ck := InboundCheckpoint{Cursor: b.cursor, Incarnation: b.incarnation, Highest: make(map[InboundStream]uint64, len(b.highest)), Relay: b.authority}
	for st, seq := range b.highest {
		ck.Highest[st] = seq
	}
	return b.inbound.Save(ck)
}

// routeInput hands a keystroke/resize frame to the lease conn of the session named
// INSIDE the sealed frame (InputFrame.Session). Because that id is bound under the
// AEAD, it is authentic end to end: the untrusted relay can drop or reorder sealed
// frames but cannot alter their contents, so an input for session B always names B --
// if B's take_control was dropped, B has no lease and the frame is dropped, never
// riding another session's live lease (A7 cross-session misroute).
//
// The frame is dropped -- never routed -- when it names no target (empty Session) or
// when it follows a mailbox gap (frame.Gap: a preceding seq was skipped, so a frame --
// possibly the target's take_control -- was lost and the routing state is uncertain).
// A dropped keystroke is safer than one misrouted onto another session's lease.
func (b *CommandBridge) routeInput(f MailboxFrame) error {
	if b.cfg.Leases == nil {
		return nil
	}
	if f.Input.Session == "" || f.Gap {
		return nil
	}
	return b.cfg.Leases.Input(f.Input.Session, f.Input)
}

// routeCommand dispatches an opened RemoteCommand: take_control opens the session's
// lease; take_control_end tears it down; every other action (kill/delete/launch) is
// forwarded to the daemon on a fresh conn and its reply is sealed back to the phone.
// take_control and its teardown carry their OWN target session, so no mutable focus
// state is kept -- input frames route by the session sealed into each frame, not by
// the last take_control (routeInput).
func (b *CommandBridge) routeCommand(ctx context.Context, rc protocol.RemoteCommand) error {
	switch rc.Action {
	case protocol.ActionTakeControl:
		if b.cfg.Leases == nil {
			return nil
		}
		// Both outcomes are CONFIRMED to the phone (lease_confirm.go): silence is
		// indistinguishable from a slow grant, which is how a keystroke gets sent against
		// a lease that does not exist.
		return b.confirmLease(ctx, rc, b.beginLeaseCtx(ctx, rc))
	case protocol.OpTakeControlEnd:
		// take_control_end has no signed Action constant; the daemon op string is its
		// wire action. Tearing down the lease conn (End) is the phone's take_control_end.
		if b.cfg.Leases == nil {
			return nil
		}
		b.cfg.Leases.End(rc.Session)
		return nil
	case protocol.ActionTerminalWatch:
		// A READ, and it GRANTS NO INPUT AUTHORITY (ADR-017 T4). It is NOT forwarded to
		// the daemon's device authenticator (an unsigned watch); the peek plane may be
		// disabled (nil Watchers).
		//
		// THE SESSION CAPABILITY GATE IS THE DAEMON'S, AND IT IS NOW SESSION-SCOPED
		// (ADR-017 amendment T2-c). Until R8 the sentence "the daemon gates the peek
		// itself (capability + kill switch)" named the negotiated remote-gateway
		// capability and the kill switch -- NEITHER of which is about the session -- so a
		// downlevel or compromised app that merely asked got a supervised, reconnecting
		// peek started on its behalf onto a healthy Claude session. handleTerminalSubscribe
		// now refuses such a session with CodeCapabilityRefused before it opens any tap,
		// and TerminalWatcher.run ENDS the watch on that code instead of reconnecting, so
		// a refusal costs one dial and leaves no backoff loop behind.
		if b.cfg.Watchers == nil {
			return nil
		}
		b.cfg.Watchers.Watch(rc.Session)
		return nil
	case protocol.ActionTerminalUnwatch:
		if b.cfg.Watchers == nil {
			return nil
		}
		b.cfg.Watchers.Unwatch(rc.Session)
		return nil
	case protocol.ActionTerminalRenew:
		// ADR-017 T4-b: the phone's evidence that someone is still looking. It NEVER
		// starts a watch -- Renew is a no-op for a session with no live one -- so it
		// cannot be a way to acquire a peek without the verb the capability gate is
		// written over.
		if b.cfg.Watchers == nil {
			return nil
		}
		b.cfg.Watchers.Renew(rc.Session)
		return nil
	case protocol.ActionJournalResync:
		// A READ, and PB-SYNC-5's decision: UNSIGNED, so it is NOT forwarded to the daemon's
		// device authenticator, exactly like the two watches above. The gateway is what
		// performs the journal_read and it holds no device signing key -- it opens the
		// phone's sealed commands and forwards them, it cannot author a signature -- so
		// putting this behind the mutating-op gate would refuse every repair from a device
		// whose view is stale, permanently. The gates that do apply are the daemon's:
		// handleJournalRead requires the negotiated `journal` capability and the kill switch
		// (PB-SYNC-4), and the seal under the epoch content key is already proof the asker is
		// the paired device.
		if b.cfg.Resync == nil {
			return nil
		}
		return b.cfg.Resync.Resync(ctx, rc.ResyncCursor, rc.RosterOnly, rc.DiscardedBacklog, rc.DiscardRecoveryToken)
	case protocol.ActionPushPrefs:
		// Authorized by the DAEMON, persisted HERE (PB-PUSH-8 / PB-PUSH-10): see
		// applyPushPrefs.
		return b.applyPushPrefs(ctx, rc)
	default:
		return b.forward(ctx, rc)
	}
}

// forward sends a mutating command to the daemon and seals its reply back to the
// phone mailbox.
func (b *CommandBridge) forward(ctx context.Context, rc protocol.RemoteCommand) error {
	op, err := opForAction(rc)
	if err != nil {
		return b.refuseCommand(ctx, rc, err)
	}
	reply, err := b.forwardCtx(ctx, op, rc)
	if err != nil {
		return fmt.Errorf("forward: %w", err)
	}
	// Through the ONE serialised producer (lease_confirm.go): a second inline
	// allocate -> append here would reintroduce the out-of-order hazard for this class.
	return b.sealReplyForOperation(ctx, rc.OperationID, reply)
}

// forwardCtx races Forwarder.ForwardCommand against ctx, so a command in flight when the
// gateway shuts down no longer blocks Service.Run's shutdown WaitGroup for the call's own
// timeout.
//
// CommandForwarder.ForwardCommand takes no context.Context (see the interface doc: adding
// one would ripple its signature into every fake across the tree). Its production
// implementation dials a fresh daemon connection per call and blocks on a fixed 10s
// reply-read deadline, entirely independent of ctx. forward/applyPushPrefs reach it
// SYNCHRONOUSLY, once per dispatched command, inside a single Run loop iteration, so
// without this race a command in flight at the moment ctx is cancelled kept Service.Run
// parked for however long that round trip took -- observed up to ~2.65s in practice,
// comfortably under ForwardCommand's own 10s ceiling but past every caller's shutdown
// bound.
//
// The abandoned call is left running to its own deadline; nothing here waits for or
// consumes its outcome once ctx is gone. That is safe because handle's existing
// crash-shaped recovery already covers it: the item was never consume()d, so a restart
// re-serves it and the daemon's own two-phase idempotency (D6) suppresses the duplicate --
// the same margin an actual process crash mid-call already relies on. Unlike beginLeaseCtx,
// no self-cleanup is needed on the abandoned side: ForwardCommand's own `defer dc.Close()`
// runs inside that goroutine regardless of who is still listening, so the daemon
// connection it opened is never left dangling past that 10s ceiling.
func (b *CommandBridge) forwardCtx(ctx context.Context, op string, rc protocol.RemoteCommand) (protocol.Control, error) {
	type result struct {
		reply protocol.Control
		err   error
	}
	done := make(chan result, 1)
	go func() {
		reply, err := b.cfg.Forwarder.ForwardCommand(op, rc)
		done <- result{reply, err}
	}()
	select {
	case res := <-done:
		return res.reply, res.err
	case <-ctx.Done():
		return protocol.Control{}, ctx.Err()
	}
}

// beginLeaseCtx races Leases.Begin against ctx, for the same reason forwardCtx races
// ForwardCommand: LeaseRouter.Begin also takes no ctx and blocks on its own fixed
// timeout (LeaseAwait, default 5s) independent of the caller's.
//
// Unlike forwardCtx's abandoned daemon round trip, a successful Begin here registers a
// LIVE, PERSISTENT lease conn in the router (LeaseManager.conns) that does NOT self-close
// -- it lives until End/Close/supersede. Left to complete after the caller has given up,
// it could register the conn AFTER Service.Run's deferred leases.Close() already ran,
// leaking the connection and its readLoop goroutine for good (not merely for a bounded
// 10s, the way an abandoned ForwardCommand call is). The atomic CAS below makes the
// caller's abandonment and the goroutine's completion mutually exclusive -- exactly one
// side "wins" the claim -- so a Begin that finishes right as ctx cancels is either handed
// to the caller intact or torn down by the goroutine itself, never both and never
// neither.
func (b *CommandBridge) beginLeaseCtx(ctx context.Context, rc protocol.RemoteCommand) error {
	var claimed atomic.Bool
	done := make(chan error, 1)
	go func() {
		err := b.cfg.Leases.Begin(rc)
		if !claimed.CompareAndSwap(false, true) {
			// The caller already gave up on ctx cancel; a successful Begin still
			// registered a conn nobody now owns the teardown of. Close it directly
			// rather than leave it outliving both the caller and the Service.
			if err == nil {
				b.cfg.Leases.End(rc.Session)
			}
			return
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if claimed.CompareAndSwap(false, true) {
			return ctx.Err()
		}
		// Begin already won the claim above and is about to send; take its real
		// result rather than reporting ctx.Err() for a lease that was in fact
		// granted.
		return <-done
	}
}

// refuseCommand answers a command this gateway cannot route -- an action with no arm, or one
// whose in-envelope body is missing -- and returns the reason so the item still fails locally.
//
// AN UNANSWERED COMMAND IS UNANSWERABLE, and that is the whole of why this exists
// (agents-tracker-nx44.4). opForAction's refusal used to return straight out of forward, one
// hop short of the daemon and BEFORE handle's consume, so nothing was ever sealed back: the
// phone's operation stayed pending for the life of the install, because a reply is the only
// thing that resolves one. S18 found it for device_revoke and IS-LIFE-4 found it for approve;
// both fixes added the missing ARM, and neither closed the class -- which is what happens when
// there is no arm, and a phone one release ahead of its gateway is the ordinary way to get one.
//
// THE ERROR IS STILL RETURNED, so the reason rides out in a poll error an operator can read.
// It is returned as a refusedCommand once the reply is SEALED, which is what tells handle to
// consume the item anyway (agents-tracker-2pnu F3): the answer is out and no retry can change
// it, so an unconsumed item would only wedge the cursor behind a question already answered. A
// failed SEAL is the other case and keeps the old shape -- a refusal the phone never received
// must be re-served, because that is the one retry that can still deliver.
//
// refusePushPrefs has the same shape and the same reasons, including the absence of an
// ErrorCode -- none of the six in the taxonomy describes "this build has no arm for that", and
// inventing a mapping would tell the phone's retry policy something untrue.
func (b *CommandBridge) refuseCommand(ctx context.Context, rc protocol.RemoteCommand, reason error) error {
	if sealErr := b.sealReply(ctx, protocol.Control{
		Op:          protocol.OpError,
		SessionID:   rc.Session,
		OperationID: rc.OperationID,
		Error:       reason.Error(),
	}); sealErr != nil {
		return errors.Join(reason, sealErr)
	}
	return refusedCommand{reason}
}

// errNoPrefsCustody refuses push_prefs on a bridge assembled without durable custody. The
// phone is told; answering OK to a preference nothing stored would leave its settings
// screen authoritative about a value the machine has never held.
var errNoPrefsCustody = errors.New("remotegw: push_prefs refused: no durable preference custody configured")

// errNoPrefsBody refuses a push_prefs whose preference body is absent -- the counterpart of
// the launch path's "missing its launch spec in-envelope". A stripped body must be refused
// loudly, never applied as some default: the default is ENABLED, so a body a relay dropped
// would silently re-enable notifications the user had turned off.
var errNoPrefsBody = errors.New("remotegw: push_prefs command missing its preference body in-envelope")

// applyPushPrefs runs one push_prefs: the DAEMON decides, the GATEWAY stores.
//
// The split is not arbitrary. The gateway holds no device key, so if it decided this verb
// locally anyone who could get a plaintext-shaped frame into the mailbox would be deciding
// whether the owner's phone gets woken -- and silence is the kind of failure nobody
// reports, because nothing appears to break. So it rides the one authorization plane every
// other signed action rides (requireRemoteAuthz), and the gateway applies only on the
// daemon's OK. Durability lands here because PB-PUSH-10 puts it where DELIVERY is decided,
// and delivery is decided by the notifier reading this same record.
//
// Every exit seals a reply. An unanswered push_prefs leaves the phone's settings screen
// waiting forever, which is a worse object than one that is wrongly confident: at least a
// refusal can be retried.
func (b *CommandBridge) applyPushPrefs(ctx context.Context, rc protocol.RemoteCommand) error {
	if rc.PushPrefs == nil {
		return b.refusePushPrefs(ctx, rc, errNoPrefsBody)
	}
	if b.cfg.Prefs == nil {
		return b.refusePushPrefs(ctx, rc, errNoPrefsCustody)
	}
	op, err := opForAction(rc)
	if err != nil {
		return err
	}
	// The preference body stays HERE: it is the gateway's own durable custody (PB-PUSH-10) and
	// the daemon only authorizes, so the frame forwarded carries the tuple and nothing else.
	reply, err := b.forwardCtx(ctx, op, protocol.RemoteCommand{DeviceCommandAuth: rc.DeviceCommandAuth})
	if err != nil {
		return fmt.Errorf("forward: %w", err)
	}
	reply.OperationID = rc.OperationID
	if reply.Op == protocol.OpError {
		// The daemon refused -- an unknown device, a forged or expired signature, an
		// insufficient capability, or the kill switch. The gateway cannot tell these apart
		// and must not: all it may do is NOT apply the preference, and say so.
		return b.sealReply(ctx, reply)
	}
	// The ack the phone receives is what makes its settings screen authoritative, so it
	// must not be able to precede a failed persist: the screen would then say "off" while
	// the next gateway start pushes again -- the exact defect PB-PUSH-10 names.
	if err := b.cfg.Prefs.SavePrefs(*rc.PushPrefs); err != nil {
		return b.refusePushPrefs(ctx, rc, err)
	}
	return b.sealReply(ctx, reply)
}

// refusePushPrefs tells the phone why its preference was not applied and returns the reason
// so the item still fails locally (no inbound high-water advance, a poll error an operator
// can see). The seal error is joined rather than swallowed: a refusal the phone never
// received leaves it with neither the setting nor the reason.
//
// The reply carries no ErrorCode. None of the six in the taxonomy describes a machine-side
// custody failure, and inventing a mapping would tell the phone's retry policy something
// untrue -- the same shape confirmLease's refusals already take.
func (b *CommandBridge) refusePushPrefs(ctx context.Context, rc protocol.RemoteCommand, reason error) error {
	refusal := fmt.Errorf("push_prefs: %w", reason)
	if sealErr := b.sealReply(ctx, protocol.Control{
		Op:          protocol.OpError,
		SessionID:   rc.Session,
		OperationID: rc.OperationID,
		Error:       reason.Error(),
	}); sealErr != nil {
		return errors.Join(refusal, sealErr)
	}
	// refuseCommand's shape, for its reason (agents-tracker-2pnu F3): once the phone has been
	// told, re-serving the item only re-tells it. The persist failure that reaches here is the
	// one arguable case and takes the same answer -- a preference the phone was told was NOT
	// applied is resolved on its side, and 3 re-tells a second against a full disk is not a
	// retry policy.
	return refusedCommand{refusal}
}

// opForAction maps a command action to the daemon wire op. kill/delete carry no body
// and map to identically-named ops. launch additionally requires the LaunchReq to
// ride in the sealed envelope (RemoteCommand.Launch); a launch action with no body is
// refused loudly rather than forwarded with a nil spec (which would fail the daemon's
// content-hash binding). push_prefs carries its body in RemoteCommand.PushPrefs, which
// applyPushPrefs has already checked by the time it asks for the op. approve carries an
// ApproveReq under the same rule as launch: see its arm.
//
// device_revoke's arm was MISSING until S18, and the omission was the whole of the phone's
// panic button. ActionDeviceRevoke is in the signed action set, skeleton/deviceauth.go classes
// it, and handleDeviceRevoke serves it -- but a phone-sealed revoke fell through to the default
// here and was refused "unsupported command action" one hop short of the daemon, WITH NO REPLY
// SEALED, so the op could never resolve either. mobile.RevokeThisDevice worked around it by
// sealing nothing and recording a durable local refusal; with the arm in place that workaround
// is gone and the verb rides this path like every other mutation.
//
// approve's arm was MISSING for exactly one slice longer than device_revoke's, and the reason
// was true when it was written: "approve is not a daemon remote op (D6/D7)". It is now
// (protocol.OpApprove), and the daemon-side validation it reaches -- the ADR-007 D7 binding
// tuple, the content hash, the daemon-authoritative expiry -- had no production caller until
// this arm existed.
func opForAction(rc protocol.RemoteCommand) (string, error) {
	switch rc.Action {
	case protocol.ActionKill:
		return protocol.OpKill, nil
	case protocol.ActionDelete:
		return protocol.OpDelete, nil
	case protocol.ActionDeviceRevoke:
		return protocol.OpDeviceRevoke, nil
	case protocol.ActionLaunch:
		if rc.Launch == nil {
			return "", errors.New("remotegw: launch command missing its launch spec in-envelope")
		}
		return protocol.OpLaunch, nil
	case protocol.ActionApprove:
		// Launch's rule, for the one other action whose body the daemon reads. A stripped
		// ApproveReq forwarded as a zero body would be an approve naming no interaction, which
		// the daemon refuses CodeStaleApproval -- so the user would be told their card is out of
		// date by a frame that merely lost its payload.
		if rc.Approve == nil {
			return "", errors.New("remotegw: approve command missing its approve body in-envelope")
		}
		return protocol.OpApprove, nil
	case protocol.ActionPushPrefs:
		return protocol.OpPushPrefs, nil
	case protocol.ActionSessionLaunch:
		// Wave R5: the real session_launch. Launch's and approve's own rule, inherited: a
		// session_launch whose SessionLaunch body was stripped in transit must NOT be
		// forwarded as a bodyless frame -- it would reach the daemon as a launch naming no
		// preset, and the user would be told their preset is invalid by a frame that
		// merely lost its payload. The gateway stays a blind conduit for the body's
		// CONTENT: every refusal decision (kill switch, tier, unknown/stale preset,
		// roots, options) is machine-side, behind requireRemoteAuthz.
		if rc.SessionLaunch == nil {
			return "", errors.New("remotegw: session_launch command missing its preset body in-envelope")
		}
		return protocol.OpSessionLaunch, nil
	case protocol.ActionLaunchPresets:
		// Wave R5: the signed read of the machine-authored preset list, forwarded like
		// every semantic op (Op == Action) -- only the daemon holds the device registry
		// and the preset custody.
		return protocol.OpLaunchPresets, nil
	case protocol.ActionComposerSend:
		// Wave R6: the real composer_send. launch/approve/session_launch's rule, inherited
		// by the fourth body-carrying action: a composer_send whose body was stripped in
		// transit is REFUSED here, never forwarded bodyless -- a zero body would reach the
		// daemon as a send naming no text and surface to the user as some other refusal
		// for a frame that merely lost its payload. The gateway stays a blind conduit for
		// the body's CONTENT: integrity is the phone signature's job via
		// ComposerSendContentHash, recomputed daemon-side.
		if rc.ComposerSend == nil {
			return "", errors.New("remotegw: composer_send command missing its composer body in-envelope")
		}
		return protocol.OpComposerSend, nil
	case protocol.ActionTurnInterrupt:
		// Wave R6 FIX-PACK B7: turn_interrupt WAS bodyless by design, and the design was
		// wrong. Finding B7 proved that an interrupt carrying no turn coordinate types the
		// cancel sequence into whatever turn is current when it ARRIVES -- in playbook
		// §8.1, the turn the owner just started at the terminal, whose half-typed line the
		// cancel key clears. The op now carries composer_send's own precondition, so it
		// inherits composer_send's own body gate: a stripped body must never be forwarded,
		// because a zero body is an interrupt naming no turn, which is exactly the frame
		// this fix exists to make unspeakable.
		if rc.TurnInterrupt == nil {
			return "", errors.New("remotegw: turn_interrupt command missing its interrupt body in-envelope")
		}
		return protocol.OpTurnInterrupt, nil
	case protocol.ActionInteractionHistory:
		// Wave R6 FIX-PACK B6 (ADR-014 §1 named this route as accepted and it did not
		// exist: a phone-issued read fell to the default arm below and was refused
		// "unsupported command action"). An UNSIGNED read, forwarded to the daemon like
		// every other op -- unlike terminal_watch and journal_resync it has no
		// gateway-local plane to serve it from: the journal and the retained bodies live
		// on the DAEMON. It carries no device signature, and the gates that apply are the
		// daemon's own (negotiated `journal` capability + kill switch, fix-pack B2),
		// exactly as for journal_read.
		if rc.History == nil {
			return "", errors.New("remotegw: interaction_history command missing its read body in-envelope")
		}
		return protocol.OpInteractionHistory, nil
	case protocol.ActionInteractionDetail:
		if rc.Detail == nil {
			return "", errors.New("remotegw: interaction_detail command missing its read body in-envelope")
		}
		return protocol.OpInteractionDetail, nil
	case protocol.ActionTerminalControlBegin:
		// Wave R8 (ADR-017 T6): launch/approve/composer_send's rule, inherited by the
		// action that mints a control generation. A begin whose body was stripped in
		// transit must NEVER be forwarded bodyless: a zero body is a generation bound to
		// no incarnation, which is precisely the frame T8-a makes unspeakable -- it would
		// authorise raw bytes into the PTY that replaced the one the user was reading.
		if rc.TerminalControlBegin == nil {
			return "", errors.New("remotegw: terminal_control_begin command missing its body in-envelope")
		}
		return protocol.OpTerminalControlBegin, nil
	case protocol.ActionTerminalInput:
		// Wave R8 (ADR-017 T6): one of the exception's exactly two UNSIGNED frame kinds.
		// It is forwarded to the daemon like the M3 reads -- no device signature, no
		// actionClass entry -- because what authorises it is the E2EE frame's own sender
		// and sequence plus the CONFIRMED GENERATION, which the daemon re-evaluates per
		// frame alongside the kill switch, the device registration and the capability
		// record (T6-e). A stripped body is refused here rather than forwarded: a zero
		// body is raw input naming no generation and no incarnation.
		if rc.TerminalInput == nil {
			return "", errors.New("remotegw: terminal_input command missing its input body in-envelope")
		}
		return protocol.OpTerminalInput, nil
	case protocol.ActionTerminalControlKeepalive:
		// The second and last unsigned frame kind. It has no body -- the generation IS the
		// frame -- so a missing generation is what a stripped frame looks like.
		if rc.ControlGeneration == "" {
			return "", errors.New("remotegw: terminal_control_keepalive command missing its control generation")
		}
		return protocol.OpTerminalControlKeepalive, nil
	case protocol.ActionOperationStatus, protocol.ActionTerminalControlEnd:
		// The remaining semantic ops: forwarded to the daemon like kill/delete/approve/
		// push_prefs, Op == Action, never gateway-locally refused -- only the daemon holds
		// the device registry requireRemoteAuthz authorizes against. (session_launch and
		// composer_send moved to their own arms above when R5/R6 gave each a body, and
		// turn_interrupt joined them in the R6 fix-pack when finding B7 proved that a Stop
		// with no turn coordinate is not a Stop; operation_status carries only
		// subject_operation_id and needs no body gate.)
		// Wave R8 gave terminal_control_begin, terminal_input and terminal_control_keepalive
		// their own arms above: the first because it now carries a body the daemon reads,
		// the other two because they are ADR-017 T6's UNSIGNED pair. They are NOT signed
		// actions -- they have no actionClass entry and the daemon never asks its device
		// authenticator about them -- which is exactly journal_resync's and the M3 reads'
		// class, not a new one.
		return rc.Action, nil
	default:
		return "", fmt.Errorf("remotegw: unsupported command action %q", rc.Action)
	}
}
