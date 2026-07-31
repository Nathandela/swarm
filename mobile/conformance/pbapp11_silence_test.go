package conformance_test

// FAILING-FIRST (TDD RED, GG-5) for PB-APP-11 -- staleness BY SILENCE.
//
// THE ATTACK, AND WHY NOTHING IN THE TREE SEES IT. Every staleness mechanism this phone has
// keys on a GAP, and a gap is observable only when a LATER seq arrives. So the declared
// adversary (ADR-007 D9) never has to forge, reorder or replay: it stops delivering the
// newest frames and keeps answering the polls, with an empty page. Then no gap forms, so
// nothing is marked stale; the poll SUCCEEDS, so no connection-state machinery fires; and
// Presence() asks THE WITHHOLDING PARTY whether the machine is alive. The phone renders
// arbitrarily old sessions and grids as live, indefinitely, with ConnectionState "online".
//
// WHY THE MACHINE'S STAMP IS THE ONLY HONEST CLOCK HERE, and why this test moves it rather
// than any constant (ADR-007 B113). The phone cannot distinguish "the machine sealed this
// six minutes ago" from "the relay held it for six minutes" -- and it does not have to:
// IssuedAt is AAD-covered, so the relay can only make a frame LOOK OLDER by withholding it,
// never newer. This test therefore mutates the CONNECTION -- what the machine stamped and
// what the relay delivered -- and asserts on what the phone then shows. Six minutes is
// inside PB-TIME-2's ten-minute bounded-age window, so every frame here is ACCEPTED: this is
// emphatically NOT the age-refusal path (ADR-007 B42), which already has its own signal.
//
// This file contains NO implementation.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// pbapp11Streams are the four repair channels PB-SYNC-1 splits the inbound plane into. All
// four are rendered from content that arrived over the SAME withheld link, so silence past
// the budget stales all four -- unlike a gap, which belongs to one bucket.
var pbapp11Streams = []string{"journal", "terminal", "reply", "grant"}

// TestPBAPP11_SilenceIsNotLive drives the whole attack against a real relay and the real
// machine-side sealer: the machine's newest word is six minutes old, and from that moment
// the relay answers every poll successfully with nothing in it.
func TestPBAPP11_SilenceIsNotLive(t *testing.T) {
	h := newHarness(t)

	// The machine's last word, six minutes ago. Sealed by the REAL RelaySink, delivered by
	// the REAL relay, opened by the REAL phone: only the machine's clock moved.
	h.SealOffset(-6 * time.Minute)
	h.PushRoster(schema.JournalRecord{
		SessionID: testMachineID + "/sess-withheld", Type: "roster", Group: "working",
	})
	eventually(t, "the phone never received the roster, so there is nothing being presented "+
		"as live and every assertion below would be vacuous", func() bool {
		list, err := h.App.Roster()
		if err != nil {
			return false
		}
		n, _ := list.Count()
		return n > 0
	})

	// THE PREMISE OF THE ATTACK, ASSERTED RATHER THAN ASSUMED. If the transport had noticed
	// anything, this requirement would be redundant and this test would be proving something
	// else. The polls are still succeeding -- with empty pages -- and the phone reads online.
	state, err := h.App.ConnectionState()
	if err != nil {
		t.Fatalf("ConnectionState: %v", err)
	}
	if state != "online" {
		t.Fatalf("PREMISE LOST: ConnectionState = %q, want \"online\". This test is only "+
			"meaningful while the connection machinery has nothing to say -- that is what makes "+
			"withholding cheaper for the adversary than any forgery", state)
	}

	// THE CLAIM. Nothing rendered from a machine that has been silent past section 6.0's
	// budget may be presented as live.
	for _, stream := range pbapp11Streams {
		got, err := h.App.StreamState(stream)
		if err != nil {
			t.Fatalf("StreamState(%q): %v", stream, err)
		}
		if got != "stale" {
			t.Errorf("the machine's newest authenticated word is 6 minutes old and "+
				"StreamState(%q) = %q, want \"stale\". Section 6.0 has budgeted 5 minutes of "+
				"cached-state freshness since v2 and the staleness decision has no clock input at "+
				"any layer, so a relay that simply stops delivering leaves this phone showing "+
				"old content as current forever (PB-APP-11, ADR-007 B121)", stream, got)
		}
	}

	// The read models carry the same verdict, because a screen that has to remember to ask
	// beside every read is one that will forget once, silently (screen_coverage.tsv's own
	// words about stale_state). These are the three surfaces a user actually looks at.
	list, err := h.App.Roster()
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if stale, err := list.Stale(); err != nil || !stale {
		t.Errorf("SessionList.Stale() = %v (err %v), want true: the triage inbox is the first "+
			"screen the user opens and the one they act on, and every row in it was rendered "+
			"from a machine that has said nothing for 6 minutes", stale, err)
	}
	page, err := h.App.ReadJournal(0, 0)
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	if stale, err := page.Stale(); err != nil || !stale {
		t.Errorf("JournalPage.Stale() = %v (err %v), want true", stale, err)
	}

	// AND THE EXPLICIT STATE, which is the half a stale flag cannot carry: the user has to be
	// told WHEN, or "stale" is indistinguishable from a hole the phone could repair by asking.
	fresh, err := h.App.MachineFreshness()
	if err != nil {
		t.Fatalf("MachineFreshness: %v", err)
	}
	if !fresh.Silent {
		t.Errorf("MachineFreshness().Silent = false after 6 minutes of silence, want true: the "+
			"screen has nothing to render \"not heard from your machine since HH:MM\" from, so the "+
			"phone degrades to a successful empty poll instead (PB-APP-11)")
	}
	if fresh.LastHeardUnixMs == 0 {
		t.Fatalf("MachineFreshness().LastHeardUnixMs = 0 after a frame was accepted; there is no "+
			"timestamp to show the user")
	}
	// THE STAMP IS THE MACHINE'S, NOT THE PHONE'S ARRIVAL INSTANT. An implementation that
	// recorded arrival would read ~0 here and would ALSO never report silence at all under
	// this attack, because a withheld frame arrives exactly when the relay chooses to release
	// it -- the coordinate would be measuring the adversary's schedule.
	if age := time.Since(time.UnixMilli(fresh.LastHeardUnixMs)); age < 5*time.Minute {
		t.Errorf("MachineFreshness().LastHeardUnixMs is %v old; want the machine's own stamp, ~6 "+
			"minutes. A coordinate this fresh is the phone's ARRIVAL time, which is the one clock "+
			"in this exchange the relay controls", age.Round(time.Second))
	}
}

