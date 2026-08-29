package skeleton

// R9 (bd agents-tracker-hggx.10) -- playbook 11.1 "Structured-spool daemon-kill tests at
// every Claude hook stage": ONE table-driven matrix over EVERY hook stage the Claude
// adapter maps x two daemon kill points, asserting exactly-once delivery per stage after
// the daemon restarts. The R6 tests (r6_hookdrain_test.go, internal/shim/
// r6_hookspool_test.go) proved the drain machinery piecewise on single stages; this file
// closes the release-gate gap that no test enumerated ALL stages against BOTH kill
// points.
//
// THE STAGE LIST IS DERIVED FROM THE PRODUCTION TABLE, NOT HAND-COPIED: claudeHookStages
// reads internal/adapter/claude's hookEvents rows through the adapter's public
// SignalSources() (exactly one Kind=="hook" source per table row), so a stage added to
// the adapter tomorrow joins this matrix automatically -- coverage cannot silently trail
// the table.
//
// The two kill points bracket the drain boundary:
//
//   - killed-before-drain: the daemon dies AFTER the shim durably acked the stage's
//     spool append but BEFORE any drain reached it. The restarted daemon must deliver
//     the item from spool replay -- never zero (playbook 6.1's "daemon unavailability
//     neither fails a provider hook nor loses an accepted item").
//
//   - killed-after-drain-before-ack: the daemon dies AFTER draining, journalling and
//     cursor-persisting the item but BEFORE the fold that acknowledges it to the shim
//     (folding rides the NEXT drain's FoldSeq, so the records provably still sit in the
//     shim's spool -- the peek below asserts that premise rather than assuming it). The
//     restarted daemon must not apply them a second time -- never twice.
//
// Rig and kill helper are r6_hookdrain_test.go's own (startHookShim / hookShim.kill /
// assembleAt / registerTestSession / awaitJournalTexts); the daemon
// "kill" is Close + re-assemble over the same state dir, exactly the restart those tests
// model. All stages share one daemon restart per kill point (runtime stays seconds, not
// minutes) while every assertion stays per-stage.

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/shim"
)

// claudeHookStages derives every hook stage the Claude adapter maps from the PRODUCTION
// hookEvents table, via the adapter's public SignalSources() -- one Kind=="hook" source
// per table row. A hand-copied list here would let a newly added stage ship without
// matrix coverage; a derived one cannot.
func claudeHookStages(t *testing.T) []string {
	t.Helper()
	var stages []string
	for _, src := range claude.New().SignalSources() {
		if src.Kind != "hook" {
			continue
		}
		event := src.Descriptor["event"]
		if event == "" {
			t.Fatalf("claude adapter declared a hook SignalSource with no event name: %v", src.Descriptor)
		}
		stages = append(stages, event)
	}
	// Floor on the DERIVATION, not a freeze of the table: if SignalSources() ever stops
	// carrying the hook rows (a refactor of the descriptor shape), this must fail loudly
	// rather than run an empty matrix and report green. The four names are playbook
	// 11.1's own.
	for _, must := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		found := false
		for _, s := range stages {
			if s == must {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("derived stage list %v is missing %q -- the SignalSources derivation no longer reflects the hookEvents table", stages, must)
		}
	}
	return stages
}

// postStageEvent is postTurnEvent (r6_hookdrain_test.go) parameterized on the event
// name: one authenticated, sequence-distinct hook post for the given stage, durably
// acked by the shim's spool. The explicit turn payload gives EVERY stage a named status
// dimension uniformly (deriveDims honors pre-normalized dims verbatim), which is what
// engages the engine's cb.Sequence replay guard identically across the matrix.
//
// label rides the callback's Raw body (ADR-010 section 6 carriage, verbatim through the
// spool and the drain) so the DELIVERY side can attribute every journal item to the
// exact (kill point, stage) post that produced it. Labeling per RECORD rather than per
// capture-order matters: a mutation that shapes one record twice shifts an order-based
// script's labels and slips through, while a record-carried label re-shapes the SAME
// text and trips the per-stage exactly-once count.
func postStageEvent(t *testing.T, hookSocketPath, sessionID, token, event, label string, seq uint64) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"swarm_test_label": label})
	if err != nil {
		t.Fatalf("encode label body: %v", err)
	}
	cb := engine.Callback{
		SessionID: sessionID, Token: token, Sequence: seq, Event: event,
		Payload: map[string]string{"turn": "active"},
		Raw:     raw,
	}
	acked, err := hookclient.PostToShim(hookSocketPath, cb)
	if err != nil {
		t.Fatalf("post %s hook event seq=%d: %v", event, seq, err)
	}
	if !acked {
		t.Fatalf("%s hook event seq=%d was not acked by the shim's hook socket", event, seq)
	}
}

