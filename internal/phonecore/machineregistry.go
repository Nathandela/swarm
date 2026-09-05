package phonecore

// The machine REGISTRY is the durable N-entry v2 phone layout. One root holds one
// registry file plus one namespace directory per pairing; nothing in a namespace is
// shared with another pairing. v2 starts fresh: old singleton roots and old registry
// formats are refused for reset, never imported or resumed.

import (
	"crypto/rand"
	"encoding/hex"
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
	// is the authority for the v2 registry.
	registryFileName = "machine-registry.json"
	bootstrapPrefix  = ".staging-"
)

var (
	// ErrRegistryNotLive means this root has no registry yet.
	ErrRegistryNotLive = errors.New("phonecore: no live machine registry at this root")

	// ErrLegacyStateResetRequired refuses a v1 singleton root. There is no migration.
	ErrLegacyStateResetRequired = errors.New("phonecore: legacy singleton state requires reset and fresh pairing")

	// ErrLegacyRegistryResetRequired refuses a registry written by the retired layout.
	ErrLegacyRegistryResetRequired = errors.New("phonecore: legacy machine registry requires reset and fresh pairing")
	ErrBootstrapCommitUncertain    = errors.New("phonecore: bootstrap registry commit durability is uncertain")

	// ErrStateMigrated remains for Core.Resume's existing root-directory guard while its
	// call site is removed separately; v2 bootstrap never relies on this branch.
	ErrStateMigrated = errors.New("phonecore: root state cannot be resumed while a machine registry exists")
)

// MachineRegistry is the durable registry of machine pairings under one root. All
// methods are safe for concurrent use.
type MachineRegistry struct {
	mu        sync.Mutex
	root      string
	entries   []MachineDescriptor
	namespace map[string]string
	bootstrap string
}

// registryFile is the on-disk shape of the committed registry.
type registryFile struct {
	SchemaVersion int             `json:"schema_version"`
	Machines      []registryEntry `json:"machines"`
	Bootstrap     string          `json:"bootstrap_namespace"`
}

type registryEntry struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	Namespace   string `json:"namespace"`
}

// registrySchemaVersion stamps the registry file, mirroring StateSchemaVersion's rule
// one file over: a future field set must be refusable, never silently reinterpreted.
const registrySchemaVersion = 3

var removeRegistryNamespace = os.RemoveAll

func newBootstrapNamespace() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return bootstrapPrefix + hex.EncodeToString(b), nil
}

func isBootstrapNamespace(s string) bool {
	if !strings.HasPrefix(s, bootstrapPrefix) || len(s) != len(bootstrapPrefix)+32 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(s, bootstrapPrefix))
	return err == nil
}

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
	if isBootstrapNamespace(id) || id == ".staging" {
		return fmt.Errorf("phonecore: machine id %q is reserved for bootstrap", id)
	}
	return nil
}

// registryPath is the committed registry file under root.
func registryPath(root string) string {
	return filepath.Join(root, machinesDirName, registryFileName)
}

func checkRegistryDir(root string) error {
	dir := filepath.Join(root, machinesDirName)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("phonecore: unsafe registry directory %q", dir)
	}
	return nil
}

func namespaceName(id string) (string, error) {
	if err := validMachineID(id); err != nil {
		return "", err
	}
	return id, nil
}

func namespacePath(root, namespace string) (string, error) {
	if !isBootstrapNamespace(namespace) {
		if _, err := namespaceName(namespace); err != nil {
			return "", err
		}
	}
	return filepath.Join(root, machinesDirName, namespace), nil
}

