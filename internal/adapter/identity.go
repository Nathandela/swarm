package adapter

// Conversation identity is intentionally an optional extension of Adapter. Most
// providers have no authenticated structured event carrying their native id, and
// absence must remain a supported state rather than widening the frozen Adapter
// method set.

import (
	"bytes"
	"encoding/json"
	"io"
)

const maxConversationIdentityEventBytes = 1 << 20

// ConversationIdentitySource extracts a provider-owned conversation id from one
// authenticated structured event. Implementations are pure, total and
// deterministic; ok implies a canonical non-empty id.
type ConversationIdentitySource interface {
	ConversationIDFromEvent(HookPayload) (string, bool)
}

// ConversationIDValidator is an optional adapter extension for providers whose
// native conversation IDs have a defensible syntax. It lets generic resume
// paths reject corrupt persisted metadata without adding provider switches or
// widening the frozen Adapter interface. Implementations are pure, total and
// deterministic.
type ConversationIDValidator interface {
	IsValidConversationID(string) bool
}

// AsConversationIdentitySource reports whether a implements the optional
// authenticated-event identity extension.
func AsConversationIdentitySource(a Adapter) (ConversationIdentitySource, bool) {
	src, ok := a.(ConversationIdentitySource)
	return src, ok
}

// AsConversationIDValidator reports whether a implements the optional native-ID
// validation extension.
func AsConversationIDValidator(a Adapter) (ConversationIDValidator, bool) {
	validator, ok := a.(ConversationIDValidator)
	return validator, ok
}

// AcceptsConversationID applies an adapter's native-ID validator when it has
// one. Adapters without this optional extension retain their historical opaque-
// ID behavior.
func AcceptsConversationID(a Adapter, id string) bool {
	validator, ok := AsConversationIDValidator(a)
	return !ok || validator.IsValidConversationID(id)
}

// IsCanonicalConversationID accepts the canonical lowercase UUID spelling used
// by both Codex and Claude. Provider ids are opaque beyond this syntax: no UUID
// version is imposed because currently observed Codex ids include UUIDv7 while
// Claude ids include UUIDv4.
func IsCanonicalConversationID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	for i := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// CanonicalTopLevelConversationID reads exactly one named string field from one
// top-level JSON object. It rejects duplicate keys (including duplicates with the
// same value), trailing JSON and oversized bodies so an adapter cannot guess an
// identity out of ambiguous or unbounded input.
func CanonicalTopLevelConversationID(raw json.RawMessage, key string) (string, bool) {
	if len(raw) == 0 || len(raw) > maxConversationIdentityEventBytes || key == "" {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return "", false
	}
	seen := make(map[string]struct{})
	var found string
	for dec.More() {
		tok, err = dec.Token()
		if err != nil {
			return "", false
		}
		name, ok := tok.(string)
		if !ok {
			return "", false
		}
		if _, duplicate := seen[name]; duplicate {
			return "", false
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return "", false
		}
		if name == key {
			if err := json.Unmarshal(value, &found); err != nil || !IsCanonicalConversationID(found) {
				return "", false
			}
		}
	}
	if tok, err = dec.Token(); err != nil || tok != json.Delim('}') {
		return "", false
	}
	if tok, err = dec.Token(); err != io.EOF {
		_ = tok
		return "", false
	}
	return found, found != ""
}
