package phonecore

// The phone's TRANSCRIPT model: the consumer half of docs/specifications/interaction-schema.md
// (ADR-009, ADR-010, Accepted 2026-08-07).
//
// An interaction item travels as a BARE journal record whose type is "interaction" and whose
// payload is the item object -- no new mailbox kind, no new demux branch, no new repair
// channel (IS-LAYER-1/-4). So nothing here touches snapshot.go's kind switch: the fold hangs
// off the EXISTING kind-less branch, one level deeper, on the record's own type.
//
// It is a SECOND model beside SessionCache rather than a widening of it, because IS-SS-1
// splits them: group_transition shapes the roster, session_status shapes the transcript, and
// the two are rendered by different screens. Folding an item into the roster would put a
// session on the triage list off the back of a tool call.

import (
	"crypto/sha256"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// RecordTypeInteraction is journal.TypeInteraction as it appears ON THE WIRE (IS-LAYER-1,
// Level 2 of §1's three discriminator levels). It is spelled out here rather than imported:
// internal/journal is daemon-side and outside the bound dependency closure (PB-BIND-0), and
// the phone only ever sees the string.
const RecordTypeInteraction = "interaction"

// RecordTypeStructuredGap is journal.TypeStructuredGap as it appears ON THE WIRE: the
// daemon-authored capability-degrade boundary of ADR-017 T2 rule 2 / playbook §6.1. Spelled
// out here for RecordTypeInteraction's reason (internal/journal is outside PB-BIND-0's bound
// closure).
const RecordTypeStructuredGap = "structured_gap"

// ItemSchemaVersion is the item schema version this build understands -- the item's own `v`
// (§2), distinct from journal.SchemaVersion, from protocol.Version and from
// StateSchemaVersion. A higher one is DEGRADED, never dropped (IS-COMPAT-4).
const ItemSchemaVersion = 1

// MaxItemsPerSession bounds the durable transcript per session.
//
// The number is §5's own proposed retention figure ("the most recent 200 items per session",
// IS-CAP-3) and is PROPOSED AND UNRATIFIED, exactly as §5's preamble says of every number in
// that section. It is reused here rather than invented because the daemon's detail-fetch
// retention and the phone's transcript retention answer the same question, and two different
// numbers would mean a client offering "view full output" for items the daemon has dropped.
//
// It is PER SESSION rather than global so a busy session cannot evict a quiet one's
// transcript, and it is bounded at all because the alternative is MailboxRouter.rebind's
// recorded OpOutcomes residual ("never pruned, so every launch re-offers every outcome ever
// recorded") reproduced on a plane that grows per tool call rather than per operation.
const MaxItemsPerSession = 200

// MaxBackfillPerSession bounds the OLDER items one session may hold from explicit "load
// earlier" reads (ADR-014), BESIDE MaxItemsPerSession's live window.
//
// IT IS A SECOND REGION AND NOT A LARGER BOUND, and the reason is the Wave R6 round-2 probe:
// with one bound, insertLocked puts a page's older records at the FRONT and trimLocked evicts
// oldest-first, so the page and the trim target the same end. At the bound a 50-record page
// landed and was evicted inside the same call -- 200 items held before, 200 after, none of
// the 50 surviving -- while the screen went on offering the control, which is the livelock
// historyPageStart's own doc says it refused to create on the daemon side.
//
// The live window is therefore trimmed WITHOUT counting backfilled items and never evicts
// one, and the backfill window is bounded by refusing a page that does not fit (ApplyPage).
// The total a session can hold is the two together, which is what a handset must be able to
// afford: history the reader asked for, on top of the recent conversation the retention bound
// exists to protect.
const MaxBackfillPerSession = 200

// The eight kinds of §3. A kind outside this set is skipped and costs the consumer nothing
// but a cursor advance (IS-COMPAT-1).
const (
	KindUserMessage     = "user_message"
	KindAgentMessage    = "agent_message"
	KindToolRun         = "tool_run"
	KindFileChange      = "file_change"
	KindApprovalRequest = "approval_request"
	KindApprovalResolve = "approval_resolved"
	KindPlanUpdate      = "plan_update"
	KindSessionStatus   = "session_status"
)

// KindStructuredGap is the NINTH kind, and it is deliberately not one of §3's eight: no
// producer of an interaction item ever emits it. It is the daemon's own structured_gap
// boundary (ADR-017 T2 rule 2) folded into the transcript AS AN ELEMENT, so the tear has a
// row of its own between the items either side of it.
//
// IT EXISTS BECAUSE THE TEAR WAS INVISIBLE (Wave R6 review finding B4). applyLocked dropped
// every record whose type was not `interaction`, so a probe that folded item A at cursor 1, a
// structured_gap at cursor 2 and item B at cursor 3 rendered A and B ADJACENT: the phone drew
// a continuous conversation across a boundary the daemon had proved was discontinuous. The
// gap must be able to be DRAWN, and a client can only draw what the fold retains.
const KindStructuredGap = "structured_gap"

// The four statuses of §4. Three are terminal, and IS-ST-1 allows at most one of them per
// item_id with no record after it.
const (
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusDeclined   = "declined"
)

// Item is ONE folded interaction item: every record that carried its item_id, merged by the
// rules of §6 (IS-ENV-2 folds by item_id, never by position).
//
// Body is the LATEST record's item object VERBATIM, and Text is the reconstruction. The two
// are not redundant: for a streamed agent_message the latest record carries only the last
// increment (IS-DELTA-1), so a client that read `text` out of Body would render the tail of a
// message as the whole of it. Every other kind's fields are read from Body -- this package
// deliberately decodes none of them, which is what makes an unknown field free (IS-COMPAT-2)
// and keeps the per-kind rendering where it belongs, in the client.
type Item struct {
	SessionID string
	ItemID    string
	// Cursor is the FIRST record's cursor: the item's position in the transcript, which
	// ordering follows (IS-LAYER-3). Taking the latest record's cursor instead would move a
	// streaming message to the end of the transcript on every increment.
	Cursor uint64
	// LastCursor is the cursor of the LATEST record folded into this item: the per-item high
	// water the fold refuses to go behind. IS-LAYER-3 gives items no private sequence number
	// ("for successive records of one streamed item, cursor order IS delta order"), so a record
	// that does not advance past it is a replay or a reorder and not a delta -- and folding one
	// concatenates an increment twice (IS-DELTA-1) and re-collapses the item's fields to older
	// values. The repair channel PRODUCES those records by design: IS-CAP-4 sizes a reseed's
	// events half to fit one frame, so it re-delivers whatever the cut includes.
	//
	// It is per ITEM and not per stream, which is the one place this cannot copy
	// SessionCache's single cursor: a repair legitimately re-delivers records the phone MISSED
	// at cursors BELOW its highest folded one -- that is what the repair is for -- and a
	// stream-wide high water would reject exactly those.
	//
	// It is DURABLE with the item for the reason Resolved is: a guard rebuilt in memory comes
	// back zero, and the first resync after a process death asks from cursor zero (the journal
	// read cursor is memory-only), so the reseed answering it re-delivers the very records that
	// built the restored transcript.
	LastCursor uint64
	Kind       string
	Status     string
	TurnID     string
	TSUnixMs   int64
	Text       string
	Truncated  bool
	Detail     bool
	// Source is a user_message's honest phone-vs-terminal attribution (Mirror M2.4:
	// `source` is daemon-stamped at injection time, never guessed). Empty where the
	// wire carried none -- an absent fact stays absent.
	Source string
	// OperationID names WHICH of this phone's sends the agent echoed back, stamped by
	// the daemon beside [Source] at the same moment and for the same reason: it is the
	// only party that watched the injection.
	//
	// IT IS WHAT LETS A SENT MESSAGE SETTLE HONESTLY (owner ruling R6). A send is
	// acknowledged when the daemon wrote bytes into a PTY, not when the CLI accepted
	// them, so a bubble that settles on the acknowledgement claims a delivery the wire
	// cannot back. This is the echo, and the echo is the delivery. Matching on TEXT
	// instead would inherit the mis-attribution the daemon has probed and refuses to
	// rely on (skeleton/chat.go: an owner-typed "yes" while a phone send of "yes" was
	// pending). Empty on every item nobody claimed -- an owner's own prompt included.
	OperationID string
	// ToolKind is a tool_run's flat glyph vocabulary (Mirror M2.2, `tool_kind`): the §7
	// action type journalled at the item's top level so a card picks a glyph from one
	// field and parses nothing (IS-TOOL-1). Empty where the wire carried none.
	ToolKind string
	// Degraded marks an item whose `v` is higher than ItemSchemaVersion: rendered as far as
	// this build understands it, never dropped and never fatal (IS-COMPAT-4).
	Degraded bool
	// Resolved is set on an approval_request once its approval_resolved has been folded
	// (IS-LIFE-2). It is what lifts the retention exemption of IS-LIFE-3, and it is DURABLE
	// with the item because the exemption has to survive the process death Android hands out
	// routinely -- a flag rebuilt in memory comes back clear and re-exempts a card that was
	// answered hours ago.
	Resolved bool
	// Revision is plan_update's monotonic per-session counter (§3.7). It is carried so a LATE
	// lower revision can be discarded rather than applied (IS-PLAN-1).
	Revision int
	Body     json.RawMessage
	// Backfilled marks an item this phone holds because the READER asked for it -- a page of
	// ADR-014 history folded through ApplyPage -- rather than because the machine pushed it.
	// It is what puts the item in the second retention region: see MaxBackfillPerSession.
	Backfilled bool
}

// terminal reports whether a status ends the item (§4).
func terminal(s string) bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusDeclined
}

