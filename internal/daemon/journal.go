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
	// Atomicity (R-JRN.4): the roster snapshot and the journal cursor must be taken
	// as ONE consistent view — a "roster as-of cursor N". Every SESSION-AFFECTING meta
	// mutation flows through the single meta writer under d.writeMu (G6), and its
	// cursor-advancing journal append happens inside that same writeMu section
	// (saveMetaLocked, and Delete's `deleted` append). Holding writeMu here therefore
	// freezes both the roster-visible meta set and any cursor advance that could change
	// it, so no such write can interleave between reading the cursor and scanning the
	// roster. Session-NEUTRAL appends (RecordGatewayPresence) may advance the cursor
	// without writeMu, but they carry no SessionID and never touch d.sessions, so they
	// cannot make the roster inconsistent — j.mu just serializes them before or after
	// the read. The lock order (writeMu -> j.mu, taken inside ReadFrom) is identical to
	// the append path, so no new lock cycle is introduced.
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	res, err := d.journal.ReadFrom(from)
	if err != nil {
		return journal.Resume{}, err
	}
	res.Roster = d.rosterSnapshotLocked()
	return res, nil
}

// rosterSnapshotLocked builds the live-session roster for a JournalReadFrom snapshot
// (the snapshot half of R-JRN.4). The caller MUST hold d.writeMu so the scan is
// consistent with the journal cursor read in the same section. It emits one synthetic
// journal.Record (Type journal.TypeRoster — never appended) per PERSISTED,
// non-tombstoned session, carrying the server-derived display Group. Persisted-only is
// deliberate: a launch reservation (persisted=false) is not yet a real session, so
// including it would show the phone a phantom that may never materialize. A reconcile-
// reconnected Running session is adopted via putMem with persisted=true and emits NO
// journal event, so the roster is the ONLY path by which the phone can enumerate it.
func (d *Daemon) rosterSnapshotLocked() []journal.Record {
	d.mu.Lock()
	metas := make([]persist.Meta, 0, len(d.sessions))
	for _, s := range d.sessions {
		if !s.persisted {
			continue
		}
		metas = append(metas, s.meta)
	}
	d.mu.Unlock()

	roster := make([]journal.Record, 0, len(metas))
	for _, m := range metas {
		if d.isDeleted(m.ID) {
			continue
		}
		// Cursor is deliberately left unset (0): a roster record is a set member keyed
		// by SessionID, NOT a point in the cursor-ordered event stream. res.Cursor is
		// the single snapshot boundary; a consumer merges the roster as the baseline
		// state and then applies Events (cursor > from) on top. A consumer must not
		// sort roster records by their own Cursor.
		roster = append(roster, journal.Record{
			SessionID: m.ID,
			Type:      journal.TypeRoster,
			Group:     status.Derive(m.Status),
		})
	}
	return roster
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
