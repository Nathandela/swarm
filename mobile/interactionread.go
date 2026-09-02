package swarmmobile

// Mirror M3.1 / M3.3's two READS on the facade (ADR-014), added by the Wave R6 fix-pack.
//
// WHY THEY ARE HERE AT ALL. ADR-014 §1 stated as an accepted decision that
// interaction_history and interaction_detail "are gateway-routed", and review finding B6
// established that no such route existed: internal/remotegw had no arm for either action and
// no action constant for either name, so a phone-issued read fell to opForAction's default
// and was refused "unsupported command action". The gateway arm now exists -- and an arm with
// no producer is the same unreachable claim one layer over, so the phone's half is built with
// it. What stays deferred, and is disclosed as deferred, is the Kotlin AFFORDANCE: the "load
// earlier" control and the full-output expander are the M3 view slice's, not this one's.
//
// They are UNSIGNED, which is journal_resync's decision verbatim (schema.ActionJournalResync
// records the whole argument): a device-signed read would need an actionClass entry, the only
// fitting class is control, capability is pinned at enrollment and never read from the wire,
// and the result would be that an observe-tier device could not read its own transcript's
// past. Sealing under the epoch content key already proves the asker is the paired device.
// The gates that DO apply are the daemon's, and finding B2 is the reason that sentence can
// now be written honestly: handleInteractionHistory and handleInteractionDetail require the
// negotiated `journal` capability and honor the kill switch, exactly as journal_read does.
//
// Unlike the other unsigned reads they carry an OPERATION ID, because unlike the other
// unsigned reads they have an ANSWER. terminal_watch and journal_resync are fire-and-forget
// -- their effect arrives as frames on another plane -- while these two reply with the page
// or the body, and a reply with no operation id is a reply no screen can claim.

