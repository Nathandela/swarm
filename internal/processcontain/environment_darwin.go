//go:build darwin

package processcontain

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Darwin cannot adopt/reap escaped children. Read same-user native process
// environments in memory to prove no writer still owns a protected auth home.
func EnvironmentWriters(homes, sessionIDs []string) ([]Identity, error) {
	if len(homes) == 0 && len(sessionIDs) == 0 {
		return nil, nil
	}
	var writers []Identity
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("%w: snapshot: %v", ErrUnavailable, err)
	}
	for _, proc := range processes {
		pid := int(proc.Proc.P_pid)
		if pid <= 0 || pid == os.Getpid() || proc.Eproc.Ucred.Uid != uint32(os.Getuid()) || proc.Proc.P_stat == 5 {
			continue
		}
		raw, readErr := unix.SysctlRaw("kern.procargs2", pid)
		current, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.ENOENT) {
			continue
		}
		if err != nil {
			if possibleNativeWriter(proc) {
				return nil, ErrUnavailable
			}
			continue
		}
		if !sameLiveDarwinProcess(proc, *current) {
			continue
		}
		if readErr != nil {
			if possibleNativeWriter(proc) {
				return nil, fmt.Errorf("%w: process %d argument read: %v", ErrUnavailable, pid, readErr)
			}
			continue
		}
		matches, err := darwinAuthEnvironmentMatches(raw, homes, sessionIDs)
		if err != nil {
			if possibleNativeWriter(proc) {
				return nil, fmt.Errorf("%w: process %d argument shape: %v", ErrUnavailable, pid, err)
			}
			continue
		}
		if matches {
			writers = append(writers, Identity{PID: pid, StartTime: int64(proc.Proc.P_starttime.Sec)*1_000_000 + int64(proc.Proc.P_starttime.Usec)})
		}
	}
	return writers, nil
}

func sameLiveDarwinProcess(before, current unix.KinfoProc) bool {
	// A process can exit after the snapshot but before its arguments are read.
	return current.Eproc.Ucred.Uid == before.Eproc.Ucred.Uid && current.Proc.P_starttime == before.Proc.P_starttime && current.Proc.P_stat != 5
}

func possibleNativeWriter(proc unix.KinfoProc) bool {
	var name []byte
	for _, c := range proc.Proc.P_comm {
		if c == 0 {
			break
		}
		name = append(name, byte(c))
	}
	value := strings.ToLower(string(name))
	return strings.Contains(value, "codex") || strings.Contains(value, "claude") || value == "node" || value == "bun"
}
func darwinAuthEnvironmentMatches(raw []byte, homes, sessionIDs []string) (bool, error) {
	if len(raw) < 4 || len(raw) > 8<<20 {
		return false, errors.New("invalid process environment")
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	if argc < 1 || argc > 65536 {
		return false, errors.New("invalid process argument count")
	}
	offset := 4
	readString := func() ([]byte, bool) {
		if offset >= len(raw) {
			return nil, false
		}
		end := bytes.IndexByte(raw[offset:], 0)
		if end < 0 {
			return nil, false
		}
		value := raw[offset : offset+end]
		offset += end + 1
		return value, true
	}
	if _, ok := readString(); !ok {
		return false, errors.New("invalid executable path")
	}
	for offset < len(raw) && raw[offset] == 0 {
		offset++
	}
	for i := 0; i < argc; i++ {
		if _, ok := readString(); !ok {
			return false, errors.New("invalid process arguments")
		}
	}
	for offset < len(raw) {
		entry, ok := readString()
		if !ok {
			return false, errors.New("invalid process environment")
		}
		if len(entry) == 0 {
			continue
		}
		key, value, ok := bytes.Cut(entry, []byte{'='})
		if !ok {
			continue
		}
		switch string(key) {
		case "CODEX_HOME", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR":
			for _, home := range homes {
				if filepath.Clean(string(value)) == home {
					return true, nil
				}
			}
		case "SWARM_SESSION_ID":
			for _, id := range sessionIDs {
				if string(value) == id {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
