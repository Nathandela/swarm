package remotegw

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// The two item kinds the admission queue must tell apart to order and merge correctly
// (interaction-schema.md §3). Every other kind is handled generically, so adding a kind to
// the schema costs this file nothing (IS-COMPAT-1/-3).
//
// They are duplicated as string literals rather than imported from the item producer on
// purpose: this package is the gateway's, it must not link the daemon, and the two names are
// wire vocabulary frozen by the schema.
const (
	itemKindAgentMessage    = "agent_message"
	itemKindApprovalRequest = "approval_request"
)

// itemFieldText is the ONE field this queue ever merges by concatenation (IS-DELTA-1).
const itemFieldText = "text"

// DefaultItemWindow is the ITEM plane's share of the machine->phone budget, split explicitly
// from the terminal plane's instead of letting the two race for it (ADR-013 §R7.4).
//
// THE ARITHMETIC THE OLD NUMBER GOT RIGHT ONLY AT N=1. There are TWO independent 125 ms
// floors, in two processes. This queue releases at most one item record per window,
// machine-wide. CoalescingSink is the SECOND floor of the same width, and it is "THE ONE
// PLACE THE COMBINED CEILING CAN BE ENFORCED" (coalesce.go) because an item released here
// arrives there AS A JOURNAL RECORD, is forwarded immediately and may never be coalesced or
// dropped (R-GW.5) -- and STILL SPENDS THE SLOT, with terminal snapshots held oldest-first
// behind it. At three streaming sessions the item plane's 8 releases/s therefore consumed
// ALL EIGHT of CoalescingSink's slots per second and the terminal plane got exactly ZERO: a
// live peek frozen on a stale grid for as long as the sessions stream, which is the very
// guarantee DefaultAppendWindow exists to protect (PB-GW-7).
//
// 250 ms splits the budget evenly: <= 4 item releases/s machine-wide, leaving >= 4 snapshot
// slots/s for the terminal plane AT EVERY N. Widening is SAFE precisely because the merge is
// LOSSLESS -- a wider window merges MORE and loses nothing -- so the cost is latency and only
// latency (200 ms at the adapter edge + N x 250 ms here + 125 ms downstream).
//
// IT IS THE OWNER'S KNOB. Lowering it back toward DefaultAppendWindow restores the tighter
// token latency and, at N >= 3, restores the frozen peek.
const DefaultItemWindow = 250 * time.Millisecond

// ItemAdmissionConfig configures an ItemAdmission.
type ItemAdmissionConfig struct {
	// Append releases one admitted item toward the journal -- in production
	// daemon.RecordInteraction, which appends it as a bare `interaction` record
	// (IS-LAYER-1). It is called with the queue's lock held, so it MUST NOT call back into
	// the queue.
	Append func(session string, item json.RawMessage) error
	// Window is the minimum spacing between two releases (0 => DefaultItemWindow).
	Window time.Duration
	// Now is the clock seam (nil => time.Now).
	Now func() time.Time
}

// ItemAdmission is ADR-010 §7's producer-side append floor for interaction items: at most
// one item append per window per TARGET, releasing what it holds oldest-first and merging
// losslessly inside the window.
//
// WHERE IT RUNS. This is PRODUCER-side code -- it runs in the daemon's address space, ahead
// of the journal append, not in the gateway sidecar. It lives in this package because the
// window it enforces is this package's (`DefaultAppendWindow`, §6.0's combined ≤ 8
// appends/s) and ADR-010 §7 names it here; it is `CoalescingSink.Terminal`'s shape moved
// upstream of a sink that may not use it. It does not weaken interaction-schema.md §10's
// "the gateway parses no item": nothing in the sidecar's own path (RelaySink, CoalescingSink,
// PushNotifier) reads an item, and this type is never wired into one.
//
// WHY IT EXISTS AT ALL. The gateway forwards a journal record immediately and is forbidden
// to coalesce or drop one (R-GW.5), while each record consumes a slot in the budget shared
// with the terminal stream. ADR-009 makes the journal dense (two appends per tool call
// before any file change), so the only place the stream can be bounded losslessly is here,
// before the record exists.
//
// THE RULES IT IMPLEMENTS, and where each comes from:
//   - one release per window per TARGET, across every session and every kind (IS-DELTA-2a --
//     the governing rule: a per-item_id window does not bind, because N concurrent sessions
//     multiply straight past it, and an overrun is not merely late, it burns an outbound seq
//     and manufactures a gap that stales journal and terminal alike);
//   - a SPACING FLOOR, not a batching delay: an item offered a full window after the last
//     release is admitted at once (IS-DELTA-2);
//   - `agent_message` is the only kind merged by text concatenation, and only within one
//     `item_id` (IS-DELTA-1/-3);
//   - every other kind merges by RECORD COLLAPSE within one `item_id` -- a field-wise union
//     of the two records, later-wins per key, never text (ADR-010 §7, IS-DELTA-3);
//   - `approval_request` takes the head of the queue, then every other non-`agent_message`
//     kind, then prose (IS-DELTA-3). "Never merged" and "never delayed" are different
//     guarantees and only the first is compatible with the ceiling: an approval waits at
//     most one window, at the front.
type ItemAdmission struct {
	cfg    ItemAdmissionConfig
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	last    time.Time               // when the shared slot was last consumed
	uniq    uint64                  // makes an approval_request's key its own, so it never folds
	pending map[string]*pendingItem // session+item_id -> the item awaiting its slot
	// order holds those keys in ARRIVAL order. Release scans it for the head of the
	// highest-priority class, which is oldest-first within the class -- so no session buys
	// its own budget by being loud, and none is starved by another (ADR-010 §7's
	// "releasing per-session queues oldest-first", expressed as one ordered queue because
	// the selection is identical and the bookkeeping is a third of the size).
	order []string
}

