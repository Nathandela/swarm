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
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/grantwire"
)

// InboundMaxAge bounds the authenticated age of a MACHINE -> phone sealed frame (PB-TIME-2,
// value from §6.0). It is the mirror of the gateway's own inbound bound and MUST hold the
// same number as remotegw.InboundMaxAge -- one §6.0 budget, not two -- which the S11 tests
// pin across the two packages (phonecore cannot import remotegw: PB-BIND-0).
//
// Like the gateway's, it is a BACKSTOP behind the per-(sender, epoch) replay high-water,
// not a replacement: a fresh receiver has seen == false for every stream and skips the
// staleness check on the first frame, so a relay that retained frames would otherwise have
// them re-accepted at the guard after a restart. Ten minutes sits well above §6.0's
// +/-30 s skew budget and any plausible relay delay, and well below the 7 d retention cap.
const InboundMaxAge = 10 * time.Minute

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
	kindJournalReseed    = "journal_reseed"    // atomic roster+events snapshot -> the JOURNAL repair (PB-SYNC-2)
	kindPush             = "push"              // reserved: no live push in Phase A
)

// The four REPAIR CHANNELS of PB-SYNC-1. Staleness is MARKED per SEQ BUCKET -- journal and
// terminal share one (sender, epoch) space and crypto.MailboxResult carries a bare Gap bool
// with no frame kind, so a skipped seq CANNOT be attributed to one of them -- but it is
// CLEARED per channel, because the four are repaired by four different things.
const (
	// StreamJournal is repaired by an atomic roster+events reseed (schema.JournalReseed).
	StreamJournal = "journal"
	// StreamTerminal is repaired by a fresh full server-rendered snapshot. A journal
	// reseed cannot repair a missed grid.
	StreamTerminal = "terminal"
	// StreamReply is repaired by the durable outcome of each op, or it stays unresolved.
	StreamReply = "reply"
	// StreamGrant is repaired by PB-KEY-3's re-grant.
	StreamGrant = "grant"
)

// streamsOf are the repair channels one seq bucket carries, and therefore the channels a
// hole in it stales. The machine stamps its routing key id on journal, terminal AND
// reconcile frames; command replies deliberately leave SenderKeyID zero (command_in.go),
// which is the split that makes the reply stream's contiguity independent of the other two.
func streamsOf(b Bucket) []string {
	if b.Sender == ([8]byte{}) {
		return []string{StreamReply}
	}
	return []string{StreamJournal, StreamTerminal}
}

// repairedBy is the channel one frame kind REPAIRS, or "" for a kind that repairs nothing.
// It is the only place the PB-SYNC-2 mapping is written down: a reseed carries a roster and
// events and NO grid, and a grid says nothing about which sessions exist -- so neither can
// stand in for the other, and a kind that is absent here (a reply, a reconcile record, a
// rotation grant) clears nothing at all.
func repairedBy(kind string) string {
	switch kind {
	case kindJournalReseed:
		return StreamJournal
	case kindTerminalSnapshot:
		return StreamTerminal
	default:
		return ""
	}
}

// snapshotFrame is the wire shape of a sealed terminal-snapshot mailbox plaintext: the
// schema.TerminalSnapshot fields (promoted via anonymous embedding, so its frozen json
// tags -- session/lines/cols/rows -- stay the single source of truth) plus a "kind" tag.
// The daemon-side encoder MUST marshal this exact shape.
type snapshotFrame struct {
	Kind                    string `json:"kind"`
	schema.TerminalSnapshot        // session, lines, cols, rows (promoted) -- FROZEN, unchanged

	// The versioned view's coordinates (ADR-017 T4-a / T8-a), added by the closing round.
	//
	// THEY ARE SIBLING KEYS AND NOT A SWAPPED EMBED, and every one is omitempty (the time a
	// pointer, because a zero time.Time is not omitted by encoding/json). A frame carrying
	// none of them therefore serializes BYTE-IDENTICALLY to the shape this wire has always
	// had, which is the GG-7 rule the Control struct states in as many words.
	// `TestSnapshotFrame_WireShape` pins both plaintexts -- the bare legacy frame AND one
	// carrying all five sibling keys -- and `TestSnapshotWireParity_GatewayMarshalMatchesPhoneFrame`
	// ties this declaration byte-for-byte to remotegw's twin (bead 65bj). A machine that predates the
	// closing round sends none of them and the phone reads zero, which is the honest answer:
	// "this machine does not version its views", never a fabricated epoch.
	SessionInstance string     `json:"session_instance,omitempty"`
	ViewEpoch       uint64     `json:"view_epoch,omitempty"`
	Revision        uint64     `json:"revision,omitempty"`
	Reset           bool       `json:"reset,omitempty"`
	RenderedAt      *time.Time `json:"rendered_at,omitempty"`
}

