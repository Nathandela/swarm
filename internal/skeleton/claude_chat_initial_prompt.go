package skeleton

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/shimwire"
)

// Only the winning live launch invokes this; hydration never submits owner input.
func (d *Daemon) bootstrapClaudeNativeLaunch(m persist.Meta, _ string, text string) error {
	operation := "claude-initial:" + m.ID
	var req protocol.ComposerSendReq
	if text != "" {
		var fresh bool
		var err error
		req, fresh, err = d.claimClaudeNativeInitial(m, operation, text)
		if err != nil || !fresh {
			return err
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for {
		last = d.registerClaudeNativeChat(m)
		if last == nil {
			d.startHookDrain(m.ID)
			s := d.claudeNativeState(m.ID)
			s.mu.Lock()
			hd := s.drainer
			s.mu.Unlock()
			if hd == nil {
				last = errClaudeFixturePending
			} else {
				_, _, last = hd.DrainOnce()
				if last == nil {
					last = d.proveClaudeNativeReady(m.ID, s)
				}
				if last == nil && text == "" {
					_, last = d.readClaudeNativeHistory(s)
				}
			}
		}
		if last == nil {
			break
		}
		if time.Now().After(deadline) {
			last = fmt.Errorf("native Claude chat started; initial message remains unsent: %w", last)
			if text != "" {
				d.rejectClaudeNativeInitial(operation, last)
			}
			return last
		}
		select {
		case <-d.closing:
			if text != "" {
				d.rejectClaudeNativeInitial(operation, errors.New("daemon stopped before initial dispatch"))
			}
			return errors.New("daemon stopped before native chat readiness")
		case <-time.After(25 * time.Millisecond):
		}
	}
	d.authorSessionCapabilitiesWhenDecided(m.ID, m.AgentType, m.ShimPID, m.ShimStartTime)
	if text == "" {
		return nil
	}
	return d.dispatchClaudeNativeInitial(m, operation, req)
}

func (d *Daemon) sendClaudeNativeInitialPrompt(m persist.Meta, operation, text string) error {
	req, fresh, err := d.claimClaudeNativeInitial(m, operation, text)
	if err != nil || !fresh {
		return err
	}
	return d.dispatchClaudeNativeInitial(m, operation, req)
}

func (d *Daemon) claimClaudeNativeInitial(m persist.Meta, operation, text string) (protocol.ComposerSendReq, bool, error) {
	var req protocol.ComposerSendReq
	if operation == "" || len(operation) > 128 || text == "" || !utf8.ValidString(text) || len(text) > shimwire.MaxClaudeFixtureTextBytes || m.SchemaVersion != 6 || m.RequiredRuntime != persist.RequiredRuntimeClaudeChat || m.AgentType != "claude" || m.LoginRuntime == nil || m.CLIIdentity == nil {
		return req, false, errClaudeFixtureMessage
	}
	current, ok := d.core.Get(m.ID)
	if !ok || current.ShimPID != m.ShimPID || current.ShimStartTime != m.ShimStartTime || current.ConversationID != m.ConversationID || current.LoginRuntime == nil || current.CLIIdentity == nil || *current.LoginRuntime != *m.LoginRuntime || *current.CLIIdentity != *m.CLIIdentity || current.ProviderCwd() != m.ProviderCwd() {
		return req, false, errClaudeFixtureMessage
	}
	instance, ok := d.sessionInstance(m.ID)
	if !ok {
		return req, false, errClaudeFixtureMessage
	}
	req = protocol.ComposerSendReq{Session: protocol.NamespacedID(d.machineID(), m.ID), SessionInstance: instance, Text: text}
	hash := hex.EncodeToString(schema.ComposerSendContentHash(&req))
	lane := d.composerLaneFor(m.ID)
	lane.claudeAdmitMu.Lock()
	defer lane.claudeAdmitMu.Unlock()
	if old, exists := d.core.ComposerOperation(operation); exists {
		if old.Action != "semantic_send" || old.SessionID != m.ID || old.SessionInstance != instance || old.RequestHash != hash || old.Phase != idempotency.PhaseCompleted {
			return req, false, errClaudeFixtureMessage
		}
		return req, false, nil
	}
	phase, _, err := d.core.ClaimComposerOperation(operation, "semantic_send", m.ID, instance, hash)
	if err != nil {
		return req, false, err
	}
	if phase != string(idempotency.PhasePrepared) {
		return req, false, errClaudeFixtureMessage
	}
	return req, true, nil
}

func (d *Daemon) dispatchClaudeNativeInitial(m persist.Meta, operation string, req protocol.ComposerSendReq) error {
	retained, present := d.claudeNativeStops.Load(m.ID)
	if !present {
		d.rejectClaudeNativeInitial(operation, errClaudeFixtureMessage)
		return errClaudeFixtureMessage
	}
	handle := retained.(claudeNativeStopHandle)
	if handle.binding.SessionID != m.ID || handle.binding.ShimPID != m.ShimPID || handle.binding.ShimStartTime != m.ShimStartTime || handle.binding.Runtime != *m.LoginRuntime || handle.binding.CLIIdentity != *m.CLIIdentity || handle.binding.Cwd != m.ProviderCwd() || handle.binding.NativeSessionID != m.ConversationID || handle.binding.ScopeGeneration == "" {
		d.rejectClaudeNativeInitial(operation, errClaudeFixtureMessage)
		return errClaudeFixtureMessage
	}
	_, err := d.composerSendFrom(d.machineID(), operation, req, func() error { return d.core.BeginComposerOperation(operation) }, adapter.SourceOwner)
	if err != nil {
		if !errors.Is(err, protocol.ErrComposerOutcomeUnknown) {
			d.rejectClaudeNativeInitial(operation, err)
		}
		return err
	}
	outcome, _ := json.Marshal(struct {
		OK bool `json:"ok"`
	}{true})
	return d.core.CommitComposerOperation(operation, outcome, true)
}

func (d *Daemon) rejectClaudeNativeInitial(operation string, err error) {
	rec, ok := d.core.ComposerOperation(operation)
	if !ok || rec.Phase != idempotency.PhasePrepared {
		return
	}
	outcome, _ := json.Marshal(struct {
		OK      bool               `json:"ok"`
		Code    protocol.ErrorCode `json:"code"`
		Message string             `json:"message"`
	}{false, protocol.CodeInputBusy, "Initial message remains unsent; edit the draft and send explicitly when ready: " + err.Error()})
	_ = d.core.CommitComposerOperation(operation, outcome, false)
}
