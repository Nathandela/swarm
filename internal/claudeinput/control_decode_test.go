package claudeinput

import (
	"encoding/json"
	"strings"
	"testing"
)

func durableControlFixture() ControlEnvelope {
	return ControlEnvelope{Event: NativeControlRuntime, RequiredRuntime: NativeControlRuntime, Version: 1, SessionID: "local", Token: strings.Repeat("a", 64), DescriptorGeneration: strings.Repeat("b", 32), ShimPID: 123, ShimStartTime: 456, Worker: Worker{NativeSessionID: "native", Generation: "guard", Version: "2.1.310"}, Sequence: 1, ReportHash: strings.Repeat("c", 64), Invocation: 1, NativeEvent: "turn.step", Phase: "entered", Context: json.RawMessage(`{"turn_id":"turn","index":0,"message_count":1,"origin":{"plugin":"engine","tier":"core"}}`), Data: json.RawMessage(`{}`)}
}
func TestNativeControlDurableDecoder(t *testing.T) {
	original := durableControlFixture()
	raw, _ := json.Marshal(original)
	if _, err := DecodeControlEnvelope(raw); err != nil {
		t.Fatal("valid stamped frame rejected", err)
	}
	for _, kind := range []string{"missing-tag", "duplicate", "alias", "null", "wrong-runtime", "unknown-version", "unknown-field", "shape"} {
		t.Run(kind, func(t *testing.T) {
			var fields map[string]any
			_ = json.Unmarshal(raw, &fields)
			switch kind {
			case "missing-tag":
				delete(fields, "event")
			case "alias":
				fields["Event"] = fields["event"]
				delete(fields, "event")
			case "null":
				fields["event"] = nil
			case "wrong-runtime":
				fields["required_runtime"] = "claude-chat"
			case "unknown-version":
				fields["version"] = 999
			case "unknown-field":
				fields["surprise"] = true
			case "shape":
				fields["data"] = map[string]any{"answer": "body"}
			}
			mutated, _ := json.Marshal(fields)
			if kind == "duplicate" {
				mutated = append([]byte(`{"event":"Stop",`), raw[1:]...)
			}
			if _, err := DecodeControlEnvelope(mutated); err == nil {
				t.Fatal("invalid durable frame accepted")
			}
		})
	}
}
func TestNativeControlFingerprintCoversSanitizedEnvelope(t *testing.T) {
	original := durableControlFixture()
	expected := ControlEnvelopeFingerprint(original)
	changed := original
	changed.Token = strings.Repeat("f", 64)
	if ControlEnvelopeFingerprint(changed) != expected {
		t.Fatal("auth token entered fingerprint")
	}
	for _, mutate := range []func(*ControlEnvelope){
		func(e *ControlEnvelope) { e.SessionID = "other" }, func(e *ControlEnvelope) { e.DescriptorGeneration = strings.Repeat("e", 32) }, func(e *ControlEnvelope) { e.ShimPID++ }, func(e *ControlEnvelope) { e.ShimStartTime++ }, func(e *ControlEnvelope) { e.Worker.Generation = "other" }, func(e *ControlEnvelope) { e.Worker.NativeSessionID = "other" }, func(e *ControlEnvelope) { e.Worker.Version = "other" }, func(e *ControlEnvelope) { e.Sequence++ }, func(e *ControlEnvelope) { e.Invocation++ }, func(e *ControlEnvelope) { e.NativeEvent = "turn.complete" }, func(e *ControlEnvelope) { e.Phase = "threw" }, func(e *ControlEnvelope) { e.Context = json.RawMessage(`{"turn_id":"foreign"}`) }, func(e *ControlEnvelope) { e.Data = json.RawMessage(`{"reason":"aborted"}`) }, func(e *ControlEnvelope) { e.ReportHash = strings.Repeat("d", 64) }, func(e *ControlEnvelope) { e.Version++ }, func(e *ControlEnvelope) { e.RequiredRuntime = "other" }, func(e *ControlEnvelope) { e.Event = "other" },
	} {
		changed := original
		mutate(&changed)
		if ControlEnvelopeFingerprint(changed) == expected {
			t.Fatal("changed sanitized envelope aliased original receipt")
		}
	}
	reordered := original
	reordered.Context = json.RawMessage(`{"origin":{"tier":"core","plugin":"engine"},"message_count":1,"index":0,"turn_id":"turn"}`)
	if ControlEnvelopeFingerprint(reordered) != expected {
		t.Fatal("object key order changed semantic fingerprint")
	}
}
func TestNativeControlExplicitOrdinaryLane(t *testing.T) {
	raw := []byte(`{"session_id":"local","token":"token","sequence":1,"event":"PreToolUse","payload":{"nativeCamel":"preserved"},"raw":{"toolUseId":"native","optional":null,"event":"claude-native-control"}}`)
	if err := ValidateControlOrdinaryHook(raw); err != nil {
		t.Fatal("normal native hook body was interpreted as control protocol", err)
	}
	if ControlShaped(raw) {
		t.Fatal("nested native body changed unbound lane")
	}
	for _, body := range []string{`{"session_id":"local","token":"token","sequence":1,"event":"Stop","event":"PreToolUse"}`, `{"session_id":"local","token":"token","sequence":1,"event":"Stop","worker":{}}`, `{"session_id":"local","token":"token","sequence":1,"event":"Stop","Event":"Stop"}`, `{"session_id":"local","token":"token","sequence":1,"event":null}`, `{"session_id":"local","token":"token","sequence":1,"event":"unmapped"}`} {
		if ValidateControlOrdinaryHook([]byte(body)) == nil {
			t.Fatal("ambiguous/unpermitted callback accepted", body)
		}
	}
	if !ControlShaped([]byte(`{"event":"claude-native-control","event":"Stop"}`)) {
		t.Fatal("duplicate discriminator smuggled through best effort")
	}
}
