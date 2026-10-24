package loginworker

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

type refreshSocket struct {
	path          string
	parent        *os.Root
	alias, socket os.FileInfo
	parentInfo    os.FileInfo
}

func captureRefreshSocket(f *jobFiles) (*refreshSocket, error) {
	if err := f.safe(); err != nil {
		return nil, err
	}
	alias, err := f.root.Lstat("n")
	if err != nil {
		return nil, err
	}
	path, err := codexSocketPath(filepath.Join(f.path, "n"))
	if err != nil {
		return nil, err
	}
	parentInfo, err := safeAbsolute(filepath.Dir(path), true, true)
	if err != nil {
		return nil, err
	}
	return pinRefreshSocket(f, path, alias, parentInfo)
}

func pinRefreshSocket(f *jobFiles, path string, alias, parentInfo os.FileInfo) (*refreshSocket, error) {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	currentAlias, err := f.root.Lstat("n")
	openedParent, parentErr := parent.Stat(".")
	if err != nil || parentErr != nil || !os.SameFile(alias, currentAlias) || !os.SameFile(parentInfo, openedParent) {
		_ = parent.Close()
		return nil, ErrUnsafe
	}
	socket, err := parent.Lstat(filepath.Base(path))
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	stat, ok := socket.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || socket.Mode()&os.ModeSocket == 0 || socket.Mode().Perm() != 0600 {
		_ = parent.Close()
		return nil, ErrUnsafe
	}
	resolved, err := codexSocketPath(filepath.Join(f.path, "n"))
	finalAlias, aliasErr := f.root.Lstat("n")
	finalParent, parentErr := safeAbsolute(filepath.Dir(path), true, true)
	physical, socketErr := os.Lstat(path)
	if err != nil || resolved != path || aliasErr != nil || !os.SameFile(alias, finalAlias) || parentErr != nil || !os.SameFile(parentInfo, finalParent) || socketErr != nil || !os.SameFile(socket, physical) || (path == filepath.Join(f.path, "n") && !os.SameFile(alias, socket)) {
		_ = parent.Close()
		return nil, ErrUnsafe
	}
	return &refreshSocket{path: path, parent: parent, alias: alias, socket: socket, parentInfo: parentInfo}, nil
}

func (s *refreshSocket) close() { _ = s.parent.Close() }

// The caller must contain the native writer first. Anchored inode checks keep
// cleanup from unlinking a replacement, including Codex's physical /tmp socket.
func (s *refreshSocket) removeAfterStop(f *jobFiles) error {
	if err := f.safe(); err != nil {
		return err
	}
	currentParent, err := safeAbsolute(filepath.Dir(s.path), true, true)
	if err != nil || !os.SameFile(s.parentInfo, currentParent) {
		return ErrUnsafe
	}
	alias, aliasErr := f.root.Lstat("n")
	if aliasErr != nil && !errors.Is(aliasErr, os.ErrNotExist) {
		return aliasErr
	}
	if aliasErr == nil && !os.SameFile(s.alias, alias) {
		return ErrUnsafe
	}
	socket, socketErr := s.parent.Lstat(filepath.Base(s.path))
	if socketErr != nil && !errors.Is(socketErr, os.ErrNotExist) {
		return socketErr
	}
	if socketErr == nil && (!os.SameFile(s.socket, socket) || socket.Mode()&os.ModeSocket == 0) {
		return ErrUnsafe
	}
	if socketErr == nil {
		if err := s.parent.Remove(filepath.Base(s.path)); err != nil {
			return err
		}
	}
	if err := f.root.Remove("n"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
