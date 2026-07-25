package remotegw

import (
	"context"
	"errors"
	"fmt"
	"sync"
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

// CommandForwarder forwards a device-signed command to the daemon and returns the
// reply. (*Gateway).ForwardCommand satisfies it.
type CommandForwarder interface {
	ForwardCommand(op, sessionID string, cmd protocol.DeviceCommandAuth, launch *protocol.LaunchReq) (protocol.Control, error)
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
}

// *TerminalWatcher is the production TerminalWatchRouter. Pinned at compile time.
var _ TerminalWatchRouter = (*TerminalWatcher)(nil)

// JournalResyncer serves PB-SYNC-2's journal repair: read the daemon's atomic roster +
// the events after the phone's cursor and publish them as one reseed frame. *Gateway is the
// production implementation (Resync); the seam keeps the loop's dispatch unit-testable
// without a live daemon.
type JournalResyncer interface {
	Resync(ctx context.Context, from uint64) error
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
}

// CommandBridge is the command-IN + reply half of the gateway (R-GW.3/.7): it polls
// the machine's relay mailbox for phone-authored sealed command envelopes, opens
// each under the epoch content key, forwards the device-signed command to the daemon
// (a blind conduit -- the daemon verifies the signature independently, R-POL.9), and
// seals the daemon's reply back to the phone's mailbox. It complements RelaySink's
// journal-OUT with the command-IN direction.
//
// The read cursor advances past every item it reads -- INCLUDING a malformed or
// unforwardable one -- so a poisoned envelope can neither wedge the loop nor be
// retried forever; per-item failures are aggregated into the returned error while
// the good items still process. Both the read cursor and the per-(sender,epoch) replay
// high-water are DURABLE (PB-GW-1, see the Inbound seam), so a restart resumes where the
// previous run stopped and refuses anything the relay retained beyond it. The daemon's own
// two-phase idempotency (D6) covers only the one bounded re-delivery a crash between a
// daemon forward and its persist can produce (see handle).
type CommandBridge struct {
	cfg      CommandBridgeConfig
	recv     *crypto.MailboxReceiver // per-(sender,epoch) seq guard against relay replay/reorder
	replySeq SeqSource               // OUTBOUND reply seq (durable across restart, C2b)
	inbound  InboundState            // INBOUND checkpoint custody (durable across restart, PB-GW-1)

	// replyMu serialises the whole seq-allocate -> seal -> append of the OUTBOUND reply
	// bucket. It is separate from mu because sealReply's failure path calls setErr, which
	// takes mu. See sealReply for why the three steps cannot be split.
	replyMu sync.Mutex

	mu      sync.Mutex
	cursor  uint64
	highest map[InboundStream]uint64 // in-memory mirror of the persisted per-stream high-water
	pollErr error                    // first error the Run loop's polls hit (see Err)
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
	b := &CommandBridge{
		cfg:      cfg,
		recv:     crypto.NewMailboxReceiver(),
		replySeq: replySeq,
		inbound:  inbound,
		highest:  map[InboundStream]uint64{},
	}
	ck := inbound.Load()
	for st, seq := range ck.Highest {
		b.recv.SeedHighWater(st.Sender, st.Epoch, seq)
		b.highest[st] = seq
	}
	b.cursor = ck.Cursor
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

// Cursor is the highest relay mailbox cursor the bridge has consumed (its durable
// resume point).
func (b *CommandBridge) Cursor() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cursor
}

// SetCursor seeds the read cursor from durable state on resume (monotonic; a lower
// value is ignored so a stale seed cannot replay already-consumed commands).
func (b *CommandBridge) SetCursor(c uint64) {
	b.mu.Lock()
	if c > b.cursor {
		b.cursor = c
	}
	b.mu.Unlock()
}

// PollOnce reads every mailbox item past the current cursor, processes each (open ->
// forward -> seal reply), advances the cursor past all of them, and returns how many
// were forwarded successfully. Per-item failures (a malformed/wrong-key envelope, a
// forward error, a reply-seal error) are joined into the returned error but do not
// stop the batch or hold back the cursor.
func (b *CommandBridge) PollOnce(ctx context.Context) (int, error) {
	items, err := b.cfg.Mailbox.MailboxRead(ctx, b.Cursor())
	if err != nil {
		return 0, err
	}
	processed, maxCursor, errs := b.processBatch(ctx, items)
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

// processBatch handles one batch of mailbox items and returns how many forwarded
// successfully, the highest cursor consumed (0 when the batch was empty), and the
// per-item failures. It is shared by the wait-driven Run and by PollOnce, which
// differ only in how the batch was fetched and where the ack goes.
func (b *CommandBridge) processBatch(ctx context.Context, items []relay.Item) (int, uint64, []error) {
	processed := 0
	var errs []error
	var maxCursor uint64
	for _, it := range items {
		if it.Cursor > maxCursor {
			maxCursor = it.Cursor
		}
		if err := b.handle(ctx, it); err != nil {
			errs = append(errs, fmt.Errorf("cursor %d: %w", it.Cursor, err))
			continue
		}
		processed++
	}
	// Advance past every item read, so a poisoned envelope is not retried forever, and
	// persist that advance so a restart does not re-read it either. Only the READ CURSOR
	// moves here: the per-stream replay high-water is advanced by consume, per frame, in
	// the order its action class requires.
	if maxCursor > 0 {
		b.SetCursor(maxCursor)
		if err := b.saveCheckpoint(); err != nil {
			errs = append(errs, fmt.Errorf("persist cursor %d: %w", maxCursor, err))
		}
	}
	return processed, maxCursor, errs
}

// Run drives the command-IN path off the relay's BOUNDED SERVER-SIDE WAIT until ctx is
// cancelled, returning ctx.Err().
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
	acks := transport.NewAckBatcher(func(actx context.Context, cursor uint64) error {
		return b.cfg.Mailbox.MailboxAck(actx, cursor)
	})
	ackCtx, stopAcks := context.WithCancel(ctx)
	acksDone := make(chan struct{})
	go func() { defer close(acksDone); acks.Run(ackCtx) }()
	defer func() { stopAcks(); <-acksDone }()

	pacer := transport.NewDrainPacer()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := pacer.Pace(ctx); err != nil {
			return ctx.Err()
		}
		items, _, err := b.cfg.Mailbox.MailboxWait(ctx, b.Cursor())
		pacer.Observe(len(items))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
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
		_, maxCursor, errs := b.processBatch(ctx, items)
		if maxCursor > 0 {
			acks.Record(maxCursor)
		}
		if err := errors.Join(errs...); err != nil {
			b.setErr(err)
		}
	}
}

