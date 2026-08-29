package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the FACADE-TO-SCREEN half of slice I1: the Android
// conversation is rendered from bytes THE REAL SHIPPED STACK DELIVERED, not from JSON an app
// author invented.
//
// WHY THIS FILE EXISTS BESIDE THE OTHER TWO E2Es. interaction_chain_e2e_test.go proves the real
// producer's items reach the phone; approve_roundtrip_e2e_test.go proves a tap on
// swarmmobile.App.Approve answers one over the whole stack. Both stop at the facade. Everything
// above it -- InteractionItem's decode, TranscriptScreen's copy, transcriptView's composition,
// ApprovalItem, ApprovalSheetScreen -- was asserted in Robolectric against fixtures hand-written
// in Kotlin, and NOTHING JOINED THE TWO SETS OF BYTES. That is the defect class this repository
// has paid for most often and names in as many words elsewhere: "a beautifully-tested model that
// nothing constructs from swarmmobile.App, with the real screen reading the facade directly and
// disagreeing with every assertion" (FacadeBridge.kt), and "EXPECTED_DARK_COLORS ... literals
// transcribed from the implementation, compared against the implementation"
// (android/app/build.gradle.kts).
//
// So this test RECORDS the crossing. It drives the recorded Claude Code corpus through the real
// adapter, the real producer, a separate gateway process, the real relay and the real phone core,
// reads it back through the real bound facade, taps the card with the decision id the CARD ITSELF
// offered, and writes both sides of that resolution to a checked-in golden. The Android suite
// (TranscriptScreenGoldenTest) renders THAT FILE. Neither half can drift without the other going
// red: a change in what the producer emits changes the golden here, and a screen that stops
// rendering it fails there.
//
// WHAT IS NORMALIZED AND WHY THAT WEAKENS NOTHING. Six values differ on every run -- the session
// id, the item and turn ULIDs, the ordering cursors, the capture instants, and the approval's
// ADR-007 D7 binding tuple (content_hash, expires_at, agent_instance). The Android decode reads
// NONE of them: `InteractionItem.fields()` and `ApprovalItem.of` between them read `tool`,
// `action`, `output_excerpt`, `truncation_marker`, `change`, `path`, `old_path`, `added`,
// `removed`, `diff_excerpt`, `steps`, `decision`, `by`, `process`, `turn`, `interaction`, `note`,
// `summary`, `decisions`, `mode` and `prompt_lines`, and the binding tuple is deliberately absent
// from the app (IS-APR-2: the phone echoes it through App.Approve and a model that carried it is
// a model a screen could compute). So the volatile values are pinned to placeholders and every
// key is KEPT -- the golden is the real body with the unstable values held still, which is what
// makes it checkable in rather than a fresh recording each run.
//
// RED, before internal/skeleton/testdata/i1-transcript-screen.golden.json existed: no pinned
// crossing at .../i1-transcript-screen.golden.json.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/protocol"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

var updateScreenGolden = flag.Bool("update-screen-golden", false,
	"rewrite internal/skeleton/testdata/i1-transcript-screen.golden.json from a live run")

// screenGoldenPath is read by BOTH halves: this test writes it, and
// android/app/build.gradle.kts stages this exact file onto the Robolectric classpath. A copy on
// the Android side would be the second spelling this file exists to prevent.
const screenGoldenPath = "testdata/i1-transcript-screen.golden.json"

// screenGolden is the whole crossing: one recorded turn as the phone holds it while the machine
// is blocked, and the same turn after the phone's own tap answered it.
type screenGolden struct {
	// Corpus names the recording every byte below descends from, so a reader of the golden can
	// find the source without reading this file.
	Corpus  string           `json:"corpus"`
	Pending screenGoldenSide `json:"pending"`
	// Answered is the SAME conversation after the resolution folded. It is recorded rather than
	// derived because IS-LIFE-2's dismissal is the property the screen has to honour: the request
	// stays in the transcript (it is what was asked) and stops being a decision.
	Answered screenGoldenSide `json:"answered"`
}

