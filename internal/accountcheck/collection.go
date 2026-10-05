package accountcheck

import (
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/processcontain"
)

const CollectionSchemaVersion = 1
const CollectionDirectory = "check-gc"
const collectionBatch = 64
const collectionFiles = 64
const collectionRemovals = 16

// CustodyReference is supplied by the serialized recovery owner from both its
// memory and the checked durable journal. A zero Worker retains the whole
// credential generation, including checks not yet admitted to a permit.
type CustodyReference struct {
	Binding accounts.Binding
	Worker  processcontain.Identity
}

type collectionFile struct {
	Path      string `json:"path"`
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	Directory bool   `json:"directory"`
}

// CollectionIntent is deletion authority only. It never grants native admission
// or stopped-writer proof. It survives outside a partially deleted directory.
type CollectionIntent struct {
	SchemaVersion int              `json:"schema_version"`
	Ref           Ref              `json:"ref"`
	Stopped       Stopped          `json:"stopped"`
	Files         []collectionFile `json:"files"`
}

// Collector runs bounded work on the existing recovery owner, without a timer
// or goroutine. Its directory cursors make progress even beyond admission's
// inventory limit; reaching EOF starts a fresh pass on the next Step.
type Collector struct {
	stateRoot string
	checks    *os.File
	gc        *os.File
}

func NewCollector(stateRoot string) *Collector { return &Collector{stateRoot: stateRoot} }

func (c *Collector) Close() {
	if c.checks != nil {
		_ = c.checks.Close()
		c.checks = nil
	}
	if c.gc != nil {
		_ = c.gc.Close()
		c.gc = nil
	}
}

// Step preserves live, referenced, malformed and incomplete custody. The same
// nonblocking credential flock used by Run prevents collecting an admission
// while its worker is waiting on this owner. Lock inodes are never removed.
func (c *Collector) Step(retained []CustodyReference) (int, error) {
	checks, err := openCheckRoot(c.stateRoot, false)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = checks.Close() }()
	state, err := os.OpenRoot(c.stateRoot)
	if err != nil {
		return 0, err
	}
	defer func() { _ = state.Close() }()
	parent, err := state.OpenRoot("accounts")
	if err != nil {
		return 0, err
	}
	defer func() { _ = parent.Close() }()
	gc, err := openCollectionRoot(parent, false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if gc != nil {
		defer func() { _ = gc.Close() }()
	}
	removed := 0
	if gc != nil {
		entries, err := collectionEntries(gc, &c.gc)
		if err != nil {
			return removed, err
		}
		for _, entry := range entries {
			if removed >= collectionRemovals {
				break
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".check-") && validGeneration(strings.TrimPrefix(name, ".check-")) {
				info, err := gc.Lstat(name)
				if err != nil || !privateCollectionFile(info) || info.Size() > 16<<10 {
					return removed, ErrCustodyUnknown
				}
				if err := gc.Remove(name); err != nil {
					return removed, err
				}
				if err := syncRoot(gc); err != nil {
					return removed, err
				}
				continue
			}
			if entry.IsDir() && validGeneration(name) {
				// A directory may precede its intent in enumeration order, but
				// an orphan is never silently treated as collectible.
				if _, err := gc.Lstat(name + ".json"); err != nil {
					if errors.Is(err, os.ErrNotExist) {
						if _, dirErr := gc.Lstat(name); errors.Is(dirErr, os.ErrNotExist) {
							continue // collected earlier in this bounded batch
						}
					}
					return removed, ErrCustodyUnknown
				}
				continue
			}
			if !strings.HasSuffix(name, ".json") || !validGeneration(strings.TrimSuffix(name, ".json")) {
				return removed, ErrCustodyUnknown
			}
			var intent CollectionIntent
			if readJSON(gc, name, &intent) != nil || ValidateCollectionIntent(intent) != nil || intent.Ref.Generation+".json" != name {
				return removed, ErrCustodyUnknown
			}
			done, err := collectIntent(parent, checks, gc, intent, retained, syncRoot)
			if err != nil {
				return removed, err
			}
			if done {
				removed++
			}
		}
	}
	if removed >= collectionRemovals {
		return removed, nil
	}
	entries, err := collectionEntries(checks, &c.checks)
	if err != nil {
		return removed, err
	}
	for _, entry := range entries {
		if removed >= collectionRemovals {
			break
		}
		if !validGeneration(entry.Name()) || !entry.IsDir() {
			continue
		}
		files, err := openGeneration(checks, entry.Name())
		if err != nil {
			continue
		}
		var ref Ref
		var proof Stopped
		valid := readJSON(files, WorkerFile, &ref) == nil && ref.Generation == entry.Name() && readJSON(files, StoppedFile, &proof) == nil
		if !valid || collectionRetained(ref, retained) || ref.Worker.Alive() || proof.Native.Alive() {
			_ = files.Close()
			continue
		}
		inventory, inventoryErr := collectionInventory(files)
		_ = files.Close()
		intent := CollectionIntent{SchemaVersion: CollectionSchemaVersion, Ref: ref, Stopped: proof, Files: inventory}
		if inventoryErr != nil || ValidateCollectionIntent(intent) != nil {
			continue
		}
		if gc == nil {
			gc, err = openCollectionRoot(parent, true)
			if err != nil {
				return removed, err
			}
			defer func() { _ = gc.Close() }()
		}
		// collectIntent writes and confirms authority while holding the same
		// generation lock; even a visible-but-unsynced intent is retried.
		done, err := collectIntent(parent, checks, gc, intent, retained, syncRoot)
		if err != nil {
			return removed, err
		}
		if done {
			removed++
		}
	}
	return removed, nil
}

