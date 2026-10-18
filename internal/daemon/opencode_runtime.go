package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

const openCodeJoinTimeout = 45 * time.Second

func (d *Daemon) readOpenCodeLaunch(id string) (shimSpawnConfig, error) {
	b, err := d.SessionOpenCodeBridge(id)
	if err != nil {
		return shimSpawnConfig{}, err
	}
	root, err := os.OpenRoot(d.sessionDir(id))
	if err != nil {
		return shimSpawnConfig{}, errors.New("OpenCode runtime: launch custody unavailable")
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(shimLaunchConfigFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return shimSpawnConfig{}, errors.New("OpenCode runtime: launch custody unavailable")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 256<<10 {
		return shimSpawnConfig{}, errors.New("OpenCode runtime: unsafe launch custody")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		return shimSpawnConfig{}, errors.New("OpenCode runtime: unsafe launch custody")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 {
		return shimSpawnConfig{}, errors.New("OpenCode runtime: launch custody unreadable")
	}
	var lc shimSpawnConfig
	if json.Unmarshal(raw, &lc) != nil || lc.SessionID != id || lc.Cwd != b.Directory || lc.OpenCode == nil || lc.OpenCode.RootID != b.RootID || lc.OpenCode.Generation != b.BackendGeneration || shim.ValidateOpenCodeConfig(shim.Config{SessionDir: lc.SessionDir, Argv: lc.Argv, OpenCode: lc.OpenCode}) != nil {
		return shimSpawnConfig{}, errors.New("OpenCode runtime: launch custody mismatch")
	}
	return lc, nil
}

// OpenCodeResumeEnvironment restores native sources from the original private launch, not a new caller.
func (d *Daemon) OpenCodeResumeEnvironment(id string) ([]string, error) {
	lc, err := d.readOpenCodeLaunch(id)
	if err != nil {
		return nil, err
	}
	return d.OpenCodeEnvironment(lc.OpenCodeEnv)
}

func (d *Daemon) openCodeCurrentMeta(b opencodebridge.Binding) (persist.Meta, error) {
	m, ok := d.Get(b.WorkerID)
	if !ok || m.RequiredRuntime != persist.RequiredRuntimeOpenCode || m.OpenCodeBinding == nil || m.OpenCodeBinding.Generation != b.BackendGeneration || m.ConversationID != b.RootID || m.ProviderCwd() != b.Directory || (m.OpenCodeBinding.Initial != nil) != b.InitialPrompt || b.NativeCreatedAtMS > 0 && m.OpenCodeBinding.NativeCreatedAtMS != b.NativeCreatedAtMS {
		return persist.Meta{}, errors.New("OpenCode runtime: session retired or native binding changed")
	}
	return m, nil
}

func sameOpenCodeIdentity(i opencodebridge.Identity, b opencodebridge.Binding) bool {
	return i.Version == 1 && i.WorkerID == b.WorkerID && i.SessionInstance == b.SessionInstance && i.RootID == b.RootID && i.Directory == b.Directory && i.BackendGeneration == b.BackendGeneration && i.NativeCreatedAtMS > 0 && (b.NativeCreatedAtMS == 0 || i.NativeCreatedAtMS == b.NativeCreatedAtMS)
}

func waitOpenCodeReady(ctx context.Context, c *opencodebridge.Client, b opencodebridge.Binding, initial bool) (opencodebridge.Ready, error) {
	for {
		r, err := c.Ready(ctx)
		if err == nil {
			if r.Version != 1 || r.WorkerID != b.WorkerID || r.SessionInstance != b.SessionInstance || r.RootID != b.RootID || r.Directory != b.Directory || r.BackendGeneration != b.BackendGeneration || r.NativeVersion != "1.19.3" {
				return r, errors.New("OpenCode runtime: native bridge identity mismatch")
			}
			if r.Bound && !sameOpenCodeIdentity(r.Identity, b) {
				return r, errors.New("OpenCode runtime: native creation identity mismatch")
			}
			if r.Bound && r.Ready && (!initial || r.InitialSupported) {
				return r, nil
			}
		} else {
			var rejected *opencodebridge.Error
			if errors.As(err, &rejected) {
				return r, errors.New("OpenCode runtime: native bridge refused readiness")
			}
		}
		select {
		case <-ctx.Done():
			return opencodebridge.Ready{}, errors.New("OpenCode runtime: native bridge readiness unconfirmed")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (d *Daemon) joinOpenCode(ctx context.Context, id string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-d.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	b, err := d.SessionOpenCodeBridge(id)
	if err != nil {
		return err
	}
	lc, err := d.readOpenCodeLaunch(id)
	if err != nil {
		return err
	}
	c, err := opencodebridge.NewClient(b.SocketPath, b.Token)
	if err != nil {
		return err
	}
	defer c.Close()
	ready, err := waitOpenCodeReady(ctx, c, b, false)
	if err != nil {
		return err
	}
	m, err := d.openCodeCurrentMeta(b)
	if err != nil {
		return err
	}
	if m.OpenCodeBinding.Phase == persist.OpenCodePhasePlanned {
		next := *persist.CloneOpenCodeBinding(m.OpenCodeBinding)
		next.Revision++
		next.Phase = persist.OpenCodePhaseBound
		next.NativeCreatedAtMS = ready.NativeCreatedAtMS
		if err := d.SetOpenCodeBinding(id, m.OpenCodeBinding.Revision, next); err != nil {
			return err
		}
		m, err = d.openCodeCurrentMeta(b)
		if err != nil {
			return err
		}
	}
	b.NativeCreatedAtMS = m.OpenCodeBinding.NativeCreatedAtMS
	ack, err := c.Ack(ctx, opencodebridge.AckRequest{Version: 1, SessionInstance: b.SessionInstance, BackendGeneration: b.BackendGeneration, RootID: b.RootID, NativeCreatedAtMS: b.NativeCreatedAtMS})
	if err != nil || !ack.Acknowledged || !ack.Bound || !ack.Ready || !sameOpenCodeIdentity(ack.Identity, b) {
		return errors.New("OpenCode runtime: durable binding acknowledgement unconfirmed")
	}
	if m.OpenCodeBinding.Phase == persist.OpenCodePhaseBound {
		next := *persist.CloneOpenCodeBinding(m.OpenCodeBinding)
		next.Revision++
		next.Phase = persist.OpenCodePhaseReady
		if err := d.SetOpenCodeBinding(id, m.OpenCodeBinding.Revision, next); err != nil {
			return err
		}
		m, err = d.openCodeCurrentMeta(b)
		if err != nil {
			return err
		}
	}
	if d.cfg.OpenCodeReady != nil {
		if err := d.cfg.OpenCodeReady(ctx, cloneOpenCodeMeta(m)); err != nil {
			return errors.New("OpenCode runtime: native observation not ready")
		}
	}
	if m.OpenCodeBinding.Initial != nil && m.OpenCodeBinding.Initial.Phase != persist.OpenCodeInitialAdmitted {
		if err := d.admitOpenCodeInitial(ctx, c, b, m, lc.OpenCodeInitialPrompt); err != nil {
			return err
		}
		m, err = d.openCodeCurrentMeta(b)
		if err != nil {
			return err
		}
	}
	if initial := m.OpenCodeBinding.Initial; initial != nil {
		if requested := m.LaunchOptions["model"]; requested != "" && requested != initial.Model {
			return errors.New("OpenCode runtime: admitted initial model differs from requested selection")
		}
		if requested := m.LaunchOptions["agent"]; requested != "" && requested != initial.Agent {
			return errors.New("OpenCode runtime: admitted initial agent differs from requested selection")
		}
	}
	return d.releaseOpenCodeEmbargo(id, lc)
}

func (d *Daemon) admitOpenCodeInitial(ctx context.Context, c *opencodebridge.Client, b opencodebridge.Binding, m persist.Meta, text string) error {
	i := m.OpenCodeBinding.Initial
	hash := sha256.Sum256([]byte(text))
	if text == "" || hex.EncodeToString(hash[:]) != i.RequestHash {
		return errors.New("OpenCode runtime: initial input custody mismatch")
	}
	var receipt *opencodebridge.Receipt
	var err error
	if i.Phase == persist.OpenCodeInitialPrepared {
		if _, err := waitOpenCodeReady(ctx, c, b, true); err != nil {
			return err
		}
		next := *persist.CloneOpenCodeBinding(m.OpenCodeBinding)
		next.Revision++
		next.Initial.Phase = persist.OpenCodeInitialStarted
		if err := d.SetOpenCodeBinding(m.ID, m.OpenCodeBinding.Revision, next); err != nil {
			return err
		}
		m, err = d.openCodeCurrentMeta(b)
		if err != nil {
			return err
		}
		r, callErr := c.Initial(ctx, opencodebridge.InitialRequest{Version: 1, SessionInstance: b.SessionInstance, BackendGeneration: b.BackendGeneration, RootID: b.RootID, OperationID: i.OperationID, RequestHash: i.RequestHash, Text: text, ExpectedModel: m.LaunchOptions["model"], ExpectedAgent: m.LaunchOptions["agent"]})
		receipt, err = &r, callErr
	} else {
		receipt, err = c.InitialReceipt(ctx)
	}
	if err != nil || receipt == nil || receipt.Status != "admitted" || !sameOpenCodeIdentity(receipt.Identity, b) || receipt.OperationID != i.OperationID || receipt.RequestHash != i.RequestHash || receipt.Model == nil {
		if m.OpenCodeBinding.Initial.Phase != persist.OpenCodeInitialUncertain {
			next := *persist.CloneOpenCodeBinding(m.OpenCodeBinding)
			next.Revision++
			next.Initial.Phase = persist.OpenCodeInitialUncertain
			if saveErr := d.SetOpenCodeBinding(m.ID, m.OpenCodeBinding.Revision, next); saveErr != nil {
				return saveErr
			}
		}
		return errors.New("OpenCode runtime: initial admission uncertain; input remains held and no retry was sent")
	}
	next := *persist.CloneOpenCodeBinding(m.OpenCodeBinding)
	next.Revision++
	next.Initial.Phase = persist.OpenCodeInitialAdmitted
	next.Initial.MessageID = receipt.MessageID
	next.Initial.Agent, next.Initial.Model, next.Initial.Variant = receipt.Agent, receipt.Model.ProviderID+"/"+receipt.Model.ModelID, receipt.Variant
	if err := d.SetOpenCodeBinding(m.ID, m.OpenCodeBinding.Revision, next); err != nil {
		return err
	}
	return nil
}

func (d *Daemon) releaseOpenCodeEmbargo(id string, lc shimSpawnConfig) error {
	m, ok := d.Get(id)
	if !ok || m.Status.Process != status.ProcessRunning || m.OpenCodeBinding == nil || m.OpenCodeBinding.Generation != lc.OpenCode.Generation || m.OpenCodeBinding.Phase != persist.OpenCodePhaseReady || m.OpenCodeBinding.Initial != nil && m.OpenCodeBinding.Initial.Phase != persist.OpenCodeInitialAdmitted {
		return errors.New("OpenCode runtime: input release identity mismatch")
	}
	conn, caps, err := dialShimHello(lc.SocketPath)
	if err != nil {
		return errors.New("OpenCode runtime: input release channel unavailable")
	}
	defer func() { _ = conn.Close() }()
	if !caps.OpenCodeInputEmbargo {
		return errors.New("OpenCode runtime: shim input fence unavailable")
	}
	_ = conn.SetDeadline(time.Now().Add(helloIO))
	data, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeOpenCodeEmbargoRelease, IncidentID: lc.OpenCode.Generation, Token: lc.OpenCode.EmbargoToken})
	if err != nil || wire.WriteFrame(conn, wire.TControl, data) != nil {
		return errors.New("OpenCode runtime: input release unconfirmed")
	}
	kind, raw, err := wire.ReadFrame(conn)
	if err != nil {
		return errors.New("OpenCode runtime: input release unconfirmed")
	}
	result, err := shimwire.Decode(raw)
	if err != nil || kind != wire.TControl || result.Type != shimwire.TypeOpenCodeEmbargoResult || result.Refused != "" {
		return errors.New("OpenCode runtime: input release refused")
	}
	return nil
}

func (d *Daemon) openCodeRuntimeError(id string, err error) {
	d.writeMu.Lock()
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	m, ok := d.Get(id)
	written := false
	if !closed && ok && m.RequiredRuntime == persist.RequiredRuntimeOpenCode {
		m.BackendPlanError = ""
		if err != nil {
			m.BackendPlanError = "OpenCode native binding or initial admission is unconfirmed; retain the recorded root and do not resend initial input"
		}
		written, _ = d.saveMetaLocked(&m)
	}
	d.writeMu.Unlock()
	if written && d.cfg.onMetaSave != nil {
		d.cfg.onMetaSave(m)
	}
}

func (d *Daemon) rejoinOpenCode(id string) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), openCodeJoinTimeout)
		defer cancel()
		err := d.joinOpenCode(ctx, id)
		d.openCodeRuntimeError(id, err)
	}()
}

func openCodeLaunchComplete(m persist.Meta) bool {
	if m.RequiredRuntime != persist.RequiredRuntimeOpenCode {
		return true
	}
	b := m.OpenCodeBinding
	return b != nil && b.Phase == persist.OpenCodePhaseReady && (b.Initial == nil || b.Initial.Phase == persist.OpenCodeInitialAdmitted) && m.BackendPlanError == ""
}
