package phonecore

// FAILING-FIRST (TDD RED, GG-5) for W4 -- the phone-side ingest of interaction items
// (ADR-009, ADR-010, docs/specifications/interaction-schema.md, all Accepted 2026-08-07).
//
// An interaction item travels as a BARE journal record whose type is "interaction" and whose
// payload is the item object (IS-LAYER-1): no new mailbox kind, no new demux branch, no new
// repair channel. So every test here drives the ORDINARY kind-less mailbox frame the phone
// already accepts, and asserts on what the fold does with it.
//
// THE FOUR PROPERTIES THAT NEED A TEST BECAUSE NOTHING ELSE CAN NOTICE THEM:
//
//  1. the transcript is not the roster (IS-SS-1) -- an item that marked its session Present
//     would put a session on the triage screen off the back of a tool call;
//  2. records fold by item_id and agent_message text CONCATENATES (IS-ENV-2, IS-DELTA-1) --
//     a latest-wins fold silently deletes every increment but the last, and the transcript
//     still looks like prose;
//  3. a malformed, unknown-kind or newer-schema item is skipped/degraded rather than
//     dropped or fatal (IS-ENV-3, IS-COMPAT-1/-2/-4), and skipping never stales the stream;
//  4. an UNRESOLVED approval_request survives retention trimming and rides a reseed
//     (IS-LIFE-3) -- the one item whose loss leaves a machine blocked on a card the phone
//     can no longer show.

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

// interactionFrame seals one bare journal record carrying an item object, at the shared
// bucket's next seq. item is raw JSON so a test states the WIRE shape, not a Go literal's
// rendering of it -- an item is produced by the daemon and this side must read whatever it
// sends, including fields this build has never heard of (IS-COMPAT-2).
func interactionFrame(t *testing.T, seq, cursor uint64, session, item string) []byte {
	t.Helper()
	return sealFrameFrom(t, testContentKey(), machineSender, 7, seq,
		marshalEvent(t, schema.JournalRecord{
			Cursor:    cursor,
			SessionID: session,
			Type:      RecordTypeInteraction,
			Item:      json.RawMessage(item),
		}))
}

// agentMessage is one record of a streamed agent_message: the increment this record appends
// (interaction-schema.md §3.2), never the whole body.
func agentMessage(id, text, statusValue string) string {
	return fmt.Sprintf(`{"v":1,"item_id":%q,"ts":"2026-08-07T10:00:00Z","turn_id":"t-1",`+
		`"kind":"agent_message","status":%q,"text":%q}`, id, statusValue, text)
}

// approvalRequest is a pending permission card (§3.5).
func approvalRequest(id string) string {
	return fmt.Sprintf(`{"v":1,"item_id":%q,"ts":"2026-08-07T10:00:00Z","kind":"approval_request",`+
		`"status":"in_progress","summary":"write src/main.rs","content_hash":"sha256:abc",`+
		`"expires_at":"2026-08-07T10:05:00Z","mode":"card"}`, id)
}

// approvalResolved is the resolution every request reaches exactly once (IS-LIFE-2, §3.6).
func approvalResolved(id, requestID, decision string) string {
	return fmt.Sprintf(`{"v":1,"item_id":%q,"ts":"2026-08-07T10:01:00Z","kind":"approval_resolved",`+
		`"status":"completed","interaction_id":%q,"decision":%q,"by":"phone"}`, id, requestID, decision)
}

// driveInteraction accepts one interaction frame and fails the test if the frame itself was
// refused. A SKIPPED item is not a refused frame: IS-ENV-3 and IS-COMPAT-1 both require the
// consumer to advance its cursor over one, so the frame must still commit and ack.
func driveInteraction(t *testing.T, r *MailboxRouter, seq, cursor uint64, session, item string) Receipt {
	t.Helper()
	rcpt, err := r.AcceptCommit(interactionFrame(t, seq, cursor, session, item), 100+cursor)
	if err != nil {
		t.Fatalf("AcceptCommit(interaction seq %d): %v", seq, err)
	}
	return rcpt
}

// ---------------------------------------------------------------------------
// IS-LAYER-1 / IS-SS-1: the transcript is a second model, not a roster update.
// ---------------------------------------------------------------------------

