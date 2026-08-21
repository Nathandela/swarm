package remotegw

// FAILING-FIRST (TDD RED, GG-5) for ADR-013 §R7.4's budget SPLIT. Bead: agents-tracker-hggx.8.
//
// THE ARITHMETIC THE REJECTED DRAFT GOT RIGHT ONLY AT N=1. There are TWO 125 ms floors, not
// one:
//
//   - ItemAdmission (daemon-side, this package) releases at most one item record per
//     DefaultAppendWindow = 125 ms, machine-wide, merging the surplus losslessly by text
//     concatenation within one item_id.
//   - CoalescingSink (gateway-side) is a SECOND, INDEPENDENT floor of the same width, and it
//     states in as many words that it "IS THE ONE PLACE THE COMBINED CEILING CAN BE ENFORCED",
//     because an ItemAdmission release arrives there AS A JOURNAL RECORD and is "charged to
//     the same slot as a snapshot" (coalesce.go:36-45).
//   - Journal records are forwarded immediately and may never be coalesced or dropped (R-GW.5,
//     coalesce.go:46-49) -- but they still SPEND the slot. Terminal snapshots are held
//     oldest-first BEHIND it.
//
// So at N=3 streaming Codex sessions the draft's own worst case offers 15 records/s,
// ItemAdmission releases 8/s, and those 8 consume ALL EIGHT of CoalescingSink's slots per
// second, leaving the terminal plane exactly ZERO. "Nothing is dropped" is true of items and
// FALSE of the guarantee DefaultAppendWindow exists to protect (coalesce.go:11-16, PB-GW-7): a
// live peek freezes on a stale grid for as long as the sessions stream.
//
// THE DECISION: split the target budget explicitly instead of letting the two planes race for
// it. ItemAdmission's floor widens to DefaultItemWindow = 250 ms (<= 4 item releases/s
// machine-wide), leaving >= 4 snapshot slots/s for the terminal plane AT EVERY N. Widening is
// SAFE precisely because the merge is lossless -- a wider window merges MORE and loses nothing
// -- so the cost is latency and only latency.

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// TestR7ItemWindow_TheItemFloorIsTwoHundredFiftyMillisecondsAndIsItsOwnConstant pins the knob.
// It is named as the OWNER's: 250 ms splits the budget evenly, and lowering it back toward
// 125 ms restores the draft's token latency and, at N >= 3, restores the frozen peek.
func TestR7ItemWindow_TheItemFloorIsTwoHundredFiftyMillisecondsAndIsItsOwnConstant(t *testing.T) {
	if DefaultItemWindow != 250*time.Millisecond {
		t.Fatalf("DefaultItemWindow = %s, want 250ms (ADR-013 §R7.4)", DefaultItemWindow)
	}
	if DefaultItemWindow == DefaultAppendWindow {
		t.Fatal("DefaultItemWindow is the SAME constant as DefaultAppendWindow. The two floors are " +
			"independent and sit in different processes; collapsing them to one number is exactly " +
			"the reasoning error §R7.4 corrects -- ADR-010 §7 reasoned about ItemAdmission as though " +
			"CoalescingSink were not downstream of it")
	}
}

