package skeleton

// FAILING-FIRST (TDD RED, GG-5) for Wave R6's Mirror M3.1 (paged interaction history: the
// daemon session-item index behind ADR-014's `(session, before_item, limit)` read) and
// M3.3 (detail-on-demand: full pre-truncation payloads retained at capture time, served
// by IS-CAP-2's unsigned read, refused `unavailable` outside retention per IS-CAP-3).
// Bead: agents-tracker-hggx.7. Undefined symbols -> compile-fail RED is expected.
//
// THE CONTRACT these tests freeze, on the coreAPI seam (the daemon halves of the
// protocol-side InteractionHistorian / InteractionDetailer frozen in
// internal/protocol/r6_historydetail_test.go):
//
//	func (a *coreAPI) InteractionHistory(session, beforeItem string, limit int) ([]protocol.JournalRecord, bool, protocol.ErrorCode, error)
//	func (a *coreAPI) InteractionDetail(session, itemID string) (json.RawMessage, protocol.ErrorCode, error)

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

// r6HistoryRig captures n self-contained user messages and returns (daemon, namespaced
// session, journalled items in order).
func r6HistoryRig(t *testing.T, local string, n int) (*Daemon, string, []map[string]any) {
	t.Helper()
	sk := assemble(t)
	for i := 0; i < n; i++ {
		sk.captureInteractions(local, newCaptureAdapter(adapter.Interaction{
			Kind: adapter.KindUserMessage, Source: adapter.SourceOwner,
			Text: "msg-" + string(rune('0'+i)),
		}), adapter.HookPayload{Event: "UserPromptSubmit"})
	}
	items := awaitItems(t, sk, local, n)
	return sk, protocol.NamespacedID(sk.api.endpointID, local), items
}

func r6ItemID(t *testing.T, it map[string]any) string {
	t.Helper()
	id, _ := it["item_id"].(string)
	if id == "" {
		t.Fatalf("item carries no item_id: %v", it)
	}
	return id
}

// TestR6History_PagesStrictlyOlderThanBeforeItemInAscendingOrder is M3.1's paging
// conformance: the window immediately preceding before_item, ascending by cursor, bound
// by limit, every record for the named session.
func TestR6History_PagesStrictlyOlderThanBeforeItemInAscendingOrder(t *testing.T) {
	sk, session, items := r6HistoryRig(t, "s-history-page", 5)

	recs, floor, code, err := sk.api.InteractionHistory(session, r6ItemID(t, items[4]), 2)
	if err != nil || code != "" {
		t.Fatalf("history page refused: code %q err %v", code, err)
	}
	if len(recs) != 2 {
		t.Fatalf("history page holds %d records, want 2 (the limit)", len(recs))
	}
	if floor {
		t.Error("floor=true with two older items still retained; the phone would stop offering 'load earlier' early")
	}
	var got []string
	for i, rec := range recs {
		var item struct {
			ItemID string `json:"item_id"`
		}
		if uerr := json.Unmarshal(rec.Item, &item); uerr != nil {
			t.Fatalf("history record %d carries no decodable item: %v", i, uerr)
		}
		got = append(got, item.ItemID)
		if i > 0 && recs[i-1].Cursor >= rec.Cursor {
			t.Errorf("history records out of ascending cursor order at %d: %d then %d", i, recs[i-1].Cursor, rec.Cursor)
		}
	}
	want := []string{r6ItemID(t, items[2]), r6ItemID(t, items[3])}
	if got[0] != want[0] || got[1] != want[1] {
		t.Errorf("history page = %v, want the two items immediately before before_item: %v", got, want)
	}
}

// TestR6History_TheOldestPageReportsTheFloorHonestly: asking below the oldest retained
// item answers an empty page WITH the floor flag -- the phone renders a retention floor,
// not a spinner (M3.1; the honest-retention row of the complete-chat table).
func TestR6History_TheOldestPageReportsTheFloorHonestly(t *testing.T) {
	sk, session, items := r6HistoryRig(t, "s-history-floor", 3)

	recs, floor, code, err := sk.api.InteractionHistory(session, r6ItemID(t, items[0]), 10)
	if err != nil || code != "" {
		t.Fatalf("oldest history page refused: code %q err %v", code, err)
	}
	if len(recs) != 0 {
		t.Errorf("page below the oldest item holds %d records, want 0", len(recs))
	}
	if !floor {
		t.Error("the oldest page reports no floor; 'load earlier' would spin forever")
	}
}