// TestInteraction_FoldsIntoTheTranscriptAndNotTheRoster is the demux itself. An interaction
// record reaches foldContent's kind-less branch, which today unconditionally treats every
// bare record as roster-shaping -- so without the branch this test is asking for, a tool call
// puts its session on the roster with a Group nobody derived, and the durable session list
// grows an entry the machine never announced.
//
// IS-SS-1 is the rule: session_status is the TRANSCRIPT's marker and group_transition remains
// the ROSTER's. A client renders one from each, so an item must shape exactly one of them.
func TestInteraction_FoldsIntoTheTranscriptAndNotTheRoster(t *testing.T) {
	c, r := s10Router(t)

	driveInteraction(t, r, 1, 10, "m1/s-alpha", agentMessage("itm-1", "hello", "in_progress"))

	items := r.Items().Session("m1/s-alpha")
	if len(items) != 1 {
		t.Fatalf("transcript holds %d item(s) for m1/s-alpha; want 1 (IS-LAYER-1: an interaction record folds into the transcript)", len(items))
	}
	if items[0].ItemID != "itm-1" || items[0].Kind != "agent_message" || items[0].Text != "hello" {
		t.Fatalf("folded item = %+v; want item_id itm-1, kind agent_message, text %q", items[0], "hello")
	}
	if items[0].Cursor != 10 {
		t.Errorf("item Cursor = %d; want 10 -- ordering is the journal cursor, ascending (IS-LAYER-3)", items[0].Cursor)
	}

	if _, ok := r.Sessions().Get("m1/s-alpha"); ok {
		t.Error("an interaction record put a session in the ROSTER cache. IS-SS-1 splits the two models: " +
			"group_transition shapes the roster, session_status shapes the transcript, and a tool call " +
			"must not make a session appear on the triage screen")
	}
	if st := c.State(); len(st.Sessions) != 0 {
		t.Errorf("durable Sessions = %+v; want none -- the durable roster must not grow off an item either", st.Sessions)
	}
	if st := c.State(); len(st.Items) != 1 {
		t.Fatalf("durable Items = %d; want 1. The transcript is durable for the reason Sessions and "+
			"Snapshots are: Android SIGKILLs the app routinely, and a transcript held only in memory "+
			"comes back empty behind a high-water that refuses the redelivery", len(st.Items))
	}
}

// TestInteraction_TheJournalReadCursorStillAdvances. The transcript is folded off the shared
// journal stream, so the cursor a resync resumes from must move with it. If only roster
// records advanced it, an interaction-heavy stream (which after ADR-009 is every stream)
// would leave the resync asking for a range thousands of records behind -- and IS-CAP-4 then
// cuts that oversized reseed at a floor, which is content the phone asked for and lost.
func TestInteraction_TheJournalReadCursorStillAdvances(t *testing.T) {
	_, r := s10Router(t)

	driveInteraction(t, r, 1, 41, "m1/s-alpha", agentMessage("itm-1", "hi", "completed"))

	if got := r.Sessions().Cursor(); got != 41 {
		t.Fatalf("journal read cursor = %d after an interaction record at cursor 41; want 41. "+
			"Ordering is the journal cursor (IS-LAYER-3) and the repair channel is the journal's "+
			"own (IS-LAYER-4), so a record consumed and not folded into the roster still moves the "+
			"read position", got)
	}
}

// ---------------------------------------------------------------------------
// IS-ENV-2 / IS-DELTA-1: fold by item_id, and agent_message text concatenates.
// ---------------------------------------------------------------------------

