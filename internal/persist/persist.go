// Package persist is the durable source of truth for session state (ADR-003):
// one meta.json per session under <root>/<id>/, written atomically so a crash
// mid-write is observed as old-or-new and never torn, and so a single corrupt
// session file never prevents scanning the rest. It carries invariant S8
// (atomic-durable state) and part of ADR-004 (path-safe session ids).
package persist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// SchemaVersion is the meta.json schema this build writes. Older versions are
// migrated forward on read (see Load); newer versions are rejected loudly.
const SchemaVersion = 1

// metaFile is the committed per-session state file name within a session dir.
const metaFile = "meta.json"

// Meta is the persisted state of a single session (S-2). Every field carries an
// explicit snake_case JSON tag: the on-disk key set is the durable data
// contract, so tags are deliberately not omitempty — the key is always present.
type Meta struct {
	SchemaVersion  int               `json:"schema_version"`
	ID             string            `json:"id"`
	AgentType      string            `json:"agent_type"`
	Cwd            string            `json:"cwd"`
	LaunchOptions  map[string]string `json:"launch_options"`
	Env            []string          `json:"env"`
	CreatedAt      time.Time         `json:"created_at"`
	Status         status.Status     `json:"status"`
	LastActivity   time.Time         `json:"last_activity"`
	ShimPID        int               `json:"shim_pid"`
	ShimStartTime  int64             `json:"shim_start_time"`
	ConversationID string            `json:"conversation_id"`
	ExitCode       *int              `json:"exit_code"`
	ResumedFrom    string            `json:"resumed_from"`
}

// Store is a session store rooted at a single state directory. The root and
// every session dir are 0700; committed meta.json files are 0600.
type Store struct {
	root string
}

// NewStore returns a Store rooted at dir, creating dir (0700) if it is missing.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{root: dir}, nil
}

// idRE is the path-safe session-id pattern (ADR-004).
var idRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// validateID enforces the orchestrator-pinned id contract per ADR-004: ids are
// path-safe by validation. An id must match idRE and must not be ".", "..", or
// start with "-", so it can never escape the store root or be mistaken for a flag.
func validateID(id string) error {
	if !idRE.MatchString(id) || id == "." || id == ".." || strings.HasPrefix(id, "-") {
		return fmt.Errorf("invalid session id %q: must match %s and not be %q, %q, or start with %q",
			id, idRE.String(), ".", "..", "-")
	}
	return nil
}

// Save writes m atomically to <root>/<m.ID>/meta.json: marshal, write a 0600
// temp file in the session dir, then rename over meta.json. A crash before the
// rename leaves the previous meta.json (or nothing) intact — never a torn file.
func (s *Store) Save(m Meta) error {
	if err := validateID(m.ID); err != nil {
		return err
	}
	dir := filepath.Join(s.root, m.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, metaFile+".tmp*") // os.CreateTemp creates the file 0600
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename; cleans up on any error path
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, metaFile))
}

// Load reads and decodes <root>/<id>/meta.json. Older schema versions are
// migrated forward in the returned Meta (no file rewrite — G6); a future schema
// version, a corrupt file, or a missing session all return an error.
func (s *Store) Load(id string) (Meta, error) {
	if err := validateID(id); err != nil {
		return Meta{}, err
	}
	data, err := os.ReadFile(filepath.Join(s.root, id, metaFile))
	if err != nil {
		return Meta{}, err
	}
	return decodeMeta(data)
}

// Scan rebuilds the roster purely by directory scan (ADR-003: roster.json is a
// disposable index). It returns every session whose meta.json decodes cleanly
// and silently skips non-session entries and corrupt or future-version files, so
// one bad session never hides the rest (S8 isolation half).
func (s *Store) Scan() ([]Meta, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if !e.IsDir() || validateID(e.Name()) != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, e.Name(), metaFile))
		if err != nil {
			continue
		}
		m, err := decodeMeta(data)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// Delete removes a session's whole directory (R-3 retention delete).
func (s *Store) Delete(id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.root, id))
}

// decodeMeta parses a meta.json body, rejecting a schema version newer than this
// build and applying read-side migration for older versions (G6). No field has
// been removed or renamed between v0 and the current schema, so migration only
// stamps the current version onto the returned Meta; the file is never rewritten.
func decodeMeta(data []byte) (Meta, error) {
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, err
	}
	if m.SchemaVersion > SchemaVersion {
		return Meta{}, fmt.Errorf("meta schema version %d is newer than supported version %d", m.SchemaVersion, SchemaVersion)
	}
	if m.SchemaVersion < SchemaVersion {
		m.SchemaVersion = SchemaVersion
	}
	return m, nil
}

// DefaultDir returns the default state directory per the XDG Base Directory
// spec: $XDG_STATE_HOME/swarm/sessions when XDG_STATE_HOME is an absolute path,
// otherwise $HOME/.local/state/swarm/sessions (R-1).
func DefaultDir() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "swarm", "sessions"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "swarm", "sessions"), nil
}