// TestR7ItemWindow_ItemAdmissionDefaultsToTheItemFloorAndNotTheAppendFloor is the wiring
// fence. NewItemAdmission with a zero Window must take DefaultItemWindow -- the production
// path (skeleton/interaction.go's initInteractionsLocked) passes no Window at all.
func TestR7ItemWindow_ItemAdmissionDefaultsToTheItemFloorAndNotTheAppendFloor(t *testing.T) {
	clk := &r7Clock{t: time.Unix(1_700_000_000, 0)}
	var released int
	q := NewItemAdmission(ItemAdmissionConfig{
		Append: func(string, json.RawMessage) error { released++; return nil },
		Now:    clk.now,
	})

	// Two items for two DIFFERENT ids, offered back to back. The first takes the slot.
	if err := q.Offer("s1", r7Item("a", "one")); err != nil {
		t.Fatalf("offer a: %v", err)
	}
	if err := q.Offer("s1", r7Item("b", "two")); err != nil {
		t.Fatalf("offer b: %v", err)
	}
	if released != 1 {
		t.Fatalf("%d items were released back to back, want 1 (the floor is a SPACING floor)", released)
	}

	// At 200 ms -- past the old 125 ms floor, short of the new 250 ms one -- nothing more may go.
	clk.advance(200 * time.Millisecond)
	if err := q.Offer("s1", r7Item("c", "three")); err != nil {
		t.Fatalf("offer c: %v", err)
	}
	if released != 1 {
		t.Errorf("a second item was released %s after the first; the item floor is %s, and an "+
			"ItemAdmission still keyed to DefaultAppendWindow is the mutation this fences",
			200*time.Millisecond, DefaultItemWindow)
	}

	clk.advance(60 * time.Millisecond)
	if err := q.Offer("s1", r7Item("d", "four")); err != nil {
		t.Fatalf("offer d: %v", err)
	}
	if released < 2 {
		t.Errorf("nothing was released past the %s floor; a floor that never opens is a transcript "+
			"that never arrives", DefaultItemWindow)
	}
}

// TestR7ItemWindow_AtThreeStreamingSessionsTheTerminalPlaneSTILLGetsSlots is §R7.4's named
// mutation fence: "the existing append_budget_test.go gains an N-session case asserting that
// terminal snapshots still reach the sink while item records are being released -- mutate
// DefaultItemWindow back to 125 ms and it must fail."
//
// It is modelled at the layer where the two planes actually meet: CoalescingSink, where an
// item release arrives as a journal record and is charged to the same slot as a snapshot.
func TestR7ItemWindow_AtThreeStreamingSessionsTheTerminalPlaneSTILLGetsSlots(t *testing.T) {
	clk := &r7Clock{t: time.Unix(1_700_000_000, 0)}
	inner := &countingSink{}
	sink := NewCoalescingSink(CoalesceConfig{Inner: inner, Now: clk.now})

	// Three sessions, each an ItemAdmission client sharing ONE machine-wide queue -- which is
	// what the production wiring is (skeleton's `items` is "ONE queue for the whole machine,
	// because IS-DELTA-2a's ceiling is per TARGET across every session and kind").
	items := NewItemAdmission(ItemAdmissionConfig{
		Append: func(session string, item json.RawMessage) error {
			return sink.Event(r7Record(session, item))
		},
		Now: clk.now,
	})

	// One simulated second of three sessions each offering the post-batch rate the R7 pump
	// produces: 5 offered records/s (42 delta frames/s folded at 200 ms).
	//
	// THE RELEASE TICKER IS MODELLED, and it is what makes this fence bind (Wave R7 review
	// MEDIUM 8). Round 1 drove Offer alone, and ItemAdmission releases on an Offer -- so with
	// no ticker the release rate was bounded by the OFFER INSTANTS (5/s), which is under
	// CoalescingSink's 8 slots/s at BOTH window widths, and the assertion could not tell 125 ms
	// from 250 ms. Production has `releaseInteractions`, a DefaultAppendWindow ticker calling
	// Flush (skeleton/interaction.go), which drains the machine-wide queue as fast as the
	// window allows -- so the window, not the offer rate, is what bounds releases. Modelling it
	// is what puts the real 8-releases/s pressure on the shared slot at a 125 ms floor.
	const millisPerTick = 10
	for ms := 0; ms < 1000; ms += millisPerTick {
		for s := 1; s <= 3; s++ {
			session := fmt.Sprintf("s%d", s)
			if ms%200 == 0 {
				if err := items.Offer(session, r7Item(fmt.Sprintf("%s-msg", session), "tok")); err != nil {
					t.Fatalf("offer: %v", err)
				}
			}
			// A live peek on each session at the render debounce rate.
			if ms%16 < millisPerTick {
				if err := sink.Terminal(protocol.TerminalViewV1{Session: session, Lines: []string{"grid"}, Cols: 80, Rows: 24}); err != nil {
					t.Fatalf("terminal: %v", err)
				}
			}
		}
		if ms%int(DefaultAppendWindow/time.Millisecond) < millisPerTick {
			if err := items.Flush(); err != nil {
				t.Fatalf("flush the append floor: %v", err)
			}
		}
		clk.advance(millisPerTick * time.Millisecond)
	}
	if err := sink.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if inner.terminals == 0 {
		t.Fatalf("in one simulated second with THREE streaming sessions, the terminal plane reached "+
			"the sink %d times. At a 125ms item floor, 8 item releases/s consume ALL EIGHT of "+
			"CoalescingSink's slots and a live peek freezes on a stale grid for as long as the "+
			"sessions stream -- which is the guarantee DefaultAppendWindow exists to protect "+
			"(PB-GW-7)", inner.terminals)
	}
	if inner.terminals < 3 {
		t.Errorf("the terminal plane got only %d slots in one second; the split reserves >= 4 "+
			"snapshot slots/s AT EVERY N", inner.terminals)
	}
	if inner.events == 0 {
		t.Error("no item release reached the sink at all; the split must not starve the plane it " +
			"is bounding")
	}
}

