package skeleton

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func TestOpenCodeCurrentOutcomesDoNotFloodJournalUnderTranscriptChurn(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	now := time.Now()
	outcomes := 0
	d := &Daemon{core: f.d.core, items: remotegw.NewItemAdmission(remotegw.ItemAdmissionConfig{
		Window: time.Nanosecond,
		Now:    func() time.Time { now = now.Add(time.Millisecond); return now },
		Append: func(_ string, raw json.RawMessage) error {
			var item struct {
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			if item.Kind == "input_outcome" {
				outcomes++
			}
			return nil
		},
	})}
	meta, _ := f.d.core.Get(f.binding.WorkerID)
	projection, err := newOpenCodeTranscript(d, meta, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	refreshOutcomes := func() {
		for i := range 128 {
			id := fmt.Sprintf("msg_settled_%d", i)
			if err := projection.offer("input_outcome", id, id, "completed", time.UnixMilli(int64(1000+i)), "live", map[string]any{"outcome": "completed", "native_input_id": id}, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	refreshOutcomes()
	for i := range 129 {
		if err := projection.offer("message_snapshot", fmt.Sprintf("msg_new_%d", i), "", adapter.StatusCompleted, time.UnixMilli(int64(3000+i)), "live", map[string]any{"text": "new native prose", "role": "assistant"}, false); err != nil {
			t.Fatal(err)
		}
		refreshOutcomes()
	}
	if outcomes != 128 {
		t.Fatalf("unchanged current outcomes were republished under bounded transcript churn: %d records, want128", outcomes)
	}
}
