package skeleton

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"strings"
	"testing"
)

type nativePageReadFake struct {
	meta    persist.Meta
	rows    []json.RawMessage
	cursors map[string]int
	limits  []int
}

func newNativePageReadFake(t *testing.T) *nativePageReadFake {
	t.Helper()
	return &nativePageReadFake{meta: persist.Meta{ID: "worker", AgentType: "opencode", RequiredRuntime: persist.RequiredRuntimeOpenCode, ConversationID: "ses_" + strings.Repeat("a", 32), Cwd: t.TempDir(), OpenCodeBinding: &persist.OpenCodeBinding{NativeCreatedAtMS: 1, Generation: strings.Repeat("b", 32), ProfileEnv: []string{"HOME=" + t.TempDir()}}}, cursors: map[string]int{}}
}
func (f *nativePageReadFake) row(n, parts, width int) json.RawMessage {
	id := fmt.Sprintf("msg_%032x", n)
	list := []any{}
	for i := 0; i < parts; i++ {
		list = append(list, map[string]any{"id": fmt.Sprintf("prt_%032x", n*1000+i), "sessionID": f.meta.ConversationID, "messageID": id, "type": "text", "text": fmt.Sprintf("row%d:", n) + strings.Repeat("a", width)})
	}
	raw, _ := json.Marshal(map[string]any{"info": map[string]any{"id": id, "sessionID": f.meta.ConversationID, "role": "assistant", "parentID": "msg_parent", "time": map[string]int64{"created": int64(n + 1), "completed": int64(n + 2)}, "finish": "stop"}, "parts": list})
	return raw
}
func (f *nativePageReadFake) read(_ context.Context, limit int, before string) (nativeHistorySlice, error) {
	f.limits = append(f.limits, limit)
	end := len(f.rows)
	if before != "" {
		n, ok := f.cursors[before]
		if !ok {
			return nativeHistorySlice{}, fmt.Errorf("invented provider cursor %q", before)
		}
		end = n
	}
	start := max(0, end-limit)
	next := ""
	if start > 0 {
		next = fmt.Sprintf("opaque/%d", start)
		f.cursors[next] = start
	}
	session, _ := json.Marshal(map[string]any{"id": f.meta.ConversationID, "directory": f.meta.ProviderCwd(), "time": map[string]int64{"created": 1}})
	return nativeHistorySlice{Session: session, Messages: append([]json.RawMessage{}, f.rows[start:end]...), NextCursor: next}, nil
}
func TestNativeHistoryPagesUseOnlyRealProviderCursors(t *testing.T) {
	f := newNativePageReadFake(t)
	for n := 1; n <= 4; n++ {
		f.rows = append(f.rows, f.row(n, 1, 1))
	}
	req := schema.InteractionHistoryReq{Native: true, Limit: 1}
	page, _, err := paginateNativeHistory(context.Background(), f.meta, req, f.read)
	if err != nil {
		t.Fatal(err)
	}
	if page.Floor || page.NextCursor == "" || len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), "row4:") {
		t.Fatalf("newest page: %+v", page)
	}
	req.NativeCursor = page.NextCursor
	page, _, err = paginateNativeHistory(context.Background(), f.meta, req, f.read)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), "row3:") {
		t.Fatalf("opaque continuation skipped rows: %+v", page)
	}
	scope, _ := nativeProjectionScope(f.meta)
	req.NativeCursor = ""
	req.BeforeItem = nativeProjectionID(scope, "message_snapshot", f.meta.ConversationID+":"+fmt.Sprintf("prt_%032x", 3000))
	page, _, err = paginateNativeHistory(context.Background(), f.meta, req, f.read)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), "row2:") {
		t.Fatalf("native exact anchor ignored: %+v", page)
	}
	req.BeforeItem = "missing-native-item"
	if _, _, err := paginateNativeHistory(context.Background(), f.meta, req, f.read); err == nil {
		t.Fatal("missing anchor silently started latest history")
	}
}
func TestNativeHistoryShrinksWholeNativePagesWithoutSkippingParts(t *testing.T) {
	f := newNativePageReadFake(t)
	f.rows = []json.RawMessage{f.row(1, 80, 8000), f.row(2, 80, 8000)}
	page, _, err := paginateNativeHistory(context.Background(), f.meta, schema.InteractionHistoryReq{Native: true, Limit: 2}, f.read)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 80 || page.NextCursor == "" || len(f.limits) < 2 || f.limits[len(f.limits)-1] != 1 {
		t.Fatalf("oversized page was cut instead of requeried whole: items%d limits%v", len(page.Items), f.limits)
	}
	for _, item := range page.Items {
		if !strings.Contains(string(item), "row2:") {
			t.Fatal("page mixed selected boundary")
		}
	}
	f.rows = []json.RawMessage{f.row(1, 150, 8000)}
	if _, _, err := paginateNativeHistory(context.Background(), f.meta, schema.InteractionHistoryReq{Native: true, Limit: 1}, f.read); err == nil {
		t.Fatal("single over-bound native row was partially returned")
	}
}
func TestNativeHistoryRefusesForeignScopeCursorAndRows(t *testing.T) {
	f := newNativePageReadFake(t)
	f.rows = []json.RawMessage{f.row(1, 1, 1), f.row(2, 1, 1)}
	page, _, err := paginateNativeHistory(context.Background(), f.meta, schema.InteractionHistoryReq{Native: true, Limit: 1}, f.read)
	if err != nil {
		t.Fatal(err)
	}
	other := f.meta
	other.ConversationID = "ses_" + strings.Repeat("c", 32)
	if _, _, err := paginateNativeHistory(context.Background(), other, schema.InteractionHistoryReq{Native: true, Limit: 1, NativeCursor: page.NextCursor}, f.read); err == nil {
		t.Fatal("cursor moved across native roots")
	}
	f.rows = []json.RawMessage{json.RawMessage(strings.ReplaceAll(string(f.row(3, 1, 1)), f.meta.ConversationID, other.ConversationID))}
	if _, _, err := paginateNativeHistory(context.Background(), f.meta, schema.InteractionHistoryReq{Native: true, Limit: 1}, f.read); err == nil {
		t.Fatal("foreign native messages entered history")
	}
}

