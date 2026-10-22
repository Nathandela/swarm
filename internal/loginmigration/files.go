// Package loginmigration converts the retired account-pool format offline.
// Normal launch and recovery never decode the retired format.
package loginmigration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var (
	ErrLiveWriter      = errors.New("login migration requires stopped native writers")
	ErrUnsupported     = errors.New("login migration encountered unsupported retained state")
	ErrHistoryConflict = errors.New("native conversation histories diverge; source retained")
)

const maxJSON = 8 << 20
const maxFile = 512 << 20

func owned(info os.FileInfo, private bool) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && s.Uid == uint32(os.Getuid()) && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0022 == 0 && (!private || info.Mode().Perm()&0077 == 0) && (info.IsDir() || info.Mode().IsRegular() && s.Nlink == 1)
}
func safePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrUnsupported
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		i, e := os.Lstat(current)
		if e != nil {
			return e
		}
		if i.Mode()&os.ModeSymlink != 0 {
			return ErrUnsupported
		}
	}
	return nil
}
func readFile(path string, limit int64, private bool) ([]byte, error) {
	if e := safePath(path); e != nil {
		return nil, e
	}
	r, e := os.OpenRoot(filepath.Dir(path))
	if e != nil {
		return nil, e
	}
	defer func() { _ = r.Close() }()
	name := filepath.Base(path)
	i, e := r.Lstat(name)
	if e != nil {
		return nil, e
	}
	if !owned(i, private) || !i.Mode().IsRegular() || i.Size() > limit {
		return nil, ErrUnsupported
	}
	f, e := r.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer func() { _ = f.Close() }()
	j, e := f.Stat()
	if e != nil || !os.SameFile(i, j) {
		return nil, ErrUnsupported
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, ErrUnsupported
	}
	return b, nil
}
func readJSON(path string, v any) error {
	b, e := readFile(path, maxJSON, true)
	if e != nil {
		return e
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return ErrUnsupported
	}
	if e = validJSON(b); e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func validJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var value func() error
	value = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return ErrUnsupported
				}
				seen[s] = true
				if e = value(); e != nil {
					return e
				}
			}
			t, e = d.Token()
			if e != nil || t != json.Delim('}') {
				return ErrUnsupported
			}
		case json.Delim('['):
			for d.More() {
				if e = value(); e != nil {
					return e
				}
			}
			t, e = d.Token()
			if e != nil || t != json.Delim(']') {
				return ErrUnsupported
			}
		}
		return nil
	}
	if e := value(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrUnsupported
	}
	return nil
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
func writeJSON(path string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return replace(path, b)
}
func replace(path string, b []byte) error { return replaceMode(path, b, 0600) }
func replaceMode(path string, b []byte, mode os.FileMode) error {
	if mode != 0600 && mode != 0700 {
		return ErrUnsupported
	}
	if e := safePath(filepath.Dir(path)); e != nil {
		return e
	}
	r, e := os.OpenRoot(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer func() { _ = r.Close() }()
	name := filepath.Base(path)
	if i, e := r.Lstat(name); e == nil && !owned(i, true) {
		return ErrUnsupported
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".login-migration-")
	if e != nil {
		return e
	}
	tmp := filepath.Base(f.Name())
	defer func() { _ = r.Remove(tmp) }()
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = r.Rename(tmp, name); e != nil {
		return e
	}
	return syncDir(filepath.Dir(path))
}

// Syncing each parent edge also repairs a prior mkdir whose fsync failed.
var syncPrivateParent = syncDir

func privateDir(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrUnsupported
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(path)
		if parent == path {
			return ErrUnsupported
		}
		if err = privateDir(parent); err != nil {
			return err
		}
		root, openErr := os.OpenRoot(parent)
		if openErr != nil {
			return openErr
		}
		err = root.Mkdir(filepath.Base(path), 0700)
		_ = root.Close()
		if err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if safePath(path) != nil || !owned(info, true) || !info.IsDir() {
		return ErrUnsupported
	}
	return syncPrivateParent(filepath.Dir(path))
}
