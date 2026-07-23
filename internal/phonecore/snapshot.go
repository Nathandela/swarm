package phonecore

// A7 renderer slice C -- SNAPSHOT-RECEIVE. Server-rendered terminal snapshots arrive
// SEALED on the SAME relay mailbox as the journal records (one epoch content key, one
// seq stream). This file demuxes the two on a single crypto.MailboxReceiver: it opens
// each envelope ONCE (so the shared seq guard advances exactly once per frame), peeks a
// "kind" discriminator on the authenticated plaintext, and routes -- a terminal snapshot
// into a thin per-session cache (text lines only; no VT emulator on-device, A7 split), a
// kind-less plaintext down the EXISTING journal path (byte-identical to journal.go's
// JournalReceiver.Accept: json.Unmarshal into protocol.JournalRecord, then SessionCache).

import (
	"encoding/json"
	"sync"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// kindTerminalSnapshot tags a mailbox plaintext as a server-rendered terminal snapshot.
// A plaintext with an empty/absent kind is a journal record (backward-compatible: the
// bare protocol.JournalRecord has no kind field).
const kindTerminalSnapshot = "terminal_snapshot"

// snapshotFrame is the wire shape of a sealed terminal-snapshot mailbox plaintext: the
// protocol.TerminalSnapshot fields (promoted via anonymous embedding, so its frozen json
// tags -- session/lines/cols/rows -- stay the single source of truth) plus a "kind" tag.
// The daemon-side encoder MUST marshal this exact shape.
type snapshotFrame struct {
	Kind                      string `json:"kind"`
	protocol.TerminalSnapshot        // session, lines, cols, rows (promoted)
}

// Snapshot is the phone's cached view of one session's server-rendered terminal grid:
// sanitized plain-text lines exactly as the daemon rendered them. The phone is THIN --
// it holds text only, never a VT emulator (A7 renderer split).
type Snapshot struct {
	Session string
	Lines   []string
	Cols    int
	Rows    int
}

// SnapshotCache holds the latest server-rendered snapshot per session, keyed by
// namespaced session id. Latest wins: a newer snapshot replaces the prior one (frames
// arrive in increasing seq behind the mailbox seq gate, so last-applied is newest).
// Concurrency-safe, mirroring SessionCache.
type SnapshotCache struct {
	mu    sync.Mutex
	snaps map[string]Snapshot
}

// NewSnapshotCache returns an empty cache.
func NewSnapshotCache() *SnapshotCache { return &SnapshotCache{snaps: map[string]Snapshot{}} }

// Apply stores s as the latest snapshot for its session (overwriting any prior one).
func (c *SnapshotCache) Apply(s Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snaps[s.Session] = s
}

// Get returns the latest cached snapshot for session.
func (c *SnapshotCache) Get(session string) (Snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.snaps[session]
	return s, ok
}

// Len is the number of sessions with a cached snapshot.
func (c *SnapshotCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.snaps)
}

// MailboxRouter demuxes one shared relay mailbox into the phone's journal and snapshot
// caches. It owns a single crypto.MailboxReceiver (the shared per-(sender,epoch) seq
// guard), the epoch content key, and both caches. An untrusted relay can replay/reorder/
// drop the sealed envelopes, so every frame is authenticated + seq-guarded ONCE before
// its plaintext is demuxed -- journal and snapshot frames share one seq space.
type MailboxRouter struct {
	key       crypto.ContentKey
	recv      *crypto.MailboxReceiver
	sessions  *SessionCache
	snapshots *SnapshotCache
}

// NewMailboxRouter returns a router bound to the epoch content key with empty caches.
func NewMailboxRouter(key crypto.ContentKey) *MailboxRouter {
	return &MailboxRouter{
		key:       key,
		recv:      crypto.NewMailboxReceiver(),
		sessions:  NewSessionCache(),
		snapshots: NewSnapshotCache(),
	}
}

// Sessions is the journal-derived session cache.
func (r *MailboxRouter) Sessions() *SessionCache { return r.sessions }

// Snapshots is the server-rendered snapshot cache.
func (r *MailboxRouter) Snapshots() *SnapshotCache { return r.snapshots }

// SeedHighWater seeds the resume high-water mark for a (sender, epoch) stream, matching
// JournalReceiver.SeedHighWater -- an envelope at seq <= N is rejected on resume (F4).
func (r *MailboxRouter) SeedHighWater(sender [8]byte, epoch uint32, seq uint64) {
	r.recv.SeedHighWater(sender, epoch, seq)
}

// Accept parses one sealed envelope, authenticates + seq-guards it through the shared
// mailbox receiver EXACTLY ONCE, then demuxes on the "kind" discriminator: a
// terminal_snapshot frame updates the snapshot cache; any other (kind-less) plaintext
// takes the existing journal path into the session cache. gap=true reports a SKIPPED seq
// (the phone should resync). A replayed/reordered seq or an unauthenticated frame returns
// the error and mutates nothing (fail-closed, R-PHC.5).
func (r *MailboxRouter) Accept(raw []byte) (gap bool, err error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return false, err
	}
	res, err := r.recv.Accept(r.key, env)
	if err != nil {
		return false, err
	}
	// Peek the discriminator on the AUTHENTICATED plaintext (never on cleartext header).
	var disc struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(res.Plaintext, &disc); err != nil {
		return false, err
	}
	if disc.Kind == kindTerminalSnapshot {
		var f snapshotFrame
		if err := json.Unmarshal(res.Plaintext, &f); err != nil {
			return false, err
		}
		r.snapshots.Apply(Snapshot{
			Session: f.Session,
			Lines:   f.Lines,
			Cols:    f.Cols,
			Rows:    f.Rows,
		})
		return res.Gap, nil
	}
	// Kind-less plaintext: the existing journal decode, byte-identical to
	// JournalReceiver.Accept (journal.go).
	var rec protocol.JournalRecord
	if err := json.Unmarshal(res.Plaintext, &rec); err != nil {
		return false, err
	}
	r.sessions.Apply(rec)
	return res.Gap, nil
}