// pendingItem is one item holding a place in the queue, plus anything that folded into it.
type pendingItem struct {
	session string
	kind    string
	// raw is the offered bytes, kept BYTE-EXACT while nothing has folded in. An unmerged
	// item is therefore forwarded exactly as the producer serialized it -- which is what
	// lets an approval_request's bytes stay the bytes the daemon hashed (IS-APR-2).
	raw json.RawMessage
	// merged is non-nil once a second record folded in, and then supersedes raw.
	merged map[string]json.RawMessage
}

// NewItemAdmission returns an admission queue that releases into cfg.Append under the §6.0
// floor. One instance per target: the ceiling is per target (IS-DELTA-2a), and a second
// instance would be a second budget for the same phone.
func NewItemAdmission(cfg ItemAdmissionConfig) *ItemAdmission {
	window := cfg.Window
	if window <= 0 {
		// DefaultItemWindow, not DefaultAppendWindow: the two floors are independent and sit
		// in different processes, and collapsing them to one number is the reasoning error
		// ADR-013 §R7.4 corrects. The production path (skeleton's initInteractionsLocked)
		// passes no Window at all, so this default IS the shipped floor.
		window = DefaultItemWindow
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &ItemAdmission{
		cfg:     cfg,
		window:  window,
		now:     now,
		pending: make(map[string]*pendingItem),
	}
}

// Offer hands one serialized item to the queue and releases whatever is due.
//
// The item either merges into the one already pending for its `item_id` or takes its own
// place in the queue; nothing is ever dropped. The returned error is the RELEASE's (or a
// malformed item's): being queued is never an error, exactly as being coalesced is never one
// for a terminal snapshot.
func (a *ItemAdmission) Offer(session string, item json.RawMessage) error {
	itemID, kind, err := itemIdentity(item)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// The key is session-scoped because §2 scopes item_id to the session ("unique within
	// the session"): two sessions minting the same id must never fold into each other.
	key := session + "\x00" + itemID
	if kind == itemKindApprovalRequest {
		// IS-DELTA-3 scopes ADR-010 §7's record collapse to `tool_run` and `file_change` and
		// says every remaining kind "SHALL KEEP ITS OWN RECORD", then says it again for this
		// one: an approval_request is never merged, only delayed at most one window. So a
		// second record for one request -- the CLI withdrawing it, or re-announcing it -- takes
		// its OWN place in the queue instead of folding, which a unique key is the whole of.
		//
		// Its bytes are the content the daemon hashed: §3.5's content_hash is SHA-256 over the
		// item AS SHIPPED with its own slot zeroed, so a field-wise union would re-marshal the
		// object into bytes the digest no longer names -- and IS-APR-2 forbids the phone
		// recomputing one, so nothing downstream could correct it.
		a.uniq++
		key = fmt.Sprintf("%s\x00#%d", key, a.uniq)
	}
	if p, held := a.pending[key]; held {
		if err := p.fold(item); err != nil {
			return err
		}
	} else {
		a.pending[key] = &pendingItem{session: session, kind: kind, raw: item}
		a.order = append(a.order, key)
	}
	return a.release(a.now())
}

// Flush releases the head of the queue if the slot is free, so a transcript that goes quiet
// still ships its last item. It is a no-op when nothing is held or the window has not
// elapsed, and it releases ONE item per call -- the caller drives it on a ticker at the
// window, the same way RunTerminal drives CoalescingSink.Flush on its idle wake.
func (a *ItemAdmission) Flush() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.release(a.now())
}

// Pending reports how many items are waiting for a slot, so a driver knows whether to keep
// ticking.
func (a *ItemAdmission) Pending() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.order)
}

// release forwards the head of the queue and consumes the shared slot. Caller holds a.mu.
//
// ponytail: the queue is UNBOUNDED, deliberately. §7 says the floor "merges rather than
// drops", and every bound worth having here is a drop policy -- which would silently lose a
// tool run or an approval and needs an ADR, not a constant. What keeps it finite in practice
// is the merging: prose folds to one pending item per item_id, and a tool run's open and
// close fold to one record, so the floor binds only above roughly 3-4 tool calls/s
// machine-wide (ADR-010 §7's own arithmetic).
func (a *ItemAdmission) release(now time.Time) error {
	if len(a.order) == 0 || now.Sub(a.last) < a.window {
		return nil
	}
	i := a.headLocked()
	key := a.order[i]
	a.order = append(a.order[:i], a.order[i+1:]...)
	p := a.pending[key]
	delete(a.pending, key)
	a.last = now
	payload, err := p.payload()
	if err != nil {
		return err
	}
	return a.cfg.Append(p.session, payload)
}

