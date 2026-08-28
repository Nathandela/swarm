package skeleton

// The INTERACTION PRODUCER: the one path that turns an adapter's shaped interactions into
// `interaction` journal records (ADR-009, ADR-010, docs/specifications/interaction-schema.md,
// all Accepted 2026-08-07).
//
// WHY IT LIVES IN THE ASSEMBLY LAYER. ADR-010 §7's append floor is
// remotegw.ItemAdmission, and internal/remotegw ALREADY DEPENDS on internal/daemon
// (remotegw -> protocol -> daemon), so a daemon that imported the floor would be an import
// cycle. internal/skeleton is the only package that imports the adapter contract, the adapter
// registry, the core daemon AND the gateway package -- and skeleton.Daemon IS the assembled
// daemon, so nothing is smuggled up a layer by putting the producer here.
//
// THE PIPELINE, and the rule each stage owns:
//
//	serveHook          an AUTHENTICATED hook callback (S6/G5) with its raw body
//	  -> adapterFor    the session's registry adapter (registry.New in production)
//	  -> AsInteractionSource   ADR-010 §5: absence is the GENERIC-FALLBACK signal, never a defect
//	  -> Interactions          the adapter's pure shaping; the daemon supplies nothing to it
//	  -> Validate              IS-ENV-3: an unshapeable item is emitted NOT AT ALL
//	  -> shapeItem             §2's envelope: v, the minted ULID item_id, ts, turn_id
//	  -> fitItem               §5's caps and IS-CAP-1's truncator, then the serialized bytes
//	  -> ItemAdmission.Offer   ADR-010 §7: one append per window per target, merged not dropped
//	  -> RecordInteractionRaw  the bare journal record (IS-LAYER-1)
//
// WHAT THE DAEMON OWNS AND THE ADAPTER DOES NOT (ADR-010 §3): item ids, ordering, timestamps,
// the turn, and everything below the Offer. What the adapter owns and the daemon does not: the
// content. Neither half is allowed to reach into the other, which is why the CLI's own request
// id (Interaction.Ref) is consumed HERE and never reaches the wire (IS-APR-1).

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/remotegw"
	"github.com/Nathandela/swarm/internal/status"
)

// interaction-schema.md §5's PER-FIELD size caps. daemon.MaxItemBytes is the whole-item cap and
// lives beside the envelope it is measured on; these are the §3 kind fields' caps and live here,
// with the shaping that writes those fields -- which is exactly where daemon.MaxItemBytes's own
// comment says they belong.
//
// THESE NUMBERS ARE STILL PROPOSED AND UNRATIFIED -- §5's own preamble says ADR-009 carries none
// of them and hands the question back, and ADR-009's Amendment 1 ratified only the WHOLE-ITEM cap
// (daemon.MaxItemBytes), deriving it from these as they stand. A later ruling that raises one of
// them must re-derive that cap: the amendment's claim is a RELATION between the two, not a
// property of either alone (fenced by TestInteractionCap_TheItemCapAdmitsEveryDocumentedFieldMaximum).
//
// ponytail: unexported. They are the producer's, no other package shapes a kind field, and an
// exported constant nothing outside can reach is a new entry in B94's unreachable ledger.
const (
	maxTextBytes       = 4 << 10 // `text`, `output_excerpt`, `diff_excerpt`
	maxSummaryBytes    = 256     // `summary`, each `action` string field, each decisions[].label
	maxPromptLines     = 40      // `prompt_lines`: 40 lines...
	maxPromptLineRunes = 200     // ...x 200 RUNES, which is what §5 counts, not bytes
	maxSteps           = 64      // `plan_update.steps`: 64 steps...
	maxStepBytes       = 200     // ...x 200 B
	maxDecisions       = 8       // `decisions`
)

// hookBodyLimit bounds the hook callback the daemon reads off one connection. Reading it at
// all is new -- serveHook used to hand the raw connection to a streaming decoder -- so the
// bound is deliberately WELL ABOVE the largest legitimate post rather than equal to it:
// cmd/swarm's hookStdinLimit caps a CLI's body at 1 MiB, and JSON-escaping that body into the
// callback envelope can nearly double it. A callback over this bound is dropped whole (status
// and interaction alike), which is the same outcome as the malformed-callback path that has
// always existed, and it is what keeps a wedged peer from being an unbounded allocation.
const hookBodyLimit = 4 << 20

// initInteractions wires the append floor and the adapter resolver. It is called from Serve,
// and separately (lazily) by the capture path so a test Daemon literal need not set either --
// the sampleFn/captureFn precedent in serve.go.
func (d *Daemon) initInteractions() {
	d.itemMu.Lock()
	defer d.itemMu.Unlock()
	d.initInteractionsLocked()
}

