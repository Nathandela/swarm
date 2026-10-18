package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

// DispatchOpenCode requires the caller's durable operation marker and controller/input fences.
func (d *Daemon) DispatchOpenCode(ctx context.Context, id string, req opencodebridge.DispatchRequest) (opencodebridge.Receipt, error) {
	m, ok := d.Get(id)
	if !ok || m.Status.Process != status.ProcessRunning || !openCodeLaunchComplete(m) {
		return opencodebridge.Receipt{}, errors.New("OpenCode runtime: native binding not ready")
	}
	b, err := d.SessionOpenCodeBridge(id)
	if err != nil {
		return opencodebridge.Receipt{}, err
	}
	if req.Version != 1 || req.RootID != b.RootID || req.SessionInstance != b.SessionInstance || req.BackendGeneration != b.BackendGeneration {
		return opencodebridge.Receipt{}, errors.New("OpenCode runtime: dispatch binding mismatch")
	}
	raw, err := json.Marshal(req)
	if err != nil || len(raw) > 64<<10 {
		return opencodebridge.Receipt{}, errors.New("OpenCode runtime: dispatch exceeds bridge boundary")
	}
	conn, caps, err := dialShimHello(shimSocketPath(d.cfg.StateDir, id))
	if err != nil {
		return opencodebridge.Receipt{}, errors.New("OpenCode runtime: dispatch channel unavailable")
	}
	defer conn.Close()
	if !caps.OpenCodeDispatch {
		return opencodebridge.Receipt{}, errors.New("OpenCode runtime: shim dispatch fence unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	if ctx.Err() != nil {
		return opencodebridge.Receipt{}, ctx.Err()
	}
	data, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeOpenCodeDispatch, OpenCodeRequest: raw})
	if wire.WriteFrame(conn, wire.TControl, data) != nil {
		return opencodebridge.Receipt{}, errors.New("OpenCode runtime: dispatch write outcome uncertain")
	}
	kind, data, err := wire.ReadFrame(conn)
	if err != nil {
		return opencodebridge.Receipt{}, errors.New("OpenCode runtime: dispatch outcome uncertain")
	}
	result, err := shimwire.Decode(data)
	if err != nil || kind != wire.TControl || result.Type != shimwire.TypeOpenCodeDispatchResult {
		return opencodebridge.Receipt{}, errors.New("OpenCode runtime: dispatch receipt unavailable")
	}
	if result.Refused != "" {
		return opencodebridge.Receipt{Status: "refused", Reason: result.Refused, OperationID: req.OperationID}, nil
	}
	if result.OpenCodeError != "" {
		return opencodebridge.Receipt{}, errors.New("OpenCode runtime: native dispatch outcome uncertain")
	}
	var receipt opencodebridge.Receipt
	if json.Unmarshal(result.OpenCodeReceipt, &receipt) != nil || !sameOpenCodeIdentity(receipt.Identity, b) || receipt.OperationID != req.OperationID {
		return opencodebridge.Receipt{}, errors.New("OpenCode runtime: dispatch receipt identity unconfirmed")
	}
	return receipt, nil
}
