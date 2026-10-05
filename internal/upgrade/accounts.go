package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/accountcheck"
	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/enrollment"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
)

// Capability 3 adds retained executable digests; existing account-check and
// stopped-writer proof schemas remain 2 and retain their original meaning.
const RetainedNativeWorkerCapability = 3

// accountStateGuard covers non-session state too: an enrollment-only machine
// can have credential writers even when maxPersistedSchema finds zero metas.
func accountStateGuard(stateRoot string, card CompatManifest) error {
	if err := accountRetainedSessionGuard(stateRoot, card); err != nil {
		return err
	}
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
	if err := accountNativeRuntimeGuard(root, stateRoot, card); err != nil {
		return err
	}
	if err := accountStockCustodyGuard(root, card); err != nil {
		return err
	}
	configurations, err := accountConfigurationGuard(root, card)
	if err != nil {
		return err
	}
	registry, err := readAccountDocument(root, "registry.json")
	// Prepare can publish immutable projection metadata before accounts.Open
	// creates a registry. Permit only that exact metadata-only directory; any
	// profile, worker, or unknown neighboring state still requires a registry.
	if configurations && errors.Is(err, os.ErrNotExist) && configurationOnlyAccounts(root) {
		return nil
	}
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
	for _, raw := range reg.Accounts {
		var account struct {
			Generations map[string]struct {
				ErasureInventory json.RawMessage `json:"erasure_inventory"`
			} `json:"generations"`
		}
		if json.Unmarshal(raw, &account) != nil {
			return errors.New("account erasure inventory cannot be verified")
		}
		for _, generation := range account.Generations {
			if generation.ErasureInventory == nil {
				continue
			}
			var contract string
			if json.Unmarshal(generation.ErasureInventory, &contract) != nil || contract == "" {
				return errors.New("account erasure inventory cannot be verified")
			}
			if card.AccountInventory < accounts.NativeInventorySchemaVersion {
				return errors.New("the target build cannot preserve native account erasure inventory")
			}
		}
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
		for _, raw := range jobs.Jobs {
			var job struct {
				State           string          `json:"state"`
				CustodyConsumed json.RawMessage `json:"custody_consumed"`
			}
			if json.Unmarshal(raw, &job) != nil {
				return errors.New("account enrollment custody cannot be verified")
			}
			if job.CustodyConsumed == nil {
				continue
			}
			var consumed bool
			if string(job.CustodyConsumed) == "null" || json.Unmarshal(job.CustodyConsumed, &consumed) != nil || (consumed && job.State != "admitted" && job.State != "cancelled") {
				return errors.New("account enrollment custody cannot be verified")
			}
			if card.AccountInventory < accounts.NativeInventorySchemaVersion {
				return errors.New("the target build cannot preserve consumed account enrollment custody")
			}
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
			SchemaVersion     int    `json:"schema_version"`
			Provider          string `json:"provider"`
			NativePath        string `json:"native_path"`
			NativeVersion     string `json:"native_version"`
			NativeFingerprint string `json:"native_fingerprint"`
		}
		if json.Unmarshal(data, &cfg) != nil || (cfg.SchemaVersion != enrollment.SchemaVersion && cfg.SchemaVersion != enrollment.RetainedNativeConfigSchemaVersion) || (cfg.SchemaVersion == enrollment.SchemaVersion && (cfg.NativeFingerprint != "" || card.AccountWorker < 1)) {
			return errors.New("the target build cannot contain persisted account workers")
		}
		if cfg.SchemaVersion == enrollment.RetainedNativeConfigSchemaVersion && (card.AccountWorker < RetainedNativeWorkerCapability || cfg.Provider != accounts.ProviderClaude || cfg.NativeVersion != accountconfig.CharacterizedClaudeVersion || cfg.NativePath != filepath.Join(stateRoot, "accounts", "native", "claude-"+cfg.NativeVersion, "claude") || !persist.IsCLIContentFingerprint(cfg.NativeFingerprint)) {
			return errors.New("the target build cannot verify retained account enrollment executables")
		}
	}
	return nil
}

