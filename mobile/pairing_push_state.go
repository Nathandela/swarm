package swarmmobile

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/phonecore"
)

const pairingPushOwnershipFile = "pairing-push-owned.json"

type pairingPushOwnership struct {
	SchemaVersion int    `json:"schema_version"`
	Address       string `json:"address"`
}

// ownStagedPushBindingAfterPin closes the cross-file crash window between the machine
// pin and the push store. The ownership marker is durable only after app.pin returned;
// startup can therefore complete the staged disposition deterministically. beforeCommit
// is a test-only kill seam (nil in production).
func (a *App) ownStagedPushBindingAfterPin(addr phonecore.PushAddress, beforeCommit func()) error {
	if err := a.markPairingPushOwned(addr); err != nil {
		return err
	}
	if beforeCommit != nil {
		beforeCommit()
	}
	if err := a.core.CommitStagedPushBinding(addr); err != nil {
		// Returning an error makes pairing.RunDevice invoke its revoke arm. Remove the
		// ownership decision first so a restart cannot race that rollback and re-commit it.
		_ = a.clearPairingPushOwned(addr)
		return err
	}
	// Once the push store committed, a leftover marker is harmless and startup removes
	// it idempotently. Do not turn a completed durable commit into a protocol failure just
	// because the redundant recovery marker's unlink could not be dir-synced.
	_ = a.clearPairingPushOwned(addr)
	return nil
}

func (a *App) recoverPairingPushOwnership() error {
	addr, found, err := a.readPairingPushOwnership()
	if err != nil || !found {
		return err
	}
	if err := a.core.CommitStagedPushBinding(addr); err != nil {
		return fmt.Errorf("recover pairing push ownership: %w", err)
	}
	return a.clearPairingPushOwned(addr)
}

func (a *App) markPairingPushOwned(addr phonecore.PushAddress) error {
	if existing, found, err := a.readPairingPushOwnership(); err != nil {
		return err
	} else if found && existing != addr {
		return errors.New("swarmmobile: another pairing push ownership decision is still pending")
	} else if found {
		return nil
	}
	rec := pairingPushOwnership{
		SchemaVersion: 1,
		Address:       base64.RawURLEncoding.EncodeToString(addr[:]),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return writePairingPushOwnership(filepath.Join(a.stateDir, pairingPushOwnershipFile), data)
}

func (a *App) readPairingPushOwnership() (phonecore.PushAddress, bool, error) {
	var addr phonecore.PushAddress
	data, err := os.ReadFile(filepath.Join(a.stateDir, pairingPushOwnershipFile))
	if errors.Is(err, os.ErrNotExist) {
		return addr, false, nil
	}
	if err != nil {
		return addr, false, err
	}
	var rec pairingPushOwnership
	if err := json.Unmarshal(data, &rec); err != nil {
		return addr, false, fmt.Errorf("swarmmobile: malformed pairing push ownership marker: %w", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(rec.Address)
	if rec.SchemaVersion != 1 || err != nil || len(raw) != len(addr) {
		return addr, false, errors.New("swarmmobile: malformed pairing push ownership marker")
	}
	copy(addr[:], raw)
	return addr, true, nil
}

func (a *App) clearPairingPushOwned(addr phonecore.PushAddress) error {
	existing, found, err := a.readPairingPushOwnership()
	if err != nil || !found || existing != addr {
		return err
	}
	path := filepath.Join(a.stateDir, pairingPushOwnershipFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncPairingPushDir(a.stateDir)
}

func writePairingPushOwnership(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pairing-push-owned-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncPairingPushDir(filepath.Dir(path))
}

func syncPairingPushDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
