package phonecore

// FAILING-FIRST (TDD RED, GG-5) for review finding R1 against 79a070d: the transcript fold has
// NO replay guard.
//
// interaction.go's fold keys on item_id alone (IS-ENV-2), so every record carrying an item_id it
// already holds is merged into the existing item -- including a record it has already merged.
// For an agent_message that means `next.Text = prev.Text + w.Text` runs a second time on the same
// increment, and for every kind it means the item's FIELDS are re-collapsed to whatever that
// record said. The result is a transcript that reads as prose and is wrong, which is the
// dangerous shape: nothing looks damaged.
//
// A re-delivered record is not an exotic case, it is the DESIGNED behaviour of the repair
// channel. IS-LAYER-4 gives items the journal's own repair, and IS-CAP-4 makes a reseed's events
// half a window the DAEMON sizes to fit one frame -- it is cut at a floor the phone does not
// choose, so overlap with what the phone already folded is the normal case. IS-LAYER-3 is the
// rule the fold is missing: "For successive records of one streamed item, cursor order IS delta
// order" -- so a record whose cursor does not ADVANCE past what the item has already absorbed is
// not a delta, and folding it is not idempotent.
//
// The guard idiom is one file over: SessionCache.applyLocked refuses a record behind its high
// water as "defense in depth behind the transactional cursor" (journal.go). The transcript needs
// the same discipline PER ITEM rather than per stream, because a repair legitimately re-delivers
// records the phone MISSED at cursors below its highest folded one -- a stream-wide high water
// would reject exactly the records the repair exists to deliver.

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

// r1AgentMessage is agentMessage with the turn_id spelled out, so a test can assert that a
// rejected record regressed neither the reconstructed text nor the item's FIELDS.
func r1AgentMessage(id, text, statusValue, turn string) string {
	return fmt.Sprintf(`{"v":1,"item_id":%q,"ts":"2026-08-07T10:00:00Z","turn_id":%q,`+
		`"kind":"agent_message","status":%q,"text":%q}`, id, turn, statusValue, text)
}

// r1ReseedFrame seals a contiguous journal_reseed whose events half carries recs at cursors.
// The roster half is the ordinary one-session roster: PB-SYNC-8 REPLACES the session set from
// it, and it is not read into the transcript at all.
func r1ReseedFrame(t *testing.T, seq uint64, session string, cursors []uint64, items []string) []byte {
	t.Helper()
	events := make([]schema.JournalRecord, 0, len(cursors))
	for i, cursor := range cursors {
		events = append(events, schema.JournalRecord{
			Cursor: cursor, SessionID: session, Type: RecordTypeInteraction,
			Item: json.RawMessage(items[i]),
		})
	}
	reseed := schema.JournalReseed{
		Roster: []schema.JournalRecord{{SessionID: session, Type: "roster", Group: status.GroupWorking}},
		Events: events,
		Cursor: cursors[len(cursors)-1],
	}
	plain, err := json.Marshal(reseedFrame{Kind: kindJournalReseed, JournalReseed: reseed})
	if err != nil {
		t.Fatalf("marshal reseed frame: %v", err)
	}
	return sealFrameFrom(t, testContentKey(), machineSender, 7, seq, plain)
}

// TestR1_ARepairedRecordIsNotFoldedTwice is the finding itself, driven through the channel that
// produces it: a journal repair.
//
// The phone folds two increments of one streamed agent_message and then takes the reseed that
// answers its resync. IS-CAP-4 sizes the events half to fit one frame, so it re-delivers the two
// records the phone already holds together with the one it missed. Without a guard the two
// re-delivered increments are concatenated a second time and the message the user reads is
// "Let me Let me read the read the file." -- prose, and a lie.
func TestR1_ARepairedRecordIsNotFoldedTwice(t *testing.T) {
	c, r := s10Router(t)

	driveInteraction(t, r, 1, 10, "m1/s-alpha", agentMessage("itm-1", "Let me ", "in_progress"))
	driveInteraction(t, r, 2, 11, "m1/s-alpha", agentMessage("itm-1", "read the ", "in_progress"))

	frame := r1ReseedFrame(t, 3, "m1/s-alpha", []uint64{10, 11, 12}, []string{
		agentMessage("itm-1", "Let me ", "in_progress"),
		agentMessage("itm-1", "read the ", "in_progress"),
		agentMessage("itm-1", "file.", "completed"),
	})
	if _, err := r.AcceptCommit(frame, 203); err != nil {
		t.Fatalf("AcceptCommit(reseed): %v", err)
	}

	items := r.Items().Session("m1/s-alpha")
	if len(items) != 1 {
		t.Fatalf("transcript holds %d items; want 1 (IS-ENV-2 folds by item_id)", len(items))
	}
	if items[0].Text != "Let me read the file." {
		t.Errorf("reconstructed text after the repair = %q; want %q. The reseed re-delivered records "+
			"this phone had already folded, and a record whose cursor does not ADVANCE past what the "+
			"item absorbed is not a delta (IS-LAYER-3: cursor order IS delta order)",
			items[0].Text, "Let me read the file.")
	}
	if items[0].Status != "completed" {
		t.Errorf("item Status = %q; want completed -- the ONE new record in the events half still folds",
			items[0].Status)
	}
	if got := c.State().Items; len(got) != 1 || got[0].Text != "Let me read the file." {
		t.Errorf("durable transcript after the repair = %+v; want the same single item the live store "+
			"holds -- the repair commits with its watermark (PB-SYNC-3), so a doubled fold is doubled "+
			"on disk too and no later repair can undo it", got)
	}
}