type screenGoldenSide struct {
	// Items is TranscriptPage walked exactly as FacadeBridge.transcript walks it. The JSON keys
	// are the BOUND FIELD NAMES, so the golden names the getters the Kotlin calls.
	Items []swarmmobile.TranscriptItem `json:"items"`
	// Approvals is App.PendingApprovals, which is what decides whether a card is on screen.
	Approvals []swarmmobile.TranscriptItem `json:"approvals"`
	// Tap is the three flat strings a button passes to App.Approve, and nothing else (IS-APR-2).
	// It is recorded on the side where the tap was possible and empty on the other.
	Tap *screenGoldenTap `json:"tap,omitempty"`
}

type screenGoldenTap struct {
	Session string `json:"session"`
	ItemID  string `json:"item_id"`
	// DecisionID is the CLI's OWN vocabulary, read off the card's `decisions[]` rather than
	// written here: §3.5 keeps the ids the CLI's, and a test that typed one would be asserting a
	// vocabulary it does not own.
	DecisionID string `json:"decision_id"`
}

// TestI1_TheScreensBytesAreTheFacadesBytes.
func TestI1_TheScreensBytesAreTheFacadesBytes(t *testing.T) {
	rig := newS19Rig(t)
	rig.Pair()
	rig.StartGateway()
	rig.Eventually("the machine's reconcile record reached the phone", func() bool {
		return rig.Summary().Reconciled
	})

	// The session PAINTS a recorded claude permission dialog and blocks on it, because since
	// M1.2 a phone approve is APPLIED by typing that dialog's own keys into this PTY
	// (mirror-program.md section 3) -- an approve onto a session showing no dialog is refused.
	// The adapter resolver is the real claude one for the same reason: it holds the key map.
	//
	// It is the EDIT dialog because the corpus replayed below is the recorded EDIT permission,
	// and since M1.8 the gate refuses to type a request's verdict into a dialog raised by a
	// different tool. One screen, one request: until M1.8 nothing checked that they matched.
	rig.sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return claude.New(), true })
	dialog, cols, rows := gridScript(t, editDialogGrid)
	sessionID := rig.LaunchOnMachineSized(dialog, cols, rows)
	rig.Eventually("the phone's roster shows the session the machine launched", func() bool {
		return rig.RosterHas(sessionID)
	})
	_, localID, ok := protocol.ParseID(sessionID)
	if !ok {
		t.Fatalf("owner Launch returned %q, which is not a namespaced id", sessionID)
	}

	// One real recorded Claude Code turn: a prompt, a Read, an Edit that escalated to a
	// permission dialog, the applied change, and the agent's closing sentence.
	const corpus = "claude-edit-permissionrequest-run1.json"
	replayClaudeCorpus(t, rig.sk, localID, corpus)

	var pending []swarmmobile.TranscriptItem
	rig.Eventually("the recorded turn's six items reached the phone", func() bool {
		pending = readTranscript(t, rig, sessionID)
		return len(pending) >= 6
	})
	var card swarmmobile.TranscriptItem
	rig.Eventually("the card the machine is blocked on reached the phone", func() bool {
		p := readPendingApprovals(t, rig)
		if len(p) == 1 {
			card = p[0]
			return true
		}
		return false
	})
	pendingApprovals := readPendingApprovals(t, rig)

	// ---- what the screen needs, asserted where it is produced ------------------
	// The Android decode is total by construction (IS-COMPAT-1/-2: an unreadable body costs a row
	// and nothing else), which is right for a handset and useless as a contract: a producer that
	// stopped sending `body` would render six neutral rows saying only their kind, and every
	// Robolectric assertion would still pass. So the presence of what the screen reads is asserted
	// HERE, against the real stack, where its absence is a defect rather than a degraded row.
	assertScreenReadable(t, pending)

	// ---- the tap, with the id the CARD offered --------------------------------
	// The decision id comes off `decisions[]` rather than from a literal, because that is exactly
	// what the sheet does: ApprovalSheetScreen labels one button per `decisions[].label` and
	// answers with the matching `id`. A hardcoded "allow" here would pass while the app sent
	// something the CLI never offered.
	decisionID := firstDecisionID(t, card)
	op, err := rig.App().Approve(sessionID, card.ItemID, decisionID)
	if err != nil {
		t.Fatalf("App.Approve(%q, %q, %q) was refused: %v%s",
			sessionID, card.ItemID, decisionID, err, rig.gatewayTail())
	}
	if op == nil || op.OperationID == "" {
		t.Fatalf("App.Approve returned %+v; the tap must carry the phone-minted operation id", op)
	}

	// ---- the answer comes back, and the card leaves the pending set -----------
	// The daemon TYPED the answer into the dialog and resolved nothing; §3.6's record lands on
	// the machine's own observation that the dialog left the screen (M1.2). The op crosses a
	// relay and a separate gateway process, so wait for the injection before driving that
	// observation -- the other order resolves the card as answered_locally, a different path.
	awaitApplied(t, rig.sk, localID)
	dialogLeaves(rig.sk, localID)
	awaitFacadeResolution(t, rig, sessionID, card.ItemID)
	rig.Eventually("the answered card left the phone's pending set", func() bool {
		return len(readPendingApprovals(t, rig)) == 0
	})
	answered := readTranscript(t, rig, sessionID)
	if got := findItem(answered, card.ItemID); got == nil || !got.Resolved {
		t.Fatalf("the answered approval_request is %v on the phone; a resolution marks the request "+
			"Resolved and must NOT delete it -- the screen draws it as what was asked and stops "+
			"offering the tap", got)
	}

	// ---- and that crossing is what the Android suite renders ------------------
	got := screenGolden{
		Corpus: corpus,
		Pending: screenGoldenSide{
			Items:     pending,
			Approvals: pendingApprovals,
			Tap: &screenGoldenTap{
				Session:    sessionID,
				ItemID:     card.ItemID,
				DecisionID: decisionID,
			},
		},
		Answered: screenGoldenSide{
			Items:     answered,
			Approvals: readPendingApprovals(t, rig),
		},
	}
	normalizeScreenGolden(t, &got, sessionID)
	compareScreenGolden(t, got)
}

