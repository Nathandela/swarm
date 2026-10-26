package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	storageWarnBytes = 256 << 20
	storageMinFree   = 1 << 30
)

type storageBytes struct{ allocated, logical int64 }
type storageUsage struct {
	supervisor, native, other   storageBytes
	free                        uint64
	freeKnown, partial, missing bool
	links                       int
}

func (s storageUsage) totalAllocated() int64 {
	return s.supervisor.allocated + s.native.allocated + s.other.allocated
}

func doctorStorageCheck(path string) doctorFinding {
	return storageFinding(scanStorage(path, 100000, time.Now().Add(2*time.Second)))
}

func storageFinding(s storageUsage) doctorFinding {
	f := doctorFinding{Check: "storage", Status: "ok"}
	if s.missing {
		f.Detail = "no state directory; no storage measured"
		return f
	}
	coverage := "observed snapshot"
	if s.partial {
		coverage = "partial lower bound"
		f.Status = "warn"
	}
	f.Detail = fmt.Sprintf("%s: %d bytes allocated, %d regular-file bytes logical; supervisor=%d, managed=%d, other retained=%d allocated bytes; %d symlink targets excluded",
		coverage, s.totalAllocated(), s.supervisor.logical+s.native.logical+s.other.logical,
		s.supervisor.allocated, s.native.allocated, s.other.allocated, s.links)
	if s.freeKnown {
		f.Detail += fmt.Sprintf("; filesystem available=%d bytes", s.free)
	} else {
		f.Detail += "; filesystem availability unknown"
		f.Status = "warn"
	}
	if s.totalAllocated() >= storageWarnBytes || (s.freeKnown && s.free < storageMinFree) {
		f.Status = "warn"
	}
	f.Detail += "; external histories, projects and temporary caches not measured; no automatic cleanup or admission limit"
	if f.Status == "warn" {
		f.Fix = "review storage ownership and available space; preserve histories and unresolved recovery data"
	}
	return f
}

// scanStorage reads metadata through an anchored root; it never opens file bodies.
func scanStorage(path string, maxEntries int, deadline time.Time) storageUsage {
	var result storageUsage
	before, err := os.Lstat(path)
	if os.IsNotExist(err) {
		result.missing = true
		return result
	}
	if err != nil || !before.IsDir() {
		result.partial = true
		return result
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		result.partial = true
		return result
	}
	defer func() { _ = root.Close() }()
	seen := make(map[[2]uint64]bool)
	entries := 0
	var walk func(string, os.FileInfo)
	walk = func(rel string, info os.FileInfo) {
		if entries >= maxEntries || time.Now().After(deadline) {
			result.partial = true
			return
		}
		entries++
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			result.partial = true
			return
		}
		key := [2]uint64{uint64(st.Dev), st.Ino}
		if seen[key] {
			return
		}
		seen[key] = true
		if info.Mode()&os.ModeSymlink != 0 {
			result.links++
			if st.Blocks < 0 {
				result.partial = true
			} else {
				result.other.allocated += st.Blocks * 512
			}
			return
		}
		bucket := &result.other
		top, _, _ := strings.Cut(rel, string(filepath.Separator))
		switch top {
		case "accounts", "logins":
			bucket = &result.native
		case ".", "journal", "idempotency", "remote", "devices", "upgrade":
			bucket = &result.supervisor
		default:
			if !strings.Contains(rel, string(filepath.Separator)) && info.Mode().IsRegular() {
				bucket = &result.supervisor
			}
			if strings.Contains(rel, string(filepath.Separator)) {
				switch filepath.Base(rel) {
				case "meta.json", "shim-launch.json", "exit.json", "identity-tail.bin", "hooks.spool", "hooks.spool.floor", "hooks.spool.incarnation", "hook.fold", "hook.applied", "capabilities.json", "backend.json", "observed-cli.json", "session-instance":
					bucket = &result.supervisor
				}
			}
		}
		if st.Blocks < 0 {
			result.partial = true
		} else {
			bucket.allocated += st.Blocks * 512
		}
		if info.Mode().IsRegular() {
			bucket.logical += info.Size()
		}
		if !info.IsDir() {
			return
		}
		f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			result.partial = true
			return
		}
		defer func() { _ = f.Close() }()
		current, err := f.Stat()
		if err != nil || !os.SameFile(info, current) {
			result.partial = true
			return
		}
		if rel == "." {
			var fs syscall.Statfs_t
			if syscall.Fstatfs(int(f.Fd()), &fs) == nil && fs.Bsize > 0 && uint64(fs.Bavail) <= ^uint64(0)/uint64(fs.Bsize) {
				result.free = uint64(fs.Bavail) * uint64(fs.Bsize)
				result.freeKnown = true
			}
		}
		for {
			if entries >= maxEntries || time.Now().After(deadline) {
				result.partial = true
				return
			}
			children, readErr := f.ReadDir(128)
			for _, child := range children {
				if entries >= maxEntries || time.Now().After(deadline) {
					result.partial = true
					return
				}
				childRel := filepath.Join(rel, child.Name())
				childInfo, err := root.Lstat(childRel)
				if err != nil {
					result.partial = true
					continue
				}
				walk(childRel, childInfo)
			}
			if errors.Is(readErr, io.EOF) {
				return
			}
			if readErr != nil {
				result.partial = true
				return
			}
		}
	}
	walk(".", before)
	return result
}