// TestR7ItemWindow_WideningTheFloorLosesNOTHING is the safety argument, tested rather than
// asserted: ItemAdmission merges what it holds by text concatenation within one item_id
// (ADR-010 §7), so a wider window merges MORE and loses nothing. The cost is latency and only
// latency.
func TestR7ItemWindow_WideningTheFloorLosesNOTHING(t *testing.T) {
	clk := &r7Clock{t: time.Unix(1_700_000_000, 0)}
	var got []json.RawMessage
	q := NewItemAdmission(ItemAdmissionConfig{
		Append: func(_ string, item json.RawMessage) error {
			got = append(got, append(json.RawMessage(nil), item...))
			return nil
		},
		Now: clk.now,
	})

	const tokens = 20
	for i := 0; i < tokens; i++ {
		if err := q.Offer("s1", r7Item("msg", fmt.Sprintf("t%d ", i))); err != nil {
			t.Fatalf("offer %d: %v", i, err)
		}
		clk.advance(10 * time.Millisecond) // faster than the floor, so most are merged
	}
	// Drive the floor's clock past the window so the last hold releases.
	clk.advance(DefaultItemWindow + 10*time.Millisecond)
	if err := q.Offer("s1", r7Item("msg", "")); err != nil {
		t.Fatalf("final offer: %v", err)
	}

	var joined string
	for _, item := range got {
		var body struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item, &body) == nil {
			joined += body.Text
		}
	}
	for i := 0; i < tokens; i++ {
		want := fmt.Sprintf("t%d ", i)
		if !containsR7(joined, want) {
			t.Fatalf("token %q was LOST at a %s floor; the whole justification for widening is that "+
				"the merge is LOSSLESS, so a dropped token invalidates the decision rather than "+
				"just this test", want, DefaultItemWindow)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type r7Clock struct {
	t time.Time
}

func (c *r7Clock) now() time.Time          { return c.t }
func (c *r7Clock) advance(d time.Duration) { c.t = c.t.Add(d) }

// countingSink counts what actually reached the relay, per plane. It is the observation the
// N-session fence turns on: "nothing is dropped" is true of items and false of the terminal
// guarantee, and only a per-plane count can tell the two apart.
type countingSink struct {
	events    int
	snapshots int
	terminals int
}

func (s *countingSink) Snapshot([]protocol.JournalRecord, uint64) error { s.snapshots++; return nil }
func (s *countingSink) Event(protocol.JournalRecord) error              { s.events++; return nil }
func (s *countingSink) Terminal(protocol.TerminalViewV1) error          { s.terminals++; return nil }

// r7Record wraps one released item in the journal record the gateway forwards.
func r7Record(session string, item json.RawMessage) protocol.JournalRecord {
	return protocol.JournalRecord{
		Type: "interaction", SessionID: session, Item: item,
	}
}

func r7Item(itemID, text string) json.RawMessage {
	body, _ := json.Marshal(map[string]any{
		"v": 1, "item_id": itemID, "kind": itemKindAgentMessage, "status": "in_progress", "text": text,
	})
	return body
}

func containsR7(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
