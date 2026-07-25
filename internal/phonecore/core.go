package phonecore

// Resume is the phone's process-start entry point (PB-STATE-1/-2): it reads the ONE durable
// State blob and assembles every component whose in-memory counter would otherwise restart
// at zero after the OS kills the app -- the send-seq allocator, the receive replay guard,
// the grant watermark, the decoded caches and the offline op queue.
//
// There is NO Close. An Android process is SIGKILLed, never shut down cleanly, so nothing
// here may depend on a graceful exit: every durable write happens on the path that needs it.

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// Acker releases one relay mailbox item so the relay may compact it. It is INJECTED because
// the relay client is outside the bound dependency closure (PB-BIND-0): phonecore must not
// import it.
type Acker interface {
	Ack(cursor uint64) error
}

// Config assembles a Core. Dir is the phone's state directory (device keys + state blob);
// Machine is the endpoint id the durable coordinates must belong to (empty adopts whatever
// the blob describes); State overrides the file-backed custody (test wiring); Ack releases
// consumed relay items.
//
// WakeSealer and ContentSealer are PB-KEY-2's two tier KEKs, held outside the Go core
// (PB-KEY-9). Both are REQUIRED whenever anything is written at rest -- Resume fails with
// ErrNoSealer rather than writing key material in the clear (ADR-007 B18(c)). They are
// separate because one KEK over both tiers is the collapse the plaintext files already had:
// the wake KEK opens with no user present, so anything under it is reachable without the
// biometric the content tier exists to require.
type Config struct {
	Dir           string
	Machine       string
	State         Store
	Ack           Acker
	WakeSealer    Sealer
	ContentSealer Sealer
}

// Core is the assembled phone: durable custody plus the components resumed from it.
type Core struct {
	store Store
	ks    crypto.KeyStore
	ack   Acker

	seq    *Sequencer
	router *MailboxRouter
	ops    *OpQueue
	// leases and skew are DELIBERATELY NOT resumed from the durable blob and are not
	// rebound by Save: a lease IS a live daemon connection, so one restored from disk names
	// a connection that cannot exist (PB-INPUT-2), and a skew measurement is only as good
	// as the round trip that produced it.
	leases *LeaseState
	skew   *SkewMonitor

	mu     sync.Mutex
	st     State
	grants *crypto.GrantReceiver

	// rebindMu serialises ONE rebind's read of the durable state with its application to the
	// derived components. mu cannot do that job: it is released between the two, and every
	// component rebind feeds takes its own lock, so holding mu across them would put mu above
	// locks that callers already take below it.
	//
	// Without it the two halves of two rebinds interleave, and PB-KEY-7 is the direction that
	// matters: a Save whose rebind read PRE-purge state applies after the purge's, leaving
	// MailboxRouter bound to the content key the purge destroyed -- after PurgeKeys has
	// returned and every writer has finished, with the screen locked. That is the memory half
	// PB-KEY-7 lists FIRST, and it is the same race round 3 closed on the durable side; the
	// argument is the one that fix already made, because PurgeKeys arrives from an Android
	// lifecycle callback on another thread.
	//
	// LOCK ORDER is rebindMu -> mu, never the reverse. rebind is only ever entered with mu
	// RELEASED (PurgeKeys unlocks before it, Save's persist has returned), and no path holding
	// mu takes rebindMu -- which is what makes the ordering total and the deadlock impossible.
	rebindMu sync.Mutex
}

// Resume opens the durable state and rebuilds every resume-critical component from it. It
// fails closed: an unreadable blob or unreadable key custody is an error, never a silent
// start from zero.
func Resume(cfg Config) (*Core, error) {
	store := cfg.State
	if store == nil {
		path := ""
		if cfg.Dir != "" {
			path = filepath.Join(cfg.Dir, StateFileName)
		}
		var err error
		if store, err = OpenStore(path, cfg.Machine, cfg.WakeSealer, cfg.ContentSealer); err != nil {
			return nil, err
		}
	}
	ks, err := openKeyStore(cfg.Dir, cfg.WakeSealer, cfg.ContentSealer)
	if err != nil {
		return nil, err
	}
	c := &Core{
		store:  store,
		ks:     ks,
		ack:    cfg.Ack,
		seq:    &Sequencer{},
		ops:    NewOpQueue(0),
		leases: NewLeaseState(),
		skew:   NewSkewMonitor(time.Now),
		st:     store.Load().clone(),
	}
	c.router = newMailboxRouter(crypto.ContentKey{}, c)
	c.rebind()
	return c, nil
}