// assertScreenReadable checks that every item carries what the Android decode reads for its kind.
//
// IT IS THE READ-SET AND NOT THE WHOLE SCHEMA. Only what a screen actually renders is asserted,
// because that is what can break the app: the envelope's own required fields are ADR-010's and
// have their own tests, and the binding tuple is deliberately unread here (IS-APR-2).
func assertScreenReadable(t *testing.T, items []swarmmobile.TranscriptItem) {
	t.Helper()
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.Kind] = true
		if it.ItemID == "" || it.Kind == "" {
			t.Errorf("item %+v carries no item_id or no kind; IS-ENV-3 requires the kind on every "+
				"item and IS-APR-1 makes the item_id the only handle a tap has", it)
		}
		body := map[string]any{}
		if err := json.Unmarshal([]byte(it.Body), &body); err != nil {
			t.Errorf("item %s (%s) body does not parse: %v. The app's decode is total and would "+
				"render this as a bare kind, so a producer that stopped filling `body` would break "+
				"every row while the Robolectric suite stayed green", it.ItemID, it.Kind, err)
			continue
		}
		switch it.Kind {
		case "user_message", "agent_message":
			// The RECONSTRUCTION and not the body's `text`: a streamed agent_message's latest
			// record carries only the last increment (IS-DELTA-1), so TranscriptScreen takes the
			// line from TranscriptItem.Text.
			if it.Text == "" {
				t.Errorf("%s item %s has empty Text; the transcript renders the message from the "+
					"FOLD's reconstruction and would draw a bare kind instead", it.Kind, it.ItemID)
			}
		case "tool_run":
			// IS-TOOL-1/-2: the tool names what ran and §7's action carries the one literal the
			// adapter classified. The row reads "Read <path>" out of exactly these two.
			if body["tool"] == nil {
				t.Errorf("tool_run %s carries no `tool`; the row has nothing to name", it.ItemID)
			}
			if body["action"] == nil {
				t.Errorf("tool_run %s carries no `action`; IS-TOOL-1 forbids the phone inferring "+
					"one from the tool name, so the row loses its literal", it.ItemID)
			}
		case "file_change":
			for _, field := range []string{"change", "path"} {
				if body[field] == nil {
					t.Errorf("file_change %s carries no %q; the row's sentence is built from it",
						it.ItemID, field)
				}
			}
		case "approval_request":
			// §3.5's three the CARD is made of. A request offering no decisions is not answerable
			// at all, and both the adapter and ApprovalItem.of refuse to make a card from one.
			if body["summary"] == nil {
				t.Errorf("approval_request %s carries no `summary`; that IS the blocking question "+
					"the sheet asks (§3.5)", it.ItemID)
			}
			decisions, _ := body["decisions"].([]any)
			if len(decisions) == 0 {
				t.Errorf("approval_request %s offers no decisions; a card with no buttons cannot "+
					"be answered and must not be drawn", it.ItemID)
			}
		}
	}
	// The recorded turn is the one that exercises every block the transcript can draw except the
	// plan and the status marker; if the corpus stops producing one of these the golden silently
	// stops covering that row.
	for _, kind := range []string{"user_message", "agent_message", "tool_run", "file_change", "approval_request"} {
		if !seen[kind] {
			t.Errorf("the recorded turn produced no %s item, so the golden no longer covers that "+
				"row of the transcript", kind)
		}
	}
}

