package shim

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/procstart"
)

const NativeProcessFile = "native-process.json"

type NativeProcessInfo struct {
	SchemaVersion int              `json:"schema_version"`
	PID           int              `json:"pid"`
	PGID          int              `json:"pgid"`
	StartTime     int64            `json:"start_time"`
	ShimPID       int              `json:"shim_pid"`
	ShimStartTime int64            `json:"shim_start_time"`
	Binding       accounts.Binding `json:"binding"`
	IncidentID    string           `json:"incident_id,omitempty"`
}

func recordManagedNativeProcess(cfg Config, pid int) error {
	if cfg.AccountBinding == nil {
		return nil
	}
	start, err := procstart.StartTime(pid)
	if err != nil {
		return errors.New("shim: native writer identity unavailable")
	}
	shimStart, err := procstart.StartTime(os.Getpid())
	if err != nil {
		return errors.New("shim: native writer identity unavailable")
	}
	info := NativeProcessInfo{SchemaVersion: 1, PID: pid, PGID: pid, StartTime: start, ShimPID: os.Getpid(), ShimStartTime: shimStart, Binding: *cfg.AccountBinding, IncidentID: cfg.InputEmbargo}
	root, err := os.OpenRoot(cfg.SessionDir)
	if err != nil {
		return errors.New("shim: native writer record unavailable")
	}
	defer func() { _ = root.Close() }()
	if err = writeAccountRecord(root, NativeProcessFile, info); err != nil {
		return errors.New("shim: native writer record could not be made durable")
	}
	return nil
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
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