// knownKind reports whether kind is one of §3's eight.
func knownKind(kind string) bool {
	switch kind {
	case KindUserMessage, KindAgentMessage, KindToolRun, KindFileChange,
		KindApprovalRequest, KindApprovalResolve, KindPlanUpdate, KindSessionStatus:
		return true
	}
	return false
}

// wireItem is the item object as it arrives: §2's envelope plus the FOUR per-kind fields the
// fold itself needs. Everything else stays in the raw body.
//
// The four are not an exception to "the client decodes the kinds": each one is a rule this
// package must apply, not a field it renders. `text` is IS-DELTA-1's increment, `revision` is
// IS-PLAN-1's discriminator, `interaction_id` is what makes an approval_resolved resolve
// something (IS-LIFE-2), and the envelope is IS-ENV-3's all-or-nothing check.
type wireItem struct {
	V             int       `json:"v"`
	ItemID        string    `json:"item_id"`
	TS            time.Time `json:"ts"`
	TurnID        string    `json:"turn_id"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	Truncated     bool      `json:"truncated"`
	Detail        bool      `json:"detail"`
	Text          string    `json:"text"`
	Revision      int       `json:"revision"`
	InteractionID string    `json:"interaction_id"`
	// Source and ToolKind are carried onto Item verbatim (Mirror M2.4/M2.2): the fold
	// itself applies no rule to either, but they are the two per-kind facts the
	// transcript renders per row, so they cross the fold rather than costing a JSON
	// parse of Body per draw.
	Source      string `json:"source"`
	ToolKind    string `json:"tool_kind"`
	OperationID string `json:"operation_id"`
}

// ItemStore is the phone's transcript: items folded by item_id, ordered by cursor.
// Concurrency-safe, mirroring SessionCache and SnapshotCache.
type ItemStore struct {
	mu    sync.Mutex
	items []Item // ordered by Cursor ascending
}

// NewItemStore returns an empty transcript.
func NewItemStore() *ItemStore { return &ItemStore{} }

// Apply folds one journal record into the transcript and reports whether it mutated anything.
// A record that is not an interaction record, or whose item cannot be rendered, returns false
// -- which is a SKIP and not an error: the frame that carried it is still consumed and its
// cursor still advances (IS-ENV-3, IS-COMPAT-1), and the caller must not read false as a
// reason to stale the stream.
func (s *ItemStore) Apply(rec schema.JournalRecord) (applied bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(rec, false)
}

// ApplyPage folds ONE page of ADR-014 history -- records the READER asked for, strictly older
// than the item they named -- into the backfill region, and reports whether the page was HELD.
//
// HELD IS NOT "CHANGED SOMETHING". A page whose records this phone already folded lands nothing
// and is still held: the caller's question is whether there is room for what the machine sent,
// because the answer it renders is "this phone can hold no more of this conversation", and
// saying that over a re-delivered page would take the control away for a reason that is not
// true.
//
// IT IS ALL OR NOTHING, and that is the whole of the bound. A page that does not fit in what
// MaxBackfillPerSession leaves is refused ENTIRELY rather than partly: half a page is a hole
// in the middle of a conversation with nothing marking it, which is the silent bridge ADR-017
// forbids one plane over. A caller that gets false has not lost data -- the machine still has
// it -- but it MUST say so rather than go on offering a control that does nothing, which is
// the livelock this whole region exists to end.
//
// Headroom is measured on a shadow fold of the whole page, exactly as this store would retain
// it. Counting raw records falsely refuses a streamed item whose many deltas become one item;
// counting only new ids misses in-place folds such as plan replacement. The shadow keeps the
// item bound exact without mutating the live transcript before the all-or-nothing decision.
func (s *ItemStore) ApplyPage(recs []schema.JournalRecord) (held bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(recs) == 0 {
		// An empty page is nothing to hold and nothing to refuse: the machine answered, it
		// simply had nothing older. The floor is what says so, not this.
		return true
	}
	shadow := &ItemStore{items: append([]Item(nil), s.items...)}
	sessions := map[string]struct{}{}
	for _, rec := range recs {
		sessions[rec.SessionID] = struct{}{}
		shadow.applyLocked(rec, true)
	}
	for session := range sessions {
		if shadow.backfillHeldLocked(session) > MaxBackfillPerSession {
			return false
		}
	}
	for _, rec := range recs {
		s.applyLocked(rec, true)
	}
	return true
}

// ApplyDetail folds ONE interaction_detail reply (Mirror M3.3, IS-CAP-2): the full
// pre-truncation body of an item this phone ALREADY HOLDS, replacing the clipped
// reconstruction where the card already stands.
//
// IT IS NOT Apply, AND THE DIFFERENCE IS THE FINDING. The daemon's reply carries no cursor --
// the body comes out of the capture-time side store, not the journal -- and the card a user
// taps is `truncated` and `completed`. Both of applyLocked's guards therefore reject it: the
// cursor does not strictly advance the item, and IS-ST-1 refuses any record after a terminal
// status. Probed: the fetch folded NOTHING while the press reported success. Forcing a cursor
// above the high water does not help while the item is terminal, and with a non-terminal item
// the agent_message branch CONCATENATES (IS-DELTA-1's increment rule) and produces
// "HEAD...HEAD AND THE WHOLE REST OF IT" -- a garbled body presented as the whole of it,
// which is exactly the ambiguity IS-CAP-3 exists to forbid.
//
// So a detail reply is a REPLACEMENT and travels its own path: the item keeps its identity,
// its position and its backfill region, and its body, text and truncation flag become the
// machine's. A reply for an item this phone does not hold folds NOTHING and says so -- there
// is no cursor to insert one at, and inventing a position would drop a body somewhere in a
// conversation nobody can defend.
func (s *ItemStore) ApplyDetail(rec schema.JournalRecord) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.Type != RecordTypeInteraction || len(rec.Item) == 0 {
		return false
	}
	var w wireItem
	if err := json.Unmarshal(rec.Item, &w); err != nil {
		return false
	}
	if w.V == 0 || w.ItemID == "" || !knownKind(w.Kind) {
		return false // IS-ENV-3, exactly as the fold path applies it
	}
	i := s.indexOf(rec.SessionID, w.ItemID)
	if i < 0 {
		return false
	}
	it := s.items[i]
	it.Text = w.Text // the WHOLE body, never concatenated: see the doc
	it.Body = append(json.RawMessage(nil), rec.Item...)
	it.Truncated = w.Truncated
	it.Detail = w.Detail
	it.Degraded = w.V > ItemSchemaVersion
	s.items[i] = it
	return true
}

// applyAll folds a batch (a reseed's events half), reporting whether any record landed.
func (s *ItemStore) applyAll(recs []schema.JournalRecord) (applied bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range recs {
		if s.applyLocked(rec, false) {
			applied = true
		}
	}
	return applied
}

// backfillHeldLocked is how many of one session's items came from a reader's page.
func (s *ItemStore) backfillHeldLocked(session string) int {
	n := 0
	for _, it := range s.items {
		if it.SessionID == session && it.Backfilled {
			n++
		}
	}
	return n
}

func (s *ItemStore) applyLocked(rec schema.JournalRecord, backfill bool) bool {
	if rec.Type == RecordTypeStructuredGap {
		return s.applyStructuredGapLocked(rec, backfill)
	}
	if rec.Type != RecordTypeInteraction || len(rec.Item) == 0 {
		return false
	}
	var w wireItem
	if err := json.Unmarshal(rec.Item, &w); err != nil {
		// Not a decodable item object. Unknown FIELDS are already free here (encoding/json
		// ignores them, IS-COMPAT-2); this is a malformed body, which is the IS-ENV-3 case.
		return false
	}
	// IS-ENV-3: v, item_id and kind are all required, and a producer that would emit an item
	// without them emits nothing -- so one that arrives without them is not a partial item to
	// render. IS-COMPAT-1 puts an unknown kind in the same bucket: skipped, cursor advanced,
	// stream NOT stale.
	if w.V == 0 || w.ItemID == "" || !knownKind(w.Kind) {
		return false
	}

	next := Item{
		SessionID:   rec.SessionID,
		ItemID:      w.ItemID,
		Cursor:      rec.Cursor,
		LastCursor:  rec.Cursor,
		Kind:        w.Kind,
		Status:      w.Status,
		TurnID:      w.TurnID,
		Truncated:   w.Truncated,
		Detail:      w.Detail,
		Source:      w.Source,
		OperationID: w.OperationID,
		ToolKind:    w.ToolKind,
		Degraded:    w.V > ItemSchemaVersion, // IS-COMPAT-4: render what we understand, mark it
		Revision:    w.Revision,
		Text:        w.Text,
		Body:        append(json.RawMessage(nil), rec.Item...),
		Backfilled:  backfill,
	}
	if !w.TS.IsZero() {
		// §2: the machine's instant for THIS record. The wire journal record carries none, so
		// a consumer that substituted arrival time would be making up the one clock PB-APP-11
		// says it may not.
		next.TSUnixMs = w.TS.UnixMilli()
	}

	if i := s.indexOf(rec.SessionID, w.ItemID); i >= 0 {
		prev := s.items[i]
		if rec.Cursor <= prev.LastCursor {
			// A record that does not ADVANCE this item's cursor is a replay or a reorder, not a
			// delta (IS-LAYER-3), and the repair channel delivers both by design (IS-CAP-4).
			// Same discipline as SessionCache's "defense in depth behind the transactional
			// cursor", per item rather than per stream. STRICTLY greater, unlike SessionCache's
			// tolerance of an equal cursor: that tolerance exists for a roster snapshot, whose
			// records deliberately share one read cursor across sessions -- two records of one
			// item at one cursor would be the same record.
			//
			// ponytail: the ceiling this leaves is a record MISSED inside one item's own run --
			// the phone folded 10 and 12, and the repair returns 11. It stays missed: IS-DELTA-1
			// reconstructs by concatenation in cursor order, and an item keeps a high water
			// rather than a record of what it absorbed, so there is nowhere to put a late middle
			// increment. Dropping it beats appending it in the wrong place, and rebuilding the
			// item from the reseed instead would need the events half to be guaranteed WHOLE,
			// which IS-CAP-4's cut is exactly the absence of.
			return false
		}
		if terminal(prev.Status) {
			// IS-ST-1: at most one terminal status per item_id, and no record after it. The
			// producer owns the rule; a duplicate or reordered record must not be the place
			// the consumer breaks it by re-opening a finished card.
			return false
		}
		if w.Kind == KindAgentMessage {
			next.Text = prev.Text + w.Text // IS-DELTA-1: the increment, concatenated in cursor order
		}
		next.Cursor = prev.Cursor // the item keeps its FIRST record's position
		next.Resolved = prev.Resolved
		// AN ITEM DOES NOT CHANGE REGION UNDER A LATER RECORD. A live delta folding into a
		// backfilled item stays protected from the live trim; conversely an anchorless page
		// delta for a live item must not move it into the reader-only region.
		next.Backfilled = prev.Backfilled
		s.items[i] = next
	} else {
		if w.Kind == KindPlanUpdate && !s.acceptPlanLocked(rec.SessionID, w.Revision) {
			return false
		}
		s.insertLocked(next)
	}

	if w.Kind == KindApprovalResolve {
		s.resolveLocked(rec.SessionID, w.InteractionID)
	}
	s.trimLocked(rec.SessionID)
	return true
}

// wireGap is the structured_gap record's payload as it arrives (internal/daemon's
// StructuredGapEvent). Only the two fields the transcript renders are decoded.
type wireGap struct {
	TS     time.Time `json:"ts"`
	Reason string    `json:"reason"`
}

// applyStructuredGapLocked folds ONE daemon-authored structured_gap boundary into the
// transcript as its own element (ADR-017 T2 rule 2). See KindStructuredGap for why the fold
// must retain it at all.
//
// IDENTITY IS THE EMISSION INSTANT, not the cursor. IS-ENV-2's rule -- identity is the
// item_id, never a position -- has no item_id to work with here (the daemon authors this
// record, no producer stamps one), and a cursor-derived id would DUPLICATE the gap the first
// time a reconciliation re-delivered the record at a new cursor, which is exactly the case
// ADR-014 says cursors do not survive. The emission `ts` is minted once, durably, inside the
// payload, so two records carrying it are the same proven boundary and fold to one row.
//
// A gap with no decodable payload still lands, keyed by cursor and with no words: the
// FACT of the tear is the load-bearing half, and refusing to render an unexplained tear
// would put the fold back where finding B4 found it.
func (s *ItemStore) applyStructuredGapLocked(rec schema.JournalRecord, backfill bool) bool {
	if rec.SessionID == "" {
		// A session-neutral gap names no transcript to tear.
		return false
	}
	var g wireGap
	if len(rec.Item) > 0 {
		_ = json.Unmarshal(rec.Item, &g)
	}
	id := structuredGapID(rec)
	if i := s.indexOf(rec.SessionID, id); i >= 0 {
		// Already folded: a re-delivery of a boundary, not a second boundary.
		return false
	}
	it := Item{
		SessionID:  rec.SessionID,
		ItemID:     id,
		Cursor:     rec.Cursor,
		LastCursor: rec.Cursor,
		Kind:       KindStructuredGap,
		// Terminal by construction: a proven boundary is a fact, not a process, so
		// nothing may re-open it (IS-ST-1's posture, applied to a one-record element).
		Status:     StatusCompleted,
		Text:       g.Reason,
		Body:       append(json.RawMessage(nil), rec.Item...),
		Backfilled: backfill,
	}
	if !g.TS.IsZero() {
		it.TSUnixMs = g.TS.UnixMilli()
	}
	s.insertLocked(it)
	s.trimLocked(rec.SessionID)
	return true
}

// structuredGapID derives the one stable identity applyStructuredGapLocked uses.
func structuredGapID(rec schema.JournalRecord) string {
	var g wireGap
	if len(rec.Item) > 0 {
		_ = json.Unmarshal(rec.Item, &g)
	}
	if !g.TS.IsZero() {
		return "structured_gap:" + g.TS.UTC().Format(time.RFC3339Nano)
	}
	return "structured_gap:" + strconv.FormatUint(rec.Cursor, 10)
}

// acceptPlanLocked applies IS-PLAN-1: a plan_update is LATEST-STATE per session, so the
// session keeps exactly one -- the highest revision -- and a lower one arriving late is
// discarded rather than shown as a second, older plan. It reports whether this revision wins,
// dropping the superseded item when it does.
func (s *ItemStore) acceptPlanLocked(session string, revision int) bool {
	for i, it := range s.items {
		if it.SessionID != session || it.Kind != KindPlanUpdate {
			continue
		}
		if revision < it.Revision {
			return false
		}
		s.items = append(s.items[:i:i], s.items[i+1:]...)
		return true
	}
	return true
}

// resolveLocked marks the approval_request an approval_resolved answers (IS-LIFE-2), which is
// what ends its IS-LIFE-3 retention exemption. A resolution whose request this phone never
// held (its reseed floor cut it, IS-CAP-4) marks nothing and is not an error.
func (s *ItemStore) resolveLocked(session, interactionID string) {
	if interactionID == "" {
		return
	}
	if i := s.indexOf(session, interactionID); i >= 0 {
		s.items[i].Resolved = true
	}
}

// trimLocked enforces MaxItemsPerSession, oldest first, EXEMPTING unresolved
// approval_requests (IS-LIFE-3). The exemption can push a session above the bound; that is the
// intended direction, because the alternative is evicting the card the machine is blocked on,
// and the exemption lifts the moment the resolution lands.
// It also EXEMPTS backfilled items, from the count as well as from the eviction: they are
// the reader's own second region, bounded by ApplyPage rather than by this trim. Counting
// them here without evicting them would make one page evict a page's worth of LIVE
// conversation instead, which is the same defect wearing the other coat.
func (s *ItemStore) trimLocked(session string) {
	n := 0
	for _, it := range s.items {
		if it.SessionID == session && !it.Backfilled {
			n++
		}
	}
	for i := 0; i < len(s.items) && n > MaxItemsPerSession; i++ {
		it := s.items[i]
		if it.SessionID != session || it.Backfilled || pendingApproval(it) {
			continue
		}
		s.items = append(s.items[:i:i], s.items[i+1:]...)
		i--
		n--
	}
}

// pendingApproval reports whether it is an approval_request still awaiting its resolution.
func pendingApproval(it Item) bool {
	return it.Kind == KindApprovalRequest && !it.Resolved
}

// indexOf finds a folded item by (session, item_id) -- IS-ENV-2's key, never a position.
func (s *ItemStore) indexOf(session, itemID string) int {
	for i, it := range s.items {
		if it.SessionID == session && it.ItemID == itemID {
			return i
		}
	}
	return -1
}

// insertLocked places it in cursor order. The common case is an append (records arrive behind
// the mailbox seq guard, so in cursor order); a reseed's events half is the case that is not.
func (s *ItemStore) insertLocked(it Item) {
	i := sort.Search(len(s.items), func(i int) bool { return s.items[i].Cursor > it.Cursor })
	s.items = append(s.items, Item{})
	copy(s.items[i+1:], s.items[i:])
	s.items[i] = it
}

// Resolve looks one item up by (session, item_id) -- the M3 deep-link landing (Mirror M3,
// IS-ENV-2: item identity is the item_id, never a cursor or a position). A notification's
// cursor is stale the moment a reconciliation re-delivers the transcript at new cursors,
// so the resolver answers the item at its CURRENT fold -- and answers ok=false honestly
// for an id the retained window no longer holds or that belongs to another session: a
// deep-link that silently lands somewhere else is worse than one that says "no longer
// here".
func (s *ItemStore) Resolve(sessionID, itemID string) (Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.indexOf(sessionID, itemID); i >= 0 {
		return s.items[i], true
	}
	return Item{}, false
}

// Session is the transcript for one session, in cursor order (a snapshot copy).
func (s *ItemStore) Session(sessionID string) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Item
	for _, it := range s.items {
		if it.SessionID == sessionID {
			out = append(out, it)
		}
	}
	return out
}

// PendingApprovals is every approval_request that has not reached its approval_resolved,
// across all sessions, in cursor order. It is the read IS-LIFE-3 exists to keep answerable
// across a reconnect and a process death.
func (s *ItemStore) PendingApprovals() []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Item
	for _, it := range s.items {
		if pendingApproval(it) {
			out = append(out, it)
		}
	}
	return out
}

// ApprovalBinding is the ADR-007 D7 binding tuple of one pending approval_request: §3.5's
// three DAEMON-AUTHORITATIVE fields, read straight off the item the phone stored.
//
// It exists because an approve must ECHO those three and compute none of them (IS-APR-2), and
// the facade that authors one takes flat strings across the gomobile boundary -- so the tuple
// has to be recovered from the card the handset is holding, which is this store.
type ApprovalBinding struct {
	ShimPID       int
	ShimStartTime int64
	ContentHash   string
	ExpiresAt     time.Time
}

// approvalBody is the §3.5 subset an approve is authored from. It is the ONE place this
// package decodes per-kind item fields, and the exception is narrow on purpose: Item.Body
// exists so a client reads its own kinds (IS-COMPAT-2), but the binding tuple is not
// rendering -- it is wire content the phone must reproduce byte for byte, and a client that
// re-typed it would be one silent divergence away from a command the daemon refuses.
type approvalBody struct {
	AgentInstance struct {
		ShimPID       int   `json:"shim_pid"`
		ShimStartTime int64 `json:"shim_start_time"`
	} `json:"agent_instance"`
	ContentHash string     `json:"content_hash"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

// PendingApproval returns the binding tuple of the UNRESOLVED approval_request (session,
// itemID) names, if this phone holds one that can actually be answered.
//
// It reports false in three cases that are one fact from a caller's side -- there is no card
// here to answer. The item is absent (a reseed floor cut it, or the id is wrong); it has
// already reached its approval_resolved, so IS-LIFE-2 has spent its one resolution and every
// surface has stopped showing it; or its §3.5 fields are missing or malformed, which makes it
// a card no approve can be authored from. The third is worth failing on rather than papering
// over: an approve carrying an invented tuple is refused CodeStaleApproval, which tells the
// user their card is out of date when what actually happened is that it arrived broken.
func (s *ItemStore) PendingApproval(session, itemID string) (ApprovalBinding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(session, itemID)
	if i < 0 || !pendingApproval(s.items[i]) {
		return ApprovalBinding{}, false
	}
	var body approvalBody
	if err := json.Unmarshal(s.items[i].Body, &body); err != nil {
		return ApprovalBinding{}, false
	}
	if body.ContentHash == "" || body.ExpiresAt == nil || body.AgentInstance.ShimPID == 0 {
		return ApprovalBinding{}, false
	}
	if len(body.ContentHash) != 2*sha256.Size {
		// The daemon ships a bare 64-char hex digest (internal/skeleton/approval.go). Anything
		// else the signer would refuse anyway; refusing here names the CARD as the problem.
		return ApprovalBinding{}, false
	}
	return ApprovalBinding{
		ShimPID:       body.AgentInstance.ShimPID,
		ShimStartTime: body.AgentInstance.ShimStartTime,
		ContentHash:   body.ContentHash,
		ExpiresAt:     *body.ExpiresAt,
	}, true
}

// All is the whole transcript in cursor order (a snapshot copy) -- what the durable blob
// carries.
func (s *ItemStore) All() []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Item(nil), s.items...)
}

// Len is the number of folded items.
func (s *ItemStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// restore seeds one item from durable state, bypassing the fold (the entry IS the fold's
// result, not a record being applied on top of it) -- the SessionCache.restore rule.
func (s *ItemStore) restore(it Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, it)
}

// itemsWith folds recs into a COPY of the live transcript and returns the durable list, so a
// commit that fails leaves the live store untouched (Core.foldContent's rule, and the same
// shape sessionsWith uses).
func itemsWith(live *ItemStore, recs ...schema.JournalRecord) []Item {
	scratch := NewItemStore()
	scratch.items = live.All()
	scratch.applyAll(recs)
	return scratch.All()
}