// TestR1_AReorderedRecordBehindTheItemsHighWaterIsRejected is the second half of the same rule.
// IS-LAYER-3 gives items no private sequence number: the cursor is the only order there is. So a
// record that arrives BEHIND one already folded for its item cannot be appended -- concatenating
// it puts the increment in the wrong place, and its fields overwrite newer ones with older ones.
func TestR1_AReorderedRecordBehindTheItemsHighWaterIsRejected(t *testing.T) {
	_, r := s10Router(t)

	driveInteraction(t, r, 1, 11, "m1/s-alpha", r1AgentMessage("itm-1", "read the ", "in_progress", "t-2"))
	driveInteraction(t, r, 2, 10, "m1/s-alpha", r1AgentMessage("itm-1", "Let me ", "in_progress", "t-1"))

	items := r.Items().Session("m1/s-alpha")
	if len(items) != 1 {
		t.Fatalf("transcript holds %d items; want 1", len(items))
	}
	if items[0].Text != "read the " {
		t.Errorf("reconstructed text = %q; want %q -- a record behind the item's high water is a "+
			"reorder, and appending it renders the increments out of order",
			items[0].Text, "read the ")
	}
	if items[0].TurnID != "t-2" {
		t.Errorf("item TurnID = %q; want t-2 -- the rejected record must not re-collapse the item's "+
			"FIELDS to its own older values either", items[0].TurnID)
	}
}

// TestR1_TheFoldHighWaterSurvivesARestart. The high water has to be DURABLE with the item, for
// the reason Item.Resolved is: Android SIGKILLs the app as routine behaviour, and a guard
// rebuilt in memory comes back zero and re-admits every record the next repair re-delivers.
//
// The restart is not a hypothetical path to that repair, it is the ordinary one. The journal
// read cursor is memory-only (SessionCache.restore seeds no cursor), so the first resync after a
// process death asks from ZERO (mobile/app.go: unsignedResync(Sessions().Cursor())) and the
// reseed that answers it re-delivers the tail of the journal -- which is precisely the records
// that built the restored transcript. Any item still in_progress when the process died is
// doubled. A terminal one is already safe: IS-ST-1's guard refuses it.
func TestR1_TheFoldHighWaterSurvivesARestart(t *testing.T) {
	st := &memStore{}
	seedPaired(t, st)
	r := resumeRouter(t, st, &recordingAcker{})
	driveInteraction(t, r, 1, 10, "m1/s-alpha", agentMessage("itm-1", "Let me ", "in_progress"))
	driveInteraction(t, r, 2, 11, "m1/s-alpha", agentMessage("itm-1", "read the ", "in_progress"))

	// RESTART: the process died mid-message, the state blob did not.
	r2 := resumeRouter(t, st, &recordingAcker{})
	frame := r1ReseedFrame(t, 3, "m1/s-alpha", []uint64{10, 11, 12}, []string{
		agentMessage("itm-1", "Let me ", "in_progress"),
		agentMessage("itm-1", "read the ", "in_progress"),
		agentMessage("itm-1", "file.", "completed"),
	})
	if _, err := r2.AcceptCommit(frame, 203); err != nil {
		t.Fatalf("AcceptCommit(reseed after restart): %v", err)
	}

	items := r2.Items().Session("m1/s-alpha")
	if len(items) != 1 {
		t.Fatalf("transcript holds %d items; want 1", len(items))
	}
	if items[0].Text != "Let me read the file." {
		t.Errorf("reconstructed text after a restart and the repair that follows it = %q; want %q. "+
			"The per-item high water must be durable with the item -- a flag rebuilt in memory comes "+
			"back clear, which is the same argument Item.Resolved is durable for",
			items[0].Text, "Let me read the file.")
	}
}
