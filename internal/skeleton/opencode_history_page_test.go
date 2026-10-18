package skeleton

import (
	"context"
	"encoding/json"
	"fmt"
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
