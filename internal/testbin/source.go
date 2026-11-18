package testbin

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// WalkGoTree visits the working source, including new files, with the same hidden
// and underscore directory exclusions as Go package discovery.
func WalkGoTree(root string, visit fs.WalkDirFunc) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() && path != root {
			switch entry.Name() {
			case "build", "dist", "vendor", "node_modules":
				return fs.SkipDir
			}
			if strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), "_") {
				return fs.SkipDir
			}
			// A separate checkout may also have an ordinary directory name.
			if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
				return fs.SkipDir
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return visit(path, entry, err)
	})
}

// CanonicalTempDir supplies the path a private-storage or executable identity check expects.
func CanonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
