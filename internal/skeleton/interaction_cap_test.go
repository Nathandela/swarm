package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the OWNER RULING of 2026-08-07 recorded as ADR-009's
// Amendment 1: interaction-schema.md §5's MaxItemBytes is RAISED so that
//
//  1. §5's own per-field maxima JOINTLY fit inside it (they did not: a plan_update on the table's
//     own numbers serializes to 15 203 B against an 8 KiB item cap), and
//  2. the ONE merge the schema sanctions -- IS-DELTA-2's lossless concatenation of two
//     `agent_message` increments, each already clipped to MaxTextBytes -- can no longer produce
//     an item the append boundary refuses. That refusal is the confirmed defect recorded in
//     a1-carriage.md's re-review of R2: the merged item is DEQUEUED and then DROPPED, so 8 KiB of
//     the agent's message never reaches the transcript with nothing on any surface marked damaged.
//
// The path exercised is the SHIPPED one, end to end: captureInteractions -> shapeItem -> fitItem
// -> ItemAdmission.Offer (where the merge happens) -> daemon.RecordInteractionRaw -> the journal.
// Reading the journal is the point: it is what the gateway forwards, so an item missing here is
// missing from the phone.

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// awaitKind polls the journal for the first item of one kind, and fails with the caller's own
// message rather than a generic count. A DROPPED item is indistinguishable from a slow one at
// this seam, which is exactly why it needs a deadline and not a sleep.
func awaitKind(t *testing.T, sk *Daemon, session, kind string, why string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, it := range interactionItems(t, sk, session) {
			if it["kind"] == kind {
				return it
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s item reached the journal for session %s after 10s. %s", kind, session, why)
	return nil
}

// pinnedClock is a monotone clock the test moves by hand. Every method is mutex-guarded: the
// append floor reads it from the daemon's own release ticker while the test advances it.
type pinnedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *pinnedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *pinnedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// assembleOnPinnedFloorClock stands up the assembly with the ADR-010 §7 append floor on a clock
// the TEST moves, and returns both.
//
// WHY A TEST NEEDS THIS AND NOT A SLEEP. The floor's window is wall-clock and its rule is a
// SPACING FLOOR, not a batching delay (IS-DELTA-2): an item offered a full window after the last
// release is admitted at once. So whether two increments of one item_id fold into one lossless
// append or ship as two separate records is decided by how much REAL time elapsed between two
// Offer calls -- microseconds on an idle machine, and more than a window whenever the scheduler
// takes the producer away in between. Under CI's full-suite parallelism it does exactly that, and
// this test then read the first increment alone and failed on its 4096 vs 8192 bytes
// (docs/verification/r0-flake-rootcause.md records the reproduction and the injection proof).
//
// With the clock pinned, "both increments are inside one window" is a fact of the rig: no time
// passes between the offers no matter how long the machine takes over them, and the release
// happens when the test says so -- through the daemon's OWN release ticker, which is still the
// production driver calling the production Flush.
func assembleOnPinnedFloorClock(t *testing.T) (*Daemon, *pinnedClock) {
	t.Helper()
	clk := &pinnedClock{now: time.Now()}
	sk := assemble(t, func(cfg *Config) { cfg.ItemClock = clk.Now })
	return sk, clk
}

// TestInteractionCap_TwoMaxTextIncrementsMergeAndAreNotDropped is the confirmed defect, in the
// shipped producer's own terms.
//
// The first item consumes the target's one slot, so the two increments that follow are offered
// INSIDE one DefaultAppendWindow and ItemAdmission folds them by concatenation (IS-DELTA-1/-2).
// Each increment is already at §5's MaxTextBytes, so the union is 2 x 4 KiB of text plus the
// envelope -- 8 405 B as the floor re-marshals it, over an 8 KiB MaxItemBytes and under a raised
// one. IS-DELTA-2 calls the merge "lossless text concatenation", so BOTH halves must arrive.
func TestInteractionCap_TwoMaxTextIncrementsMergeAndAreNotDropped(t *testing.T) {
	sk, clock := assembleOnPinnedFloorClock(t)
	const session = "s-cap-merge"
	inc := func(text string) adapter.Interaction {
		return adapter.Interaction{
			Kind: adapter.KindAgentMessage, Status: adapter.StatusInProgress, Ref: "msg-merge", Text: text,
		}
	}
	sk.captureInteractions(session, newCaptureAdapter(
		adapter.Interaction{Kind: adapter.KindUserMessage, Text: "go", Source: adapter.SourceOwner},
		inc(strings.Repeat("a", specMaxTextBytes)),
		inc(strings.Repeat("b", specMaxTextBytes)),
	), adapter.HookPayload{Event: "PostToolUse"})

	// All three were offered at the SAME pinned instant: the user_message took the free slot,
	// and both increments folded behind it. Opening the window is what lets the merged item out,
	// and the daemon's own release ticker is what carries it.
	clock.advance(remotegw.DefaultAppendWindow)

	item := awaitKind(t, sk, session, adapter.KindAgentMessage,
		"IS-DELTA-2 merges two pending increments for one item_id into ONE lossless append; §5's "+
			"MaxItemBytes must admit that union, or the floor dequeues the item and the append "+
			"boundary refuses it -- and the agent's text is gone with nothing marked damaged")

	text, _ := item["text"].(string)
	if len(text) != 2*specMaxTextBytes {
		t.Fatalf("the merged agent_message carries %d bytes of text; want %d -- IS-DELTA-2's merge is "+
			"LOSSLESS text concatenation of two increments each at §5's MaxTextBytes", len(text), 2*specMaxTextBytes)
	}
	if want := strings.Repeat("a", specMaxTextBytes) + strings.Repeat("b", specMaxTextBytes); text != want {
		t.Errorf("the merged text is not the two increments in offer order; IS-DELTA-1 reconstructs by "+
			"concatenation in CURSOR order, so the fold must preserve it (got %q...%q)",
			text[:8], text[len(text)-8:])
	}
}

// TestInteractionCap_APlanUpdateAtTheDocumentedMaximaFitsWhole is the ruling's first half: §5's
// per-field maxima are JOINTLY bounded by MaxItemBytes.
//
// plan_update is the binding case, not approval_request: 64 steps x 200 B with the longest step
// state serializes to 15 203 B, which is the largest single item §5's own table can describe. At
// the documented maxima nothing is over a per-field cap, so a correct producer clips NOTHING --
// no ceiling falls, `truncated` stays absent (§2 sets it only when a field WAS clipped), and every
// step arrives at its full 200 B.
func TestInteractionCap_APlanUpdateAtTheDocumentedMaximaFitsWhole(t *testing.T) {
	sk := assemble(t)
	const session = "s-cap-plan"
	steps := make([]adapter.PlanStep, specMaxSteps)
	for i := range steps {
		steps[i] = adapter.PlanStep{Text: strings.Repeat("t", specMaxStepBytes), State: "in_progress"}
	}
	sk.captureInteractions(session, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindPlanUpdate, Revision: 4096, Steps: steps,
	}), adapter.HookPayload{Event: "PostToolUse"})

	item := awaitKind(t, sk, session, adapter.KindPlanUpdate,
		"a plan_update on §5's own maxima must reach the journal")

	if _, clipped := item["truncated"]; clipped {
		t.Errorf("a plan_update sitting exactly on §5's maxima reports truncated = %v; the maxima are "+
			"JOINTLY bounded by MaxItemBytes, so nothing on them is clipped and §2 sets the flag only "+
			"when a field WAS", item["truncated"])
	}
	got := itemObjects(t, item, "steps")
	if len(got) != specMaxSteps {
		t.Fatalf("journalled steps holds %d entries; §5's MaxSteps is %d and the plan carried exactly that",
			len(got), specMaxSteps)
	}
	for i, s := range got {
		if txt, _ := s["text"].(string); len(txt) != specMaxStepBytes {
			t.Errorf("journalled steps[%d].text is %d bytes; §5 allows a step %d B and the item cap must "+
				"admit all %d of them", i, len(txt), specMaxStepBytes, specMaxSteps)
		}
	}
}

