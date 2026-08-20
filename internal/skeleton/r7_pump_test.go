package skeleton

// FAILING-FIRST (TDD RED, GG-5) for Mirror M4.2's PRODUCER EDGE: the per-session pump that
// turns app-server JSON-RPC frames into adapter.HookPayloads, with the 200 ms delta batching
// M4.2 requires "AT THE ADAPTER FROM DAY ONE". Bead: agents-tracker-hggx.8. ADR-013 §R7.3/§R7.4.
//
// WHY THE BATCHER IS NOT IN THE ADAPTER, AND WHY THAT IS STILL M4.2's SENTENCE.
// InteractionSource.Interactions is required to be PURE and TOTAL and internal/adapter is
// grep-fenced against every fd/socket/exec token, so a connection, a correlation table and a
// 200 ms timer are three kinds of state a stateless strategy object may not hold. Read
// literally, "batched 200 ms at the adapter" is not implementable. It sits at the PRODUCER
// EDGE instead -- in internal/skeleton, between the client's frame channel and
// captureInteractions -- which is where the intent lands: the coalescing happens BEFORE AN ITEM
// EXISTS, which is where the program put it. Not the gateway (too late, the record already
// exists), not the shim (the PTY plane).
//
// THE CONTRACT these tests freeze:
//
//	const backendBatchWindow = 200 * time.Millisecond
//	func (d *Daemon) ingestBackendFrame(sessionID string, frame []byte, receivedAtMs int64)
//	func (d *Daemon) flushBackendFrames(sessionID string)   // session end, and ordering boundaries
//
// THE RULES, all fenceable by mutation:
//  1. Consecutive item/agentMessage/delta frames for one (session, itemId) fold into ONE
//     synthesized frame carrying the CONCATENATION of their `delta` strings and the last
//     frame's other fields -- the same method name, the same shape as the RECORDED delta frame.
//     It is exactly the frame the server would have emitted had it chunked more coarsely.
//  2. 200 ms flush, or earlier ON AN ORDERING BOUNDARY: an open batch is flushed BEFORE any
//     other frame for that session is emitted, so ordering is never disturbed. Session end flushes.
//  3. Approvals BYPASS the batcher entirely, mirroring IS-DELTA-3's head-of-queue rule one
//     layer up.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
)

// r7FixturePath is the R1 gate's fixture directory from internal/skeleton.
const r7FixturePath = "../../docs/verification/r1-codex-fixtures"

// r7DeltaFrame builds a delta frame with the RECORDED shape (frame-samples.json's
// item/agentMessage/delta), varying only itemId and delta.
func r7DeltaFrame(itemID, delta string) []byte {
	return []byte(fmt.Sprintf(
		`{"method":"item/agentMessage/delta","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce",`+
			`"turnId":"01a0033b-d0be-77e1-88e7-584ddeea562d","itemId":%q,"delta":%q},"emittedAtMs":1786760648895}`,
		itemID, delta))
}

// r7RecordedApproval is the RECORDED server-request frame, verbatim.
func r7RecordedApproval(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r7FixturePath, "approval-request.json"))
	if err != nil {
		t.Fatalf("read approval-request.json: %v", err)
	}
	return data
}

// r7RecordingAdapter records every HookPayload the pump hands it, and shapes an
// agent_message increment out of each delta so the daemon's own fold is exercised too.
type r7RecordingAdapter struct {
	adapter.Adapter
	// mu guards seen. The pump has TWO emitters -- the connection's read loop and the fold's
	// own 200 ms timer -- so a recorder read from the test goroutine while the timer writes
	// is a real data race, and an unsynchronized append silently LOSES entries (which is how
	// this file's own losslessness assertion first failed).
	mu   sync.Mutex
	seen []adapter.HookPayload
}

