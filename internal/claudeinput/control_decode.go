package claudeinput

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// DecodeControlEnvelope validates the shim-stamped durable lane, not a callback.
func DecodeControlEnvelope(raw []byte) (ControlEnvelope, error) {
	var envelope ControlEnvelope
	if len(raw) > 64<<10 || strictControlJSON(raw, &envelope) != nil || envelope.Event != NativeControlRuntime || envelope.RequiredRuntime != NativeControlRuntime || envelope.Version != 1 || !controlString(mustControlJSON(envelope.SessionID), 128) || len(envelope.Token) != 64 || !controlHex(envelope.DescriptorGeneration, 16) || !controlHex(envelope.ReportHash, 32) || envelope.ShimPID <= 0 || envelope.ShimStartTime <= 0 || envelope.Worker.Version != "2.1.310" || !controlString(mustControlJSON(envelope.Worker.NativeSessionID), 128) || !controlString(mustControlJSON(envelope.Worker.Generation), 128) || !validControlReport(controlReport{Generation: envelope.DescriptorGeneration, Worker: envelope.Worker, Sequence: envelope.Sequence, Invocation: envelope.Invocation, Event: envelope.NativeEvent, Phase: envelope.Phase, Context: envelope.Context, Data: envelope.Data}) {
		return ControlEnvelope{}, errors.New("claude control envelope invalid")
	}
	return envelope, nil
}

func mustControlJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
func controlHex(value string, bytes int) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == bytes && hex.EncodeToString(raw) == value
}

// ControlShaped is a denial check for the unbound best-effort socket, never a
// classifier permitting a bound receipt to skip validation.
func ControlShaped(raw []byte) bool {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return false
	}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return false
		}
		name, ok := key.(string)
		if !ok {
			return false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return false
		}
		lower := strings.ToLower(name)
		if strings.Contains(" required_runtime descriptor_generation shim_pid shim_start_time worker report_hash invocation native_event phase context data ", " "+lower+" ") {
			return true
		}
		if lower == "event" {
			var event string
			if json.Unmarshal(value, &event) == nil && strings.EqualFold(event, NativeControlRuntime) {
				return true
			}
		}
	}
	return false
}

// ControlEnvelopeFingerprint covers all sanitized custody fields, never auth.
// The opaque producer report_hash alone cannot detect a changed context or result.
func ControlEnvelopeFingerprint(envelope ControlEnvelope) string {
	envelope.Token = ""
	var context, data any
	_ = json.Unmarshal(envelope.Context, &context)
	_ = json.Unmarshal(envelope.Data, &data)
	envelope.Context = mustControlJSON(context)
	envelope.Data = mustControlJSON(data)
	raw, _ := json.Marshal(envelope)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// ValidateControlOrdinaryHook admits only the explicit classic Claude lane. Raw
// native bodies are opaque; top-level reserved control fields are never callbacks.
func ValidateControlOrdinaryHook(raw []byte) error {
	if len(raw) > 4<<20 || !utf8.Valid(raw) {
		return errors.New("claude ordinary envelope exceeds bound")
	}
	var wire struct {
		SessionID string          `json:"session_id"`
		Token     string          `json:"token"`
		Sequence  uint64          `json:"sequence"`
		Event     string          `json:"event"`
		Payload   json.RawMessage `json:"payload"`
		Raw       json.RawMessage `json:"raw,omitempty"`
	}
	// Classic raw hook bodies may contain native camelCase/null fields. Only their
	// outer callback envelope is protocol, so validate it separately.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errors.New("claude ordinary envelope invalid")
	}
	outer := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		outer[key] = value
	}
	delete(outer, "raw")
	delete(outer, "payload")
	outerRaw, _ := json.Marshal(outer)
	// The original outer key spelling/duplicates must be checked before map decoding.
	if !strictOrdinaryOuter(raw) || strictControlJSON(outerRaw, &wire) != nil || wire.SessionID == "" || wire.Token == "" || wire.Sequence == 0 || !strings.Contains(" SessionStart UserPromptSubmit PreToolUse PostToolUse Notification Stop SubagentStart SubagentStop PermissionRequest StopFailure SwarmUsage ", " "+wire.Event+" ") {
		return errors.New("claude ordinary callback invalid")
	}
	if payload, ok := fields["payload"]; ok {
		var values map[string]string
		if json.Unmarshal(payload, &values) != nil {
			return errors.New("claude ordinary payload invalid")
		}
	}
	return nil
}

// Native raw/payload content is not the envelope discriminator. The map decoder
// must not erase ambiguous outer keys before they reach this check.
func strictOrdinaryOuter(raw []byte) bool {
	fields := map[string]bool{}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return false
		}
		name, ok := key.(string)
		if !ok || name != strings.ToLower(name) || fields[name] || !strings.Contains(" session_id token sequence event payload raw ", " "+name+" ") {
			return false
		}
		fields[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
		if name != "raw" && name != "payload" && strictControlJSON(value, new(any)) != nil {
			return false
		}
	}
	if _, err = decoder.Token(); err != nil {
		return false
	}
	var tail any
	return decoder.Decode(&tail) == io.EOF
}

// ValidateControlInvocation validates durable metadata without a credential field.
func ValidateControlInvocation(event string, sequence uint64, context, data json.RawMessage) error {
	if strictControlJSON(context, new(any)) != nil || strictControlJSON(data, new(any)) != nil || !validControlReport(controlReport{Event: event, Phase: "entered", Sequence: sequence, Invocation: sequence, Context: context, Data: data}) {
		return errors.New("claude invocation metadata invalid")
	}
	return nil
}

// ControlTerminalMatches joins the closure's exact entry witness, not a later actor turn.
func ControlTerminalMatches(event string, context, data json.RawMessage, terminal ControlEnvelope) bool {
	var before, after any
	if json.Unmarshal(context, &before) != nil || json.Unmarshal(terminal.Context, &after) != nil {
		return false
	}
	if string(mustControlJSON(before)) != string(mustControlJSON(after)) {
		return false
	}
	var beforeData, afterData any
	if json.Unmarshal(data, &beforeData) != nil || json.Unmarshal(terminal.Data, &afterData) != nil {
		return false
	}
	return event == terminal.NativeEvent && sameControlWitness(controlReport{Event: event, Data: mustControlJSON(beforeData)}, controlReport{Event: terminal.NativeEvent, Phase: terminal.Phase, Data: mustControlJSON(afterData)})
}
