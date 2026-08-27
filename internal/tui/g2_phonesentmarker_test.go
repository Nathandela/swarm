package tui

// FAILING-FIRST (TDD RED, GG-5) for Wave G item G.2:
// docs/specifications/chat-surface-plan.md §9, "`phone control` becomes `phone sent HH:mm`".
// Bead: agents-tracker-tbpm.9. Evidence: docs/verification/chat-surface.md, Wave G.
//
// THE DEFECT THE DESIGN-HONESTY REVIEW FOUND. The row shipped the bare noun `phone`. The
// drawing and the plan both table `phone sent HH:mm`, and three comments in
// internal/skeleton/phonepresence.go asserted the shipped wording in the present tense --
// "the terminal renders it in those words ('phone sent 09:41')" -- describing a string that
// was not in the tree.
//
// WHY THE SHORT FORM IS NOT MERELY TERSER. The marker column already carries
// `supervisor pending` and `supervisor gone`. A bare noun sitting in that company reads as a
// CONDITION -- a phone is on this session -- which is the presence claim plan G.5 rules out
// in as many words: presence needs begin, renew, end, expiry, session binding, transport-loss
// cleanup and multi-device aggregation, and nobody on this wire measures any of it. What the
// daemon observes is a MESSAGE ARRIVING, at an instant. `phone sent 09:41` states that event
// and is self-describing; `phone` states a condition and is not.
//
// The mechanism's own defence -- "the marker's own presence carries the recency claim",
// because it appears on a send and ages out with skeleton.phoneActiveHorizon -- is true of the
// mechanism and invisible to the reader, who sees one word and no horizon.
//
// THE INSTANT IS THE FACT, NOT THE FLAG. The marker is drawn from SessionView.RemoteActivityAt
// and from nothing else. RemoteControlled stays exactly as it was on the wire, because it
// answers a different question for different consumers (the supervision gate of ADR-010
// Amendment 3 C3, and the roster poller's diff key) -- but a row cannot be worded from a
// boolean, and a lease-only row has no instant and therefore no marker. That residue is
// unreachable from any shipped client: every verb behind the lease gates is in
// android/unbound-verbs.tsv with zero Kotlin callers since R6 replaced them with
// composer_send, so no phone has taken a lease since. Inventing copy for a state with no
// producer would be the same unsigned-string mistake this file exists to correct.

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// atLocal builds an instant at a wall-clock time in the machine's OWN zone, so the expected
// literal below is the same on every machine while still being a literal rather than the
// renderer's own formatting handed back to it.
func atLocal(hour, minute int) time.Time {
	return time.Date(2026, 8, 26, hour, minute, 0, 0, time.Local)
}

// rowWithPhoneSend renders one working row whose session last received a phone message at at.
func rowWithPhoneSend(at time.Time) string {
	s := sWorking("endpoint/chatty", "claude", "~/Code/x", "", time.Minute)
	s.Name = "chatty-work"
	if !at.IsZero() {
		s.RemoteActivityAt = &at
	}
	gm := generalModel{sessions: []protocol.SessionView{s}, width: testCols}
	return stripANSI(gm.renderRow(s, s.Group, false))
}

// TestG2_TheRowSaysPhoneSentAndTheTime pins the copy the owner signed, as a literal. It is
// deliberately NOT shape-tolerant: the assertion this replaces accepted any wording that
// contained the word "phone", and the wording is the whole finding.
func TestG2_TheRowSaysPhoneSentAndTheTime(t *testing.T) {
	row := rowWithPhoneSend(atLocal(9, 41))
	const want = "phone sent 09:41"
	if !strings.Contains(row, want) {
		t.Fatalf("the roster row reads %q, and the marker the drawing tables is %q.\n"+
			"A bare noun in the marker column, beside \"supervisor pending\", reads as a CONDITION -- "+
			"a phone is on this session -- and that is the presence claim plan G.5 rules out. The "+
			"daemon observes a message arriving at an instant; the row says so or it says more than "+
			"is known.", row, want)
	}
}

// TestG2_TheRowNeverSaysPhoneWithoutSayingWhen is the finding stated as a fence. Any future
// marker that names the phone must also carry the event and its time, so the short form
// cannot come back by accident or by truncation on a comfortable terminal.
func TestG2_TheRowNeverSaysPhoneWithoutSayingWhen(t *testing.T) {
	row := rowWithPhoneSend(atLocal(9, 41))
	i := strings.Index(row, "phone")
	if i < 0 {
		t.Fatalf("the row names no phone at all; got %q", row)
	}
	if !strings.HasPrefix(row[i:], "phone sent ") {
		t.Fatalf("the row says %q. Wherever this row names a phone it must state what the phone "+
			"DID and when: \"phone sent HH:mm\" is an event, and every shorter form is a claim "+
			"about presence that nothing on this wire measures.", row[i:])
	}
}