// TestPBAPP11_ALateFrameDoesNotMoveTheCoordinateBackwards is the monotonic clause.
//
// The relay retains frames (section 6.0's 7-day cap) and chooses what to hand over, so it can
// deliver a genuinely old one at any moment. If the newest stamp were simply overwritten, that
// retained frame would drop a healthy phone into the silent state on the adversary's command --
// the same lever, pointed the other way, and a phone that cries wolf is one whose warnings
// stop meaning anything.
func TestPBAPP11_ALateFrameDoesNotMoveTheCoordinateBackwards(t *testing.T) {
	h := newHarness(t)

	h.PushRoster(schema.JournalRecord{
		SessionID: testMachineID + "/sess-now", Type: "roster", Group: "working",
	})
	eventually(t, "the phone never received the fresh roster", func() bool {
		list, err := h.App.Roster()
		if err != nil {
			return false
		}
		n, _ := list.Count()
		return n > 0
	})
	before, err := h.App.MachineFreshness()
	if err != nil {
		t.Fatalf("MachineFreshness: %v", err)
	}
	if before.Silent || before.LastHeardUnixMs == 0 {
		t.Fatalf("the phone is not in the healthy state this test starts from: %+v", before)
	}

	// Now the relay hands over something the machine sealed six minutes ago.
	h.SealOffset(-6 * time.Minute)
	h.PushEvent(schema.JournalRecord{
		Cursor: 9, SessionID: testMachineID + "/sess-now", Type: "event",
	})
	eventually(t, "the late frame never reached the phone", func() bool {
		page, err := h.App.ReadJournal(0, 0)
		if err != nil {
			return false
		}
		n, _ := page.Count()
		return n > 0
	})

	after, err := h.App.MachineFreshness()
	if err != nil {
		t.Fatalf("MachineFreshness after the late frame: %v", err)
	}
	if after.LastHeardUnixMs < before.LastHeardUnixMs {
		t.Errorf("a frame the machine stamped 6 minutes ago moved LastHeardUnixMs BACKWARDS "+
			"(%d -> %d). The relay decides what to deliver and when, so a coordinate that is "+
			"simply overwritten hands it a switch for the phone's warning state",
			before.LastHeardUnixMs, after.LastHeardUnixMs)
	}
	if after.Silent {
		t.Errorf("a late frame put a phone that heard from its machine seconds ago into the "+
			"silent state")
	}
}

