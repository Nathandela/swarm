//go:build linux

package processcontain

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// EnvironmentWriters inspects owner-local process environments without exposing
// them. Call after the known native roots stop, before changing their auth home.
func EnvironmentWriters(homes, sessionIDs []string) ([]Identity, error) {
	if len(homes) == 0 && len(sessionIDs) == 0 {
		return nil, nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, ErrUnavailable
	}
	var writers []Identity
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == os.Getpid() {
			continue
		}
		dir := filepath.Join("/proc", entry.Name())
		info, err := os.Stat(dir)
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Getuid()) {
			continue
		}
		before, err := readProc(pid)
		if err != nil || before.state == "Z" || before.state == "X" {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(dir, "environ"))
		after, err := readProc(pid)
		if err != nil || after.identity != before.identity {
			continue
		}
		if readErr != nil {
			comm, _ := os.ReadFile(filepath.Join(dir, "comm"))
			name := strings.TrimSpace(strings.ToLower(string(comm)))
			if strings.Contains(name, "codex") || strings.Contains(name, "claude") || name == "node" || name == "bun" {
				return nil, ErrUnavailable
			}
			continue
		}
		if len(raw) > 8<<20 {
			return nil, ErrUnavailable
		}
		matched := false
		for _, value := range bytes.Split(raw, []byte{0}) {
			key, path, ok := bytes.Cut(value, []byte{'='})
			if !ok {
				continue
			}
			switch string(key) {
			case "CODEX_HOME", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR":
				for _, home := range homes {
					matched = matched || filepath.Clean(string(path)) == home
				}
			case "SWARM_SESSION_ID":
				for _, id := range sessionIDs {
					matched = matched || string(path) == id
				}
			}
		}
		if matched {
			writers = append(writers, before.identity)
		}
	}
	return writers, nil
}
