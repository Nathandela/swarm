package upgrade

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/accountcheck"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
)

// accountStateGuard covers non-session state too: an enrollment-only machine
// can have credential writers even when maxPersistedSchema finds zero metas.
func accountStateGuard(stateRoot string, card CompatManifest) error {
	if err := accountObservationHoldGuard(stateRoot, card); err != nil {
		return err
	}
	if err := accountRecoveryGuard(stateRoot, card); err != nil {
		return err
	}
	accountPath := filepath.Join(stateRoot, "accounts")
	info, err := os.Lstat(accountPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !privateAccountInfo(info, true) {
		return errors.New("account state cannot be verified")
	}
	root, err := os.OpenRoot(accountPath)
	if err != nil {
		return errors.New("account state cannot be verified")
	}
	defer func() { _ = root.Close() }()
	registry, err := readAccountDocument(root, "registry.json")
	if err != nil {
		return errors.New("account registry cannot be verified")
	}
	var reg struct {
		SchemaVersion int                        `json:"schema_version"`
		Accounts      map[string]json.RawMessage `json:"accounts"`
	}
	if json.Unmarshal(registry, &reg) != nil || reg.SchemaVersion < 1 || reg.Accounts == nil {
		return errors.New("account registry cannot be verified")
	}
	if reg.SchemaVersion > accounts.SchemaVersion {
		return errors.New("account registry has an unsupported schema")
	}
	store, openErr := accounts.OpenReadOnly(stateRoot)
	if openErr != nil {
		return errors.New("account registry cannot be verified")
	}
	_, snapshotErr := store.Snapshot()
	_ = store.Close()
	if snapshotErr != nil {
		return errors.New("account registry cannot be verified")
	}
	if len(reg.Accounts) > 0 {
		if card.AccountSchema < reg.SchemaVersion || card.AccountShim < shim.ManagedWriterSchemaVersion || card.AccountConfig < 1 {
			return errors.New("the target build cannot preserve private account bindings and configuration")
		}
	}
	if data, readErr := readAccountDocument(root, "enrollment.json"); readErr == nil {
		var jobs struct {
			SchemaVersion int                        `json:"schema_version"`
			Jobs          map[string]json.RawMessage `json:"jobs"`
		}
		if json.Unmarshal(data, &jobs) != nil || jobs.SchemaVersion != 1 || jobs.Jobs == nil {
			return errors.New("account enrollment state cannot be verified")
		}
		if len(jobs.Jobs) > 0 && (card.AccountJobs < jobs.SchemaVersion || card.AccountWorker < 1) {
			return errors.New("the target build cannot manage persisted account enrollment workers")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return errors.New("account enrollment state cannot be verified")
	}
	if err := accountInboxGuard(root, card); err != nil {
		return err
	}
	if err := accountCheckGuard(root, card); err != nil {
		return err
	}
	if err := accountCheckCollectionGuard(root, card); err != nil {
		return err
	}
	// Candidate workers have separate files, which cannot be hidden by an empty registry.
	jobsPath := filepath.Join(accountPath, "jobs")
	jobsInfo, err := root.Lstat("jobs")
	if err != nil || !privateAccountInfo(jobsInfo, true) {
		return errors.New("account worker inventory cannot be verified")
	}
	dirs, err := os.ReadDir(jobsPath)
	if err != nil {
		return errors.New("account worker inventory cannot be verified")
	}
	for _, entry := range dirs {
		info, statErr := root.Lstat(filepath.Join("jobs", entry.Name()))
		if statErr != nil || !privateAccountInfo(info, true) {
			return errors.New("account worker inventory contains an unsafe entry")
		}
		jobRoot, err := root.OpenRoot(filepath.Join("jobs", entry.Name()))
		if err != nil {
			return errors.New("account worker state cannot be verified")
		}
		data, readErr := readAccountDocument(jobRoot, "worker-config.json")
		_ = jobRoot.Close()
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return errors.New("account worker state cannot be verified")
		}
		var cfg struct {
			SchemaVersion int `json:"schema_version"`
		}
		if json.Unmarshal(data, &cfg) != nil || cfg.SchemaVersion != 1 || card.AccountWorker < cfg.SchemaVersion {
			return errors.New("the target build cannot contain persisted account workers")
		}
	}
	return nil
}

// A failed native event admission can leave only a per-session hold. It is
// independent of both the account registry and the recovery journal.
func accountObservationHoldGuard(stateRoot string, card CompatManifest) error {
	root, err := os.OpenRoot(stateRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("account observation holds cannot be verified")
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(".")
	if err != nil {
		return errors.New("account observation holds cannot be verified")
	}
	entries, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return errors.New("account observation holds cannot be verified")
	}
	for _, entry := range entries {
		if !entry.IsDir() || !persist.ValidID(entry.Name()) {
			continue
		}
		name := filepath.Join(entry.Name(), "account-observation-hold.json")
		if _, err := root.Lstat(name); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return errors.New("account observation holds cannot be verified")
		}
		info, err := root.Lstat(entry.Name())
		if err != nil || !privateAccountInfo(info, true) {
			return errors.New("account observation holds cannot be verified")
		}
		data, err := readAccountDocument(root, name)
		var hold struct {
			SchemaVersion int    `json:"schema_version"`
			Kind          string `json:"kind"`
			Local         string `json:"local"`
		}
		if err != nil || json.Unmarshal(data, &hold) != nil || hold.SchemaVersion != 1 || hold.Kind != "hold" || hold.Local != entry.Name() {
			return errors.New("account observation holds cannot be verified")
		}
		if card.AccountRecovery < accounts.RecoverySchemaVersion {
			return errors.New("the target build cannot preserve account observation holds")
		}
	}
	return nil
}

