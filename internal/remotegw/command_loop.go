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
}

// CommandForwarder forwards a device-signed command to the daemon and returns the
// reply. (*Gateway).ForwardCommand satisfies it.
type CommandForwarder interface {
	ForwardCommand(op, sessionID string, cmd protocol.DeviceCommandAuth, launch *protocol.LaunchReq) (protocol.Control, error)
}

// CommandBridgeConfig configures a CommandBridge.
type CommandBridgeConfig struct {
	Mailbox     Mailbox           // the machine's own relay mailbox (read) + the phone's (append)
	Forwarder   CommandForwarder  // forwards opened commands to the daemon remote.sock
	Key         crypto.ContentKey // K_epoch content key shared with the phone
	EpochID     uint32            // the epoch the content key belongs to
	ReplyTarget string            // the phone's relay routing id (where replies are appended)
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
	cfg CommandBridgeConfig

	mu       sync.Mutex
	cursor   uint64
	replySeq uint64
}

// NewCommandBridge returns a bridge over cfg. The read cursor starts at 0; a caller
// resuming across a restart should seed it via SetCursor from durable state.
func NewCommandBridge(cfg CommandBridgeConfig) *CommandBridge {
	return &CommandBridge{cfg: cfg}
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

// handle opens one command envelope, forwards it to the daemon, and seals the reply
// back to the phone mailbox.
func (b *CommandBridge) handle(ctx context.Context, it relay.Item) error {
	rc, err := OpenRemoteCommand(b.cfg.Key, it.Envelope)
	if err != nil {
		return fmt.Errorf("open command: %w", err)
	}
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
