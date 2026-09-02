package skeleton

// The APPROVAL LIFECYCLE's BACK HALF -- interaction-schema.md §8 (IS-LIFE-2 and IS-LIFE-4) and
// §4's IS-ST-2, all daemon-side.
//
// A1a shipped the front half: an adapter's pending permission becomes an `approval_request`
// item that reaches the phone and survives a journal repair. This file is the rest of the
// contract that item is only half of:
//
//	IS-LIFE-4  the item ships the ADR-007 D7 BINDING TUPLE -- agent_instance, content_hash and
//	           the daemon-authoritative expires_at -- and an arriving approve is validated
//	           against the stored tuple BEFORE any effect, refused with a D10 code otherwise.
//	IS-LIFE-2  EVERY approval_request reaches EXACTLY ONE approval_resolved, on all five paths:
//	           allowed/denied, cancelled, superseded, expired, answered_locally.
//	IS-ST-2    an agent instance that dies with items still `in_progress` closes each `failed`,
//	           before the session's terminal session_status.
//
// WHY HERE AND NOT internal/daemon. Same forced answer as the producer's (interaction.go): a
// resolution is an ITEM, items are released through remotegw.ItemAdmission, and
// `remotegw -> protocol -> daemon` makes a daemon that imported the floor an import cycle.
// skeleton.Daemon IS the assembled daemon, so nothing is smuggled up a layer.
//
// ONE PENDING APPROVAL PER SESSION, which is the schema's own model rather than a convenience:
// IS-LIFE-3 rules out the roster half for re-delivery partly because a roster record "cannot
// hold two pending approvals for one session". A second request for one session is therefore
// not a second card, it is a supersession -- which is exactly what §3.6's `superseded` names.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// approvalTTL is the DAEMON-AUTHORITATIVE window an approval_request may be answered in (§3.5:
// "expires_at is daemon-authoritative; a phone countdown is display-only").
//
// 120 s is spike-SC's shorter measured CLI hold, not a guess: Codex's
// item/commandExecution/requestApproval was verified waiting to 120 s with no timeout or
// auto-deny (the maximum that spike tested), Claude Code's PermissionRequest to 300 s. The
// daemon's window must sit INSIDE the CLI's own, because an approve the daemon still accepts
// after the CLI stopped waiting is applied to nothing. It also leaves room for ADR-010 §4's
// push-wake deferral (<= 30 s), which is the arithmetic that ADR names: 120 - 30 = 90 s, still
// above spike-SC's 60 s one-tap floor.
//
// ponytail: PROPOSED AND UNRATIFIED on §5's own terms -- one constant, no per-CLI table. A
// per-adapter window would be the right shape only once an adapter can source its own hold, and
// no adapter implements the capture extension at all today.
const approvalTTL = 120 * time.Second

// §3.6's resolution vocabulary -- the six values `decision` may take.
const (
	resolveAllowed         = "allowed"
	resolveDenied          = "denied"
	resolveCancelled       = "cancelled"
	resolveSuperseded      = "superseded"
	resolveExpired         = "expired"
	resolveAnsweredLocally = "answered_locally"
)

// §3.6's `by`: who resolved it. The daemon owns expiry; the agent owns cancel and supersede.
const (
	byPhone  = "phone"
	byOwner  = "owner"
	byDaemon = "daemon"
	byAgent  = "agent"
)

// contentHashSlot is the fixed-width placeholder the item is serialized WITH, so the hash can
// be taken over the item as shipped without changing its length.
//
// THE CANONICALIZATION, stated once because a hash whose input is not stated is not checkable:
// content_hash is SHA-256 over THE SHIPPED BYTES WITH ITS OWN SLOT ZEROED. Self-reference has
// no other honest resolution -- a hash cannot cover itself -- and this form is re-derivable by
// anyone holding the item, needs no second serialization, and costs the §5 caps nothing because
// the placeholder is exactly as wide as the digest that replaces it. It follows R2's rule
// (truncate, THEN hash): the bytes hashed are the bytes the card renders, so an approve echoed
// off a truncated card still matches (IS-APR-2, ADR-007 D7).
var (
	contentHashZeros = strings.Repeat("0", 64)
	contentHashSlot  = []byte(`"content_hash":"` + contentHashZeros + `"`)
)

