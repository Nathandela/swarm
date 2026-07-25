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
	"maps"
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

	// core is the durable custody AcceptCommit commits through, nil for a bare router
	// (Accept-only, no persistence).
	core *Core

	grantMu sync.Mutex
	grants  [][]byte // pending epoch-grant plaintexts; C5 wires machine-side delivery + consumption

	// mu guards the BOUND state above (key, receiver, caches) together with the reconcile
	// slot below: rebind replaces all of them at once when the Core adopts a new epoch, so a
	// reader takes the lock just long enough to copy the pointer it needs.
	mu      sync.Mutex
	recon   schema.ReconcileRecord // latest reconcile record (PB-SYNC-7)
	reconOK bool                   // a record has ARRIVED
	adopted bool                   // ... and its authorities have been applied (gates mutating ops)
	reconAt Bucket                 // the bucket it arrived on: its self-certifying JournalCeiling
	epoch   uint32                 // the epoch whose buckets are unverified while unadopted
	stale   map[Bucket]bool        // persisted stale flags, restored from State.Stale
}

// bound copies the router's currently-bound key, receiver and caches.
func (r *MailboxRouter) bound() (crypto.ContentKey, *crypto.MailboxReceiver, *SessionCache, *SnapshotCache, *ReplyCache) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.key, r.recv, r.sessions, r.snapshots, r.replies
}

// NewMailboxRouter returns a router bound to the epoch content key with empty caches. It
// has no durable custody, so a reconcile record is adopted by its mere arrival: there is no
// state to validate it against and no coordinate for it to move.
func NewMailboxRouter(key crypto.ContentKey) *MailboxRouter {
	return newMailboxRouter(key, nil)
}

func newMailboxRouter(key crypto.ContentKey, core *Core) *MailboxRouter {
	return &MailboxRouter{
		key:       key,
		recv:      crypto.NewMailboxReceiver(),
		sessions:  NewSessionCache(),
		snapshots: NewSnapshotCache(),
		replies:   NewReplyCache(),
		core:      core,
		stale:     map[Bucket]bool{},
	}
}

// rebind rebuilds the router from durable state: the epoch content key, the per-bucket
// receive high-waters replayed into a fresh crypto.MailboxReceiver (a receiver that was
// never seeded SKIPS the staleness check on the first frame of every stream, so a retaining
// relay could redeliver everything), the decoded caches, and the persisted stale flags.
func (r *MailboxRouter) rebind(st State) {
	recv := crypto.NewMailboxReceiver()
	for b, seq := range st.Receive {
		recv.SeedHighWater(b.Sender, b.Epoch, seq)
	}
	sessions := NewSessionCache()
	for _, cs := range st.Sessions {
		sessions.restore(cs)
	}
	snapshots := NewSnapshotCache()
	for _, s := range st.Snapshots {
		snapshots.Apply(s)
	}

	r.mu.Lock()
	r.key, r.recv = st.Keys.ContentKey, recv
	r.sessions, r.snapshots = sessions, snapshots
	// The delivery FIFO is rebuilt from the DURABLE outcomes: a reply is decoded content
	// like any other, so losing it on a process death would leave the phone unable to
	// settle an op whose frame the durable high-water now refuses on redelivery
	// (PB-SYNC-2: an op is resolved by its durable outcome, or the stream stays
	// unresolved). Only contiguously-received replies are in there -- see
	// Core.commitReceive. RESIDUAL: OpOutcomes is never pruned, so every launch re-offers
	// every outcome ever recorded; bounding it needs a retention coordinate this slice's
	// pinned schema does not carry.
	r.replies = NewReplyCache()
	for _, ctrl := range st.OpOutcomes {
		r.replies.Append(ctrl)
	}
	r.recon, r.reconOK, r.adopted, r.reconAt = schema.ReconcileRecord{}, false, false, Bucket{}
	r.epoch, r.stale = st.EpochID, maps.Clone(st.Stale)
	if r.stale == nil {
		r.stale = map[Bucket]bool{}
	}
	r.mu.Unlock()
}

// adopt records that Core.Reconcile applied rec: the reply bucket is reseeded from its own
// authority (the shared journal/terminal bucket was reseeded by the record's own arrival),
// the stale flags this epoch cleared are dropped, and mutating ops are unblocked.
func (r *MailboxRouter) adopt(st State, journal, reply Bucket, rec schema.ReconcileRecord) {
	_, recv, _, _, _ := r.bound()
	recv.SeedHighWater(reply.Sender, reply.Epoch, rec.ReplyCeiling)
	recv.SeedHighWater(journal.Sender, journal.Epoch, rec.JournalCeiling)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adopted = true
	r.stale = maps.Clone(st.Stale)
	if r.stale == nil {
		r.stale = map[Bucket]bool{}
	}
}

// reconcileRecord is the arrived record plus the bucket it arrived on (whose high-water its
// JournalCeiling certifies).
func (r *MailboxRouter) reconcileRecord() (schema.ReconcileRecord, Bucket, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recon, r.reconAt, r.reconOK
}