// KeyStore is the device's key custody (never regenerated across a restart).
func (c *Core) KeyStore() crypto.KeyStore { return c.ks }

// Seq is the durable phone -> machine send-seq allocator.
func (c *Core) Seq() *Sequencer { return c.seq }

// Router is the machine -> phone receive path (demux + replay guard + durable commit).
func (c *Core) Router() *MailboxRouter { return c.router }

// Grants is the epoch-grant receiver, seeded at the persisted watermark so a relay cannot
// replay an old correctly-signed grant after a restart (crypto/epoch.go).
func (c *Core) Grants() *crypto.GrantReceiver {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.grants
}

// Ops is the offline mutating-op queue, restored from State.PendingOps.
func (c *Core) Ops() *OpQueue { return c.ops }

// Leases is the control-lease gate (PB-INPUT-2), fed automatically from the authenticated
// inbound path. It starts EMPTY on every Resume: a lease cannot survive a process death,
// so the only correct durable representation of one is none at all.
func (c *Core) Leases() *LeaseState { return c.leases }

// SkewMonitor is the clock-skew estimator (PB-TIME-1/-3), fed the AAD-covered IssuedAt of
// every command reply the inbound path authenticates.
func (c *Core) SkewMonitor() *SkewMonitor { return c.skew }

// State returns a copy of the durable state as it currently stands.
func (c *Core) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st.clone()
}

// Save adopts st as the phone's durable state and REBINDS every component derived from it
// (send-seq epoch, grant watermark, epoch content key, receive high-waters, caches, op
// queue). It is how the app records a pairing, an accepted grant or an epoch rotation: a
// Save that persisted the coordinates but left the live objects on the old epoch would keep
// reserving seqs against a stream that no longer exists.
func (c *Core) Save(st State) error {
	if err := c.persist(st); err != nil {
		return err
	}
	c.rebind()
	return nil
}

// PurgeKeys is PB-KEY-7's lock purge at the durable layer: the epoch keys go, the sealed
// blobs with them, and so does every DECRYPTED cache they protected. It REBINDS afterwards
// for the same reason Save does -- the live objects must come off the purged epoch, or the
// router keeps opening frames under a key the phone no longer holds.
//
// It is a distinct verb from Save because a Save whose keys are zero is ambiguous: the wake
// path holds zeros for a content key it could not read and Saves constantly, so custody
// cannot tell "nothing to write" from "destroy this" by looking at the bytes (see
// Store.PurgeKeys).
//
// The durable error is REPORTED but never short-circuits the rest: custody purges its own
// memory unconditionally, so adopting and rebinding is what carries that through to the live
// objects. Returning early on a failed write left the keys in c.st and bound in the router
// with the screen locked -- the half of PB-KEY-7 that cannot fail, gated behind the half that
// can.
func (c *Core) PurgeKeys() error {
	c.mu.Lock()
	err := c.store.PurgeKeys()
	c.st = c.store.Load().clone()
	c.mu.Unlock()
	c.rebind()
	return err
}

// persist writes st through custody and adopts whatever custody made of it (the file store
// merges the replay guards monotonically). A failed write leaves the in-memory copy
// untouched, so nothing is claimed that is not durable.
func (c *Core) persist(st State) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.persistLocked(st)
}

func (c *Core) persistLocked(st State) error {
	if err := c.store.Save(st.clone()); err != nil {
		return err
	}
	c.st = c.store.Load().clone()
	return nil
}

// testHookRebindRead, when non-nil, is invoked inside rebind once the durable state has been
// read and before any derived component has been touched. That gap is the whole subject of
// the ordering rule above, and nothing observable from outside the package can be parked in
// it: the seam exists so the interleaving is DRIVEN rather than raced. Always nil in
// production (mirroring internal/shim's testHookAfterSignalArm).
var testHookRebindRead func()

