package phonecore

// The machine REGISTRY (ADR-018 MM6, wave R4): the durable N-entry replacement for the
// single-machine state directory. One root holds one registry file plus one namespace
// directory PER PAIRING; nothing in a namespace is shared with any other (MM2), so a
// cross-pairing bug has no medium to travel through.
//
// MigrateSingletonToRegistry is the TRANSACTIONAL migration of the pre-R4 singleton
// state into that shape (playbook section 12's six Android-state steps):
//
//  1. verify custody BEFORE modifying anything -- the singleton blob must open;
//  2. key the entry by the AUTHENTICATED machine id the durable blob carries, never a
//     display name;
//  3. copy the state files into the per-machine namespace (sealed blobs move verbatim,
//     so the same Keystore-held KEKs open them there);
//  4. commit the registry LAST -- the one write that flips authority;
//  5. after the commit the OLD singleton dir refuses to Resume (ErrStateMigrated),
//     because two live send sequencers for one pairing re-issue seqs the gateway
//     stale-drops forever;
//  6. the old blob itself is left byte-identical, rollback-readable for one release
//     train.
//
// Crash semantics are pinned by the MigrationKillPoint seam: at every kill point the
// world afterwards is EITHER the old state fully intact and authoritative (points
// before the commit) OR the new registry fully live (the commit and after) -- never
// both live, never neither readable.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// machinesDirName is the registry's directory inside the phone's state root: the
	// registry file plus one namespace directory per pairing.
	machinesDirName = "machines"
	// registryFileName is the committed registry inside machinesDirName. Its EXISTENCE
	// is the commit: every pre-commit crash leaves it absent, and Resume over the old
	// singleton dir keys ErrStateMigrated off exactly this file.
	registryFileName = "machine-registry.json"
)

var (
	// ErrRegistryNotLive refuses OpenMachineRegistry until the registry commit is
	// durable. A pre-commit crash, a refused migration and a never-migrated phone all
	// answer this: the old singleton state is authoritative in every one of those worlds.
	ErrRegistryNotLive = errors.New("phonecore: no live machine registry at this root")

	// ErrStateMigrated refuses Resume over the OLD singleton dir once the registry is
	// live. It is MM6's load-bearing prohibition -- "never produce two live send
	// sequencers for one pairing" -- as a sentinel: a re-issued seq under a retained
	// epoch is stale-dropped by the gateway permanently.
	ErrStateMigrated = errors.New("phonecore: singleton state was migrated into the machine registry; resume the per-machine namespace instead")
)

// MachineRegistry is the durable registry of machine pairings under one root. All
// methods are safe for concurrent use.
type MachineRegistry struct {
	mu      sync.Mutex
	root    string
	entries []MachineDescriptor
}

// registryFile is the on-disk shape of the committed registry.
type registryFile struct {
	SchemaVersion int             `json:"schema_version"`
	Machines      []registryEntry `json:"machines"`
}

type registryEntry struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

// registrySchemaVersion stamps the registry file, mirroring StateSchemaVersion's rule
// one file over: a future field set must be refusable, never silently reinterpreted.
const registrySchemaVersion = 1

// validMachineID refuses an id that cannot safely name a namespace directory. The id is
// the machine ENDPOINT id the durable blob authenticates; anything path-shaped here
// would let a hostile blob escape the registry root.
func validMachineID(id string) error {
	if id == "" {
		return errors.New("phonecore: empty machine id")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("phonecore: machine id %q cannot name a registry namespace", id)
	}
	// The registry file's own name: MachineDir(id) would be the committed registry's
	// path, so the namespace MkdirAll creates a directory where commitLocked must
	// rename a file -- a wedged root no retry can clear.
	if id == registryFileName {
		return fmt.Errorf("phonecore: machine id %q collides with the registry file", id)
	}
	return nil
}

// registryPath is the committed registry file under root.
func registryPath(root string) string {
	return filepath.Join(root, machinesDirName, registryFileName)
}

