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
	"fmt"
	"sync"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// The kind discriminator names each frame family the phone demuxes off the ONE shared
// relay mailbox. A plaintext with an empty/absent kind is a journal record (backward-
// compatible: the bare protocol.JournalRecord has no kind field, so the journal producer
// is not restamped). Every other family carries an explicit kind so Accept can route it
// instead of swallowing it into the session cache (C8 / codex#7).
const (
	kindTerminalSnapshot = "terminal_snapshot" // server-rendered terminal grid -> snapshot cache
	kindCommandReply     = "command_reply"     // daemon reply to a phone command -> reply cache
	kindEpochGrant       = "epoch_grant"        // sealed epoch-rotation grant -> pending-grant slot (C5 consumes)
	kindPush             = "push"               // reserved: no live push in Phase A
)

// snapshotFrame is the wire shape of a sealed terminal-snapshot mailbox plaintext: the
// protocol.TerminalSnapshot fields (promoted via anonymous embedding, so its frozen json
// tags -- session/lines/cols/rows -- stay the single source of truth) plus a "kind" tag.
// The daemon-side encoder MUST marshal this exact shape.
type snapshotFrame struct {
	Kind                      string `json:"kind"`
	protocol.TerminalSnapshot        // session, lines, cols, rows (promoted)
}

// replyFrame is the wire shape of a sealed command-reply mailbox plaintext: the daemon's
// protocol.Control (promoted via anonymous embedding so its frozen json tags stay the
// single source of truth) plus a kind tag. The gateway's SealControlReply MUST marshal
// this exact shape so the router demuxes a reply instead of decoding it as a journal record.
type replyFrame struct {
	Kind            string `json:"kind"`
	protocol.Control        // op, session_id, operation_id, ... (promoted)
}

// ReplyCache is a FIFO of the command replies the router demuxed off the shared mailbox,
// drained by the phone with Take. A reply must land here, never in the session cache
// (C8 / codex#7). Concurrency-safe, mirroring SnapshotCache.
type ReplyCache struct {
	mu      sync.Mutex
	replies []protocol.Control
}

// NewReplyCache returns an empty cache.
func NewReplyCache() *ReplyCache { return &ReplyCache{} }

// Append enqueues a demuxed reply.
func (c *ReplyCache) Append(ctrl protocol.Control) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.replies = append(c.replies, ctrl)
}

// Take pops the oldest cached reply (found=false when empty).
func (c *ReplyCache) Take() (protocol.Control, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.replies) == 0 {
		return protocol.Control{}, false
	}
	ctrl := c.replies[0]
	c.replies = c.replies[1:]
	return ctrl, true
}

// Len is the number of undrained replies.
func (c *ReplyCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.replies)
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
	replies   *ReplyCache

	grantMu sync.Mutex
	grants  [][]byte // pending epoch-grant plaintexts; C5 wires machine-side delivery + consumption
}

// NewMailboxRouter returns a router bound to the epoch content key with empty caches.
func NewMailboxRouter(key crypto.ContentKey) *MailboxRouter {
	return &MailboxRouter{
		key:       key,
		recv:      crypto.NewMailboxReceiver(),
		sessions:  NewSessionCache(),
		snapshots: NewSnapshotCache(),
		replies:   NewReplyCache(),
	}
}

// Sessions is the journal-derived session cache.
func (r *MailboxRouter) Sessions() *SessionCache { return r.sessions }

// Snapshots is the server-rendered snapshot cache.
func (r *MailboxRouter) Snapshots() *SnapshotCache { return r.snapshots }

// Replies is the command-reply cache the phone drains after driving a command.
func (r *MailboxRouter) Replies() *ReplyCache { return r.replies }

// TakeGrant pops the oldest pending epoch-grant plaintext demuxed off the mailbox
// (found=false when none). Route+expose only: pairing / epoch-rotation (C5) opens it.
func (r *MailboxRouter) TakeGrant() ([]byte, bool) {
	r.grantMu.Lock()
	defer r.grantMu.Unlock()
	if len(r.grants) == 0 {
		return nil, false
	}
	g := r.grants[0]
	r.grants = r.grants[1:]
	return g, true
}

// SeedHighWater seeds the resume high-water mark for a (sender, epoch) stream, matching
// JournalReceiver.SeedHighWater -- an envelope at seq <= N is rejected on resume (F4).
func (r *MailboxRouter) SeedHighWater(sender [8]byte, epoch uint32, seq uint64) {
	r.recv.SeedHighWater(sender, epoch, seq)
}

// Accept parses one sealed envelope, authenticates + seq-guards it through the shared
// mailbox receiver EXACTLY ONCE, then demuxes on the "kind" discriminator with an EXPLICIT
// switch over the frame families that share this one mailbox and seq space (C8 / codex#7):
// a terminal_snapshot updates the snapshot cache; a command_reply is enqueued on the reply
// cache (drained by the phone, never mistaken for a journal record); an epoch_grant is
// stashed for pairing / epoch-rotation (C5) to open; a push frame is reserved (dropped);
// and ONLY a kind-less plaintext takes the existing journal path into the session cache. An
// unrecognised kind fails closed rather than being mis-applied. gap=true reports a SKIPPED
// seq (the phone should resync). The seq gap is authenticated the moment r.recv.Accept
// returns, BEFORE any kind-specific decode runs; every branch that returns after that point
// reports the TRUE res.Gap (never a hardcoded false), so a decode failure -- an unrecognised
// kind, or a malformed frame under a future protocol version -- never silently erases a real
// gap (round-4 re-audit, codex#3 + sonnet#2). A replayed/reordered seq or an unauthenticated
// frame (res not yet known) returns false and mutates nothing (fail-closed, R-PHC.5).
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
		return res.Gap, err
	}
	switch disc.Kind {
	case kindTerminalSnapshot:
		var f snapshotFrame
		if err := json.Unmarshal(res.Plaintext, &f); err != nil {
			return res.Gap, err
		}
		r.snapshots.Apply(Snapshot{Session: f.Session, Lines: f.Lines, Cols: f.Cols, Rows: f.Rows})
	case kindCommandReply:
		var f replyFrame
		if err := json.Unmarshal(res.Plaintext, &f); err != nil {
			return res.Gap, err
		}
		r.replies.Append(f.Control)
	case kindEpochGrant:
		// Route+expose only: stash the authenticated plaintext for C5 to open. NEVER journal it.
		r.grantMu.Lock()
		r.grants = append(r.grants, res.Plaintext)
		r.grantMu.Unlock()
	case kindPush:
		// Reserved: no live push in Phase A. Recognised and dropped so it is never
		// mis-applied as a journal record (the core C8 regression).
	case "":
		// Kind-less plaintext is a journal record (backward-compatible: the bare
		// protocol.JournalRecord has no kind field), decoded byte-identically to
		// JournalReceiver.Accept (journal.go).
		var rec protocol.JournalRecord
		if err := json.Unmarshal(res.Plaintext, &rec); err != nil {
			return res.Gap, err
		}
		r.sessions.Apply(rec)
	default:
		// An unrecognised kind is NOT a journal record: swallowing it into the session
		// cache is exactly the C8 regression. Fail closed rather than mis-apply it.
		return res.Gap, fmt.Errorf("phonecore: unrecognised mailbox frame kind %q", disc.Kind)
	}
	return res.Gap, nil
}