// firstDecisionID is the id of the first button the card offers, in the adapter's own order.
func firstDecisionID(t *testing.T, card swarmmobile.TranscriptItem) string {
	t.Helper()
	var body struct {
		Decisions []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(card.Body), &body); err != nil {
		t.Fatalf("the card's body does not parse: %v", err)
	}
	if len(body.Decisions) == 0 {
		t.Fatal("the card offers no decisions, so there is no tap to make")
	}
	if body.Decisions[0].ID == "" || body.Decisions[0].Label == "" {
		t.Fatalf("the card's first decision is %+v; §3.5 carries {id, label} and the sheet drops "+
			"one missing either", body.Decisions[0])
	}
	return body.Decisions[0].ID
}

// normalizeScreenGolden pins the six per-run values. See the file header for why every key is
// kept and why the app reads none of them.
func normalizeScreenGolden(t *testing.T, g *screenGolden, sessionID string) {
	t.Helper()

	// The item ids are numbered in the transcript's own order, so the golden's tap names the same
	// placeholder the Android test taps and the mapping is readable in the file.
	ids := map[string]string{}
	for i, it := range g.Pending.Items {
		ids[it.ItemID] = fixedItemID(i)
	}
	for _, it := range g.Answered.Items {
		if _, known := ids[it.ItemID]; !known {
			ids[it.ItemID] = fixedItemID(len(ids))
		}
	}

	sides := []*screenGoldenSide{&g.Pending, &g.Answered}
	for _, side := range sides {
		for i := range side.Items {
			normalizeScreenItem(t, &side.Items[i], ids, sessionID, i)
		}
		for i := range side.Approvals {
			normalizeScreenItem(t, &side.Approvals[i], ids, sessionID, i)
		}
		if side.Tap != nil {
			side.Tap.Session = fixedSessionID
			side.Tap.ItemID = ids[side.Tap.ItemID]
		}
	}
}

