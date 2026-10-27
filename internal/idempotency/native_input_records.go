package idempotency

import (
	"encoding/json"
	"errors"
)

func (s *Store) NativeInputRecords(session string, limit int) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	for _, rec := range s.records {
		if rec.SessionID != session || (rec.Action != "semantic_send" && rec.Action != "composer_send") {
			continue
		}
		if len(out) >= limit {
			return nil, errors.New("idempotency: native input inventory exceeds bound")
		}
		rec.Request = append(json.RawMessage(nil), rec.Request...)
		rec.Evidence, rec.Outcome = nil, nil
		out = append(out, rec)
	}
	return out, nil
}