// TestInteraction_AgentMessageRecordsConcatenateByItemID is IS-DELTA-1 verbatim: "a record's
// text SHALL be the increment appended since the previous record of that item_id. A consumer
// reconstructs by concatenation in cursor order."
//
// The failure a latest-wins fold produces is the dangerous kind: the transcript still reads
// as prose, so nothing looks broken -- the message is simply missing its first paragraphs.
func TestInteraction_AgentMessageRecordsConcatenateByItemID(t *testing.T) {
	_, r := s10Router(t)

	driveInteraction(t, r, 1, 10, "m1/s-alpha", agentMessage("itm-1", "Let me ", "in_progress"))
	driveInteraction(t, r, 2, 11, "m1/s-alpha", agentMessage("itm-1", "read the ", "in_progress"))
	driveInteraction(t, r, 3, 12, "m1/s-alpha", agentMessage("itm-1", "file.", "completed"))

	items := r.Items().Session("m1/s-alpha")
	if len(items) != 1 {
		t.Fatalf("transcript holds %d items; want 1 -- three records of ONE item_id fold into one item (IS-ENV-2)", len(items))
	}
	if items[0].Text != "Let me read the file." {
		t.Errorf("reconstructed text = %q; want %q (IS-DELTA-1: increments concatenate in cursor order)",
			items[0].Text, "Let me read the file.")
	}
	if items[0].Status != "completed" {
		t.Errorf("item Status = %q; want completed -- the latest record's status wins", items[0].Status)
	}
	if items[0].Cursor != 10 {
		t.Errorf("item Cursor = %d; want 10 -- a streamed item keeps the ordering position of its FIRST "+
			"record, or every increment jumps it to the end of the transcript", items[0].Cursor)
	}
}

// TestInteraction_NoRecordLandsAfterATerminalStatus is IS-ST-1: "an item_id SHALL reach at
// most one terminal status, and SHALL emit no further record after it." The producer owns the
// rule, and the consumer must not be the place it is broken -- a duplicate or reordered
// record that the seq guard let through must not re-open a finished card.
func TestInteraction_NoRecordLandsAfterATerminalStatus(t *testing.T) {
	_, r := s10Router(t)

	driveInteraction(t, r, 1, 10, "m1/s-alpha", agentMessage("itm-1", "done.", "completed"))
	driveInteraction(t, r, 2, 11, "m1/s-alpha", agentMessage("itm-1", " and more", "in_progress"))

	items := r.Items().Session("m1/s-alpha")
	if len(items) != 1 {
		t.Fatalf("transcript holds %d items; want 1", len(items))
	}
	if items[0].Text != "done." || items[0].Status != "completed" {
		t.Errorf("item after a post-terminal record = {Text:%q Status:%q}; want {done. completed}: "+
			"IS-ST-1 makes the terminal status final", items[0].Text, items[0].Status)
	}
}

// ---------------------------------------------------------------------------
// IS-ENV-3 / IS-COMPAT-1/-2/-4: what a consumer does with an item it cannot use.
// ---------------------------------------------------------------------------

// TestInteraction_UnusableItemsAreSkippedWithoutStalingTheStream covers the three refusals
// that must all behave the same way -- skip the ITEM, keep the FRAME, advance the cursor:
//
//   - IS-ENV-3: an item lacking v, item_id or kind is not a partial item to render;
//   - IS-COMPAT-1: an unknown kind is skipped and "skipping SHALL NOT mark a stream stale --
//     an unknown kind is not a gap".
//
// The staleness half is the one worth the test. Marking the journal stale on an unknown kind
// would make every future kind the machine adds look like transport damage to every older
// phone, which then resyncs, gets the same records back, and stales again.
func TestInteraction_UnusableItemsAreSkippedWithoutStalingTheStream(t *testing.T) {
	_, r := s10Router(t)

	cases := []struct {
		name string
		item string
	}{
		{"no v", `{"item_id":"itm-x","ts":"2026-08-07T10:00:00Z","kind":"agent_message","text":"x"}`},
		{"no item_id", `{"v":1,"ts":"2026-08-07T10:00:00Z","kind":"agent_message","text":"x"}`},
		{"no kind", `{"v":1,"item_id":"itm-x","ts":"2026-08-07T10:00:00Z","text":"x"}`},
		{"unknown kind", `{"v":1,"item_id":"itm-x","ts":"2026-08-07T10:00:00Z","kind":"telepathy","text":"x"}`},
		{"not an object", `"itm-x"`},
	}
	for i, tc := range cases {
		seq := uint64(i + 1)
		rcpt := driveInteraction(t, r, seq, 10+seq, "m1/s-alpha", tc.item)
		if !rcpt.Acked {
			t.Errorf("%s: the frame was not acked. A skipped ITEM is still a consumed FRAME -- the "+
				"consumer advances its cursor over it (IS-ENV-3, IS-COMPAT-1), and never acking it "+
				"pins the drain on the same relay page forever", tc.name)
		}
		if rcpt.Gap {
			t.Errorf("%s: the frame reported a gap; an unusable item is not a transport hole", tc.name)
		}
	}
	if n := r.Items().Len(); n != 0 {
		t.Errorf("transcript holds %d items; want 0 -- none of these may be rendered", n)
	}
	if r.StreamStale(StreamJournal) {
		t.Error("skipping an unusable item staled the JOURNAL channel. IS-COMPAT-1: \"skipping SHALL " +
			"NOT mark a stream stale -- an unknown kind is not a gap\". A phone that stales here " +
			"resyncs, receives the same records, and stales again")
	}
}