func (d *Daemon) initInteractionsLocked() {
	if d.adapterFor == nil {
		d.adapterFor = d.registryAdapter
	}
	if d.itemIDs == nil {
		d.itemIDs = map[string]string{}
	}
	if d.turnIDs == nil {
		d.turnIDs = map[string]string{}
	}
	if d.nativeTurns == nil {
		d.nativeTurns = map[string]string{}
	}
	if d.closedTurns == nil {
		d.closedTurns = map[string]string{}
	}
	if d.approvals == nil {
		// The approval lifecycle's state (approval.go), initialized on the same lazy path so a
		// test Daemon literal need not set any of it.
		d.approvals = map[string]*pendingApproval{}
		d.openItems = map[string]map[string]openItem{}
		d.interacted = map[string]status.Interaction{}
	}
	if d.items == nil {
		// ONE queue for the whole machine, because IS-DELTA-2a's ceiling is per TARGET across
		// every session and kind: a per-session queue would be N budgets for one phone.
		d.items = remotegw.NewItemAdmission(remotegw.ItemAdmissionConfig{
			Append: func(session string, item json.RawMessage) error {
				return d.core.RecordInteractionRaw(session, item)
			},
			// nil in production: the floor defaults to time.Now. See Config.ItemClock.
			Now: d.itemClock,
		})
	}
}

// releaseInteractions drives the floor's clock. ItemAdmission releases at most one item per
// Offer, so a transcript that goes quiet would hold its last item until the next event
// arrived -- and the last item of a turn is exactly the one a user is waiting for. The ticker
// runs at the window itself: a tick with nothing held is a lock and a comparison.
func (d *Daemon) releaseInteractions(ctx context.Context) {
	t := time.NewTicker(remotegw.DefaultAppendWindow)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// IS-LIFE-2's `expired` rides the same tick: expiry is the daemon's own observation
			// and needs a clock nobody else drives (approval.go). A tick with nothing pending is
			// a lock and an empty range.
			d.sweepExpiredApprovals()
			if err := d.items.Flush(); err != nil {
				// The backlog is the actionable half: an append that fails once is a hiccup, one
				// that fails with items piling up behind it is a stalled transcript.
				log.Printf("interaction: append floor release failed (%d item(s) still held): %v",
					d.items.Pending(), err)
			}
		}
	}
}

// serveHookInteractions is serveHook's second half: the AUTHENTICATED callback's captured body,
// offered to the session's adapter. It runs only after engine.HandleCallback accepted the
// callback -- an unauthenticated post must not reach the owner's transcript any more than it
// reaches their status (S6/G5).
// A callback with NO captured body is still offered. Skipping it here would be the daemon
// deciding what an event can shape, and that is the adapter's decision alone: `Interactions`
// takes the whole HookPayload, so a shaper may legitimately answer from the event name. A
// non-capture row shapes nothing because the SHAPER finds nothing in it, which is where
// ADR-010 §5 puts that judgement -- measured: the guard cost two existing tests (§4).
func (d *Daemon) serveHookInteractions(cb engine.Callback) {
	// The session's agent type comes from the core's own launch record when the
	// daemon has one; a callback for a session the core does not know about (a
	// drain replaying a spool the launch-time wiring has not yet threaded through,
	// R6) falls back to the empty string, which registryAdapter (production)
	// resolves to nothing -- the SAME "no capture" outcome an early return here
	// would give, with no behavior change for any session the core DOES know.
	var agentType string
	if m, ok := d.core.Get(cb.SessionID); ok {
		agentType = m.AgentType
	}
	ad, ok := d.resolveAdapter(agentType)
	if !ok {
		return
	}
	payload := adapter.HookPayload{
		Event: cb.Event,
		// The CLI's OWN event body, kept whole by `swarm hook` for this event's capture=raw row
		// and carried on the callback (ADR-010 §6). It is what the flattened Payload structurally
		// cannot hold: `tool_input`, `tool_response` and a diff are nested objects.
		Raw:          cb.Raw,
		ReceivedAtMs: time.Now().UnixMilli(),
	}
	// ingestHookBytes authenticated this callback before reaching this function.
	// Persist identity before shaping so the durable resume seam cannot be skipped
	// by an adapter that produces no interaction for this event. A write failure is
	// best-effort for the callback and never suppresses its transcript item; a later
	// event carrying the same id retries SetConversationID.
	if identity, ok := adapter.AsConversationIdentitySource(ad); ok {
		if id, ok := identity.ConversationIDFromEvent(payload); ok {
			if err := d.core.SetConversationID(cb.SessionID, id); err != nil {
				log.Printf("skeleton: could not persist authenticated conversation identity for session %s", cb.SessionID)
			}
		}
	}
	// With the identity known, the CLI's own name for this conversation can be looked up
	// (ADR-022). Best-effort and synchronous: a handful of small files per callback.
	d.adoptLiveSessionName(cb.SessionID, ad)
	d.captureInteractions(cb.SessionID, ad, payload)
}