// NewMachineRegistry creates a fresh, LIVE, initially-empty registry under root -- the
// first-run multi-machine state, with no singleton to migrate. It refuses a root that
// already holds a live registry (overwriting one would orphan every namespace it named)
// and a root that still holds an UNMIGRATED singleton blob: committing an empty
// registry there makes Resume refuse with ErrStateMigrated -- keyed off registry-file
// existence alone -- while the registry names zero machines, bricking a pairing whose
// blob is intact on disk. The migration is that root's only doorway.
func NewMachineRegistry(root string) (*MachineRegistry, error) {
	if root == "" {
		return nil, errors.New("phonecore: NewMachineRegistry requires a root directory")
	}
	if _, err := os.Stat(registryPath(root)); err == nil {
		return nil, fmt.Errorf("phonecore: a machine registry is already live under %s", root)
	}
	if _, err := os.Stat(filepath.Join(root, StateFileName)); err == nil {
		return nil, fmt.Errorf("phonecore: %s still holds an unmigrated singleton state blob; "+
			"MigrateSingletonToRegistry is the only doorway that does not strand it", root)
	}
	r := &MachineRegistry{root: root}
	if err := r.commitLocked(); err != nil {
		return nil, err
	}
	return r, nil
}

// OpenMachineRegistry opens the COMMITTED registry under root. Until the commit is
// durable it answers ErrRegistryNotLive -- which is what makes every pre-commit crash
// leave the old singleton state authoritative.
func OpenMachineRegistry(root string) (*MachineRegistry, error) {
	data, err := os.ReadFile(registryPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("phonecore: open machine registry under %s: %w", root, ErrRegistryNotLive)
	}
	if err != nil {
		return nil, fmt.Errorf("phonecore: open machine registry: %w", err)
	}
	var f registryFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("phonecore: corrupt machine registry: %w", err)
	}
	if f.SchemaVersion > registrySchemaVersion {
		return nil, fmt.Errorf("phonecore: machine registry schema %d is newer than this build", f.SchemaVersion)
	}
	r := &MachineRegistry{root: root}
	for _, e := range f.Machines {
		r.entries = append(r.entries, MachineDescriptor(e))
	}
	r.purgeOrphanNamespaces()
	return r, nil
}

// purgeOrphanNamespaces collects namespace directories the COMMITTED registry does not
// name: RemoveMachine commits first and deletes second, so a crash in between leaves
// the forgotten pairing's sealed key material on disk with nothing referencing it.
// Best-effort by design -- a directory that cannot be removed here is cleared by
// AddMachine before any re-registration can adopt it, and failing Open over it would
// strand every healthy pairing for a hygiene error.
func (r *MachineRegistry) purgeOrphanNamespaces() {
	dirents, err := os.ReadDir(filepath.Join(r.root, machinesDirName))
	if err != nil {
		return
	}
	named := make(map[string]bool, len(r.entries))
	for _, e := range r.entries {
		named[e.ID] = true
	}
	for _, de := range dirents {
		if !de.IsDir() || named[de.Name()] {
			continue
		}
		_ = os.RemoveAll(filepath.Join(r.root, machinesDirName, de.Name()))
	}
}

// Root is the directory the registry was created under.
func (r *MachineRegistry) Root() string { return r.root }

// Entries returns the registered descriptors in registration order.
func (r *MachineRegistry) Entries() []MachineDescriptor {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]MachineDescriptor, len(r.entries))
	copy(out, r.entries)
	return out
}

// MachineDir is the per-machine namespace directory for id. It is only meaningful for a
// registered id; the path is stated rather than checked so a caller mid-AddMachine can
// use it too.
func (r *MachineRegistry) MachineDir(id string) string {
	return filepath.Join(r.root, machinesDirName, id)
}