// rebind rebuilds the derived components from the current durable state. The state is read
// and applied under rebindMu so no other rebind can land between the two -- see the field's
// own comment for why that ordering is PB-KEY-7's, and why it cannot deadlock.
func (c *Core) rebind() {
	c.rebindMu.Lock()
	defer c.rebindMu.Unlock()

	st := c.State()
	if testHookRebindRead != nil {
		testHookRebindRead()
	}

	c.mu.Lock()
	if st.GrantEpoch != 0 || st.GrantSeq != 0 {
		c.grants = crypto.NewGrantReceiverAt(st.GrantEpoch, st.GrantSeq)
	} else {
		c.grants = crypto.NewGrantReceiver()
	}
	c.mu.Unlock()

	c.seq.bind(st.EpochID, st.SendSeq[st.EpochID], c.reserveSendSeq)
	c.router.rebind(st)
	c.ops.reset(st.PendingOps)
}

// reserveSendSeq persists a send-seq reservation ceiling for one epoch (PB-STATE-3). It is
// the Sequencer's only durable dependency: on failure NO seq is issued, because handing one
// out that was never durably reserved is precisely how a seq gets reused across a restart.
func (c *Core) reserveSendSeq(epoch uint32, ceiling uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.st.clone()
	if st.SendSeq == nil {
		st.SendSeq = map[uint32]uint64{}
	}
	if ceiling <= st.SendSeq[epoch] {
		return nil
	}
	st.SendSeq[epoch] = ceiling
	return c.persistLocked(st)
}

// ErrGrantLost is PB-KEY-3's DELIVERY loss, and it is deliberately a DIFFERENT error from
// the two custody refusals in internal/remote/crypto:
//
//   - crypto.ErrKeyAuthRequired  -- the key is here, the user must authenticate. "Unlock."
//   - crypto.ErrKeyInvalidated   -- this device's custody is gone. "Re-pair."
//   - ErrGrantLost               -- custody is fine; the MACHINE's grant never arrived, or
//     the relay purged it before the phone woke. "The machine must re-grant."
//
// Collapsing the third into the second sends a user with a perfectly good handset through a
// re-pair that BeginPairing refuses outright while a device is registered -- PB-STATE-10's
// brick, exitable only by physical access to the machine. Three remedies, three identities,
// and only one of them is the user's to perform.
var ErrGrantLost = errors.New("phonecore: no epoch grant was ever delivered and none is recoverable from the relay; the machine must re-grant")

// errNoGrantCustody refuses a bootstrap grant on a router with no durable custody: there is
// no key store to open it with and no watermark to refuse a replay against.
var errNoGrantCustody = errors.New("phonecore: a router without durable custody cannot authenticate an epoch grant")

// StreamStale reports whether one repair channel's content may not be trusted (PB-SYNC-1),
// for a caller that holds the Core rather than the router.
func (c *Core) StreamStale(stream string) bool { return c.router.StreamStale(stream) }

// MarkGrantLost records PB-KEY-3's terminal state durably: the phone has drained its
// mailbox, found no grant it can open, and the relay can no longer be holding one (the
// retention cap has passed). Without it the phone has no state at all for this -- every send
// simply fails with errNoContentKey, which is the indefinite decrypt-failure loop PB-KEY-3
// forbids and is indistinguishable on screen from a custody refusal with a completely
// different remedy.
//
// It is RECOVERABLE, never latched (PB-STATE-10): the machine's re-grant clears it on the
// ordinary inbound path, in the same transaction that installs the key.
func (c *Core) MarkGrantLost() error {
	c.mu.Lock()
	st := c.st.clone()
	if st.StaleStreams == nil {
		st.StaleStreams = map[string]bool{}
	}
	st.StaleStreams[StreamGrant] = true
	err := c.persistLocked(st)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	c.rebind()
	return nil
}