// captureInteractions shapes one captured event body into items and offers each to the append
// floor. It returns the number offered, which is what a caller can assert on: an event that
// shaped nothing and an event whose items were all refused are different outcomes.
//
// ADR-010 §5 IS THE FIRST BRANCH AND NOT AN ERROR PATH. An adapter that implements no capture
// extension is complete and fully supported; native capture is an upgrade. Every adapter except
// internal/adapter/claude is still in that state, so this returning 0 is a normal case, not a
// defect.
func (d *Daemon) captureInteractions(sessionID string, ad adapter.Adapter, p adapter.HookPayload) int {
	src, ok := adapter.AsInteractionSource(ad)
	if !ok {
		// ponytail: the generic fallback itself (deriving items from the sanitized snapshot,
		// ADR-010 §5) is NOT built here -- ADR-009's fallback adapter is excluded from this
		// program. This is the decision point, and its no-op arm is the honest state of that
		// decision today.
		return 0
	}
	d.initInteractions()
	n := 0
	for _, in := range src.Interactions(p) {
		if err := in.Validate(); err != nil {
			// IS-ENV-3, all-or-nothing: a consumer's only recourse for a partial item is to skip
			// it, and a skipped record has still burned a cursor. Its siblings still ship -- one
			// bad shape must not silence a whole event.
			log.Printf("interaction: %s dropped an unshapeable item: %v", p.Event, err)
			continue
		}
		if in.Kind == adapter.KindUserMessage && in.Source == adapter.SourceSynthetic {
			// KEEP THE TURN, DROP THE BUBBLE (phone refit W2.4, round-1 review ruling). The
			// CLI's own envelope is its only turn-opening signal, so the turn opens here exactly
			// as it would on the owner's prompt -- and the item is neither persisted nor
			// published: nothing reaches the wire or the phone, and no cursor is burned.
			d.openSyntheticTurn(sessionID, in)
			continue
		}
		payload, resolved, err := d.shapeItem(sessionID, in, p)
		if err != nil {
			log.Printf("interaction: %s could not be shaped: %v", p.Event, err)
			continue
		}
		// IS-LIFE-2's supersede/cancel resolutions, authored by the shaping that observed them
		// (approval.go). They are offered FIRST so the older card's dismissal is ordered ahead of
		// whatever replaced it, and they are not counted: n is what the ADAPTER shaped.
		d.offerAll(sessionID, resolved)
		if err := d.items.Offer(sessionID, payload); err != nil {
			log.Printf("interaction: %s was refused by the append floor: %v", p.Event, err)
			continue
		}
		n++
	}
	return n
}

// openSyntheticTurn runs the turn-open half of shapeItem for a SourceSynthetic user_message
// and nothing else: no item id, no journal record, no composer-echo correlation (an envelope
// echoes no phone send).
func (d *Daemon) openSyntheticTurn(sessionID string, in adapter.Interaction) {
	d.itemMu.Lock()
	defer d.itemMu.Unlock()
	d.initInteractionsLocked()
	d.turnIDLocked(sessionID, in)
}

// shapeItem builds §2's envelope around the adapter's normalized fields and returns the item
// SERIALIZED. Everything it decides is daemon-authoritative by ADR-010 §3: the schema version,
// the id, the instant, the turn -- and the bytes, because §5's caps are measured on the
// serialization and IS-CAP-1's `truncated`/`full_bytes` pair is decided by it.
//
// It also returns any approval_resolved items the shaping OBSERVED (approval.go): a second
// pending request for the session supersedes the first, and a terminal record for the pending
// one is the CLI withdrawing it. Both are IS-LIFE-2 paths and both are visible only here, at the
// moment the item is shaped -- which is why they ride out of this function rather than being
// discovered by a scan somewhere else.
func (d *Daemon) shapeItem(sessionID string, in adapter.Interaction, p adapter.HookPayload) (json.RawMessage, []json.RawMessage, error) {
	fields := interactionFields(in)
	ts := time.Now().UTC()
	if p.ReceivedAtMs > 0 {
		// The CAPTURE instant, not the append instant. Substituting the latter is the PB-APP-11
		// clock mistake, and the floor can hold an item for a window before it appends.
		ts = time.UnixMilli(p.ReceivedAtMs).UTC()
	}

	d.itemMu.Lock()
	defer d.itemMu.Unlock()
	d.initInteractionsLocked()
	it := daemon.InteractionItem{
		V:      daemon.InteractionSchemaVersion,
		ItemID: d.itemIDLocked(sessionID, in.Ref),
		TS:     ts,
		TurnID: d.turnIDLocked(sessionID, in),
		Kind:   in.Kind,
		Status: in.Status,
	}

	if in.Kind == adapter.KindUserMessage {
		// Injection-time correlation (Mirror M2.4, 8.1 step 3): the adapter honestly
		// reports every prompt as owner-typed -- it cannot know -- and the daemon, the
		// only party that watched the injection, re-attributes the one that echoes an
		// accepted composer send (chat.go).
		d.stampComposerEchoLocked(sessionID, in.Text, in.ClientRef, fields)
	}

	var resolved []json.RawMessage
	pending := in.Kind == adapter.KindApprovalRequest && in.Status == adapter.StatusInProgress
	if pending {
		resolved = d.openApprovalLocked(sessionID, it, in, fields)
	} else if in.Kind == adapter.KindApprovalRequest {
		// A terminal record for the request the daemon is holding: the CLI withdrew the prompt
		// (IS-LIFE-2's `cancelled`). If some other path already resolved it -- the owner answered
		// at the machine, the window lapsed -- there is nothing pending and this is a no-op, which
		// is what keeps "exactly one" true.
		if ap := d.approvals[sessionID]; ap != nil && ap.itemID == it.ItemID {
			resolved = d.resolveApprovalLocked(sessionID, resolveCancelled, byAgent, "")
		}
	}
	d.noteItemLocked(sessionID, it)

	payload, full, err := fitItem(it, fields)
	if err != nil {
		return nil, resolved, err
	}
	if full != nil {
		// M3.3's capture-time retention: the truncated item shipped with detail=true, so
		// the full pre-truncation body must be retrievable past the clip (IS-CAP-2).
		d.retainDetailLocked(sessionID, it.ItemID, full)
	}
	if pending {
		// After the fit, never before: R2's rule is TRUNCATE, THEN HASH, so the digest names the
		// bytes the card renders and an approve echoed off a truncated card still matches.
		payload = d.sealApprovalLocked(sessionID, payload)
	}
	return payload, resolved, nil
}

