// Package persist is the durable source of truth for session state (ADR-003):
// one meta.json per session under <root>/<id>/, written atomically so a crash
// mid-write is observed as old-or-new and never torn, and so a single corrupt
// session file never prevents scanning the rest. It carries invariant S8
// (atomic-durable state) and part of ADR-004 (path-safe session ids).
package persist

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/status"
)

// SchemaVersion is the meta.json schema this build writes. Older versions are
// migrated forward on read (see Load); newer versions are rejected loudly.
const SchemaVersion = 1

// LoginSchemaVersion protects provider runtime provenance from older readers.
const LoginSchemaVersion = 4

// ClaudeSchemaVersion preserves owned native reservations and their retirement.
const ClaudeSchemaVersion = logins.ClaudeReservationSchemaVersion

// ClaudeChatSchemaVersion protects structured-primary metadata from PTY readers.
const ClaudeChatSchemaVersion = logins.ClaudeChatReservationSchemaVersion
const RequiredRuntimeClaudeChat = "claude-chat"

// IsClaudeChatRuntime is conservative routing only. It grants no managed custody
// or input authority; decoding still requires the exact owned schema/runtime pair.
func IsClaudeChatRuntime(m Meta) bool {
	return m.SchemaVersion == ClaudeChatSchemaVersion || m.RequiredRuntime == RequiredRuntimeClaudeChat
}

// ClaudeMetadataSchemaVersion selects the owned writer version even when a
// metadata update reset its incoming baseline version to one.
func ClaudeMetadataSchemaVersion(m Meta) int {
	if IsClaudeChatRuntime(m) {
		return ClaudeChatSchemaVersion
	}
	return ClaudeSchemaVersion
}

// metaFile is the committed per-session state file name within a session dir.
const metaFile = "meta.json"

// writeTemp writes data to the freshly-created temp file during Save. It is a
// package-level indirection defaulting to the real *os.File.Write so a test can
// inject a disk-full (ENOSPC) fault at the exact byte-commit point and prove the
// atomic temp+rename never tears the committed meta.json (S8). Production always
// uses the default; behavior is identical.
var writeTemp = func(f *os.File, data []byte) (int, error) { return f.Write(data) }

