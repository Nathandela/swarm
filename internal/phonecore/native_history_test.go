package phonecore

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func nativeHistoryItem(id, text string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"v": 2, "kind": "message_snapshot", "item_id": id, "ts": time.Unix(1700000000, 0).UTC(), "role": "user", "source": "native", "text": text, "provenance": "imported"})
	return raw
}
func nativeHistoryCore(t *testing.T) *Core {
	c := publicationCore(t)
	if err := c.Mutate(func(st *State) {
		st.Sessions = []CachedSession{{SessionID: "m1/s1", Capabilities: &schema.SessionCapabilities{Provider: "opencode", SessionInstance: "instance", NativeInteractionVersion: 2, TerminalFallback: true}}}
	}); err != nil {
		t.Fatal(err)
	}
	return c
}
func prepareNativePage(t *testing.T, c *Core, id, before string) {
	t.Helper()
	p := testComposerPublication(id)
	p.Kind = PublicationHistory
	p.Command.Action = schema.ActionInteractionHistory
	p.Composer = nil
	p.Text = ""
	p.SessionInstance = ""
	p.ExpectedTurn = ""
	req := c.HistoryRequest(p.SessionID, before, 50)
	p.History = &req
	if !req.Native {
		t.Fatal("proven native session selected journal history")
	}
	if err := c.PreparePublication(p); err != nil {
		t.Fatal(err)
	}
}
func deliverNativePage(t *testing.T, c *Core, id string, page schema.NativeHistoryPage, seq uint64) {
	t.Helper()
	ctrl := schema.Control{Op: schema.ActionInteractionHistory, OperationID: id, SessionID: "m1/s1", NativeHistory: &page}
	raw, _ := json.Marshal(replyFrame{Kind: kindCommandReply, Control: ctrl})
	st := c.State()
	if _, err := c.Router().AcceptCommit(sealFrameFrom(t, st.Keys.ContentKey, [8]byte{}, st.EpochID, seq, raw), seq); err != nil {
		t.Fatal(err)
	}
}
func TestNativeHistoryCursorlessPagePersistsWithoutMovingLiveOrder(t *testing.T) {
	c := nativeHistoryCore(t)
	prepareNativePage(t, c, "page-1", "")
	page := schema.NativeHistoryPage{Version: 2, Scope: strings.Repeat("a", 64), NextCursor: "native-next", Items: []json.RawMessage{nativeHistoryItem("older", "old words")}}
	deliverNativePage(t, c, "page-1", page, 1)
	got := c.Router().Items().Session("m1/s1")
	if len(got) != 1 || got[0].Cursor != 0 || got[0].LastCursor != 0 || !got[0].Backfilled {
		t.Fatalf("imported page became journal history: %+v", got)
	}
	if c.State().OpOutcomes["page-1"].NativeHistory != nil {
		t.Fatal("page payload retained inside operation outcome")
	}
	req := c.HistoryRequest("m1/s1", "older", 50)
	if !req.Native || req.NativeCursor != "native-next" {
		t.Fatalf("native continuation lost: %+v", req)
	}
	if c.HistoryRequest("m1/s1", "", 50).NativeCursor != "" {
		t.Fatal("explicit newest did not reset native cursor")
	}
	st := c.State()
	st.NativeHistory["m1/s1"] = NativeHistoryState{}
	if c.HistoryRequest("m1/s1", "older", 50).NativeCursor != "native-next" {
		t.Fatal("state reader aliased history cursor")
	}
	saved := c.store.(*fileStore)
	reopened, err := OpenStore(saved.path, "m1", saved.wake, saved.content)
	if err != nil {
		t.Fatal(err)
	}
	next, err := Resume(Config{Machine: "m1", State: reopened})
	if err != nil {
		t.Fatal(err)
	}
	if next.HistoryRequest("m1/s1", "older", 50).NativeCursor != "native-next" || len(next.Router().Items().Session("m1/s1")) != 1 {
		t.Fatal("native page/cursor lost on restart")
	}
}
func TestNativeHistoryRefusesAuthorityForeignScopeAndLateReads(t *testing.T) {
	c := nativeHistoryCore(t)
	scope := strings.Repeat("a", 64)
	prepareNativePage(t, c, "first", "")
	deliverNativePage(t, c, "first", schema.NativeHistoryPage{Version: 2, Scope: scope, NextCursor: "cursor", Items: []json.RawMessage{nativeHistoryItem("old", "old")}}, 1)
	prepareNativePage(t, c, "second", "old")
	deliverNativePage(t, c, "second", schema.NativeHistoryPage{Version: 2, Scope: strings.Repeat("b", 64), Floor: true, Items: []json.RawMessage{nativeHistoryItem("foreign", "wrong")}}, 2)
	if _, ok := c.State().OpOutcomes["second"]; ok || c.State().HistoryFloor["m1/s1"] {
		t.Fatal("foreign scope completed native history")
	}
	prepareNativePage(t, c, "replacement", "")
	deliverNativePage(t, c, "second", schema.NativeHistoryPage{Version: 2, Scope: scope, Floor: true, Items: []json.RawMessage{nativeHistoryItem("late", "wrong")}}, 3)
	if _, ok := c.State().OpOutcomes["second"]; ok {
		t.Fatal("late page changed latest read")
	}
	bad := strings.Replace(string(nativeHistoryItem("bad", "bad")), `"message_snapshot"`, `"permission_request"`, 1)
	deliverNativePage(t, c, "replacement", schema.NativeHistoryPage{Version: 2, Scope: scope, Floor: true, Items: []json.RawMessage{json.RawMessage(bad)}}, 4)
	if _, ok := c.State().OpOutcomes["replacement"]; ok {
		t.Fatal("history granted request authority")
	}
	if len(c.Router().Items().Session("m1/s1")) != 1 {
		t.Fatal("refused native pages changed transcript")
	}
}
func TestNativeHistoryLiveWinsAndCapacityIsAtomic(t *testing.T) {
	c := nativeHistoryCore(t)
	scope := strings.Repeat("a", 64)
	raw := strings.Replace(string(nativeHistoryItem("same", "current")), `"imported"`, `"live"`, 1)
	c.Router().Items().Apply(schema.JournalRecord{Type: RecordTypeInteraction, SessionID: "m1/s1", Cursor: 77, Item: json.RawMessage(raw)})
	if err := c.Mutate(func(st *State) { st.Items = c.Router().Items().All() }); err != nil {
		t.Fatal(err)
	}
	prepareNativePage(t, c, "first", "")
	items := []json.RawMessage{nativeHistoryItem("same", "old")}
	for i := 0; i < 199; i++ {
		items = append(items, nativeHistoryItem(fmt.Sprint("old", i), "old"))
	}
	deliverNativePage(t, c, "first", schema.NativeHistoryPage{Version: 2, Scope: scope, NextCursor: "next", Items: items}, 1)
	live, _ := c.Router().Items().Resolve("m1/s1", "same")
	if live.Text != "current" || live.Cursor != 77 {
		t.Fatal("native page overwrote live row")
	}
	prepareNativePage(t, c, "overfull", "old0")
	deliverNativePage(t, c, "overfull", schema.NativeHistoryPage{Version: 2, Scope: scope, Floor: true, Items: []json.RawMessage{nativeHistoryItem("extra1", "x"), nativeHistoryItem("extra2", "y")}}, 2)
	if !c.State().HistoryCapped["m1/s1"] || c.State().HistoryFloor["m1/s1"] || c.HistoryRequest("m1/s1", "old0", 50).NativeCursor != "next" {
		t.Fatal("capacity refusal advanced cursor/floor or lost its warning")
	}
	if len(c.Router().Items().Session("m1/s1")) != 200 {
		t.Fatal("capacity refusal partially retained native page")
	}
}