// commandRetryDelay bounds how fast the wait loop retries a failing relay. It is not a
// poll cadence: it applies only after an error, and a healthy loop never reaches it.
const commandRetryDelay = 250 * time.Millisecond

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
	if err := b.routeCommand(ctx, frame.Command); err != nil {
		return err
	}
	return b.consume(frame, it.Cursor)
}

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
	ck := InboundCheckpoint{Cursor: b.cursor, Highest: make(map[InboundStream]uint64, len(b.highest))}
	for st, seq := range b.highest {
		ck.Highest[st] = seq
	}
	b.mu.Unlock()
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
		return b.confirmLease(ctx, rc, b.cfg.Leases.Begin(rc))
	case protocol.OpTakeControlEnd:
		// take_control_end has no signed Action constant; the daemon op string is its
		// wire action. Tearing down the lease conn (End) is the phone's take_control_end.
		if b.cfg.Leases == nil {
			return nil
		}
		b.cfg.Leases.End(rc.Session)
		return nil
	case protocol.ActionTerminalWatch:
		// A READ: start a server-rendered peek for the session. It is NOT forwarded to the
		// daemon's device authenticator (an unsigned watch); the daemon gates the peek
		// itself (capability + kill switch). The peek plane may be disabled (nil Watchers).
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
		return b.cfg.Resync.Resync(ctx, rc.ResyncCursor)
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
	op, err := opForAction(rc.Action, rc.Launch)
	if err != nil {
		return err
	}
	reply, err := b.cfg.Forwarder.ForwardCommand(op, rc.Session, rc.DeviceCommandAuth, rc.Launch)
	if err != nil {
		return fmt.Errorf("forward: %w", err)
	}
	// Through the ONE serialised producer (lease_confirm.go): a second inline
	// allocate -> append here would reintroduce the out-of-order hazard for this class.
	return b.sealReply(ctx, reply)
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
	op, err := opForAction(rc.Action, rc.Launch)
	if err != nil {
		return err
	}
	reply, err := b.cfg.Forwarder.ForwardCommand(op, rc.Session, rc.DeviceCommandAuth, nil)
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
	sealErr := b.sealReply(ctx, protocol.Control{
		Op:          protocol.OpError,
		SessionID:   rc.Session,
		OperationID: rc.OperationID,
		Error:       reason.Error(),
	})
	return errors.Join(fmt.Errorf("push_prefs: %w", reason), sealErr)
}

// opForAction maps a command action to the daemon wire op. kill/delete carry no body
// and map to identically-named ops. launch additionally requires the LaunchReq to
// ride in the sealed envelope (RemoteCommand.Launch); a launch action with no body is
// refused loudly rather than forwarded with a nil spec (which would fail the daemon's
// content-hash binding). push_prefs carries its body in RemoteCommand.PushPrefs, which
// applyPushPrefs has already checked by the time it asks for the op. approve is not a
// daemon remote op (D6/D7).
func opForAction(action string, launch *protocol.LaunchReq) (string, error) {
	switch action {
	case protocol.ActionKill:
		return protocol.OpKill, nil
	case protocol.ActionDelete:
		return protocol.OpDelete, nil
	case protocol.ActionLaunch:
		if launch == nil {
			return "", errors.New("remotegw: launch command missing its launch spec in-envelope")
		}
		return protocol.OpLaunch, nil
	case protocol.ActionPushPrefs:
		return protocol.OpPushPrefs, nil
	default:
		return "", fmt.Errorf("remotegw: unsupported command action %q", action)
	}
}