// Stale reports whether a bucket's content may not be trusted. A flag persisted across the
// restart is one cause; the other is that this epoch has no authority in hand at all --
// while the phone is unreconciled, or while a burned seq gap (PB-STATE-8) leaves the fate of
// its in-flight ops unknown, EVERY bucket of the current epoch is unverified.
func (r *MailboxRouter) Stale(b Bucket) bool {
	r.mu.Lock()
	persisted, adopted, epoch := r.stale[b], r.adopted, r.epoch
	r.mu.Unlock()
	if persisted {
		return true
	}
	if r.core == nil || b.Epoch != epoch {
		return false
	}
	return !adopted || r.core.Seq().GapPending()
}

// Sessions is the journal-derived session cache.
func (r *MailboxRouter) Sessions() *SessionCache { _, _, s, _, _ := r.bound(); return s }

// Snapshots is the server-rendered snapshot cache.
func (r *MailboxRouter) Snapshots() *SnapshotCache { _, _, _, s, _ := r.bound(); return s }

// Replies is the command-reply cache the phone drains after driving a command.
func (r *MailboxRouter) Replies() *ReplyCache { _, _, _, _, c := r.bound(); return c }

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
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recon, r.reconOK
}

// RequireReconciled is the fail-closed gate every MUTATING op passes through: nil once a
// reconcile record has been ADOPTED, ErrUnreconciled until then. Reads are deliberately NOT
// gated -- a phone that shows nothing is indistinguishable from a dead one.
//
// Arrival is not adoption when the router has durable custody: a record naming another
// machine or epoch is refused by Core.Reconcile, and a refused authority is not a
// reconciliation. A bare router has nothing to validate against, so arrival is adoption.
func (r *MailboxRouter) RequireReconciled() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.adopted {
		return ErrUnreconciled
	}
	return nil
}

// SeedHighWater seeds the resume high-water mark for a (sender, epoch) stream, matching
// JournalReceiver.SeedHighWater -- an envelope at seq <= N is rejected on resume (F4).
func (r *MailboxRouter) SeedHighWater(sender [8]byte, epoch uint32, seq uint64) {
	_, recv, _, _, _ := r.bound()
	recv.SeedHighWater(sender, epoch, seq)
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
	_, res, f, err := r.open(raw)
	if err != nil {
		gap := false
		if res != nil {
			gap = res.Gap
		}
		return gap, err
	}
	r.apply(f)
	return res.Gap, nil
}

// inboundFrame is one authenticated plaintext, decoded but NOT yet applied. Splitting the
// decode from the application is what lets AcceptCommit order the durable commit around it.
type inboundFrame struct {
	kind     string
	bucket   Bucket
	seq      uint64
	snapshot Snapshot
	reply    schema.Control
	record   schema.JournalRecord
	grant    []byte
	recon    schema.ReconcileRecord
}

// open parses, authenticates and seq-guards one envelope EXACTLY ONCE, then decodes its
// plaintext. res is non-nil once the frame authenticated, so a caller can report the TRUE
// gap even when the decode then fails.
func (r *MailboxRouter) open(raw []byte) (Bucket, *crypto.MailboxResult, inboundFrame, error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return Bucket{}, nil, inboundFrame{}, err
	}
	b := Bucket{Sender: env.Header.SenderKeyID, Epoch: env.Header.EpochID}
	key, recv, _, _, _ := r.bound()
	res, err := recv.Accept(key, env)
	if err != nil {
		return b, nil, inboundFrame{}, err
	}
	// Peek the discriminator on the AUTHENTICATED plaintext (never on cleartext header).
	var disc struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(res.Plaintext, &disc); err != nil {
		return b, res, inboundFrame{}, err
	}
	f := inboundFrame{kind: disc.Kind, bucket: b, seq: env.Header.Seq}
	switch disc.Kind {
	case kindTerminalSnapshot:
		var sf snapshotFrame
		if err := json.Unmarshal(res.Plaintext, &sf); err != nil {
			return b, res, inboundFrame{}, err
		}
		f.snapshot = Snapshot{Session: sf.Session, Lines: sf.Lines, Cols: sf.Cols, Rows: sf.Rows}
	case kindCommandReply:
		var rf replyFrame
		if err := json.Unmarshal(res.Plaintext, &rf); err != nil {
			return b, res, inboundFrame{}, err
		}
		f.reply = rf.Control
	case kindReconcile:
		// PB-SYNC-7: adopt the machine's authorities WHOLE or not at all -- a decode
		// failure leaves the router unreconciled (and still refusing mutating ops) rather
		// than half-applying a partial authority.
		var cf reconcileFrame
		if err := json.Unmarshal(res.Plaintext, &cf); err != nil {
			return b, res, inboundFrame{}, err
		}
		f.recon = cf.ReconcileRecord
	case kindEpochGrant:
		// Route+expose only: stash the authenticated plaintext for C5 to open. NEVER journal it.
		f.grant = res.Plaintext
	case kindPush:
		// Reserved: no live push in Phase A. Recognised and dropped so it is never
		// mis-applied as a journal record (the core C8 regression).
	case "":
		// Kind-less plaintext is a journal record (backward-compatible: the bare
		// schema.JournalRecord has no kind field), decoded byte-identically to
		// JournalReceiver.Accept (journal.go).
		if err := json.Unmarshal(res.Plaintext, &f.record); err != nil {
			return b, res, inboundFrame{}, err
		}
	default:
		// An unrecognised kind is NOT a journal record: swallowing it into the session
		// cache is exactly the C8 regression. Fail closed rather than mis-apply it.
		return b, res, inboundFrame{}, fmt.Errorf("phonecore: unrecognised mailbox frame kind %q", disc.Kind)
	}
	return b, res, f, nil
}

