package skeleton

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/persist"
)

// Captured from immutable local metadata before any destructive DRAIN request.
type claudeControlBinding struct {
	meta       persist.Meta
	failed     bool
	descriptor *claudeinput.Descriptor
}

// Reuses the existing drain cadence; a stored heartbeat never renews this lease.
func (hd *HookDrainer) refreshClaudeLease() {
	if hd.claudeControl == nil {
		return
	}
	hd.drainMu.Lock()
	if hd.claudeControl == nil {
		hd.drainMu.Unlock()
		return
	}
	meta := hd.claudeControl.meta
	hd.drainMu.Unlock()
	if hd.d.eng == nil {
		return
	}
	view := claudeinput.ObservationLease{Revoked: true}
	started := time.Now()
	if desc, err := claudeinput.ReadDescriptor(filepath.Join(hd.d.stateDir, meta.ID)); err == nil && desc.RequiredRuntime == claudeinput.NativeControlRuntime && desc.SessionID == meta.ID && desc.ShimPID == meta.ShimPID && desc.ShimStartTime == meta.ShimStartTime {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		current, err := claudeinput.NewClient(desc).Observation(ctx)
		cancel()
		if err == nil {
			view = current
		}
	}
	_ = hd.d.eng.SetClaudeObservationLease(meta.ID, view, time.Since(started))
}