// TestInteraction_ANewerItemSchemaIsDegradedNotDropped is IS-COMPAT-4: "IF a consumer sees an
// item v higher than it supports, THEN it SHALL render what it understands and mark the item
// degraded. It SHALL NOT drop the transcript and SHALL NOT error the connection."
func TestInteraction_ANewerItemSchemaIsDegradedNotDropped(t *testing.T) {
	_, r := s10Router(t)

	driveInteraction(t, r, 1, 10, "m1/s-alpha",
		`{"v":99,"item_id":"itm-future","ts":"2026-08-07T10:00:00Z","kind":"agent_message","status":"completed","text":"hi","nobody_knows":{"a":1}}`)

	items := r.Items().Session("m1/s-alpha")
	if len(items) != 1 {
		t.Fatalf("transcript holds %d items; want 1 -- a newer schema version is rendered, not dropped (IS-COMPAT-4)", len(items))
	}
	if !items[0].Degraded {
		t.Errorf("item from v99 is not marked Degraded; IS-COMPAT-4 requires the mark so a screen can " +
			"say the item is only partly understood")
	}
	if items[0].Text != "hi" {
		t.Errorf("item Text = %q; want hi -- what this build DOES understand is still rendered", items[0].Text)
	}
}

// ---------------------------------------------------------------------------
// IS-PLAN-1: latest revision wins, per session.
// ---------------------------------------------------------------------------

// TestInteraction_PlanUpdateKeepsOnlyTheHighestRevision is IS-PLAN-1: "a plan_update is
// latest-state, not incremental. A consumer SHALL keep only the highest revision per session
// and SHALL discard a lower one that arrives late."
func TestInteraction_PlanUpdateKeepsOnlyTheHighestRevision(t *testing.T) {
	_, r := s10Router(t)

	plan := func(id string, revision int) string {
		return fmt.Sprintf(`{"v":1,"item_id":%q,"ts":"2026-08-07T10:00:00Z","kind":"plan_update",`+
			`"revision":%d,"steps":[{"text":"step","state":"pending"}]}`, id, revision)
	}
	driveInteraction(t, r, 1, 10, "m1/s-alpha", plan("plan-1", 1))
	driveInteraction(t, r, 2, 11, "m1/s-alpha", plan("plan-2", 2))
	driveInteraction(t, r, 3, 12, "m1/s-alpha", plan("plan-late", 1))

	var plans []Item
	for _, it := range r.Items().Session("m1/s-alpha") {
		if it.Kind == "plan_update" {
			plans = append(plans, it)
		}
	}
	if len(plans) != 1 {
		t.Fatalf("transcript holds %d plan_update items; want 1 -- a plan is latest-state per SESSION, "+
			"not one card per revision (IS-PLAN-1)", len(plans))
	}
	if plans[0].Revision != 2 {
		t.Errorf("kept plan revision = %d; want 2 (the late revision 1 must be discarded, not applied)", plans[0].Revision)
	}
}

// ---------------------------------------------------------------------------
// IS-LIFE-3: the retention exemption for an unresolved approval_request.
// ---------------------------------------------------------------------------

