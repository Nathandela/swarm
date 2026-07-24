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
)

// The production relay client is a Mailbox (read + append). This assertion pins the
// seam so a relay-client signature change is caught at compile time.
var _ Mailbox = (*relay.Client)(nil)

// The gateway is a CommandForwarder via ForwardCommand. Pinned at compile time.
var _ CommandForwarder = (*Gateway)(nil)

// Mailbox is the relay seam the command loop needs: read the machine's own inbox
// (commands the phone appended to the machine's routing id) and append sealed
// replies to the phone's mailbox. relay.Client satisfies it.
type Mailbox interface {
	MailboxRead(ctx context.Context, cursor uint64) ([]relay.Item, error)
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

// CommandBridgeConfig configures a CommandBridge.
type CommandBridgeConfig struct {
	Mailbox     Mailbox             // the machine's own relay mailbox (read) + the phone's (append)
	Forwarder   CommandForwarder    // forwards opened mutating commands to the daemon remote.sock
	Leases      LeaseRouter         // routes take_control + input frames to per-session lease conns (nil => input plane disabled)
	Watchers    TerminalWatchRouter // routes terminal_watch/terminal_unwatch to per-session peeks (nil => peek plane disabled)
	Key         crypto.ContentKey   // K_epoch content key shared with the phone
	EpochID     uint32              // the epoch the content key belongs to
	ReplyTarget string              // the phone's relay routing id (where replies are appended)
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
// the good items still process. The daemon's own two-phase idempotency (D6) dedups a
// command that is redelivered after a crash before the cursor was persisted.
type CommandBridge struct {
	cfg  CommandBridgeConfig
	recv *crypto.MailboxReceiver // per-(sender,epoch) seq guard against relay replay/reorder

	mu       sync.Mutex
	cursor   uint64
	replySeq uint64
}

// NewCommandBridge returns a bridge over cfg. The read cursor starts at 0; a caller
// resuming across a restart should seed it via SetCursor from durable state.
func NewCommandBridge(cfg CommandBridgeConfig) *CommandBridge {
	return &CommandBridge{cfg: cfg, recv: crypto.NewMailboxReceiver()}
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
	// Advance past every item read, so a poisoned envelope is not retried forever.
	if maxCursor > 0 {
		b.SetCursor(maxCursor)
		// Ack durably purges consumed items from the relay's mailbox store, so a
		// restarted bridge (fresh in-memory cursor) never re-reads them. A failed
		// ack surfaces as an error but must not lose the in-memory cursor advance
		// above -- the next poll will simply try to ack forward again.
		if err := b.cfg.Mailbox.MailboxAck(ctx, maxCursor); err != nil {
			errs = append(errs, fmt.Errorf("ack cursor %d: %w", maxCursor, err))
		}
	}
	return processed, errors.Join(errs...)
}

// Run polls in a loop every interval until ctx is cancelled, returning ctx.Err().
// Poll errors are non-fatal (a transient relay error should not tear the bridge
// down); the caller may log them via a wrapped Mailbox if desired.
func (b *CommandBridge) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			_, _ = b.PollOnce(ctx)
		}
	}
}

// handle opens ONE mailbox envelope (a single Accept advancing the shared seq
// high-water) and routes it by kind: an input frame rides the lease conn of the
// session named INSIDE its sealed plaintext; a command dispatches on its action
// (take_control/take_control_end to the lease plane, kill/delete/launch to the
// daemon). A replayed seq is rejected by the single Accept before any dispatch, so it
// can neither double-lease nor double-forward nor reach Input.
func (b *CommandBridge) handle(ctx context.Context, it relay.Item) error {
	frame, err := OpenMailboxFrame(b.recv, b.cfg.Key, it.Envelope)
	if err != nil {
		return fmt.Errorf("open frame: %w", err)
	}
	if frame.Kind == FrameInput {
		return b.routeInput(frame)
	}
	return b.routeCommand(ctx, frame.Command)
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
		if err := b.cfg.Leases.Begin(rc); err != nil {
			return fmt.Errorf("take_control: %w", err)
		}
		return nil
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
	b.mu.Lock()
	b.replySeq++
	seq := b.replySeq
	b.mu.Unlock()
	env, err := SealControlReply(b.cfg.Key, b.cfg.EpochID, seq, reply)
	if err != nil {
		return fmt.Errorf("seal reply: %w", err)
	}
	if _, err := b.cfg.Mailbox.MailboxAppend(ctx, b.cfg.ReplyTarget, env); err != nil {
		return fmt.Errorf("append reply: %w", err)
	}
	return nil
}

// opForAction maps a command action to the daemon wire op. kill/delete carry no body
// and map to identically-named ops. launch additionally requires the LaunchReq to
// ride in the sealed envelope (RemoteCommand.Launch); a launch action with no body is
// refused loudly rather than forwarded with a nil spec (which would fail the daemon's
// content-hash binding). approve is not a daemon remote op (D6/D7).
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
	default:
		return "", fmt.Errorf("remotegw: unsupported command action %q", action)
	}
}
