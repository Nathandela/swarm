package remotegw

import (
	"errors"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// AppendOutcome classifies what a failed mailbox append DID to the relay's state, which is
// what decides whether its outbound seq may be reused (PB-GW-7). "The append failed" is not
// enough on its own: the relay STORES the item and only then writes its reply
// (relay/server.go handleMailboxAppend), and relay.Client.MailboxAppend errors when the
// RESPONSE read fails -- so a failure whose reply was merely lost may already have committed.
type AppendOutcome int

const (
	// AppendDelivered: the relay acked. The seq is spent.
	AppendDelivered AppendOutcome = iota
	// AppendRefused: a DEFINITIVE pre-commit refusal -- the relay replied before storing
	// anything, so the seq was never spent. Burning it manufactures a gap the phone reports
	// on its next frame, and PB-SYNC-1 cannot attribute a gap in the shared bucket to
	// journal or terminal, so one burned seq costs a conservative resync of BOTH streams.
	AppendRefused
	// AppendUnknown: the append may or may not have committed. Such a seq may only be
	// burned or re-appended VERBATIM; re-sealing a fresh plaintext at it leaves two rival
	// envelopes at one seq, of which the phone keeps whichever lands first and stale-drops
	// the other -- silent journal loss or reordering.
	AppendUnknown
)

// ClassifyAppend maps a MailboxAppend error to its outcome. Only the relay's REFUSAL
// SENTINELS prove a pre-commit refusal: relay.decodeError maps just the codes in its
// codeToErr table, so bad_request, auth_failed and unsupported arrive as a bare
// fmt.Errorf("relay: %s", code) that errors.Is cannot tell apart from a transport failure.
// Sniffing that message TEXT would make the seq-reuse decision depend on a string the relay
// never promised, so everything unsentinelled is conservatively AppendUnknown.
func ClassifyAppend(err error) AppendOutcome {
	switch {
	case err == nil:
		return AppendDelivered
	case errors.Is(err, relay.ErrQuotaExceeded),
		errors.Is(err, relay.ErrNotAuthorized),
		errors.Is(err, relay.ErrRevoked):
		return AppendRefused
	default:
		return AppendUnknown
	}
}