// echoCaptureAdapter shapes, for every captured event, exactly one agent_message whose
// text is the label postStageEvent embedded in the callback's Raw body. Unlike
// scriptedCapture's order-consuming script, the mapping from record to label is carried
// BY the record: a redelivered record re-shapes the SAME label (a fresh incarnation
// mints a fresh item_id for it, so the duplicate reaches the journal and is counted),
// and delivery attribution never depends on capture order.
type echoCaptureAdapter struct{ adapter.Adapter }

func (e echoCaptureAdapter) Interactions(p adapter.HookPayload) []adapter.Interaction {
	var body struct {
		Label string `json:"swarm_test_label"`
	}
	if err := json.Unmarshal(p.Raw, &body); err != nil || body.Label == "" {
		return nil
	}
	return []adapter.Interaction{{
		Kind: adapter.KindAgentMessage, Status: adapter.StatusCompleted,
		Ref: body.Label, Text: body.Label, StopReason: "end_turn",
	}}
}

func (echoCaptureAdapter) Decision(string, string) (adapter.DecisionAction, bool) {
	return adapter.DecisionAction{}, false
}

// echoCapture wires sk's adapter resolution to echoCaptureAdapter for every session.
func echoCapture(sk *Daemon) {
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) {
		return echoCaptureAdapter{Adapter: newPlainAdapter()}, true
	})
}

// peekSpoolRecords issues a read-only DRAIN (FoldSeq=0: fold nothing) against the shim's
// hook socket and returns the records with Seq>fromSeq still held in the spool. It is
// how the killed-after-drain-before-ack leg PROVES its premise: the drained records are
// still re-offerable, so only the daemon's persisted fold cursor stands between them and
// a duplicate delivery.
func peekSpoolRecords(t *testing.T, hookSocketPath string, fromSeq uint64) []shim.HookRecord {
	t.Helper()
	conn, err := net.DialTimeout("unix", hookSocketPath, 3*time.Second)
	if err != nil {
		t.Fatalf("dial hook socket to peek spool: %v", err)
	}
	defer func() { _ = conn.Close() }()
	req, err := json.Marshal(shim.HookDrainRequest{FromSeq: fromSeq, FoldSeq: 0})
	if err != nil {
		t.Fatalf("encode peek request: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(append([]byte{shim.HookDrainTag}, req...)); err != nil {
		t.Fatalf("write peek request: %v", err)
	}
	var resp shim.HookDrainResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode peek response: %v", err)
	}
	return resp.Records
}

