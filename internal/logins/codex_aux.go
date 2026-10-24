package logins

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const codexAuxFile = ".grant-aux"
const maxCodexAuxBytes int64 = 32 << 20

var codexAuxRoots = []string{"goals_1.sqlite", "goals_1.sqlite-shm", "goals_1.sqlite-wal", "logs_2.sqlite", "logs_2.sqlite-shm", "logs_2.sqlite-wal", "memories_1.sqlite", "memories_1.sqlite-shm", "memories_1.sqlite-wal", "queue_1.sqlite", "queue_1.sqlite-shm", "queue_1.sqlite-wal", "state_5.sqlite", "state_5.sqlite-shm", "state_5.sqlite-wal", "installation_id", "skills", "tmp"}

type codexAuxOwnership struct {
	Version       int    `json:"version"`
	NativeVersion string `json:"native_version"`
}

func grantBaseFile(name string) bool {
	switch name {
	case "auth.json", "credential.json", ".grant-lock", ".grant-owner", codexAuxFile:
		return true
	}
	return false
}
func grantAuxRoot(name string) bool {
	for _, root := range codexAuxRoots {
		if root == name {
			return true
		}
	}
	return false
}

// PrepareAuxiliary owns only the qualified helper's generated roots in an
// otherwise auth-only home. No native history or user configuration is copied.
func (l *GrantLease) PrepareAuxiliary(version string) error {
	if version != "0.163.0" {
		return ErrIneligible
	}
	if e := l.ProveStopped(); e != nil {
		return e
	}
	l.nativeIntent = true
	home, e := openPrivateDir(l.store.root, filepath.Join("saved", l.login.ID))
	if e != nil {
		return e
	}
	defer home.Close()
	if _, e = home.Lstat(codexAuxFile); e == nil {
		if e = l.CleanupAuxiliary(); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	f, e := home.Open(".")
	if e != nil {
		return e
	}
	entries, e := f.ReadDir(2049)
	_ = f.Close()
	if e != nil && !errors.Is(e, io.EOF) {
		return e
	}
	if len(entries) > 2048 {
		return ErrIneligible
	}
	for _, entry := range entries {
		if !grantBaseFile(entry.Name()) {
			return errUnknownSavedData
		}
	}
	b, _ := json.Marshal(codexAuxOwnership{Version: 1, NativeVersion: version})
	_, e = durableReplace(home, codexAuxFile, b, l.store.writeOps)
	return e
}

// CheckAuxiliary bounds logical scratch bytes and entries without following
// native helper symlinks. Activation additionally requires a host filesystem reservation for hard limits.
func (l *GrantLease) CheckAuxiliary() error { return l.checkAuxiliary(true) }
func (l *GrantLease) checkAuxiliary(quota bool) error {
	if e := l.validate(); e != nil {
		return e
	}
	home, e := openPrivateDir(l.store.root, filepath.Join("saved", l.login.ID))
	if e != nil {
		return e
	}
	defer home.Close()
	raw, e := readPrivate(home, codexAuxFile, 1024)
	if e != nil {
		return e
	}
	var marker codexAuxOwnership
	if validateJSON(raw) != nil || json.Unmarshal(raw, &marker) != nil || marker.Version != 1 || marker.NativeVersion != "0.163.0" {
		return ErrIneligible
	}
	var entries int
	var bytes int64
	var walk func(string) error
	walk = func(path string) error {
		info, e := home.Lstat(path)
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil {
			return e
		}
		entries++
		bytes += info.Size()
		if quota && (entries > 2048 || bytes > maxCodexAuxBytes) {
			return ErrIneligible
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Getuid()) {
			return ErrUnsafePath
		}
		if info.IsDir() {
			f, e := home.Open(path)
			if e != nil {
				return e
			}
			children, e := f.ReadDir(2049)
			_ = f.Close()
			if e != nil && !errors.Is(e, io.EOF) {
				return e
			}
			if len(children) > 2048 {
				return ErrIneligible
			}
			for _, child := range children {
				if e = walk(filepath.Join(path, child.Name())); e != nil {
					return e
				}
			}
		}
		return nil
	}
	f, e := home.Open(".")
	if e != nil {
		return e
	}
	roots, e := f.ReadDir(2049)
	_ = f.Close()
	if e != nil && !errors.Is(e, io.EOF) {
		return e
	}
	if len(roots) > 2048 {
		return ErrIneligible
	}
	for _, root := range roots {
		if grantBaseFile(root.Name()) {
			continue
		}
		if !grantAuxRoot(root.Name()) {
			return errUnknownSavedData
		}
		if quota {
			if e = walk(root.Name()); e != nil {
				return e
			}
		}
	}
	return nil
}

// CleanupAuxiliary retires only marker-owned roots after native stop proof.
// Unexpected bytes remain for the owner and block removal rather than vanish.
func (l *GrantLease) CleanupAuxiliary() error {
	if e := l.ProveStopped(); e != nil {
		return e
	}
	if e := l.checkAuxiliary(false); e != nil {
		return e
	}
	home, e := openPrivateDir(l.store.root, filepath.Join("saved", l.login.ID))
	if e != nil {
		return e
	}
	defer home.Close()
	for _, root := range codexAuxRoots {
		if e = home.RemoveAll(root); e != nil {
			return e
		}
	}
	if e = home.Remove(codexAuxFile); e != nil {
		return e
	}
	if e = syncRootDir(home, "."); e != nil {
		return ErrDurabilityUncertain
	}
	return nil
}
