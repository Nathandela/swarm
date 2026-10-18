package remotegw

import (
	"encoding/json"
	"testing"
)

func TestNativeAdmissionBoundsDistinctItemsButReplacesHeldSnapshots(t *testing.T) {
	queue, log, clock := newAdmissionHarness()
	offerItem(t, queue, "worker", itemJSON(t, "seed", "session_status", nil))
	first := nativeAdmissionItem(t, "message", "message_snapshot", map[string]any{"text": "first", "old_field": "must disappear"})
	if err := queue.OfferBounded("worker", first, 2); err != nil {
		t.Fatal(err)
	}
	if err := queue.OfferBounded("worker", nativeAdmissionItem(t, "tool", "tool_snapshot", nil), 2); err != nil {
		t.Fatal(err)
	}
	if err := queue.OfferBounded("worker", nativeAdmissionItem(t, "overflow", "tool_snapshot", nil), 2); err == nil {
		t.Fatal("native distinct item queue grew beyond its budget")
	}
	if err := queue.OfferBounded("worker", nativeAdmissionItem(t, "message", "message_snapshot", map[string]any{"text": "replacement"}), 2); err != nil {
		t.Fatal("existing snapshot could not coalesce at capacity")
	}
	if queue.Pending() != 2 {
		t.Fatal("refused native item changed queue custody")
	}
	for i := 0; i < 2; i++ {
		clock.Advance(DefaultAppendWindow)
		if err := queue.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range log.all() {
		var item map[string]any
		if err := json.Unmarshal(record.item, &item); err != nil {
			t.Fatal(err)
		}
		if item["item_id"] == "message" {
			if item["text"] != "replacement" || item["old_field"] != nil {
				t.Fatal("native full snapshot was concatenated or merged with stale fields")
			}
		}
	}
}

func TestNativeRequestsTakePriorityOverSnapshotProse(t *testing.T) {
	queue, log, clock := newAdmissionHarness()
	offerItem(t, queue, "worker", itemJSON(t, "seed", "session_status", nil))
	offerItem(t, queue, "worker", nativeAdmissionItem(t, "text", "message_snapshot", nil))
	offerItem(t, queue, "worker", nativeAdmissionItem(t, "request", "question_request", nil))
	clock.Advance(DefaultAppendWindow)
	if err := queue.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := decodeItem(t, log.all()[1].item).Kind; got != "question_request" {
		t.Fatalf("pending native request waited behind prose: %s", got)
	}
}

func nativeAdmissionItem(t *testing.T, id, kind string, fields map[string]any) json.RawMessage {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["v"] = 2
	return itemJSON(t, id, kind, fields)
}
