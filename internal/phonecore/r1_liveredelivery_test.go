package phonecore

// RE-REVIEW fence for finding R1, driven through a repair channel the R1 suite does not use.
//
// R1's own restart case repairs through a journal_reseed. This one repairs through the LIVE
// event stream instead: the gateway resumes from its own durable PB-GW-8 delivered cursor, so a
// phone that died between applying a frame and that frame being acked receives those records
// again as ordinary Event frames at fresh mailbox seqs -- no reseed involved, and nothing on the
// frame marks it as a repeat. The per-item high water is the only thing that can tell them
// apart (IS-LAYER-3: for successive records of one streamed item, cursor order IS delta order).

import (
	"testing"
)

// TestR1_LiveRedeliveryStraddlingARestartIsNotFolded.
func TestR1_LiveRedeliveryStraddlingARestartIsNotFolded(t *testing.T) {
	st := &memStore{}
	seedPaired(t, st)
	r := resumeRouter(t, st, &recordingAcker{})
	driveInteraction(t, r, 1, 10, "m1/s-alpha", agentMessage("itm-1", "Let me ", "in_progress"))
	driveInteraction(t, r, 2, 11, "m1/s-alpha", agentMessage("itm-1", "read the ", "in_progress"))

	// RESTART, then the gateway resumes and re-sends 10 and 11 as live events at fresh seqs,
	// followed by the one record the phone never saw.
	r2 := resumeRouter(t, st, &recordingAcker{})
	driveInteraction(t, r2, 3, 10, "m1/s-alpha", agentMessage("itm-1", "Let me ", "in_progress"))
	driveInteraction(t, r2, 4, 11, "m1/s-alpha", agentMessage("itm-1", "read the ", "in_progress"))
	driveInteraction(t, r2, 5, 12, "m1/s-alpha", agentMessage("itm-1", "file.", "completed"))

	items := r2.Items().Session("m1/s-alpha")
	if len(items) != 1 {
		t.Fatalf("transcript holds %d items; want 1 (IS-ENV-2 folds by item_id)", len(items))
	}
	if items[0].Text != "Let me read the file." {
		t.Errorf("reconstructed text after a resume that re-sent two folded records = %q; want %q. "+
			"A re-sent LIVE record carries no marker distinguishing it from a delta, so the item's own "+
			"high water is the only guard there is", items[0].Text, "Let me read the file.")
	}
}
