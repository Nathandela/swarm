package testbin

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func processIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, entry := range entries {
		if pid, err := strconv.Atoi(entry.Name()); err == nil {
			info, err := entry.Info()
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return nil, fmt.Errorf("process %d has no ownership metadata", pid)
			}
			if stat.Uid == uint32(os.Getuid()) {
				pids = append(pids, pid)
			}
		}
	}
	return pids, nil
}

func processExecutable(pid int) (string, error) {
	exe, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	return strings.TrimSuffix(exe, " (deleted)"), err
}