func normalizeScreenItem(t *testing.T, it *swarmmobile.TranscriptItem, ids map[string]string, sessionID string, index int) {
	t.Helper()
	if it.SessionID != sessionID {
		t.Errorf("item %s belongs to session %q, not the one under test %q", it.ItemID, it.SessionID, sessionID)
	}
	stable, known := ids[it.ItemID]
	if !known {
		t.Fatalf("item %s was not numbered; the normalizer and the walk disagree", it.ItemID)
	}
	body := map[string]any{}
	if err := json.Unmarshal([]byte(it.Body), &body); err != nil {
		t.Fatalf("item %s body does not parse: %v", it.ItemID, err)
	}
	body["item_id"] = stable
	if _, has := body["turn_id"]; has {
		body["turn_id"] = fixedTurnID
	}
	if _, has := body["ts"]; has {
		body["ts"] = fixedTimestamp
	}
	// ADR-007 D7's binding tuple. It is pinned rather than dropped so the golden still shows that
	// the machine sends it -- and the Android suite still renders a card without reading any of it,
	// which is IS-APR-2 holding on the screen side.
	if _, has := body["content_hash"]; has {
		body["content_hash"] = fixedContentHash
	}
	if _, has := body["expires_at"]; has {
		body["expires_at"] = fixedExpiry
	}
	if _, has := body["agent_instance"]; has {
		body["agent_instance"] = map[string]any{"shim_pid": 1, "shim_start_time": 1}
	}
	// §3.6's back-references on an approval_resolved. `interaction_id` names the REQUEST, so it is
	// mapped through the same numbering rather than pinned -- the golden then shows the resolution
	// pointing at the card above it, which is the relationship IS-LIFE-2 is about. `operation_id`
	// is the phone's own per-tap mint and is pinned.
	if raw, has := body["interaction_id"]; has {
		if named, ok := raw.(string); ok {
			if stableRef, known := ids[named]; known {
				body["interaction_id"] = stableRef
			} else {
				t.Errorf("approval_resolved %s references interaction_id %q, which is no item in "+
					"this transcript; the screen could not join the two", it.ItemID, named)
			}
		}
	}
	if _, has := body["operation_id"]; has {
		body["operation_id"] = fixedOperationID
	}
	// THE ITEM-LEVEL FIELD NEEDS THE SAME TREATMENT AS THE ONE IN THE BODY, and it did not get it
	// when the field was added.
	//
	// `OperationID` reached `TranscriptItem` so the phone could settle a sent bubble against the
	// agent's own echo (owner ruling R6). The body's copy has been normalised since this file was
	// written; the new field was not, so the golden captured whichever id that run happened to
	// mint -- a fresh 32 hex characters every time. The result was a byte-exact golden that could
	// only ever match the run that wrote it, failing forever afterwards while pointing at a diff
	// whose only difference was a random number.
	//
	// It is a REAL id in production and carries a real fact; what it must not do is enter a
	// recorded crossing that is compared byte for byte. Normalised here rather than dropped,
	// because dropping it would stop the recording asserting the field crosses at all -- which is
	// the whole reason it was added.
	if it.OperationID != "" {
		it.OperationID = fixedOperationID
	}
	// json.Marshal sorts map keys, which is what makes the re-encoded body byte-stable. The
	// producer already emits sorted keys (see the recorded probe in the evidence file), so this
	// reproduces its own ordering rather than imposing a new one.
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("re-encoding item %s body: %v", it.ItemID, err)
	}
	it.SessionID = fixedSessionID
	it.ItemID = stable
	it.Body = string(encoded)
	it.Cursor = int64(index + 1)
	it.TurnID = fixedTurnID
	it.TSUnixMs = 0
}

func fixedItemID(index int) string { return fmt.Sprintf("item-%d", index+1) }

const (
	fixedSessionID   = "machine/session"
	fixedTurnID      = "turn-1"
	fixedTimestamp   = "2026-08-08T00:00:00.000Z"
	fixedContentHash = "0000000000000000000000000000000000000000000000000000000000000000"
	fixedExpiry      = "2026-08-08T00:02:00.000Z"
	fixedOperationID = "00000000000000000000000000000000"
)

// compareScreenGolden diffs the recorded crossing against the checked-in one, or rewrites it
// under -update-screen-golden. Regenerating is the point at which someone must justify the diff:
// a change here is a change in what every Android row renders.
func compareScreenGolden(t *testing.T, got screenGolden) {
	t.Helper()
	// The transcript's order is the fold's (ascending cursor), which the phone already guarantees;
	// sorting here would hide a producer that started emitting them shuffled.
	if !sort.SliceIsSorted(got.Pending.Items, func(i, j int) bool {
		return got.Pending.Items[i].Cursor < got.Pending.Items[j].Cursor
	}) {
		t.Error("the transcript page is not in ascending cursor order; the conversation would read " +
			"with the agent's answer above the question it answers")
	}

	blob, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("encoding the golden: %v", err)
	}
	blob = append(blob, '\n')

	if *updateScreenGolden {
		if err := os.MkdirAll(filepath.Dir(screenGoldenPath), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(screenGoldenPath, blob, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("rewrote %s (%d pending items, %d answered)",
			screenGoldenPath, len(got.Pending.Items), len(got.Answered.Items))
		return
	}

	want, err := os.ReadFile(screenGoldenPath)
	if err != nil {
		t.Fatalf("no pinned crossing at %s: %v. The Android suite renders this file; without it "+
			"the screen is asserted only against fixtures written on its own side", screenGoldenPath, err)
	}
	if string(blob) == string(want) {
		return
	}
	t.Errorf("what the real stack delivers to the facade drifted from the crossing the Android "+
		"suite renders.\n--- pinned (%s)\n%s\n--- recorded now\n%s\nIf the change is intended and "+
		"reviewed, re-run with -update-screen-golden, and expect the Android assertions that name "+
		"the old copy to go red with it.", screenGoldenPath, want, blob)
}
