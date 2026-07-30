// Package phonecore is the gomobile-ready phone-side client logic for remote control
// (R-PHC): pairing, transport, command signing, and consuming the daemon journal --
// all in Go, tested against itself on the build machine (ADR-007 D12). The SwiftUI
// shell is a thin layer over this compiled later.
//
// This slice implements the JOURNAL-RECEIVE path (R-PHC.3/.5): open a mailbox envelope
// under the epoch content key, decode the journal record, and apply it to a merged
// session cache whose Group is taken VERBATIM from the wire (never derived on-device).
package phonecore

import (
	"encoding/json"
	"sync"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/status"
)

// OpenJournalEnvelope parses and decrypts one mailbox envelope under the epoch content
// key and decodes the journal record it carries, returning the record and the
// envelope's Seq. It is fail-closed: a malformed envelope, a wrong/mismatched key, or a
// non-record plaintext all return an error and NO record (R-PHC.5: reject, never
// log-and-continue, an item that does not authenticate).
func OpenJournalEnvelope(key crypto.ContentKey, raw []byte) (schema.JournalRecord, uint64, error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return schema.JournalRecord{}, 0, err
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		return schema.JournalRecord{}, 0, err
	}
	var rec schema.JournalRecord
	if err := json.Unmarshal(plain, &rec); err != nil {
		return schema.JournalRecord{}, 0, err
	}
	return rec, env.Header.Seq, nil
}

// JournalReceiver is the phone's replay/reorder/gap-protected journal receive path
// (R-PHC.5, R-JRN.6). It wraps a crypto.MailboxReceiver plus the epoch content key: an
// untrusted relay stores the sealed envelopes and can replay, reorder, or drop them, so
// every envelope is run through the receiver's per-(sender,epoch) seq guard before its
// record is decoded.
//
// DIRECTION-BLIND, and it has NO production caller: MailboxRouter superseded it (its
// kind-less branch decodes byte-identically, snapshot.go), and only MailboxRouter.open
// checks the direction tag before Accept (direction.go). Accept here would take a frame the
// phone itself sealed for the machine and let it advance the receiver -- the reflection the
// tag exists to refuse. It stays for the tests that pin the journal decode; do not wire it
// into a phone.
type JournalReceiver struct {
	key  crypto.ContentKey
	recv *crypto.MailboxReceiver
}

// NewJournalReceiver returns a receiver bound to the epoch content key.
func NewJournalReceiver(key crypto.ContentKey) *JournalReceiver {
	return &JournalReceiver{key: key, recv: crypto.NewMailboxReceiver()}
}

// Accept parses one sealed envelope, authenticates + seq-guards it through the mailbox
// receiver, and decodes the journal record. A replayed/reordered seq returns
// crypto.ErrStaleSeq and a zero record (the caller must NOT apply it). A valid but
// SKIPPED seq returns gap=true alongside the decoded record, so the phone
// journal_read-resyncs instead of trusting contiguity.
func (r *JournalReceiver) Accept(raw []byte) (rec schema.JournalRecord, gap bool, err error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return schema.JournalRecord{}, false, err
	}
	res, err := r.recv.Accept(r.key, env)
	if err != nil {
		return schema.JournalRecord{}, false, err
	}
	if err := json.Unmarshal(res.Plaintext, &rec); err != nil {
		return schema.JournalRecord{}, false, err
	}
	return rec, res.Gap, nil
}

// SeedHighWater seeds the resume high-water mark for a (sender, epoch) stream to a
// journal_read snapshot cursor N, so an envelope at seq <= N is rejected on resume (F4).
func (r *JournalReceiver) SeedHighWater(sender [8]byte, epoch uint32, seq uint64) {
	r.recv.SeedHighWater(sender, epoch, seq)
}

// CachedSession is the phone's view of one session. Group is verbatim from the wire.
type CachedSession struct {
	SessionID string
	Group     status.Group
	Present   bool
}

