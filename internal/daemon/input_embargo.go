package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/wire"
)

// ReleaseInputEmbargo is called only after the recovery authority has
// durably committed identity/history readiness for this exact successor.
func (d *Daemon) ReleaseInputEmbargo(id, incidentID string) error {
	meta, ok := d.Get(id)
	if !ok || meta.LoginRuntime == nil || meta.InputEmbargo != incidentID || incidentID == "" {
		return errors.New("daemon: input embargo identity mismatch")
	}
	raw, err := os.ReadFile(filepath.Join(d.sessionDir(id), shimLaunchConfigFile))
	if err != nil {
		return err
	}
	var cfg shimSpawnConfig
	if json.Unmarshal(raw, &cfg) != nil || cfg.InputEmbargo != incidentID || cfg.InputEmbargoToken == "" {
		return errors.New("daemon: input embargo unavailable")
	}
	conn, caps, err := dialShimHello(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if !caps.InputEmbargo {
		return errors.New("daemon: shim lacks input fencing")
	}
	_ = conn.SetDeadline(time.Now().Add(helloIO))
	payload, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeInputEmbargoRelease, IncidentID: incidentID, Token: cfg.InputEmbargoToken})
	if err != nil {
		return err
	}
	if err = wire.WriteFrame(conn, wire.TControl, payload); err != nil {
		return err
	}
	kind, raw, err := wire.ReadFrame(conn)
	if err != nil {
		return err
	}
	result, err := shimwire.Decode(raw)
	if err != nil || kind != wire.TControl || result.Type != shimwire.TypeInputEmbargoResult || result.Refused != "" {
		return errors.New("daemon: input embargo release unconfirmed")
	}
	return nil
}
