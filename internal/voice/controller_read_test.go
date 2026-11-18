package voice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestControllerCodexAndClaudeReadRetainedOutputWithoutClaimingFullNativeHistory(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			c, _, _, _ := provenControllerFixture(t)
			worker := &controllerWorkerFixture{rows: []protocol.SessionView{{ID: "machine/worker", Agent: provider, Capabilities: &schema.SessionCapabilities{SessionInstance: "instance"}}}}
			c.workers = worker
			args, _ := json.Marshal(ReadInput{SessionInput: SessionInput{"machine/worker", "instance"}})
			raw, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_read", Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			var page WorkerOutputPage
			if err = json.Unmarshal(raw, &page); err != nil {
				t.Fatal(err)
			}
			if page.Source != "retained" || page.Complete || !page.Floor || len(page.Items) != 1 || worker.nativeReads != 0 || worker.retainedReads != 1 || !strings.Contains(page.Scope, "retained") {
				t.Fatal("retained output was lost or relabeled as full native history")
			}
		})
	}
}
func TestControllerReadCursorsCannotCrossSourceOrWorkerInstance(t *testing.T) {
	c, _, _, _ := provenControllerFixture(t)
	worker := &controllerWorkerFixture{rows: []protocol.SessionView{{ID: "machine/worker", Agent: "opencode", Capabilities: &schema.SessionCapabilities{SessionInstance: "instance"}}}}
	c.workers = worker
	input := ReadInput{SessionInput: SessionInput{"machine/worker", "instance"}, Source: "native"}
	args, _ := json.Marshal(input)
	raw, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_read", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var page WorkerOutputPage
	if err = json.Unmarshal(raw, &page); err != nil || page.Source != "native" || page.NextCursor == "" || worker.retainedReads != 0 {
		t.Fatal("native source changed", err)
	}
	input.Cursor = page.NextCursor
	input.Source = "retained"
	args, _ = json.Marshal(input)
	if _, err = c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_read", Arguments: args}); !errors.Is(err, ErrStale) {
		t.Fatal("native cursor crossed into retained source", err)
	}
	input.Source = "native"
	input.SessionInstance = "replacement"
	if _, err = decodeWorkerReadCursor(input.Cursor, input); !errors.Is(err, ErrStale) {
		t.Fatal("cursor crossed worker instance")
	}
	worker.nativeErr = errors.New("native history unavailable")
	input.SessionInstance = "instance"
	input.Cursor = ""
	args, _ = json.Marshal(input)
	if _, err = c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_read", Arguments: args}); err == nil || worker.retainedReads != 0 {
		t.Fatal("native source silently fell back to retained output")
	}
}

func TestRetainedWorkerOutputFoldsWholeStreamingItemsAndKeepsGapBoundaries(t *testing.T) {
	records := make([]protocol.JournalRecord, 600)
	for i := range records {
		item, _ := json.Marshal(map[string]any{"v": 1, "item_id": "answer", "kind": "agent_message", "text": strings.Repeat("x", 80), "status": "in_progress", "turn_id": "turn", "ts": "2026-11-08T00:00:00Z"})
		records[i] = protocol.JournalRecord{Cursor: uint64(i + 1), Type: "interaction", Item: item}
	}
	terminal := json.RawMessage(`{"v":1,"item_id":"answer","kind":"agent_message","text":" done","status":"completed"}`)
	records = append(records, protocol.JournalRecord{Cursor: 601, Type: "interaction", Item: terminal})
	items, before, err := retainedWorkerItems(records)
	if err != nil {
		t.Fatal(err)
	}
	var folded struct{ Text, Status string }
	if len(items) != 1 || before != "answer" || json.Unmarshal(items[0], &folded) != nil || folded.Text != strings.Repeat("x", 48000)+" done" || folded.Status != "completed" {
		t.Fatal("whole streamed output was cut or not folded")
	}
	records = []protocol.JournalRecord{{Type: "interaction", Item: json.RawMessage(`{"v":1,"item_id":"same","kind":"agent_message","text":"before"}`)}, {Type: "structured_gap", Item: json.RawMessage(`{"kind":"structured_gap","reason":"history unavailable"}`)}, {Type: "interaction", Item: json.RawMessage(`{"v":1,"item_id":"same","kind":"agent_message","text":"after"}`)}}
	items, _, err = retainedWorkerItems(records)
	if err != nil || len(items) != 3 {
		t.Fatal("gap was silently collapsed", err)
	}
	var one, two struct{ Text string }
	_ = json.Unmarshal(items[0], &one)
	_ = json.Unmarshal(items[2], &two)
	if one.Text != "before" || two.Text != "after" {
		t.Fatal("text was concatenated across unavailable history")
	}
}
