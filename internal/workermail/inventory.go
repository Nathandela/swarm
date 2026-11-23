package workermail

import (
	"context"
	"encoding/json"
	"errors"

	bolt "go.etcd.io/bbolt"
)

// PendingReceipt is the checkout-relevant metadata of an accepted worker note.
type PendingReceipt struct {
	Binding
	ID, Kind, State string
}

// InspectPending reads the pending index without acknowledging or importing notes.
func InspectPending(ctx context.Context, dir string) ([]PendingReceipt, error) {
	var records []PendingReceipt
	err := transact(dir, false, func(tx *bolt.Tx) error {
		index, notes := tx.Bucket([]byte("pending")), tx.Bucket([]byte("notes"))
		if index == nil || notes == nil {
			return errors.New("worker mailbox: missing receipt index or note records")
		}
		return index.ForEach(func(_, key []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(records) == 4096 {
				return errors.New("worker mailbox: inventory limit reached")
			}
			var rec PendingReceipt
			if err := json.Unmarshal(notes.Get(key), &rec); err != nil {
				return err
			}
			records = append(records, rec)
			return nil
		})
	})
	return records, err
}
