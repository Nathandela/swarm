package phonecore

// A7 renderer slice C -- SNAPSHOT-RECEIVE. Server-rendered terminal snapshots arrive
// SEALED on the SAME relay mailbox as the journal records (one epoch content key, one
// seq stream). This file demuxes the two on a single crypto.MailboxReceiver: it opens
// each envelope ONCE (so the shared seq guard advances exactly once per frame), peeks a
// "kind" discriminator on the authenticated plaintext, and routes -- a terminal snapshot
// into a thin per-session cache (text lines only; no VT emulator on-device, A7 split), a
// kind-less plaintext down the EXISTING journal path (byte-identical to journal.go's
// JournalReceiver.Accept: json.Unmarshal into schema.JournalRecord, then SessionCache).

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// The kind discriminator names each frame family the phone demuxes off the ONE shared
// relay mailbox. A plaintext with an empty/absent kind is a journal record (backward-
// compatible: the bare schema.JournalRecord has no kind field, so the journal producer
// is not restamped). Every other family carries an explicit kind so Accept can route it
// instead of swallowing it into the session cache (C8 / codex#7).
const (
	kindTerminalSnapshot = "terminal_snapshot" // server-rendered terminal grid -> snapshot cache
	kindCommandReply     = "command_reply"     // daemon reply to a phone command -> reply cache
	kindEpochGrant       = "epoch_grant"       // sealed epoch-rotation grant -> pending-grant slot (C5 consumes)
	kindReconcile        = "reconcile"         // machine-published rollback authorities -> reconcile slot (PB-SYNC-7)
	kindPush             = "push"              // reserved: no live push in Phase A
)

// snapshotFrame is the wire shape of a sealed terminal-snapshot mailbox plaintext: the
// schema.TerminalSnapshot fields (promoted via anonymous embedding, so its frozen json
// tags -- session/lines/cols/rows -- stay the single source of truth) plus a "kind" tag.
// The daemon-side encoder MUST marshal this exact shape.
type snapshotFrame struct {
	Kind                    string `json:"kind"`
	schema.TerminalSnapshot        // session, lines, cols, rows (promoted)
}

// replyFrame is the wire shape of a sealed command-reply mailbox plaintext: the daemon's
// schema.Control (promoted via anonymous embedding so its frozen json tags stay the
// single source of truth) plus a kind tag. The gateway's SealControlReply MUST marshal
// this exact shape so the router demuxes a reply instead of decoding it as a journal record.
type replyFrame struct {
	Kind           string `json:"kind"`
	schema.Control        // op, session_id, operation_id, ... (promoted)
}

// reconcileFrame is the wire shape of a sealed reconcile mailbox plaintext: the
// schema.ReconcileRecord fields (promoted via anonymous embedding so its frozen json tags
// stay the single source of truth) plus a kind tag. The gateway's RelaySink.Reconcile
// MUST marshal this exact shape.
type reconcileFrame struct {
	Kind                   string `json:"kind"`
	schema.ReconcileRecord        // machine, epoch_id, the three authorities, issued_at (promoted)
}

// ErrUnreconciled is the fail-closed refusal PB-STATE-4 demands of every MUTATING op
// until the machine's reconcile record has been obtained: with no authority the phone
// cannot know whether its persisted send-seq, receive high-waters or grant watermark
// were rolled back, so authoring a command is unsafe. It is RECOVERABLE, never latched --
// the refusal clears the moment the record lands (PB-STATE-10 forbids a permanent brick),
// and observation (journal, terminal) is never gated on it.
var ErrUnreconciled = errors.New("phonecore: no reconcile record; mutating ops refused until the machine publishes its authorities")

// ReplyCache is a FIFO of the command replies the router demuxed off the shared mailbox,
// drained by the phone with Take. A reply must land here, never in the session cache
// (C8 / codex#7). Concurrency-safe, mirroring SnapshotCache.
type ReplyCache struct {
	mu      sync.Mutex
	replies []schema.Control
}

// NewReplyCache returns an empty cache.
func NewReplyCache() *ReplyCache { return &ReplyCache{} }

// Append enqueues a demuxed reply.
func (c *ReplyCache) Append(ctrl schema.Control) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.replies = append(c.replies, ctrl)
}

// Take pops the oldest cached reply (found=false when empty).
func (c *ReplyCache) Take() (schema.Control, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.replies) == 0 {
		return schema.Control{}, false
	}
	ctrl := c.replies[0]
	c.replies = c.replies[1:]
	return ctrl, true
}

// TakeFor claims the cached reply that answers operationID, in ANY arrival order, and
// consumes it. It is the attribution PB-SYNC-2 / PB-STATE-1 / PB-INPUT-4 need: a phone
// with two ops in flight cannot repair, persist an outcome or drive retry off a FIFO it
// cannot attribute.
//
// An UNTAGGED reply (empty operation_id) matches NOTHING -- not the empty key, not some
// pending op by proximity. Attributing it would persist the wrong outcome for a mutating
// op, which is worse than leaving it unattributed; Take still drains it, so nothing is
// silently lost.
func (c *ReplyCache) TakeFor(operationID string) (schema.Control, bool) {
	if operationID == "" {
		return schema.Control{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, ctrl := range c.replies {
		if ctrl.OperationID != operationID {
			continue
		}
		c.replies = append(c.replies[:i:i], c.replies[i+1:]...)
		return ctrl, true
	}
	return schema.Control{}, false
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

	reconMu sync.Mutex
	recon   schema.ReconcileRecord // latest reconcile record (PB-SYNC-7); reconOK gates mutating ops
	reconOK bool
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

// Reconciled returns the latest reconcile record the router demuxed off the shared
// mailbox (found=false until one lands). Its three authorities are what PB-STATE-4's
// resume steps consume: Sequencer.SeedFrom(InboundHighWater), SeedHighWater for the
// reply bucket (the shared journal/terminal bucket is reseeded by the frame's own
// arrival), and crypto.NewGrantReceiverAt(GrantEpoch, GrantSeq).
func (r *MailboxRouter) Reconciled() (schema.ReconcileRecord, bool) {
	r.reconMu.Lock()
	defer r.reconMu.Unlock()
	return r.recon, r.reconOK
}

// RequireReconciled is the fail-closed gate every MUTATING op passes through: nil once a
// reconcile record has been obtained, ErrUnreconciled until then. Reads are deliberately
// NOT gated -- a phone that shows nothing is indistinguishable from a dead one.
func (r *MailboxRouter) RequireReconciled() error {
	if _, ok := r.Reconciled(); !ok {
		return ErrUnreconciled
	}
	return nil
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
// cache (drained by the phone, never mistaken for a journal record); a reconcile record
// fills the reconcile slot (PB-SYNC-7, clearing the mutating-op refusal); an epoch_grant is
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
	case kindReconcile:
		// PB-SYNC-7: adopt the machine's authorities WHOLE or not at all -- a decode
		// failure leaves the router unreconciled (and still refusing mutating ops) rather
		// than half-applying a partial authority.
		var f reconcileFrame
		if err := json.Unmarshal(res.Plaintext, &f); err != nil {
			return res.Gap, err
		}
		r.reconMu.Lock()
		r.recon, r.reconOK = f.ReconcileRecord, true
		r.reconMu.Unlock()
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
		// schema.JournalRecord has no kind field), decoded byte-identically to
		// JournalReceiver.Accept (journal.go).
		var rec schema.JournalRecord
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