// derefTime reads an optional wire time as a value. Absent stays ZERO, which the snapshot
// cache and the phone's staleness indicator both read as "the machine sent no render time"
// rather than as the epoch.
func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
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

// reseedFrame is the wire shape of a sealed journal-reseed plaintext: the
// schema.JournalReseed fields (promoted via anonymous embedding so its pinned json tags
// stay the single source of truth) plus a kind tag. The gateway's RelaySink.Reseed MUST
// marshal this exact shape.
//
// It is ONE frame and not N roster frames because PB-SYNC-3 requires the repair to be
// committed atomically with the matching transport watermark, and N independent frames
// cannot be: a death between frames leaves the phone with half a snapshot and a watermark
// claiming the whole. The frame's own arrival seq IS that watermark.
type reseedFrame struct {
	Kind                 string `json:"kind"`
	schema.JournalReseed        // roster, events, cursor (promoted)
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

	// The versioned view's own coordinates (ADR-017 T4-a / T8-a), carried since the closing
	// round because they were produced by the daemon and thrown away before the wire.
	//
	// SessionInstance is WHICH INCARNATION this screen belongs to, ViewEpoch is which
	// render-loop run produced it, Revision is its order within that run, and Reset marks the
	// first emission of every epoch on every path. RenderedAt is the MACHINE's clock, which is
	// the only clock a staleness indicator may be written over: arrival time renders a replayed
	// backlog as fresh and a held relay as live.
	//
	// A machine that predates the closing round sends none of them, and zero is the honest
	// answer there -- "this machine does not version its views" -- never a fabricated epoch.
	SessionInstance string
	ViewEpoch       uint64
	Revision        uint64
	Reset           bool
	RenderedAt      time.Time
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

// Apply stores s as the latest snapshot for its session, under ADR-017 T4-a's ordering rule:
//
//	differing epoch = HARD RESET, adopt s and discard what is held
//	same epoch      = adopt s only if its revision is STRICTLY GREATER
//
// LATEST-WINS WAS WRONG IN ONE DIRECTION AND HIGHEST-REVISION-WINS IS WRONG IN THE OTHER, which
// is exactly why the epoch exists. Two frames of one run that arrive out of order left the phone
// on the older screen permanently; and a rule written over the revision alone discards every
// frame of the NEXT run, because a new render loop restarts the revision at 1 while the phone
// holds N. That second failure is the frozen screen T4-a names: a session replaced under the
// same id, rendering a plausible view that stopped being true.
//
// AN UNVERSIONED FRAME ALWAYS LANDS (epoch 0). Two things produce one and both must be applied:
// a machine that predates the closing round, whose every frame is epoch 0 and would otherwise
// never update; and `Gateway.BlankTerminal`, the machine saying "I am no longer rendering this"
// to a phone that never asked it to stop -- a blank dropped as unversioned is a dead grid the
// screen goes on calling fresh.
//
// AND THE MACHINE'S OWN `reset` MARKER ALWAYS LANDS, whatever the epoch and revision say
// (closing round 2). The epoch counter is a bare process-global in the daemon
// (internal/daemon/terminalview.go), so it RESTARTS AT 1 in every daemon process -- and a
// session surviving a daemon crash, restart or upgrade is a designed property of this system.
// A phone holding {epoch 1, revision 40} therefore discarded the restarted daemon's {epoch 1,
// revision 1} AND the 39 revisions after it, and the user read a plausible, frozen, pre-restart
// terminal: the same failure T4-a names, in the variant where the epoch happens to collide.
// `reset` is documented (protocol.md, T4-a) as "true on the FIRST snapshot of every epoch, on
// every path" and as what "tells the phone to discard prior state"; the daemon has always sent
// it and the decoder has always read it. Reading it HERE is what makes the epoch's
// process-locality harmless -- a colliding epoch is still a hard reset, because the machine
// says it is one -- and it is deliberately the only way a lower revision may win, so the
// reorder rule above is untouched for every frame the machine did not mark.
func (c *SnapshotCache) Apply(s Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if held, ok := c.snaps[s.Session]; ok && !s.Reset && s.ViewEpoch != 0 &&
		s.ViewEpoch == held.ViewEpoch && s.Revision <= held.Revision {
		return
	}
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
	// acceptMu serialises the receiver's in-memory replay-guard advance with the durable
	// commit that either adopts or rolls it back. MailboxReceiver.Accept necessarily moves
	// first; without this outer transaction lock, restoring from durable state after a Save
	// failure could race another accepted frame and erase its live high-water.
	acceptMu sync.Mutex

	key       crypto.ContentKey
	recv      *crypto.MailboxReceiver
	sessions  *SessionCache
	snapshots *SnapshotCache
	replies   *ReplyCache
	// items is the TRANSCRIPT (interaction.go). It is bound beside the other caches and
	// replaced by the same rebind, but it is deliberately NOT in bound(): every existing
	// caller of that helper wants a different cache, and widening its result would edit seven
	// call sites to carry a pointer none of them reads. Items() takes the same lock for the
	// same instant instead.
	items *ItemStore

	// core is the durable custody AcceptCommit commits through, nil for a bare router
	// (Accept-only, no persistence).
	core *Core

	grantMu sync.Mutex
	grants  [][]byte // pending epoch-grant plaintexts; C5 wires machine-side delivery + consumption

	// mu guards the BOUND state above (key, receiver, caches) together with the reconcile
	// slot below: rebind replaces all of them at once when the Core adopts a new epoch, so a
	// reader takes the lock just long enough to copy the pointer it needs.
	mu       sync.Mutex
	recon    schema.ReconcileRecord // latest reconcile record (PB-SYNC-7)
	reconOK  bool                   // a record has ARRIVED
	adopted  bool                   // ... and its authorities have been applied (gates mutating ops)
	reconAt  Bucket                 // the bucket it arrived on: its self-certifying JournalCeiling
	epoch    uint32                 // the epoch whose buckets are unverified while unadopted
	stale    map[Bucket]bool        // persisted stale flags, restored from State.Stale
	staleStr map[string]bool        // persisted per-CHANNEL flags, restored from State.StaleStreams

	// ageRefused is "the last thing this drain did with a frame was throw it away for being
	// past PB-TIME-2's bound". It is LIVE, never durable, and it is not a stale flag: no seq
	// was skipped and no repair channel has a hole -- the frames are simply not usable, so
	// there is nothing for a resync to repair and nothing a later Save should remember.
	//
	// It exists because the condition is otherwise INVISIBLE. The websocket is up, so the
	// connection state machine has nothing to say and App.ConnectionState reads "online" --
	// which a screen renders as "Connected to your machine." while every frame the machine
	// sends is refused on arrival. ADR-007 B42.
	ageRefused bool
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
		items:     NewItemStore(),
		core:      core,
		stale:     map[Bucket]bool{},
		staleStr:  map[string]bool{},
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
	// The transcript is restored for the reason the caches above are, and the case is sharper:
	// the receive high-water is durable, so a relay redelivery of the frames that built it is
	// REFUSED (crypto.ErrStaleSeq). A transcript held only in memory is therefore gone for
	// good after the SIGKILL Android hands out routinely, not merely re-fetched -- and with it
	// goes any pending approval card the machine is still blocked on (IS-LIFE-3).
	items := NewItemStore()
	for _, it := range st.Items {
		items.restore(it)
	}

	r.mu.Lock()
	r.key, r.recv = st.Keys.ContentKey, recv
	r.sessions, r.snapshots, r.items = sessions, snapshots, items
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
	r.ageRefused = false
	r.epoch, r.stale = st.EpochID, maps.Clone(st.Stale)
	if r.stale == nil {
		r.stale = map[Bucket]bool{}
	}
	r.staleStr = maps.Clone(st.StaleStreams)
	if r.staleStr == nil {
		r.staleStr = map[string]bool{}
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

// Items is the transcript folded from the journal's interaction records (IS-LAYER-1).
func (r *MailboxRouter) Items() *ItemStore {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.items
}

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
	return r.AcceptAt(raw, time.Now())
}

// AcceptAt is Accept with the age clock injected, so PB-TIME-2's bound is testable at its
// boundary without waiting ten minutes. Production reads through Accept and AcceptCommit,
// which pass time.Now().
func (r *MailboxRouter) AcceptAt(raw []byte, now time.Time) (gap bool, err error) {
	r.acceptMu.Lock()
	defer r.acceptMu.Unlock()
	_, res, f, err := r.open(raw, now)
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
	kind   string
	bucket Bucket
	seq    uint64
	// issuedAt is the envelope's AAD-COVERED machine stamp, in unix millis. It is the only
	// authenticated machine time the phone ever sees, so it is what the skew monitor
	// brackets (PB-TIME-3) -- and it is carried on the frame rather than re-read from the
	// header later, so it can only be used for a frame the AEAD has already vouched for.
	issuedAt int64
	snapshot Snapshot
	reply    schema.Control
	record   schema.JournalRecord
	grant    []byte
	recon    schema.ReconcileRecord
	reseed   schema.JournalReseed
}

// errDiscardableFrame tags an item whose bytes cannot become useful on retry: malformed
// wire data, failed authentication, wrong direction, malformed authenticated plaintext, or
// an unknown kind. The original error remains wrapped for errors.Is. This tag is authored at
// each proof site instead of treating every open error as discardable at AcceptCommit; a new
// failure mode therefore defaults to ReceiptRetained until it is reviewed explicitly.
var errDiscardableFrame = errors.New("phonecore: discardable mailbox frame")

func discardableFrameError(err error) error {
	return fmt.Errorf("%w: %w", errDiscardableFrame, err)
}

// open parses, authenticates and seq-guards one envelope EXACTLY ONCE, then decodes its
// plaintext. res is non-nil once the frame authenticated, so a caller can report the TRUE
// gap even when the decode then fails.
func (r *MailboxRouter) open(raw []byte, now time.Time) (Bucket, *crypto.MailboxResult, inboundFrame, error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return Bucket{}, nil, inboundFrame{}, discardableFrameError(err)
	}
	b := Bucket{Sender: env.Header.SenderKeyID, Epoch: env.Header.EpochID}
	key, recv, _, _, _ := r.bound()
	// PB-TIME-2's bounded-age backstop.
	//
	// WHY IT LIVES AT THIS SEAM rather than on the receiver: internal/remote/crypto is
	// FROZEN, MailboxReceiver.maxAge has no setter and NewMailboxReceiver takes no
	// arguments, so the property is enforced here -- exactly as S7b enforced it at the
	// gateway seam one hop over.
	//
	// WHY IT IS BEFORE Accept: a refusal must advance NO high-water, or one retained frame
	// carrying an absurd seq silences the machine for the rest of the epoch -- no lease
	// confirmation, no op outcome, no journal. The receiver cannot be relied on for this.
	// Its own age check is the `maxAge > 0` branch, and maxAge is zero here BY CONSTRUCTION
	// (see above), so Accept does not reject a stale frame at all: it accepts it and
	// advances the high-water. Moving this check below Accept is caught by
	// TestS11ReplyAge_RejectionDoesNotPoisonTheStream, which then fails with the machine's
	// next live reply refused as ErrStaleSeq.
	//
	// The bound is ONE-SIDED. IssuedAt is AAD-covered, so the untrusted relay can only make
	// a frame OLDER, never newer, and bounding the future would refuse live traffic from a
	// machine whose clock runs fast (the same reasoning S7b pinned one hop over).
	//
	// A stale CLAIM belongs to the relay until the AEAD vouches for it, so the refusal
	// happens only after crypto.OpenMailbox authenticates the header -- a forgery is refused
	// as a forgery, not as stale. OpenMailbox does not touch the receiver, so this costs no
	// seq, and everything between it and Accept below may refuse freely.
	//
	// The open is now UNCONDITIONAL rather than only on the stale branch, because the
	// direction check below also needs the authenticated plaintext before the receiver moves.
	// Accept then decrypts the same envelope a second time; MailboxReceiver exposes no
	// pre-advance hook and crypto is FROZEN, so that second open is what the direction binding
	// costs.
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		return b, nil, inboundFrame{}, discardableFrameError(err)
	}
	if now.Sub(time.UnixMilli(env.Header.IssuedAt)) > InboundMaxAge {
		return b, nil, inboundFrame{}, crypto.ErrStaleAge
	}
	// DIRECTION (direction.go), and it must precede Accept for exactly the reason the age
	// check does -- a refusal that has already advanced the high-water is not a refusal. This
	// is the relay re-serving a command or keystroke it observed on the phone -> machine leg
	// back at the phone: it authenticates under the shared content key, and a keystroke carries
	// a seq thousands ahead of the reply stream (one Sequencer feeds commands and input), so
	// letting the receiver see it silences every machine reply for the epoch. It is also FRESH
	// by construction, so the age check above would never catch it.
	//
	// The tag is a POSITIVE match on this side: the phone's own seals are the only frames that
	// carry it, so the rule is exact and no machine fixture is disturbed.
	var dir struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(plain, &dir); err == nil && dir.Kind == kindPhoneToMachine {
		return b, nil, inboundFrame{}, discardableFrameError(ErrWrongDirection)
	}
	res, err := recv.Accept(key, env)
	if err != nil {
		return b, nil, inboundFrame{}, err
	}
	// Peek the discriminator on the AUTHENTICATED plaintext (never on cleartext header).
	var disc struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(res.Plaintext, &disc); err != nil {
		return b, res, inboundFrame{}, discardableFrameError(err)
	}
	f := inboundFrame{kind: disc.Kind, bucket: b, seq: env.Header.Seq, issuedAt: env.Header.IssuedAt}
	switch disc.Kind {
	case kindTerminalSnapshot:
		var sf snapshotFrame
		if err := json.Unmarshal(res.Plaintext, &sf); err != nil {
			return b, res, inboundFrame{}, discardableFrameError(err)
		}
		f.snapshot = Snapshot{
			Session: sf.Session, Lines: sf.Lines, Cols: sf.Cols, Rows: sf.Rows,
			SessionInstance: sf.SessionInstance, ViewEpoch: sf.ViewEpoch,
			Revision: sf.Revision, Reset: sf.Reset, RenderedAt: derefTime(sf.RenderedAt),
		}
	case kindCommandReply:
		var rf replyFrame
		if err := json.Unmarshal(res.Plaintext, &rf); err != nil {
			return b, res, inboundFrame{}, discardableFrameError(err)
		}
		f.reply = rf.Control
	case kindReconcile:
		// PB-SYNC-7: adopt the machine's authorities WHOLE or not at all -- a decode
		// failure leaves the router unreconciled (and still refusing mutating ops) rather
		// than half-applying a partial authority.
		var cf reconcileFrame
		if err := json.Unmarshal(res.Plaintext, &cf); err != nil {
			return b, res, inboundFrame{}, discardableFrameError(err)
		}
		f.recon = cf.ReconcileRecord
	case kindJournalReseed:
		// PB-SYNC-2's journal repair. Decoded WHOLE or not at all, for the reason the
		// reconcile arm gives one case up: a half-decoded snapshot applied over the live
		// cache is worse than no repair, because it clears the flag that says so.
		var rf reseedFrame
		if err := json.Unmarshal(res.Plaintext, &rf); err != nil {
			return b, res, inboundFrame{}, discardableFrameError(err)
		}
		f.reseed = rf.JournalReseed
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
			return b, res, inboundFrame{}, discardableFrameError(err)
		}
	default:
		// An unrecognised kind is NOT a journal record: swallowing it into the session
		// cache is exactly the C8 regression. Fail closed rather than mis-apply it.
		return b, res, inboundFrame{}, discardableFrameError(fmt.Errorf("phonecore: unrecognised mailbox frame kind %q", disc.Kind))
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
		if r.core != nil {
			// A command reply is the ONLY machine -> phone frame the phone can correlate to
			// a send it made, so it is where both of S11's time-and-lease consumers live:
			// PB-INPUT-2's lease lifecycle (the OpLease confirmation and the OpDetach
			// severance ride this kind) and PB-TIME-3's skew bracket. Both run here, AFTER
			// the AEAD has vouched for the frame -- reading either from an unauthenticated
			// header would make the untrusted relay the authority.
			r.core.leases.Apply(f.reply)
			// ADR-017 T6, the terminal control plane's half of the same frame -- and on its
			// OWN plane (OPEN-C4), which is why it is a second call and not a branch inside
			// the first. The instance comes from the session's own capability record rather
			// than from the reply, because the record is what the machine will re-check the
			// generation against per frame (T6-e): a generation the phone bound to a stale
			// instance is refused stale_instance, which is the answer that keeps a screen
			// from typing into the PTY that REPLACED the one it is showing.
			r.core.terminalControl.Apply(f.reply, r.core.sessionInstance(f.reply.SessionID), time.Now())
			// The verdict is deliberately dropped here: a skewed clock is not a reason to
			// refuse an INBOUND frame (the machine's stamp is the thing being measured).
			// The app reads it from SkewMonitor.Check in its reply handler and reports it on
			// the event plane -- NOT at a command-authoring site, where refusing on it would
			// stop the very command that re-measures the clock (PB-TIME-1, and the fence in
			// mobile/s11r_livesend_test.go).
			_, _ = r.core.skew.Observe(f.reply.OperationID, time.UnixMilli(f.issuedAt))
		}
	case kindReconcile:
		r.mu.Lock()
		r.recon, r.reconOK, r.reconAt = f.recon, true, f.bucket
		// A bare router has no coordinates to move and nothing to validate the record
		// against, so arrival IS adoption; with durable custody Core.Reconcile adopts.
		if r.core == nil {
			r.adopted = true
		}
		r.mu.Unlock()
	case kindJournalReseed:
		// PB-SYNC-8: the reseed REPLACES the cached set and the cursor. Merging it would
		// discard every roster record it carries -- they arrive with Cursor 0, deliberately
		// (internal/daemon/journal.go), and SessionCache.Apply drops anything below the
		// highest applied cursor -- so the designated repair channel would report success
		// and change nothing.
		sessions.reseed(f.reseed)
		// The TRANSCRIPT merges instead, and the difference is not an oversight. PB-SYNC-8's
		// replace rule is about a SET whose absent members have ended; a transcript is a
		// cursor-ordered log, and IS-CAP-4 lets the reseed's events half be CUT at a floor to
		// keep the repair inside one frame -- so replacing would delete history the phone
		// legitimately holds on every repair. The events half is also where IS-LIFE-3
		// re-delivers unresolved approval_requests, at their own cursors, which is why the
		// roster half is not read here at all: a roster record's cursor is deliberately zero
		// (PB-SYNC-8) and cannot be ordered against an approval_resolved.
		r.Items().applyAll(f.reseed.Events)
	case kindEpochGrant:
		r.grantMu.Lock()
		r.grants = append(r.grants, f.grant)
		r.grantMu.Unlock()
	case "":
		// IS-LAYER-1 forbids a new MAILBOX kind for an item, not a branch on the record's own
		// type inside the existing kind-less one: an interaction record IS a bare journal
		// record, routed by the existing journal path. It shapes the TRANSCRIPT and not the
		// roster (IS-SS-1) -- an item that marked its session Present would put a session on
		// the triage screen off the back of a tool call.
		if f.record.Type == RecordTypeInteraction {
			items := r.Items()
			items.Apply(f.record)
			// The read position still moves. Ordering is the journal cursor (IS-LAYER-3) and
			// the repair channel is the journal's own (IS-LAYER-4), so a record consumed here
			// and not folded into the roster must not leave Resync asking from behind it --
			// after ADR-009 items are the bulk of the stream, and the reseed that answered an
			// oversized range would be cut at IS-CAP-4's floor.
			sessions.AdvanceCursor(f.record.Cursor)
			return
		}
		if f.record.Type == RecordTypeStructuredGap {
			// A gap is a transcript boundary only. Runtime capabilities use the dedicated,
			// same-instance/delta-fenced capability_transition channel; reading capability
			// fields here would make the gap an unfenced second authority channel.
			r.Items().Apply(f.record)
			sessions.AdvanceCursor(f.record.Cursor)
			return
		}
		sessions.Apply(f.record)
	}
}

