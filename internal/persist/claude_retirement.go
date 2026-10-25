package persist

import (
	"os"

	"github.com/Nathandela/swarm/internal/logins"
)

// RetireClaudeReservation durably revokes one exact owned reservation before
// workspace or payload cleanup. A non-nil originalNoFork belongs only to the
// original launch stack, which can still positively exclude crossing its fork
// boundary; restart, owner Delete and replay paths must pass nil.
func (s *Store) RetireClaudeReservation(id string, originalNoFork func() error) (Meta, error) {
	if err := validateID(id); err != nil {
		return Meta{}, err
	}
	var retired Meta
	err := logins.WithClaudeReservationRetirement(s.root, id, originalNoFork, func(marker logins.ClaudeReservationRetirement) error {
		m, err := s.Load(id)
		if os.IsNotExist(err) && marker == (logins.ClaudeReservationRetirement{}) {
			return nil
		}
		if err != nil {
			return err
		}
		if marker == (logins.ClaudeReservationRetirement{}) {
			retired = m
			return nil
		}
		// A visible marker may follow a failed post-rename sync. Settle its
		// directory before cleanup without rewriting the exact authorization.
		if m.SchemaVersion == ClaudeSchemaVersion && m.ClaudeRetirement != nil && *m.ClaudeRetirement == marker {
			dir, err := s.sessionPath(id)
			if err != nil {
				return err
			}
			if err := syncMetadataDirectory(dir); err != nil {
				return err
			}
			retired = m
			return nil
		}
		m.ClaudeRetirement = &marker
		m.SchemaVersion = ClaudeSchemaVersion
		m, data, err := s.metadataBytes(m)
		if err != nil {
			return err
		}
		if err := s.writeMetadata(id, data, true); err != nil {
			return err
		}
		retired = m
		return nil
	})
	return retired, err
}