// TestR6History_AnUnknownBeforeItemIsRefusedInvalidField: paging from an id the session
// never held is a coded refusal, never an arbitrary page.
func TestR6History_AnUnknownBeforeItemIsRefusedInvalidField(t *testing.T) {
	sk, session, _ := r6HistoryRig(t, "s-history-unknown", 2)

	_, _, code, err := sk.api.InteractionHistory(session, "01JNEVERANITEM", 5)
	if err == nil || code != protocol.CodeInvalidField {
		t.Fatalf("unknown before_item = code %q err %v, want invalid_field with an error", code, err)
	}
}

// TestR6History_ANonPositiveLimitIsRefusedNotDefaulted: there is no limit 0; a caller
// that sends one has a bug this layer must surface, not paper over.
func TestR6History_ANonPositiveLimitIsRefusedNotDefaulted(t *testing.T) {
	sk, session, items := r6HistoryRig(t, "s-history-limit", 2)

	_, _, code, err := sk.api.InteractionHistory(session, r6ItemID(t, items[1]), 0)
	if err == nil || code != protocol.CodeInvalidField {
		t.Fatalf("limit 0 = code %q err %v, want invalid_field with an error", code, err)
	}
}

// TestR6Detail_ATruncatedItemSetsDetailAndTheFullBodyIsRetrievable is M3.3's row test:
// an output past MaxTextBytes journals truncated with detail=true (IS-CAP-2), and the
// detail read returns the WHOLE body -- including bytes far past the clip.
func TestR6Detail_ATruncatedItemSetsDetailAndTheFullBodyIsRetrievable(t *testing.T) {
	sk := assemble(t)
	local := "s-detail-full"
	const marker = "THE-VERY-END-OF-THE-OUTPUT"
	big := strings.Repeat("x", 10*1024) + marker
	sk.captureInteractions(local, newCaptureAdapter(adapter.Interaction{
		Kind: adapter.KindToolRun, Status: adapter.StatusCompleted, Tool: "Bash",
		Action: adapter.ToolAction{Type: "execute", Command: "make everything"}, OutputExcerpt: big,
	}), adapter.HookPayload{Event: "PostToolUse"})
	items := awaitItems(t, sk, local, 1)
	var toolRun map[string]any
	for _, it := range items {
		if it["kind"] == adapter.KindToolRun {
			toolRun = it
		}
	}
	if toolRun == nil {
		t.Fatalf("no tool_run journalled: %v", items)
	}
	if trunc, _ := toolRun["truncated"].(bool); !trunc {
		t.Fatalf("a 10 KiB output journalled untruncated; the §5 caps did not apply: %v", toolRun)
	}
	if det, _ := toolRun["detail"].(bool); !det {
		t.Error("truncated item carries detail=false; IS-CAP-2 requires detail=true when the full body is retrievable")
	}

	session := protocol.NamespacedID(sk.api.endpointID, local)
	full, code, err := sk.api.InteractionDetail(session, r6ItemID(t, toolRun))
	if err != nil || code != "" {
		t.Fatalf("detail read refused: code %q err %v", code, err)
	}
	if !strings.Contains(string(full), marker) {
		t.Errorf("the detail body lacks the output's final bytes; a partial body presented as " +
			"whole is exactly what IS-CAP-3 forbids")
	}
}

// TestR6Detail_AnUnknownItemAnswersUnavailable: outside retention (or never captured) is
// IS-CAP-3's `unavailable`, its own code.
func TestR6Detail_AnUnknownItemAnswersUnavailable(t *testing.T) {
	sk, session, _ := r6HistoryRig(t, "s-detail-unknown", 1)

	_, code, err := sk.api.InteractionDetail(session, "01JNEVERSTORED")
	if err == nil || code != protocol.CodeUnavailable {
		t.Fatalf("unknown detail = code %q err %v, want unavailable with an error", code, err)
	}
}