// TestInteractionCap_TheItemCapAdmitsEveryDocumentedFieldMaximum is the arithmetic itself, pinned
// as a fence: the amendment's claim is a RELATION between §5's numbers, so LOWERING the item cap
// under either measured worst case fails here rather than in a transcript.
//
// The other direction -- a later ruling that RAISES a per-field cap without re-deriving the item
// cap -- is caught by the behavioural fence above, not by this one: the numbers below are the
// measurements the amendment recorded and do not recompute, while
// APlanUpdateAtTheDocumentedMaximaFitsWhole builds its plan from specMaxSteps/specMaxStepBytes and
// so grows with §5.
func TestInteractionCap_TheItemCapAdmitsEveryDocumentedFieldMaximum(t *testing.T) {
	// The worst single item §5's table can describe (measured, ADR-009 Amendment 1): a
	// plan_update at 64 steps x 200 B. The union the schema sanctions: two agent_message
	// increments at MaxTextBytes, as the floor re-marshals them.
	const (
		worstSingleItem = 15203
		worstMergeUnion = 8405
	)
	if daemon.MaxItemBytes < worstSingleItem {
		t.Errorf("MaxItemBytes = %d, under the %d-byte plan_update §5's own maxima describe; the field "+
			"maxima must be JOINTLY bounded by the item cap (ADR-009 Amendment 1)", daemon.MaxItemBytes, worstSingleItem)
	}
	if daemon.MaxItemBytes < worstMergeUnion {
		t.Errorf("MaxItemBytes = %d, under the %d-byte union of two MaxTextBytes increments; IS-DELTA-2's "+
			"merge is lossless, so an item cap below it drops the agent's text", daemon.MaxItemBytes, worstMergeUnion)
	}
}
