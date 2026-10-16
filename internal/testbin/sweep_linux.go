package testbin

import (
	"os"
	"path/filepath"
	"strconv"
)

func processIDs() []int {
	entries, _ := os.ReadDir("/proc")
	var pids []int
	for _, entry := range entries {
		if pid, err := strconv.Atoi(entry.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func processExecutable(pid int) (string, error) {
	return os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
}
