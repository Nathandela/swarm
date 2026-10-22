package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
)

func TestLoginStateAnchorCannotBeSymlink(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(t.TempDir(), state); err != nil {
		t.Fatal(err)
	}
	if _, err := Serve(Config{StateDir: state}); !errors.Is(err, logins.ErrUnsafePath) {
		t.Fatalf("symlink state accepted: %v", err)
	}
}
