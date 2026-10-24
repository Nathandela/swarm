package testbin

import (
	"errors"
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
	var scanErr error
	for _, entry := range entries {
		if pid, err := strconv.Atoi(entry.Name()); err == nil {
			info, err := entry.Info()
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				scanErr = errors.Join(scanErr, err)
				continue
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				scanErr = errors.Join(scanErr, fmt.Errorf("process %d has no ownership metadata", pid))
				continue
			}
			if stat.Uid == uint32(os.Getuid()) {
				// Nondumpable/privileged processes have kernel-owned proc contents.
				// Such fixtures need recorded PID/start custody; executable scanning cannot own them.
				exeInfo, err := os.Lstat(filepath.Join("/proc", entry.Name(), "exe"))
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					scanErr = errors.Join(scanErr, err)
					continue
				}
				exeStat, ok := exeInfo.Sys().(*syscall.Stat_t)
				if !ok {
					scanErr = errors.Join(scanErr, fmt.Errorf("process %d executable has no ownership metadata", pid))
					continue
				}
				if exeStat.Uid != uint32(os.Getuid()) {
					continue
				}
				pids = append(pids, pid)
			}
		}
	}
	return pids, scanErr
}

func processExecutable(pid int) (string, error) {
	exe, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	return strings.TrimSuffix(exe, " (deleted)"), err
}
