package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// The owner receipt hashes this pointer along with the launch request. Include
// the document digest so recreating a lost snapshot cannot change its payload
// under an already accepted operation ID.
func operationHandoff(id, source string) (string, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(os.TempDir())
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, fmt.Sprintf("swarm-handoff-%d-%x", os.Getuid(), sha256.Sum256([]byte(id+"\x00"+source))))
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	d, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = d.Close() }()
	info, err := d.Stat()
	if err != nil || !ownedHandoffPath(info) || !info.IsDir() {
		return "", fmt.Errorf("unsafe handoff directory %s", dir)
	}
	if err := unix.Flock(int(d.Fd()), unix.LOCK_EX); err != nil {
		return "", err
	}
	// A killed writer releases the lock. Its temporary file never appeared in
	// a launch request, so the next caller can recover the same directory.
	staging := filepath.Join(dir, ".partial")
	if err := os.Remove(staging); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	names, err := d.Readdirnames(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	body, readErr := os.ReadFile(source)
	if readErr != nil && (len(names) == 0 || !errors.Is(readErr, os.ErrNotExist)) {
		return "", readErr
	}
	var dest string
	switch len(names) {
	case 0:
		dest = filepath.Join(dir, fmt.Sprintf("%x.md", sha256.Sum256(body)))
		file, err := os.OpenFile(staging, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		_, writeErr := file.Write(body)
		if writeErr == nil {
			writeErr = file.Sync()
		}
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return "", err
		}
		if err := os.Rename(staging, dest); err != nil {
			return "", err
		}
	case 1:
		dest = filepath.Join(dir, names[0])
		info, err := os.Lstat(dest)
		if err != nil || !ownedHandoffPath(info) || !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 {
			return "", fmt.Errorf("unsafe handoff snapshot %s", dest)
		}
		snapshot, err := os.ReadFile(dest)
		if err != nil {
			return "", err
		}
		if names[0] != fmt.Sprintf("%x.md", sha256.Sum256(snapshot)) {
			return "", fmt.Errorf("invalid handoff snapshot digest in %s", dir)
		}
		if readErr == nil && !bytes.Equal(snapshot, body) {
			return "", errors.New("handoff document changed for this operation ID; check its status before issuing new work")
		}
	default:
		return "", fmt.Errorf("ambiguous handoff snapshot in %s", dir)
	}
	// A previous writer may have died after rename but before syncing either
	// directory. Make both namespace entries durable before daemon dispatch.
	if err := d.Sync(); err != nil {
		return "", err
	}
	return dest, syncLocalPath(root)
}

func ownedHandoffPath(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid() && info.Mode().Perm()&0077 == 0
}
