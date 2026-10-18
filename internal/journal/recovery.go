package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

var errNoLegacyRecovery = errors.New("no recoverable legacy interaction")

// recoverLegacyInteraction recognizes a torn interaction payload immediately
// followed by a complete record. Migration discards interactions anyway, but must
// retain the following control and its cursor. Never salvage arbitrary garbage,
// a torn control, or an unsupported schema by searching for a plausible suffix.
func recoverLegacyInteraction(line []byte) (Record, error) {
	recoveryErr := errNoLegacyRecovery
	marker := []byte(`{"schema_version":`)
	for offset := 1; offset < len(line); {
		next := bytes.Index(line[offset:], marker)
		if next < 0 {
			break
		}
		next += offset
		if prefix, ok := tornInteractionHeader(line[:next]); ok {
			record, err := DecodeRecord(line[next:])
			if err == nil && record.Cursor > prefix.Cursor && record.SessionID != "" && record.Type != "" {
				return record, nil
			}
			// A recognized torn header followed by an invalid record is not a
			// harmless EOF tail, especially when the suffix uses a future schema.
			if err != nil {
				if json.Valid(line[next:]) {
					recoveryErr = err
				}
			} else {
				recoveryErr = errors.New("invalid recovered cursor or identity")
			}
		}
		offset = next + len(marker)
	}
	return Record{}, recoveryErr
}

func tornInteractionHeader(prefix []byte) (Record, bool) {
	decoder := json.NewDecoder(bytes.NewReader(prefix))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return Record{}, false
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return Record{}, false
		}
		key, ok := token.(string)
		if !ok {
			return Record{}, false
		}
		if _, duplicate := fields[key]; duplicate {
			return Record{}, false
		}
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			if key != "payload" || !errors.Is(err, io.ErrUnexpectedEOF) {
				return Record{}, false
			}
			header, err := json.Marshal(fields)
			if err != nil {
				return Record{}, false
			}
			var r Record
			if err := json.Unmarshal(header, &r); err != nil {
				return Record{}, false
			}
			_, versioned := fields["schema_version"]
			return r, versioned && r.SchemaVersion <= SchemaVersion && r.SchemaVersion >= 0 && r.Type == TypeInteraction && r.Cursor > 0 && r.SessionID != ""
		}
		fields[key] = value
	}
	return Record{}, false
}