// TestInteraction_UnresolvedApprovalSurvivesRetentionTrim is the phone-side half of IS-LIFE-3.
// The durable transcript is bounded -- it has to be, or it is the unpruned OpOutcomes residual
// again, one plane over -- and the bound must not evict a card the machine is still blocked
// on. "The daemon SHALL exempt an unresolved approval_request from journal trimming ... until
// its approval_resolved is journalled"; the phone's own retention answers to the same rule,
// because an evicted card is a card the user can no longer answer.
func TestInteraction_UnresolvedApprovalSurvivesRetentionTrim(t *testing.T) {
	_, r := s10Router(t)

	// The approval is the OLDEST item in the session, so ordinary oldest-first trimming would
	// take it first.
	driveInteraction(t, r, 1, 10, "m1/s-alpha", approvalRequest("apr-1"))
	seq, cursor := uint64(2), uint64(11)
	for i := 0; i < MaxItemsPerSession+5; i++ {
		driveInteraction(t, r, seq, cursor, "m1/s-alpha", agentMessage(fmt.Sprintf("itm-%d", i), "x", "completed"))
		seq, cursor = seq+1, cursor+1
	}

	if n := len(r.Items().Session("m1/s-alpha")); n > MaxItemsPerSession+1 {
		t.Fatalf("transcript holds %d items for one session; the bound is %d plus its exemptions", n, MaxItemsPerSession)
	}
	pending := r.Items().PendingApprovals()
	if len(pending) != 1 || pending[0].ItemID != "apr-1" {
		t.Fatalf("PendingApprovals() = %+v; want the unresolved apr-1. IS-LIFE-3 exempts an unresolved "+
			"approval_request from trimming until its approval_resolved lands -- trimming it leaves the "+
			"machine blocked on a card no surface can show", pending)
	}

	// Once RESOLVED the exemption ends: the item is ordinary transcript again and the bound
	// may take it. An exemption that never lifted would be an unbounded transcript with extra
	// steps.
	driveInteraction(t, r, seq, cursor, "m1/s-alpha", approvalResolved("res-1", "apr-1", "allowed"))
	if pending := r.Items().PendingApprovals(); len(pending) != 0 {
		t.Fatalf("PendingApprovals() = %+v after the resolution; want none (IS-LIFE-2: every request "+
			"reaches exactly one resolution, and a stale card dismisses on every surface)", pending)
	}
	seq, cursor = seq+1, cursor+1
	for i := 0; i < MaxItemsPerSession+5; i++ {
		driveInteraction(t, r, seq, cursor, "m1/s-alpha", agentMessage(fmt.Sprintf("post-%d", i), "x", "completed"))
		seq, cursor = seq+1, cursor+1
	}
	for _, it := range r.Items().Session("m1/s-alpha") {
		if it.ItemID == "apr-1" {
			t.Error("a RESOLVED approval_request is still exempt from trimming; the exemption is until " +
				"the resolution lands, not forever (IS-LIFE-3)")
		}
	}
}

