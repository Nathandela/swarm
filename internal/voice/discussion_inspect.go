package voice

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	bolt "go.etcd.io/bbolt"
)

// InspectDiscussion reads a retained Discord ledger without opening a controller.
// A live writer owns the exclusive bbolt lock and makes offline inspection unavailable.
func InspectDiscussion(dir string) (DiscussionNativeStatus, error) {
	var status DiscussionNativeStatus
	id := filepath.Base(dir)
	if !managerproof.ValidHash(id) {
		return status, ErrInvalid
	}
	marker, err := readVoiceFile(dir, "voice.initialized", 64)
	if err != nil {
		return status, err
	}
	if string(marker) != "voice-store-v1\n" {
		return status, ErrCorrupt
	}
	db, err := bolt.Open(filepath.Join(dir, "voice.db"), 0, &bolt.Options{
		ReadOnly: true, Timeout: 50 * time.Millisecond, OpenFile: openInventoryLedger,
	})
	if err != nil {
		return status, err
	}
	defer func() { _ = db.Close() }()
	err = db.View(func(tx *bolt.Tx) error {
		header, err := readHeader(tx)
		if err != nil {
			return err
		}
		if !currentStoreSchema(header.Version) {
			return ErrStale
		}
		st, err := readState(tx)
		if err != nil {
			return err
		}
		conversation := DiscussionConversationPrefix + id
		if st.CurrentConversation != conversation || st.Conversations[conversation].Binding.ConversationID != conversation {
			return ErrStale
		}
		controller := DiscussionController{dir: dir, cfg: Config{ConversationID: conversation}, now: time.Now}
		status, err = controller.statusFromState(context.Background(), st, false)
		return err
	})
	return status, err
}

func openInventoryLedger(path string, _ int, _ os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !privateRegular(info) || info.Mode().Perm() != 0600 || info.Size() == 0 || info.Size() > MaxStoreBytes*4 {
		_ = f.Close()
		return nil, ErrInvalid
	}
	return f, nil
}
