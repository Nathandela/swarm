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
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// RecordTypeInteraction is journal.TypeInteraction as it appears ON THE WIRE (IS-LAYER-1,
// Level 2 of §1's three discriminator levels). It is spelled out here rather than imported:
// internal/journal is daemon-side and outside the bound dependency closure (PB-BIND-0), and
// the phone only ever sees the string.
const RecordTypeInteraction = "interaction"

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
	Cursor    uint64
	Kind      string
	Status    string
	TurnID    string
	TSUnixMs  int64
	Text      string
	Truncated bool
	Detail    bool
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
	return s.applyLocked(rec)
}

// applyAll folds a batch (a reseed's events half), reporting whether any record landed.
func (s *ItemStore) applyAll(recs []schema.JournalRecord) (applied bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range recs {
		if s.applyLocked(rec) {
			applied = true
		}
	}
	return applied
}

func (s *ItemStore) applyLocked(rec schema.JournalRecord) bool {
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
		SessionID: rec.SessionID,
		ItemID:    w.ItemID,
		Cursor:    rec.Cursor,
		Kind:      w.Kind,
		Status:    w.Status,
		TurnID:    w.TurnID,
		Truncated: w.Truncated,
		Detail:    w.Detail,
		Degraded:  w.V > ItemSchemaVersion, // IS-COMPAT-4: render what we understand, mark it
		Revision:  w.Revision,
		Text:      w.Text,
		Body:      append(json.RawMessage(nil), rec.Item...),
	}
	if !w.TS.IsZero() {
		// §2: the machine's instant for THIS record. The wire journal record carries none, so
		// a consumer that substituted arrival time would be making up the one clock PB-APP-11
		// says it may not.
		next.TSUnixMs = w.TS.UnixMilli()
	}

	if i := s.indexOf(rec.SessionID, w.ItemID); i >= 0 {
		prev := s.items[i]
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
func (s *ItemStore) trimLocked(session string) {
	n := 0
	for _, it := range s.items {
		if it.SessionID == session {
			n++
		}
	}
	for i := 0; i < len(s.items) && n > MaxItemsPerSession; i++ {
		it := s.items[i]
		if it.SessionID != session || pendingApproval(it) {
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