import (
	"context"
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// maxHistoryPage bounds what one LoadEarlierInteractions may ask for. The daemon refuses a
// non-positive limit as a caller bug (ADR-014 §2) and bounds nothing above; the bound above
// is the phone's, because the reply must fit one sealed mailbox frame and a screen that asks
// for ten thousand records gets a frame it cannot receive rather than a page.
const maxHistoryPage = 200

// LoadEarlierInteractions asks the machine for one bounded page of transcript records
// (Mirror M3.1, ADR-014): ascending, at most limit records, rounded down to an item boundary
// so a page never begins in the middle of a message. With a non-empty beforeItem the page is
// strictly older than that item. An empty beforeItem asks for the newest retained page, which
// is the anchorless form used by cold-open and conversation Reload.
//
// beforeItem is an ITEM ID and never a cursor or a position (IS-ENV-2): a daemon restart's
// reconciliation legitimately re-delivers the same items at new cursors, so a cursor-paged
// read would silently skip or repeat after one. Empty is the explicit newest-page sentinel,
// not a cursor substitute. The authenticated answer is folded durably with its receive
// high-water before Outcome can expose its payload-free verdict; HistoryFloor -- "nothing
// older than this is retained" -- therefore survives process death with the page it describes.
//
// LIVE-ONLY: with no connection it refuses ErrClassOffline having stored nothing. A queued
// read is a page delivered against a transcript the user has since left.
func (a *App) LoadEarlierInteractions(session, beforeItem string, limit int) (op *Op, err error) {
	defer barrier(&err)
	if session == "" {
		return nil, classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: LoadEarlierInteractions needs a session id"))
	}
	if limit <= 0 || limit > maxHistoryPage {
		return nil, classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: LoadEarlierInteractions needs a positive page limit no larger than the frame bound"))
	}
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	return a.unsignedRead(schema.ActionInteractionHistory, session, readBody{
		history: &schema.InteractionHistoryReq{Session: session, BeforeItem: beforeItem, Limit: limit},
	})
}

// LoadInteractionDetail asks the machine for ONE item's full pre-truncation body (Mirror
// M3.3, IS-CAP-2). Outside the daemon's retention window the answer is the coded
// `unavailable` refusal carrying no records at all -- never a partial body presented as
// whole (IS-CAP-3), which is why a screen must render the refusal rather than whatever it
// already holds.
func (a *App) LoadInteractionDetail(session, itemID string) (op *Op, err error) {
	defer barrier(&err)
	if session == "" || itemID == "" {
		return nil, classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: LoadInteractionDetail needs a session id and an item id"))
	}
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	return a.unsignedRead(schema.ActionInteractionDetail, session, readBody{
		detail: &schema.InteractionDetailReq{Session: session, ItemID: itemID},
	})
}

// readBody is commandBody's unsigned sibling: exactly one of these is set per read.
type readBody struct {
	history *schema.InteractionHistoryReq
	detail  *schema.InteractionDetailReq
}

// unsignedRead mints an operation id, seals one unsigned read with its body, appends it and
// tracks it in flight so Outcome can claim the answer. The bucket discipline is
// unsignedCommandAt's, verbatim and for its stated reason: every producer on the mailbox --
// keystrokes, commands and reads alike -- draws its seq from the one Sequencer inside the
// same held section, or a read numbered outside it is overtaken and dies as stale.
func (a *App) unsignedRead(action, session string, body readBody) (*Op, error) {
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	sc, err := a.sendContext()
	if err != nil {
		return nil, err
	}
	id, err := newOperationID()
	if err != nil {
		return nil, err
	}
	auth := schema.DeviceCommandAuth{
		Action: action, Machine: core.State().Machine, Session: session, OperationID: id,
	}
	var kind phonecore.PublicationKind
	switch {
	case body.history != nil && body.detail == nil:
		kind = phonecore.PublicationHistory
	case body.detail != nil && body.history == nil:
		kind = phonecore.PublicationDetail
	default:
		return nil, classed(ErrClassInvalidRequest, errors.New("swarmmobile: unsigned read needs exactly one body"))
	}
	a.bucketMu.Lock()
	defer a.bucketMu.Unlock()
	if err := core.ExpirePublications(time.Now()); err != nil {
		return nil, err
	}
	pending := phonecore.PendingPublication{
		LogicalID: id, OperationID: id, Kind: kind, SessionID: session,
		Command: auth,
		History: body.history, Detail: body.detail,
		Phase: phonecore.PublicationPrepared, CreatedAt: time.Now(),
	}
	if err := a.preparePublicationLocked(core, sc, pending, ""); err != nil {
		return nil, err
	}
	if err := a.flushPendingPublicationsLocked(context.Background(), sc); err != nil {
		a.wakePublicationPump()
	}
	// Wake even after successful admission: the pump owns the bounded no-reply timer.
	a.wakePublicationPump()
	op := &Op{OperationID: id, Action: action, SessionID: session}
	a.issue(op)
	return op, nil
}

// HistoryFloor reports whether the machine has said that NOTHING older than this session's
// oldest folded item is still retained (ADR-014 §2's honest floor). A screen reads it to stop
// offering "load earlier" forever: false means either that more exists or that no page has
// been read yet, and those are the same state to a screen -- it should offer the control.
func (a *App) HistoryFloor(session string) (atFloor bool, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return false, err
	}
	return core.State().HistoryFloor[session], nil
}

// HistoryAtCapacity reports whether THIS PHONE could not hold the last page it asked for
// (phonecore.MaxBackfillPerSession), so more history exists and this handset cannot show it.
//
// IT IS NOT HistoryFloor, and the two must never be collapsed. The floor is the MACHINE's
// sentence -- "nothing older than this is retained" -- and a screen that dropped its "load
// earlier" control on THIS fact while showing the floor's silence would be telling the reader
// they had reached the beginning of a conversation that goes further back. A screen that reads
// true here stops offering the control AND says why; that is the honest end of a bounded
// transcript, and the alternative measured in review round 2 is a control the user taps
// forever with nothing moving.
func (a *App) HistoryAtCapacity(session string) (capped bool, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return false, err
	}
	return core.State().HistoryCapped[session], nil
}