// itemIDLocked maps the CLI's own id to the item's minted ULID, so successive records of ONE
// interaction fold under one item_id (IS-ENV-2 folds by item_id and never by position; the
// agent_message increments of IS-DELTA-1 and the tool_run open+close of IS-DELTA-3 are the two
// cases that need it). The adapter is the only party that sees the CLI's id, and the map is
// the only place it is ever read -- IS-APR-1 leaves exactly one id on the wire.
//
// An interaction with no Ref is self-contained: it gets a fresh id and is never folded into.
//
// ponytail: the map is per-session and cleared by endSession, which is the whole of its
// retention policy. A session that ran for a week accumulates one entry per distinct CLI
// interaction; bounding that is a drop policy (a fold key dropped mid-item splits one item in
// two, which is worse than the memory) and needs a ruling, not a constant.
func (d *Daemon) itemIDLocked(sessionID, ref string) string {
	if ref == "" {
		return newItemID()
	}
	key := sessionID + "\x00" + ref
	if id, ok := d.itemIDs[key]; ok {
		return id
	}
	id := newItemID()
	d.itemIDs[key] = id
	return id
}

// turnIDLocked applies IS-ENV-1: a turn OPENS on a user_message and CLOSES on any terminal
// agent_message status. Every item inside carries the open turn's id; `turn_id` is empty
// outside one. The rule is the daemon's alone -- no adapter sources a turn.
//
// ONE ADDITION, AND IT IS A REJOIN RULE (Wave R7, review round 3 MEDIUM 1). A daemon that
// joins a session MID-TURN never saw that turn's opening frame -- the agent's `item/started`
// userMessage fired before this daemon existed -- so the user_message arm above cannot run,
// and round 2 therefore held NO turn for a turn that was demonstrably running. Everything
// downstream then read the session as IDLE: the phone rendered no open turn, a composer send
// carried `expected_turn: ""`, MATCHED, and took the `turn/start` branch -- which
// deliverComposerText's own comment says "would QUEUE A SECOND TURN, so the owner's question
// and the phone's would arrive as two separate conversations" -- and Stop was impossible,
// because interruptTurn refuses an empty expected_turn.
//
// So a frame that NAMES A NATIVE TURN this daemon has not already closed opens one. The
// closed-turn guard is what keeps IS-ENV-1's closure the daemon's own decision: a trailing
// frame of a turn this daemon SAW COMPLETE must never resurrect it, or the session looks busy
// forever and no new turn can be started.
func (d *Daemon) turnIDLocked(sessionID string, in adapter.Interaction) string {
	if in.Kind == adapter.KindUserMessage {
		id := newTurnID()
		d.turnIDs[sessionID] = id
		delete(d.closedTurns, sessionID)
		d.noteNativeTurnLocked(sessionID, in.TurnRef)
		return id
	}
	closing := in.Kind == adapter.KindAgentMessage && terminalStatus(in.Status)
	turn := d.turnIDs[sessionID]
	if turn == "" && !closing && in.TurnRef != "" && d.closedTurns[sessionID] != in.TurnRef {
		turn = newTurnID()
		d.turnIDs[sessionID] = turn
	}
	if turn != "" {
		// Every frame of one Codex turn carries the same `turnId`, so this is idempotent
		// after the opening user_message -- and it is what keeps the native id available
		// for a turn whose opening frame this daemon never saw.
		d.noteNativeTurnLocked(sessionID, in.TurnRef)
	}
	if closing {
		if in.TurnRef != "" {
			if d.closedTurns == nil {
				d.closedTurns = map[string]string{}
			}
			d.closedTurns[sessionID] = in.TurnRef
		}
		delete(d.turnIDs, sessionID)
		delete(d.nativeTurns, sessionID)
	}
	return turn
}

