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

// ReleaseAccountEmbargo is called only after the recovery authority has
// durably committed identity/history readiness for this exact successor.
func (d *Daemon) ReleaseAccountEmbargo(id, incidentID string) error {
	meta, ok := d.Get(id)
	if !ok || meta.AccountBinding == nil || meta.InputEmbargo != incidentID || incidentID == "" {
		return errors.New("daemon: account embargo identity mismatch")
	}
	raw, err := os.ReadFile(filepath.Join(d.sessionDir(id), shimLaunchConfigFile))
	if err != nil {
		return err
	}
	var cfg shimSpawnConfig
	if json.Unmarshal(raw, &cfg) != nil || cfg.InputEmbargo != incidentID || cfg.InputEmbargoToken == "" {
		return errors.New("daemon: account embargo unavailable")
	}
	conn, caps, err := dialShimHello(cfg.SocketPath)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if !caps.AccountInputEmbargo {
		return errors.New("daemon: shim lacks account input fencing")
	}
	_ = conn.SetDeadline(time.Now().Add(helloIO))
	payload, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeAccountEmbargoRelease, IncidentID: incidentID, Token: cfg.InputEmbargoToken})
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
	if err != nil || kind != wire.TControl || result.Type != shimwire.TypeAccountEmbargoResult || result.Refused != "" {
		return errors.New("daemon: account embargo release unconfirmed")
	}
	return nil
}