// ReceiptDisposition tells a mailbox drain whether an item that returned an error may be
// skipped while sweeping the rest of the relay page. The distinction cannot be reconstructed
// from error types at the transport seam: parse/auth/decode refusals are discardable hostile
// input, while stale-age/custody refusals and a failed durable commit retain recoverable
// evidence whose later cursors must not advance past it.
type ReceiptDisposition uint8

const (
	// ReceiptRetained is deliberately zero: a new error return that does not make an
	// affirmative skip decision must stop the page rather than silently advance past it.
	ReceiptRetained ReceiptDisposition = iota
	ReceiptDiscardable
)

// Receipt reports what became of one committed frame: whether its seq revealed a GAP,
// whether the relay was acked for it, and -- when an error is returned -- whether the item
// must remain ahead of all later cursors.
type Receipt struct {
	Gap         bool
	Acked       bool
	Disposition ReceiptDisposition
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
	return r.AcceptCommitAt(raw, cursor, time.Now())
}

// AcceptCommitAt is AcceptCommit with the age clock injected, so PB-TIME-2's bound -- and the
// RECOVERY from a phone clock that was wrong -- are testable without waiting ten minutes or
// moving the host's clock. Production reads through AcceptCommit, which passes time.Now().
func (r *MailboxRouter) AcceptCommitAt(raw []byte, cursor uint64, now time.Time) (Receipt, error) {
	r.acceptMu.Lock()
	defer r.acceptMu.Unlock()

	// PB-KEY-10, and it must come BEFORE ParseEnvelope. The machine's bootstrap grant is a
	// TAGGED PLAINTEXT frame, not a ContentKey-sealed envelope -- deliberately, because it is
	// what DELIVERS the ContentKey -- so the envelope parser refuses it, commits nothing and
	// acks nothing, and the relay cursor never advances past it. Every later frame then sits
	// behind it unreachable for the whole 7-day retention window while the drain re-reads one
	// page forever.
	//
	// It rides THIS path rather than a facade verb because the custody surface is inbound-only
	// by design (ADR-007 B8) and the golden-pinned facade has no verb that ingests a grant --
	// so the phone's ONE inbound path is the only place the key can arrive. Everything the
	// open needs is already here: the device KeyStore, the pinned machine grant-signing pub,
	// and the replay watermark seeded from durable state.
	if g, ok := grantwire.ParseBootstrap(raw); ok {
		return r.acceptBootstrap(g, cursor)
	}
	b, res, f, err := r.open(raw, now)
	if err != nil {
		if errors.Is(err, crypto.ErrStaleSeq) {
			// ALREADY APPLIED. The content is on disk behind the durable high-water, so the
			// relay's copy is redundant and the ack destroys nothing. It is the idempotent
			// half of the retry: without it the phone re-reads the same item on every drain
			// for the whole retention window and the mailbox never compacts (PB-SYNC-6).
			return Receipt{Acked: r.ack(cursor) == nil, Disposition: ReceiptDiscardable}, err
		}
		if errors.Is(err, crypto.ErrStaleAge) {
			// open authenticated the envelope before returning ErrStaleAge. If durable
			// custody already covers its authenticated seq, this is a restored replay,
			// not content the phone refused only because its clock may be wrong. Compacting
			// that redundant copy is safe and lets an explicit relay-cursor rewind cross
			// an old full page instead of stalling behind the age backstop.
			if r.core != nil {
				if env, parseErr := crypto.ParseEnvelope(raw); parseErr == nil && r.core.State().Receive[b] >= env.Header.Seq {
					return Receipt{Acked: r.ack(cursor) == nil, Disposition: ReceiptDiscardable}, err
				}
			}
			// PAST PB-TIME-2's BOUND, AND THEREFORE NEVER ACKED. This branch used to share
			// the ack above on the reasoning that the frame is "never usable" -- and that
			// reasoning is false in the one case that matters. The bound compares the
			// machine's authenticated stamp against THIS PHONE's clock, so a phone running
			// eleven minutes fast reads every freshly-sealed frame as past it. The frame is
			// intact; the phone's reading of it is not. Acking it told the relay to compact
			// away the only copy, so the phone destroyed its entire inbound plane as it
			// arrived and correcting the clock recovered nothing (ADR-007 B42).
			//
			// It is not a clock bug either. The relay is the declared adversary (ADR-007 D9)
			// and it schedules delivery: withholding for ten minutes and then releasing puts
			// every released frame past the bound. Acking made that silent, permanent content
			// destruction PERFORMED BY THE VICTIM.
			//
			// THE COST IS A STALL, AND IT IS THE RIGHT TRADE. An un-acked item is never
			// compacted, so the drain re-reads it until the relay's own retention cap (§6.0,
			// 7 d) drops it -- the same bounded stall an unopenable frame already causes. A
			// stall is recoverable (the frame is still there when the clock is fixed) and it
			// is loud (InboundAgeRefused stops the phone reading "online"); a deletion is
			// neither. Nothing is persisted here: a fail-closed refusal commits no content.
			r.markAgeRefused(true)
			return Receipt{Disposition: ReceiptRetained}, err
		}
		gap := false
		if res != nil {
			gap = res.Gap
		}
		disposition := ReceiptRetained
		if errors.Is(err, errDiscardableFrame) {
			disposition = ReceiptDiscardable
		}
		return Receipt{Gap: gap, Disposition: disposition}, err
	}
	// A frame the phone TOOK is proof the inbound plane works, so the refusal condition
	// clears with it. It is cleared here rather than latched until something explicitly
	// resets it: latching would leave a phone reporting itself broken for the life of the
	// process over one straggler, which is the brick PB-STATE-10 forbids.
	r.markAgeRefused(false)
	if r.core == nil {
		r.apply(f)
		return Receipt{Gap: res.Gap, Acked: true}, nil
	}
	// One Save covers the guard AND the content; a frame that left a hole in the bucket is
	// delivered but NOT trusted into the durable model -- the phone resyncs (or reconciles)
	// that stream first. See commitReceive.
	contiguous, streams, err := r.core.commitReceive(b, f, cursor, now)
	if err != nil {
		// MailboxReceiver.Accept advanced its in-memory guard before the durable transaction
		// failed. Restore every bucket from the unchanged durable state so this same process
		// can retry the retained frame; otherwise the retry is mislabeled ErrStaleSeq and
		// acked even though its content never committed.
		r.restoreReceiveGuards(r.core.State())
		return Receipt{Gap: res.Gap, Disposition: ReceiptRetained}, err
	}
	gap := res.Gap || !contiguous
	if !contiguous {
		r.markStale(b)
	}
	// The per-channel flags are adopted from what the transaction COMMITTED, never computed
	// again here. PB-SYNC-3 is fail-closed both ways: a repair whose Save did not land
	// returned above with the flag untouched, and one that did land cleared it in the same
	// write that carried its content and its watermark.
	r.adoptStaleStreams(streams)
	r.apply(f)
	if err := r.ack(cursor); err != nil {
		return Receipt{Gap: gap, Disposition: ReceiptDiscardable}, err
	}
	return Receipt{Gap: gap, Acked: true}, nil
}

