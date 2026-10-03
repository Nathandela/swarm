package shim

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

const ManagedWriterSchemaVersion = 2

const NativeProcessFile = "native-process.json"
const NativeStoppedFile = "native-writers-stopped.json"

type NativeProcessInfo struct {
	SchemaVersion    int              `json:"schema_version"`
	Generation       string           `json:"generation"`
	BackendPID       int              `json:"backend_pid,omitempty"`
	BackendStartTime int64            `json:"backend_start_time,omitempty"`
	PID              int              `json:"pid"`
	PGID             int              `json:"pgid"`
	StartTime        int64            `json:"start_time"`
	ShimPID          int              `json:"shim_pid"`
	ShimStartTime    int64            `json:"shim_start_time"`
	Binding          accounts.Binding `json:"binding"`
	IncidentID       string           `json:"incident_id,omitempty"`
}

// NativeStoppedInfo is published by a clean managed shim only after all
// descendants (including setsid/double-fork children) have died and been reaped.
// Absence after SIGKILL/crash is deliberately not a writer-death proof.
type NativeStoppedInfo struct {
	SchemaVersion  int               `json:"schema_version"`
	Native         NativeProcessInfo `json:"native"`
	WritersStopped bool              `json:"writers_stopped"`
}

type managedNativeScope struct {
	root     *os.Root
	info     NativeProcessInfo
	recorded bool
}

func beginManagedNativeScope(cfg Config) (*managedNativeScope, error) {
	if cfg.AccountBinding == nil {
		return nil, nil
	}
	// Run is the dedicated per-session shim. Installing this in a shared daemon
	// or test process would confer custody over unrelated children.
	if err := processcontain.EnableSubreaper(); err != nil {
		return nil, errors.New("shim: managed descendant containment unavailable")
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
	scope := &managedNativeScope{root: root, info: NativeProcessInfo{SchemaVersion: ManagedWriterSchemaVersion, Generation: hex.EncodeToString(generation[:]), ShimPID: os.Getpid(), ShimStartTime: start, Binding: *cfg.AccountBinding, IncidentID: cfg.InputEmbargo}}
	for _, name := range []string{NativeStoppedFile, NativeProcessFile} {
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = root.Close()
			return nil, errors.New("shim: old native writer proof could not be removed")
		}
	}
	if err := fsyncAccountRoot(root); err != nil {
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
	if err := writeAccountRecord(s.root, NativeProcessFile, s.info); err != nil {
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
	if err := processcontain.ContainAndReap(); err != nil {
		return errors.New("shim: managed descendants could not be contained and reaped")
	}
	if !s.recorded {
		// Startup can fail before the main CLI identity is captured, even after
		// a backend has spawned descendants. ECHILD above proves the whole
		// dedicated shim scope is drained; publish its exact zero/native record
		// so a clean failure can be retried without inferring safety from absence.
		if err := writeAccountRecord(s.root, NativeProcessFile, s.info); err != nil {
			return errors.New("shim: native writer record could not be made durable")
		}
	}
	proof := NativeStoppedInfo{SchemaVersion: ManagedWriterSchemaVersion, Native: s.info, WritersStopped: true}
	if err := writeAccountRecord(s.root, NativeStoppedFile, proof); err != nil {
		return errors.New("shim: native writer death proof could not be made durable")
	}
	return nil
}

func fsyncAccountRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func writeAccountRecord(root *os.Root, name string, value any) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := ".account-state-" + hex.EncodeToString(nonce[:])
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
	return fsyncAccountRoot(root)
}
