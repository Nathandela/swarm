package skeleton

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestOpenCodeDefaultVariantReceiptAndSupervision(t *testing.T) {
	for _, tc := range []struct {
		name, retained, native string
		match                  bool
	}{
		{"omitted native default", "default", "", true},
		{"omitted receipt default", "", "default", true},
		{"explicit default", "default", "default", true},
		{"explicit max", "max", "max", true},
		{"different variant", "max", "high", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOpenCodeSendFixture(t, func(f *openCodeSendFixture) {
				f.promptReceipt = func(_ opencodebridge.DispatchRequest, r opencodebridge.Receipt) opencodebridge.Receipt {
					r.Variant = tc.retained
					var row map[string]any
					if err := json.Unmarshal(f.nativeRow.Load().(json.RawMessage), &row); err != nil {
						t.Error(err)
						return r
					}
					model := row["info"].(map[string]any)["model"].(map[string]any)
					delete(model, "variant")
					if tc.native != "" {
						model["variant"] = tc.native
					}
					raw, _ := json.Marshal(row)
					f.nativeRow.Store(json.RawMessage(raw))
					return r
				}
			})
			id, err := protocol.NewSemanticOperationID()
			if err != nil {
				t.Fatal(err)
			}
			if err := f.client.SemanticSend(id, f.request); err != nil {
				t.Fatal(err)
			}
			record, _ := f.d.core.ComposerOperation(id)
			var intent openCodeSendIntent
			if err := json.Unmarshal(record.Request, &intent); err != nil {
				t.Fatal(err)
			}
			observed, err := qualifyOpenCodeAdmission(id, intent, f.nativeRow.Load().(json.RawMessage))
			if err != nil {
				t.Fatal(err)
			}
			if err := f.d.recordOpenCodeAdmission(id, intent, observed); (err == nil) != tc.match {
				t.Errorf("native variant %q vs retained %q: admission conflict=%v", tc.native, tc.retained, err)
			}
			input := OpenCodeInput{MessageID: intent.MessageID, Text: f.request.Text, PlainTextInput: true, Classified: true, At: intent.NotBeforeMS + 1, Outcome: OpenCodeOutcomeCompleted, OutcomeSource: OpenCodeOutcomeSourceImported, OutcomeAt: intent.NotBeforeMS + 2, Agent: observed.Agent, ProviderID: observed.Model.ProviderID, ModelID: observed.Model.ModelID, Variant: observed.Variant}
			f.d.openCode.mu.Lock()
			f.d.openCode.sessions[f.binding.WorkerID].view = OpenCodeView{Root: f.identity.RootID, Generation: f.identity.BackendGeneration, Epoch: 1, BusyKnown: true, CurrentScopeKnown: true, Inputs: []OpenCodeInput{input}}
			f.d.openCode.mu.Unlock()
			meta, _ := f.d.core.Get(f.binding.WorkerID)
			if event := f.d.nativeSupervisionState(meta); event.Owned != tc.match {
				t.Errorf("native variant %q vs retained %q: supervision ownership=%v", tc.native, tc.retained, event.Owned)
			}
			if f.calls.Load() != 1 {
				t.Fatal("variant reconciliation dispatched another native input")
			}
		})
	}
}

func TestOpenCodeDefaultVariantEventsPreserveFullInputProof(t *testing.T) {
	for _, variants := range [][2]string{{"", "default"}, {"default", ""}} {
		s, epoch := newOCState(t, "ses_root")
		s.Apply(ocUserTuple("build", "model", variants[0]), epoch)
		s.Apply(ocUserTuple("build", "model", variants[1]), epoch)
		var event struct {
			Properties struct{ Info json.RawMessage } `json:"properties"`
		}
		if err := json.Unmarshal(ocUserTuple("build", "model", variants[0]), &event); err != nil {
			t.Fatal(err)
		}
		row, _ := json.Marshal(map[string]any{"info": event.Properties.Info, "parts": []any{map[string]any{"id": "prt_text", "sessionID": "ses_root", "messageID": "msg_user", "type": "text", "text": "task"}}})
		if err := s.ImportHistory([]json.RawMessage{row}, epoch); err != nil {
			t.Fatal(err)
		}
		if view := s.View(); view.Degraded || !ocInput(view, "msg_user").PlainTextInput || ocInput(view, "msg_user").Variant != variants[0] {
			t.Fatalf("equivalent native default representation lost original input proof: %+v", view)
		}
		s.Apply(ocUserTuple("build", "model", "high"), epoch)
		if !s.View().Degraded {
			t.Fatal("a different native variant retained default selection authority")
		}
	}
}
