package tui

// agents-tracker-nx44.7 -- the board's half of the roster badge, after the marker stopped
// being worded from a lease (conversation surface, Wave G item G.2; agents-tracker-tbpm.9).
//
// WHAT THIS FILE USED TO PIN, AND WHAT BECAME OF EACH ASSERTION. Both tests were
// shape-tolerant by design: they required the marker to be PRESENT and to name the phone,
// "not any particular wording -- the exact token is a rendering choice". The wording turned
// out to be the whole finding. `phone` alone, sitting beside "supervisor pending", reads as a
// CONDITION rather than an event, and that is the presence claim plan G.5 rules out; the
// signed copy is `phone sent HH:mm`. A shape-tolerant assertion could not tell the two apart,
// so the replacement is a literal.
//
//	TestRoster_RemoteControlledRowIsMarked      MOVED -> TestRoster_APhoneSentRowIsMarked,
//	                                                     driven by the INSTANT rather than the
//	                                                     lease flag, keeping its own subject:
//	                                                     the marker is scoped to its row and
//	                                                     not to the board.
//	TestRoster_ControlMarkerCoexistsWithLineage MOVED -> g2_phonesentmarker_test.go's
//	                                                     TestG2_TheMarkerCoexistsWithLineageAndSupervision,
//	                                                     which runs the same check against a
//	                                                     marker three times as wide.
//
// The lease itself is not deleted and this file does not claim it is: SessionView.RemoteControlled
// still answers the supervision gate (ADR-010 Amendment 3 C3) and the roster poller's diff key.
// What it no longer does is WORD a row, because a boolean cannot say when -- see
// TestG2_ALeaseAloneDrawsNothing for that residue stated as its own assertion.

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// TestRoster_APhoneSentRowIsMarked keeps this file's own subject: the marker belongs to the
// ROW that earned it, not to the board. A marker that leaked onto a quiet session would tell
// the owner a conversation is happening where none is.
func TestRoster_APhoneSentRowIsMarked(t *testing.T) {
	at := time.Date(2026, 8, 26, 9, 41, 0, 0, time.Local)
	messaged := sWorking("endpoint/held1", "claude", "~/Code/x", "", time.Minute)
	messaged.Name = "held-work"
	messaged.RemoteActivityAt = &at

	quiet := sWorking("endpoint/free2", "claude", "~/Code/x", "", time.Minute)
	quiet.Name = "free-work"

	gm := generalModel{sessions: []protocol.SessionView{messaged, quiet}, width: testCols}

	messagedRow := stripANSI(gm.renderRow(messaged, messaged.Group, false))
	if !strings.Contains(messagedRow, "phone sent 09:41") {
		t.Errorf("a session a phone has messaged must carry the marker and its time; got:\n%q", messagedRow)
	}

	quietRow := stripANSI(gm.renderRow(quiet, quiet.Group, false))
	if strings.Contains(quietRow, "phone") {
		t.Errorf("a session no phone has messaged carries a marker; got:\n%q", quietRow)
	}
}