// AddMachine creates the per-machine namespace and the durable descriptor row, and
// returns the namespace directory. A duplicate id is refused and the FIRST registration
// stands: two namespaces for one pairing is two live send sequencers (MM6 step 5).
func (r *MachineRegistry) AddMachine(d MachineDescriptor) (string, error) {
	if err := validMachineID(d.ID); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.ID == d.ID {
			return "", fmt.Errorf("phonecore: machine %q is already registered", d.ID)
		}
	}
	dir := r.MachineDir(d.ID)
	// A namespace path the committed registry does not name can only be crash residue
	// (RemoveMachine's commit-then-delete window). It holds the FORGOTTEN pairing's
	// sealed key material and durable coordinates, and a bare MkdirAll would let the
	// re-added pairing silently adopt them -- so the directory is cleared before use.
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	r.entries = append(r.entries, d)
	if err := r.commitLocked(); err != nil {
		r.entries = r.entries[:len(r.entries)-1]
		return "", err
	}
	return dir, nil
}

// RemoveMachine deletes exactly that pairing's descriptor and namespace (MM7's forget
// arm). Every other namespace is not read, not rewritten and not invalidated.
func (r *MachineRegistry) RemoveMachine(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := -1
	for i, e := range r.entries {
		if e.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("phonecore: remove machine %q: %w", id, ErrMachineNotFound)
	}
	removed := r.entries[idx]
	r.entries = append(r.entries[:idx:idx], r.entries[idx+1:]...)
	if err := r.commitLocked(); err != nil {
		r.entries = append(r.entries[:idx:idx], append([]MachineDescriptor{removed}, r.entries[idx:]...)...)
		return err
	}
	// The registry no longer names the pairing; the namespace goes with it. A crash
	// between the two leaves an orphan directory holding the forgotten pairing's
	// sealed key material -- collected by purgeOrphanNamespaces at the next Open, and
	// cleared by AddMachine before any re-registration of the same id can adopt it.
	return os.RemoveAll(r.MachineDir(id))
}