// headLocked returns the index in a.order of the item to release: the oldest member of the
// most urgent class. Caller holds a.mu.
func (a *ItemAdmission) headLocked() int {
	head, best := 0, itemClass(a.pending[a.order[0]].kind)
	for i := 1; i < len(a.order) && best > classApproval; i++ {
		if c := itemClass(a.pending[a.order[i]].kind); c < best {
			head, best = i, c
		}
	}
	return head
}

// The admission classes of IS-DELTA-3, most urgent first: an approval_request heads the
// queue, then every other kind, then agent_message prose. Prose is last because it is the
// only kind whose wait is free -- an increment held back is merged into the next release
// rather than delayed on its own, while an approval held behind a prose backlog is an
// expiring request the owner cannot answer.
const (
	classApproval = iota
	classOther
	classAgentMessage
)

func itemClass(kind string) int {
	switch kind {
	case itemKindApprovalRequest:
		return classApproval
	case itemKindAgentMessage:
		return classAgentMessage
	default:
		return classOther
	}
}

// fold merges next into p, under the one rule that differs per kind (IS-DELTA-3): only
// `agent_message` concatenates text; every other kind is a field-wise union, later-wins.
//
// The union is what makes a collapse LOSSLESS: a tool_run's open carries `tool` and
// `action`, its close carries `output_excerpt` and `exit_code`, and a replacement would drop
// half the card. Later-wins on a shared key is right for the envelope fields that genuinely
// move (`ts`, `status`, `stop_reason`) and harmless for the ones that do not.
//
// ponytail: `truncated`/`full_bytes` are unioned like any other key, so a merged record
// reports the LAST clipped record's byte count rather than the sum. The alternative is
// per-field arithmetic this seam cannot do -- it does not know which §5 cap clipped what --
// and the pair's job is to tell a consumer "something here was clipped", which survives.
func (p *pendingItem) fold(next json.RawMessage) error {
	if p.merged == nil {
		m, err := decodeItemObject(p.raw)
		if err != nil {
			return err
		}
		p.merged = m
	}
	extra, err := decodeItemObject(next)
	if err != nil {
		return err
	}
	var text json.RawMessage
	if p.kind == itemKindAgentMessage {
		if text, err = concatText(p.merged[itemFieldText], extra[itemFieldText]); err != nil {
			return err
		}
	}
	for k, v := range extra {
		p.merged[k] = v
	}
	if text != nil {
		p.merged[itemFieldText] = text
	}
	return nil
}

// payload returns the bytes to append: the offered bytes untouched when nothing folded in,
// the merged object otherwise.
func (p *pendingItem) payload() (json.RawMessage, error) {
	if p.merged == nil {
		return p.raw, nil
	}
	return json.Marshal(p.merged)
}

// concatText joins two `text` increments in offer order, which is cursor order (IS-LAYER-3),
// so the consumer's concatenation reproduces the message exactly (IS-DELTA-1). A missing
// increment counts as empty; a non-string one is a producer bug and is refused rather than
// silently rendered as its JSON.
func concatText(a, b json.RawMessage) (json.RawMessage, error) {
	var sa, sb string
	if len(a) > 0 {
		if err := json.Unmarshal(a, &sa); err != nil {
			return nil, fmt.Errorf("remotegw: interaction item %q is not a string: %w", itemFieldText, err)
		}
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &sb); err != nil {
			return nil, fmt.Errorf("remotegw: interaction item %q is not a string: %w", itemFieldText, err)
		}
	}
	return json.Marshal(sa + sb)
}

func decodeItemObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("remotegw: interaction item is not a JSON object: %w", err)
	}
	return obj, nil
}

// itemIdentity reads the two envelope fields the queue keys and orders on. It reads NO other
// field: merging is generic over the §3 kinds, so an unknown kind queues, merges by union
// and ships (IS-COMPAT-1/-2).
//
// An item without both is refused rather than queued: IS-ENV-3 makes such an item
// unemittable at the producer, and one that reached here anyway has no id to fold by
// (IS-ENV-2) and would silently merge into every other id-less item.
func itemIdentity(item json.RawMessage) (itemID, kind string, err error) {
	var env struct {
		ItemID string `json:"item_id"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal(item, &env); err != nil {
		return "", "", fmt.Errorf("remotegw: interaction item is not a JSON object: %w", err)
	}
	if env.ItemID == "" || env.Kind == "" {
		return "", "", errors.New(`remotegw: interaction item has no "item_id" or no "kind" (IS-ENV-3)`)
	}
	return env.ItemID, env.Kind, nil
}
