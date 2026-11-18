package testbin

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWalkGoTreeVisitsCurrentCheckout(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"src/live.go", "nested/.git", "nested/sibling.go", ".worktrees/other/sibling.go", "_releaseprobe-123/main.go"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var visited []string
	if err := WalkGoTree(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			visited = append(visited, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(visited) != 1 || visited[0] != filepath.Join(root, "src", "live.go") {
		t.Fatalf("visited %v", visited)
	}
}