func (r *MailboxRouter) restoreReceiveGuards(st State) {
	recv := crypto.NewMailboxReceiver()
	for b, seq := range st.Receive {
		recv.SeedHighWater(b.Sender, b.Epoch, seq)
	}
	r.mu.Lock()
	r.recv = recv
	r.mu.Unlock()
}

// acceptBootstrap consumes the machine's tagged plaintext epoch-grant frame: verify it
// against the pinned machine grant-signing key and the durable replay watermark, install the
// epoch keys, and commit both with the relay read cursor in ONE transaction.
//
// An UNOPENABLE grant is still acked, for the same reason a stale seq is: delivery is
// at-least-once (deliver.go appends once per gateway session) and an untrusted relay can
// re-serve a retained pre-rotation grant, so the phone WILL see grants it must refuse -- and
// one that is never compacted pins the drain on its page forever, which is the denial lever
// PB-SYNC-6 forbids. Nothing is persisted on that path: a refusal commits no key.
//
// A grant that OPENED but whose commit failed is deliberately NOT acked. That is the ack
// ordering AcceptCommit states one function down -- acking a frame whose content is not yet
// durable lets the relay compact the only copy a SIGKILL is about to erase -- and it bites
// hardest here: this frame arrives ONCE per gateway session and it is the only thing that
// carries the epoch key, so dropping it leaves a phone that can neither send nor open
// anything, with nothing left to redeliver.
//
// A CUSTODY REFUSAL IS NOT AN UNOPENABLE GRANT, and conflating the two is the same loss one
// step earlier. The sealed-box open runs through the device's CONTENT tier, so
// crypto.ErrKeyAuthRequired -- the locked handset -- surfaces here as a grant that "did not
// open", when in fact the frame is intact and will open the moment the tier does. That state
// is not an error path: openSealedDeviceKeys tolerates a locked content tier ON PURPOSE so
// the wake tier keeps the relay dialled and the drain running with nobody present, which is
// exactly when the bootstrap frame arrives. Acking it there deletes the only copy of the
// epoch key for a reason that clears itself. crypto.ErrKeyInvalidated is treated the same
// way: it is a verdict on this device's tier, never on the grant, and nothing behind the
// frame can be opened either -- so there is no compaction to buy.
func (r *MailboxRouter) acceptBootstrap(g *crypto.EpochGrant, cursor uint64) (Receipt, error) {
	if r.core == nil {
		// A bare router holds no key custody and no watermark, so it cannot authenticate a
		// grant at all. Fail closed rather than route the sealed bytes somewhere hopeful.
		return Receipt{}, errNoGrantCustody
	}
	opened, err := r.core.installGrant(g, cursor)
	if err != nil && (opened || isCustodyRefusal(err)) {
		return Receipt{Disposition: ReceiptRetained}, err
	}
	acked := r.ack(cursor) == nil
	return Receipt{Acked: acked, Disposition: ReceiptDiscardable}, err
}