func accountNativeRuntimeGuard(root *os.Root, stateRoot string, card CompatManifest) error {
	dir, entries, err := openAccountInventory(root, "native")
	if err != nil || dir == nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if len(entries) == 0 {
		return nil
	}
	if card.AccountWorker < RetainedNativeWorkerCapability {
		return errors.New("the target build cannot verify retained managed native executables")
	}
	for _, entry := range entries {
		info, err := dir.Lstat(entry.Name())
		if entry.Name() == ".lock" {
			if err != nil || !privateAccountInfo(info, false) || info.Size() != 0 {
				return errors.New("retained native runtime lock cannot be verified")
			}
			continue
		}
		if err != nil || !privateAccountInfo(info, true) {
			return errors.New("retained native runtime inventory cannot be verified")
		}
		if strings.HasPrefix(entry.Name(), ".stage-") {
			if err := accountNativeStageGuard(dir, entry.Name()); err != nil {
				return err
			}
			continue
		}
		if entry.Name() != "claude-"+accountconfig.CharacterizedClaudeVersion {
			return errors.New("retained native runtime version cannot be verified")
		}
		version, err := dir.OpenRoot(entry.Name())
		if err != nil {
			return errors.New("retained native runtime metadata cannot be verified")
		}
		raw, err := readAccountDocument(version, "manifest.json")
		_ = version.Close()
		var manifest struct {
			Version       string              `json:"version"`
			Path          string              `json:"path"`
			ContentSHA256 string              `json:"content_sha256"`
			Source        persist.CLIIdentity `json:"source"`
		}
		fields, shapeErr := retainedManifestKeys(raw, []string{"version", "path", "content_sha256", "source"})
		_, sourceErr := retainedManifestKeys(fields["source"], []string{"path", "version", "fingerprint"})
		if err != nil || len(raw) > 8<<10 || shapeErr != nil || sourceErr != nil || json.Unmarshal(raw, &manifest) != nil || manifest.Version != accountconfig.CharacterizedClaudeVersion || manifest.Source.Version != manifest.Version || manifest.Source.Path == "" || manifest.Source.Fingerprint == "" || manifest.Path != filepath.Join(stateRoot, "accounts", "native", "claude-"+manifest.Version, "claude") || !persist.IsCLIContentFingerprint("sha256:"+manifest.ContentSHA256+":"+strings.Repeat("0", 64)) {
			return errors.New("retained native runtime metadata cannot be verified")
		}
	}
	return nil
}

// The retained manifest has two known objects. Reject future or duplicate keys
// before an older build can admit security-bearing metadata it would ignore.
func retainedManifestKeys(raw []byte, allowed []string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid retained native manifest object")
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		token, err := dec.Token()
		key, ok := token.(string)
		if err != nil || !ok || !slices.Contains(allowed, key) || fields[key] != nil {
			return nil, errors.New("unknown or duplicate retained native manifest key")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if token, err := dec.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("invalid retained native manifest object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple retained native manifest documents")
	}
	if len(fields) != len(allowed) {
		return nil, errors.New("missing retained native manifest key")
	}
	return fields, nil
}

func accountNativeStageGuard(root *os.Root, name string) error {
	nonce := strings.TrimPrefix(name, ".stage-")
	if len(nonce) != 26 || !strings.ContainsRune("01234567", rune(nonce[0])) {
		return errors.New("retained native stage name cannot be verified")
	}
	for _, value := range nonce {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", value) {
			return errors.New("retained native stage name cannot be verified")
		}
	}
	stage, err := root.OpenRoot(name)
	if err != nil {
		return errors.New("retained native stage cannot be verified")
	}
	defer func() { _ = stage.Close() }()
	dir, err := stage.Open(".")
	if err != nil {
		return errors.New("retained native stage cannot be verified")
	}
	entries, err := dir.ReadDir(3)
	_ = dir.Close()
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > 2 {
		return errors.New("retained native stage cannot be verified")
	}
	for _, entry := range entries {
		limit := int64(8 << 10)
		if entry.Name() == "claude" {
			limit = 1 << 30
		} else if entry.Name() != "manifest.json" {
			return errors.New("retained native stage contains unknown state")
		}
		info, err := stage.Lstat(entry.Name())
		if err != nil || !privateAccountInfo(info, false) || info.Size() > limit {
			return errors.New("retained native stage custody cannot be verified")
		}
	}
	return nil
}

