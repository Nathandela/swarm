package skeleton

import (
	"encoding/json"
	"strings"
	"testing"
)

func ocUserTuple(agent, model, variant string) []byte {
	selection := map[string]any{"providerID": "opencode", "modelID": model}
	if variant != "" {
		selection["variant"] = variant
	}
	raw, _ := json.Marshal(map[string]any{"type": "message.updated", "properties": map[string]any{"info": map[string]any{"id": "msg_user", "sessionID": "ses_root", "role": "user", "time": map[string]int64{"created": 1000}, "agent": agent, "model": selection}}})
	return raw
}

func TestOpenCodeInputRetainsExactNativeUserSelection(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	s.Apply(ocUserTuple("plan", "deepseek-v4.1-flash-free", "max"), e)
	s.Apply(ocUserTextPart("ses_root", "msg_user", "prt_user", "task"), e)
	in := ocInput(s.View(), "msg_user")
	if in.Agent != "plan" || in.ProviderID != "opencode" || in.ModelID != "deepseek-v4.1-flash-free" || in.Variant != "max" {
		t.Fatalf("native selection lost: %+v", in)
	}
	s.Apply(ocUserTuple("build", "glm-5.3-flash-free", "max"), e)
	if v := s.View(); !v.Degraded || ocInput(v, "msg_user").Agent != "plan" {
		t.Fatal("native input selection changed after admission")
	}
}

func TestOpenCodeInputImportDoesNotInventSelectionDefaults(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	var event struct {
		Properties struct {
			Info json.RawMessage `json:"info"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(ocUserTuple("plan", "deepseek-v4.1-flash-free", ""), &event); err != nil {
		t.Fatal(err)
	}
	row, _ := json.Marshal(map[string]any{"info": event.Properties.Info, "parts": []any{map[string]any{"id": "prt_user", "messageID": "msg_user", "sessionID": "ses_root", "type": "text", "text": "task"}}})
	if err := s.ImportHistory([]json.RawMessage{row}, e); err != nil {
		t.Fatal(err)
	}
	in := ocInput(s.View(), "msg_user")
	if in.Agent != "plan" || in.ModelID != "deepseek-v4.1-flash-free" || in.Variant != "" {
		t.Fatalf("history tuple invented or lost: %+v", in)
	}
	s.Apply(ocUserMessage("ses_root", "msg_unknown", 2000), e)
	if in := ocInput(s.View(), "msg_unknown"); in.Agent != "" || in.ModelID != "" || in.ProviderID != "" || in.Variant != "" {
		t.Fatal("absent selection received a default")
	}
}

func TestOpenCodeInputTextExcludesSyntheticContextFromOwnedBody(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	s.Apply(ocUserTuple("plan", "deepseek-v4.1-flash-free", "max"), e)
	for _, id := range []string{"prt_context1", "prt_context2", "prt_context3", "prt_context4"} {
		s.Apply(ocSyntheticTextPart("ses_root", "msg_user", id, strings.Repeat("x", maxOCPartText+1)), e)
	}
	s.Apply(ocUserTextPart("ses_root", "msg_user", "prt_user", "exact requested body"), e)
	in := ocInput(s.View(), "msg_user")
	if in.Text != "exact requested body" || in.TextTruncated || in.Synthetic {
		t.Fatalf("context consumed or changed owned text: %+v", in)
	}
	s.Apply(ocSyntheticTextPart("ses_root", "msg_user", "prt_later", "later context"), e)
	if in := ocInput(s.View(), "msg_user"); in.Text != "exact requested body" || in.TextTruncated {
		t.Fatal("later context changed exact owner text")
	}
	retained := 0
	for _, part := range s.inputs["msg_user"].parts {
		retained += len(part.text)
	}
	if retained > maxOCInputText {
		t.Fatal("genuine input priority exceeded retained text cap")
	}
}