// isCustodyRefusal reports whether err is one of the two PB-KEY-2 tier verdicts, which say
// nothing about the frame that provoked them and are answered by the user (unlock) or by a
// re-pair -- never by discarding the frame.
func isCustodyRefusal(err error) bool {
	return errors.Is(err, crypto.ErrKeyAuthRequired) || errors.Is(err, crypto.ErrKeyInvalidated)
}

// InboundAgeRefused reports that the phone is DISCARDING the frames the machine sends it,
// because their authenticated age is past PB-TIME-2's bound as this phone reads its own clock.
//
// It is the health signal for a condition the transport cannot see: the websocket is up, so
// the connection state machine has nothing to say, and a phone that answers "online" while
// deleting its inbound plane is reporting the destruction as health (ADR-007 B42). It is LIVE
// -- raised by a refusal, cleared by the next frame the phone takes -- because it describes
// the drain that is happening now and must not outlive it.
func (r *MailboxRouter) InboundAgeRefused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ageRefused
}

func (r *MailboxRouter) markAgeRefused(refused bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ageRefused = refused
}

// markStale mirrors the persisted stale flag into the live router.
func (r *MailboxRouter) markStale(b Bucket) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stale[b] = true
}

// StreamStale reports whether one REPAIR CHANNEL's content may not be trusted (PB-SYNC-1).
//
// MARKING is per BUCKET: a gap in the shared (sender, epoch) space marks BOTH journal and
// terminal, conservatively, because the Gap bit carries no kind. A gap in the sender-zero
// command-reply space marks only reply.
//
// CLEARING is per CHANNEL and only ever by that channel's own repair, committed atomically
// with the matching transport watermark (PB-SYNC-3). A failed repair stays stale.
//
// It is deliberately NOT MailboxRouter.Stale's unadopted-epoch clause. That one answers "is
// this BUCKET's content verified against an authority", which is false for every bucket of
// an epoch with no reconcile record; this one answers "does this CHANNEL have a known hole",
// which a phone that has simply not reconciled yet does not.
func (r *MailboxRouter) StreamStale(stream string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.staleStr[stream]
}

// adoptStaleStreams mirrors the committed per-channel flags into the live router. The
// durable set is authoritative: it is what the marking and the clearing were both computed
// into, inside the one Save that carried the frame.
func (r *MailboxRouter) adoptStaleStreams(streams map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.staleStr = maps.Clone(streams)
	if r.staleStr == nil {
		r.staleStr = map[string]bool{}
	}
}

// ack releases the relay item; a router with no Core (or a Core with no Acker) manages no
// mailbox and has nothing outstanding.
func (r *MailboxRouter) ack(cursor uint64) error {
	if r.core == nil {
		return nil
	}
	return r.core.ackCursor(cursor)
}
