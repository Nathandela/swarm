package skeleton

import (
	"bytes"
	"encoding/json"
	"io"
)

// decodeStrictObject preserves raw member values while rejecting duplicate keys
// and trailing JSON. Callers recursively decode critical nested objects with the
// same helper, so provider identity cannot depend on encoding/json's last-key-wins
// behavior.
func decodeStrictObject(raw []byte) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, false
	}
	out := make(map[string]json.RawMessage)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, false
		}
		if _, duplicate := out[key]; duplicate {
			return nil, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		out[key] = value
	}
	if tok, err = dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	if tok, err = dec.Token(); err != io.EOF {
		_ = tok
		return nil, false
	}
	return out, true
}

func strictJSONString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}