// TestG2_ZeroPaddingAndTheLocalClock covers the two ways a time renders wrong: an unpadded
// hour that misaligns the column, and an instant shown in the daemon's zone rather than the
// reader's. The marker is read by a person at their own terminal, so it is their clock.
func TestG2_ZeroPaddingAndTheLocalClock(t *testing.T) {
	if row := rowWithPhoneSend(atLocal(7, 5)); !strings.Contains(row, "phone sent 07:05") {
		t.Errorf("an early-morning send rendered as %q, want \"phone sent 07:05\": the hour and the "+
			"minute are both zero-padded, or the marker column ragged-edges against its neighbours", row)
	}
	if row := rowWithPhoneSend(atLocal(23, 59)); !strings.Contains(row, "phone sent 23:59") {
		t.Errorf("a late send rendered as %q, want \"phone sent 23:59\": the clock is 24-hour, "+
			"because a marker with no am/pm and a 12-hour clock is ambiguous twice a day", row)
	}

	// A UTC instant is rendered in the READER's zone. On a UTC machine the two coincide and
	// this asserts nothing beyond the format, which is why the padding cases above stand on
	// their own.
	utc := time.Date(2026, 8, 26, 9, 41, 0, 0, time.UTC)
	want := "phone sent " + utc.Local().Format("15:04")
	if row := rowWithPhoneSend(utc); !strings.Contains(row, want) {
		t.Errorf("a UTC instant rendered as %q, want %q: the daemon may be in another zone and the "+
			"person reading the row is not", row, want)
	}
}

// TestG2_NoSendNoMarker is the other direction, and it is what keeps the marker meaningful:
// a row that has seen no phone message inside the horizon says nothing about a phone. The
// daemon withholds the instant past the horizon, so absence here IS the ageing-out.
func TestG2_NoSendNoMarker(t *testing.T) {
	if row := rowWithPhoneSend(time.Time{}); strings.Contains(row, "phone") {
		t.Errorf("a session no phone has messaged carries a phone marker; got %q", row)
	}
}

// TestG2_ALeaseAloneDrawsNothing is the assertion MOVED out of remotecontrol_test.go, stated
// as what it now is. RemoteControlled is untouched on the wire and still answers the
// supervision gate and the poller's diff key; what it no longer does is WORD a row, because a
// boolean cannot say when.
func TestG2_ALeaseAloneDrawsNothing(t *testing.T) {
	s := sWorking("endpoint/leased", "claude", "~/Code/x", "", time.Minute)
	s.Name = "leased-work"
	s.RemoteControlled = true

	gm := generalModel{sessions: []protocol.SessionView{s}, width: testCols}
	row := stripANSI(gm.renderRow(s, s.Group, false))
	if strings.Contains(row, "phone") {
		t.Fatalf("a row with a controller lease and no delivered message drew %q.\n"+
			"There is no instant behind a lease, so any marker here would have to invent one or "+
			"drop the time -- and dropping the time is the defect. The state has no producer in "+
			"any shipped client (android/unbound-verbs.tsv: zero Kotlin callers since R6), and the "+
			"drawing tables no copy for it.", row)
	}
}

// TestG2_TheMarkerCoexistsWithLineageAndSupervision is the additive check the short marker
// already had, re-run against a marker three times as wide -- which is where a longer string
// would start displacing its neighbours rather than clamping the summary.
func TestG2_TheMarkerCoexistsWithLineageAndSupervision(t *testing.T) {
	at := atLocal(9, 41)
	child := sWorking("endpoint/child9", "claude", "~/Code/x", "", time.Minute)
	child.Name = "child-work"
	child.SpawnedFrom = "parent1"
	child.SpawnIntent = "handoff"
	child.RemoteActivityAt = &at

	gm := generalModel{sessions: []protocol.SessionView{child}, width: testCols}
	row := stripANSI(gm.renderRow(child, child.Group, false))
	if !strings.Contains(row, "phone sent 09:41") {
		t.Errorf("the marker was dropped from a row that also carries lineage; got:\n%q", row)
	}
	if !strings.Contains(row, "from") || !strings.Contains(row, "parent1") {
		t.Errorf("the marker displaced the lineage badge; got:\n%q", row)
	}
}
