package daemon

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/shim"
)

// SessionNativeGeneration is stable for the actual shim-owned native process,
// not for an observer's socket connection or daemon incarnation.
func (d *Daemon) SessionNativeGeneration(id string) (string, error) {
	unavailable := errors.New("manager native process custody unavailable")
	meta, ok := d.Get(id)
	if !ok || meta.AgentType != "codex" && meta.AgentType != "claude" {
		return "", unavailable
	}
	root, err := os.OpenRoot(d.sessionDir(id))
	if err != nil {
		return "", unavailable
	}
	defer func() { _ = root.Close() }()
	file, err := root.OpenFile(shim.NativeProcessFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", unavailable
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm() != 0600 || stat.Size() > 16<<10 {
		return "", unavailable
	}
	owner, ok := stat.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) || owner.Nlink != 1 {
		return "", unavailable
	}
	raw, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return "", unavailable
	}
	var native shim.NativeProcessInfo
	if json.Unmarshal(raw, &native) != nil || native.SchemaVersion != shim.NativeWriterSchemaVersion || len(native.Generation) != 32 || strings.ToLower(native.Generation) != native.Generation || native.Provider != meta.AgentType || native.ShimPID != meta.ShimPID || native.ShimStartTime != meta.ShimStartTime {
		return "", unavailable
	}
	if _, err := hex.DecodeString(native.Generation); err != nil {
		return "", unavailable
	}
	alive := func(pid int, start int64) bool {
		if pid <= 0 || start <= 0 || !pidAlive(pid) {
			return false
		}
		actual, err := procStartTimeFn(pid)
		return err == nil && actual == start
	}
	if !alive(native.ShimPID, native.ShimStartTime) || !alive(native.PID, native.StartTime) {
		return "", unavailable
	}
	if meta.LoginRuntime != nil {
		if native.Runtime != *meta.LoginRuntime {
			return "", unavailable
		}
	} else if native.Runtime != (logins.Runtime{}) {
		return "", unavailable
	}
	if meta.AgentType == "codex" {
		backend, ok := shim.ReadBackendInfo(d.sessionDir(id))
		if !ok || backend.PID != native.BackendPID || backend.StartedAtMS != native.BackendStartTime || !alive(native.BackendPID, native.BackendStartTime) {
			return "", unavailable
		}
	}
	current, ok := d.Get(id)
	if !ok || current.ShimPID != meta.ShimPID || current.ShimStartTime != meta.ShimStartTime {
		return "", unavailable
	}
	return native.Generation, nil
}