// installGrant consumes one machine-sealed bootstrap EpochGrant (PB-KEY-10): it verifies the
// grant against the pinned machine grant-signing key and the durable replay watermark, then
// commits the epoch keys, the epoch id, the advanced watermark and the relay read cursor in
// ONE transaction and rebinds every component derived from them.
//
// The WATERMARK IS THE WHOLE REPLAY DEFENCE and it must be persisted WITH the keys. Delivery
// is at-least-once and the relay is the declared adversary: it can re-serve a retained
// pre-rotation grant, which -- accepted -- would rewind the phone onto a content key a
// revoked device may still hold. crypto.GrantReceiver enforces strict (epoch, seq)
// monotonicity, and Resume seeds it from exactly these two fields, so a watermark that did
// not reach disk means the next process death re-opens the hole.
//
// The REBIND is not bookkeeping either: the router must come onto the granted key inside the
// same act that installed it, or every frame the machine sealed under the new epoch is
// undecodable and the drain makes no progress behind the grant it just consumed.
//
// It reports whether the grant OPENED, which is what tells the caller whether to ack. A
// grant that could not be opened -- a replay, a forged signature, a seal for another device
// -- can never become usable no matter how often it is re-read, so acking it is the only way
// the mailbox ever compacts it. One that opened but could not be COMMITTED is a transient
// durable failure, and acking THAT would let the relay drop the only copy of the one frame
// that carries this epoch's key: it is delivered once per gateway session, so the phone would
// come back from the SIGKILL with no key and nothing left to deliver one.
func (c *Core) installGrant(g *crypto.EpochGrant, cursor uint64) (opened bool, err error) {
	c.mu.Lock()
	epoch, seq, keys, err := c.grants.Accept(c.ks, ed25519.PublicKey(c.st.MachineSignPub), g)
	if err != nil {
		c.mu.Unlock()
		return false, err
	}
	st := c.st.clone()
	st.EpochID, st.Keys = epoch, keys
	st.GrantEpoch, st.GrantSeq = epoch, seq
	if cursor > st.RelayCursor {
		st.RelayCursor = cursor
	}
	// The grant channel's repair has landed: PB-KEY-3's terminal state is recoverable, and
	// this is the only thing that recovers it.
	delete(st.StaleStreams, StreamGrant)
	err = c.persistLocked(st)
	c.mu.Unlock()
	if err != nil {
		return true, err
	}
	c.rebind()
	return true, nil
}

