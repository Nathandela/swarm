package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

var ErrControllerJournalResync = errors.New("voice: daemon journal reconnect and atomic snapshot required")

type controllerJournalSource interface {
	EndpointID() string
	Capabilities() []string
	JournalSubscribeFrom(context.Context, uint64) (protocol.JournalResume, <-chan protocol.JournalRecord, error)
}

var controllerJournalCaps = []string{protocol.CapJournal, protocol.CapJournalSubscribeFrom, protocol.CapJournalSourceIdentity}

// FollowDaemonJournal owns a dedicated source-aware connection until EOF/error.
// The service reconnects after errors; device disconnect never owns this lifetime.
func (c *Controller) FollowDaemonJournal(ctx context.Context, socket string) error {
	client, err := protocol.DialContext(ctx, socket, controllerJournalCaps)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	return c.consumeDaemonJournal(ctx, client)
}
func (c *Controller) consumeDaemonJournal(ctx context.Context, source controllerJournalSource) error {
	endpoint, _, ok := protocol.ParseID(c.binding.SessionID)
	if !ok || source.EndpointID() != endpoint {
		return ErrStale
	}
	for _, capability := range controllerJournalCaps {
		if !slices.Contains(source.Capabilities(), capability) {
			return ErrControllerUnsupported
		}
	}
	st, err := c.store.Snapshot()
	if err != nil {
		return err
	}
	if err = checkBinding(&st, c.binding); err != nil {
		return err
	}
	previous := st.Journal[c.binding.ConversationID]
	resume, live, err := source.JournalSubscribeFrom(ctx, previous.Sequence)
	if err != nil {
		return err
	}
	if !protocol.ValidJournalSourceID(resume.SourceID) {
		return ErrCorrupt
	}
	recovery := previous.Binding == "" || previous.Binding != resume.SourceID || resume.FullResync || resume.HistoryGap
	examined := previous.Sequence
	if !recovery {
		for _, record := range resume.Events {
			if record.Cursor <= previous.Sequence {
				continue
			}
			if record.Cursor != examined+1 {
				recovery = true
				break
			}
			if _, err := c.qualifiedJournalRecord(st, endpoint, record); err != nil {
				recovery = true
				break
			}
			examined = record.Cursor
		}
		if examined != resume.Cursor {
			recovery = true
		}
	}
	examined = previous.Sequence
	if recovery {
		metadata, _ := json.Marshal(struct {
			HistoryAvailable bool   `json:"history_available"`
			Reason           string `json:"reason"`
			Tracked          int    `json:"tracked_workers"`
		}{false, "initial_gap_or_namespace_recovery", len(st.Workers)})
		cursor := JournalCursor{Binding: resume.SourceID, Sequence: resume.Cursor}
		snapshot := JournalSnapshot{Previous: previous, Metadata: metadata}
		owned := false
		for _, worker := range st.Workers {
			if worker.ConversationID == c.binding.ConversationID {
				owned = true
				break
			}
		}
		for _, input := range st.Admissions {
			if input.ConversationID == c.binding.ConversationID && input.SettledAt.IsZero() {
				owned = true
				break
			}
		}
		if owned {
			_, err = c.AcceptRecoverySnapshot(cursor, snapshot)
		} else {
			err = c.store.checkpointControllerRecovery(c.binding, cursor, snapshot)
		}
		if err != nil {
			return err
		}
		examined = resume.Cursor
	} else {
		for _, record := range resume.Events {
			if record.Cursor <= examined {
				continue
			}
			if err = c.consumeJournalRecord(endpoint, resume.SourceID, examined, record); err != nil {
				return err
			}
			examined = record.Cursor
		}
	}
	// Irrelevant transcript positions are coalesced, never persisted once per token.
	flush := func() error { return c.checkpointExaminedJournal(resume.SourceID, examined) }
	if err = flush(); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err = flush(); err != nil {
				return err
			}
		case record, open := <-live:
			if !open {
				if err = flush(); err != nil {
					return err
				}
				return ErrControllerJournalResync
			}
			if record.Cursor <= examined {
				continue
			}
			if record.Cursor != examined+1 {
				return ErrControllerJournalResync
			}
			if err = c.consumeJournalRecord(endpoint, resume.SourceID, examined, record); err != nil {
				return err
			}
			examined = record.Cursor
		}
	}
}
func (c *Controller) qualifiedJournalRecord(st State, endpoint string, record protocol.JournalRecord) (bool, error) {
	relevant, _ := journalWorkerSignal(record)
	if !relevant {
		return false, nil
	}
	session, err := journalSession(endpoint, record.SessionID)
	if err != nil {
		return false, err
	}
	if session == c.binding.SessionID || record.Role == "manager" {
		return false, nil
	}
	trackedSession := false
	exact := false
	for _, worker := range st.Workers {
		if worker.ConversationID == c.binding.ConversationID && !worker.Reserved && worker.Session == session {
			trackedSession = true
			if worker.SessionInstance == record.SessionInstance {
				exact = true
			}
		}
	}
	if !trackedSession {
		return false, nil
	}
	if record.SessionInstance == "" || !exact {
		return false, ErrControllerJournalResync
	}
	return true, nil
}
func (c *Controller) consumeJournalRecord(endpoint, sourceID string, examined uint64, record protocol.JournalRecord) error {
	relevantSignal, _ := journalWorkerSignal(record)
	if !relevantSignal {
		return nil
	}
	normalized, err := journalSession(endpoint, record.SessionID)
	if err != nil {
		return err
	}
	if normalized == c.binding.SessionID || record.Role == "manager" {
		return nil
	}
	st, err := c.store.Snapshot()
	if err != nil {
		return err
	}
	relevant, err := c.qualifiedJournalRecord(st, endpoint, record)
	if err != nil {
		return err
	}
	if !relevant {
		return nil
	}
	session, err := journalSession(endpoint, record.SessionID)
	if err != nil {
		return err
	}
	_, recovery := journalWorkerSignal(record)
	var item struct {
		ItemID, Kind, Status, TurnID string
		Provenance                   string
	}
	if len(record.Item) > 0 {
		var fields struct {
			ItemID     string `json:"item_id"`
			Kind       string `json:"kind"`
			Status     string `json:"status"`
			TurnID     string `json:"turn_id"`
			Provenance string `json:"provenance"`
		}
		if json.Unmarshal(record.Item, &fields) != nil {
			return ErrCorrupt
		}
		item.ItemID, item.Kind, item.Status, item.TurnID, item.Provenance = fields.ItemID, fields.Kind, fields.Status, fields.TurnID, fields.Provenance
	}
	metadata, _ := json.Marshal(struct {
		Type   string       `json:"type"`
		Group  status.Group `json:"group,omitempty"`
		ItemID string       `json:"item_id,omitempty"`
		Kind   string       `json:"kind,omitempty"`
		Status string       `json:"status,omitempty"`
		Turn   string       `json:"native_turn_id,omitempty"`
	}{record.Type, record.Group, item.ItemID, item.Kind, item.Status, item.TurnID})
	id := "journal-" + hashValue(struct {
		Source string
		Cursor uint64
	}{sourceID, record.Cursor})
	event := Event{ID: id, Cursor: JournalCursor{Binding: sourceID, Sequence: record.Cursor}, Metadata: metadata, RecoveryOnly: recovery}
	if record.Cursor > 0 {
		event.ExaminedThrough = JournalCursor{Binding: sourceID, Sequence: examined}
	}
	_, err = c.AcceptWorkerEvent(session, record.SessionInstance, event)
	return err
}
func journalSession(endpoint, session string) (string, error) {
	if owner, _, ok := protocol.ParseID(session); ok {
		if owner != endpoint {
			return "", ErrStale
		}
		return session, nil
	}
	if session == "" || strings.Contains(session, "/") {
		return "", ErrCorrupt
	}
	return protocol.NamespacedID(endpoint, session), nil
}
func journalWorkerSignal(record protocol.JournalRecord) (bool, bool) {
	switch record.Type {
	case "lost", "deleted", "exited", "structured_gap":
		return true, true
	case "group_transition", "session_state":
		return record.Group == status.GroupNeedsInput || record.Group == status.GroupReadyForReview || record.Group == status.GroupCompleted, false
	case "interaction":
		var item struct{ Kind, Status, Provenance string }
		if json.Unmarshal(record.Item, &item) != nil {
			return false, false
		}
		if item.Provenance == "imported" || item.Provenance == "reconciled" {
			return false, false
		}
		switch item.Kind {
		case "agent_message", "message_snapshot":
			return item.Status == "completed" || item.Status == "failed" || item.Status == "declined", false
		case "approval_request", "permission_request", "question_request":
			return item.Status != "completed" && item.Status != "declined", false
		}
	}
	return false, false
}
func (c *Controller) checkpointExaminedJournal(source string, through uint64) error {
	c.scheduler.Lock()
	defer c.scheduler.Unlock()
	st, err := c.store.Snapshot()
	if err != nil {
		return err
	}
	previous := st.Journal[c.binding.ConversationID]
	if previous.Binding != "" && previous.Binding != source {
		return ErrStale
	}
	if previous.Sequence >= through {
		return nil
	}
	metadata := st.JournalSnapshots[c.binding.ConversationID].Metadata
	var fields map[string]json.RawMessage
	if len(metadata) > 0 && json.Unmarshal(metadata, &fields) != nil {
		return ErrCorrupt
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	fields["examined_through"] = json.RawMessage(strconv.FormatUint(through, 10))
	metadata, err = json.Marshal(fields)
	if err != nil {
		return err
	}
	if len(metadata) > MaxControlFrameBytes {
		return fmt.Errorf("%w: source checkpoint metadata", ErrCapacity)
	}
	return c.store.ReconcileJournal(c.binding, JournalCursor{Binding: source, Sequence: through}, JournalSnapshot{Previous: previous, Metadata: metadata})
}
