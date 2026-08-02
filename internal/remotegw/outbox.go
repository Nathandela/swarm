package remotegw

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
)

// OutboxEntry is one journal record's outbound attempt: the cursor it carries and the EXACT
// sealed bytes that were (or may have been) appended. A replay re-appends Envelope verbatim;
// re-sealing would mint a fresh nonce (and possibly a fresh seq), so the phone would either
// accept the record twice or stale-drop one of two rival envelopes at the same seq.
type OutboxEntry struct {
	Cursor   uint64 // the journal cursor this envelope carries
	Envelope []byte // the EXACT sealed bytes; a replay re-appends these, never re-seals
}

// Outbox is the gateway's durable OUTBOUND journal custody (PB-GW-8). It couples
// {journal cursor, sealed envelope, relay outcome} so a restart can tell "maybe delivered"
// from "never attempted". A bare durable cursor cannot: persisting it is not atomic with the
// remote append, and the crash window between the two IS the delivery-unknown case.
type Outbox interface {
	// Reserve durably records the sealed envelope for cursor BEFORE the append is
	// attempted, so a crash mid-append leaves the exact bytes to replay.
	Reserve(cursor uint64, env []byte) error
	// Commit records that the relay acked cursor: it raises Cursor and drops the entry.
	Commit(cursor uint64) error
	// Pending returns the reserved-but-uncommitted entries, oldest first.
	Pending() ([]OutboxEntry, error)
	// Cursor is the highest journal cursor durably COMMITTED.
	Cursor() uint64
	// Purge abandons every pending entry, keeping the committed cursor. It is the
	// owner's revoke and nothing else: the pending bytes are sealed under the epoch
	// the revoke rotates away, so they can never be opened again by anyone.
	Purge() error
}

// outboxSchemaVersion stamps the on-disk file so the format can migrate forward. A file
// stamped with anything else is refused rather than reinterpreted.
const outboxSchemaVersion = 1

// outboxFile is the on-disk shape. It is JSON for the same reason the inbound checkpoint is
// (inboundstate.go): a variable-length record list written at JOURNAL-RECORD rate -- a human
// rate, unlike the outbound seq's per-frame hot path -- so a self-describing, versioned,
// inspectable encoding costs nothing and makes a stuck entry readable. Envelope is base64 by
// encoding/json's []byte rule.
type outboxFile struct {
	SchemaVersion int            `json:"schema_version"`
	Cursor        uint64         `json:"cursor"`
	Pending       []outboxRecord `json:"pending"`
}

type outboxRecord struct {
	Cursor   uint64 `json:"cursor"`
	Envelope []byte `json:"envelope"`
}

// fileOutbox is an Outbox backed by one JSON file, held in memory and rewritten atomically
// on every change. An empty path makes it purely in-memory (no durability): the default for
// callers that do not provision a state dir, mirroring durableSeq and fileInboundState.
type fileOutbox struct {
	mu      sync.Mutex
	path    string
	cursor  uint64
	pending []OutboxEntry
}

// errCorruptOutbox flags an unreadable or unsupported outbox file. Custody fails closed
// exactly like the seq ceiling and the inbound checkpoint: silently starting empty would
// forget both the committed cursor (re-appending the whole journal) and any in-flight
// envelope (re-sealing it at a fresh seq).
var errCorruptOutbox = errors.New("remotegw: corrupt outbound-journal outbox file")

// OpenOutbox opens the durable outbound journal outbox at path, loading any previously
// persisted state. A missing file starts fresh (first run); a present-but-malformed or
// wrongly-versioned file is an error, never a silent reset. An empty path returns a purely
// in-memory outbox (today's behaviour: nothing survives a restart).
func OpenOutbox(path string) (Outbox, error) {
	o := &fileOutbox{path: path}
	if path == "" {
		return o, nil
	}
	cursor, pending, err := loadOutbox(path)
	if err != nil {
		return nil, err
	}
	o.cursor, o.pending = cursor, pending
	return o, nil
}

// Reserve records (or replaces) the entry for cursor and persists before returning, so a
// caller that sees nil knows the bytes survive a crash.
func (o *fileOutbox) Reserve(cursor uint64, env []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	next := o.withoutLocked(cursor)
	next = append(next, OutboxEntry{Cursor: cursor, Envelope: append([]byte(nil), env...)})
	sort.Slice(next, func(i, j int) bool { return next[i].Cursor < next[j].Cursor })
	return o.adoptLocked(o.cursor, next)
}