// pendingApproval is the ADR-007 D7 binding tuple for ONE unresolved approval_request, held
// machine-side. `machine` and `session` are the map key and the daemon's own identity, so only
// the remaining four are stored.
type pendingApproval struct {
	itemID    string    // the item_id, which IS D7's interaction_id (IS-APR-1)
	turnID    string    // the turn the request belongs to, so its resolution joins it (IS-ENV-1)
	shimPID   int       // the agent instance, as recorded at capture...
	shimStart int64     // ...both halves, because a REUSED pid is a mismatch (S3/F6)
	hash      string    // the content hash the item shipped
	expiresAt time.Time // the daemon's window; a phone countdown is display-only
	// action is the request's own ToolAction.Type, as the ADAPTER classified it at capture
	// (§7's read | edit | write | search | execute | fetch | other). It is held for ONE job:
	// the injection gate hands it back to the adapter, which refuses to type into a dialog
	// raised by a different action than this request's (M1.8). Without it the gate proves only
	// that AN answerable dialog is on screen -- and a hook is fire-and-forget, so a dialog
	// reaches the glass before its own card exists, which is the window in which a phone answer
	// for the request just closed at the terminal would be typed into the one that replaced it.
	action string
	// decisions maps each id the card offered -- in the CLI's own vocabulary (§3.5) -- to the
	// verdict the ADAPTER classified it as at capture (allow | deny | other). It is both the
	// membership set an arriving decision is checked against and the only source for §3.6's
	// allowed/denied split, which is why it is one map and not a set beside a table.
	decisions map[string]string
	// applied is §3.6's resolution the DAEMON ITSELF typed into the session's dialog on the
	// phone's behalf (Mirror M1.2), and appliedOp the phone's operation_id. They are recorded
	// AT INJECTION and read by the OBSERVATION that resolves the request, which is the only
	// thing that lets that record name who answered: a resolution seen after the daemon typed
	// the phone's key is not the owner's `answered_locally`, and saying so would put a decision
	// in the mouth of somebody who never touched the keyboard. Empty until a phone answer is
	// applied, and cleared again if the injection is refused.
	applied   string
	appliedOp string
}

// openItem is one item the producer has journalled `in_progress` and not yet closed. It carries
// only what a terminal record needs: IS-ENV-3 makes `kind` required, and IS-ENV-1 keeps the
// closing record inside the turn that opened it.
type openItem struct {
	kind string
	turn string
}

// ---- capture-side: opening and sealing an approval_request -----------------

// openApprovalLocked writes §3.5's three DAEMON-AUTHORITATIVE fields onto a pending
// approval_request's field set and stores its binding tuple, superseding any predecessor for
// the session. It returns the resolutions to offer. Caller holds itemMu.
//
// The hash is NOT set here: it can only be taken once the item is serialized under §5's caps
// (sealApprovalLocked). The slot is reserved at full width so the seal never changes the length.
func (d *Daemon) openApprovalLocked(session string, it daemon.InteractionItem, in adapter.Interaction, fields map[string]any) []json.RawMessage {
	// IS-LIFE-2's `superseded`: one pending approval per session (IS-LIFE-3), so a newer
	// request replaces the older and the older's card must dismiss everywhere.
	//
	// A record for the item ALREADY pending is not a newer request, it is the SAME one
	// re-announced -- an adapter shaping the still-outstanding permission off a second hook,
	// which itemIDLocked folds under one item_id by design. Superseding it with itself would
	// give one request TWO approval_resolved records (IS-LIFE-2 says exactly one) and would lift
	// the phone's IS-LIFE-3 retention exemption -- ItemStore.resolveLocked marks the request
	// Resolved, dropping the card off PendingApprovals -- while the CLI is still blocked on it.
	// The binding tuple below is still RESTAMPED, because the phone folds the newer record over
	// the older one and so echoes the newer hash and expiry (IS-APR-2).
	var out []json.RawMessage
	if ap := d.approvals[session]; ap == nil || ap.itemID != it.ItemID {
		out = d.resolveApprovalLocked(session, resolveSuperseded, byAgent, "")
	}

	m, _ := d.core.Get(session)
	expires := it.TS.Add(approvalTTL)
	fields["agent_instance"] = map[string]any{"shim_pid": m.ShimPID, "shim_start_time": m.ShimStartTime}
	fields["expires_at"] = expires
	fields["content_hash"] = contentHashZeros

	ids := make(map[string]string, len(in.Decisions))
	for _, c := range in.Decisions {
		ids[c.ID] = c.Verdict
	}
	next := &pendingApproval{
		itemID: it.ItemID, turnID: it.TurnID,
		shimPID: m.ShimPID, shimStart: m.ShimStartTime,
		expiresAt: expires, decisions: ids, action: in.Action.Type,
	}
	// THE RE-ANNOUNCEMENT CARRIES ITS OWN ANSWER FORWARD. The branch above is the same request
	// announced again, and a fresh binding would drop the two fields the injection wrote onto
	// it: `applied` is what tells the OBSERVATION the phone answered (forgetting it attributes
	// the answer to an owner who never touched the keyboard), and `ap.applied != ""` is the
	// case approveInteraction refuses a re-delivered approve on (forgetting it lets a SECOND
	// key into a dialog that has one answer left in it). Latent for claude today -- its ref
	// carries the hook's arrival instant, so a re-announcement mints a new item_id and
	// supersedes -- and not a property of this daemon's side of the contract.
	if ap := d.approvals[session]; ap != nil && ap.itemID == it.ItemID {
		next.applied, next.appliedOp = ap.applied, ap.appliedOp
	}
	d.approvals[session] = next
	return out
}

