//go:build linux

package persist

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nathandela/swarm/internal/procstart"
)

// MatchCLIProcessImage checks the loaded executable, including an unlinked image
// retained across an installer update. PID/start alone cannot detect a later exec.
func MatchCLIProcessImage(pid int, start int64, path, expected string) bool {
	if pid <= 0 || start <= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	before, err := procstart.StartTime(pid)
	if err != nil || before != start {
		return false
	}
	if strings.HasPrefix(expected, "sha256:") {
		if !MatchCLIFingerprint(path, expected) {
			return false
		}
		expected = strings.Split(expected, ":")[2]
	}
	executable := fmt.Sprintf("/proc/%d/exe", pid)
	match := func() bool {
		file, err := os.Open(executable)
		if err != nil {
			return false
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return false
		}
		actual, err := cliMetadataFingerprint(path, info)
		target, linkErr := os.Readlink(executable)
		return err == nil && actual == expected && linkErr == nil && (target == path || target == path+" (deleted)")
	}
	// Reopen independently: an earlier fd still describes the old image after exec.
	if !match() || !match() {
		return false
	}
	after, err := procstart.StartTime(pid)
	return err == nil && after == before
}