// Commit raises the committed high-water and drops that cursor's entry. It drops ONLY the
// committed cursor: an older entry still pending is a record whose delivery is genuinely
// unknown, and abandoning it would lose the one thing that can recover it (its sealed bytes).
func (o *fileOutbox) Commit(cursor uint64) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	high := o.cursor
	if cursor > high {
		high = cursor
	}
	return o.adoptLocked(high, o.withoutLocked(cursor))
}

// Purge drops every pending entry and keeps the committed cursor (PB-STATE-10).
//
// IT IS THE ONE PLACE THE OUTBOX'S AT-LEAST-ONCE CONTRACT IS DELIBERATELY BROKEN, and the
// revoke is the only thing entitled to break it. A pending entry is the EXACT sealed bytes
// of an undelivered frame; a revoke rotates the machine epoch, so those bytes are sealed
// under a key no future device will ever hold. Replaying them — which is precisely what the
// next gateway does, verbatim and by contract — puts unopenable frames in the re-paired
// phone's mailbox.
//
// THE CURSOR STAYS. It is the resume point the next gateway seeds journal_read from
// (RelaySink.DeliveredCursor); resetting it to zero would re-read the whole journal and
// re-flood the mailbox. The re-paired phone gets its state from the reconnect roster
// snapshot, which is current state rather than replayed history.
//
// An empty outbox is a no-op that writes NOTHING, so a machine that never ran a gateway does
// not acquire an outbox file as a side effect of revoking.
func (o *fileOutbox) Purge() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.pending) == 0 {
		return nil
	}
	return o.adoptLocked(o.cursor, nil)
}

func (o *fileOutbox) Pending() ([]OutboxEntry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]OutboxEntry(nil), o.pending...), nil
}

func (o *fileOutbox) Cursor() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.cursor
}

// withoutLocked copies the pending list minus cursor's entry. The caller must hold o.mu.
func (o *fileOutbox) withoutLocked(cursor uint64) []OutboxEntry {
	next := make([]OutboxEntry, 0, len(o.pending)+1)
	for _, e := range o.pending {
		if e.Cursor != cursor {
			next = append(next, e)
		}
	}
	return next
}

// adoptLocked persists the new state and only THEN adopts it in memory, so a failed write
// leaves exactly what a crashed process would have left: nothing durable, nothing claimed
// (fileInboundState.Save's rule). The caller must hold o.mu.
func (o *fileOutbox) adoptLocked(cursor uint64, pending []OutboxEntry) error {
	if o.path != "" {
		if err := persistOutbox(o.path, cursor, pending); err != nil {
			return err
		}
	}
	o.cursor, o.pending = cursor, pending
	return nil
}

// loadOutbox reads and validates the persisted outbox. A missing file is empty (first run);
// anything unparseable or wrongly-versioned is an error.
func loadOutbox(path string) (uint64, []OutboxEntry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, fmt.Errorf("read outbound outbox: %w", err)
	}
	var f outboxFile
	if err := json.Unmarshal(data, &f); err != nil {
		return 0, nil, fmt.Errorf("%w: %s: %v", errCorruptOutbox, path, err)
	}
	if f.SchemaVersion != outboxSchemaVersion {
		return 0, nil, fmt.Errorf("%w: %s: schema version %d unsupported (want %d)",
			errCorruptOutbox, path, f.SchemaVersion, outboxSchemaVersion)
	}
	pending := make([]OutboxEntry, 0, len(f.Pending))
	for _, rec := range f.Pending {
		pending = append(pending, OutboxEntry(rec))
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Cursor < pending[j].Cursor })
	return f.Cursor, pending, nil
}

// persistOutbox writes the outbox atomically (temp + fsync + rename + parent dir fsync), the
// same durability idiom persistSeqCeiling and persistInboundCheckpoint use. Without the dir
// fsync a power loss could resurrect an OLDER outbox, whose lower committed cursor re-appends
// the journal and whose lost reservation re-seals an in-flight record at a fresh seq.
func persistOutbox(path string, cursor uint64, pending []OutboxEntry) error {
	f := outboxFile{SchemaVersion: outboxSchemaVersion, Cursor: cursor}
	for _, e := range pending {
		f.Pending = append(f.Pending, outboxRecord(e))
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, ".outbound-outbox-*", data)
}
