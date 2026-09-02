package skeleton

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remotegw"
)

const (
	machinePushCustodySchema = 1
	machinePushCustodyFile   = "pairing-push-revoke.json"
)

// machinePushCustodyRecord is self-contained cleanup authority. Pairing writes it before
// the test wake can bind an allocation; explicit device revoke writes it before deleting
// the registry row. A restart therefore never needs the row it may be recovering from.
type machinePushCustodyRecord struct {
	DeviceID string             `json:"device_id"`
	Push     device.PushBinding `json:"push"`
}

type machinePushCustodyDisk struct {
	SchemaVersion int                      `json:"schema_version"`
	Live          bool                     `json:"live"`
	Record        machinePushCustodyRecord `json:"record"`
}

type machinePushCustody struct {
	mu   sync.Mutex
	path string
	live bool
	rec  machinePushCustodyRecord
}

func openMachinePushCustody(stateDir string) (*machinePushCustody, error) {
	if stateDir == "" {
		return nil, errors.New("machine push custody requires a state directory")
	}
	path := filepath.Join(stateDir, "remote", machinePushCustodyFile)
	s := &machinePushCustody{path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read machine push custody: %w", err)
	}
	var disk machinePushCustodyDisk
	if err := json.Unmarshal(raw, &disk); err != nil {
		return nil, fmt.Errorf("parse machine push custody: %w", err)
	}
	if disk.SchemaVersion != machinePushCustodySchema {
		return nil, fmt.Errorf("machine push custody schema %d unsupported", disk.SchemaVersion)
	}
	if disk.Live {
		if err := validateMachinePushCustodyRecord(disk.Record); err != nil {
			return nil, fmt.Errorf("invalid machine push custody: %w", err)
		}
		s.live = true
		s.rec = cloneMachinePushCustodyRecord(disk.Record)
	}
	return s, nil
}

func validateMachinePushCustodyRecord(rec machinePushCustodyRecord) error {
	if rec.DeviceID == "" {
		return errors.New("empty device id")
	}
	return device.ValidatePushBinding(rec.Push)
}

// Stage is reserve-before-effect custody. Re-staging the exact record is idempotent; an
// unresolved different authority is never overwritten, because that would permanently
// lose the only capability able to delete its address.
func (s *machinePushCustody) Stage(deviceID string, push device.PushBinding) error {
	rec := machinePushCustodyRecord{DeviceID: deviceID, Push: clonePushBinding(push)}
	if err := validateMachinePushCustodyRecord(rec); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live {
		if sameMachinePushCustodyRecord(s.rec, rec) {
			return nil
		}
		return errors.New("a different machine push revoke is still in durable custody")
	}
	committed, err := s.persistLocked(machinePushCustodyDisk{
		SchemaVersion: machinePushCustodySchema, Live: true, Record: rec,
	})
	if err == nil || committed {
		s.live = true
		s.rec = cloneMachinePushCustodyRecord(rec)
	}
	return err
}

func (s *machinePushCustody) Pending() (machinePushCustodyRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.live {
		return machinePushCustodyRecord{}, false
	}
	return cloneMachinePushCustodyRecord(s.rec), true
}

// ClearExact writes an explicit empty state instead of unlinking the file. The exact
// compare prevents a delayed cleanup from erasing a newer obligation staged concurrently.
func (s *machinePushCustody) ClearExact(want machinePushCustodyRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.live {
		return nil
	}
	if !sameMachinePushCustodyRecord(s.rec, want) {
		return errors.New("refusing to clear a different machine push custody record")
	}
	committed, err := s.persistLocked(machinePushCustodyDisk{SchemaVersion: machinePushCustodySchema})
	if err == nil || committed {
		s.live = false
		s.rec = machinePushCustodyRecord{}
	}
	return err
}

func (s *machinePushCustody) persistLocked(disk machinePushCustodyDisk) (bool, error) {
	raw, err := json.Marshal(disk)
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".pairing-push-revoke-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return false, err
	}
	return true, syncMachinePushCustodyDir(dir)
}

var syncMachinePushCustodyDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

func clonePushBinding(push device.PushBinding) device.PushBinding {
	push.Address = append([]byte(nil), push.Address...)
	push.WakeKey = append([]byte(nil), push.WakeKey...)
	return push
}

func cloneMachinePushCustodyRecord(rec machinePushCustodyRecord) machinePushCustodyRecord {
	rec.Push = clonePushBinding(rec.Push)
	return rec
}

func samePushBinding(a, b device.PushBinding) bool {
	return a.GatewayURL == b.GatewayURL &&
		bytes.Equal(a.Address, b.Address) &&
		a.SubmitCapability == b.SubmitCapability &&
		a.MachineRevokeCapability == b.MachineRevokeCapability &&
		bytes.Equal(a.WakeKey, b.WakeKey) &&
		a.CapabilityRecordVersion == b.CapabilityRecordVersion &&
		a.Transport == b.Transport
}

func sameMachinePushCustodyRecord(a, b machinePushCustodyRecord) bool {
	return a.DeviceID == b.DeviceID && samePushBinding(a.Push, b.Push)
}

// reconcileMachinePushCustody classifies a restart atomically from durable facts. An
// exact registry match means ownership committed and only the stage needs clearing. Any
// other shape presents the self-contained revoke; custody clears only after a 2xx,
// including the gateway's idempotent tombstone response.
func reconcileMachinePushCustody(ctx context.Context, custody *machinePushCustody, registry *device.Registry, client *http.Client) error {
	if custody == nil {
		return nil
	}
	rec, ok := custody.Pending()
	if !ok {
		return nil
	}
	if registry != nil {
		if owned, found := registry.Get(rec.DeviceID); found && owned.Push != nil && samePushBinding(*owned.Push, rec.Push) {
			stateDir := filepath.Dir(filepath.Dir(custody.path))
			sealedGrant, err := grant.Load(filepath.Join(stateDir, "devices"), rec.DeviceID)
			if err != nil {
				return fmt.Errorf("load staged device grant: %w", err)
			}
			if sealedGrant != nil {
				return custody.ClearExact(rec)
			}
		}
	}
	var addr remotegw.PushAddress
	copy(addr[:], rec.Push.Address)
	revoker := &remotegw.HTTPAddressRevoker{
		BaseURL: rec.Push.GatewayURL, MachineRevokeCapability: rec.Push.MachineRevokeCapability, Client: client,
	}
	if err := revoker.RevokeAddress(ctx, addr); err != nil {
		return err
	}
	return custody.ClearExact(rec)
}