// TestPBAPP11_APhoneThatHasNeverHeardIsNotLive is the fail-closed end of the same rule. The
// caches restored from disk are as old as they are; nothing has confirmed otherwise this
// session, and saying "live" would be a guess in the one direction that costs the user
// something.
func TestPBAPP11_APhoneThatHasNeverHeardIsNotLive(t *testing.T) {
	h := newHarness(t)

	fresh, err := h.App.MachineFreshness()
	if err != nil {
		t.Fatalf("MachineFreshness: %v", err)
	}
	if fresh.LastHeardUnixMs != 0 {
		t.Fatalf("a phone that has received nothing reports LastHeardUnixMs = %d",
			fresh.LastHeardUnixMs)
	}
	if !fresh.Silent {
		t.Errorf("MachineFreshness().Silent = false on a phone that has never heard from its "+
			"machine at all, so its restored state would be presented as live (PB-APP-11)")
	}
}

// TestPBAPP11_AMachineInsideTheBudgetIsStillLive is the non-vacuity control, and it is the
// same test with ONE input changed: the machine's stamp. Without it, an implementation that
// reported "stale" unconditionally would pass the test above and destroy the product.
func TestPBAPP11_AMachineInsideTheBudgetIsStillLive(t *testing.T) {
	h := newHarness(t)

	h.SealOffset(-1 * time.Minute) // inside section 6.0's 5 minutes
	h.PushRoster(schema.JournalRecord{
		SessionID: testMachineID + "/sess-fresh", Type: "roster", Group: "working",
	})
	eventually(t, "the phone never received the roster", func() bool {
		list, err := h.App.Roster()
		if err != nil {
			return false
		}
		n, _ := list.Count()
		return n > 0
	})

	for _, stream := range pbapp11Streams {
		got, err := h.App.StreamState(stream)
		if err != nil {
			t.Fatalf("StreamState(%q): %v", stream, err)
		}
		if got != "live" {
			t.Errorf("the machine spoke 1 minute ago and StreamState(%q) = %q, want \"live\". "+
				"A freshness bound that fires inside its own budget is an outage the product "+
				"invented for itself", stream, got)
		}
	}
}

// TestPBAPP11_TheVerdictSurvivesARestart is the durable half.
//
// It is the case that matters MOST, and the one a live-only mirror gets wrong: an Android
// process death is routine, the next launch renders the RESTORED caches, and a freshness
// coordinate held only in memory comes back clear -- so the phone re-presents content it
// already knew was old as live, which is precisely the state PB-APP-8 forbids.
func TestPBAPP11_TheVerdictSurvivesARestart(t *testing.T) {
	h := newHarness(t)

	h.SealOffset(-6 * time.Minute)
	h.PushRoster(schema.JournalRecord{
		SessionID: testMachineID + "/sess-restart", Type: "roster", Group: "working",
	})
	eventually(t, "the phone never received the roster", func() bool {
		list, err := h.App.Roster()
		if err != nil {
			return false
		}
		n, _ := list.Count()
		return n > 0
	})

	before, err := h.App.MachineFreshness()
	if err != nil {
		t.Fatalf("MachineFreshness: %v", err)
	}

	// Process death, then a fresh App over the SAME state directory. The relay is still
	// answering; it just has nothing to hand over.
	if err := h.App.Close(); err != nil {
		t.Fatalf("close the app: %v", err)
	}
	h.App = h.openApp()

	after, err := h.App.MachineFreshness()
	if err != nil {
		t.Fatalf("MachineFreshness after restart: %v", err)
	}
	if after.LastHeardUnixMs != before.LastHeardUnixMs {
		t.Errorf("the machine's stamp did not survive the restart: %d -> %d. A freshness "+
			"coordinate held only in memory comes back clear on every Android process death, "+
			"and process death is routine", before.LastHeardUnixMs, after.LastHeardUnixMs)
	}
	if !after.Silent {
		t.Errorf("MachineFreshness().Silent = false after a restart over caches the phone " +
			"already knew were six minutes old")
	}

	list, err := h.App.Roster()
	if err != nil {
		t.Fatalf("Roster after restart: %v", err)
	}
	if n, _ := list.Count(); n == 0 {
		t.Fatalf("the restart restored no roster at all, so nothing is being presented and this "+
			"test proves nothing")
	}
	for _, stream := range pbapp11Streams {
		got, err := h.App.StreamState(stream)
		if err != nil {
			t.Fatalf("StreamState(%q) after restart: %v", stream, err)
		}
		if got != "stale" {
			t.Errorf("after a restart StreamState(%q) = %q, want \"stale\". The restored caches "+
				"are exactly as old as they were before the process died, and the phone is "+
				"showing them as current", stream, got)
		}
	}
}
