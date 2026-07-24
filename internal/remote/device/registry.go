package device

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// devicesFile is the single durable registry file under the registry directory.
const devicesFile = "devices.json"

// registrySchemaVersion stamps the on-disk envelope for forward migration.
const registrySchemaVersion = 1

// DeviceIDFor derives the canonical device id from the device's Ed25519
// command-signing public key: the hex SHA-256 of the key. It is deterministic and
// collision-avoiding, and it binds the device id to exactly the key R-POL.9 verifies
// command signatures against.
func DeviceIDFor(commandSignPub []byte) string {
	sum := sha256.Sum256(commandSignPub)
	return hex.EncodeToString(sum[:])
}

// Record is one paired device pinned in the registry (R-DEV.1): its identity keys,
// routing/name, capability tier, pairing time, and the epoch it was granted at.
type Record struct {
	DeviceID       string     `json:"device_id"`
	Name           string     `json:"name"`
	NoiseStaticPub []byte     `json:"noise_static_pub"`
	RelayAuthPub   []byte     `json:"relay_auth_pub"`
	CommandSignPub []byte     `json:"command_sign_pub"`
	RecipientPub   []byte     `json:"recipient_pub"`
	RoutingID      []byte     `json:"routing_id"`
	Capability     Capability `json:"capability"`
	PairedAt       time.Time  `json:"paired_at"`
	GrantedEpoch   uint32     `json:"granted_epoch"`
}

// envelope is the versioned on-disk container so the format can migrate forward.
type envelope struct {
	SchemaVersion int      `json:"schema_version"`
	Devices       []Record `json:"devices"`
}

// Registry is the durable, mutex-guarded device store (R-DEV.1). Its in-memory map
// is always consistent with the single devices.json on disk: every mutation persists
// atomically (temp+rename+Sync) and rolls back the in-memory change on a write error.
type Registry struct {
	dir  string
	path string
	mu   sync.Mutex
	byID map[string]Record
}

// Open opens (creating the directory if needed) the device registry rooted at dir,
// loading any previously persisted devices. A malformed registry file is an error
// (fail-closed): the daemon must not silently start with zero paired devices, which
// would both drop remote access and, via R-KS.2, flip the kill switch.
func Open(dir string) (*Registry, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	r := &Registry{
		dir:  dir,
		path: filepath.Join(dir, devicesFile),
		byID: map[string]Record{},
	}
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("device: malformed registry %s: %w", r.path, err)
	}
	// A version this build did not write is refused loudly, never loaded: a future
	// schema could change a record's meaning (e.g. add a revoked flag or narrow a
	// capability), and silently ignoring the unknown fields would grant authority the
	// writer did not intend. An unstamped (0) file was never legitimately written.
	if env.SchemaVersion != registrySchemaVersion {
		return nil, fmt.Errorf("device: registry %s schema version %d unsupported (want %d)", r.path, env.SchemaVersion, registrySchemaVersion)
	}
	// A pre-existing file may have a loosened mode; reharden it to 0600.
	_ = os.Chmod(r.path, 0o600)
	for _, rec := range env.Devices {
		// A persisted record with an invalid key or capability is rejected loudly
		// rather than admitted -- the registry is the R-POL.9 authorization
		// authority, so a malformed identity must never load (fail-closed).
		if err := validateRecord(rec); err != nil {
			return nil, fmt.Errorf("device: invalid persisted record %q: %w", rec.DeviceID, err)
		}
		r.byID[rec.DeviceID] = cloneRecord(rec)
	}
	return r, nil
}