func (a *r7RecordingAdapter) Interactions(p adapter.HookPayload) []adapter.Interaction {
	a.mu.Lock()
	a.seen = append(a.seen, p)
	a.mu.Unlock()
	var fr struct {
		Method string `json:"method"`
		Params struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		} `json:"params"`
	}
	if json.Unmarshal(p.Raw, &fr) != nil {
		return nil
	}
	switch p.Event {
	case "item/agentMessage/delta":
		return []adapter.Interaction{{
			Kind: adapter.KindAgentMessage, Status: adapter.StatusInProgress,
			Ref: fr.Params.ItemID, Text: fr.Params.Delta,
		}}
	case "item/fileChange/requestApproval":
		return []adapter.Interaction{{
			Kind: adapter.KindApprovalRequest, Status: adapter.StatusInProgress,
			Ref: "exec-29bcdedd-84f6-423c-931d-0f0433cc3328", Mode: adapter.ModeCard,
			Summary:   "write ws/hello.txt",
			Decisions: []adapter.DecisionChoice{{ID: "accept", Label: "Yes", Verdict: adapter.VerdictAllow}},
		}}
	}
	return nil
}

func (a *r7RecordingAdapter) Decision(string, string) (adapter.DecisionAction, bool) {
	return adapter.DecisionAction{Reply: json.RawMessage(`{"decision":"accept"}`)}, true
}

// payloads is a snapshot of what the adapter was handed, in order.
func (a *r7RecordingAdapter) payloads() []adapter.HookPayload {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]adapter.HookPayload(nil), a.seen...)
}

// r7Events lists the events the adapter was handed, in order.
func (a *r7RecordingAdapter) events() []string {
	seen := a.payloads()
	out := make([]string, 0, len(seen))
	for _, p := range seen {
		out = append(out, p.Event)
	}
	return out
}

// r7PumpRig assembles a daemon with the recording adapter as its resolver.
func r7PumpRig(t *testing.T) (*Daemon, *r7RecordingAdapter, string) {
	t.Helper()
	sk := assemble(t)
	ad := &r7RecordingAdapter{Adapter: newPlainAdapter().Adapter}
	sk.adapterFor = func(string) (adapter.Adapter, bool) { return ad, true }
	m := launchFake(t, sk, "idle 600s\n")
	return sk, ad, m.ID
}

// ---------------------------------------------------------------------------
// Rule 1 -- the fold
// ---------------------------------------------------------------------------