func (hd *HookDrainer) bindClaudeControl() {
	if hd.d.core == nil {
		return
	}
	meta, ok := hd.d.core.Get(hd.sessionID)
	if ok && meta.RequiredRuntime == persist.RequiredRuntimeClaudeNativeControl {
		if meta.LoginRuntime != nil {
			value := *meta.LoginRuntime
			meta.LoginRuntime = &value
		}
		if meta.CLIIdentity != nil {
			value := *meta.CLIIdentity
			meta.CLIIdentity = &value
		}
		hd.claudeControl = &claudeControlBinding{meta: meta}
		if desc, err := claudeinput.ReadDescriptor(filepath.Join(hd.d.stateDir, meta.ID)); err == nil {
			hd.claudeControl.descriptor = &desc
		}
	}
}
func (hd *HookDrainer) checkClaudeControlBinding() error {
	if hd.d.core == nil {
		if hd.claudeControl != nil {
			return &engine.ExecutionCustodyError{Err: errors.New("claude control metadata unavailable")}
		}
		return nil
	}
	current, exists := hd.d.core.Get(hd.sessionID)
	bound := hd.claudeControl
	if bound == nil {
		if exists && current.RequiredRuntime == persist.RequiredRuntimeClaudeNativeControl {
			return &engine.ExecutionCustodyError{Err: errors.New("claude control drainer was not bound")}
		}
		return nil
	}
	original := bound.meta
	if bound.failed || !exists || current.ID != original.ID || current.RequiredRuntime != original.RequiredRuntime || current.SchemaVersion != 5 || current.AgentType != "claude" || current.ShimPID != original.ShimPID || current.ShimStartTime != original.ShimStartTime || current.LoginRuntime == nil || original.LoginRuntime == nil || *current.LoginRuntime != *original.LoginRuntime || current.CLIIdentity == nil || original.CLIIdentity == nil || *current.CLIIdentity != *original.CLIIdentity || current.Cwd != original.Cwd || original.ConversationID != "" && current.ConversationID != original.ConversationID {
		return hd.failClaudeControl(errors.New("claude control drainer binding lost"))
	}
	if original.ConversationID == "" && current.ConversationID != "" {
		bound.meta.ConversationID = current.ConversationID
	}
	desc, err := claudeinput.ReadDescriptor(filepath.Join(hd.d.stateDir, current.ID))
	if err != nil || desc.RequiredRuntime != claudeinput.NativeControlRuntime || desc.SessionID != current.ID || desc.ShimPID != current.ShimPID || desc.ShimStartTime != current.ShimStartTime || bound.descriptor != nil && *bound.descriptor != desc {
		return hd.failClaudeControl(errors.New("claude control descriptor binding lost"))
	}
	if bound.descriptor == nil {
		bound.descriptor = &desc
	}
	return nil
}
func (hd *HookDrainer) failClaudeControl(err error) error {
	if hd.claudeControl != nil {
		hd.claudeControl.failed = true
		if hd.d.eng != nil {
			err = errors.Join(err, hd.d.eng.InvalidateExecution(hd.sessionID))
		}
	}
	return &engine.ExecutionCustodyError{Err: err}
}
func (hd *HookDrainer) ingestBoundHook(raw []byte) error {
	if err := hd.checkClaudeControlBinding(); err != nil {
		return err
	}
	if hd.claudeControl == nil {
		return hd.d.ingestHookBytes(raw)
	}
	if envelope, err := claudeinput.DecodeControlEnvelope(raw); err == nil {
		meta := hd.claudeControl.meta
		if envelope.SessionID != meta.ID || envelope.ShimPID != meta.ShimPID || envelope.ShimStartTime != meta.ShimStartTime || meta.ConversationID != "" && envelope.Worker.NativeSessionID != meta.ConversationID {
			return hd.failClaudeControl(errors.New("claude control envelope binding mismatch"))
		}
		desc, descErr := claudeinput.ReadDescriptor(filepath.Join(hd.d.stateDir, meta.ID))
		if descErr != nil || desc.RequiredRuntime != claudeinput.NativeControlRuntime || desc.SessionID != meta.ID || desc.Generation != envelope.DescriptorGeneration || desc.ShimPID != meta.ShimPID || desc.ShimStartTime != meta.ShimStartTime || hd.d.eng == nil || hd.d.executionStore == nil {
			return hd.failClaudeControl(errors.New("claude control descriptor/store mismatch"))
		}
		if err := hd.d.eng.AuthenticateClaudeControl(envelope); err != nil {
			return hd.failClaudeControl(err)
		}
		if meta.ConversationID == "" {
			if !adapter.IsCanonicalConversationID(envelope.Worker.NativeSessionID) {
				return hd.failClaudeControl(errors.New("claude control native session is not canonical"))
			}
			if err := hd.d.persistConversationIdentity(meta.ID, envelope.Worker.NativeSessionID); err != nil {
				return &engine.ExecutionCustodyError{Err: err}
			}
			root, err := openRecoveryRoot(hd.d.stateDir)
			if err != nil {
				return &engine.ExecutionCustodyError{Err: err}
			}
			defer func() { _ = root.Close() }()
			directory, err := openRecoveryDir(root, meta.ID)
			if err != nil {
				return &engine.ExecutionCustodyError{Err: err}
			}
			defer func() { _ = directory.Close() }()
			if err := syncRecoveryDir(directory); err != nil {
				return &engine.ExecutionCustodyError{Err: err}
			}
			if err := hd.checkClaudeControlBinding(); err != nil {
				return err
			}
		}
		return hd.d.eng.ApplyClaudeControlEnvelope(meta.ID, envelope)
	}
	if err := claudeinput.ValidateControlOrdinaryHook(raw); err != nil {
		return hd.failClaudeControl(err)
	}
	cb, err := hookclient.Decode(bytes.NewReader(raw))
	if err != nil || cb.SessionID != hd.sessionID || hd.d.eng == nil {
		return hd.failClaudeControl(errors.New("claude ordinary callback binding mismatch"))
	}
	if hd.d.executionStore == nil {
		return hd.failClaudeControl(errors.New("claude ordinary execution store unavailable"))
	}
	snapshot, snapshotErr := hd.d.eng.ExecutionSnapshot(cb.SessionID)
	if snapshotErr != nil || snapshot.Claude == nil || snapshot.Incarnation != executionIncarnation(hd.claudeControl.meta) {
		return hd.failClaudeControl(errors.New("claude ordinary execution record unavailable"))
	}
	if err := hd.d.eng.AuthenticateCallback(cb); err != nil {
		return hd.failClaudeControl(err)
	}
	if hd.d.hookSeqDuplicate(cb.SessionID, cb.Sequence) {
		err := hd.d.eng.AcknowledgeExecutionCallback(hd.d.statusHookCallback(cb))
		var custody *engine.ExecutionCustodyError
		if err != nil && !errors.As(err, &custody) {
			return hd.failClaudeControl(err)
		}
		return err
	}
	if err := hd.d.ingestOrdinaryHookBytes(raw); err != nil {
		var custody *engine.ExecutionCustodyError
		if errors.As(err, &custody) {
			return err
		}
		return hd.failClaudeControl(fmt.Errorf("claude ordinary callback: %w", err))
	}
	return nil
}