// RecordOutcome persists the durable outcome of one mutating op (PB-SYNC-2): it is what
// resolves an op whose reply the phone may never see, so an operation gap does not leave the
// phone guessing. An UNTAGGED outcome is refused -- attributing it to some op by proximity
// would persist the wrong verdict for a mutating op.
func (c *Core) RecordOutcome(ctrl schema.Control) error {
	if ctrl.OperationID == "" {
		return errors.New("phonecore: outcome carries no operation id")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.st.clone()
	if st.OpOutcomes == nil {
		st.OpOutcomes = map[string]schema.Control{}
	}
	st.OpOutcomes[ctrl.OperationID] = ctrl
	return c.persistLocked(st)
}

// UnresolvedOps are the queued ops whose durable outcome has never landed. After a burned
// seq gap (PB-STATE-8) the phone cannot know whether they reached the daemon, so they stay
// unresolved until an outcome is recorded.
func (c *Core) UnresolvedOps() []QueuedOp {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []QueuedOp
	for _, op := range c.st.PendingOps {
		if _, done := c.st.OpOutcomes[op.Cmd.OperationID]; !done {
			out = append(out, op)
		}
	}
	return out
}

// Reconcile adopts the machine's reconcile record (PB-STATE-4/PB-SYNC-7) into EVERY
// coordinate a rollback could have moved, and persists the result before touching the live
// objects -- a repair that lived only in memory would be undone by the next process death.
//
// The record is refused unless it names THIS phone's machine and epoch: a relay retaining a
// pre-rotation record can re-serve it under the retained content key, and its stale
// InboundHighWater -- adopted monotonically, hence unrewindable -- would push the send-seq
// into a range the new epoch's gateway stream has never seen.
//
// Until a record is adopted, RequireReconciled refuses mutating ops. The refusal is
// RECOVERABLE, never latched (PB-STATE-10).
func (c *Core) Reconcile() error {
	rec, bucket, ok := c.router.reconcileRecord()
	if !ok {
		return ErrUnreconciled
	}

	c.mu.Lock()
	st := c.st.clone()
	if st.Machine != "" && rec.Machine != st.Machine {
		c.mu.Unlock()
		return fmt.Errorf("phonecore: reconcile record names machine %q, not %q", rec.Machine, st.Machine)
	}
	if st.EpochID != 0 && rec.EpochID != st.EpochID {
		c.mu.Unlock()
		return fmt.Errorf("phonecore: reconcile record names epoch %d, not %d", rec.EpochID, st.EpochID)
	}

	reply := Bucket{Epoch: rec.EpochID} // sender-zero: the command-reply bucket
	// (a) send-seq, (b) both receive high-waters, (c) the grant watermark. All monotonic:
	// an authority may only ever raise a guard.
	if st.SendSeq == nil {
		st.SendSeq = map[uint32]uint64{}
	}
	if rec.InboundHighWater > st.SendSeq[rec.EpochID] {
		st.SendSeq[rec.EpochID] = rec.InboundHighWater
	}
	if st.Receive == nil {
		st.Receive = map[Bucket]uint64{}
	}
	if rec.JournalCeiling > st.Receive[bucket] {
		st.Receive[bucket] = rec.JournalCeiling
	}
	if rec.ReplyCeiling > st.Receive[reply] {
		st.Receive[reply] = rec.ReplyCeiling
	}
	if rec.GrantEpoch > st.GrantEpoch || (rec.GrantEpoch == st.GrantEpoch && rec.GrantSeq > st.GrantSeq) {
		st.GrantEpoch, st.GrantSeq = rec.GrantEpoch, rec.GrantSeq
	}
	// The BUCKETS the authorities cover are no longer unverified. State.StaleStreams is
	// deliberately NOT cleared with them: a reconcile record republishes coordinates, not the
	// frames the phone missed, and PB-SYNC-2 clears a repair channel only on that channel's
	// own repair. Clearing here would present the content of the hole as live the moment the
	// gateway reconnected -- which is every reconnect.
	for b := range st.Stale {
		if b.Epoch == rec.EpochID {
			delete(st.Stale, b)
		}
	}
	if err := c.persistLocked(st); err != nil {
		c.mu.Unlock()
		return err
	}
	c.grants = crypto.NewGrantReceiverAt(st.GrantEpoch, st.GrantSeq)
	c.mu.Unlock()

	c.seq.SeedFrom(rec.InboundHighWater)
	c.router.adopt(st, bucket, reply, rec)
	return nil
}

// commitReceive is the WHOLE receive transaction (PB-STATE-7), in ONE durable Save: the
// replay guard -- the bucket's high-water, the stale flags and the relay read cursor --
// together with the DECODED CONTENT of the frame, all made durable BEFORE the relay is
// acked. The two halves cannot be split around the ack in either direction. Acking first
// would let the relay compact the only copy of a frame the phone has recorded only in
// memory, and a SIGKILL in that window loses it for good: the durable high-water has moved,
// so the redelivery is refused with crypto.ErrStaleSeq and the reply (or the session's
// exited record) is simply gone. Committing the guard without the content has the same
// effect. Persisting the content first and the guard after would instead let the redelivery
// be applied a second time.
//
// It reports whether the frame is CONTIGUOUS with what the phone has durably recorded on
// that bucket (seq == high-water + 1). A frame that is not leaves a HOLE: the phone never
// saw the frames in between and cannot know what they said, so the bucket is marked stale
// and only the guard is committed -- folding the content of a stream with holes into the
// durable model is exactly the "later state trusted before reconciliation" PB-STATE-8
// forbids. The content is still delivered in memory (nothing is dropped from the live
// view), and the durable model catches up on the next contiguous frame.
//
// crypto.MailboxResult.Gap cannot answer this: a receiver reports no gap on the FIRST frame
// of a stream (seen == false), which is precisely the post-restart case.
// It also carries PB-SYNC-1's per-CHANNEL staleness through the SAME write, which is what
// makes PB-SYNC-3's "committed atomically with the matching transport watermark" true rather
// than asserted: a hole stales every channel this bucket carries, a contiguous repair clears
// the one channel its kind repairs, and both land in the one Save that moves the high-water.
// A failed Save therefore leaves the flags exactly as they were -- the repair is repeatable,
// and the phone never comes back believing it is fresh over content it never recorded.
func (c *Core) commitReceive(b Bucket, f inboundFrame, cursor uint64) (contiguous bool, streams map[string]bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.st.clone()
	if st.Receive == nil {
		st.Receive = map[Bucket]uint64{}
	}
	contiguous = f.seq == st.Receive[b]+1
	if st.StaleStreams == nil {
		st.StaleStreams = map[string]bool{}
	}
	if !contiguous {
		if st.Stale == nil {
			st.Stale = map[Bucket]bool{}
		}
		st.Stale[b] = true
		for _, s := range streamsOf(b) {
			st.StaleStreams[s] = true
		}
	} else if s := repairedBy(f.kind); s != "" {
		// The repair is only a repair if it is CONTIGUOUS: a reseed that itself arrived
		// behind a hole says nothing about the frames in that hole, and clearing on it would
		// be the optimistic clear PB-SYNC-3 forbids one level up.
		delete(st.StaleStreams, s)
	}
	if f.seq > st.Receive[b] {
		st.Receive[b] = f.seq
	}
	if cursor > st.RelayCursor {
		st.RelayCursor = cursor
	}
	if contiguous {
		c.foldContent(&st, f)
	}
	if err := c.persistLocked(st); err != nil {
		return contiguous, nil, err
	}
	return contiguous, maps.Clone(c.st.StaleStreams), nil
}

// foldContent folds the decoded frame into the state the transaction is about to write, so
// the phone does not lose the content of a frame its high-water will now refuse on
// redelivery. It runs BEFORE the live caches are mutated -- a commit that fails must leave
// nothing behind (S2's rule: the cache mutation is part of the transaction, not before it)
// -- so the journal branch folds the record into a COPY of the session cache rather than
// reading the live one.
func (c *Core) foldContent(st *State, f inboundFrame) {
	switch f.kind {
	case kindTerminalSnapshot:
		st.Snapshots = upsertSnapshot(st.Snapshots, f.snapshot)
	case kindCommandReply:
		if f.reply.OperationID == "" {
			return // unattributable: Take still drains it in memory, nothing is silently mis-keyed
		}
		if st.OpOutcomes == nil {
			st.OpOutcomes = map[string]schema.Control{}
		}
		st.OpOutcomes[f.reply.OperationID] = f.reply
	case kindJournalReseed:
		// PB-SYNC-8: REPLACE, never merge. The durable list is computed the same way the
		// live cache will be (a scratch cache the reseed is applied to), so the two cannot
		// disagree once MailboxRouter.apply mirrors it.
		scratch := NewSessionCache()
		scratch.reseed(f.reseed)
		st.Sessions = sortedSessions(scratch)
	case "":
		st.Sessions = sessionsWith(c.router.Sessions(), f.record)
	}
	// Grants, reserved kinds and the reconcile record carry no cache state.
}

// sessionsWith folds rec into a COPY of the live session cache and returns the durable
// list. The copy carries the cache's own stale-cursor guard across (SessionCache.Apply
// refuses a record older than the highest applied one), so the committed list is what the
// live cache will hold once the transaction lands and MailboxRouter.apply mirrors it.
func sessionsWith(live *SessionCache, rec schema.JournalRecord) []CachedSession {
	scratch := NewSessionCache()
	live.mu.Lock()
	maps.Copy(scratch.sessions, live.sessions)
	scratch.cursor = live.cursor
	live.mu.Unlock()
	scratch.Apply(rec)
	return sortedSessions(scratch)
}

// sortedSessions is a cache's contents in a stable order, so the durable blob is
// byte-stable across rewrites (Go map iteration is randomized).
func sortedSessions(c *SessionCache) []CachedSession {
	out := c.List()
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

// upsertSnapshot replaces the cached grid for a session (latest wins), matching
// SnapshotCache.Apply.
func upsertSnapshot(snaps []Snapshot, s Snapshot) []Snapshot {
	for i := range snaps {
		if snaps[i].Session == s.Session {
			snaps[i] = s
			return snaps
		}
	}
	return append(snaps, s)
}

// ackCursor releases one consumed relay item. A Core with no Acker manages no relay
// mailbox, so there is nothing outstanding to release.
func (c *Core) ackCursor(cursor uint64) error {
	if c.ack == nil {
		return nil
	}
	return c.ack.Ack(cursor)
}
