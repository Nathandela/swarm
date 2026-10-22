package upgrade

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/loginmigration"
	"github.com/Nathandela/swarm/internal/logins"
)

// Login state matters even on an enrollment-only machine with no session metas.
func loginStateGuard(stateRoot string, card CompatManifest) error {
	if _, err := os.Lstat(filepath.Join(stateRoot, "logins")); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Lstat(filepath.Join(stateRoot, "accounts")); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if card.LoginSchema < logins.SchemaVersion {
			// A refused conversion may need the previous binary to resolve its
			// old recovery journal. No new login state exists in this branch.
			if card.Schema >= 2 && card.AccountSchema >= 1 && card.AccountJobs >= 1 && card.AccountRecovery >= 3 && card.AccountWorker >= 6 && card.AccountShim >= 2 && card.AccountConfig >= 4 && card.AccountInventory >= 1 && card.AccountAuth >= 1 {
				return nil
			}
			return errors.New("the target build cannot read the retained login migration input")
		}
		return loginmigration.Preflight(stateRoot)
	} else if err != nil {
		return errors.New("login state cannot be verified")
	}
	if card.LoginSchema < logins.SchemaVersion {
		return errors.New("the target build cannot read saved logins; restoring it would lose login and switch recovery state")
	}
	store, err := logins.OpenReadOnly(stateRoot)
	if err != nil {
		return errors.New("login state cannot be verified")
	}
	defer func() { _ = store.Close() }()
	_, err = store.Snapshot()
	if err != nil {
		return errors.New("login state cannot be verified")
	}
	return nil
}