// noteNativeTurnLocked records the CLI's own id for the session's open turn. Caller holds
// itemMu.
//
// An EMPTY ref never clears a recorded one: an adapter that sources no turn identity, or a
// single frame that happens to omit it, must not erase the id the steer and the interrupt
// depend on. That asymmetry is the whole rule -- turn CLOSURE is IS-ENV-1's, above, and is
// never inferred from a missing field.
func (d *Daemon) noteNativeTurnLocked(sessionID, ref string) {
	if ref == "" {
		return
	}
	d.nativeTurns[sessionID] = ref
}

// terminalStatus reports whether s ends an item (§4). in_progress is the only non-terminal
// status, and an absent status belongs to a kind that carries none.
func terminalStatus(s string) bool {
	switch s {
	case adapter.StatusCompleted, adapter.StatusFailed, adapter.StatusDeclined:
		return true
	}
	return false
}

// forgetInteractions drops a session's fold keys and open turn. Called from endSession: the
// ids are meaningless once the CLI they name is gone, and a reused local session id would
// otherwise inherit a stranger's item.
func (d *Daemon) forgetInteractions(sessionID string) {
	d.itemMu.Lock()
	defer d.itemMu.Unlock()
	delete(d.turnIDs, sessionID)
	delete(d.nativeTurns, sessionID)
	delete(d.closedTurns, sessionID)
	// The approval lifecycle's state for the session (approval.go). sweepSessionInteractions has
	// already drained all three; these deletes are what stop a reused local session id inheriting
	// a stranger's pending card if it ever had not.
	delete(d.approvals, sessionID)
	delete(d.openItems, sessionID)
	delete(d.interacted, sessionID)
	delete(d.hookSeq, sessionID) // R6: ingestHookBytes's per-session dedup high-water mark
	// The complete-chat state (chat.go): a dead session's pending injections can never
	// echo, and its retained detail bodies name items nothing can fetch. The store's byte
	// count and order shed the session's entries with it.
	delete(d.pendingSends, sessionID)
	for _, body := range d.details[sessionID] {
		d.detailBytes -= len(body)
	}
	delete(d.details, sessionID)
	kept := d.detailOrder[:0]
	for _, k := range d.detailOrder {
		if k.session != sessionID {
			kept = append(kept, k)
		}
	}
	d.detailOrder = kept
	prefix := sessionID + "\x00"
	for k := range d.itemIDs {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			delete(d.itemIDs, k)
		}
	}
}

// resolveAdapter returns the adapter for an agent type through the overridable seam, defaulting
// it on first use so a test Daemon literal need not set it (the sampleFn/captureFn precedent).
func (d *Daemon) resolveAdapter(agentType string) (adapter.Adapter, bool) {
	d.itemMu.Lock()
	d.initInteractionsLocked()
	fn := d.adapterFor
	d.itemMu.Unlock()
	return fn(agentType)
}

// interactionFields collects §3's per-kind fields into the flat object that rides beside the
// envelope. It emits ONLY what the adapter sourced: an absent field means "not applicable to
// this kind" (§2), and a zero-valued one emitted anyway would read as content.
//
// It applies NO cap. Capping is capFields/clipStrings, downstream, because §2's `full_bytes` is
// "the byte length of the untruncated payload" and the only honest way to know it is to
// serialize the untruncated item once.
//
// ponytail: an approval_request's `agent_instance`, `content_hash` and `expires_at` are
// deliberately ABSENT. All three are daemon-authoritative D7 binding material whose only
// consumer is IS-LIFE-4's ApproveReq wire body and its validation, which no slice has built --
// and IS-APR-2 makes the phone echo them verbatim rather than compute them, so a hash minted
// now with nothing to check it against would be a value nobody can verify. They land with
// ApproveReq. The `keystrokes` map is absent for the opposite reason: IS-APR-3 FORBIDS it on
// the item, and the daemon holds it machine-side.
//
// WHEN content_hash DOES LAND, IT IS HASHED OVER THE ITEM AS SHIPPED -- after fitItem, never
// before. IS-APR-2 makes the phone echo the hash VERBATIM and forbids it computing one, and
// ADR-007 D7 makes the daemon recompute and reject a mismatch; so a hash taken over the
// pre-truncation content would name a body no surface holds, the rendered card could never
// reproduce it, and every approve from a truncated card would be refused as stale. The rule is
// one-directional and cheap to state: TRUNCATE, THEN HASH.
func interactionFields(in adapter.Interaction) map[string]any {
	f := map[string]any{}
	put := func(k string, v any) {
		switch t := v.(type) {
		case string:
			if t == "" {
				return
			}
		case int:
			if t == 0 {
				return
			}
		}
		f[k] = v
	}

	switch in.Kind {
	case adapter.KindUserMessage:
		put("text", in.Text)
		put("source", in.Source)
	case adapter.KindAgentMessage:
		put("text", in.Text)
		put("stop_reason", in.StopReason)
	case adapter.KindToolRun:
		put("tool", in.Tool)
		// Mirror M2.2: `tool_kind` mirrors §7's action type FLAT at the item's top level,
		// beside the envelope, so the phone picks a card glyph from one field and parses
		// nothing (IS-TOOL-1). It is exactly the adapter's classification -- `other` stays
		// `other`, never upgraded (IS-TOOL-2) -- and an unclassified call (zero Action)
		// carries no tool_kind at all, the same absence rule putAction applies.
		put("tool_kind", in.Action.Type)
		putAction(f, in.Action)
		put("output_excerpt", in.OutputExcerpt)
		put("truncation_marker", in.TruncationMarker)
		put("exit_code", in.ExitCode)
	case adapter.KindFileChange:
		put("path", in.Path)
		put("change", in.Change)
		put("old_path", in.OldPath)
		put("diff_excerpt", in.DiffExcerpt)
		put("added", in.Added)
		put("removed", in.Removed)
	case adapter.KindApprovalRequest:
		put("summary", in.Summary)
		putAction(f, in.Action)
		put("mode", in.Mode)
		decisions := make([]map[string]string, 0, len(in.Decisions))
		for _, d := range in.Decisions {
			decisions = append(decisions, map[string]string{"id": d.ID, "label": d.Label})
		}
		f["decisions"] = decisions
		if len(in.PromptLines) > 0 {
			// COPIED, not referenced: capFields and clipStrings clip in place, and the adapter's
			// own slice is not this producer's to overwrite.
			f["prompt_lines"] = append([]string(nil), in.PromptLines...)
		}
	case adapter.KindPlanUpdate:
		put("revision", in.Revision)
		steps := make([]map[string]string, 0, len(in.Steps))
		for _, s := range in.Steps {
			steps = append(steps, map[string]string{"text": s.Text, "state": s.State})
		}
		f["steps"] = steps
	}
	return f
}