func TestNativeHistoryContinuationRejectsNonOlderRows(t *testing.T) {
	for _, newer := range []bool{false, true} {
		t.Run(fmt.Sprint(newer), func(t *testing.T) {
			f := newNativePageReadFake(t)
			f.rows = []json.RawMessage{f.row(1, 1, 1), f.row(2, 1, 1)}
			page, _, err := paginateNativeHistory(context.Background(), f.meta, schema.InteractionHistoryReq{Native: true, Limit: 1}, f.read)
			if err != nil {
				t.Fatal(err)
			}
			bad := f.row(2, 1, 1)
			if newer {
				bad = f.row(3, 1, 1)
			}
			read := func(ctx context.Context, limit int, before string) (nativeHistorySlice, error) {
				p, e := f.read(ctx, limit, before)
				p.Messages = []json.RawMessage{bad}
				return p, e
			}
			if _, _, err := paginateNativeHistory(context.Background(), f.meta, schema.InteractionHistoryReq{Native: true, Limit: 1, NativeCursor: page.NextCursor}, read); err == nil {
				t.Fatal("native continuation accepted an equal or newer boundary row")
			}
		})
	}
	f := newNativePageReadFake(t)
	f.rows = []json.RawMessage{f.row(1, 1, 1), f.row(2, 1, 1)}
	scope, _ := nativeProjectionScope(f.meta)
	token, _ := encodeNativeHistoryCursor(scope, "sdk", "opaque/1", f.row(2, 1, 1))
	f.cursors["opaque/1"] = 1
	for _, n := range []int{1, 2, 3} {
		raw := json.RawMessage(strings.ReplaceAll(string(f.row(n, 1, 1)), fmt.Sprintf(`"created":%d`, n+1), `"created":3`))
		read := func(ctx context.Context, limit int, before string) (nativeHistorySlice, error) {
			p, e := f.read(ctx, limit, before)
			p.Messages = []json.RawMessage{raw}
			return p, e
		}
		_, _, err := paginateNativeHistory(context.Background(), f.meta, schema.InteractionHistoryReq{Native: true, Limit: 1, NativeCursor: token}, read)
		if (err == nil) != (n == 1) {
			t.Fatalf("same-time ID ordering n=%d: %v", n, err)
		}
	}
}