// TestInteraction_ReseedMergesTheTranscriptAndRedeliversUnresolvedApprovals is IS-LIFE-3's
// delivery half: unresolved approvals "SHALL be re-delivered in the reseed's EVENTS half, at
// their own cursors" -- never the roster half, whose cursor is deliberately zero (PB-SYNC-8)
// and which therefore cannot be ordered against an approval_resolved.
//
// It also pins the one place the transcript must NOT copy the roster: PB-SYNC-8 makes a reseed
// REPLACE the session set, because a roster is a set and a session absent from it has ended. A
// transcript is a cursor-ordered log, and IS-CAP-4 lets the reseed's events half be CUT at a
// floor to fit one frame -- so replacing the transcript with it would delete history the phone
// legitimately holds every time a repair lands.
func TestInteraction_ReseedMergesTheTranscriptAndRedeliversUnresolvedApprovals(t *testing.T) {
	c, r := s10Router(t)

	driveInteraction(t, r, 1, 10, "m1/s-alpha", agentMessage("itm-old", "held before the repair", "completed"))

	reseed := schema.JournalReseed{
		Roster: []schema.JournalRecord{{SessionID: "m1/s-alpha", Type: "roster", Group: status.GroupWorking}},
		Events: []schema.JournalRecord{{
			Cursor: 30, SessionID: "m1/s-alpha", Type: RecordTypeInteraction,
			Item: json.RawMessage(approvalRequest("apr-repaired")),
		}},
		Cursor: 30,
	}
	plain, err := json.Marshal(reseedFrame{Kind: kindJournalReseed, JournalReseed: reseed})
	if err != nil {
		t.Fatalf("marshal reseed frame: %v", err)
	}
	if _, err := r.AcceptCommit(sealFrameFrom(t, testContentKey(), machineSender, 7, 2, plain), 202); err != nil {
		t.Fatalf("AcceptCommit(reseed): %v", err)
	}

	ids := map[string]bool{}
	for _, it := range r.Items().Session("m1/s-alpha") {
		ids[it.ItemID] = true
	}
	if !ids["apr-repaired"] {
		t.Error("the reseed's events half carried an unresolved approval_request and the transcript " +
			"did not fold it. IS-LIFE-3 makes the events half the re-delivery channel")
	}
	if !ids["itm-old"] {
		t.Error("the reseed REPLACED the transcript. PB-SYNC-8's replace rule is about the ROSTER (a set " +
			"whose absent members have ended); a transcript is a cursor-ordered log whose events half " +
			"IS-CAP-4 may cut at a floor, so replacing it discards history on every repair")
	}
	if len(c.State().Items) != 2 {
		t.Errorf("durable Items after the reseed = %d; want 2 -- the repair commits with its watermark "+
			"(PB-SYNC-3), so what the live store holds and what the blob holds cannot differ", len(c.State().Items))
	}
}

// ---------------------------------------------------------------------------
// Durability and the purge.
// ---------------------------------------------------------------------------

// TestInteraction_TranscriptSurvivesARestart is the reason State carries the items at all. On
// Android the process is SIGKILLed as routine behaviour; the receive high-water is durable, so
// a relay redelivery of the same frames is REFUSED -- meaning a transcript held only in memory
// is gone for good rather than merely re-fetched.
func TestInteraction_TranscriptSurvivesARestart(t *testing.T) {
	st := &memStore{}
	seedPaired(t, st)
	r := resumeRouter(t, st, &recordingAcker{})
	driveInteraction(t, r, 1, 10, "m1/s-alpha", agentMessage("itm-1", "before the kill", "completed"))
	driveInteraction(t, r, 2, 11, "m1/s-alpha", approvalRequest("apr-1"))

	// RESTART: the process died, the state blob did not.
	r2 := resumeRouter(t, st, &recordingAcker{})
	items := r2.Items().Session("m1/s-alpha")
	if len(items) != 2 {
		t.Fatalf("transcript after a restart holds %d items; want 2", len(items))
	}
	if items[0].Text != "before the kill" {
		t.Errorf("restored item text = %q; want %q", items[0].Text, "before the kill")
	}
	if pending := r2.Items().PendingApprovals(); len(pending) != 1 {
		t.Errorf("PendingApprovals() after a restart = %d; want 1. The pending card is exactly what a "+
			"phone that was killed while the machine waited must come back holding", len(pending))
	}
}

// TestInteraction_ThePurgeDestroysTheTranscript. PB-KEY-7's purge names the DECRYPTED CACHES,
// and the transcript is one: it is machine-sealed content the phone decrypted, sitting beside
// the sessions and grids the purge already destroys. Leaving it behind would keep the whole
// conversation readable at rest on a handset whose content tier was destroyed.
func TestInteraction_ThePurgeDestroysTheTranscript(t *testing.T) {
	st := fullStateWithItems()
	got := dropContentMaterial(st)
	if len(got.Items) != 0 {
		t.Errorf("dropContentMaterial left %d transcript item(s). The transcript is a DECRYPTED CACHE "+
			"(PB-KEY-7), the same class as Sessions, Snapshots and OpOutcomes, and it is the most "+
			"revealing of the four", len(got.Items))
	}
}

// fullStateWithItems is a State holding a transcript, for the purge test.
func fullStateWithItems() State {
	st := fullState()
	if len(st.Items) == 0 {
		st.Items = []Item{{SessionID: "m1/s1", ItemID: "itm-1", Kind: "agent_message"}}
	}
	return st
}
