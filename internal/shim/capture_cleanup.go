package shim

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Only a new shim retires its old-format captures; never unlink a live old shim's log.
func removeLegacyCaptures(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		legacy := name == SnapshotFile || name == TranscriptFile
		if suffix, ok := strings.CutPrefix(name, TranscriptFile+"."); ok {
			n, err := strconv.Atoi(suffix)
			legacy = err == nil && n > 0
		}
		if legacy {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("shim: remove legacy capture %q: %w", name, err)
			}
		}
	}
	return nil
}