func TestHookStageKillMatrix_EveryStageExactlyOnceAcrossBothKillPoints(t *testing.T) {
	stages := claudeHookStages(t)
	n := len(stages)

	dir, err := os.MkdirTemp("/tmp", "swskhk")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sessionDir, err := os.MkdirTemp("/tmp", "swskhks")
	if err != nil {
		t.Fatalf("session dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sessionDir) })
	cursorPath := filepath.Join(sessionDir, "hook.fold")

	const sessionID, token = "s-r9-kill", "tok-r9-kill"
	h := startHookShim(t, sessionDir, sessionID)
	t.Cleanup(func() { h.kill(t) })

	// -----------------------------------------------------------------------
	// Kill point 1: killed-before-drain. Every stage's event is durably acked by
	// the shim while the FIRST daemon incarnation lives, then that incarnation is
	// killed WITHOUT ever draining.
	// -----------------------------------------------------------------------
	sk1 := assembleAt(t, dir)
	registerTestSession(sk1, sessionID, token)
	for i, stage := range stages {
		postStageEvent(t, h.cfg.HookSocketPath, sessionID, token, stage, "predrain:"+stage, uint64(i+1))
	}
	if err := sk1.Close(); err != nil {
		t.Fatalf("kill (close) the never-drained daemon incarnation: %v", err)
	}

	// The restarted daemon drains the backlog: exactly one delivery per stage.
	sk2 := assembleAt(t, dir)
	registerTestSession(sk2, sessionID, token)
	echoCapture(sk2)

	hd2 := NewHookDrainer(sk2, sessionID, h.cfg.HookSocketPath, cursorPath)
	applied1, skipped1, err := hd2.DrainOnce()
	if err != nil {
		t.Fatalf("drain after killed-before-drain restart: %v", err)
	}
	if applied1 != n || skipped1 != 0 {
		t.Fatalf("killed-before-drain restart drain applied=%d skipped=%d, want applied=%d skipped=0 (one per stage)", applied1, skipped1, n)
	}
	got1 := awaitJournalTexts(t, sk2, sessionID, n)
	if len(got1) != n {
		t.Fatalf("journal holds %d item(s) after the killed-before-drain restart, want exactly %d (one per stage)", len(got1), n)
	}
	for i, stage := range stages {
		if want := "predrain:" + stage; got1[i] != want {
			t.Errorf("killed-before-drain item %d = %q, want %q (spool order per stage)", i, got1[i], want)
		}
	}

	// -----------------------------------------------------------------------
	// Kill point 2: killed-after-drain-before-ack. Every stage posts again; the
	// SAME incarnation drains and journals them (cursor persisted per record),
	// then dies before any fold ever acknowledges them to the shim.
	// -----------------------------------------------------------------------
	for i, stage := range stages {
		postStageEvent(t, h.cfg.HookSocketPath, sessionID, token, stage, "preack:"+stage, uint64(n+i+1))
	}
	applied2, skipped2, err := hd2.DrainOnce()
	if err != nil {
		t.Fatalf("second drain (pre-ack batch): %v", err)
	}
	if applied2 != n || skipped2 != 0 {
		t.Fatalf("pre-ack batch drain applied=%d skipped=%d, want applied=%d skipped=0", applied2, skipped2, n)
	}
	// Wait for the append floor to durably journal both batches BEFORE the kill, so
	// the kill point is genuinely "after drain, before ack".
	awaitJournalTexts(t, sk2, sessionID, 2*n)
	// Prove the "before ack" premise: the drained batch is still re-offerable from
	// the shim's spool (this drain's FoldSeq folded only the PRIOR batch).
	if held := peekSpoolRecords(t, h.cfg.HookSocketPath, uint64(n)); len(held) != n {
		t.Fatalf("shim spool re-offers %d record(s) past seq %d, want %d -- the pre-ack kill point needs the drained records still unfolded", len(held), n, n)
	}
	if err := sk2.Close(); err != nil {
		t.Fatalf("kill (close) the drained-but-unacked daemon incarnation: %v", err)
	}

	// The restarted daemon must NOT deliver any stage a second time. The echo adapter
	// keeps the trap armed: a record re-applied here re-shapes its own label under a
	// FRESH item_id (this incarnation's item-id map has never seen the ref), so a
	// duplicate lands in the journal and the per-stage count below catches it.
	sk3 := assembleAt(t, dir)
	t.Cleanup(func() { _ = sk3.Close() })
	registerTestSession(sk3, sessionID, token)
	echoCapture(sk3)
	hd3 := NewHookDrainer(sk3, sessionID, h.cfg.HookSocketPath, cursorPath)
	applied3, skipped3, err := hd3.DrainOnce()
	if err != nil {
		t.Fatalf("drain after killed-after-drain-before-ack restart: %v", err)
	}
	if applied3 != 0 || skipped3 != 0 {
		t.Fatalf("post-kill drain applied=%d skipped=%d, want 0/0 -- the persisted fold cursor must already cover the drained-but-unacked batch", applied3, skipped3)
	}

	gotAll := awaitJournalTexts(t, sk3, sessionID, 2*n)
	if len(gotAll) != 2*n {
		t.Fatalf("journal holds %d item(s) across the whole matrix, want exactly %d (each stage once per kill point)", len(gotAll), 2*n)
	}
	counts := make(map[string]int, 2*n)
	for _, text := range gotAll {
		counts[text]++
	}
	for _, kp := range []struct{ name, prefix string }{
		{"killed-before-drain", "predrain:"},
		{"killed-after-drain-before-ack", "preack:"},
	} {
		for _, stage := range stages {
			kp, stage := kp, stage
			t.Run(kp.name+"/"+stage, func(t *testing.T) {
				if got := counts[kp.prefix+stage]; got != 1 {
					t.Errorf("stage %s at kill point %s delivered %d time(s), want exactly 1 (never zero, never twice)", stage, kp.name, got)
				}
			})
		}
	}
}
