package phonecore

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 deliverable 1 (bead agents-tracker-hggx.5):
// the TRANSACTIONAL MIGRATION of the current single-machine state into a machine
// registry, ADR-018 MM6 / playbook section 12's six Android-state steps (:939-947).
//
// THE CONTRACT UNDER TEST (all symbols deliberately undefined today -- this file IS the
// frozen surface the implementation must supply):
//
//   - MigrateSingletonToRegistry(MigrationConfig) (*MachineRegistry, error): reads the
//     existing singleton state at Root, VERIFIES CUSTODY BEFORE MODIFYING ANYTHING
//     (step 1), creates a registry entry keyed by the AUTHENTICATED machine id from the
//     durable blob (step 2, never the display name), moves state into a per-machine
//     namespace (step 3), and COMMITS THE REGISTRY LAST (step 4).
//   - MigrationConfig.Kill is the kill-point seam: called at each named
//     MigrationKillPoint; a non-nil return simulates process death AT that point --
//     the migration stops there, having performed no write beyond it.
//   - OpenMachineRegistry(root): opens the committed registry, ErrRegistryNotLive
//     until the commit is durable.
//   - ErrStateMigrated: Resume over the OLD singleton dir refuses once the registry is
//     live. This is MM6's load-bearing prohibition -- "never produce two live send
//     sequencers for one pairing" -- as a sentinel, because a re-issued seq under a
//     retained epoch is stale-dropped by the gateway permanently
//     (s9_machineid_test.go:16-19).
//
// CRASH SEMANTICS PINNED: at every kill point the world afterwards is EITHER the old
// state fully intact and authoritative (points before the commit) OR the new registry
// fully live (the commit and after) -- never both live, never neither readable.

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// r4Phone is a provisionable singleton phone: one state dir, one machine id, the two
// tier sealers held stable across resumes exactly as Android's Keystore is.
type r4Phone struct {
	dir     string
	machine string
	wake    *s14aSealer
	content *s14aSealer
}

func newR4Phone(t *testing.T, machine string) *r4Phone {
	t.Helper()
	return &r4Phone{dir: t.TempDir(), machine: machine, wake: s14aNewSealer(t), content: s14aNewSealer(t)}
}

func (p *r4Phone) resume(t *testing.T) *Core {
	t.Helper()
	core, err := p.tryResume()
	if err != nil {
		t.Fatalf("Resume(%s): %v", p.machine, err)
	}
	return core
}

func (p *r4Phone) tryResume() (*Core, error) {
	return Resume(Config{Dir: p.dir, Machine: p.machine, WakeSealer: p.wake, ContentSealer: p.content})
}

// resumeAt resumes the same custody over a DIFFERENT directory -- the post-migration
// per-machine namespace.
func (p *r4Phone) resumeAt(t *testing.T, dir string) *Core {
	t.Helper()
	core, err := Resume(Config{Dir: dir, Machine: p.machine, WakeSealer: p.wake, ContentSealer: p.content})
	if err != nil {
		t.Fatalf("Resume(%s) at %s: %v", p.machine, dir, err)
	}
	return core
}

// provisionSingleton gives the phone real durable coordinates worth losing: a push
// binding with one accepted wake, a nonzero relay cursor, and a send-seq ceiling.
// Returns the binding so post-migration wakes can be sealed under the same key.
func (p *r4Phone) provisionSingleton(t *testing.T) (PushAddress, crypto.WakeKey) {
	t.Helper()
	core := p.resume(t)
	addr, key := r3aBinding(t, core, 0xA4)
	if err := core.AcceptWakeV1(r3aSeal(t, key, addr, 1, time.Now())); err != nil {
		t.Fatalf("provisioning wake: %v", err)
	}
	if err := core.Mutate(func(st *State) {
		st.RelayCursor = 42
		if st.SendSeq == nil {
			st.SendSeq = map[uint32]uint64{}
		}
		st.SendSeq[st.EpochID] = 100
	}); err != nil {
		t.Fatalf("provisioning durable coordinates: %v", err)
	}
	return addr, key
}

// r4MigrationKills is the pre-commit half of the kill matrix: after a crash at any of
// these, the OLD state is authoritative and the registry is NOT live.
var r4MigrationKills = []MigrationKillPoint{
	MigKillAfterVerify,
	MigKillAfterNamespace,
	MigKillAfterStateCopy,
	MigKillBeforeCommit,
}

// killAt returns a MigrationConfig.Kill seam that dies at exactly the named point.
func killAt(point MigrationKillPoint) func(MigrationKillPoint) error {
	return func(p MigrationKillPoint) error {
		if p == point {
			return fmt.Errorf("simulated process death at %s", p)
		}
		return nil
	}
}