// putAction emits §7's structured tool summary, omitted entirely when the adapter classified
// nothing (IS-TOOL-2: an unclassifiable call is "other", never guessed at -- and a call the
// adapter did not classify at all carries no action rather than an empty one).
func putAction(f map[string]any, a adapter.ToolAction) {
	if a == (adapter.ToolAction{}) {
		return
	}
	action := map[string]string{}
	for k, v := range map[string]string{"type": a.Type, "path": a.Path, "query": a.Query, "command": a.Command} {
		if v != "" {
			action[k] = v
		}
	}
	f["action"] = action
}

// ---- §5's caps and IS-CAP-1's truncator ------------------------------------

// fitItem serializes the item with §3's kind fields flat beside the envelope, under §5's caps.
//
// TWO STAGES:
//
//  1. capFields applies §5's per-field caps (the table's own numbers, per field).
//  2. if the item is STILL over daemon.MaxItemBytes, clipStrings lowers ONE ceiling across
//     every string alike, halving it until the item fits.
//
// STAGE 2 NO LONGER FIRES ON §5'S OWN MAXIMA, and that is the whole of ADR-009's Amendment 1:
// the item cap was raised until the per-field maxima fit jointly inside it (the binding case is
// a plan_update at 15 203 B). It is KEPT, unchanged, because three things §5's table does not
// bound in serialized bytes still overrun it, and each is reachable:
//
//   - `prompt_lines` is capped in RUNES, so 40 x 200 four-byte runes is 32 000 B (fenced by
//     TestApprovalRequest_AtTheMaximaTheHashStillNamesTheBytesItShipped);
//   - §5 gives NO cap at all to `tool`, `path`, `old_path`, `truncation_marker` or a decision's
//     `id` -- IS-TOOL-3 requires the truncation marker verbatim, so the item cap is deliberately
//     their only bound;
//   - JSON escaping expands a byte-capped field by up to 6x (a control rune becomes \uXXXX), and
//     §5 counts the field's own bytes, not its encoding.
//
// WHY A UNIFORM CEILING AND NOT A PRIORITY ORDER. Something has to give, and §5 names no order
// in which fields should give it. A privilege list would be this seam ruling on which half of a
// card matters -- a judgement for the schema, not for the producer. The uniform ceiling makes
// the choice no-one's: it cuts the longest strings hardest and leaves short ones untouched, and
// it needs no per-kind knowledge, so a ninth kind costs it nothing (IS-COMPAT-3).
//
// AN ITEM IS NEVER DROPPED FOR SIZE. That is the whole finding: IS-CAP-1 makes an over-cap item
// TRUNCATED with `truncated`/`full_bytes`, and IS-CAP-2 leaves the body fetchable. The append
// boundary's own refusal (daemon.RecordInteractionRaw) is left for genuinely malformed items.
//
// `full_bytes` is measured on the item as it serialized with NOTHING clipped, which is §2's
// "byte length of the untruncated payload" -- and is why stage 1 runs after that first marshal
// rather than inside the field builder.
//
// SINCE WAVE R6 (M3.3) a truncated item ALSO ships `detail: true` and the SECOND return
// value is the item's full pre-truncation serialization -- nil when nothing was clipped --
// so the caller can retain it for IS-CAP-2's detail read. The one first serialization the
// fit already performs IS that body; nothing is serialized twice for it.
func fitItem(it daemon.InteractionItem, fields map[string]any) (json.RawMessage, json.RawMessage, error) {
	payload, err := serializeItem(&it, fields)
	if err != nil {
		return nil, nil, err
	}
	untruncated := len(payload)
	if !capFields(fields) && untruncated <= daemon.MaxItemBytes {
		return payload, nil, nil
	}
	full := payload
	it.Truncated = true
	it.FullBytes = untruncated
	// IS-CAP-2: `detail` says the full body is retrievable, and the caller retaining the
	// returned full body is what makes that true rather than optimistic.
	it.Detail = true
	for ceiling := maxTextBytes; ; ceiling /= 2 {
		if payload, err = serializeItem(&it, fields); err != nil {
			return nil, nil, err
		}
		if len(payload) <= daemon.MaxItemBytes {
			return payload, full, nil
		}
		if ceiling == 0 {
			// Unreachable with the shapes above -- at a one-byte ceiling the structural residue
			// (envelope, 64 step states, 8 decision ids, 40 empty lines) is well under 8 KiB. It
			// is an error rather than a panic because a later kind could invent a field this
			// walker does not reach, and a silent oversized item would be refused downstream with
			// no clue where it came from.
			return nil, nil, fmt.Errorf("interaction: a %s item of %d bytes will not fit the %d-byte cap "+
				"even with every string emptied (interaction-schema.md §5)", it.Kind, len(payload), daemon.MaxItemBytes)
		}
		clipStrings(fields, ceiling)
	}
}

