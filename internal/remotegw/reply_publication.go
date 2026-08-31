package remotegw

import (
	"sync"
)

// replyPublicationFence orders the two producers that jointly define the phone's reply
// authority:
//
//   - CommandBridge allocates and appends command-reply sequence N.
//   - RelaySink reads the issued watermark and appends a reconcile publishing that ceiling.
//
// The critical section has to include the relay append on both sides. Serialising only
// SeqSource.Next and Issued would still allow a reconcile carrying N to reach relay custody
// before reply N. pending retains the exact delivery-unknown bytes so either producer can
// put them in custody before reconciliation publishes N. This matters for unsolicited
// replies (for example lease severance): there may be no retained phone command to drive
// CommandBridge's ordinary retry path.
type replyPublicationFence struct {
	mu      sync.Mutex
	pending *pendingCommandReply
}
