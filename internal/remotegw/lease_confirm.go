package remotegw

// PB-SYNC-7's lease CONFIRMATION. routeCommand used to seal nothing for take_control, so
// PB-INPUT-2's "no keystroke is ever sent without a confirmed current lease generation"
// had nothing to confirm against: the phone could only ASSUME the lease, which is exactly
// what PB-INPUT-2 forbids. The gateway already holds the generation (LeaseRouter.
// Generation, captured from the daemon's OpLease grant); this seals it back.
//
// Silence on failure is the worse half: a refused lease that produces no reply is
// indistinguishable from one that is merely slow, and that is how a keystroke gets sent
// against a lease that does not exist. So BOTH outcomes seal -- a grant or a refusal.

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nathandela/swarm/internal/protocol"
)

// errLeaseSevered is the refusal reason for a lease that was granted and then died before
// its generation could be read back.
var errLeaseSevered = errors.New("lease ended before it could be confirmed")

// confirmLease seals the outcome of one take_control back to the phone, tagged with the
// command's operation_id so it is attributable, and returns the error the bridge should
// surface locally. A granted lease seals an OpLease carrying the daemon-granted
// generation; a refused one seals an OpError carrying the reason and NO generation
// (nothing was granted, so nothing may be confirmed).
//
// A granted lease whose generation reads 0 is refused too. The generation is read AFTER
// Begin returned, and LeaseManager.Generation reports 0 for a session that holds no conn
// -- which is precisely the state the per-conn watcher leaves behind the moment the lease
// dies (kill switch, device revoke, session exit). Sealing OpLease with generation 0 would
// be a POSITIVE confirmation naming a lease the daemon does not hold, and the phone gates
// keystrokes on exactly that number (PB-INPUT-2).
//
// beginErr stays authoritative for the caller: a refusal must still fail the item (no
// inbound high-water advance, a poll error the operator can see), even though the phone
// was told about it -- joined with the seal error, because a refusal the phone never
// received leaves it with neither the lease nor the reason, and that must not be silent.
// The severed-lease refusal does NOT fail the item: Begin consumed the take_control, so
// holding back the high-water would only invite a replay of a command that already ran.
func (b *CommandBridge) confirmLease(ctx context.Context, rc protocol.RemoteCommand, beginErr error) error {
	reply := protocol.Control{
		Op:          protocol.OpError,
		SessionID:   rc.Session,
		OperationID: rc.OperationID,
	}
	switch gen := b.cfg.Leases.Generation(rc.Session); {
	case beginErr != nil:
		reply.Error = beginErr.Error()
	case gen == 0:
		reply.Error = errLeaseSevered.Error()
	default:
		reply.Op = protocol.OpLease
		reply.Generation = gen
	}
	sealErr := b.sealReply(ctx, reply)
	if beginErr != nil {
		return errors.Join(fmt.Errorf("take_control: %w", beginErr), sealErr)
	}
	return sealErr
}

// sealReply allocates the next OUTBOUND reply seq, seals reply under the epoch content
// key and appends it to the phone's mailbox. Command replies ride the sender-zero bucket
// with their own durable seq source (command_in.go's deliberate split from the shared
// journal/terminal bucket). It is the ONE producer on that bucket: confirmLease, forward and
// sealSevered all go through here.
//
// THE WHOLE allocate -> append RUNS UNDER replyMu, for the reason RelaySink.sealAtSeqLocked
// states one bucket over. Releasing the lock after allocating the seq would let a LATER seq
// reach the relay first, and the phone has a single MailboxReceiver for this bucket: it
// refuses the earlier frame with crypto.ErrStaleSeq and the app returns early, so
// LeaseState.Apply never sees it. That stopped being theoretical when the lease-severance
// notice arrived, because it seals from the per-conn WATCHER goroutine while confirmLease
// and forward seal from the command-poll goroutine -- and a supersede drives both at once,
// so the loser is either the new lease's OpLease (a dead keyboard) or the OpDetach that
// exists to prevent typing into a void. Replies are the gateway's outbound path and not hot,
// so serialising them is cheap.
func (b *CommandBridge) sealReply(ctx context.Context, reply protocol.Control) error {
	b.replyMu.Lock()
	defer b.replyMu.Unlock()
	seq, err := b.replySeq.Next()
	if err != nil {
		return fmt.Errorf("reply seq: %w", err)
	}
	env, err := SealControlReply(b.cfg.Key, b.cfg.EpochID, seq, reply)
	if err != nil {
		return fmt.Errorf("seal reply: %w", err)
	}
	if _, err := b.cfg.Mailbox.MailboxAppend(ctx, b.cfg.ReplyTarget, env); err != nil {
		return fmt.Errorf("append reply: %w", err)
	}
	return nil
}