// sealApprovalLocked stamps the content hash onto a FITTED approval_request payload and records
// it on the binding. Caller holds itemMu.
func (d *Daemon) sealApprovalLocked(session string, payload json.RawMessage) json.RawMessage {
	if !bytes.Contains(payload, contentHashSlot) {
		// Unreachable: the slot is written by openApprovalLocked and is excluded from the fit
		// ceiling (itemUnclippedFields). It is a log rather than a panic because an item with no
		// hash is refused by the validation below -- a card nobody can answer, not a crash.
		log.Printf("interaction: an approval_request lost its content_hash slot before sealing; " +
			"the card will be unanswerable (interaction-schema.md §3.5)")
		return payload
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	if ap := d.approvals[session]; ap != nil {
		ap.hash = digest
	}
	return bytes.Replace(payload, contentHashSlot, []byte(`"content_hash":"`+digest+`"`), 1)
}

// noteItemLocked tracks (or releases) an item for IS-ST-2's sweep. A kind that carries no status
// carries no `in_progress` either (§2: "absent means not applicable to the kind"), so it is
// never open. Caller holds itemMu.
func (d *Daemon) noteItemLocked(session string, it daemon.InteractionItem) {
	if it.Status != adapter.StatusInProgress {
		if open := d.openItems[session]; open != nil {
			delete(open, it.ItemID)
			if len(open) == 0 {
				delete(d.openItems, session)
			}
		}
		return
	}
	open := d.openItems[session]
	if open == nil {
		open = map[string]openItem{}
		d.openItems[session] = open
	}
	open[it.ItemID] = openItem{kind: it.Kind, turn: it.TurnID}
}

// ---- the resolver: IS-LIFE-2's exactly-one guarantee -----------------------

// resolveApprovalLocked emits §3.6's approval_resolved for the session's pending request and
// clears the binding, returning the item to offer. It is the ONE place a resolution is authored
// -- five paths, one record shape -- and it is a no-op when nothing is pending, which is what
// makes "exactly one" hold under a race between two paths (an expiry ticking while a withdrawal
// arrives resolves once, and the loser sees nothing to resolve).
//
// Caller holds itemMu. It returns the payload rather than offering it so no caller holds itemMu
// across a journal append.
func (d *Daemon) resolveApprovalLocked(session, decision, by, operationID string) []json.RawMessage {
	ap := d.approvals[session]
	if ap == nil {
		return nil
	}
	delete(d.approvals, session)
	fields := map[string]any{
		"interaction_id": ap.itemID,
		"decision":       decision,
		"by":             by,
	}
	if operationID != "" {
		// §3.6 echoes it only when a phone ActionApprove drove the resolution. It is the phone's
		// idempotency key and IS-APR-1 forbids it ever equalling the interaction_id.
		fields["operation_id"] = operationID
	}
	payload, _, err := fitItem(daemon.InteractionItem{
		V:      daemon.InteractionSchemaVersion,
		ItemID: newItemID(),
		TS:     time.Now().UTC(),
		TurnID: ap.turnID,
		Kind:   adapter.KindApprovalResolved,
	}, fields)
	if err != nil {
		log.Printf("interaction: an approval_resolved for %s could not be shaped: %v", ap.itemID, err)
		return nil
	}
	return []json.RawMessage{payload}
}

// sweepExpiredApprovals is IS-LIFE-2's `expired` path, driven by the append floor's own ticker.
// Expiry is the daemon's observation and nobody else's: the phone's countdown is display-only
// (§3.5), and a request whose window lapsed with no answer still owes exactly one resolution or
// the card is stuck on every surface.
func (d *Daemon) sweepExpiredApprovals() {
	d.itemMu.Lock()
	now := time.Now()
	out := map[string][]json.RawMessage{}
	for session, ap := range d.approvals {
		if now.After(ap.expiresAt) {
			out[session] = d.resolveApprovalLocked(session, resolveExpired, byDaemon, "")
		}
	}
	d.itemMu.Unlock()
	for session, items := range out {
		d.offerAll(session, items)
	}
}

// noteInteractionStatus is IS-LIFE-2's `answered_locally` path: the owner answered at the
// machine, so the session's pending interaction dimension LEAVES the waiting state without a
// remote decision ever arriving. It is the only one of the five with no adapter event and no
// daemon action behind it -- the transition is the whole observation.
//
// IT FIRES ON THE TRANSITION AND NOT ON THE STATE, which is the difference between a rule and a
// bug: a resolver keyed on "the session is not waiting" dismisses a live card the moment the
// session reports anything at all, including the emit that precedes the capture.
func (d *Daemon) noteInteractionStatus(session string, cur status.Interaction) {
	d.itemMu.Lock()
	d.initInteractionsLocked()
	prev := d.interacted[session]
	d.interacted[session] = cur
	var out []json.RawMessage
	if awaitingInput(prev) && !awaitingInput(cur) {
		// M1.2: this transition is ALSO the observation that a phone answer landed. When the
		// daemon typed that answer itself, the dialog leaving is the phone's decision being
		// applied -- so the record carries what was typed, `by: phone`, and the phone's own
		// operation_id. It is the SAME observation either way; only the attribution differs,
		// and only because the daemon has first-hand knowledge of what it pressed.
		decision, by, operation := resolveAnsweredLocally, byOwner, ""
		if ap := d.approvals[session]; ap != nil && ap.applied != "" {
			decision, by, operation = ap.applied, byPhone, ap.appliedOp
		}
		out = d.resolveApprovalLocked(session, decision, by, operation)
	}
	d.itemMu.Unlock()
	d.offerAll(session, out)
}

// awaitingInput reports whether the session is blocked on a human (status.Status.NeedsInput's
// own condition, restated because this needs the dimension and not the derived group).
func awaitingInput(i status.Interaction) bool {
	return i == status.InteractionPrompt || i == status.InteractionPermission
}

// ---- IS-ST-2: the orphan sweep ---------------------------------------------

// sweepSessionInteractions closes out a session whose agent instance is gone. It runs from
// endSession, which is the ONE hook fired for a shim exit, a lost session and a delete alike.
//
// Order is IS-ST-2's: every item still `in_progress` is closed `failed`, and the session's
// terminal session_status comes after them. The approval resolution goes first because
// IS-LIFE-2 is unconditional -- a request whose agent died still owes exactly one resolution,
// and until it lands the phone's IS-LIFE-3 retention exemption keeps the card both unanswerable
// and unevictable.
//
// ponytail: it emits NOTHING for a session with no open items and no pending approval. The
// terminal session_status is emitted HERE as the marker IS-ST-2 orders the failures against; a
// session_status on every session end regardless is IS-SS-1's transcript marker, which is a
// different rule and no slice has built it. Emitting one for every session that ever ran would
// put a record on the journal for sessions with no transcript at all.
func (d *Daemon) sweepSessionInteractions(session string) {
	d.itemMu.Lock()
	d.initInteractionsLocked()
	out := d.resolveApprovalLocked(session, resolveCancelled, byAgent, "")
	open := d.openItems[session]
	// Sorted so the sweep's own order is deterministic; item_ids are ULIDs, so this is mint
	// order and therefore the order the items appear in the transcript.
	ids := make([]string, 0, len(open))
	for id := range open {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	now := time.Now().UTC()
	for _, id := range ids {
		oi := open[id]
		payload, _, err := fitItem(daemon.InteractionItem{
			V:      daemon.InteractionSchemaVersion,
			ItemID: id,
			TS:     now,
			TurnID: oi.turn,
			Kind:   oi.kind,
			Status: adapter.StatusFailed,
		}, nil)
		if err != nil {
			log.Printf("interaction: could not close orphaned item %s: %v", id, err)
			continue
		}
		out = append(out, payload)
	}
	delete(d.openItems, session)
	delete(d.interacted, session)
	if len(out) > 0 {
		out = append(out, d.sessionStatusItem(session, now)...)
	}
	d.itemMu.Unlock()
	d.offerAll(session, out)
}

// sessionStatusItem is §3.8's transcript marker for the ended session: the roster's own
// dimensions, plus the server-derived display group (IS-SS-1 -- `session_status` is the
// TRANSCRIPT's marker and `group_transition` stays the ROSTER's, and the overlap is intended).
// A session the core no longer knows is reported `lost`, which is the honest reading of "the
// daemon cannot say how it ended".
func (d *Daemon) sessionStatusItem(session string, now time.Time) []json.RawMessage {
	st := status.Status{Process: status.ProcessLost, Turn: status.TurnUnknown, Interaction: status.InteractionUnknown}
	var exit *int
	if m, ok := d.core.Get(session); ok {
		st, exit = m.Status, m.ExitCode
	}
	fields := map[string]any{
		"process":     string(st.Process),
		"turn":        string(st.Turn),
		"interaction": string(st.Interaction),
		"group":       string(status.Derive(st)),
	}
	if exit != nil {
		fields["exit_code"] = *exit
	}
	payload, _, err := fitItem(daemon.InteractionItem{
		V: daemon.InteractionSchemaVersion, ItemID: newItemID(), TS: now, Kind: adapter.KindSessionStatus,
	}, fields)
	if err != nil {
		log.Printf("interaction: could not shape the terminal session_status for %s: %v", session, err)
		return nil
	}
	return []json.RawMessage{payload}
}

// ---- IS-LIFE-4: validating an arriving approve -----------------------------

// approveInteraction validates ONE arriving approve against the stored binding tuple and the
// daemon's own clock, BEFORE any effect, and then APPLIES it: it types the session's own dialog
// keys into the PTY the daemon owns (Mirror M1.2, inject.go). A stale or mismatched approve is
// refused with a code from D10's taxonomy and applies nothing (ADR-007 D7: "never translated
// into a blind keystroke").
//
// machine is the endpoint id the signed command tuple named (D4); operationID is the phone's
// idempotency key off the enclosing Control. The signature, the device capability and the kill
// switch are checked BEFORE this by authorizeCommand and requireRemoteAuthz -- this is the
// content check those cannot make, and it is the one D7 specifies.
//
// IT NOW APPLIES, AND IT NO LONGER RESOLVES. Both halves of that sentence are the M1.2 change,
// and they are one decision. The note that used to stand here -- "what is still the PRODUCER's
// and not this function's is APPLYING the decision" -- described a function that dismissed the
// card on every surface while the CLI stayed blocked on a dialog nobody had answered. The card
// lied. So this now writes the dialog's recorded keys, and emits NOTHING: §3.6's record lands
// when the daemon OBSERVES the dialog leave (noteInteractionStatus above), which is the only
// evidence that anything happened. A dialog that does not move is surfaced by the watchdog.
//
// THE GATE BEFORE THE KEYSTROKE is what makes typing safe at all. Between the phone rendering
// its card and the tap arriving, the owner may have answered at the terminal; the dialog is
// then gone and a "1" lands in the composer as USER INPUT the agent will act on. So the live
// grid must still positively show a dialog the session's adapter has a RECORDED key map for
// (M1.1's fixtures), and anything else is a refusal.
//
// AND IT MUST BE THIS REQUEST'S DIALOG, not merely an answerable one (M1.8). The request's own
// §7 action is carried down to the adapter, which refuses a variant that action does not name.
// Without it the daemon answered whatever question was on the glass: a hook is fire-and-forget,
// so a dialog reaches the screen before its card exists, and a phone answer for the request the
// owner just closed at the terminal was typed into the one that replaced it. The bind is
// PARTIAL and the residue is named rather than papered over -- two Bash dialogs in a row are
// indistinguishable to a recognizer that reads a variant and not a command.
func (d *Daemon) approveInteraction(machine, operationID string, req protocol.ApproveReq) (protocol.ErrorCode, error) {
	endpoint, local, ok := protocol.ParseID(req.Session)
	if !ok {
		return protocol.CodeInvalidField, errIsLife4("approve names %q, which is not a namespaced session id", req.Session)
	}
	// D7's tuple opens on `machine`. An approve routed to the wrong daemon must not resolve a
	// same-named session here, and the signed tuple's machine must be the one the session names.
	if me := d.machineID(); me != "" && (endpoint != me || (machine != "" && machine != me)) {
		return protocol.CodeInvalidField, errIsLife4("approve binds machine %q/%q; this machine is %q", machine, endpoint, me)
	}
	// An approval is provider input just like a composer send. Take its normal
	// FIFO position before re-reading the pending card so a card resolved while
	// this request waited cannot be applied, and so an auth recycle at the queue
	// head embargoes both native decisions and PTY keys.
	lane := d.composerLaneFor(local)
	lane.enter()
	defer lane.leave()
	if lane.recyclingNow() {
		return protocol.CodeInputBusy, errIsLife4(
			"session %q is recycling stale credentials, so approval %q was not applied; nothing was typed",
			req.Session, req.InteractionID)
	}

	d.itemMu.Lock()
	d.initInteractionsLocked()
	ap := d.approvals[local]
	switch {
	case ap == nil || ap.itemID != req.InteractionID:
		// Already resolved, expired, superseded, or never existed. All four are the same fact
		// from the phone's side: the card it is holding is no longer the machine's state.
		d.itemMu.Unlock()
		return protocol.CodeStaleApproval, errIsLife4("no approval %q is pending for session %q", req.InteractionID, req.Session)
	case ap.applied != "":
		// M1.2: an answer has already been TYPED for this request and the daemon is waiting for
		// the observation that resolves it. This is the window a re-delivered approve arrives in
		// -- the resolution no longer lands on the tap, so "already resolved" cannot catch it --
		// and a second keystroke is exactly what must not happen: harmless while the dialog is
		// still up, and a stray character in the composer the moment it goes.
		d.itemMu.Unlock()
		return protocol.CodeStaleApproval, errIsLife4(
			"approval %q has already been answered from a phone; the machine is waiting for its dialog to close",
			req.InteractionID)
	case ap.shimPID != req.AgentInstance.ShimPID || ap.shimStart != req.AgentInstance.ShimStartTime:
		d.itemMu.Unlock()
		return protocol.CodeStaleApproval, errIsLife4("approve names agent instance {%d,%d}; the request was raised by {%d,%d}",
			req.AgentInstance.ShimPID, req.AgentInstance.ShimStartTime, ap.shimPID, ap.shimStart)
	case ap.hash == "" || ap.hash != req.ContentHash:
		d.itemMu.Unlock()
		return protocol.CodeStaleApproval, errIsLife4("approve echoes a content hash that is not the request's")
	case req.ExpiresAt == nil || !req.ExpiresAt.Equal(ap.expiresAt):
		// IS-APR-2: the phone echoes expires_at VERBATIM and computes none of its own, so a value
		// that is not the daemon's is either a stale card or an attempt to buy window.
		d.itemMu.Unlock()
		return protocol.CodeStaleApproval, errIsLife4("approve echoes an expiry that is not the daemon's")
	}
	if time.Now().After(ap.expiresAt) {
		// The window lapsed. Resolve it as expired on the way out: IS-LIFE-2 owes exactly one
		// resolution and the daemon is what observed the lapse.
		out := d.resolveApprovalLocked(local, resolveExpired, byDaemon, "")
		d.itemMu.Unlock()
		d.offerAll(local, out)
		return protocol.CodeStaleApproval, errIsLife4("the daemon's window for approval %q has passed", req.InteractionID)
	}
	verdict, offered := ap.decisions[req.Decision]
	if req.Decision == "" || !offered {
		// The card labels its buttons from decisions[].label (IS-APR-3), so an id outside that
		// set was never rendered to anybody -- which makes it the gateway's or a bug's, not the
		// owner's. Left PENDING: the owner has not answered.
		d.itemMu.Unlock()
		return protocol.CodeInvalidField, errIsLife4("decision %q was not offered by approval %q", req.Decision, req.InteractionID)
	}

	// §3.6's allowed/denied split, classified from the verdict the ADAPTER attached to this
	// decision at capture (owner ruling 2026-08-07). It is the one thing about a decision that is
	// normalized: §3.5 keeps the ids the CLI's own -- Codex offers accept |
	// acceptWithExecpolicyAmendment | cancel -- so a daemon reading `cancel` as a refusal would be
	// guessing at a vocabulary it does not own, which is the posture IS-TOOL-2 forbids for exactly
	// this reason. Conformance obliges every offered decision to carry one.
	//
	// SINCE M1.2 THE VERDICT ALSO SELECTS THE KEYS, which closes the hole its own ponytail note
	// used to record: `other` and a verdict-less decision used to resolve `allowed` with the
	// rest, because §3.6 offers no third value. That is no longer a weaker reading, it is an
	// UNANSWERABLE one -- the recorded dialog has exactly two answerable options and nothing says
	// which of them a decision the adapter could place neither way belongs to. Typing one anyway
	// would be precisely the guess the verdict exists to remove, so it is refused instead.
	decision := resolveAllowed
	switch verdict {
	case adapter.VerdictAllow:
	case adapter.VerdictDeny:
		decision = resolveDenied
	default:
		d.itemMu.Unlock()
		return protocol.CodeInvalidField, errIsLife4(
			"decision %q of approval %q carries no grant/refuse verdict, so no key on the session's "+
				"dialog answers it", req.Decision, req.InteractionID)
	}

	// RECORDED BEFORE IT IS TYPED. The observation that resolves this request can land the
	// instant the keys do -- the status engine may already be mid-sample -- and a resolution
	// that arrived between the write and this note would be attributed to an owner who was
	// never there.
	itemID, action := ap.itemID, ap.action
	ref := d.approvalRefLocked(local, itemID)
	ap.applied, ap.appliedOp = decision, operationID
	d.itemMu.Unlock()

	// THE NATIVE BRANCH FIRST (Wave R7, Mirror M4.3; ADR-013 §R7.5). A session with a live
	// app-server answers its approvals BY JSON-RPC, on the daemon's OWN connection with the id
	// THAT connection received -- r1-codex-gate.md:130-134 recorded exactly this against the
	// real CLI: "NO KEY WAS EVER PRESSED IN THE TUI, yet the TUI's approval dialog closed".
	//
	// Until R7 this path fell straight through to applyDecision -> dialogTap -> ApprovalKeys ->
	// PTY, and Codex was saved from being typed at ONLY by the accident that it proves no
	// ApprovalApplier. The branch order is what makes the prohibition structural.
	if native, handled := d.applyNativeDecision(local, ref, req.Decision); handled {
		if native != nil {
			d.clearAppliedNote(local, itemID)
			return "", errIsLife4("approval %q could not be answered on its backend: %v", req.InteractionID, native)
		}
		// NO watchInjection: nothing was typed, so there is no dialog to watch. Resolution
		// still arrives only BY OBSERVATION -- here the server's own serverRequest/resolved
		// broadcast (backend.go), which is strictly better evidence than a grid read.
		return "", nil
	}

	if err := d.applyDecision(local, verdict, action); err != nil {
		d.clearAppliedNote(local, itemID)
		if errors.Is(err, errNoDialog) {
			// The terminal answered a beat earlier, or the screen is one no recorded key map
			// covers. Either way the card the phone holds is no longer the machine's state,
			// which is what stale_approval says to a retry policy. The card stays PENDING: the
			// daemon refused to type, it did not decide anything.
			return protocol.CodeStaleApproval, errIsLife4("approval %q was not applied: %v", req.InteractionID, err)
		}
		// A machine-side capability or transport failure. It carries NO code for
		// ApproveInteraction's reason: none of D10's six describes one, and mapping it to
		// not_authorized would send a correctly-paired owner off to re-pair a device that is fine.
		return "", errIsLife4("approval %q could not be applied: %v", req.InteractionID, err)
	}
	d.watchInjection(local, itemID, verdict, action)
	return "", nil
}

// approvalRefLocked recovers the CLI's OWN id for the pending approval named by itemID -- the
// ref the adapter attached at capture and that the pending JSON-RPC server-request was keyed
// by. The daemon consumes the ref and never puts it on the wire (IS-APR-1), so this reverse
// lookup over the fold map is the only way back to it. Caller holds itemMu.
func (d *Daemon) approvalRefLocked(session, itemID string) string {
	prefix := session + "\x00"
	for key, id := range d.itemIDs {
		if id == itemID && strings.HasPrefix(key, prefix) {
			return key[len(prefix):]
		}
	}
	return ""
}

// applyNativeDecision answers a pending approval over the session's app-server connection.
//
// handled==false means this session has NO native channel and the caller must fall through to
// the keystroke path -- which is Claude's, unchanged. handled==true with a non-nil error means
// the native channel was the right answer and it failed; the caller must NOT then type,
// because a provider whose approvals are answered by RPC has no key to press, ever.
//
// The reply body is the ADAPTER's: InteractionSource.Decision(ref, decisionID) returns the
// descriptor and the CORE writes it (ADR-010 §4, E9.2). That method had NO PRODUCTION CALLER
// ANYWHERE IN THE REPO before this line -- a fact worth stating plainly, and what M4.3 changes.
func (d *Daemon) applyNativeDecision(local, ref, decisionID string) (error, bool) {
	b, ok := d.sessionBackendFor(local)
	if !ok {
		return nil, false
	}
	m, ok := d.core.Get(local)
	if !ok {
		return errors.New("the session is gone"), true
	}
	ad, ok := d.resolveAdapter(m.AgentType)
	if !ok {
		return fmt.Errorf("agent %q has no adapter", m.AgentType), true
	}
	src, ok := adapter.AsInteractionSource(ad)
	if !ok {
		return fmt.Errorf("agent %q sources no interactions, so it can describe no decision", m.AgentType), true
	}
	act, ok := src.Decision(ref, decisionID)
	if !ok || len(act.Reply) == 0 {
		return fmt.Errorf("agent %q describes no way to apply decision %q", m.AgentType, decisionID), true
	}
	// The id THAT CONNECTION received, consumed so a re-delivered approve cannot write a
	// second reply the server would apply to whatever replaced this request.
	id, ok := d.takeServerRequest(local, ref)
	if !ok {
		return fmt.Errorf("no pending server request is outstanding for approval %q", ref), true
	}
	ctx, cancel := context.WithTimeout(context.Background(), backendCallTimeout)
	defer cancel()
	if err := b.conn.Respond(ctx, id, act.Reply); err != nil {
		return err, true
	}
	return nil, true
}

// clearAppliedNote drops the injection note from a session's pending approval, iff it is still
// the same request. A refused injection typed nothing, so nothing may later be attributed to it.
func (d *Daemon) clearAppliedNote(session, itemID string) {
	d.itemMu.Lock()
	if ap := d.approvals[session]; ap != nil && ap.itemID == itemID {
		ap.applied, ap.appliedOp = "", ""
	}
	d.itemMu.Unlock()
}

// machineID is this daemon's stable federation endpoint id, or "" before the assembly wired one
// (a bare test Daemon literal). An empty id skips the machine check rather than failing closed:
// there is nothing to compare against, and the tuple's other four members still bind.
func (d *Daemon) machineID() string {
	if d.api == nil {
		return ""
	}
	return d.api.endpointID
}

// errIsLife4 formats a refusal. The prose rides BESIDE the code, never instead of it: R-PROT.7
// exists because a string-only error cannot drive a phone's retry policy, and D10's taxonomy is
// what the caller seals into Control.ErrorCode.
func errIsLife4(format string, args ...any) error {
	return fmt.Errorf("interaction: "+format, args...)
}

// ---- offering ---------------------------------------------------------------

// offerAll releases lifecycle items into ADR-010 §7's append floor. Every one of them is
// subject to IS-DELTA-2a's per-target ceiling exactly like a captured item -- a resolution is
// not exempt from the budget, it merely takes the head of the queue ahead of prose (IS-DELTA-3).
func (d *Daemon) offerAll(session string, payloads []json.RawMessage) {
	for _, p := range payloads {
		if err := d.items.Offer(session, p); err != nil {
			log.Printf("interaction: an approval-lifecycle item for %s was refused by the append floor: %v", session, err)
		}
	}
}
