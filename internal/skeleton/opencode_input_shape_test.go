package skeleton

import (
	"encoding/json"
	"testing"
)

func TestOpenCodeObservedAdmissionRequiresCompleteSingleTextShape(t *testing.T) {
	intent, original := nativeReceiptFixture()
	for _, extra := range []string{"file", "text", "agent"} {
		t.Run(extra, func(t *testing.T) {
			var row map[string]any
			if err := json.Unmarshal(original, &row); err != nil {
				t.Fatal(err)
			}
			row["parts"] = append(row["parts"].([]any), map[string]any{"id": "prt_extra", "sessionID": intent.Identity.RootID, "messageID": intent.MessageID, "type": extra, "text": ""})
			raw, _ := json.Marshal(row)
			s, _ := NewOpenCodeState(intent.Identity.RootID, intent.Identity.BackendGeneration)
			epoch := s.BeginEpoch()
			if err := s.ImportHistory([]json.RawMessage{raw}, epoch); err != nil {
				t.Fatal(err)
			}
			if _, err := qualifyObservedOpenCodeAdmission("semantic:owned", intent, s.View().Inputs[0]); err == nil {
				t.Fatal("extra genuine context minted observed admission")
			}
		})
	}
	s, _ := NewOpenCodeState(intent.Identity.RootID, intent.Identity.BackendGeneration)
	epoch := s.BeginEpoch()
	var row struct {
		Info  json.RawMessage   `json:"info"`
		Parts []json.RawMessage `json:"parts"`
	}
	if err := json.Unmarshal(original, &row); err != nil {
		t.Fatal(err)
	}
	event, _ := json.Marshal(map[string]any{"type": "message.updated", "properties": map[string]any{"info": row.Info}})
	s.Apply(event, epoch)
	event, _ = json.Marshal(map[string]any{"type": "message.part.updated", "properties": map[string]any{"part": row.Parts[0]}})
	s.Apply(event, epoch)
	if _, err := qualifyObservedOpenCodeAdmission("semantic:owned", intent, s.View().Inputs[0]); err == nil {
		t.Fatal("partial events proved absence of additional genuine context")
	}
	if err := s.ImportHistory([]json.RawMessage{original}, epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := qualifyObservedOpenCodeAdmission("semantic:owned", intent, s.View().Inputs[0]); err != nil {
		t.Fatalf("complete exact row did not qualify: %v", err)
	}
	s.Apply(event, epoch)
	if _, err := qualifyObservedOpenCodeAdmission("semantic:owned", intent, s.View().Inputs[0]); err == nil {
		t.Fatal("unsequenced part event retained full-row admission proof")
	}
}