// TestR7Pump_ConsecutiveDeltasForOneItemFoldIntoONEFrameWithConcatenatedText is the fold rule.
// The synthesized frame keeps the METHOD NAME and the SHAPE of the recorded one, so the
// adapter sees a frame the server could genuinely have emitted; no shape is invented.
func TestR7Pump_ConsecutiveDeltasForOneItemFoldIntoONEFrameWithConcatenatedText(t *testing.T) {
	sk, ad, local := r7PumpRig(t)

	now := time.Now().UnixMilli()
	for i, part := range []string{"Hel", "lo, ", "world"} {
		sk.ingestBackendFrame(local, r7DeltaFrame("msg_abc", part), now+int64(i))
	}
	sk.flushBackendFrames(local)

	seen := ad.payloads()
	if len(seen) != 1 {
		t.Fatalf("the adapter was handed %d payloads (%v), want exactly 1. R1 leg 4 recorded 586 "+
			"delta frames in ONE turn over ~14s -- about 42/s -- and every unfolded frame costs a "+
			"serialize, a cap pass and an append-floor slot", len(seen), ad.events())
	}
	p := seen[0]
	if p.Event != "item/agentMessage/delta" {
		t.Errorf("the synthesized frame's Event = %q, want the METHOD NAME unchanged; the adapter "+
			"dispatches on it", p.Event)
	}
	var fr struct {
		Method string `json:"method"`
		Params struct {
			ItemID  string `json:"itemId"`
			TurnID  string `json:"turnId"`
			Delta   string `json:"delta"`
			ThreadI string `json:"threadId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(p.Raw, &fr); err != nil {
		t.Fatalf("the synthesized frame is not the recorded shape: %v (%s)", err, p.Raw)
	}
	if fr.Params.Delta != "Hello, world" {
		t.Errorf("the folded delta = %q, want the CONCATENATION %q", fr.Params.Delta, "Hello, world")
	}
	if fr.Params.ItemID != "msg_abc" || fr.Params.TurnID == "" || fr.Params.ThreadI == "" {
		t.Errorf("the synthesized frame dropped the last frame's other fields: %+v", fr.Params)
	}
	if p.ReceivedAtMs != now {
		t.Errorf("ReceivedAtMs = %d, want the FIRST folded frame's %d. A batched delta's honest "+
			"capture instant is its EARLIEST content's -- shapeItem's own rule "+
			"(interaction.go:238-241) -- and using the flush instant is the PB-APP-11 clock mistake",
			p.ReceivedAtMs, now)
	}
}

// TestR7Pump_DeltasForDIFFERENTItemsDoNotFoldTogether is the obvious way to get the fold
// wrong: two concurrent messages merged into one bubble.
func TestR7Pump_DeltasForDIFFERENTItemsDoNotFoldTogether(t *testing.T) {
	sk, ad, local := r7PumpRig(t)
	now := time.Now().UnixMilli()

	sk.ingestBackendFrame(local, r7DeltaFrame("msg_a", "alpha"), now)
	sk.ingestBackendFrame(local, r7DeltaFrame("msg_b", "beta"), now+1)
	sk.flushBackendFrames(local)

	if len(ad.payloads()) != 2 {
		t.Fatalf("two deltas for DIFFERENT itemIds produced %d payloads, want 2; folding across "+
			"item ids merges two messages into one", len(ad.payloads()))
	}
}

// ---------------------------------------------------------------------------
// Rule 2 -- the ordering boundary
// ---------------------------------------------------------------------------

// TestR7Pump_AnOpenBatchIsFlushedBEFOREAnyOtherFrameForThatSession is what makes the batcher
// safe. If the pump held prose while emitting a turn/completed, the transcript would show the
// turn closing before the words it closed on.
func TestR7Pump_AnOpenBatchIsFlushedBEFOREAnyOtherFrameForThatSession(t *testing.T) {
	sk, ad, local := r7PumpRig(t)
	now := time.Now().UnixMilli()

	sk.ingestBackendFrame(local, r7DeltaFrame("msg_a", "the answer is "), now)
	sk.ingestBackendFrame(local, []byte(`{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"turn1","items":[],"itemsView":"notLoaded","status":"completed"}}}`), now+5)

	events := ad.events()
	if len(events) < 2 {
		t.Fatalf("the pump emitted %v; the open batch was not flushed by the arriving frame", events)
	}
	if events[0] != "item/agentMessage/delta" || events[1] != "turn/completed" {
		t.Errorf("the pump emitted %v, want the held prose FIRST and then turn/completed; a "+
			"reordering here shows the turn closing before the words it closed on", events)
	}
}

// TestR7Pump_ApprovalsBypassTheBatcherEntirely is rule 3, and it is a latency rule with real
// stakes: an approval sitting behind a 200 ms prose batch is 200 ms of a session blocked
// waiting for a human who has not been told yet.
func TestR7Pump_ApprovalsBypassTheBatcherEntirely(t *testing.T) {
	sk, ad, local := r7PumpRig(t)
	now := time.Now().UnixMilli()

	sk.ingestBackendFrame(local, r7DeltaFrame("msg_a", "let me "), now)
	start := time.Now()
	sk.ingestBackendFrame(local, r7RecordedApproval(t), now+1)
	elapsed := time.Since(start)

	events := ad.events()
	var sawApproval bool
	for _, e := range events {
		if e == "item/fileChange/requestApproval" {
			sawApproval = true
		}
	}
	if !sawApproval {
		t.Fatalf("the approval did not reach the adapter immediately; the pump emitted %v", events)
	}
	if elapsed > backendBatchWindow {
		t.Errorf("the approval took %s to reach the adapter, longer than the %s batch window; "+
			"an approval delayed by a prose batch is a session blocked on a human who has not been "+
			"told yet (IS-DELTA-3's head-of-queue rule, one layer up)", elapsed, backendBatchWindow)
	}
}

// TestR7Pump_TheWindowIsTwoHundredMilliseconds pins the constant M4.2 names, and pins that an
// UNFLUSHED batch still lands: a turn whose last delta is followed by nothing must not sit in
// the pump forever.
func TestR7Pump_TheWindowIsTwoHundredMilliseconds(t *testing.T) {
	if backendBatchWindow != 200*time.Millisecond {
		t.Fatalf("backendBatchWindow = %s, want 200ms (M4.2, \"BATCHED 200 ms AT THE ADAPTER FROM "+
			"DAY ONE -- the 8 appends/s gateway ceiling is real, not theoretical\")", backendBatchWindow)
	}

	sk, ad, local := r7PumpRig(t)
	sk.ingestBackendFrame(local, r7DeltaFrame("msg_tail", "the last word"), time.Now().UnixMilli())

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(ad.payloads()) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a delta with nothing after it NEVER reached the adapter; a batcher with no timer is " +
		"a transcript that stops one message short of every turn")
}

// ---------------------------------------------------------------------------
// The append rate, proved under a fast emitter
// ---------------------------------------------------------------------------

// TestR7Pump_AFastEmitterProducesAtMostFiveOfferedRecordsPerSecondForAPUREDeltaStream is
// M4.2's own arithmetic, measured rather than asserted. R1 leg 4 recorded 586
// item/agentMessage/delta frames in one turn over roughly 14 s -- about 42 frames/s. Batching
// at 200 ms turns that into <= 5 offered records/s for that session. That is the whole reason
// the batcher exists: the gateway's combined machine->phone ceiling is 8 appends/s across
// journal AND terminal (remotegw.DefaultAppendWindow), so an unbatched Codex session on its own
// exceeds it fivefold and starves the terminal plane it shares a target with.
//
// ITS NAME NOW SAYS WHAT IT MEASURES (review round 4, MEDIUM 3). A PURE delta stream is not the
// stream the R1 gate recorded; the headline number for the RECORDED MIX is fenced by
// TestR7Pump_TheRECORDEDFrameMixStaysInsideTheAppendCeiling below, and it is the one M4.2's
// claim rests on.
func TestR7Pump_AFastEmitterProducesAtMostFiveOfferedRecordsPerSecondForAPUREDeltaStream(t *testing.T) {
	sk, ad, local := r7PumpRig(t)

	// 42 frames/s for one second, at the RECORDED rate.
	const frames = 42
	start := time.Now().UnixMilli()
	for i := 0; i < frames; i++ {
		sk.ingestBackendFrame(local, r7DeltaFrame("msg_fast", fmt.Sprintf("%d ", i)), start+int64(i*1000/frames))
		time.Sleep(time.Second / frames)
	}
	sk.flushBackendFrames(local)

	seen := ad.payloads()
	got := len(seen)
	if got > 6 { // 5 windows in a second, +1 for the closing flush
		t.Fatalf("a 42 frames/s emitter produced %d offered records in one second; the 200ms batch "+
			"admits at most 5 (+1 flush). Unbatched, one Codex session offers 42/s into a machine-wide "+
			"budget of 8/s, and PB-GW-7's live peek freezes on a stale grid for as long as it streams", got)
	}
	if got == 0 {
		t.Fatal("a 42 frames/s emitter produced NOTHING")
	}

	// Nothing is lost: every delta's text is present, in order, across the folded frames.
	var joined strings.Builder
	for _, p := range seen {
		var fr struct {
			Params struct {
				Delta string `json:"delta"`
			} `json:"params"`
		}
		if json.Unmarshal(p.Raw, &fr) == nil {
			joined.WriteString(fr.Params.Delta)
		}
	}
	for i := 0; i < frames; i++ {
		if !strings.Contains(joined.String(), fmt.Sprintf("%d ", i)) {
			t.Fatalf("delta %d was LOST by the batcher; the merge is lossless text concatenation "+
				"within one item_id and losing a token is the one outcome batching may not have", i)
		}
	}
}

// The RECORDED mid-stream lifecycle frames, verbatim from frame-samples.json (indices 6, 7 and
// 20). None of them is an item boundary for an agentMessage fold: the codex adapter shapes an
// interaction for item/started, item/completed, item/agentMessage/delta, turn/completed and the
// two */requestApproval methods and for NOTHING ELSE (interaction.go:127-141), so none of these
// three can appear in a transcript at all, let alone ahead of the prose they interleave.
const (
	r7RecordedStatusFrame = `{"method":"thread/status/changed","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","status":{"type":"active","activeFlags":[]}},"emittedAtMs":1786760520125}`
	r7RecordedTokenFrame  = `{"method":"thread/tokenUsage/updated","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","turnId":"01a00339-e135-7440-aec5-25b5a347a7c5","tokenUsage":{"total":{"totalTokens":13049}}},"emittedAtMs":1786760522734}`
	r7RecordedDiffFrame   = `{"method":"turn/diff/updated","params":{"threadId":"01a00335-9a50-79e2-8253-e08861d67c4d","turnId":"01a00335-d9d8-7d72-8682-775ad560a74c","diff":"diff --git a/ws/hello.txt b/ws/hello.txt\n"},"emittedAtMs":1786760261847}`
)

// TestR7Pump_AFrameThatCanShapeNOItemDoesNotFlushTheFold is the per-row fence for
// backendFoldPassthrough, and it is the one a single-row mutation must fail.
//
// THE RULE, per row: a boundary flush exists so a frame cannot be shown ahead of prose it should
// follow. These three shape NO interaction at all -- the codex adapter dispatches on
// item/started, item/completed, item/agentMessage/delta, turn/completed and the two
// */requestApproval methods and returns nil for everything else -- so they can never appear in a
// transcript, and holding the fold across one changes no observable order.
//
// MUTATION FENCE: delete ANY single row from backendFoldPassthrough and that row's subtest fails.
func TestR7Pump_AFrameThatCanShapeNOItemDoesNotFlushTheFold(t *testing.T) {
	for _, tc := range []struct{ name, frame string }{
		{"thread/tokenUsage/updated", r7RecordedTokenFrame},
		{"thread/status/changed", r7RecordedStatusFrame},
		{"turn/diff/updated", r7RecordedDiffFrame},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sk, ad, local := r7PumpRig(t)
			sk.ingestBackendFrame(local, r7DeltaFrame("msg_hold", "hello "), time.Now().UnixMilli())
			sk.ingestBackendFrame(local, []byte(tc.frame), time.Now().UnixMilli())

			seen := ad.payloads()
			lifecycle, folded := 0, 0
			for _, p := range seen {
				switch p.Event {
				case tc.name:
					lifecycle++
				case backendDeltaMethod:
					folded++
				}
			}
			if lifecycle != 1 {
				t.Errorf("the pump delivered %d %s frames of the 1 fed; not flushing a fold must "+
					"not mean swallowing the frame -- M4.5's typed status is driven by these",
					lifecycle, tc.name)
			}
			if folded != 0 {
				t.Fatalf("%s FLUSHED the open agentMessage fold. It shapes no interaction at all, "+
					"so it can never appear in a transcript and cannot reorder one; flushing on it "+
					"multiplies the offered-record rate by the recorded lifecycle-frame ratio and "+
					"puts one streaming session above the machine's whole 8 appends/s budget", tc.name)
			}
			// ...and the held prose is not lost: the window still releases it.
			sk.flushBackendFrames(local)
			for _, p := range ad.payloads() {
				if p.Event == backendDeltaMethod {
					return
				}
			}
			t.Fatal("the held fold never arrived at all; holding is not dropping")
		})
	}
}

// TestR7Pump_AnItemBoundaryStillFlushesTheFold is the negative control that stops the fence
// above from being satisfiable by a pump that never flushes on anything.
func TestR7Pump_AnItemBoundaryStillFlushesTheFold(t *testing.T) {
	sk, ad, local := r7PumpRig(t)
	sk.ingestBackendFrame(local, r7DeltaFrame("msg_hold", "hello "), time.Now().UnixMilli())
	sk.ingestBackendFrame(local, []byte(r7RecordedTurnCompletedFrame), time.Now().UnixMilli())

	order := []string{}
	for _, p := range ad.payloads() {
		order = append(order, p.Event)
	}
	if len(order) < 2 || order[0] != backendDeltaMethod {
		t.Fatalf("the pump emitted %v; a turn/completed CAN appear in a transcript, so the held "+
			"prose must go out FIRST or the turn closes before the words it closed on", order)
	}
}

// TestR7Pump_TheRECORDEDFrameMixStaysInsideTheAppendCeiling is review round 4's MEDIUM 3, and
// it is the number M4.2's claim actually rests on.
//
// THE CLAIM WAS MEASURED AGAINST A STREAM THAT NEVER OCCURS. The fence above feeds 42 PURE
// delta frames, and ingestBackendFrame treated EVERY non-delta frame as an ordering boundary
// that flushes the fold. The R1 census (r1-codex-gate.md:101-104) recorded 97 frames in one
// turn: 49 item/agentMessage/delta alongside 3 thread/tokenUsage/updated, 3
// thread/status/changed and 4 turn/diff/updated -- about one lifecycle frame per five deltas.
// PROBED at that recorded ratio against round 3: 9 offered records in one second, ABOVE the
// 8 appends/s ceiling the batcher exists to respect, from ONE session.
//
// THE RULE THIS PINS: a frame that CANNOT REORDER the folded item must not flush it. A token
// count and a status flip are not item boundaries for an agent_message fold; an item/started,
// an item/completed, a turn/completed and a delta for a DIFFERENT item all still are, and those
// are fenced by the ordering tests above.
//
// MUTATION FENCE: remove any method from backendFoldPassthrough and this test fails on the RATE
// it measures -- offered records per elapsed second, not a fixed count, so the verdict is the
// implementation's and not the machine's.
func TestR7Pump_TheRECORDEDFrameMixStaysInsideTheAppendCeiling(t *testing.T) {
	sk, ad, local := r7PumpRig(t)

	// The RECORDED census for one turn, at the RECORDED ratio: 49 deltas for one item with the
	// 10 mid-stream lifecycle frames interleaved one per five.
	lifecycle := []string{
		r7RecordedTokenFrame, r7RecordedStatusFrame, r7RecordedDiffFrame,
		r7RecordedTokenFrame, r7RecordedStatusFrame, r7RecordedDiffFrame,
		r7RecordedTokenFrame, r7RecordedStatusFrame, r7RecordedDiffFrame,
		r7RecordedDiffFrame,
	}
	const deltas = 49
	next := 0
	start := time.Now()
	for i := 0; i < deltas; i++ {
		sk.ingestBackendFrame(local, r7DeltaFrame("msg_mix", fmt.Sprintf("%d ", i)), time.Now().UnixMilli())
		if i%5 == 4 && next < len(lifecycle) {
			sk.ingestBackendFrame(local, []byte(lifecycle[next]), time.Now().UnixMilli())
			next++
		}
		time.Sleep(time.Second / deltas)
	}
	sk.flushBackendFrames(local)
	elapsed := time.Since(start)

	// Only the frames that SHAPE AN ITEM cost an append; the lifecycle frames shape none, which
	// is exactly why they must not be allowed to flush the fold either.
	offered := 0
	for _, p := range ad.payloads() {
		if p.Event == backendDeltaMethod {
			offered++
		}
	}
	// THE BOUND IS A RATE, MEASURED AGAINST THE ELAPSED TIME THIS TEST ALREADY TAKES.
	// Thresholding a FIXED count would make the verdict a property of the machine: the loop is
	// ~1.09 s unloaded, and a CI runner that stretches it to 1.5 s would fail a CORRECT
	// implementation for being slow. The 200 ms fold admits at most 5 offered records per second
	// however long the interval runs; the +1 is the closing flush, which happens once.
	rate := float64(offered) / elapsed.Seconds()
	if limit := 5*elapsed.Seconds() + 1; float64(offered) > limit {
		t.Fatalf("the RECORDED frame mix produced %d folded agentMessage records in %s -- %.1f/s, "+
			"against a bound of %.1f records (5/s, +1 for the closing flush). Every mid-stream "+
			"tokenUsage, status and diff frame is flushing a fold it cannot possibly reorder, so "+
			"one streaming session offers %.1f appends/s into a machine-wide budget of 8 "+
			"(remotegw.DefaultAppendWindow) and PB-GW-7's live peek freezes on a stale grid for "+
			"as long as it streams", offered, elapsed, rate, limit, rate)
	}
	if offered == 0 {
		t.Fatal("the recorded frame mix produced NOTHING")
	}

	// The lifecycle frames still REACH the adapter and the engine: not flushing the fold must
	// not mean swallowing the frame. M4.5's typed status is driven by exactly these.
	seenStatus := 0
	for _, p := range ad.payloads() {
		if p.Event == "thread/status/changed" {
			seenStatus++
		}
	}
	if seenStatus != 3 {
		t.Errorf("the pump delivered %d thread/status/changed frames of the 3 fed; a frame that "+
			"does not flush the fold must still be emitted, or M4.5's typed status goes dark", seenStatus)
	}

	// Nothing is lost.
	var joined strings.Builder
	for _, p := range ad.payloads() {
		var fr struct {
			Params struct {
				Delta string `json:"delta"`
			} `json:"params"`
		}
		if p.Event == backendDeltaMethod && json.Unmarshal(p.Raw, &fr) == nil {
			joined.WriteString(fr.Params.Delta)
		}
	}
	for i := 0; i < deltas; i++ {
		if !strings.Contains(joined.String(), fmt.Sprintf("%d ", i)) {
			t.Fatalf("delta %d was LOST when the mix was folded; losing a token is the one outcome "+
				"batching may not have", i)
		}
	}
}

// TestR7Pump_TheWholeFrameReachesTheAdapterVerbatim is ADR-013 §R7.3's table: Raw is "the whole
// frame, verbatim as recorded", which is what makes r1-codex-fixtures/frame-samples.json
// literally the golden vector set (ADR-010's own stated benefit of reusing HookPayload).
func TestR7Pump_TheWholeFrameReachesTheAdapterVerbatim(t *testing.T) {
	sk, ad, local := r7PumpRig(t)
	approval := r7RecordedApproval(t)

	sk.ingestBackendFrame(local, approval, time.Now().UnixMilli())
	seen := ad.payloads()
	if len(seen) == 0 {
		t.Fatal("the approval never reached the adapter")
	}
	p := seen[len(seen)-1]
	var got, want map[string]any
	if json.Unmarshal(p.Raw, &got) != nil || json.Unmarshal(approval, &want) != nil {
		t.Fatalf("could not compare the frames: %s", p.Raw)
	}
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Errorf("the adapter was handed a REWRITTEN frame.\n got: %s\nwant: %s\nA pump that "+
			"reshapes frames makes the recorded corpus stop being a golden vector set", a, b)
	}
	if p.Event != "item/fileChange/requestApproval" {
		t.Errorf("Event = %q, want the JSON-RPC METHOD -- the same slot cb.Event fills for a hook", p.Event)
	}
}

// TestR7Pump_ItemsReachTheJournalThroughTheREALCaptureSeam is hard rule 3 applied here: the
// pump must feed the SAME function the hook path calls
// (Daemon.captureInteractions -> shapeItem -> ItemAdmission.Offer -> RecordInteractionRaw), not
// a parallel path that happens to produce similar records. Assert on the JOURNAL, which is
// what the gateway forwards -- anything invisible there is invisible to the phone.
func TestR7Pump_ItemsReachTheJournalThroughTheREALCaptureSeam(t *testing.T) {
	sk, _, local := r7PumpRig(t)

	now := time.Now().UnixMilli()
	sk.ingestBackendFrame(local, r7DeltaFrame("msg_journal", "hello from codex"), now)
	sk.flushBackendFrames(local)

	items := awaitItems(t, sk, local, 1)
	last := items[len(items)-1]
	if last["kind"] != adapter.KindAgentMessage {
		t.Fatalf("the journalled item is %v, want an agent_message", last)
	}
	if last["text"] != "hello from codex" {
		t.Errorf("journalled text = %v, want the delta's own text", last["text"])
	}
	if id, _ := last["item_id"].(string); id == "" {
		t.Error("the journalled item carries no item_id; the ULID is DAEMON-minted (ADR-010 §3) and " +
			"its absence means the pump bypassed shapeItem entirely")
	}
	if _, hasRef := last["ref"]; hasRef {
		t.Error("the journalled item carries a `ref`; the CLI's own id is consumed by the daemon and " +
			"NEVER reaches the wire (IS-APR-1 leaves exactly one id on the wire)")
	}
}
