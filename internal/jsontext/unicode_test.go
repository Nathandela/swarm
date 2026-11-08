package jsontext

import (
	"strings"
	"testing"
)

func TestValidateUnicodeWithoutRepairOrSchemaRules(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{"text":"\ud800"}`), []byte(`{"text":"\udc00"}`), []byte(`{"text":"\ud800\ud800"}`), []byte(`{"text":"\ud800\\udc00"}`), {255}} {
		if err := ValidateUnicode(raw); err == nil {
			t.Fatalf("malformed Unicode accepted: %q", raw)
		}
	}
	for _, raw := range []string{`{"text":"\ud83d\ude80"}`, `{"text":"\\ud800"}`, `{"TEXT":"🚀","TEXT":"same schema-agnostic keys"}`} {
		if err := ValidateUnicode([]byte(raw)); err != nil {
			t.Fatalf("valid Unicode rejected: %v", err)
		}
	}
	err := ValidateUnicode([]byte(`{"secret-sentinel":"\ud800"}`))
	if err == nil || strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatalf("unsafe validation error: %v", err)
	}
}