// apply mutates the caches and slots the decoded frame belongs to.
func (r *MailboxRouter) apply(f inboundFrame) {
	_, _, sessions, snapshots, replies := r.bound()
	switch f.kind {
	case kindTerminalSnapshot:
		snapshots.Apply(f.snapshot)
	case kindCommandReply:
		replies.Append(f.reply)
	case kindReconcile:
		r.mu.Lock()
		r.recon, r.reconOK, r.reconAt = f.recon, true, f.bucket
		// A bare router has no coordinates to move and nothing to validate the record
		// against, so arrival IS adoption; with durable custody Core.Reconcile adopts.
		if r.core == nil {
			r.adopted = true
		}
		r.mu.Unlock()
	case kindEpochGrant:
		r.grantMu.Lock()
		r.grants = append(r.grants, f.grant)
		r.grantMu.Unlock()
	case "":
		sessions.Apply(f.record)
	}
}

// Receipt reports what became of one committed frame: whether its seq revealed a GAP, and
// whether the relay was acked for it.
type Receipt struct {
	Gap   bool
	Acked bool
}

// AcceptCommit is Accept plus the durable transaction PB-STATE-7 requires, in the order the
// two invariants force -- NO FRAME IS BOTH ACKED AND UNAPPLIED, NO FRAME IS APPLIED TWICE:
//
//  1. authenticate and seq-guard the envelope once;
//  2. commit ONE transaction -- this bucket's high-water, the stale flags, the relay read
//     cursor AND the decoded content -- BEFORE the ack. Acking a frame whose guard has not
//     advanced would let a redelivery be applied a second time; acking one whose content is
//     not yet durable lets the relay compact the only copy of a frame a SIGKILL is about to
//     erase, and the same durable guard then refuses the redelivery;
//  3. mirror the committed frame into the live caches, then ack the relay so the mailbox
//     can compact.
//
// Every step is fail-closed and reports its own error: a failed commit applies nothing and
// acks nothing (the relay keeps the only copy), and a frame the durable high-water has
// already seen is REFUSED with crypto.ErrStaleSeq yet still acked -- otherwise the phone
// re-reads it forever while the relay mailbox never compacts.
//
// "No frame is both acked and unapplied" is exact for a CONTIGUOUS frame. A frame that
// leaves a HOLE in its bucket is deliberately acked with its GUARD ONLY committed: folding
// the content of a stream with holes into the durable model is the "later state trusted
// before reconciliation" PB-STATE-8 forbids, so that frame is delivered to the live caches,
// its bucket is marked stale, and its content is never made durable. An op such a frame
// would have resolved therefore stays UNRESOLVED across a restart -- by design, until the
// op is re-driven or the machine's reconcile record re-establishes the authorities.
func (r *MailboxRouter) AcceptCommit(raw []byte, cursor uint64) (Receipt, error) {
	b, res, f, err := r.open(raw)
	if err != nil {
		if errors.Is(err, crypto.ErrStaleSeq) {
			// Already applied. The ack is the idempotent half of the retry.
			return Receipt{Acked: r.ack(cursor) == nil}, err
		}
		gap := false
		if res != nil {
			gap = res.Gap
		}
		return Receipt{Gap: gap}, err
	}
	if r.core == nil {
		r.apply(f)
		return Receipt{Gap: res.Gap, Acked: true}, nil
	}
	// One Save covers the guard AND the content; a frame that left a hole in the bucket is
	// delivered but NOT trusted into the durable model -- the phone resyncs (or reconciles)
	// that stream first. See commitReceive.
	contiguous, err := r.core.commitReceive(b, f, cursor)
	if err != nil {
		return Receipt{Gap: res.Gap}, err
	}
	gap := res.Gap || !contiguous
	if !contiguous {
		r.markStale(b)
	}
	r.apply(f)
	if err := r.ack(cursor); err != nil {
		return Receipt{Gap: gap}, err
	}
	return Receipt{Gap: gap, Acked: true}, nil
}

// markStale mirrors the persisted stale flag into the live router.
func (r *MailboxRouter) markStale(b Bucket) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stale[b] = true
}

// ack releases the relay item; a router with no Core (or a Core with no Acker) manages no
// mailbox and has nothing outstanding.
func (r *MailboxRouter) ack(cursor uint64) error {
	if r.core == nil {
		return nil
	}
	return r.core.ackCursor(cursor)
}