func (p *r4Phone) migrationConfig(kill func(MigrationKillPoint) error) MigrationConfig {
	return MigrationConfig{Root: p.dir, WakeSealer: p.wake, ContentSealer: p.content, Kill: kill}
}

// hashDir hashes every regular file under root (path + content), so "verified before
// mutating" and "one pairing's namespace untouched" are byte-level claims, not vibes.
func hashDir(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(h, "%s\n%x\n", rel, sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatalf("hashing %s: %v", root, err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// TestR4_Migration_MovesSingletonIntoRegistryKeyedByAuthenticatedMachineID: the happy
// path, playbook steps 1-4 end to end. The registry's sole entry is keyed by the machine
// endpoint id the durable blob authenticates, and the per-machine namespace resumes with
// every durable coordinate intact -- epoch, send-seq ceiling, relay cursor, and a push
// binding that still accepts the next wake under the SAME key.
func TestR4_Migration_MovesSingletonIntoRegistryKeyedByAuthenticatedMachineID(t *testing.T) {
	phone := newR4Phone(t, "m-alpha")
	addr, key := phone.provisionSingleton(t)

	reg, err := MigrateSingletonToRegistry(phone.migrationConfig(nil))
	if err != nil {
		t.Fatalf("MigrateSingletonToRegistry: %v", err)
	}

	entries := reg.Entries()
	if len(entries) != 1 {
		t.Fatalf("registry holds %d entries after migrating one singleton, want 1", len(entries))
	}
	if entries[0].ID != "m-alpha" {
		t.Errorf("registry entry keyed %q, want the AUTHENTICATED machine id %q (MM6 step 2: "+
			"not the configured string, not the display name)", entries[0].ID, "m-alpha")
	}

	ns := reg.MachineDir("m-alpha")
	if ns == phone.dir {
		t.Fatalf("MachineDir returned the singleton root itself; the namespace must be per-machine")
	}
	core := phone.resumeAt(t, ns)
	st := core.State()
	if st.Machine != "m-alpha" {
		t.Errorf("migrated namespace resumes machine %q, want %q", st.Machine, "m-alpha")
	}
	if got := st.SendSeq[st.EpochID]; got != 100 {
		t.Errorf("send-seq ceiling after migration: %d, want the pre-migration 100 -- a reset "+
			"ceiling re-issues seqs the gateway has already stale-dropped forever", got)
	}
	if st.RelayCursor != 42 {
		t.Errorf("relay cursor after migration: %d, want 42", st.RelayCursor)
	}
	if err := core.AcceptWakeV1(r3aSeal(t, key, addr, 2, time.Now())); err != nil {
		t.Errorf("the migrated binding refused the next wake under the SAME key: %v "+
			"(keys must move Keystore-wrapped, step 3)", err)
	}
}

// TestR4_Migration_KillBeforePointOfCommitLeavesOldStateAuthoritative: the pre-commit
// half of the kill matrix. At every kill point before the registry commit: the
// migration reports the death, the registry is NOT live, the OLD dir still resumes with
// every coordinate intact (never ErrStateMigrated), and a retry with no kill succeeds.
func TestR4_Migration_KillBeforePointOfCommitLeavesOldStateAuthoritative(t *testing.T) {
	for _, point := range r4MigrationKills {
		t.Run(string(point), func(t *testing.T) {
			phone := newR4Phone(t, "m-kill")
			addr, key := phone.provisionSingleton(t)

			if _, err := MigrateSingletonToRegistry(phone.migrationConfig(killAt(point))); err == nil {
				t.Fatalf("a migration killed at %s reported success", point)
			}

			if _, err := OpenMachineRegistry(phone.dir); !errors.Is(err, ErrRegistryNotLive) {
				t.Errorf("after a crash at %s the registry answered %v, want ErrRegistryNotLive: "+
					"the commit is LAST (MM6 step 4), so no pre-commit crash may leave a live registry", point, err)
			}

			core, err := phone.tryResume()
			if err != nil {
				t.Fatalf("after a crash at %s the OLD state did not resume: %v -- the old blob "+
					"must stay authoritative (MM6 step 5's read-only fallback)", point, err)
			}
			st := core.State()
			if got := st.SendSeq[st.EpochID]; got != 100 {
				t.Errorf("old-state send-seq ceiling after crash at %s: %d, want 100", point, got)
			}
			if err := core.AcceptWakeV1(r3aSeal(t, key, addr, 2, time.Now())); err != nil {
				t.Errorf("old-state binding refused a wake after crash at %s: %v", point, err)
			}

			// The retry is the product's own remedy (step 5: "offer retry").
			reg, err := MigrateSingletonToRegistry(phone.migrationConfig(nil))
			if err != nil {
				t.Fatalf("retry after crash at %s: %v", point, err)
			}
			if got := len(reg.Entries()); got != 1 {
				t.Errorf("retry after crash at %s left %d registry entries, want 1 -- a crashed "+
					"attempt's residue must not double-register the pairing", point, got)
			}
		})
	}
}

// TestR4_Migration_KillAfterCommitLeavesRegistryFullyLive: the post-commit half. A crash
// after the registry commit leaves the registry FULLY live: OpenMachineRegistry succeeds,
// the namespace resumes with the coordinates, and the old singleton path REFUSES with
// ErrStateMigrated -- the crash may interrupt cleanup, never the decision.
func TestR4_Migration_KillAfterCommitLeavesRegistryFullyLive(t *testing.T) {
	phone := newR4Phone(t, "m-postcommit")
	addr, key := phone.provisionSingleton(t)

	if _, err := MigrateSingletonToRegistry(phone.migrationConfig(killAt(MigKillAfterCommit))); err == nil {
		t.Fatalf("a migration killed at %s reported success", MigKillAfterCommit)
	}

	reg, err := OpenMachineRegistry(phone.dir)
	if err != nil {
		t.Fatalf("after a crash at %s the committed registry did not open: %v -- committed means "+
			"LIVE, whatever died afterwards", MigKillAfterCommit, err)
	}
	if got := len(reg.Entries()); got != 1 {
		t.Fatalf("committed registry holds %d entries, want 1", got)
	}
	core := phone.resumeAt(t, reg.MachineDir("m-postcommit"))
	if got := core.State().SendSeq[core.State().EpochID]; got != 100 {
		t.Errorf("post-commit namespace send-seq ceiling: %d, want 100", got)
	}
	if err := core.AcceptWakeV1(r3aSeal(t, key, addr, 2, time.Now())); err != nil {
		t.Errorf("post-commit namespace binding refused a wake: %v", err)
	}

	if _, err := phone.tryResume(); !errors.Is(err, ErrStateMigrated) {
		t.Errorf("Resume over the OLD singleton dir after the commit: got %v, want ErrStateMigrated "+
			"-- anything else is two live send sequencers for one pairing (MM6 step 5), and a "+
			"re-issued seq under a retained epoch is stale-dropped by the gateway permanently", err)
	}
}

// TestR4_Migration_OldBlobStaysRollbackReadableAfterCommit: MM6 step 6's one-release-
// train rollback window. After a successful migration the original singleton state blob
// still exists at the root, byte-identical to its pre-migration self -- readable by the
// previous app version, never live in this one.
func TestR4_Migration_OldBlobStaysRollbackReadableAfterCommit(t *testing.T) {
	phone := newR4Phone(t, "m-rollback")
	phone.provisionSingleton(t)

	oldBlobPath := filepath.Join(phone.dir, StateFileName)
	before, err := os.ReadFile(oldBlobPath)
	if err != nil {
		t.Fatalf("reading the pre-migration blob: %v", err)
	}

	if _, err := MigrateSingletonToRegistry(phone.migrationConfig(nil)); err != nil {
		t.Fatalf("MigrateSingletonToRegistry: %v", err)
	}

	after, err := os.ReadFile(oldBlobPath)
	if err != nil {
		t.Fatalf("the old blob is GONE after migration: %v -- step 6 keeps it rollback-readable "+
			"for at least one stable release train", err)
	}
	if string(before) != string(after) {
		t.Errorf("the old blob was REWRITTEN by the migration; rollback-readable means byte-identical")
	}
}

// TestR4_Migration_VerifiesCustodyBeforeMutatingAnything: MM6 step 1. A migration
// pointed at a corrupt singleton blob refuses, and the root directory is byte-identical
// afterwards -- no namespace, no registry stub, no partial move.
func TestR4_Migration_VerifiesCustodyBeforeMutatingAnything(t *testing.T) {
	phone := newR4Phone(t, "m-corrupt")
	phone.provisionSingleton(t)

	blob := filepath.Join(phone.dir, StateFileName)
	if err := os.WriteFile(blob, []byte("{this is not the blob"), 0o600); err != nil {
		t.Fatalf("corrupting the blob: %v", err)
	}
	before := hashDir(t, phone.dir)

	if _, err := MigrateSingletonToRegistry(phone.migrationConfig(nil)); err == nil {
		t.Fatalf("a migration over unverifiable custody reported success")
	}
	if after := hashDir(t, phone.dir); after != before {
		t.Errorf("a REFUSED migration modified the state directory: custody is verified BEFORE " +
			"anything is modified (MM6 step 1), so a refusal must leave every byte in place")
	}
	if _, err := OpenMachineRegistry(phone.dir); !errors.Is(err, ErrRegistryNotLive) {
		t.Errorf("a refused migration left a registry behind")
	}
}
