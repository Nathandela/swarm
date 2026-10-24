package loginworker

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const refreshScratchMarker = ".refresh-scratch"
const maxRefreshScratchBytes int64 = 32 << 20

var refreshScratchRoots = []string{"home", "tmp"}

type refreshScratchOwnership struct {
	Version     int    `json:"version"`
	LaunchNonce string `json:"launch_nonce"`
}

func prepareRefreshScratch(f *jobFiles, nonce string) error {
	if !validID(nonce) {
		return ErrInvalid
	}
	if _, e := f.root.Lstat(refreshScratchMarker); e == nil {
		if e = cleanupRefreshScratch(f); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return ErrUnsafe
	} else {
		for _, name := range refreshScratchRoots {
			if _, e = f.root.Lstat(name); e == nil {
				return ErrUnsafe
			} else if !errors.Is(e, os.ErrNotExist) {
				return ErrUnsafe
			}
		}
	}
	return f.write(refreshScratchMarker, refreshScratchOwnership{Version: 1, LaunchNonce: nonce})
}
func checkRefreshScratch(f *jobFiles, quota bool) error {
	if e := f.safe(); e != nil {
		return e
	}
	raw, e := readPrivate(f.root, refreshScratchMarker, 1024)
	if e != nil {
		return e
	}
	var owner refreshScratchOwnership
	if json.Unmarshal(raw, &owner) != nil || owner.Version != 1 || !validID(owner.LaunchNonce) {
		return ErrUnsafe
	}
	if !quota {
		return nil
	}
	var bytes int64
	entries := 0
	var walk func(string) error
	walk = func(name string) error {
		info, e := f.root.Lstat(name)
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil {
			return ErrUnsafe
		}
		entries++
		bytes += info.Size()
		if entries > 2048 || bytes > maxRefreshScratchBytes {
			return ErrInvalid
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Getuid()) {
			return ErrUnsafe
		}
		if !info.IsDir() {
			return nil
		}
		dir, e := f.root.Open(name)
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil {
			return ErrUnsafe
		}
		children, e := dir.ReadDir(2049)
		_ = dir.Close()
		if e != nil && !errors.Is(e, io.EOF) {
			return ErrUnsafe
		}
		if len(children) > 2048 {
			return ErrInvalid
		}
		for _, child := range children {
			if e = walk(filepath.Join(name, child.Name())); e != nil {
				return e
			}
		}
		return nil
	}
	for _, name := range refreshScratchRoots {
		if e = walk(name); e != nil {
			return e
		}
	}
	return nil
}

// cleanupRefreshScratch runs only after the owning worker contains native
// descendants and proves the grant stopped. Marker-owned roots are auth-free.
func cleanupRefreshScratch(f *jobFiles) error {
	if e := checkRefreshScratch(f, false); e != nil {
		return e
	}
	for _, name := range refreshScratchRoots {
		if e := f.root.RemoveAll(name); e != nil {
			return e
		}
	}
	if e := f.root.Remove(refreshScratchMarker); e != nil {
		return e
	}
	dir, e := f.root.Open(".")
	if e != nil {
		return e
	}
	e = dir.Sync()
	_ = dir.Close()
	return e
}
