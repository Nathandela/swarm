package daemon

import (
	"encoding/json"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// JournalReadFrom reads the daemon-wide durable journal from the given cursor
// (R-JRN.4; also backs the remote journal_read op, R-PROT.3). The Resume carries
// the events after `from` plus a full-resync signal when `from` fell below the
// retained floor.
func (d *Daemon) JournalReadFrom(from uint64) (journal.Resume, error) {
	return d.journal.ReadFrom(from)
}

// JournalSubscribe returns a live feed of journal records appended after
// subscription plus a cancel func (backs the remote journal_subscribe op,
// R-PROT.3 / R-JRN). It forwards the journal's fan-out; the caller (the protocol
// Server) fans out to per-connection subscribers. The record type is converted to
// the wire type upstream (coreAPI) to keep this package free of a protocol import.
// The cancel func is idempotent and race-free.
func (d *Daemon) JournalSubscribe() (<-chan journal.Record, func()) {
	return d.journal.Subscribe()
}

// RecordGatewayPresence appends a `presence` record when the remote gateway
// connects or disconnects (R-JRN.7) — a daemon-side liveness proxy. It carries no
// session id; the online flag rides in the opaque payload.
func (d *Daemon) RecordGatewayPresence(online bool) error {
	payload, _ := json.Marshal(struct {
		Online bool `json:"online"`
	}{online})
	_, err := d.journal.Append(journal.Record{Type: journal.TypePresence, Payload: payload})
	return err
}

// journalRecordFor derives the journal record a meta write warrants from the
// session's previous state (R-JRN.2). The display Group is computed server-side
// here via status.Derive and never on the phone. It returns ok=false for a
// same-group status tick, which is not journalworthy.
func journalRecordFor(prev persist.Meta, prevExists bool, next persist.Meta) (journal.Record, bool) {
	switch {
	case next.Status.Process == status.ProcessExited && !(prevExists && prev.Status.Process == status.ProcessExited):
		return journal.Record{SessionID: next.ID, Type: journal.TypeExited}, true
	case next.Status.Process == status.ProcessLost && !(prevExists && prev.Status.Process == status.ProcessLost):
		return journal.Record{SessionID: next.ID, Type: journal.TypeLost}, true
	case !prevExists && next.Status.Process == status.ProcessRunning:
		return journal.Record{SessionID: next.ID, Type: journal.TypeLaunched}, true
	case prevExists && status.Derive(prev.Status) != status.Derive(next.Status):
		return journal.Record{SessionID: next.ID, Type: journal.TypeGroupTransition, Group: status.Derive(next.Status)}, true
	default:
		return journal.Record{}, false
	}
}