// The cached executable may have been removed. Retained session observations
// still require a reader that cannot replace their content stamp with metadata.
func accountRetainedSessionGuard(stateRoot string, card CompatManifest) error {
	root, err := os.OpenRoot(stateRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("retained session observations cannot be verified")
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(".")
	if err != nil {
		return errors.New("retained session observations cannot be verified")
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return errors.New("retained session observations cannot be verified")
	}
	for _, entry := range entries {
		if !entry.IsDir() || !persist.ValidID(entry.Name()) {
			continue
		}
		name := filepath.Join(entry.Name(), "meta.json")
		if _, err := root.Lstat(name); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return errors.New("retained session observations cannot be verified")
		}
		info, err := root.Lstat(entry.Name())
		if err != nil || !privateAccountInfo(info, true) {
			return errors.New("retained session observations cannot be verified")
		}
		raw, err := readAccountDocument(root, name)
		var meta struct {
			CLI *persist.CLIIdentity `json:"cli_identity"`
		}
		if err != nil || json.Unmarshal(raw, &meta) != nil {
			return errors.New("retained session observations cannot be verified")
		}
		if meta.CLI != nil && strings.HasPrefix(meta.CLI.Fingerprint, "sha256:") && (card.AccountWorker < RetainedNativeWorkerCapability || !persist.IsCLIContentFingerprint(meta.CLI.Fingerprint)) {
			return errors.New("the target build cannot verify retained session executables")
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
	if schema < 0 || schema > accounts.RecoverySchemaVersion || card.AccountRecovery < schema || (hasManaged && (schema < 1 || card.AccountRecovery < 2 || card.AccountShim < shim.ManagedWriterSchemaVersion)) {
		return errors.New("the target build cannot reconcile managed account recovery")
	}
	if raw := state["account_rotations"]; raw != nil {
		var rotations map[string]struct {
			Expected *persist.CLIIdentity `json:"expected_cli_identity"`
		}
		if json.Unmarshal(raw, &rotations) != nil {
			return errors.New("retained recovery observations cannot be verified")
		}
		for _, rec := range rotations {
			if rec.Expected != nil && strings.HasPrefix(rec.Expected.Fingerprint, "sha256:") && (card.AccountWorker < RetainedNativeWorkerCapability || !persist.IsCLIContentFingerprint(rec.Expected.Fingerprint)) {
				return errors.New("the target build cannot verify retained recovery executables")
			}
		}
	}
	for _, key := range []string{"cli_refresh", "cli_candidates"} {
		if raw := state[key]; raw != nil {
			var records map[string]struct {
				Target   *persist.CLIIdentity `json:"target"`
				Identity *persist.CLIIdentity `json:"identity"`
			}
			if json.Unmarshal(raw, &records) != nil {
				return errors.New("retained CLI refresh observations cannot be verified")
			}
			for _, rec := range records {
				for _, identity := range []*persist.CLIIdentity{rec.Target, rec.Identity} {
					if identity != nil && strings.HasPrefix(identity.Fingerprint, "sha256:") && (card.AccountWorker < RetainedNativeWorkerCapability || !persist.IsCLIContentFingerprint(identity.Fingerprint)) {
						return errors.New("the target build cannot verify retained CLI refresh executables")
					}
				}
			}
		}
	}
	return nil
}

// Configuration contracts survive all credential generations and erasure. Scan
// the independent immutable inventory, including projections published before a
// session/meta reservation and old sources absent from the current roster. Only
// projected metadata is opened; user settings and source files are not read.
func accountConfigurationGuard(root *os.Root, card CompatManifest) (bool, error) {
	directory, entries, err := openAccountInventory(root, "configurations")
	if err != nil {
		return false, errors.New("account configuration inventory cannot be verified")
	}
	if directory == nil {
		return false, nil
	}
	defer func() { _ = directory.Close() }()
	for _, entry := range entries {
		ref := entry.Name()
		if len(ref) != 64 || ref != strings.ToLower(ref) {
			return true, errors.New("account configuration inventory contains an unknown entry")
		}
		if _, err := hex.DecodeString(ref); err != nil {
			return true, errors.New("account configuration inventory contains an unknown entry")
		}
		before, err := directory.Lstat(ref)
		if err != nil || !privateAccountInfo(before, true) {
			return true, errors.New("account configuration inventory contains an unsafe entry")
		}
		projection, err := directory.OpenRoot(ref)
		if err != nil {
			return true, errors.New("account configuration metadata cannot be verified")
		}
		actual, statErr := projection.Lstat(".")
		current, currentErr := directory.Lstat(ref)
		if statErr != nil || currentErr != nil || !privateAccountInfo(actual, true) || !os.SameFile(before, actual) || !os.SameFile(current, actual) {
			_ = projection.Close()
			return true, errors.New("account configuration metadata cannot be verified")
		}
		raw, readErr := readAccountDocument(projection, "projection.json")
		_ = projection.Close()
		if readErr != nil {
			return true, errors.New("account configuration metadata cannot be verified")
		}
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != ref {
			return true, errors.New("account configuration metadata cannot be verified")
		}
		need, err := accountconfig.ProjectionCompatibility(raw)
		if err != nil {
			return true, errors.New("account configuration contract cannot be verified")
		}
		if card.AccountConfig < need {
			return true, errors.New("the target build cannot preserve managed project configuration boundaries")
		}
	}
	return true, nil
}

func configurationOnlyAccounts(root *os.Root) bool {
	directory, err := root.Open(".")
	if err != nil {
		return false
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(2)
	return (err == nil || errors.Is(err, io.EOF)) && len(entries) == 1 && entries[0].Name() == "configurations" && entries[0].IsDir()
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
