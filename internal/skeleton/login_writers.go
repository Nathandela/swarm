package skeleton

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func loginProcessAlive(pid int, startTime int64) bool {
	if pid <= 0 {
		return false
	}
	start, err := procstart.StartTime(pid)
	if err != nil {
		// Darwin's empty process snapshot is a size error, not ESRCH. A failed
		// identity read therefore needs an independent absence check.
		probe := syscall.Kill(pid, 0)
		return !errors.Is(probe, os.ErrNotExist) && !errors.Is(probe, syscall.ESRCH)
	}
	return startTime <= 0 || start == startTime
}
func readNativeProof(stateRoot, local, name string, value any) error {
	if !persist.ValidID(local) {
		return logins.ErrUnsafePath
	}
	root, err := os.OpenRoot(filepath.Join(stateRoot, local))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || info.Size() > 64<<10 {
		return logins.ErrUnsafePath
	}
	raw, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return logins.ErrUnsafePath
	}
	if rejectDuplicateJSONKeys(raw) != nil {
		return logins.ErrUnsafePath
	}
	return json.Unmarshal(raw, value)
}
func verifyLoginWritersStopped(stateRoot string, meta persist.Meta) error {
	if meta.Status.Process == status.ProcessRunning || loginProcessAlive(meta.ShimPID, meta.ShimStartTime) {
		return logins.ErrInUse
	}
	var native shim.NativeProcessInfo
	err := readNativeProof(stateRoot, meta.ID, shim.NativeProcessFile, &native)
	if errors.Is(err, os.ErrNotExist) && meta.LoginRuntime == nil {
		// Offline migration validates old writer custody before removing its old
		// records; ordinary rows without a native scope have no Swarm auth writer.
		return nil
	}
	if err != nil {
		return logins.ErrInUse
	}
	if native.SchemaVersion != shim.NativeWriterSchemaVersion || native.Provider != meta.AgentType || native.ShimPID != meta.ShimPID || native.ShimStartTime != meta.ShimStartTime {
		return logins.ErrInUse
	}
	expected := logins.Runtime{}
	if meta.LoginRuntime != nil {
		expected = *meta.LoginRuntime
	}
	if native.Runtime != expected {
		return logins.ErrInUse
	}
	generation, err := hex.DecodeString(native.Generation)
	if err != nil || len(generation) != 16 {
		return logins.ErrInUse
	}
	var stopped shim.NativeStoppedInfo
	if readNativeProof(stateRoot, meta.ID, shim.NativeStoppedFile, &stopped) != nil || stopped.SchemaVersion != shim.NativeWriterSchemaVersion || !stopped.WritersStopped || stopped.Native != native {
		return logins.ErrInUse
	}
	if runtime.GOOS == "linux" && !stopped.DescendantsContained {
		return logins.ErrInUse
	}
	if loginProcessAlive(native.ShimPID, native.ShimStartTime) || loginProcessAlive(native.PID, native.StartTime) || loginProcessAlive(native.BackendPID, native.BackendStartTime) {
		return logins.ErrInUse
	}
	groups := []int{native.PGID, native.BackendPID}
	if backend, ok := shim.ReadBackendInfo(filepath.Join(stateRoot, meta.ID)); ok {
		groups = append(groups, backend.PGID)
	}
	for _, pgid := range groups {
		if pgid > 0 && syscall.Kill(-pgid, 0) != syscall.ESRCH {
			return logins.ErrInUse
		}
	}
	return nil
}

func openRecoveryDir(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, logins.ErrUnsafePath
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := child.Lstat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = child.Close()
		return nil, logins.ErrUnsafePath
	}
	return child, nil
}

func syncRecoveryDir(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

func infoSysNative(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}
