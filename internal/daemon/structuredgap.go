package daemon

// STRUCTURED_GAP: the daemon-authored capability-degrade event of ADR-017 T2 rule 2 /
// playbook §6.1. An unrecoverable shim/daemon spool or cursor gap "emits an exact
// structured_gap boundary, disables structured_chat for that session instance, and
// forbids a fabricated completion."
//
// This slice defines the event SHAPE and the emission SEAM. Emission itself is a STUB:
// the spool-boundary detection (ADR-010's spool) that would trigger it does not exist yet,
// so EmitStructuredGap returns ErrStructuredGapUnimplemented and appends nothing --
// exactly the fabricated-completion rule applied to the seam itself, better silence than a
// false event.
//
// CARRIER: StructuredGapEvent rides the existing journal.Record family under
// journal.TypeStructuredGap, mirroring how InteractionItem rides journal.TypeInteraction
// (interaction.go). Session id and cursor are deliberately absent from the event body for
// the same reason InteractionItem omits them: the enclosing journal.Record carries both.

import (
	"errors"
	"time"
)

// StructuredGapEvent is the structured_gap record's payload.
type StructuredGapEvent struct {
	TS     time.Time `json:"ts"`
	Reason string    `json:"reason"`
}

// ErrStructuredGapUnimplemented is returned by EmitStructuredGap until spool-boundary
// detection lands.
var ErrStructuredGapUnimplemented = errors.New("daemon: structured_gap emission is not yet implemented")

// EmitStructuredGap will emit a structured_gap boundary for sessionID once spool-boundary
// detection lands. Until then it is a stub: it appends nothing to the journal and returns
// ErrStructuredGapUnimplemented.
func (d *Daemon) EmitStructuredGap(sessionID, reason string) error {
	return ErrStructuredGapUnimplemented
}