// serializeItem marshals fields into the envelope's flat Fields slot and the whole item after
// it. Empty fields stay nil so MarshalJSON skips the merge entirely.
func serializeItem(it *daemon.InteractionItem, fields map[string]any) (json.RawMessage, error) {
	it.Fields = nil
	if len(fields) > 0 {
		f, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		it.Fields = f
	}
	return json.Marshal(*it)
}

// capFields applies §5's PER-FIELD caps in place and reports whether any of them bound. The keys
// are §3's own names, which is why this sits beside the builder that writes them.
func capFields(f map[string]any) bool {
	clipped := false
	clip := func(s string, max int) string {
		out := clampBytes(s, max)
		clipped = clipped || len(out) != len(s)
		return out
	}
	for _, k := range [...]string{"text", "output_excerpt", "diff_excerpt"} {
		if s, ok := f[k].(string); ok {
			f[k] = clip(s, maxTextBytes)
		}
	}
	if s, ok := f["summary"].(string); ok {
		f["summary"] = clip(s, maxSummaryBytes)
	}
	if a, ok := f["action"].(map[string]string); ok {
		for k, v := range a {
			a[k] = clip(v, maxSummaryBytes)
		}
	}
	if ds, ok := f["decisions"].([]map[string]string); ok {
		if len(ds) > maxDecisions {
			ds, clipped = ds[:maxDecisions], true
			f["decisions"] = ds
		}
		for _, d := range ds {
			d["label"] = clip(d["label"], maxSummaryBytes)
		}
	}
	if ls, ok := f["prompt_lines"].([]string); ok {
		if len(ls) > maxPromptLines {
			ls, clipped = ls[:maxPromptLines], true
			f["prompt_lines"] = ls
		}
		for i, l := range ls {
			// RUNES, not bytes: §5 caps a prompt line at 200 runes, and a byte cap would halve a
			// non-ASCII prompt while passing for correct.
			if out := clampRunes(l, maxPromptLineRunes); len(out) != len(l) {
				ls[i], clipped = out, true
			}
		}
	}
	if ss, ok := f["steps"].([]map[string]string); ok {
		if len(ss) > maxSteps {
			ss, clipped = ss[:maxSteps], true
			f["steps"] = ss
		}
		for _, s := range ss {
			s["text"] = clip(s["text"], maxStepBytes)
		}
	}
	return clipped
}