func collectionEntries(root *os.Root, cursor **os.File) ([]os.DirEntry, error) {
	if *cursor != nil {
		before, err := root.Lstat(".")
		after, statErr := (*cursor).Stat()
		if err != nil || statErr != nil || !os.SameFile(before, after) {
			_ = (*cursor).Close()
			*cursor = nil
			return nil, ErrCustodyUnknown
		}
	} else {
		var err error
		*cursor, err = root.Open(".")
		if err != nil {
			return nil, err
		}
	}
	entries, err := (*cursor).ReadDir(collectionBatch)
	if errors.Is(err, io.EOF) {
		_ = (*cursor).Close()
		*cursor = nil
		err = nil
	}
	return entries, err
}

func openCollectionRoot(parent *os.Root, create bool) (*os.Root, error) {
	if create {
		if err := parent.Mkdir(CollectionDirectory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err := syncRoot(parent); err != nil {
			return nil, err
		}
	}
	if _, err := parent.Lstat(CollectionDirectory); err != nil {
		return nil, err
	}
	return openGeneration(parent, CollectionDirectory)
}

func collectionRetained(ref Ref, retained []CustodyReference) bool {
	for _, keep := range retained {
		if keep.Binding.Provider == ref.Binding.Provider && keep.Binding.AccountID == ref.Binding.AccountID && keep.Binding.CredentialGeneration == ref.Binding.CredentialGeneration && (keep.Worker.PID == 0 || keep.Worker == ref.Worker) {
			return true
		}
	}
	return false
}

// ValidateCollectionIntent also serves the independent downgrade inventory
// guard. Inactive garbage uses the existing Worker2 capability, without a new
// execution or proof schema.
func ValidateCollectionIntent(intent CollectionIntent) error {
	r, p, b := intent.Ref, intent.Stopped, intent.Ref.Binding
	if intent.SchemaVersion != CollectionSchemaVersion || r.SchemaVersion != SchemaVersion || p.SchemaVersion != SchemaVersion || p.Ref != r || !p.WritersStopped || !validGeneration(r.Generation) || r.Worker.PID <= 0 || r.Worker.StartTime <= 0 || (r.Mode != ModeAuthStatus && r.Mode != ModeAvailability) || b.SchemaVersion != accounts.SchemaVersion || (b.Provider != accounts.ProviderCodex && b.Provider != accounts.ProviderClaude) || !validGeneration(b.AccountID) || b.CredentialGeneration == 0 || b.ConfigurationGeneration == 0 || len(b.Identity) != 64 || (p.Native.PID == 0 && p.Native.StartTime != 0) || (p.Native.PID != 0 && (p.Native.PID < 0 || p.Native.StartTime <= 0)) || len(intent.Files) < 3 || len(intent.Files) > collectionFiles {
		return ErrCustodyUnknown
	}
	seen := make(map[string]bool)
	pathBytes := 0
	for _, file := range intent.Files {
		pathBytes += len(file.Path)
		if seen[file.Path] || file.Inode == 0 || (file.Path != "." && (!filepath.IsLocal(file.Path) || filepath.Clean(file.Path) != file.Path)) || (file.Path != "." && file.Path != WorkerFile && file.Path != StoppedFile && file.Path != "native-cwd" && !strings.HasPrefix(file.Path, "native-cwd/") && file.Path != "native-config" && !strings.HasPrefix(file.Path, "native-config/")) || ((file.Path == "." || file.Path == "native-cwd" || file.Path == "native-config") && !file.Directory) || ((file.Path == WorkerFile || file.Path == StoppedFile) && file.Directory) {
			return ErrCustodyUnknown
		}
		seen[file.Path] = true
	}
	if !seen["."] || !seen[WorkerFile] || !seen[StoppedFile] || pathBytes > 2048 {
		return ErrCustodyUnknown
	}
	identity, err := hex.DecodeString(b.Identity)
	if err != nil || len(identity) != 32 || b.Identity != strings.ToLower(b.Identity) {
		return ErrCustodyUnknown
	}
	return nil
}

func collectionInventory(root *os.Root) ([]collectionFile, error) {
	var files []collectionFile
	var walk func(string, int) error
	walk = func(name string, depth int) error {
		if depth > 8 || len(files) >= collectionFiles {
			return ErrCustodyUnknown
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		directoryAllowed := info.Mode().Perm() == 0o700 || name == "native-config/backups" && info.Mode().Perm()&0o002 == 0 && info.Mode().Perm()&0o700 == 0o700
		if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode()&os.ModeSymlink != 0 || (info.IsDir() && !directoryAllowed) || (!info.IsDir() && (!info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat.Nlink != 1)) {
			return ErrCustodyUnknown
		}
		files = append(files, collectionFile{Path: name, Device: uint64(stat.Dev), Inode: stat.Ino, Directory: info.IsDir()})
		if !info.IsDir() {
			return nil
		}
		dir, err := root.Open(name)
		if err != nil {
			return err
		}
		entries, err := dir.ReadDir(collectionFiles + 1)
		_ = dir.Close()
		if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > collectionFiles {
			return ErrCustodyUnknown
		}
		for _, entry := range entries {
			path := filepath.Join(name, entry.Name())
			if name == "." && path != WorkerFile && path != StoppedFile && path != "native-cwd" && path != "native-config" {
				return ErrCustodyUnknown
			}
			if err := walk(path, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	err := walk(".", 0)
	return files, err
}

func privateCollectionFile(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && stat.Nlink == 1
}

func collectIntent(parent, checks, gc *os.Root, intent CollectionIntent, retained []CustodyReference, confirm func(*os.Root) error) (bool, error) {
	ref := intent.Ref
	if collectionRetained(ref, retained) || ref.Worker.Alive() || intent.Stopped.Native.Alive() {
		return false, nil
	}
	lock, err := checks.OpenFile(generationLockName(ref.Binding), os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false, nil
	}
	defer func() { _ = lock.Close() }()
	info, err := lock.Stat()
	pathInfo, pathErr := checks.Lstat(generationLockName(ref.Binding))
	if err != nil || pathErr != nil || !validLockInfo(info) || !os.SameFile(info, pathInfo) {
		return false, ErrCustodyUnknown
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	name := ref.Generation + ".json"
	var existing CollectionIntent
	if _, err := gc.Lstat(name); errors.Is(err, os.ErrNotExist) {
		if err := writeJSON(gc, name, intent); err != nil {
			return false, err
		}
	} else if err != nil || readJSON(gc, name, &existing) != nil || ValidateCollectionIntent(existing) != nil || existing.Ref != ref {
		return false, ErrCustodyUnknown
	} else {
		intent = existing
		if err := confirm(gc); err != nil {
			return false, err
		}
	}
	_, sourceErr := checks.Lstat(ref.Generation)
	_, targetErr := gc.Lstat(ref.Generation)
	if sourceErr == nil {
		if !errors.Is(targetErr, os.ErrNotExist) {
			return false, ErrCustodyUnknown
		}
		files, err := openGeneration(checks, ref.Generation)
		if err != nil {
			return false, err
		}
		err = validateCollectionFiles(files, intent.Files)
		_ = files.Close()
		if err != nil {
			return false, err
		}
		if err := parent.Rename("checks/"+ref.Generation, CollectionDirectory+"/"+ref.Generation); err != nil {
			return false, err
		}
	} else if !errors.Is(sourceErr, os.ErrNotExist) || (targetErr != nil && !errors.Is(targetErr, os.ErrNotExist)) {
		return false, ErrCustodyUnknown
	}
	// Confirm both rename parents even on replay, before removing any evidence.
	if err := confirm(checks); err != nil {
		return false, err
	}
	if err := confirm(gc); err != nil {
		return false, err
	}
	if _, err := gc.Lstat(ref.Generation); err == nil {
		files, err := openGeneration(gc, ref.Generation)
		if err != nil {
			return false, err
		}
		defer func() { _ = files.Close() }()
		if err := validateCollectionFiles(files, intent.Files); err != nil {
			return false, err
		}
		for i := len(intent.Files) - 1; i >= 0; i-- {
			name := intent.Files[i].Path
			if name == "." {
				continue
			}
			if err := files.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
		}
		if err := confirm(files); err != nil {
			return false, err
		}
		if err := gc.Remove(ref.Generation); err != nil {
			return false, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := confirm(gc); err != nil {
		return false, err
	}
	if err := gc.Remove(name); err != nil {
		return false, err
	}
	return true, confirm(gc)
}

func validateCollectionFiles(root *os.Root, expected []collectionFile) error {
	actual, err := collectionInventory(root)
	if err != nil {
		return err
	}
	known := make(map[string]collectionFile, len(expected))
	for _, file := range expected {
		known[file.Path] = file
	}
	for _, file := range actual {
		if known[file.Path] != file {
			return ErrCustodyUnknown
		}
	}
	return nil
}

// ValidateCollectionDirectory checks surviving inventory against the external
// intent. Missing listed entries are expected during a partial deletion.
func ValidateCollectionDirectory(root *os.Root, intent CollectionIntent) error {
	if err := ValidateCollectionIntent(intent); err != nil {
		return err
	}
	return validateCollectionFiles(root, intent.Files)
}

// Moving a stopped directory out of checks never makes malformed GC custody
// invisible to execution or history transfer. Valid intent-only/partial-trash
// states are inactive metadata; they contribute no positive proof.
func collectionStateSafe(stateRoot string) bool {
	store, err := accounts.OpenReadOnly(stateRoot)
	if err != nil {
		return false
	}
	_ = store.Close()
	state, err := os.OpenRoot(stateRoot)
	if err != nil {
		return false
	}
	defer func() { _ = state.Close() }()
	parent, err := state.OpenRoot("accounts")
	if err != nil {
		return false
	}
	defer func() { _ = parent.Close() }()
	_, err = ValidateCollectionInventory(parent)
	return err == nil
}

// ValidateCollectionInventory independently guards GC-only state during native
// admission, history transfer, activation and rollback. The parent is an
// already-validated private accounts directory. A true result means it contains
// collection state requiring the existing Worker2 reader capability.
func ValidateCollectionInventory(parent *os.Root) (bool, error) {
	gc, err := openCollectionRoot(parent, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = gc.Close() }()
	directory, err := gc.Open(".")
	if err != nil {
		return false, err
	}
	entries, err := directory.ReadDir(4097)
	_ = directory.Close()
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > 4096 {
		return false, ErrCustodyUnknown
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".check-") && validGeneration(strings.TrimPrefix(name, ".check-")) {
			info, err := gc.Lstat(name)
			if err != nil || !privateCollectionFile(info) || info.Size() > 16<<10 {
				return false, ErrCustodyUnknown
			}
			continue
		}
		generation := strings.TrimSuffix(name, ".json")
		if entry.IsDir() {
			generation = name
		}
		if !validGeneration(generation) || (!entry.IsDir() && name != generation+".json") {
			return false, ErrCustodyUnknown
		}
		var intent CollectionIntent
		if readJSON(gc, generation+".json", &intent) != nil || ValidateCollectionIntent(intent) != nil || intent.Ref.Generation != generation || intent.Ref.Worker.Alive() || intent.Stopped.Native.Alive() {
			return false, ErrCustodyUnknown
		}
		if entry.IsDir() {
			files, err := openGeneration(gc, generation)
			if err != nil {
				return false, err
			}
			err = ValidateCollectionDirectory(files, intent)
			_ = files.Close()
			if err != nil {
				return false, err
			}
		}
	}
	return len(entries) > 0, nil
}
