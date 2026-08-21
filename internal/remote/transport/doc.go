// Package transport is what remains of slice S6's client-side relay session after
// ADR-007 B98: the two INBOUND-DRAIN PACING primitives the gateway uses, and nothing
// else.
//
//   - DrainPacer meters inbound reads at §6.0's MaxDrainReadsPerSec, in a batching
//     regime that spaces reads so each one carries a batch.
//   - AckBatcher acks the relay OFF the delivery path at MaxDrainAcksPerSec.
//
// Both are constructed by internal/remotegw's command loop; since Wave R9 the phone's
// own drain (mobile/relay.go drainWait) constructs a DrainPacer too, so §6.0's inbound
// budget binds BOTH hops through one implementation.
//
// WHAT USED TO BE HERE, AND WHY IT IS GONE. This package used to own a Session type
// carrying dialing policy, reconnection with an exponential backoff schedule, a
// connection-state machine, a bounded idempotent-op queue, a retry-policy table and a
// durable relay cursor. None of it ever shipped: Session had ZERO production
// constructions (ADR-007 B94), while the phone dialled internal/remote/relay directly
// through mobile/relay.go. Four requirements were nonetheless fenced against it, so
// PB-NET-4's backoff numbers and PB-NET-7's "every call times out" read as met while
// being false of the handset. Keeping a well-documented, well-tested dead API beside a
// live one is a trap for the next implementer -- adopting Session to get the backoff
// would have silently brought back the op queue B90 established is unbuildable -- so it
// was deleted rather than left with a note.
//
// The transport-SECURITY fences that used to live beside it (TLS policy, pin renewal,
// platform pinning, the release-build probes, and the control that keeps relay.Dial free
// of production callers) had relay's own exported surface as their subject and moved to
// internal/remote/relay as package relay_test. They were never about Session.
//
// The deleted design is recoverable from history: Session.Follow was the MailboxWait-based
// concurrent-dispatch mechanism, and for six waves the shipped phone had no equivalent --
// it polled at 500 ms (ADR-007 B100). Wave R9 closed that gap WITHOUT resurrecting
// Session: the phone's drain (mobile/relay.go drainWait) parks the relay client's own
// bounded MailboxWait under a DrainPacer from this package, and the 500 ms poll survives
// only as the compatibility fallback a relay that refuses the mailbox_wait op selects
// (playbook section 10: old relays, by real refusal, never by configuration).
package transport