func TestNativeHistoryKeepsNewestLogicalPlanAndLivePromotion(t *testing.T) {
	c := nativeHistoryCore(t)
	scope := strings.Repeat("a", 64)
	plan := func(at int64, text string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"v": 2, "kind": "plan_snapshot", "item_id": "plan", "ts": time.Unix(at, 0).UTC(), "text": text, "provenance": "imported"})
		return raw
	}
	prepareNativePage(t, c, "plans", "")
	deliverNativePage(t, c, "plans", schema.NativeHistoryPage{Version: 2, Scope: scope, NextCursor: "older", Items: []json.RawMessage{plan(10, "older"), plan(20, "newest"), plan(15, "middle")}}, 1)
	got, _ := c.Router().Items().Resolve("m1/s1", "plan")
	if got.Text != "newest" {
		t.Fatalf("within-page plan regressed: %+v", got)
	}
	prepareNativePage(t, c, "older-plan", "plan")
	deliverNativePage(t, c, "older-plan", schema.NativeHistoryPage{Version: 2, Scope: scope, Floor: true, Items: []json.RawMessage{plan(5, "oldest")}}, 2)
	got, _ = c.Router().Items().Resolve("m1/s1", "plan")
	if got.Text != "newest" {
		t.Fatal("older page regressed newer imported plan")
	}
	live := strings.Replace(string(plan(30, "live")), `"imported"`, `"live"`, 1)
	if !c.Router().Items().Apply(schema.JournalRecord{Type: RecordTypeInteraction, SessionID: "m1/s1", Cursor: 99, Item: json.RawMessage(live)}) {
		t.Fatal("live promotion refused")
	}
	got, _ = c.Router().Items().Resolve("m1/s1", "plan")
	if got.Text != "live" || got.Cursor != 99 || got.Backfilled {
		t.Fatalf("live row retained cursorless history authority: %+v", got)
	}
}

func TestNativeHistoryIgnoredRepeatCannotEraseLiveOnlyGapItems(t *testing.T) {
	c := nativeHistoryCore(t)
	prepareNativePage(t, c, "first", "")
	page := schema.NativeHistoryPage{Version: 2, Scope: strings.Repeat("a", 64), NextCursor: "next", Items: []json.RawMessage{nativeHistoryItem("old", "old")}}
	deliverNativePage(t, c, "first", page, 1)
	live := strings.Replace(string(nativeHistoryItem("gap-only", "visible after gap")), `"imported"`, `"live"`, 1)
	c.Router().Items().Apply(schema.JournalRecord{Type: RecordTypeInteraction, SessionID: "m1/s1", Cursor: 90, Item: json.RawMessage(live)})
	page.Scope = strings.Repeat("b", 64)
	deliverNativePage(t, c, "first", page, 2)
	if _, ok := c.Router().Items().Resolve("m1/s1", "gap-only"); !ok {
		t.Fatal("ignored native page replay erased live-only gap content")
	}
}
