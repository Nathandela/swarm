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
	TS     time.Time `json:"ts"`
	Reason string    `json:"reason"`
}

// EmitStructuredGap durably appends a structured_gap journal record for sessionID.
// Every call appends its own record -- emission is never coalesced or deduplicated,
// because each call names a distinct proven boundary.
func (d *Daemon) EmitStructuredGap(sessionID, reason string) error {
	payload, err := json.Marshal(StructuredGapEvent{TS: time.Now(), Reason: reason})
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