// NewMachineRegistry creates a fresh, LIVE, initially-empty registry under root -- the
// first-run multi-machine state. It refuses a root that already holds a live registry
// or a legacy singleton blob; the only supported remedy for the latter is reset and
// fresh pairing.
func NewMachineRegistry(root string) (*MachineRegistry, error) {
	if root == "" {
		return nil, errors.New("phonecore: NewMachineRegistry requires a root directory")
	}
	if _, err := os.Lstat(registryPath(root)); err == nil {
		return nil, fmt.Errorf("phonecore: a machine registry is already live under %s", root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(root, StateFileName)); err == nil {
		return nil, fmt.Errorf("phonecore: %s under %s: %w", StateFileName, root, ErrLegacyStateResetRequired)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := checkRegistryDir(root); err != nil {
		return nil, err
	}
	bootstrap, err := newBootstrapNamespace()
	if err != nil {
		return nil, err
	}
	r := &MachineRegistry{root: root, namespace: map[string]string{}, bootstrap: bootstrap}
	if err := r.commitLocked(); err != nil {
		return nil, err
	}
	return r, nil
}

// OpenMachineRegistry opens the committed v2 registry under root.
func OpenMachineRegistry(root string) (*MachineRegistry, error) {
	if err := checkRegistryDir(root); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(registryPath(root))
	if errors.Is(err, os.ErrNotExist) {
		if _, stateErr := os.Lstat(filepath.Join(root, StateFileName)); stateErr == nil {
			return nil, fmt.Errorf("phonecore: %s under %s: %w", StateFileName, root, ErrLegacyStateResetRequired)
		} else if !errors.Is(stateErr, os.ErrNotExist) {
			return nil, stateErr
		}
		return nil, fmt.Errorf("phonecore: open machine registry under %s: %w", root, ErrRegistryNotLive)
	}
	if err != nil {
		return nil, fmt.Errorf("phonecore: open machine registry: %w", err)
	}
	var f registryFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("phonecore: corrupt machine registry: %w", err)
	}
	if f.SchemaVersion != registrySchemaVersion {
		return nil, fmt.Errorf("phonecore: machine registry schema %d: %w", f.SchemaVersion, ErrLegacyRegistryResetRequired)
	}
	if !isBootstrapNamespace(f.Bootstrap) {
		return nil, fmt.Errorf("phonecore: invalid bootstrap namespace")
	}
	r := &MachineRegistry{root: root, namespace: map[string]string{}, bootstrap: f.Bootstrap}
	for _, e := range f.Machines {
		if err := validMachineID(e.ID); err != nil {
			return nil, fmt.Errorf("phonecore: invalid registry machine %q: %w", e.ID, err)
		}
		if _, exists := r.namespace[e.ID]; exists {
			return nil, fmt.Errorf("phonecore: duplicate registry machine %q", e.ID)
		}
		if _, err := namespacePath(root, e.Namespace); err != nil {
			return nil, fmt.Errorf("phonecore: invalid namespace for machine %q: %w", e.ID, err)
		}
		for owner, namespace := range r.namespace {
			if namespace == e.Namespace {
				return nil, fmt.Errorf("phonecore: machines %q and %q share namespace %q", owner, e.ID, e.Namespace)
			}
		}
		r.entries = append(r.entries, MachineDescriptor{ID: e.ID, DisplayName: e.DisplayName})
		r.namespace[e.ID] = e.Namespace
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
		named[r.namespace[e.ID]] = true
	}
	if len(r.entries) == 0 {
		// A pairing may have durably pinned the staging core immediately before
		// the registry authority flip. It is recovered explicitly by mobile, never
		// mistaken for a registered pairing.
		named[r.bootstrap] = true
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
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.machineDirLocked(id)
}

func (r *MachineRegistry) machineDirLocked(id string) string {
	namespace := r.namespace[id]
	path, _ := namespacePath(r.root, namespace)
	return path
}

// BootstrapDir returns the reserved unpaired namespace. It is not a registry entry.
func (r *MachineRegistry) BootstrapDir() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	path, _ := namespacePath(r.root, r.bootstrap)
	return path
}

// EnsureBootstrap creates the sole unpaired namespace for an empty v2 registry. It
// never reuses staging once a pairing has been forgotten: RemoveMachine deletes that
// exact namespace, and a later first install receives a new directory and keys.
func (r *MachineRegistry) EnsureBootstrap() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := checkRegistryDir(r.root); err != nil {
		return "", err
	}
	if len(r.entries) != 0 {
		return "", errors.New("phonecore: bootstrap namespace requires an empty registry")
	}
	dir, err := namespacePath(r.root, r.bootstrap)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(dir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("phonecore: unsafe bootstrap namespace %q", dir)
		}
		return dir, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// CommitBootstrap makes the reserved unpaired namespace the authenticated machine's
// only durable pairing. The registry-file write is the authority flip; the directory
// is deliberately not renamed, avoiding an uncommitted move window.
func (r *MachineRegistry) CommitBootstrap(d MachineDescriptor) error {
	if err := validMachineID(d.ID); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := checkRegistryDir(r.root); err != nil {
		return err
	}
	if len(r.entries) != 0 {
		if len(r.entries) == 1 && r.entries[0] == d && r.namespace[d.ID] == r.bootstrap {
			return r.confirmBootstrapLocked(d)
		}
		return errors.New("phonecore: bootstrap namespace is no longer available")
	}
	dir, err := namespacePath(r.root, r.bootstrap)
	if err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("phonecore: bootstrap namespace is unavailable: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("phonecore: unsafe bootstrap namespace %q", dir)
	}
	r.entries = append(r.entries, d)
	r.namespace[d.ID] = r.bootstrap
	if err := r.commitLocked(); err != nil {
		if atomicWriteCommitted(err) {
			return fmt.Errorf("%w: %w", ErrBootstrapCommitUncertain, err)
		}
		delete(r.namespace, d.ID)
		r.entries = r.entries[:len(r.entries)-1]
		return err
	}
	return nil
}

// IsBootstrapMachine reports whether id owns the registry's initial staging namespace.
func (r *MachineRegistry) IsBootstrapMachine(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.namespace[id] == r.bootstrap
}

func (r *MachineRegistry) confirmBootstrapLocked(d MachineDescriptor) error {
	if len(r.entries) != 1 || r.entries[0] != d || r.namespace[d.ID] != r.bootstrap {
		return errors.New("phonecore: bootstrap authority does not match retry")
	}
	if err := r.commitLocked(); err != nil {
		if atomicWriteCommitted(err) {
			return fmt.Errorf("%w: %w", ErrBootstrapCommitUncertain, err)
		}
		return err
	}
	return nil
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
	if err := checkRegistryDir(r.root); err != nil {
		return "", err
	}
	for _, e := range r.entries {
		if e.ID == d.ID {
			return "", fmt.Errorf("phonecore: machine %q is already registered", d.ID)
		}
		if r.namespace[e.ID] == d.ID {
			return "", fmt.Errorf("phonecore: namespace %q is owned by machine %q", d.ID, e.ID)
		}
	}
	dir, err := namespacePath(r.root, d.ID)
	if err != nil {
		return "", err
	}
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
	r.namespace[d.ID] = d.ID
	if err := r.commitLocked(); err != nil {
		if atomicWriteCommitted(err) {
			return "", err
		}
		delete(r.namespace, d.ID)
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
	namespace := r.namespace[id]
	r.entries = append(r.entries[:idx:idx], r.entries[idx+1:]...)
	oldBootstrap := r.bootstrap
	if len(r.entries) == 0 {
		nextBootstrap, err := newBootstrapNamespace()
		if err != nil {
			r.entries = append(r.entries[:idx:idx], append([]MachineDescriptor{removed}, r.entries[idx:]...)...)
			return err
		}
		r.bootstrap = nextBootstrap
	}
	if err := r.commitLocked(); err != nil {
		if atomicWriteCommitted(err) {
			delete(r.namespace, id)
			return errors.Join(err, removeRegistryNamespace(filepath.Join(r.root, machinesDirName, namespace)))
		}
		r.bootstrap = oldBootstrap
		r.entries = append(r.entries[:idx:idx], append([]MachineDescriptor{removed}, r.entries[idx:]...)...)
		return err
	}
	// The registry no longer names the pairing; the namespace goes with it. A crash
	// between the two leaves an orphan directory holding the forgotten pairing's
	// sealed key material -- collected by purgeOrphanNamespaces at the next Open, and
	// cleared by AddMachine before any re-registration of the same id can adopt it.
	dir := r.machineDirLocked(id)
	delete(r.namespace, id)
	if err := removeRegistryNamespace(dir); err != nil {
		return &atomicWriteError{err: err, committed: true}
	}
	return nil
}

// commitLocked writes the registry file atomically. Caller holds r.mu (or exclusive
// ownership during construction).
func (r *MachineRegistry) commitLocked() error {
	f := registryFile{SchemaVersion: registrySchemaVersion, Machines: []registryEntry{}}
	for _, e := range r.entries {
		f.Machines = append(f.Machines, registryEntry{ID: e.ID, DisplayName: e.DisplayName, Namespace: r.namespace[e.ID]})
	}
	f.Bootstrap = r.bootstrap
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return writeFileAtomic(registryPath(r.root), ".machine-registry-*", data)
}
