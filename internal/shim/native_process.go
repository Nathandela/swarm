package shim

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

const NativeWriterSchemaVersion = 3

const NativeProcessFile = "native-process.json"
const NativeStoppedFile = "native-writers-stopped.json"

type NativeProcessInfo struct {
	SchemaVersion    int            `json:"schema_version"`
	Generation       string         `json:"generation"`
	BackendPID       int            `json:"backend_pid,omitempty"`
	BackendStartTime int64          `json:"backend_start_time,omitempty"`
	PID              int            `json:"pid"`
	PGID             int            `json:"pgid"`
	StartTime        int64          `json:"start_time"`
	ShimPID          int            `json:"shim_pid"`
	ShimStartTime    int64          `json:"shim_start_time"`
	Provider         string         `json:"provider"`
	Runtime          logins.Runtime `json:"runtime"`
	IncidentID       string         `json:"incident_id,omitempty"`
}

// NativeStoppedInfo proves the recorded native process groups stopped. Linux
// additionally proves adopted descendants were reaped; Darwin supplements the
// group proof with a provider-home process inventory before changing auth.
type NativeStoppedInfo struct {
	SchemaVersion        int               `json:"schema_version"`
	Native               NativeProcessInfo `json:"native"`
	WritersStopped       bool              `json:"writers_stopped"`
	DescendantsContained bool              `json:"descendants_contained"`
}

type managedNativeScope struct {
	root      *os.Root
	info      NativeProcessInfo
	recorded  bool
	stopReap  chan struct{}
	subreaper bool
}

// managedReapInterval paces reaping of adopted orphans while a managed session runs.
const managedReapInterval = 2 * time.Second

// reapWhileRunning reaps adopted orphans until finish, never keep (the pids an exec.Cmd waits
// for). As a subreaper this shim inherits every double-forked or setsid descendant, and nothing
// else waits for them before the final ContainAndReap.
func (s *managedNativeScope) reapWhileRunning(keep ...int) {
	if s == nil || !s.subreaper {
		return
	}
	s.stopReap = make(chan struct{})
	go func(stop <-chan struct{}) {
		tick := time.NewTicker(managedReapInterval)
		warned := false
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				if !processcontain.ReapAdoptedZombies(keep...) && !warned {
					warned = true
					log.Printf("shim: /proc/self/task/*/children is unavailable; adopted orphans are reaped only when the session ends")
				}
			}
		}
	}(s.stopReap)
}

func beginManagedNativeScope(cfg Config) (*managedNativeScope, error) {
	if cfg.NativeProvider != logins.ProviderClaude && cfg.NativeProvider != logins.ProviderCodex {
		return nil, nil
	}
	// Run is the dedicated per-session shim. Installing this in a shared daemon
	// or test process would confer custody over unrelated children.
	subreaper := true
	if err := processcontain.EnableSubreaper(); err != nil {
		if !errors.Is(err, processcontain.ErrUnsupported) {
			return nil, errors.New("shim: native descendant containment unavailable")
		}
		subreaper = false
	}
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		return nil, errors.New("shim: managed shim identity unavailable")
	}
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(cfg.SessionDir)
	if err != nil {
		return nil, errors.New("shim: native writer record unavailable")
	}
	scope := &managedNativeScope{subreaper: subreaper, root: root, info: NativeProcessInfo{SchemaVersion: NativeWriterSchemaVersion, Generation: hex.EncodeToString(generation[:]), ShimPID: os.Getpid(), ShimStartTime: start, Provider: cfg.NativeProvider, IncidentID: cfg.InputEmbargo}}
	if cfg.LoginRuntime != nil {
		scope.info.Runtime = *cfg.LoginRuntime
	}
	for _, name := range []string{NativeStoppedFile, NativeProcessFile} {
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = root.Close()
			return nil, errors.New("shim: old native writer proof could not be removed")
		}
	}
	if err := fsyncNativeRoot(root); err != nil {
		_ = root.Close()
		return nil, err
	}
	return scope, nil
}

func (s *managedNativeScope) record(pid int, backend *backendProc) error {
	if s == nil {
		return nil
	}
	start, err := procstart.StartTime(pid)
	if err != nil {
		return errors.New("shim: native writer identity unavailable")
	}
	s.info.PID, s.info.PGID, s.info.StartTime = pid, pid, start
	if backend != nil {
		if backend.startTime <= 0 {
			return errors.New("shim: backend writer identity unavailable")
		}
		s.info.BackendPID, s.info.BackendStartTime = backend.pgid, backend.startTime
	}
	if err := writeNativeRecord(s.root, NativeProcessFile, s.info); err != nil {
		return errors.New("shim: native writer record could not be made durable")
	}
	s.recorded = true
	return nil
}

func (s *managedNativeScope) finish() error {
	if s == nil {
		return nil
	}
	defer func() { _ = s.root.Close() }()
	if s.stopReap != nil {
		s.stopReap <- struct{}{} // unbuffered: returns only once the reaper is between passes
	}
	if s.subreaper {
		if err := processcontain.ContainAndReap(); err != nil {
			return errors.New("shim: native descendants could not be contained and reaped")
		}
	} else {
		deadline := time.Now().Add(2 * time.Second)
		for {
			stopped := true
			for _, pgid := range []int{s.info.PGID, s.info.BackendPID} {
				if pgid > 0 && syscall.Kill(-pgid, 0) != syscall.ESRCH {
					stopped = false
				}
			}
			if stopped {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("shim: native process groups are not confirmed stopped")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !s.recorded {
		// Startup can fail before the main CLI identity is captured, even after
		// a backend has spawned descendants. ECHILD above proves the whole
		// dedicated shim scope is drained; publish its exact zero/native record
		// so a clean failure can be retried without inferring safety from absence.
		if err := writeNativeRecord(s.root, NativeProcessFile, s.info); err != nil {
			return errors.New("shim: native writer record could not be made durable")
		}
	}
	proof := NativeStoppedInfo{SchemaVersion: NativeWriterSchemaVersion, Native: s.info, WritersStopped: true, DescendantsContained: s.subreaper}
	if err := writeNativeRecord(s.root, NativeStoppedFile, proof); err != nil {
		return errors.New("shim: native writer death proof could not be made durable")
	}
	return nil
}

func fsyncNativeRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func writeNativeRecord(root *os.Root, name string, value any) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := ".native-state-" + hex.EncodeToString(nonce[:])
	f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = root.Remove(temp) }()
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = root.Rename(temp, name); err != nil {
		return err
	}
	return fsyncNativeRoot(root)
}
