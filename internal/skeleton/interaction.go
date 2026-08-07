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
//	  -> ItemAdmission.Offer   ADR-010 §7: one append per window per target, merged not dropped
//	  -> RecordInteractionRaw  the bare journal record (IS-LAYER-1)
//
// WHAT THE DAEMON OWNS AND THE ADAPTER DOES NOT (ADR-010 §3): item ids, ordering, timestamps,
// the turn, and everything below the Offer. What the adapter owns and the daemon does not: the
// content. Neither half is allowed to reach into the other, which is why the CLI's own request
// id (Interaction.Ref) is consumed HERE and never reaches the wire (IS-APR-1).

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/remotegw"
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
	if d.items == nil {
		// ONE queue for the whole machine, because IS-DELTA-2a's ceiling is per TARGET across
		// every session and kind: a per-session queue would be N budgets for one phone.
		d.items = remotegw.NewItemAdmission(remotegw.ItemAdmissionConfig{
			Append: func(session string, item json.RawMessage) error {
				return d.core.RecordInteractionRaw(session, item)
			},
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
			if err := d.items.Flush(); err != nil {
				// The backlog is the actionable half: an append that fails once is a hiccup, one
				// that fails with items piling up behind it is a stalled transcript.
				log.Printf("interaction: append floor release failed (%d item(s) still held): %v",
					d.items.Pending(), err)
			}
		}
	}
}

// serveHookInteractions is serveHook's second half: the AUTHENTICATED callback's body, offered
// to the session's adapter. It runs only after engine.HandleCallback accepted the callback --
// an unauthenticated post must not reach the owner's transcript any more than it reaches their
// status (S6/G5).
func (d *Daemon) serveHookInteractions(cb engine.Callback, body []byte) {
	m, ok := d.core.Get(cb.SessionID)
	if !ok {
		return
	}
	ad, ok := d.resolveAdapter(m.AgentType)
	if !ok {
		return
	}
	d.captureInteractions(cb.SessionID, ad, adapter.HookPayload{
		Event: cb.Event,
		// The body the daemon RECEIVED. ADR-010 §1's `capture: raw` -- which is what makes the
		// CLI's OWN event body survive cmd/swarm's parseHookStdin flattening -- belongs to the
		// producer slices this program excludes, so until one lands this is the callback
		// envelope. That is honest input for a shaper: no shipped adapter shapes anything from
		// it, which is exactly ADR-010 §5's supported state.
		Raw:          body,
		ReceivedAtMs: time.Now().UnixMilli(),
	})
}

// captureInteractions shapes one captured event body into items and offers each to the append
// floor. It returns the number offered, which is what a caller can assert on: an event that
// shaped nothing and an event whose items were all refused are different outcomes.
//
// ADR-010 §5 IS THE FIRST BRANCH AND NOT AN ERROR PATH. An adapter that implements no capture
// extension is complete and fully supported; native capture is an upgrade. Every adapter
// shipped today is in that state, so this returning 0 is the normal case, not a defect.
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
		item, err := d.shapeItem(sessionID, in, p)
		if err != nil {
			log.Printf("interaction: %s could not be shaped: %v", p.Event, err)
			continue
		}
		payload, err := json.Marshal(item)
		if err != nil {
			log.Printf("interaction: %s could not be serialized: %v", p.Event, err)
			continue
		}
		if err := d.items.Offer(sessionID, payload); err != nil {
			log.Printf("interaction: %s was refused by the append floor: %v", p.Event, err)
			continue
		}
		n++
	}
	return n
}

// shapeItem builds §2's envelope around the adapter's normalized fields. Everything it decides
// is daemon-authoritative by ADR-010 §3: the schema version, the id, the instant and the turn.
func (d *Daemon) shapeItem(sessionID string, in adapter.Interaction, p adapter.HookPayload) (daemon.InteractionItem, error) {
	fields, err := interactionFields(in)
	if err != nil {
		return daemon.InteractionItem{}, err
	}
	ts := time.Now().UTC()
	if p.ReceivedAtMs > 0 {
		// The CAPTURE instant, not the append instant. Substituting the latter is the PB-APP-11
		// clock mistake, and the floor can hold an item for a window before it appends.
		ts = time.UnixMilli(p.ReceivedAtMs).UTC()
	}

	d.itemMu.Lock()
	defer d.itemMu.Unlock()
	d.initInteractionsLocked()
	return daemon.InteractionItem{
		V:      daemon.InteractionSchemaVersion,
		ItemID: d.itemIDLocked(sessionID, in.Ref),
		TS:     ts,
		TurnID: d.turnIDLocked(sessionID, in),
		Kind:   in.Kind,
		Status: in.Status,
		Fields: fields,
	}, nil
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
func (d *Daemon) turnIDLocked(sessionID string, in adapter.Interaction) string {
	if in.Kind == adapter.KindUserMessage {
		id := newTurnID()
		d.turnIDs[sessionID] = id
		return id
	}
	turn := d.turnIDs[sessionID]
	if in.Kind == adapter.KindAgentMessage && terminalStatus(in.Status) {
		delete(d.turnIDs, sessionID)
	}
	return turn
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

// interactionFields marshals §3's per-kind fields into the flat object that rides beside the
// envelope. It emits ONLY what the adapter sourced: an absent field means "not applicable to
// this kind" (§2), and a zero-valued one emitted anyway would read as content.
//
// ponytail: an approval_request's `agent_instance`, `content_hash` and `expires_at` are
// deliberately ABSENT. All three are daemon-authoritative D7 binding material whose only
// consumer is IS-LIFE-4's ApproveReq wire body and its validation, which no slice has built --
// and IS-APR-2 makes the phone echo them verbatim rather than compute them, so a hash minted
// now with nothing to check it against would be a value nobody can verify. They land with
// ApproveReq. The `keystrokes` map is absent for the opposite reason: IS-APR-3 FORBIDS it on
// the item, and the daemon holds it machine-side.
func interactionFields(in adapter.Interaction) (json.RawMessage, error) {
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
			f["prompt_lines"] = in.PromptLines
		}
	case adapter.KindPlanUpdate:
		put("revision", in.Revision)
		steps := make([]map[string]string, 0, len(in.Steps))
		for _, s := range in.Steps {
			steps = append(steps, map[string]string{"text": s.Text, "state": s.State})
		}
		f["steps"] = steps
	}
	if len(f) == 0 {
		return nil, nil
	}
	return json.Marshal(f)
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

// ---- the hook body ---------------------------------------------------------

// decodeHookCallback reads one callback AND the bytes it was decoded from. The raw body is what
// the producer needs (ADR-010 §1) and hookclient.Decode consumes the reader, so the value is
// captured first and decoded from memory -- a json.Decoder stops at the end of one value, so
// this does not wait for the peer to close.
func decodeHookCallback(r io.Reader) (engine.Callback, []byte, error) {
	var raw json.RawMessage
	if err := json.NewDecoder(io.LimitReader(r, hookBodyLimit)).Decode(&raw); err != nil {
		return engine.Callback{}, nil, err
	}
	cb, err := hookclient.Decode(bytes.NewReader(raw))
	return cb, raw, err
}