// validateRecord enforces the admission invariants: a non-empty device id, a
// 32-byte Ed25519 command-signing key, and a known capability tier. A device whose
// command-signing key is malformed could never have a verifiable signature, so
// admitting it would be a fail-open hole in R-POL.9.
func validateRecord(rec Record) error {
	if rec.DeviceID == "" {
		return errors.New("empty device id")
	}
	if len(rec.CommandSignPub) != ed25519.PublicKeySize {
		return fmt.Errorf("command-signing key must be %d bytes, got %d", ed25519.PublicKeySize, len(rec.CommandSignPub))
	}
	// The device id must be the canonical derivation of the command-signing key, so
	// a record's id is self-authenticating: a device cannot be admitted (or shadow
	// another) under an id unrelated to the key R-POL.9 verifies its commands against.
	if rec.DeviceID != DeviceIDFor(rec.CommandSignPub) {
		return fmt.Errorf("device id %q does not match its command-signing key", rec.DeviceID)
	}
	// The other pinned identity keys are all 32-byte X25519/Ed25519 public keys
	// (Noise-static, relay-auth, sealed-box recipient). A malformed one is refused at
	// admission, not left to fail obscurely downstream (e.g. a nil recipient key
	// leaves EpochGrant sealing with no target).
	for _, k := range []struct {
		name string
		val  []byte
	}{
		{"noise-static", rec.NoiseStaticPub},
		{"relay-auth", rec.RelayAuthPub},
		{"recipient", rec.RecipientPub},
	} {
		if len(k.val) != 32 {
			return fmt.Errorf("%s key must be 32 bytes, got %d", k.name, len(k.val))
		}
	}
	if !rec.Capability.valid() {
		return fmt.Errorf("invalid capability %d", uint8(rec.Capability))
	}
	return nil
}

// Add validates and upserts one device, persisting durably. It rejects (without
// persisting) a record with an empty device id, a non-32-byte command-signing key,
// or an unknown capability. On a persistence error the in-memory map is rolled back
// so it never diverges from disk.
func (r *Registry) Add(rec Record) error {
	if err := validateRecord(rec); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prev, had := r.byID[rec.DeviceID]
	r.byID[rec.DeviceID] = cloneRecord(rec)
	committed, err := r.persistLocked()
	if err != nil && !committed {
		// Pre-rename failure: disk is unchanged, so roll memory back to match it.
		if had {
			r.byID[rec.DeviceID] = prev
		} else {
			delete(r.byID, rec.DeviceID)
		}
		return err
	}
	// err==nil (durable) or committed post-rename dir-fsync error: the on-disk roster
	// already reflects rec, so memory stays as-is and any error is surfaced.
	return err
}

// AddSole atomically enrolls rec as the registry's SOLE device: under the registry mutex
// it rejects the commit when a DIFFERENT device is already present, so two concurrent
// single-device enrollments that both passed a Count()==0 pre-check cannot both commit
// (finding C1 -- a non-atomic check-then-Add would leave two devices and brick the
// gateway). Re-adding the SAME device id is an idempotent upsert. Like Add it validates,
// persists durably, and rolls back the in-memory change on a write error. The general Add
// stays uncapped; the single-device cap lives ONLY here, so the registry's multi-device
// tests are unaffected.
func (r *Registry) AddSole(rec Record) error {
	if err := validateRecord(rec); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.byID {
		if id != rec.DeviceID {
			return fmt.Errorf("device: a different device is already paired (single-device v1)")
		}
	}
	prev, had := r.byID[rec.DeviceID]
	r.byID[rec.DeviceID] = cloneRecord(rec)
	committed, err := r.persistLocked()
	if err != nil && !committed {
		// Pre-rename failure: disk is unchanged, so roll memory back to match it.
		if had {
			r.byID[rec.DeviceID] = prev
		} else {
			delete(r.byID, rec.DeviceID)
		}
		return err
	}
	// err==nil (durable) or committed post-rename dir-fsync error: the on-disk roster
	// already reflects rec, so memory stays as-is and any error is surfaced.
	return err
}

// Get returns a copy of the record for deviceID, or ok=false if unknown.
func (r *Registry) Get(deviceID string) (Record, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byID[deviceID]
	if !ok {
		return Record{}, false
	}
	return cloneRecord(rec), true
}

