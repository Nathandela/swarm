package appserver

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// UnixEndpoint pins the private native endpoint, including Codex's long-path alias.
// It contains no authority about the peer; that proof comes from the connected fd.
type UnixEndpoint struct {
	path, physical string
	pins           map[string]os.FileInfo
}

func CaptureUnixEndpoint(path string) (*UnixEndpoint, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("appserver: unsafe endpoint")
	}
	e := &UnixEndpoint{path: path, physical: path, pins: map[string]os.FileInfo{}}
	if err := e.pinParents(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Getuid()) {
			return nil, fmt.Errorf("appserver: unsafe alias")
		}
		tmp, err := filepath.EvalSymlinks("/tmp")
		if err != nil {
			return nil, err
		}
		target, err := os.Readlink(path)
		expected := filepath.Join(tmp, fmt.Sprintf("codex-daemon-%d", os.Getuid()), fmt.Sprintf("%x", sha256.Sum256([]byte(path))))
		if err != nil || target != expected {
			return nil, fmt.Errorf("appserver: unexpected endpoint alias")
		}
		e.pins[path] = info
		e.physical = target
		if err = e.pinParents(target); err != nil {
			return nil, err
		}
		info, err = os.Lstat(target)
		if err != nil {
			return nil, err
		}
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		return nil, fmt.Errorf("appserver: unsafe socket")
	}
	e.pins[e.physical] = info
	if !e.Unchanged() {
		return nil, fmt.Errorf("appserver: endpoint changed")
	}
	return e, nil
}

func (e *UnixEndpoint) pinParents(path string) error {
	parent := filepath.Dir(path)
	for current := parent; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (st.Uid != 0 && st.Uid != uint32(os.Getuid())) {
			return fmt.Errorf("appserver: unsafe endpoint ancestor")
		}
		if current == parent && (st.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0077 != 0) {
			return fmt.Errorf("appserver: endpoint parent not private")
		}
		if info.Mode().Perm()&0022 != 0 && (st.Uid != 0 || info.Mode()&os.ModeSticky == 0) {
			return fmt.Errorf("appserver: writable endpoint ancestor")
		}
		e.pins[current] = info
		if current == "/" {
			break
		}
	}
	return nil
}

// Unchanged rejects replacement, permission changes, and alias retargeting.
func (e *UnixEndpoint) Unchanged() bool {
	if e == nil {
		return false
	}
	for path, before := range e.pins {
		after, err := os.Lstat(path)
		if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
			return false
		}
		a, ok := before.Sys().(*syscall.Stat_t)
		b, ok2 := after.Sys().(*syscall.Stat_t)
		if !ok || !ok2 || a.Uid != b.Uid || a.Gid != b.Gid {
			return false
		}
	}
	if e.path != e.physical {
		target, err := os.Readlink(e.path)
		if err != nil || target != e.physical {
			return false
		}
	}
	return true
}
