package discord

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	bolt "go.etcd.io/bbolt"
)

// InspectCheckoutStatus reads connector receipt metadata without initializing or
// recovering the ledger. A running connector owns the write lock and exposes the
// same projection through its local read-only checkout-status route.
func InspectCheckoutStatus(dir, installation string) (TeammateStatus, error) {
	path := filepath.Join(dir, "discord.db")
	db, err := bolt.Open(path, 0o600, &bolt.Options{
		ReadOnly: true, Timeout: 100 * time.Millisecond,
		OpenFile: func(path string, _ int, _ os.FileMode) (*os.File, error) {
			return openCheckoutFile(path)
		},
	})
	if err != nil {
		return TeammateStatus{}, err
	}
	defer func() { _ = db.Close() }()
	if err := db.View(func(tx *bolt.Tx) error {
		for _, name := range bucketNames {
			if tx.Bucket([]byte(name)) == nil {
				return errors.New("discord: checkout receipt index is unavailable")
			}
		}
		return nil
	}); err != nil {
		return TeammateStatus{}, err
	}
	return (&Store{db: db}).teammateStatus(installation, nil)
}

func (s *Service) localCheckoutStatus() (TeammateStatus, error) {
	var native func(TeammateDiscussion) (TeammateNativeStatus, error)
	if reader, ok := s.teammateManager.(interface {
		CheckoutStatus(TeammateDiscussion) (TeammateNativeStatus, error)
	}); ok {
		native = reader.CheckoutStatus
	}
	return s.Store.teammateStatus(s.Config.InstallationID, native)
}

// InspectSelectedState and InspectConfig read existing connector configuration
// without creating directories or changing file permissions.
func InspectSelectedState(dir string) (string, error) {
	return readSelectedState(dir, openCheckoutFile)
}

func InspectConfig(dir string) (Config, error) {
	return readConfig(dir, openCheckoutFile)
}

func openCheckoutFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !singleStoreFile(info) {
		_ = f.Close()
		return nil, ErrInvalid
	}
	return f, nil
}