// Fresh stamps and retained-marker retries share this durability boundary.
var syncMetadataDirectory = func(dir string) error {
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

// Meta is the persisted state of a single session (S-2). Every field carries an
// explicit snake_case JSON tag: the on-disk key set is the durable data
// contract, so tags are deliberately not omitempty — the key is always present.
type Meta struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	AgentType     string `json:"agent_type"`
	Name          string `json:"name"` // user-provided session label; "" falls back to AgentType at display time
	// NameSetAt is when Name was last stamped, by a swarm rename or by adopting the name
	// the CLI itself shows for the session (ADR-022, newest wins). Additive without a
	// schema-version bump for the same rollback reason as AgentCwd below; a zero value
	// on an older record means "never stamped", which any CLI-published name beats.
	NameSetAt time.Time `json:"name_set_at"`
	Tag       string    `json:"tag"` // user-assigned grouping label; empty means untagged
	Cwd       string    `json:"cwd"`
	// AgentCwd is additive and DELIBERATELY DID NOT BUMP SchemaVersion. An adversarial
	// review proposed bumping it, reasoning that a rolled-back old daemon would rewrite
	// meta.json from its own struct and drop the field. That is true, and bumping is the
	// worse cure: Load REFUSES any meta whose schema_version exceeds the build's (see
	// below), so a v2 stamp would make a rolled-back daemon fail to load EVERY session
	// written by the newer one, rather than merely losing an optional field. Dropping
	// AgentCwd degrades ProviderCwd to Cwd -- today's behaviour before this field existed
	// -- and the value is re-stamped on the next launch. Degraded beats unloadable.
	AgentCwd      string            `json:"agent_cwd"` // resolved agent cwd when a pre-launch hook overrode Cwd; "" otherwise. See ProviderCwd.
	LaunchOptions map[string]string `json:"launch_options"`
	Env           []string          `json:"env"`
	CreatedAt     time.Time         `json:"created_at"`
	Status        status.Status     `json:"status"`
	// GroupEnteredAt is when Status last crossed a status.Derive display-group
	// boundary. It is additive without a schema-version bump for the same rollback
	// compatibility reason as AgentCwd above. Older records use LastActivity (then
	// CreatedAt) as a stable best-effort fallback until their next write.
	GroupEnteredAt time.Time `json:"group_entered_at"`
	LastActivity   time.Time `json:"last_activity"`
	ShimPID        int       `json:"shim_pid"`
	ShimStartTime  int64     `json:"shim_start_time"`
	ConversationID string    `json:"conversation_id"`
	// ShimWireVersion is the shimwire.Version compiled into the shim this session
	// runs under, stamped at spawn: the daemon spawns shims from its own
	// executable, so at that instant the two agree, and afterwards the shim
	// OUTLIVES upgrades of the binary on disk. Activation (lifecycle R3) gates a
	// wire-bump install on this field with pure disk reads -- the compat matrix
	// pins old-daemon x new-shim as ProcessLost, so a new binary must never land
	// under a daemon whose live shims it cannot serve. Additive without a schema
	// bump (the AgentCwd reason); 0 on a pre-R3 session means "unknown", which
	// gates conservatively.
	ShimWireVersion int `json:"shim_wire_version,omitempty"`
	// BackendPlanError is the reason this session launched with NO backend when its
	// adapter declared one (daemon.BackendPlanner returned an error): the agent's PTY
	// runs, but there is no app-server for the daemon to attach to. Persisted so the
	// failure has a surface (`swarm ls`, the TUI, `swarm doctor`) instead of one
	// daemon.log line -- the 2026-08-28 "attach failure" incident took a debugging
	// session to root-cause for want of exactly this field. Additive without a
	// schema-version bump for the same rollback reason as AgentCwd above; "" means
	// the backend planned fine, or the session's CLI needs none (the ordinary case,
	// indistinguishable by design -- absence of a backend is not a defect).
	BackendPlanError string `json:"backend_plan_error,omitempty"`
	// AuthIdentity is the provider-account identity (an adapter.AuthProbe digest,
	// never a secret) the session's agent was launched under (ADR-024). The auth
	// watcher compares it against the CURRENT identity to find sessions whose
	// processes hold tokens from a previous login and recycle them (kill + resume).
	// Additive without a schema-version bump for the same rollback reason as
	// AgentCwd above; "" means the provider has no probe, or the credentials were
	// unreadable at launch -- both gate conservatively (never auto-recycled).
	AuthIdentity string `json:"auth_identity,omitempty"`
	ResumeError  string `json:"resume_error,omitempty"`
	// LoginRuntime records the provider epoch loaded by the native processes.
	// Account selection belongs to the provider registry, never to this record.
	LoginRuntime     *logins.Runtime                     `json:"login_runtime,omitempty"`
	ClaudeRetirement *logins.ClaudeReservationRetirement `json:"claude_retirement,omitempty"`
	InputEmbargo     string                              `json:"input_embargo,omitempty"`
	RequiredRuntime  string                              `json:"required_runtime,omitempty"`
	OpenCodeBinding  *OpenCodeBinding                    `json:"opencode_binding,omitempty"`
	// CLIIdentity is the selected installation observed at launch; nil is unknown.
	// It does not attest a running process or a wrapper's dependencies.
	CLIIdentity *CLIIdentity `json:"cli_identity,omitempty"`
	ExitCode    *int         `json:"exit_code"`
	// RosterHidden archives an ended historical attempt without deleting its files.
	RosterHidden bool   `json:"roster_hidden,omitempty"`
	ResumedFrom  string `json:"resumed_from"`
	SpawnedFrom  string `json:"spawned_from"` // local id of the session that spawned this one (ADR-010 D4)
	SpawnIntent  string `json:"spawn_intent"` // "handoff" or "delegate"; empty when SpawnedFrom is
	Supervision  string `json:"supervision"`  // "passive", "manual" or "none" on a handoff child (ADR-010 Amendment 3 C1); empty otherwise

}