// itemUnclippedFields are the fields the fit ceiling never touches: §3/§4/§7's CLOSED
// VOCABULARIES, and the DAEMON-MINTED identifiers and digests of §3.5/§3.6. Each is short and
// fixed by the schema, so excluding them costs the fit nothing measurable -- and none of them is
// SMALLER when cut, only invalid: half an enum renders a wrong card (the phone cannot skip it
// the way IS-COMPAT-1 lets it skip an unknown KIND), half a content_hash makes a card
// permanently unanswerable (IS-APR-2 forbids the phone repairing it), and half an
// `interaction_id` resolves nothing.
//
// ponytail: the ENUM half is DELIBERATE AND CANNOT FIRE TODAY -- deleting those rows would
// change no observable behaviour, and the mutation that removes them fails no test (recorded in
// a1-carriage.md). ADR-009's Amendment 1 widened that headroom rather than closing it: the
// ceiling now only falls above 16 KiB, and a 64-step plan_update fits at the full 200 B per step
// without it falling at all. It is kept because the headroom is arithmetic over numbers §5 still
// marks PROPOSED AND UNRATIFIED: a later ruling that raises MaxSteps without re-deriving the item
// cap drives the ceiling under 11 and starts shipping `pen` for `pending`, silently. The
// IDENTIFIER half is NOT speculative: content_hash is 64 characters, which is below the ceilings
// an approval_request with multi-byte prompt lines still reaches (a1-carriage.md measured 128
// and 64).
var itemUnclippedFields = map[string]bool{
	"source": true, "stop_reason": true, "change": true, "mode": true, // top level
	"tool_kind": true,                // §7's closed vocabulary mirrored flat (M2.2); half an enum is invalid, not smaller
	"type":      true, "state": true, // action.type (§7), steps[].state (§3.7)
	"process": true, "turn": true, "interaction": true, "group": true, // session_status (§3.8)
	"decision": true, "by": true, // approval_resolved (§3.6)
	"content_hash": true, "interaction_id": true, "operation_id": true, // the minted ids and the digest
}

// clipStrings clips every non-enum string in f to ceiling bytes, in place, at a rune boundary.
// It is generic over the value shapes the builder produces -- string, []string (prompt_lines),
// map[string]string (action), []map[string]string (decisions, steps) -- so it reaches strings
// §5's table never names (`tool`, `path`, `truncation_marker`, a decision's `id`) too. Those
// carry no per-field cap on purpose: §5 does not give them one and IS-TOOL-3 requires the
// truncation marker VERBATIM, so MaxItemBytes is the only bound they answer to, and this is
// where it is applied.
func clipStrings(f map[string]any, ceiling int) {
	for k, v := range f {
		if itemUnclippedFields[k] {
			continue
		}
		switch t := v.(type) {
		case string:
			f[k] = clampBytes(t, ceiling)
		case []string:
			for i, s := range t {
				t[i] = clampBytes(s, ceiling)
			}
		case map[string]string:
			clipStringMap(t, ceiling)
		case []map[string]string:
			for _, m := range t {
				clipStringMap(m, ceiling)
			}
		}
	}
}

func clipStringMap(m map[string]string, ceiling int) {
	for k, s := range m {
		if !itemUnclippedFields[k] {
			m[k] = clampBytes(s, ceiling)
		}
	}
}

// clampBytes truncates s to at most max BYTES, backing up to a UTF-8 rune start so a multi-byte
// rune is never split (IS-CAP-1: "at a UTF-8 rune boundary, never mid-rune"). A split rune would
// not merely look wrong -- encoding/json substitutes U+FFFD for it, so the phone would render a
// replacement character the machine never saw. It is vt.clampBytes's rule, restated rather than
// imported: the producer has no other reason to link the terminal emulator.
func clampBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	b := max
	for b > 0 && !utf8.RuneStart(s[b]) {
		b--
	}
	return s[:b]
}

// clampRunes truncates s to at most max RUNES -- §5 caps `prompt_lines` per line in runes, not
// bytes. Ranging a string yields rune start offsets, so the cut is on a boundary by construction.
func clampRunes(s string, max int) string {
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

// ---- ids -------------------------------------------------------------------

// crockford is ULID's base32 alphabet (Crockford's, excluding I, L, O and U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newItemID mints §2's item_id: a ULID -- 48 bits of millisecond timestamp then 80 bits of
// crypto/rand, rendered as 26 Crockford base32 characters. It is written here rather than
// pulled in because the module takes no new dependency for 20 lines, and the property the
// schema wants (lexicographic order matches mint order, with no coordination) is the format's,
// not a library's.
//
// A rand failure is not silently degraded to a weaker id: on this machine crypto/rand does not
// fail, and an id that quietly stopped being unique would corrupt the fold rather than error.
func newItemID() string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixMilli())<<16)
	if _, err := rand.Read(b[6:]); err != nil {
		panic("skeleton: crypto/rand failed while minting an interaction item id: " + err.Error())
	}
	out := make([]byte, 26)
	// 128 bits into 26 base32 characters: the first character carries the top 2 bits (ULIDs are
	// 130 bit-slots wide and the top 2 are always zero here), the rest 5 bits each.
	var acc uint64
	bits := 0
	i := 26
	for j := 15; j >= 0; j-- {
		acc |= uint64(b[j]) << bits
		bits += 8
		for bits >= 5 && i > 0 {
			i--
			out[i] = crockford[acc&31]
			acc >>= 5
			bits -= 5
		}
	}
	for i > 0 {
		i--
		out[i] = crockford[acc&31]
		acc >>= 5
	}
	return string(out)
}

// newTurnID mints a turn id. It is the same generator as the item's: a turn is an interval the
// daemon opens and closes (IS-ENV-1) and needs the same "unique, orderable, no coordination"
// property, with no reason to be a second format.
func newTurnID() string { return newItemID() }
