package accounts

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const maxCredentialBytes = 1 << 20
const maxRegistryBytes = 8 << 20

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[:]), nil
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && id == strings.ToLower(id)
}

// Check every absolute component, including the leaf, before obtaining an
// anchored descriptor. A symlinked state root is never an execution selector.
func checkAbsolute(path string, leafDirectory bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrUnsafePath
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	current := string(filepath.Separator)
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return ErrUnsafePath
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafePath
		}
		if i < len(parts)-1 || leafDirectory {
			if !info.IsDir() {
				return ErrUnsafePath
			}
		}
	}
	return nil
}

func privateInfo(info os.FileInfo, directory bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0o077 != 0 {
		return false
	}
	if directory {
		return info.IsDir() && info.Mode()&os.ModeSymlink == 0
	}
	return info.Mode().IsRegular() && stat.Nlink == 1
}

func checkRelative(root *os.Root, name string, directory bool) (os.FileInfo, error) {
	if !filepath.IsLocal(name) || filepath.Clean(name) != name {
		return nil, ErrUnsafePath
	}
	parts := strings.Split(name, string(filepath.Separator))
	var info os.FileInfo
	var err error
	for i := range parts {
		info, err = root.Lstat(filepath.Join(parts[:i+1]...))
		if err != nil {
			return nil, err
		}
		if !privateInfo(info, i < len(parts)-1 || directory) {
			return nil, ErrUnsafePath
		}
	}
	return info, nil
}

func readPrivate(root *os.Root, name string, limit int64) ([]byte, error) {
	before, err := checkRelative(root, name, false)
	if err != nil {
		return nil, err
	}
	// NONBLOCK prevents a raced-in FIFO from hanging; NOFOLLOW rejects leaf
	// symlinks even when they happen to point to a file inside this root.
	parent, err := openPrivateDir(root, filepath.Dir(name))
	if err != nil {
		return nil, ErrUnsafePath
	}
	defer func() { _ = parent.Close() }()
	f, err := parent.OpenFile(filepath.Base(name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnsafePath
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !privateInfo(after, false) || !os.SameFile(before, after) || after.Size() > limit {
		return nil, ErrUnsafePath
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrUnsafePath
	}
	current, err := checkRelative(root, name, false)
	if err != nil || !os.SameFile(current, after) {
		return nil, ErrUnsafePath
	}
	return raw, nil
}

func readImport(path string) ([]byte, error) {
	if err := checkAbsolute(path, false); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, ErrUnsafePath
	}
	defer func() { _ = root.Close() }()
	// An explicitly selected import may live outside the private store, for
	// example in an owner-local 0755 home. Anchor its parent and require only
	// the selected inode itself to be owner-only, regular and unlinked.
	name := filepath.Base(path)
	before, err := root.Lstat(name)
	if err != nil || !privateInfo(before, false) || before.Size() > maxCredentialBytes {
		return nil, ErrUnsafePath
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnsafePath
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !privateInfo(opened, false) || !os.SameFile(before, opened) {
		return nil, ErrUnsafePath
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxCredentialBytes+1))
	current, currentErr := root.Lstat(name)
	if err != nil || currentErr != nil || len(raw) > maxCredentialBytes || !privateInfo(current, false) || !os.SameFile(current, opened) {
		return nil, ErrUnsafePath
	}
	return raw, nil
}

type writeOps struct {
	rename  func(string, string) error
	syncDir func(string) error
}

func openPrivateDir(root *os.Root, dir string) (*os.Root, error) {
	before, err := checkRelative(root, dir, true)
	if err != nil {
		return nil, ErrUnsafePath
	}
	parent, err := root.OpenRoot(dir)
	if err != nil {
		return nil, ErrUnsafePath
	}
	opened, err := parent.Lstat(".")
	current, currentErr := checkRelative(root, dir, true)
	if err != nil || currentErr != nil || !privateInfo(opened, true) || !os.SameFile(before, opened) || !os.SameFile(current, opened) {
		_ = parent.Close()
		return nil, ErrUnsafePath
	}
	return parent, nil
}

// durableReplace follows the existing auth-watch/device-registry contract:
// synced temporary inode, rename, then synced parent. The boolean distinguishes
// failure before visibility from an uncertain durability failure after rename.
func durableReplace(root *os.Root, name string, data []byte, ops writeOps) (bool, error) {
	dir := filepath.Dir(name)
	parent, err := openPrivateDir(root, dir)
	if err != nil {
		return false, ErrUnsafePath
	}
	defer func() { _ = parent.Close() }()
	if _, err := checkRelative(root, name, false); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, ErrUnsafePath
	}
	id, err := newID()
	if err != nil {
		return false, err
	}
	tmpName := filepath.Join(dir, ".account-tmp-"+id)
	tmp, err := parent.OpenFile(filepath.Base(tmpName), os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return false, err
	}
	defer func() { _ = parent.Remove(filepath.Base(tmpName)) }()
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	rename := ops.rename
	if rename == nil {
		rename = func(oldName, newName string) error {
			return parent.Rename(filepath.Base(oldName), filepath.Base(newName))
		}
	}
	if err := rename(tmpName, name); err != nil {
		return false, err
	}
	syncDir := ops.syncDir
	if syncDir == nil {
		syncDir = func(string) error { return syncRootDir(parent, ".") }
	}
	if err := syncDir(dir); err != nil {
		return true, ErrDurabilityUncertain
	}
	return true, nil
}

func syncRootDir(root *os.Root, dir string) error {
	if _, err := checkRelative(root, dir, true); err != nil {
		return err
	}
	f, err := root.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
