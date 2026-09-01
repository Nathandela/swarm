package skeleton

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/Nathandela/swarm/internal/protocol"
)

// contextGuardSettingsFile is deliberately daemon-global, unlike per-session
// context-guard lifecycle state that may be introduced later.
const contextGuardSettingsFile = "context-guard-settings.json"

const contextGuardSettingsSchemaVersion = 1

// contextGuardSettingsStore owns a single versioned settings document. mu covers the
// in-memory CAS and the entire durable-before-publish transaction, so two owner
// connections cannot both accept the same expected revision.
type contextGuardSettingsStore struct {
	mu          sync.Mutex
	path        string
	settings    protocol.ContextGuardSettings
	unavailable error
	// write is a narrow failure seam for tests. Production uses writeContextGuardSettings.
	write func(path string, data []byte) error
}

// errContextGuardSettingsPostRename marks an error after the name replacement. A
// caller cannot know whether a crash made that replacement durable, so the store
// becomes unavailable rather than claiming either the old or new document won.
var errContextGuardSettingsPostRename = errors.New("context guard settings write uncertain after rename")

type contextGuardSettingsWriteOps struct {
	rename  func(oldpath, newpath string) error
	syncDir func(dir string) error
}

func defaultContextGuardSettings() protocol.ContextGuardSettings {
	return protocol.ContextGuardSettings{
		SchemaVersion: contextGuardSettingsSchemaVersion,
		AutoCompact: protocol.ContextGuardAutoCompact{
			Enabled:          false,
			ThresholdPercent: 80,
		},
	}
}

// openContextGuardSettingsStore never prevents daemon assembly. A missing file is the
// safe default; a corrupt, unsupported, unreadable, or otherwise invalid file retains
// its evidence and places the store in a stable unavailable state. There is intentionally
// no automatic repair: an explicit repair path belongs to a later owner workflow.
func openContextGuardSettingsStore(stateDir string) *contextGuardSettingsStore {
	s := &contextGuardSettingsStore{
		path:     filepath.Join(stateDir, contextGuardSettingsFile),
		settings: defaultContextGuardSettings(),
		write:    writeContextGuardSettings,
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s
	}
	if err != nil {
		s.unavailable = fmt.Errorf("%w: read persisted document: %v", protocol.ErrContextGuardSettingsUnavailable, err)
		return s
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		s.unavailable = fmt.Errorf("%w: invalid persisted document: %v", protocol.ErrContextGuardSettingsUnavailable, err)
		return s
	}
	var settings protocol.ContextGuardSettings
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		s.unavailable = fmt.Errorf("%w: decode persisted document: %v", protocol.ErrContextGuardSettingsUnavailable, err)
		return s
	}
	if err := validateContextGuardSettings(settings); err != nil {
		s.unavailable = fmt.Errorf("%w: invalid persisted document: %v", protocol.ErrContextGuardSettingsUnavailable, err)
		return s
	}
	s.settings = settings
	return s
}

func validateContextGuardSettings(settings protocol.ContextGuardSettings) error {
	if settings.SchemaVersion != contextGuardSettingsSchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", settings.SchemaVersion)
	}
	return validateContextGuardAutoCompact(settings.AutoCompact)
}

func validateContextGuardAutoCompact(autoCompact protocol.ContextGuardAutoCompact) error {
	return protocol.ValidateContextGuardAutoCompact(autoCompact)
}

// ContextGuardSettings returns a disabled default alongside the stable unavailable
// sentinel when the persisted document could not be trusted. The default is a safety
// posture for any in-process caller; the error prevents a protocol client from mistaking
// it for recoverable configuration.
func (s *contextGuardSettingsStore) ContextGuardSettings() (protocol.ContextGuardSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable != nil {
		return defaultContextGuardSettings(), s.unavailable
	}
	return s.settings, nil
}

// SetContextGuardSettings validates a caller-owned CAS proposal, persists it fully, and
// only then publishes it to memory. An identical current-revision proposal is a no-op.
func (s *contextGuardSettingsStore) SetContextGuardSettings(expectedRevision uint64, autoCompact protocol.ContextGuardAutoCompact) (protocol.ContextGuardSettings, error) {
	if err := validateContextGuardAutoCompact(autoCompact); err != nil {
		return protocol.ContextGuardSettings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable != nil {
		return defaultContextGuardSettings(), s.unavailable
	}
	if expectedRevision != s.settings.Revision {
		return s.settings, protocol.ErrContextGuardSettingsStaleRevision
	}
	if autoCompact == s.settings.AutoCompact {
		return s.settings, nil
	}
	if s.settings.Revision == math.MaxUint64 {
		return s.settings, fmt.Errorf("%w: revision exhausted", protocol.ErrContextGuardSettingsUnavailable)
	}
	next := s.settings
	next.Revision++
	next.AutoCompact = autoCompact
	data, err := json.Marshal(next)
	if err != nil {
		return s.settings, err
	}
	if err := s.write(s.path, data); err != nil {
		if errors.Is(err, errContextGuardSettingsPostRename) {
			s.unavailable = fmt.Errorf("%w: %v", protocol.ErrContextGuardSettingsUnavailable, err)
		}
		return s.settings, err
	}
	// Publish strictly after the temporary file, file Sync, rename, and directory Sync.
	s.settings = next
	return next, nil
}

// writeContextGuardSettings atomically persists one 0600 document. The parent sync is
// essential: rename alone makes the new name visible but does not make its directory
// entry durable across a crash.
func writeContextGuardSettings(path string, data []byte) error {
	return writeContextGuardSettingsWithOps(path, data, contextGuardSettingsWriteOps{})
}

func writeContextGuardSettingsWithOps(path string, data []byte, ops contextGuardSettingsWriteOps) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, contextGuardSettingsFile+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
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
	rename := ops.rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(tmpName, path); err != nil {
		return err
	}
	syncDir := ops.syncDir
	if syncDir == nil {
		syncDir = syncContextGuardSettingsDir
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("%w: %v", errContextGuardSettingsPostRename, err)
	}
	return nil
}

func syncContextGuardSettingsDir(dir string) error {
	dirHandle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = dirHandle.Close() }()
	return dirHandle.Sync()
}

// rejectDuplicateJSONKeys walks exactly one JSON value and rejects duplicate names
// in any object. encoding/json otherwise accepts "enabled":false,"enabled":true,
// which makes a human review a different policy than the decoder adopts.
func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walkContextGuardJSON(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON documents")
		}
		return err
	}
	return nil
}

func walkContextGuardJSON(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			name, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := name.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := walkContextGuardJSON(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := walkContextGuardJSON(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

var _ protocol.ContextGuardSettingsBackend = (*contextGuardSettingsStore)(nil)
