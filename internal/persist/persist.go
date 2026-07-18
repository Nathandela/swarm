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

// writeTemp writes data to the freshly-created temp file during Save. It is a
// package-level indirection defaulting to the real *os.File.Write so a test can
// inject a disk-full (ENOSPC) fault at the exact byte-commit point and prove the
// atomic temp+rename never tears the committed meta.json (S8). Production always
// uses the default; behavior is identical.
var writeTemp = func(f *os.File, data []byte) (int, error) { return f.Write(data) }

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
// A pre-existing root is hardened to 0700 as well: MkdirAll leaves an existing
// directory's mode untouched, so the chmod is unconditional.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{root: dir}, nil
}

// idRE is the path-safe session-id pattern (ADR-004).
var idRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// ValidID reports whether id is path-safe per ADR-004: it must match idRE and
// must not be ".", "..", or start with "-", so it can never escape a store root,
// traverse a path, or be mistaken for a flag. This is the single source of truth
// for the path-safe id pattern; internal/protocol and internal/worktree both
// delegate to it instead of duplicating the regex.
func ValidID(id string) bool {
	return idRE.MatchString(id) && id != "." && id != ".." && !strings.HasPrefix(id, "-")
}

// validateID enforces the orchestrator-pinned id contract per ADR-004 (see
// ValidID), returning a descriptive error for the invalid case.
//
// Case-collisions on case-insensitive filesystems are avoided by the id generator
// (lowercase-only, Epic 5), not by validation.
func validateID(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid session id %q: must match %s and not be %q, %q, or start with %q",
			id, idRE.String(), ".", "..", "-")
	}
	return nil
}

// sessionPath returns <root>/<id>, first refusing to follow a symlink out of the
// store root: if the path exists and is a symlink, it errors (ADR-004 escape).
// A missing path is fine — there is nothing to escape through yet. Callers must
// have already validated id.
func (s *Store) sessionPath(id string) (string, error) {
	p := filepath.Join(s.root, id)
	fi, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("session path %q is a symlink; refusing to follow it out of the store root", p)
	}
	return p, nil
}

// Save writes m atomically to <root>/<m.ID>/meta.json: marshal, write a 0600
// temp file in the session dir, then rename over meta.json. A crash before the
// rename leaves the previous meta.json (or nothing) intact — never a torn file.
//
// Save is the single choke point that enforces two on-disk invariants: the env
// is allowlist-filtered (ADR-004) and the schema version is stamped to the
// current build's, so a caller can persist neither an unfiltered secret nor an
// arbitrary schema version.
//
// Durability model is process-crash (ADR-003); no parent-directory fsync
// (power-loss out of scope).
func (s *Store) Save(m Meta) error {
	if err := validateID(m.ID); err != nil {
		return err
	}
	dir, err := s.sessionPath(m.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	m.Env = FilterEnv(m.Env)
	m.SchemaVersion = SchemaVersion
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, metaFile+".tmp*") // os.CreateTemp creates the file 0600
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename; cleans up on any error path
	if _, err := writeTemp(tmp, data); err != nil {
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
	dir, err := s.sessionPath(id)
	if err != nil {
		return Meta{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return Meta{}, err
	}
	return decodeMeta(data, id)
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
		if e.Type()&os.ModeSymlink != 0 {
			continue // never follow a symlinked session entry out of the root
		}
		if !e.IsDir() || validateID(e.Name()) != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, e.Name(), metaFile))
		if err != nil {
			continue
		}
		m, err := decodeMeta(data, e.Name())
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
	dir, err := s.sessionPath(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// decodeMeta parses a meta.json body owned by session directory wantID. It
// rejects garbage-but-valid JSON (an empty id, or an id that disagrees with the
// directory it was read from) and a schema version newer than this build, then
// applies read-side migration for older versions (G6). The file is never
// rewritten; migration mutates only the returned Meta.
func decodeMeta(data []byte, wantID string) (Meta, error) {
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, err
	}
	if m.ID == "" {
		return Meta{}, fmt.Errorf("meta has empty id")
	}
	if m.ID != wantID {
		return Meta{}, fmt.Errorf("meta id %q does not match session directory %q", m.ID, wantID)
	}
	if m.SchemaVersion > SchemaVersion {
		return Meta{}, fmt.Errorf("meta schema version %d is newer than supported version %d", m.SchemaVersion, SchemaVersion)
	}
	if err := applyMigrations(&m, SchemaVersion, migrations); err != nil {
		return Meta{}, err
	}
	return m, nil
}

// migrateV0toV1 upgrades a Meta from schema v0 to v1. v0 and v1 share the same
// field set, so it makes no field change; it exists as a named registry entry so
// the migration chain is a real primitive (a future v1->v2 slots in beside it)
// rather than a bare version stamp.
func migrateV0toV1(*Meta) {}

// migrations maps schema version N to the function that upgrades a Meta from
// version N to N+1. applyMigrations walks it in order.
var migrations = map[int]func(*Meta){
	0: migrateV0toV1,
}

// applyMigrations upgrades m in place from its current SchemaVersion up to
// target, applying each registered step in ascending version order and stamping
// the version after each. A gap — no registered step for a version below target
// — is a loud error ("no migration registered from vN"), never a silent advance,
// so a file this build cannot faithfully upgrade is rejected rather than
// half-migrated. chain is a parameter so the ordering and gap behavior can be
// exercised with a synthetic migration set in tests.
func applyMigrations(m *Meta, target int, chain map[int]func(*Meta)) error {
	for m.SchemaVersion < target {
		migrate, ok := chain[m.SchemaVersion]
		if !ok {
			return fmt.Errorf("no migration registered from v%d", m.SchemaVersion)
		}
		migrate(m)
		m.SchemaVersion++
	}
	return nil
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