// commitLocked writes the registry file atomically. Caller holds r.mu (or exclusive
// ownership during construction).
func (r *MachineRegistry) commitLocked() error {
	f := registryFile{SchemaVersion: registrySchemaVersion, Machines: []registryEntry{}}
	for _, e := range r.entries {
		f.Machines = append(f.Machines, registryEntry(e))
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return writeFileAtomic(registryPath(r.root), ".machine-registry-*", data)
}

// ---------------------------------------------------------------------------
// Transactional migration (MM6).
// ---------------------------------------------------------------------------

// MigrationKillPoint names one instant of the migration a test can simulate process
// death at. The points partition the transaction: everything before the commit leaves
// the old state authoritative, the commit and after leave the registry fully live.
type MigrationKillPoint string

const (
	// MigKillAfterVerify: custody verified, nothing written.
	MigKillAfterVerify MigrationKillPoint = "after_verify"
	// MigKillAfterNamespace: the per-machine namespace directory exists, empty.
	MigKillAfterNamespace MigrationKillPoint = "after_namespace"
	// MigKillAfterStateCopy: the state files are copied into the namespace.
	MigKillAfterStateCopy MigrationKillPoint = "after_state_copy"
	// MigKillBeforeCommit: everything staged, the registry not yet written.
	MigKillBeforeCommit MigrationKillPoint = "before_commit"
	// MigKillAfterCommit: the registry is durably committed; whatever dies now, the
	// decision stands.
	MigKillAfterCommit MigrationKillPoint = "after_commit"
)

// MigrationConfig assembles one MigrateSingletonToRegistry run. Kill is the kill-point
// seam: called at each named MigrationKillPoint, a non-nil return simulates process
// death AT that point -- the migration stops there, having performed no write beyond
// it. Nil in production.
type MigrationConfig struct {
	Root          string
	WakeSealer    Sealer
	ContentSealer Sealer
	Kill          func(MigrationKillPoint) error
}

// kill runs the seam at point, wrapping a simulated death so the caller sees a failed
// migration exactly as a real crash-and-restart would report one.
func (cfg MigrationConfig) kill(point MigrationKillPoint) error {
	if cfg.Kill == nil {
		return nil
	}
	if err := cfg.Kill(point); err != nil {
		return fmt.Errorf("phonecore: migration died at %s: %w", point, err)
	}
	return nil
}

// MigrateSingletonToRegistry moves the singleton state under cfg.Root into a machine
// registry keyed by the AUTHENTICATED machine id the durable blob carries. See the file
// header for the six steps and the crash contract. It is safe to retry after any
// pre-commit death: a crashed attempt's namespace residue is overwritten, never
// double-registered, because only the commit writes the registry.
func MigrateSingletonToRegistry(cfg MigrationConfig) (*MachineRegistry, error) {
	if cfg.Root == "" {
		return nil, errors.New("phonecore: migration requires a root directory")
	}
	if _, err := os.Stat(registryPath(cfg.Root)); err == nil {
		// The commit already happened: the registry is authoritative and re-running the
		// copy would overwrite live namespace state with the stale singleton blob.
		return OpenMachineRegistry(cfg.Root)
	}

	// Step 1: verify custody BEFORE modifying anything. The blob must exist and open;
	// an empty machineID adopts whatever the blob authenticates.
	blobPath := filepath.Join(cfg.Root, StateFileName)
	if _, err := os.Stat(blobPath); err != nil {
		return nil, fmt.Errorf("phonecore: no singleton state to migrate: %w", err)
	}
	store, err := OpenStore(blobPath, "", cfg.WakeSealer, cfg.ContentSealer)
	if err != nil {
		return nil, fmt.Errorf("phonecore: migration custody verification failed: %w", err)
	}
	st := store.Load()
	// Step 2: the registry entry is keyed by the authenticated machine endpoint id --
	// never the display name (State.MachineName is display only).
	if err := validMachineID(st.Machine); err != nil {
		return nil, fmt.Errorf("phonecore: singleton state authenticates no migratable machine id: %w", err)
	}
	if err := cfg.kill(MigKillAfterVerify); err != nil {
		return nil, err
	}

	// Step 3: create the namespace and copy the state files into it. The sealed blobs
	// move VERBATIM, so the same Keystore-held KEKs open them in the namespace.
	nsDir := filepath.Join(cfg.Root, machinesDirName, st.Machine)
	if err := os.MkdirAll(nsDir, 0o700); err != nil {
		return nil, err
	}
	if err := cfg.kill(MigKillAfterNamespace); err != nil {
		return nil, err
	}
	if err := copyStateFiles(cfg.Root, nsDir); err != nil {
		return nil, err
	}
	if err := cfg.kill(MigKillAfterStateCopy); err != nil {
		return nil, err
	}
	if err := cfg.kill(MigKillBeforeCommit); err != nil {
		return nil, err
	}

	// Step 4: commit the registry LAST. This one durable write is what flips authority
	// from the singleton blob to the registry.
	reg := &MachineRegistry{
		root:    cfg.Root,
		entries: []MachineDescriptor{{ID: st.Machine, DisplayName: st.MachineName}},
	}
	if err := reg.commitLocked(); err != nil {
		return nil, err
	}
	// Step 5/6 need no further writes: Resume keys ErrStateMigrated off the committed
	// registry, and the old blob stays byte-identical at the root, rollback-readable. A
	// death after the commit interrupts nothing that matters.
	if err := cfg.kill(MigKillAfterCommit); err != nil {
		return reg, err
	}
	return reg, nil
}

// copyStateFiles copies every regular file directly under root into dst, byte for byte.
// The registry's own directory is skipped (it is what the copy feeds), and so are
// subdirectories: the singleton state is a flat set of files (phone-state.json,
// device.key, push-state.sealed, and their siblings).
func copyStateFiles(root, dst string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			return err
		}
		if err := writeFileAtomic(filepath.Join(dst, e.Name()), ".migrate-*", data); err != nil {
			return err
		}
	}
	return nil
}
