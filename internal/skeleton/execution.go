package skeleton

import (
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
)

func executionIncarnation(meta persist.Meta) string {
	return fmt.Sprintf("%d:%d", meta.ShimPID, meta.ShimStartTime)
}

func (d *Daemon) registerExecutionSession(meta persist.Meta, token string, sources []adapter.SignalSource, fromStart bool) {
	if d.executionStore == nil || !managedLoginProvider(meta.AgentType) {
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
	identity := engine.ExecutionIdentity{Incarnation: executionIncarnation(meta), ConversationID: meta.ConversationID, ObservedFromStart: fromStart && meta.ShimPID > 0 && meta.ShimStartTime > 0}
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
