package daemon

// STRUCTURED_GAP: the daemon-authored capability-degrade event of ADR-017 T2 rule 2 /
// playbook §6.1. An unrecoverable shim/daemon spool or cursor gap "emits an exact
// structured_gap boundary, disables structured_chat for that session instance, and
// forbids a fabricated completion."
//
// CARRIER: StructuredGapEvent rides the existing journal.Record family under
// journal.TypeStructuredGap, mirroring how InteractionItem rides journal.TypeInteraction
// (interaction.go). Session id and cursor are deliberately absent from the event body for
// the same reason InteractionItem omits them: the enclosing journal.Record carries both.
//
// internal/daemon cannot import internal/protocol (protocol already imports daemon), so
// the session-capability DEGRADE half of playbook 6.1 ("disables structured_chat for that
// session instance") is skeleton's obligation (internal/skeleton/hookdrain.go), which holds
// the only type (protocol.SessionCapabilities) that rule can be expressed against. This
// package's job ends at "the journal gains an honest structured_gap record" -- the caller
// proves the boundary (internal/shim's HookSpool + internal/skeleton's HookDrainer); this
// seam never fabricates one on its own.

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
)

// StructuredGapEvent is the structured_gap record's payload.
type StructuredGapEvent struct {
	TS              time.Time `json:"ts"`
	Reason          string    `json:"reason"`
	SessionInstance string    `json:"session_instance,omitempty"`
	DedupeKey       string    `json:"dedupe_key,omitempty"`
}

// EmitStructuredGap durably appends a structured_gap journal record for sessionID.
// Every call appends its own record -- emission is never coalesced or deduplicated,
// because each call names a distinct proven boundary.
func (d *Daemon) EmitStructuredGap(sessionID, reason string) error {
	return d.appendStructuredGap(StructuredGapEvent{TS: time.Now(), Reason: reason}, sessionID)
}

// EmitStructuredGapOnce appends one durable boundary for dedupeKey. The key is
// authored by the proven-gap caller from the session instance, spool incarnation,
// and numeric boundary. A daemon crash after the journal fsync but before the
// caller's checkpoint therefore replays to this record instead of appending a
// duplicate. An empty key deliberately retains EmitStructuredGap's append-every-call
// behavior for callers that do not have a stable source identity.
func (d *Daemon) EmitStructuredGapOnce(sessionID, sessionInstance, reason, dedupeKey string) error {
	if dedupeKey == "" {
		return d.EmitStructuredGap(sessionID, reason)
	}
	d.gapEmitMu.Lock()
	defer d.gapEmitMu.Unlock()

	res, err := d.journal.ReadFrom(0)
	if err != nil {
		return fmt.Errorf("daemon: read structured_gap dedupe journal: %w", err)
	}
	for _, rec := range res.Events {
		if rec.Type != journal.TypeStructuredGap || rec.SessionID != sessionID {
			continue
		}
		var prior StructuredGapEvent
		if json.Unmarshal(rec.Payload, &prior) == nil && prior.DedupeKey == dedupeKey {
			return nil
		}
	}
	return d.appendStructuredGap(StructuredGapEvent{
		TS: time.Now(), Reason: reason,
		SessionInstance: sessionInstance, DedupeKey: dedupeKey,
	}, sessionID)
}

func (d *Daemon) appendStructuredGap(event StructuredGapEvent, sessionID string) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("daemon: marshal structured_gap event: %w", err)
	}
	_, err = d.journal.Append(journal.Record{
		SessionID: sessionID,
		Type:      journal.TypeStructuredGap,
		Payload:   payload,
	})
	return err
}

// EmitCapabilityTransition durably publishes one complete, already-validated
// session-capability record. The skeleton owns the schema and the state transition;
// daemon owns only ordered journal append durability.
func (d *Daemon) EmitCapabilityTransition(sessionID string, payload []byte) error {
	if sessionID == "" || len(payload) == 0 {
		return fmt.Errorf("daemon: capability transition requires session and payload")
	}
	_, err := d.journal.Append(journal.Record{
		SessionID: sessionID,
		Type:      journal.TypeCapabilityTransition,
		Payload:   append([]byte(nil), payload...),
	})
	return err
}