// Pending input and detached checks can precede the first recovery journal.
// Inventory them independently, so an empty registry or legacy journal cannot
// conceal a required recovery or containment capability during rollback.
func accountInboxGuard(root *os.Root, card CompatManifest) error {
	dir, entries, err := openAccountInventory(root, "recovery-inbox")
	if err != nil || dir == nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	for _, entry := range entries {
		data, err := readAccountDocument(dir, entry.Name())
		var event struct {
			SchemaVersion int `json:"schema_version"`
		}
		if err != nil || json.Unmarshal(data, &event) != nil || event.SchemaVersion != 1 {
			return errors.New("account recovery inbox cannot be verified")
		}
		if card.AccountRecovery < accounts.RecoverySchemaVersion {
			return errors.New("the target build cannot preserve pending account recovery events")
		}
	}
	return nil
}

func accountCheckGuard(root *os.Root, card CompatManifest) error {
	dir, entries, err := openAccountInventory(root, "checks")
	if err != nil || dir == nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	for _, entry := range entries {
		info, err := dir.Lstat(entry.Name())
		if strings.HasPrefix(entry.Name(), ".lock-") {
			digest := strings.TrimPrefix(entry.Name(), ".lock-")
			raw, decodeErr := hex.DecodeString(digest)
			if err != nil || decodeErr != nil || len(raw) != 32 || digest != strings.ToLower(digest) || !privateAccountInfo(info, false) || info.Size() != 0 {
				return errors.New("account check lock inventory cannot be verified")
			}
			continue
		}
		if err != nil || !privateAccountInfo(info, true) {
			return errors.New("account check inventory cannot be verified")
		}
		if card.AccountWorker < accountcheck.SchemaVersion {
			return errors.New("the target build cannot contain persisted account checks")
		}
		check, err := dir.OpenRoot(entry.Name())
		if err != nil {
			return errors.New("account check inventory cannot be verified")
		}
		data, readErr := readAccountDocument(check, "worker.json")
		_ = check.Close()
		var worker struct {
			SchemaVersion int `json:"schema_version"`
		}
		if readErr != nil || json.Unmarshal(data, &worker) != nil || worker.SchemaVersion != accountcheck.SchemaVersion {
			return errors.New("account check custody cannot be verified")
		}
	}
	return nil
}

// Collection intents retain only proved-dead, durably unreferenced Worker2
// custody. They never authorize execution or replace a stopped-writer proof.
func accountCheckCollectionGuard(root *os.Root, card CompatManifest) error {
	present, err := accountcheck.ValidateCollectionInventory(root)
	if err != nil {
		return errors.New("account check collection cannot be verified")
	}
	if present && card.AccountWorker < accountcheck.SchemaVersion {
		return errors.New("the target build cannot preserve retained account check custody")
	}
	return nil
}

func openAccountInventory(root *os.Root, name string) (*os.Root, []os.DirEntry, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil || !privateAccountInfo(info, true) {
		return nil, nil, errors.New("account inventory cannot be verified")
	}
	dir, err := root.OpenRoot(name)
	if err != nil {
		return nil, nil, errors.New("account inventory cannot be verified")
	}
	f, err := dir.Open(".")
	if err != nil {
		_ = dir.Close()
		return nil, nil, errors.New("account inventory cannot be verified")
	}
	entries, err := f.ReadDir(4097)
	_ = f.Close()
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > 4096 {
		_ = dir.Close()
		return nil, nil, errors.New("account inventory cannot be verified")
	}
	return dir, entries, nil
}

func accountRecoveryGuard(stateRoot string, card CompatManifest) error {
	data, err := readPlainAccountDocument(filepath.Join(stateRoot, "auth-watch-state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("account recovery state cannot be verified")
	}
	var state map[string]json.RawMessage
	if json.Unmarshal(data, &state) != nil || state == nil {
		return errors.New("account recovery state cannot be verified")
	}
	var schema int
	if raw := state["account_schema_version"]; raw != nil && json.Unmarshal(raw, &schema) != nil {
		return errors.New("account recovery schema cannot be verified")
	}
	hasManaged := false
	for key, raw := range state {
		if strings.HasPrefix(key, "account_") && key != "account_schema_version" && string(raw) != "null" && string(raw) != "{}" && string(raw) != "[]" {
			hasManaged = true
		}
	}
	if schema < 0 || schema > accounts.RecoverySchemaVersion || card.AccountRecovery < schema || (hasManaged && (schema < 1 || card.AccountRecovery < accounts.RecoverySchemaVersion || card.AccountShim < shim.ManagedWriterSchemaVersion)) {
		return errors.New("the target build cannot reconcile managed account recovery")
	}
	return nil
}

func privateAccountInfo(info os.FileInfo, directory bool) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return false
	}
	if directory {
		return info.IsDir()
	}
	return info.Mode().IsRegular() && stat.Nlink == 1
}
func readAccountDocument(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !privateAccountInfo(info, false) || info.Size() > 8<<20 {
		return nil, fmt.Errorf("unsafe account document")
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) || !privateAccountInfo(actual, false) {
		return nil, fmt.Errorf("unsafe account document")
	}
	data, err := io.ReadAll(io.LimitReader(file, 8<<20+1))
	if len(data) > 8<<20 {
		return nil, fmt.Errorf("oversized account document")
	}
	return data, err
}
func readPlainAccountDocument(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return readAccountDocument(root, filepath.Base(path))
}