// List returns all records, deterministically ordered by device id.
func (r *Registry) List() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sortedLocked()
}

// Remove deletes deviceID, reporting whether a record was present. Removing an
// absent id is a no-op reporting false, not an error. On a persistence error the
// removal is rolled back.
func (r *Registry) Remove(deviceID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev, ok := r.byID[deviceID]
	if !ok {
		return false, nil
	}
	delete(r.byID, deviceID)
	committed, err := r.persistLocked()
	if err != nil && !committed {
		// Pre-rename failure: disk still holds the record, so restore memory to match.
		r.byID[deviceID] = prev
		return false, err
	}
	// err==nil (durable) or committed post-rename dir-fsync error: the on-disk roster
	// already reflects the removal, so memory stays removed and the removal is reported,
	// surfacing any dir-fsync error.
	return true, err
}

// Authorized reports whether the device may perform action a. It is fail-closed: an
// unknown device is authorized for nothing.
func (r *Registry) Authorized(deviceID string, a Action) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byID[deviceID]
	if !ok {
		return false
	}
	return rec.Capability.Allows(a)
}

// Count returns the number of registered devices.
func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
}

// sortedLocked returns copies of every record ordered by device id. Caller holds mu.
func (r *Registry) sortedLocked() []Record {
	out := make([]Record, 0, len(r.byID))
	for _, rec := range r.byID {
		out = append(out, cloneRecord(rec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceID < out[j].DeviceID })
	return out
}

// persistLocked writes the whole registry atomically (temp+Sync+rename, 0600),
// mirroring internal/persist's process-crash durability model. Caller holds mu.
//
// It reports committed to distinguish a PRE-rename failure from a POST-rename one
// (finding codex#5, durability -- the same pre/post distinction idempotency.rewriteLocked
// makes). committed is false while any failure could still be rolled back cleanly (the
// on-disk roster is unchanged: marshal/create/write/sync/rename all failed before the
// atomic swap). Once os.Rename lands, the NEW roster IS on disk, so committed is true even
// if the trailing dir-fsync errors: the caller must NOT roll the in-memory change back
// (that would leave memory holding the OLD roster while disk holds the NEW one). A
// post-rename dir-fsync error is still surfaced -- durable-enough, just not
// dir-fsync-confirmed -- but with committed=true so memory stays aligned with the renamed
// inode.
func (r *Registry) persistLocked() (committed bool, err error) {
	env := envelope{SchemaVersion: registrySchemaVersion, Devices: r.sortedLocked()}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(r.dir, devicesFile+".tmp*") // os.CreateTemp creates 0600
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename; cleans up on any error
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, r.path); err != nil {
		return false, err
	}
	// Finding 4 (re-audit, durability): fsync the registry dir so the rename is durable across
	// power loss (mirrors grant.Save). Without it a crash could revert the registry to a stale
	// roster -- e.g. resurrecting a just-revoked device (reopening the R-POL.9 authorization it
	// lost) or dropping a just-added one. The rename has already landed, so the change is
	// committed: surface any dir-fsync error but do NOT let the caller roll memory back.
	return true, syncDir(r.dir)
}

// syncDir fsyncs dir so a preceding rename within it is durable across a crash
// (mirrors idempotency.syncDir / grant.Save). A package var so a test can inject a
// failure that fires AFTER a successful rename, exercising the post-rename path.
var syncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// cloneRecord deep-copies a record's byte slices so the registry's internal state
// can never be mutated through a returned or stored value.
func cloneRecord(rec Record) Record {
	rec.NoiseStaticPub = append([]byte(nil), rec.NoiseStaticPub...)
	rec.RelayAuthPub = append([]byte(nil), rec.RelayAuthPub...)
	rec.CommandSignPub = append([]byte(nil), rec.CommandSignPub...)
	rec.RecipientPub = append([]byte(nil), rec.RecipientPub...)
	rec.RoutingID = append([]byte(nil), rec.RoutingID...)
	return rec
}