// SessionCache is the phone's merged session model (R-PHC.3), keyed by namespaced
// session id. Group is applied VERBATIM from each record (roster snapshots and
// group_transition events carry it); the phone never derives a Group on-device.
type SessionCache struct {
	mu       sync.Mutex
	sessions map[string]CachedSession
	cursor   uint64
}

// NewSessionCache returns an empty cache.
func NewSessionCache() *SessionCache {
	return &SessionCache{sessions: map[string]CachedSession{}}
}

// Apply folds one journal record into the cache and reports whether it mutated. A record
// with a SessionID ensures the session exists (present); a non-empty Group updates it
// verbatim; a deleted record removes it. The cursor advances to the highest applied
// record cursor. A record whose Cursor is STRICTLY LESS than the highest applied cursor
// is a stale replay/reorder (defense in depth behind the JournalReceiver seq guard): it
// mutates nothing and returns false. An equal cursor still applies -- a roster snapshot
// shares one read cursor across all its sessions.
func (c *SessionCache) Apply(rec schema.JournalRecord) (applied bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applyLocked(rec)
}

// applyLocked is Apply inside the critical section, so a caller already holding the lock
// (reseed) folds records through exactly the same rules.
func (c *SessionCache) applyLocked(rec schema.JournalRecord) (applied bool) {
	if rec.Cursor < c.cursor {
		return false
	}
	if rec.Cursor > c.cursor {
		c.cursor = rec.Cursor
	}
	if rec.SessionID == "" {
		return true // session-neutral record (e.g. presence)
	}
	if rec.Type == string(journalTypeDeleted) {
		delete(c.sessions, rec.SessionID)
		return true
	}
	cs, ok := c.sessions[rec.SessionID]
	if !ok {
		cs = CachedSession{SessionID: rec.SessionID}
	}
	cs.Present = true
	if rec.Group != "" {
		cs.Group = rec.Group // verbatim from the wire (R-PHC.3)
	}
	c.sessions[rec.SessionID] = cs
	return true
}

// reseed REPLACES the whole cached set and the cursor from an atomic roster+events snapshot
// (PB-SYNC-2 / PB-SYNC-8). It is not a merge, and it must not be:
//
//   - the ROSTER carries Cursor 0 on every record, deliberately (internal/daemon/journal.go:
//     "a roster record is a set member keyed by SessionID, NOT a point in the cursor-ordered
//     event stream"), so merging it into a cache whose cursor has advanced past zero drops
//     every record and the designated repair channel reports success while changing nothing;
//   - a session ABSENT from the roster ended while the phone was not listening, so carrying
//     it across leaves a dead session on the screen with a live-looking group -- the same lie
//     the stale flag exists to prevent.
//
// The events are applied ON TOP of the roster, which is what makes it the ATOMIC
// roster+events snapshot rather than a roster that is already out of date when it lands. The
// cursor is then set to the snapshot BOUNDARY: it REPLACES, never merges and never maxes,
// because the boundary is what the roster is current as of.
func (c *SessionCache) reseed(rs schema.JournalReseed) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions = make(map[string]CachedSession, len(rs.Roster))
	c.cursor = 0
	for _, rec := range rs.Roster {
		c.applyLocked(rec)
	}
	for _, rec := range rs.Events {
		c.applyLocked(rec)
	}
	c.cursor = rs.Cursor
}

// restore seeds one cached session from durable state, bypassing the cursor guard (the
// entry IS the resume point, not a record being applied on top of it).
func (c *SessionCache) restore(cs CachedSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[cs.SessionID] = cs
}

// Get returns the cached session for id.
func (c *SessionCache) Get(id string) (CachedSession, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cs, ok := c.sessions[id]
	return cs, ok
}

// List returns every cached session (unordered snapshot copy).
func (c *SessionCache) List() []CachedSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]CachedSession, 0, len(c.sessions))
	for _, cs := range c.sessions {
		out = append(out, cs)
	}
	return out
}

// Cursor is the highest record cursor applied so far.
func (c *SessionCache) Cursor() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cursor
}

// journalTypeDeleted mirrors journal.TypeDeleted without importing the daemon-internal
// journal package: the phone only ever sees the wire string.
const journalTypeDeleted = "deleted"
