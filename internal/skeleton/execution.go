package skeleton

import (
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func executionIncarnation(meta persist.Meta) string {
	return fmt.Sprintf("%d:%d", meta.ShimPID, meta.ShimStartTime)
}

func (d *Daemon) registerExecutionSession(meta persist.Meta, token string, sources []adapter.SignalSource, fromStart bool) {
	if d.executionStore == nil && meta.RequiredRuntime == persist.RequiredRuntimeClaudeNativeControl {
		_ = d.eng.RegisterExecutionSession(meta.ID, token, meta.ShimPID, sources, meta.Status, engine.ExecutionIdentity{Incarnation: executionIncarnation(meta), ConversationID: meta.ConversationID, ClaudeNativeControl: true}, nil)
		return
	}
	if d.executionStore == nil || (!managedLoginProvider(meta.AgentType) && (!fromStart || !freshClaudeFixture(meta))) {
		d.eng.RegisterSession(meta.ID, token, meta.ShimPID, sources, meta.Status)
		return
	}
	var saved *persist.ExecutionRecord
	record, err := d.executionStore.LoadExecution(meta.ID)
	if err == nil {
		saved = &record
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fromStart = false
	}
	// Only a launch observed from birth may start without unknown child custody.
	identity := engine.ExecutionIdentity{Incarnation: executionIncarnation(meta), ConversationID: meta.ConversationID, ObservedFromStart: fromStart && meta.ShimPID > 0 && meta.ShimStartTime > 0, ClaudeNativeControl: meta.RequiredRuntime == persist.RequiredRuntimeClaudeNativeControl}
	// Restore the monotonic input fence only for this exact persisted incarnation.
	// Restored native evidence still requires live reconciliation in the engine.
	if saved != nil && saved.Validate() == nil && saved.SessionID == meta.ID && saved.Incarnation == identity.Incarnation && saved.ConversationID == identity.ConversationID {
		identity.OwnerRevision = saved.OwnerRevision
	}
	if fromStart && freshClaudeFixture(meta) {
		identity.ObservedFromStart = false
		saved = &persist.ExecutionRecord{Version: persist.ExecutionVersion, SessionID: meta.ID, Incarnation: identity.Incarnation, ConversationID: meta.ConversationID, OwnerRevision: identity.OwnerRevision, UnknownActors: true, Incomplete: "dev_fake_chat_subset", MainTurn: meta.Status.Turn, Interaction: meta.Status.Interaction}
	}
	if err := d.eng.RegisterExecutionSession(meta.ID, token, meta.ShimPID, sources, meta.Status, identity, saved); err != nil {
		log.Printf("skeleton: execution evidence requires reconciliation for session %s", meta.ID)
	}
}

func (d *Daemon) bindExecutionConversation(meta persist.Meta) error {
	if d.executionStore == nil || d.eng == nil || !managedLoginProvider(meta.AgentType) || meta.ConversationID == "" {
		return nil
	}
	if err := d.eng.BindExecutionConversation(meta.ID, executionIncarnation(meta), meta.ConversationID); err != nil {
		return &engine.ExecutionCustodyError{Err: err}
	}
	return nil
}

func (d *Daemon) persistConversationIdentity(local, conversation string) error {
	if err := d.core.SetConversationID(local, conversation); err != nil {
		return err
	}
	meta, ok := d.core.Get(local)
	if !ok || meta.ConversationID != conversation {
		return fmt.Errorf("native conversation identity unavailable")
	}
	return d.bindExecutionConversation(meta)
}

// observeExecutionOwnerWrite runs before raw PTY or native API input effects.
// Other adapters retain their existing input policy.
func (d *Daemon) observeExecutionOwnerWrite(local string) error {
	if d.eng == nil || d.executionStore == nil || d.core == nil {
		return nil
	}
	meta, ok := d.core.Get(local)
	if !ok {
		return fmt.Errorf("execution owner input: session disappeared")
	}
	if meta.AgentType != "codex" {
		return nil
	}
	return d.eng.ObserveExecutionOwnerInput(local)
}

func (d *Daemon) executionCustodyUnsafe(local string) bool {
	if d.core == nil {
		return false
	}
	meta, ok := d.core.Get(local)
	// This observation subset never authorizes automatic session effects, even
	// when execution storage is absent or an old status writer reports Idle.
	if ok && (meta.RequiredRuntime == persist.RequiredRuntimeClaudeNativeControl || meta.RequiredRuntime == persist.RequiredRuntimeClaudeChat) {
		return true
	}
	if d.eng == nil || d.executionStore == nil || !ok || meta.AgentType != "codex" {
		return false
	}
	return d.eng.ExecutionCustodyUnsafe(local)
}

// publishExecutionStatusesForRunning closes the reconcile-to-roster gap after
// the assembly's status observers exist. It runs before ingress and grid polls;
// registration itself may run under the core lifecycle lock and cannot emit.
func (d *Daemon) publishExecutionStatusesForRunning() {
	if d.core == nil || d.eng == nil {
		return
	}
	for _, meta := range d.core.List() {
		if meta.AgentType == "codex" && meta.Status.Process == status.ProcessRunning {
			d.eng.PublishCodexExecutionStatus(meta.ID)
		} else if meta.RequiredRuntime == persist.RequiredRuntimeClaudeNativeControl && meta.Status.Process == status.ProcessRunning {
			d.eng.PublishClaudeExecutionStatus(meta.ID)
		}
	}
}
