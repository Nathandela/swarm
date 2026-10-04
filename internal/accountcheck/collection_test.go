//go:build linux

package accountcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

func collectionFixture(t *testing.T) (string, *os.Root, accounts.Binding) {
	t.Helper()
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := openChecks(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	binding := accounts.Binding{SchemaVersion: 1, Provider: accounts.ProviderCodex, AccountID: strings.Repeat("a", 32), CredentialGeneration: 1, ConfigurationGeneration: 1, Identity: strings.Repeat("b", 64)}
	lock, err := root.OpenFile(generationLockName(binding), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	return state, root, binding
}

func stoppedCollectionFixture(t *testing.T, root *os.Root, binding accounts.Binding, n int) Ref {
	t.Helper()
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ref := Ref{SchemaVersion: SchemaVersion, Generation: fmt.Sprintf("%032x", n), Worker: processcontain.Identity{PID: os.Getpid(), StartTime: start + int64(n) + 1}, Binding: binding, Mode: ModeAuthStatus}
	if err := root.Mkdir(ref.Generation, 0o700); err != nil {
		t.Fatal(err)
	}
	files, err := openGeneration(root, ref.Generation)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.Close() }()
	for name, value := range map[string]any{WorkerFile: ref, StoppedFile: Stopped{SchemaVersion: SchemaVersion, Ref: ref, WritersStopped: true}} {
		if err := writeJSON(files, name, value); err != nil {
			t.Fatal(err)
		}
	}
	return ref
}

func collectFixture(t *testing.T, collector *Collector, retained []CustodyReference) int {
	t.Helper()
	n, err := collector.Step(retained)
	if err != nil {
		t.Fatal(err)
	}
	if n > collectionRemovals {
		t.Fatal("unbounded collection step", n)
	}
	return n
}

func TestCollectionRetainsReferencesAndGenerationLock(t *testing.T) {
	state, root, binding := collectionFixture(t)
	one := stoppedCollectionFixture(t, root, binding, 1)
	two := stoppedCollectionFixture(t, root, binding, 2)
	lockBefore, _ := root.Lstat(generationLockName(binding))
	c := NewCollector(state)
	defer c.Close()
	if n := collectFixture(t, c, []CustodyReference{{Binding: binding}}); n != 0 {
		t.Fatal("whole-generation reference collected", n)
	}
	c.Close()
	if n := collectFixture(t, c, []CustodyReference{{Binding: binding, Worker: one.Worker}}); n != 1 {
		t.Fatal("exact reference should retain only its worker", n)
	}
	if _, err := root.Lstat(one.Generation); err != nil {
		t.Fatal("referenced custody lost", err)
	}
	if _, err := root.Lstat(two.Generation); !os.IsNotExist(err) {
		t.Fatal("unreferenced custody retained", err)
	}
	lockAfter, err := root.Lstat(generationLockName(binding))
	if err != nil || !os.SameFile(lockBefore, lockAfter) {
		t.Fatal("shared flock inode changed")
	}
	c.Close()
	c = NewCollector(state)
	defer c.Close()
	if n := collectFixture(t, c, nil); n != 1 {
		t.Fatal("released custody did not collect", n)
	}
}

func TestCollectionNeverWaitsForAdmittingWorker(t *testing.T) {
	state, root, binding := collectionFixture(t)
	ref := stoppedCollectionFixture(t, root, binding, 1)
	lock, err := root.OpenFile(generationLockName(binding), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	c := NewCollector(state)
	defer c.Close()
	start := time.Now()
	if n := collectFixture(t, c, nil); n != 0 {
		t.Fatal("busy admission collected")
	}
	if time.Since(start) > time.Second {
		t.Fatal("collection blocked recovery owner")
	}
	if _, err := root.Lstat(ref.Generation); err != nil {
		t.Fatal(err)
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	c.Close()
	if n := collectFixture(t, c, nil); n != 1 {
		t.Fatal("unlocked check not collected")
	}
}

func TestCollectionPreservesLiveUnknownAndUnsafeCustody(t *testing.T) {
	for _, mode := range []string{"worker-live", "native-live", "missing-proof", "bad-binding", "fifo", "foreign-inventory", "missing-lock"} {
		t.Run(mode, func(t *testing.T) {
			state, root, binding := collectionFixture(t)
			ref := stoppedCollectionFixture(t, root, binding, 1)
			files, err := openGeneration(root, ref.Generation)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = files.Close() }()
			proof := Stopped{SchemaVersion: SchemaVersion, Ref: ref, WritersStopped: true}
			start, _ := procstart.StartTime(os.Getpid())
			switch mode {
			case "worker-live":
				ref.Worker = processcontain.Identity{PID: os.Getpid(), StartTime: start}
				proof.Ref = ref
				_ = writeJSON(files, WorkerFile, ref)
				_ = writeJSON(files, StoppedFile, proof)
			case "native-live":
				proof.Native = processcontain.Identity{PID: os.Getpid(), StartTime: start}
				_ = writeJSON(files, StoppedFile, proof)
			case "missing-proof":
				_ = files.Remove(StoppedFile)
			case "bad-binding":
				ref.Binding.Identity = "wrong"
				proof.Ref = ref
				_ = writeJSON(files, WorkerFile, ref)
				_ = writeJSON(files, StoppedFile, proof)
			case "fifo":
				_ = syscall.Mkfifo(filepath.Join(state, "accounts", "checks", ref.Generation, "pipe"), 0o600)
			case "foreign-inventory":
				_ = os.WriteFile(filepath.Join(state, "accounts", "checks", ref.Generation, "unknown"), []byte("retained"), 0o600)
			case "missing-lock":
				_ = root.Remove(generationLockName(binding))
			}
			c := NewCollector(state)
			defer c.Close()
			if n := collectFixture(t, c, nil); n != 0 {
				t.Fatal("uncertain custody collected", n)
			}
			if _, err := root.Lstat(ref.Generation); err != nil {
				t.Fatal("custody lost", err)
			}
		})
	}
}

func TestCollectionCrashReplayPreservesExternalAuthority(t *testing.T) {
	for _, phase := range []string{"intent", "moved", "partial", "deleted"} {
		t.Run(phase, func(t *testing.T) {
			state, root, binding := collectionFixture(t)
			ref := stoppedCollectionFixture(t, root, binding, 1)
			files, err := openGeneration(root, ref.Generation)
			if err != nil {
				t.Fatal(err)
			}
			if err := files.Mkdir("native-cwd", 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(state, "accounts", "checks", ref.Generation, "native-cwd", "owned"), []byte("scratch"), 0o600); err != nil {
				t.Fatal(err)
			}
			inventory, err := collectionInventory(files)
			_ = files.Close()
			if err != nil {
				t.Fatal(err)
			}
			intent := CollectionIntent{SchemaVersion: CollectionSchemaVersion, Ref: ref, Stopped: Stopped{SchemaVersion: SchemaVersion, Ref: ref, WritersStopped: true}, Files: inventory}
			parent, err := os.OpenRoot(filepath.Join(state, "accounts"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = parent.Close() }()
			gc, err := openCollectionRoot(parent, true)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gc.Close() }()
			if err := writeJSON(gc, ref.Generation+".json", intent); err != nil {
				t.Fatal(err)
			}
			if phase != "intent" {
				if err := parent.Rename("checks/"+ref.Generation, CollectionDirectory+"/"+ref.Generation); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "partial" {
				if err := gc.Remove(ref.Generation + "/" + WorkerFile); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "deleted" {
				if err := os.RemoveAll(filepath.Join(state, "accounts", CollectionDirectory, ref.Generation)); err != nil {
					t.Fatal(err)
				}
			}
			c := NewCollector(state)
			defer c.Close()
			if n := collectFixture(t, c, []CustodyReference{{Binding: binding}}); n != 0 {
				t.Fatal("reference returned during crash replay")
			}
			if _, err := gc.Lstat(ref.Generation + ".json"); err != nil {
				t.Fatal("external authority lost", err)
			}
			c.Close()
			c = NewCollector(state)
			defer c.Close()
			if n := collectFixture(t, c, nil); n != 1 {
				t.Fatal("crash recovery did not finish", n)
			}
			if _, err := gc.Lstat(ref.Generation + ".json"); !os.IsNotExist(err) {
				t.Fatal("intent retained after durable deletion", err)
			}
			if _, err := root.Lstat(ref.Generation); !os.IsNotExist(err) {
				t.Fatal("source survived", err)
			}
		})
	}
}

func TestCollectionDirectorySyncFailuresKeepDeletionAuthority(t *testing.T) {
	for failure := 1; failure <= 5; failure++ {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			state, checks, binding := collectionFixture(t)
			ref := stoppedCollectionFixture(t, checks, binding, 1)
			files, err := openGeneration(checks, ref.Generation)
			if err != nil {
				t.Fatal(err)
			}
			inventory, err := collectionInventory(files)
			_ = files.Close()
			if err != nil {
				t.Fatal(err)
			}
			intent := CollectionIntent{SchemaVersion: CollectionSchemaVersion, Ref: ref, Stopped: Stopped{SchemaVersion: SchemaVersion, Ref: ref, WritersStopped: true}, Files: inventory}
			parent, err := os.OpenRoot(filepath.Join(state, "accounts"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = parent.Close() }()
			gc, err := openCollectionRoot(parent, true)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gc.Close() }()
			if err := writeJSON(gc, ref.Generation+".json", intent); err != nil {
				t.Fatal(err)
			}
			calls := 0
			var faultInode os.FileInfo
			fault := errors.New("synthetic directory fsync failure")
			confirm := func(root *os.Root) error {
				info, err := root.Lstat(".")
				if err != nil {
					return err
				}
				calls++
				if calls == failure {
					faultInode = info
				}
				if faultInode != nil && os.SameFile(faultInode, info) {
					return fault
				}
				return syncRoot(root)
			}
			for retry := 0; retry < 2; retry++ {
				if done, err := collectIntent(parent, checks, gc, intent, nil, confirm); done || !errors.Is(err, fault) {
					t.Fatal("sync uncertainty acknowledged", done, err)
				}
				if _, err := gc.Lstat(ref.Generation + ".json"); err != nil {
					t.Fatal("exact deletion authority lost", err)
				}
			}
			if failure <= 3 {
				base := checks
				if failure > 1 {
					base = gc
				}
				if _, err := base.Lstat(ref.Generation + "/" + StoppedFile); err != nil {
					t.Fatal("proof erased before move parents confirmed", err)
				}
			}
			if done, err := collectIntent(parent, checks, gc, intent, nil, syncRoot); !done || err != nil {
				t.Fatal("confirmed replay failed", done, err)
			}
			c := NewCollector(state)
			defer c.Close()
			if n := collectFixture(t, c, nil); n != 0 {
				t.Fatal("completed intent replayed twice", n)
			}
		})
	}
}

func TestCollectionRecoversOverLimitWithoutUnlinkingLocks(t *testing.T) {
	state, root, binding := collectionFixture(t)
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 4097; n++ {
		// Seed old synthetic custody in one batch; the collector itself must
		// exercise real strong writes and parent syncs for each removal.
		ref := Ref{SchemaVersion: SchemaVersion, Generation: fmt.Sprintf("%032x", n), Worker: processcontain.Identity{PID: os.Getpid(), StartTime: start + int64(n) + 1}, Binding: binding, Mode: ModeAuthStatus}
		path := filepath.Join(state, "accounts", "checks", ref.Generation)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]any{WorkerFile: ref, StoppedFile: Stopped{SchemaVersion: SchemaVersion, Ref: ref, WritersStopped: true}} {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, name), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if writersStopped(root, binding) {
		t.Fatal("new admission should remain fail closed over inventory limit")
	}
	c := NewCollector(state)
	defer c.Close()
	if removed := collectFixture(t, c, nil); removed == 0 {
		t.Fatal("bounded collection failed to recover an over-limit inventory")
	}
	if !writersStopped(root, binding) {
		t.Fatal("safe collection did not restore admission inventory")
	}
	if _, err := root.Lstat(generationLockName(binding)); err != nil {
		t.Fatal("lock inode removed", err)
	}
}

func TestCollectionCursorReachesCustodyBeyondFirstBatch(t *testing.T) {
	state, root, binding := collectionFixture(t)
	refs := make(map[string]Ref)
	for n := 1; n <= collectionBatch*3; n++ {
		ref := stoppedCollectionFixture(t, root, binding, n)
		refs[ref.Generation] = ref
	}
	dir, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	first, err := dir.ReadDir(collectionBatch)
	_ = dir.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range first {
		delete(refs, entry.Name())
	}
	var target Ref
	for _, ref := range refs {
		target = ref
		break
	}
	if target.Generation == "" {
		t.Fatal("missing later-batch target")
	}
	other := binding
	other.AccountID = strings.Repeat("d", 32)
	lock, err := root.OpenFile(generationLockName(other), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	target.Binding = other
	files, err := openGeneration(root, target.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(files, WorkerFile, target); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(files, StoppedFile, Stopped{SchemaVersion: SchemaVersion, Ref: target, WritersStopped: true}); err != nil {
		t.Fatal(err)
	}
	_ = files.Close()
	c := NewCollector(state)
	defer c.Close()
	removed := 0
	for step := 0; step < 8 && removed == 0; step++ {
		removed += collectFixture(t, c, []CustodyReference{{Binding: binding}})
	}
	if removed != 1 {
		t.Fatal("bounded cursor never reached later unreferenced worker", removed)
	}
	if _, err := root.Lstat(target.Generation); !os.IsNotExist(err) {
		t.Fatal("later custody not removed", err)
	}
}

func TestCollectionMalformedMovedCustodyRefusesNewWorker(t *testing.T) {
	exe, cfg, fixture := checkFixture(t, "success", ModeAuthStatus)
	gc := filepath.Join(cfg.StateRoot, "accounts", CollectionDirectory)
	if err := os.Mkdir(gc, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gc, strings.Repeat("a", 32)+".json"), []byte(`{"schema_version":999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	admitted := false
	if _, err := Run(context.Background(), exe, cfg, func(Ref) error { admitted = true; return nil }); !errors.Is(err, ErrCustodyUnknown) {
		t.Fatal("malformed moved custody permitted native worker", err)
	}
	if admitted {
		t.Fatal("unverifiable custody reached admission callback")
	}
	if _, err := os.Stat(filepath.Join(fixture, "writer.json")); !os.IsNotExist(err) {
		t.Fatal("native fixture spawned", err)
	}
}

func TestCollectionRealContainedWorkerAfterReferenceRelease(t *testing.T) {
	exe, cfg, _ := checkFixture(t, "success", ModeAuthStatus)
	var ref Ref
	if _, err := Run(context.Background(), exe, cfg, func(r Ref) error { ref = r; return nil }); err != nil {
		t.Fatal(err)
	}
	c := NewCollector(cfg.StateRoot)
	defer c.Close()
	if n := collectFixture(t, c, []CustodyReference{{Binding: cfg.Binding, Worker: ref.Worker}}); n != 0 {
		t.Fatal("real worker permit collected")
	}
	if !CustodyStopped(cfg.StateRoot, ref.Worker, cfg.Binding) {
		t.Fatal("permit proof lost")
	}
	c.Close()
	if n := collectFixture(t, c, nil); n != 1 {
		t.Fatal("real stopped worker not collected", n)
	}
	if !WritersStoppedForBinding(cfg.StateRoot, cfg.Binding) {
		t.Fatal("collection broke writer custody gate")
	}
}