// EffectiveGroupEnteredAt returns the durable ordering instant for a session.
// Records written before GroupEnteredAt existed fall back deterministically,
// without rewriting every meta.json merely because the daemon restarted.
func (m Meta) EffectiveGroupEnteredAt() time.Time {
	if !m.GroupEnteredAt.IsZero() {
		return m.GroupEnteredAt
	}
	if !m.LastActivity.IsZero() {
		return m.LastActivity
	}
	return m.CreatedAt
}

// ProviderCwd is the working directory the AGENT actually ran in: AgentCwd when a
// pre-launch hook resolved one, and Cwd otherwise. The two differ for a worktree-isolated
// session (Epic 12), where Cwd is the repo the launch was requested in and the agent runs
// under <repo>/.swarm/worktrees/<name-slug> (or <id> when unnamed; an id suffix
// is added when the friendly path is already occupied).
//
// An agent CLI files its own history under an encoding of the directory it ran in, so
// every provider-facing path derivation must ask for this rather than read Cwd. Cwd keeps
// its own meaning untouched and is what worktree teardown stays anchored to;
// AgentCwd supplies the exact checkout path to remove.
func (m Meta) ProviderCwd() string {
	if m.AgentCwd != "" {
		return m.AgentCwd
	}
	return m.Cwd
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
// Ordinary metadata uses process-crash durability (ADR-003). Claude custody
// metadata also syncs its directory before it can authorize cleanup.
func (s *Store) Save(m Meta) error {
	if err := validateID(m.ID); err != nil {
		return err
	}
	m, data, err := s.metadataBytes(m)
	if err != nil {
		return err
	}
	return logins.WithClaudeMetadataSave(s.root, m.ID, data, func() error {
		return s.writeMetadata(m.ID, data, m.SchemaVersion == ClaudeSchemaVersion || m.SchemaVersion == ClaudeChatSchemaVersion)
	})
}

// metadataBytes is pure serialization; it never classifies stored ownership.
// The public Save guard compares stored custody before its raw rename callback.
func (s *Store) metadataBytes(m Meta) (Meta, []byte, error) {
	m.Env = FilterEnv(m.Env)
	owned := m.SchemaVersion == ClaudeSchemaVersion || IsClaudeChatRuntime(m) || m.ClaudeRetirement != nil || m.LoginRuntime != nil && logins.IsClaudeProfileRuntime(s.root, *m.LoginRuntime)
	ownedVersion := ClaudeMetadataSchemaVersion(m)
	m.SchemaVersion = SchemaVersion
	if m.AgentType == "fake" && ownedVersion == ClaudeChatSchemaVersion {
		m.SchemaVersion = ownedVersion
		if !validClaudeChatFixtureMeta(m) {
			return m, nil, fmt.Errorf("persist: invalid Claude chat fixture")
		}
	} else if owned {
		m.SchemaVersion = ownedVersion
		if !validClaudeMeta(m) || (!logins.IsClaudeProfileRuntime(s.root, *m.LoginRuntime) && (m.ClaudeRetirement == nil || m.LoginRuntime.Home != filepath.Join(s.root, "logins", "provider", "claude", "native"))) {
			return m, nil, fmt.Errorf("persist: invalid owned Claude runtime")
		}
	} else if m.RequiredRuntime != "" || m.OpenCodeBinding != nil {
		if !validOpenCodeMeta(m) {
			return m, nil, fmt.Errorf("persist: invalid OpenCode runtime binding")
		}
		m.SchemaVersion = OpenCodeSchemaVersion
	} else if m.LoginRuntime != nil {
		if !validLoginMeta(m) {
			return m, nil, fmt.Errorf("persist: invalid provider login runtime")
		}
		m.SchemaVersion = LoginSchemaVersion
	} else if m.InputEmbargo != "" {
		return m, nil, fmt.Errorf("persist: startup input embargo has no provider runtime")
	}

	data, err := json.Marshal(m)
	if err != nil {
		return m, nil, err
	}
	return m, data, nil
}

// writeMetadata is raw and non-reentrant. Owned callers retain the registry
// fence through rename and directory sync; retirement uses the same writer.
func (s *Store) writeMetadata(id string, data []byte, durable bool) error {
	dir, err := s.sessionPath(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, metaFile+".tmp*") // os.CreateTemp creates the file 0600
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename; cleans up on any error path
	if _, err := writeTemp(tmp, data); err != nil {
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
	if err := os.Rename(tmpName, filepath.Join(dir, metaFile)); err != nil {
		return err
	}
	if durable {
		return syncMetadataDirectory(dir)
	}
	return nil
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
	return s.withExecutionDeletion(id, func() error {
		if _, err := s.RetireClaudeReservation(id, nil); err != nil {
			return err
		}
		dir, err := s.sessionPath(id)
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		// Keep recovery/ownership metadata until all payloads have been removed.
		for _, entry := range entries {
			if entry.Name() == "meta.json" {
				continue
			}
			if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
		return os.RemoveAll(dir)
	})
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
	if m.SchemaVersion > ClaudeChatSchemaVersion {
		return Meta{}, fmt.Errorf("meta schema version %d is newer than supported version %d", m.SchemaVersion, ClaudeChatSchemaVersion)
	}
	if m.AgentType == "fake" && IsClaudeChatRuntime(m) {
		if !validClaudeChatFixtureMeta(m) || logins.ValidateClaudeChatFixtureMetadata(data, wantID) != nil {
			return Meta{}, fmt.Errorf("invalid Claude chat fixture")
		}
		return m, nil
	}
	if m.SchemaVersion == ClaudeSchemaVersion || IsClaudeChatRuntime(m) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return Meta{}, err
		}
		for name := range fields {
			if name != "required_runtime" && strings.EqualFold(name, "required_runtime") {
				return Meta{}, fmt.Errorf("invalid owned Claude runtime key")
			}
		}
		if !validClaudeMeta(m) {
			return Meta{}, fmt.Errorf("invalid owned Claude runtime")
		}
		return m, nil
	}
	if m.ClaudeRetirement != nil {
		return Meta{}, fmt.Errorf("claude retirement has no owned reservation")
	}
	if m.SchemaVersion == OpenCodeSchemaVersion || m.RequiredRuntime != "" || m.OpenCodeBinding != nil {
		if m.SchemaVersion != OpenCodeSchemaVersion || !validOpenCodeMeta(m) {
			return Meta{}, fmt.Errorf("invalid OpenCode runtime binding")
		}
		return m, nil
	}
	if m.SchemaVersion == 2 {
		return Meta{}, fmt.Errorf("session requires offline login migration")
	}
	if m.SchemaVersion == LoginSchemaVersion || m.LoginRuntime != nil {
		if m.SchemaVersion != LoginSchemaVersion || !validLoginMeta(m) {
			return Meta{}, fmt.Errorf("invalid provider login runtime")
		}
		return m, nil
	}
	if m.InputEmbargo != "" {
		return Meta{}, fmt.Errorf("startup input embargo has no provider runtime")
	}

	if err := applyMigrations(&m, SchemaVersion, migrations); err != nil {
		return Meta{}, err
	}
	return m, nil
}

func validClaudeChatFixtureMeta(m Meta) bool {
	return m.SchemaVersion == ClaudeChatSchemaVersion && m.RequiredRuntime == RequiredRuntimeClaudeChat && m.AgentType == "fake" && m.LoginRuntime == nil && m.CLIIdentity == nil && m.ClaudeRetirement == nil && m.OpenCodeBinding == nil && m.InputEmbargo == ""
}

// This is decoding, not writer-stop authorization. The shared logins guard
// validates retained private proofs again before any custody mutation.
func validClaudeMeta(m Meta) bool {
	validRuntime := m.SchemaVersion == ClaudeSchemaVersion && m.RequiredRuntime == "" || m.SchemaVersion == ClaudeChatSchemaVersion && m.RequiredRuntime == RequiredRuntimeClaudeChat
	if !validRuntime {
		return false
	}
	if !validLoginMeta(m) || m.LoginRuntime.Provider != logins.ProviderClaude || m.LoginRuntime.ExternalAuth || m.InputEmbargo != "" || m.OpenCodeBinding != nil {
		return false
	}
	// These are wire shapes, not root membership or stop proof. The shared Store
	// guard binds the exact state root and retained authority before mutation.
	profile := strings.HasSuffix(m.LoginRuntime.Home, "/logins/provider/claude/profiles/"+m.LoginRuntime.Identity)
	common := strings.HasSuffix(m.LoginRuntime.Home, "/logins/provider/claude/native")
	if !profile && (m.SchemaVersion == ClaudeChatSchemaVersion || !common || m.ClaudeRetirement == nil) {
		return false
	}
	fingerprint := ""
	if m.CLIIdentity != nil {
		fingerprint = m.CLIIdentity.Fingerprint
	}
	if profile {
		cli := m.CLIIdentity
		if cli == nil || !filepath.IsAbs(cli.Path) || filepath.Clean(cli.Path) != cli.Path || cli.Version == "" || cli.Fingerprint == "" || len(cli.Fingerprint) > 256 || strings.ContainsAny(cli.Path+cli.Version+cli.Fingerprint, "\x00\r\n") {
			return false
		}
		if m.SchemaVersion == ClaudeChatSchemaVersion && (cli.Version != "2.1.310" || !logins.IsClaudeCLIFingerprint(cli.Fingerprint)) {
			return false
		}
	}
	if q := m.ClaudeRetirement; q != nil {
		if q.Version != logins.ClaudeRetirementVersion || q.SessionID != m.ID || q.Runtime != *m.LoginRuntime || q.Cwd != m.ProviderCwd() || q.CLIFingerprint != fingerprint {
			return false
		}
		switch q.Source {
		case logins.ClaudeRetirementNoFork:
			if q.Native != (logins.NativeProcessInfo{}) || q.Issuer.PID <= 0 || q.Issuer.StartTime <= 0 || m.ShimPID != 0 || m.ShimStartTime != 0 {
				return false
			}
		case logins.ClaudeRetirementStopped:
			n := q.Native
			if n.Provider != logins.ProviderClaude || n.Runtime != *m.LoginRuntime || q.Issuer.PID != 0 || q.Issuer.StartTime != 0 {
				return false
			}
			if common {
				if n.SchemaVersion != logins.NativeWriterSchemaVersion || m.ShimPID <= 0 || m.ShimStartTime <= 0 || n.ShimPID != m.ShimPID || n.ShimStartTime != m.ShimStartTime || n.SessionID != "" || n.Cwd != "" || n.CLIFingerprint != "" {
					return false
				}
			} else if n.SchemaVersion != logins.ClaudeNativeWriterSchemaVersion || n.SessionID != m.ID || n.Cwd != m.ProviderCwd() || n.CLIFingerprint != fingerprint || m.ShimPID != 0 && (n.ShimPID != m.ShimPID || n.ShimStartTime != m.ShimStartTime) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validLoginMeta(m Meta) bool {
	r := m.LoginRuntime
	if r == nil || r.Provider != m.AgentType || (r.Provider != logins.ProviderClaude && r.Provider != logins.ProviderCodex) || r.Epoch == 0 || !filepath.IsAbs(r.Home) || filepath.Clean(r.Home) != r.Home || strings.ContainsAny(r.Home, "\x00\r\n") {
		return false
	}
	if len(r.Identity) != 64 || strings.ToLower(r.Identity) != r.Identity {
		return false
	}
	if _, err := hex.DecodeString(r.Identity); err != nil {
		return false
	}
	return len(m.InputEmbargo) <= 128 && !strings.ContainsAny(m.InputEmbargo, "/\\\x00\r\n")
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
