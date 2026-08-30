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
// not a cursor substitute. The answer is claimed with Outcome, which is
// also where its records are folded into the transcript, and where HistoryFloor -- "nothing
// older than this is retained" -- becomes readable via HistoryFloor.
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
	a.bucketMu.Lock()
	defer a.bucketMu.Unlock()
	seq, err := core.Seq().NextCommand()
	if err != nil {
		return nil, err
	}
	var env []byte
	switch {
	case body.history != nil:
		env, err = phonecore.SealInteractionHistoryEnvelope(sc.key, sc.epoch, seq, auth, body.history)
	case body.detail != nil:
		env, err = phonecore.SealInteractionDetailEnvelope(sc.key, sc.epoch, seq, auth, body.detail)
	default:
		return nil, classed(ErrClassInvalidRequest, errors.New("swarmmobile: unsigned read with no body"))
	}
	if err != nil {
		return nil, err
	}
	if _, err := sc.cl.MailboxAppend(context.Background(), sc.target, env); err != nil {
		return nil, err
	}
	op := &Op{OperationID: id, Action: action, SessionID: session}
	a.issue(op)
	return op, nil
}

// adoptInteractionRead folds a claimed interaction_history / interaction_detail reply into
// the live transcript. Called from Outcome the moment the reply is TAKEN, which is also the
// only moment it exists: a durable outcome is a verdict and carries no records
// (phonecore.RecordOutcome), so folding here is not a convenience, it is the one place the
// records can be read.
//
// DURABILITY IS THE LIVE STORE'S, NOT THIS CALL'S, and that is deliberate rather than
// unnoticed: the folded page is held by the in-memory ItemStore and is NOT written into the
// durable transcript snapshot, so it is gone after a process death and a screen that wants it
// again asks again.
//
// THE TWO REPLIES FOLD DIFFERENTLY, AND THAT IS WAVE R6 REVIEW ROUND 2 (see ItemStore's own
// docs for both probes). A history page is records the reader asked for, held in the backfill
// region so the live trim cannot eat the page it was paged in behind -- and refused WHOLE when
// the phone can hold no more, which is what HistoryAtCapacity reports. A detail reply is not a
// record at all in the fold's sense: it is one held item's clipped body REPLACED by the whole
// of it, and putting it through the delta path either dropped it (no cursor, terminal status)
// or concatenated it into a garble presented as the whole (the ambiguity IS-CAP-3 forbids).
//
// A refusal folds NOTHING, including the detail read's `unavailable`: IS-CAP-3's whole point
// is that a refusal must not arrive beside a body.
func (a *App) adoptInteractionRead(ctrl schema.Control) {
	if ctrl.ErrorCode != "" {
		return
	}
	core, err := a.ready()
	if err != nil {
		return
	}
	items := core.Router().Items()
	switch ctrl.Op {
	case schema.ActionInteractionDetail:
		for _, rec := range ctrl.Journal {
			items.ApplyDetail(rec)
		}
	case schema.ActionInteractionHistory:
		held := items.ApplyPage(ctrl.Journal)
		a.mu.Lock()
		if a.historyFloor == nil {
			a.historyFloor = map[string]bool{}
		}
		if a.historyCapped == nil {
			a.historyCapped = map[string]bool{}
		}
		// The machine's floor describes THIS page. If the handset refused the page whole,
		// its oldest folded item did not move and inheriting the reply's true floor would
		// claim a beginning the transcript does not contain. Preserve the last held page's
		// floor and report the separate handset-capacity fact below.
		if held {
			a.historyFloor[ctrl.SessionID] = ctrl.HistoryFloor
		}
		a.historyCapped[ctrl.SessionID] = !held
		a.mu.Unlock()
	}
}

// HistoryFloor reports whether the machine has said that NOTHING older than this session's
// oldest folded item is still retained (ADR-014 §2's honest floor). A screen reads it to stop
// offering "load earlier" forever: false means either that more exists or that no page has
// been read yet, and those are the same state to a screen -- it should offer the control.
func (a *App) HistoryFloor(session string) (atFloor bool, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.historyFloor[session], nil
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
	if _, err = a.ready(); err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.historyCapped[session], nil
}