func TestNativeHistoryCoreReadUsesBoundSnapshotWithoutPublishing(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.d.stopOpenCodeObservation(f.binding.WorkerID)
	meta, _ := f.d.core.Get(f.binding.WorkerID)
	source := newNativePageReadFake(t)
	source.meta = meta
	source.rows = []json.RawMessage{source.row(1, 1, 20000), source.row(2, 1, 1)}
	// The production reader validates both the bridge identity and the native row.
	f.historyPage.Store(func(limit int, before string) (opencodebridge.Snapshot, error) {
		p, e := source.read(context.Background(), limit, before)
		p.Session = json.RawMessage(strings.ReplaceAll(string(p.Session), `"created":1`, `"created":123`))
		return opencodebridge.Snapshot{Identity: f.identity, Session: p.Session, Messages: p.Messages, NextCursor: p.NextCursor, Statuses: map[string]json.RawMessage{}, Permissions: []json.RawMessage{}, Questions: []json.RawMessage{}, Children: []json.RawMessage{}, Descendants: []json.RawMessage{}, HierarchyComplete: true, HierarchyScope: openCodeHierarchyScope}, e
	})
	req := schema.InteractionHistoryReq{Session: f.request.Session, Native: true, Limit: 1}
	page, code, err := f.d.api.NativeInteractionHistory(req)
	if err != nil {
		t.Fatalf("wired native history: %s %v", code, err)
	}
	if page.Floor || len(page.Items) != 1 {
		t.Fatalf("first page: %+v", page)
	}
	req.NativeCursor = page.NextCursor
	page, code, err = f.d.api.NativeInteractionHistory(req)
	if err != nil || !page.Floor || len(page.Items) != 1 {
		t.Fatalf("next page: %+v %s %v", page, code, err)
	}
	var item map[string]any
	if err = json.Unmarshal(page.Items[0], &item); err != nil {
		t.Fatal(err)
	}
	if item["provenance"] != "imported" || item["operation_id"] != nil || item["source"] == "phone" {
		t.Fatal("history acquired live authority")
	}
	full, _, err := f.d.interactionDetail(req.Session, item["item_id"].(string))
	if err != nil || len(full) <= len(page.Items[0]) {
		t.Fatal("clipped history lost its retained full detail")
	}
	if f.calls.Load() != 0 {
		t.Fatal("history read submitted native input")
	}
	now, _ := f.d.core.Get(meta.ID)
	if now.Status != meta.Status {
		t.Fatal("history read mutated native status")
	}
	req.Session = "foreign/" + meta.ID
	if _, _, err = f.d.api.NativeInteractionHistory(req); err == nil {
		t.Fatal("history crossed machine scope")
	}
}
func TestNativeHistoryFilteredAndEndedPages(t *testing.T) {
	f := newNativePageReadFake(t)
	f.rows = []json.RawMessage{f.row(1, 1, 1), f.row(2, 0, 0), f.row(3, 0, 0)}
	req := schema.InteractionHistoryReq{Native: true, Limit: 1}
	for _, live := range []bool{true, false} {
		var page schema.NativeHistoryPage
		var err error
		if live {
			page, _, err = paginateNativeHistory(context.Background(), f.meta, req, f.read)
		} else {
			page, _, err = paginateEndedNativeHistory(f.meta, req, f.rows)
		}
		if err != nil || !page.Floor || len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), "row1:") {
			t.Fatalf("filtered rows live=%v: %+v %v", live, page, err)
		}
	}
	f.rows = []json.RawMessage{f.row(1, 1, 1), f.row(2, 1, 1), f.row(3, 1, 1)}
	page, _, err := paginateEndedNativeHistory(f.meta, req, f.rows)
	if err != nil || page.Floor {
		t.Fatal(err)
	}
	req.NativeCursor = page.NextCursor
	page, _, err = paginateEndedNativeHistory(f.meta, req, f.rows)
	if err != nil || len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), "row2:") {
		t.Fatalf("ended cursor: %+v %v", page, err)
	}
}

