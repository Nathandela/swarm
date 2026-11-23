package workermail

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	bolt "go.etcd.io/bbolt"
)

// PendingReceipt is the checkout-relevant metadata of an accepted worker note.
type PendingReceipt struct {
	Binding
	ID, Kind, State string
}

// InspectPending reads the pending index without acknowledging or importing notes.
func InspectPending(ctx context.Context, dir string) ([]PendingReceipt, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("worker mailbox needs a supervisor state directory")
	}
	db, err := bolt.Open(filepath.Join(dir, "dispatcher-mail.db"), 0600, &bolt.Options{Timeout: time.Second, ReadOnly: true, OpenFile: openMailboxForRead})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	var records []PendingReceipt
	err = db.View(func(tx *bolt.Tx) error {
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

func openMailboxForRead(path string, _ int, _ os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ok || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() {
			err = errors.New("worker mailbox: expected a private regular file")
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
