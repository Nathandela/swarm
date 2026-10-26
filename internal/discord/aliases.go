package discord

import (
	"encoding/json"
	bolt "go.etcd.io/bbolt"
)

func canonicalTarget(tx *bolt.Tx, id string) (string, error) {
	aliases := tx.Bucket([]byte("aliases"))
	for range MaxRequests + 1 {
		raw := aliases.Get([]byte(id))
		if raw == nil {
			return id, nil
		}
		var next string
		if json.Unmarshal(raw, &next) != nil || next == "" || next == id {
			return "", ErrConflict
		}
		id = next
	}
	return "", ErrConflict
}
func (s *Store) ResolveTargetID(id string) (resolved string, err error) {
	err = s.db.View(func(tx *bolt.Tx) error { var e error; resolved, e = canonicalTarget(tx, id); return e })
	return
}
func followWorkerTargets(store *Store, rows []Worker) error {
	for _, row := range rows {
		for _, old := range row.Supersedes {
			if e := store.FollowTarget(Target{ID: old, Instance: row.Instance}, row.Target); e != nil {
				return e
			}
		}
	}
	return nil
}
