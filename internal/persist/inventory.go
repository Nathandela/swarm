package persist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// InspectSessions reports durable session metadata including hidden attempts.
// Unlike Scan, it reports unreadable records and never creates the state root.
func InspectSessions(ctx context.Context, dir string) ([]Meta, []error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, []error{err}
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(".")
	if err != nil {
		return nil, []error{err}
	}
	defer func() { _ = f.Close() }()

	var rows []Meta
	var issues []error
	for count := 0; count < 10000; {
		if err := ctx.Err(); err != nil {
			return rows, append(issues, err)
		}
		entries, err := f.ReadDir(min(128, 10000-count))
		for _, e := range entries {
			count++
			if !ValidID(e.Name()) {
				continue
			}
			if e.Type()&os.ModeSymlink != 0 {
				issues = append(issues, fmt.Errorf("state entry %q is a symlink", e.Name()))
				continue
			}
			if !e.IsDir() {
				continue
			}
			m, readErr := inspectSession(root, e.Name())
			if errors.Is(readErr, os.ErrNotExist) {
				// Other Swarm subsystems share this root. A launch sidefile
				// without metadata is evidence of a partial session.
				for _, side := range []string{"shim-launch.json", "shim.sock", "exit.json"} {
					if _, sideErr := root.Lstat(filepath.Join(e.Name(), side)); !errors.Is(sideErr, os.ErrNotExist) {
						issues = append(issues, fmt.Errorf("session evidence without readable metadata in %q", e.Name()))
						break
					}
				}
			} else if readErr != nil {
				issues = append(issues, fmt.Errorf("session %q: %w", e.Name(), readErr))
			} else {
				rows = append(rows, m)
			}
		}
		if err == io.EOF {
			return rows, issues
		}
		if err != nil {
			return rows, append(issues, err)
		}
	}
	return rows, append(issues, errors.New("session inventory entry limit reached"))
}

func inspectSession(root *os.Root, id string) (Meta, error) {
	f, err := root.OpenFile(filepath.Join(id, metaFile), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Meta{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return Meta{}, err
	}
	const limit = 4 << 20
	if !info.Mode().IsRegular() || info.Size() > limit {
		return Meta{}, errors.New("metadata is not a regular file or exceeds the inventory limit")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return Meta{}, err
	}
	if len(data) > limit {
		return Meta{}, errors.New("metadata exceeds the inventory limit")
	}
	return decodeMeta(data, id)
}
