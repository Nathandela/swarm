package scheduletimer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type definition struct {
	path   string
	data   []byte
	mode   os.FileMode
	exists bool
}

func snapshot(path string) (definition, error) {
	f := definition{path: path}
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if !st.Mode().IsRegular() {
		return f, fmt.Errorf("expected regular unit file at %s", path)
	}
	f.exists = true
	f.mode = st.Mode().Perm()
	f.data, err = os.ReadFile(path)
	return f, err
}

func (f definition) apply() error {
	if !f.exists {
		err := os.Remove(f.path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".swarm-schedule-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err = tmp.Write(f.data); err == nil {
		err = tmp.Chmod(f.mode)
	}
	err = errors.Join(err, tmp.Close())
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}

type transaction []func() error

func (t *transaction) remember(undo func() error) { *t = append(*t, undo) }
func (t transaction) finish(err *error) {
	if *err == nil {
		return
	}
	for i := len(t) - 1; i >= 0; i-- {
		if e := t[i](); e != nil {
			*err = errors.Join(*err, fmt.Errorf("timer rollback: %w", e))
		}
	}
}