func TestNativeHistoryAnchorsUseFullSnapshotShapes(t *testing.T) {
	f := newNativePageReadFake(t)
	id := fmt.Sprintf("msg_%032x", 2)
	part := func(kind, tool string, state any) map[string]any {
		return map[string]any{"id": "prt_" + kind, "messageID": id, "sessionID": f.meta.ConversationID, "type": "tool", "tool": tool, "state": state}
	}
	row, _ := json.Marshal(map[string]any{"info": map[string]any{"id": id, "sessionID": f.meta.ConversationID, "parentID": "msg_parent", "role": "assistant", "time": map[string]int64{"created": 3, "completed": 4}, "finish": "stop"}, "parts": []any{
		part("write", "write", map[string]any{"status": "completed", "input": map[string]any{"filePath": "/work/file"}, "metadata": map[string]any{"filepath": "/work/file", "exists": false}, "output": "done"}),
		part("plan", "todowrite", map[string]any{"status": "completed", "input": map[string]any{}, "metadata": map[string]any{"todos": []any{map[string]any{"content": "one", "status": "completed"}}}, "output": "done"}),
	}})
	scope, _ := nativeProjectionScope(f.meta)
	shaped, _, err := shapeNativeHistoryRows(f.meta, scope, []json.RawMessage{row})
	if err != nil || len(shaped) != 4 {
		t.Fatalf("tool/file/plan shapes: %d %v", len(shaped), err)
	}
	f.rows = []json.RawMessage{f.row(1, 1, 1), row, f.row(3, 1, 1)}
	for _, raw := range shaped {
		var item struct {
			ID   string `json:"item_id"`
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(raw, &item)
		page, _, err := paginateNativeHistory(context.Background(), f.meta, schema.InteractionHistoryReq{Native: true, Limit: 1, BeforeItem: item.ID}, f.read)
		if err != nil || len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), "row1:") {
			t.Fatalf("%s anchor: %+v %v", item.Kind, page, err)
		}
	}
	var user map[string]any
	_ = json.Unmarshal(f.row(2, 1, 1), &user)
	user["info"].(map[string]any)["role"] = "user"
	row, _ = json.Marshal(user)
	f.rows[1] = row
	page, _, err := paginateNativeHistory(context.Background(), f.meta, schema.InteractionHistoryReq{Native: true, Limit: 1, BeforeItem: nativeProjectionID(scope, "input_outcome", id)}, f.read)
	if err != nil || len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), "row1:") {
		t.Fatalf("outcome anchor: %+v %v", page, err)
	}
}

func TestNativeHistoryPlanAnchorUsesLatestSnapshot(t *testing.T) {
	f := newNativePageReadFake(t)
	for n := 1; n <= 5; n++ {
		raw := f.row(n, 1, 1)
		if n == 2 || n == 4 {
			var row map[string]any
			_ = json.Unmarshal(raw, &row)
			id := row["info"].(map[string]any)["id"]
			row["parts"] = []any{map[string]any{"id": fmt.Sprintf("prt_plan%d", n), "messageID": id, "sessionID": f.meta.ConversationID, "type": "tool", "tool": "todowrite", "state": map[string]any{"status": "completed", "input": map[string]any{}, "output": "done", "metadata": map[string]any{"todos": []any{map[string]any{"content": fmt.Sprint(n), "status": "completed"}}}}}}
			raw, _ = json.Marshal(row)
		}
		f.rows = append(f.rows, raw)
	}
	scope, _ := nativeProjectionScope(f.meta)
	req := schema.InteractionHistoryReq{Native: true, Limit: 1, BeforeItem: nativeProjectionID(scope, "plan_snapshot", f.meta.ConversationID+":plan:"+f.meta.ConversationID)}
	page, _, err := paginateNativeHistory(context.Background(), f.meta, req, f.read)
	if err != nil || len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), "row3:") {
		t.Fatalf("repeated plan anchor skipped rows before latest snapshot: %s %v", page.Items, err)
	}
}
