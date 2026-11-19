package worktree

import (
	"os"
	"path/filepath"
	"strings"
)

// PathsOverlap reports whether two directories are equal or one contains the
// other, including existing aliases on case-insensitive filesystems.
func PathsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return isWithin(a, b) || isWithin(b, a) || sameAncestor(a, b) || sameAncestor(b, a)
}

// Compare existing inode identities on case-insensitive filesystems and aliases.
func sameAncestor(parent, path string) bool {
	ancestor, err := os.Stat(parent)
	if err != nil {
		return false
	}
	for {
		current, err := os.Stat(path)
		if err == nil && os.SameFile(ancestor, current) {
			return true
		}
		next := filepath.Dir(path)
		if next == path {
			return false
		}
		path = next
	}
}

func isWithin(parent, candidate string) bool {
	rel, err := filepath.Rel(parent, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
