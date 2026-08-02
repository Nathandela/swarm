package remotegw

// PB-INPUT-2's gateway half: the phone is TOLD when its lease dies.
//
// Three of the five severance events PB-INPUT-2 enumerates -- a daemon restart, a session
// exit under the user, a daemon-side lease expiry -- kill the lease conn while the phone's
// relay connection stays perfectly healthy. Before this, nothing told it: LeaseManager.watch
// silently deleted the dead conn and LeaseManager.Input then returned nil for every
// subsequent keystroke, so the phone typed into a void with no error, no reply and no signal
// of any kind. PB-SYNC-7 closed exactly this hole one step earlier, for the take_control
// REPLY ("silence is indistinguishable from a slow grant, which is how a keystroke gets sent
// against a lease that does not exist"); the argument applies verbatim to a lease that is
// granted and then dies.
//
// The notice is a command_reply carrying OpDetach, tagged with the take_control's operation
// id so ReplyCache.TakeFor can attribute it, and carrying the DEAD generation -- because a
// supersede closes the OLD conn, so its notice can arrive after the replacement lease is
// already live, and keyed by session alone it would kill a lease the daemon is holding open.

import (
	"context"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// SeveredLease is one lease death, as the gateway observed it.
type SeveredLease struct {
	Session     string // namespaced session id whose lease died
	OperationID string // the take_control that opened it, so the notice is attributable
	Generation  uint64 // the DEAD generation, so a supersede cannot kill its replacement
	Reason      string // legible cause, for PB-INPUT-2's "visibly"
}

// severNoticeTimeout bounds the relay append a lease death triggers. The notice is sealed
// from the per-conn watcher, off any request path, so an unbounded append on a wedged relay
// would pin that goroutine for the life of the process.
const severNoticeTimeout = 10 * time.Second

// sealSevered seals one lease-death notice to the phone's mailbox.
//
// The context is the bridge's own, NEVER a caller's request context: a lease dies
// asynchronously to every poll, and sealing under a context a finished poll had cancelled
// would fail the append silently -- nobody is waiting on the result.
func (b *CommandBridge) sealSevered(s SeveredLease) {
	ctx, cancel := context.WithTimeout(context.Background(), severNoticeTimeout)
	defer cancel()
	err := b.sealReply(ctx, protocol.Control{
		Op:          protocol.OpDetach,
		SessionID:   s.Session,
		OperationID: s.OperationID,
		Generation:  s.Generation,
		Error:       s.Reason,
	})
	if err != nil {
		// A notice the phone never received leaves it typing into a void, which is the whole
		// defect this closes. Nobody is waiting on the result, so Err() is the only place an
		// operator can see it.
		b.setErr(fmt.Errorf("seal lease severance for %q: %w", s.Session, err))
	}
}
