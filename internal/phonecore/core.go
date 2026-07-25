package phonecore

// Resume is the phone's process-start entry point (PB-STATE-1/-2): it reads the ONE durable
// State blob and assembles every component whose in-memory counter would otherwise restart
// at zero after the OS kills the app -- the send-seq allocator, the receive replay guard,
// the grant watermark, the decoded caches and the offline op queue.
//
// There is NO Close. An Android process is SIGKILLed, never shut down cleanly, so nothing
// here may depend on a graceful exit: every durable write happens on the path that needs it.

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"

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
type Config struct {
	Dir     string
	Machine string
	State   Store
	Ack     Acker
}

// Core is the assembled phone: durable custody plus the components resumed from it.
type Core struct {
	store Store
	ks    crypto.KeyStore
	ack   Acker

	seq    *Sequencer
	router *MailboxRouter
	ops    *OpQueue

	mu     sync.Mutex
	st     State
	grants *crypto.GrantReceiver
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
		if store, err = OpenStore(path, cfg.Machine); err != nil {
			return nil, err
		}
	}
	ks, err := openKeyStore(cfg.Dir)
	if err != nil {
		return nil, err
	}
	c := &Core{
		store: store,
		ks:    ks,
		ack:   cfg.Ack,
		seq:   &Sequencer{},
		ops:   NewOpQueue(0),
		st:    store.Load().clone(),
	}
	c.router = newMailboxRouter(crypto.ContentKey{}, c)
	c.rebind()
	return c, nil
}

// openKeyStore recovers the device keys from dir, generating them on first launch. They
// must be the SAME keys across a restart: the daemon registry pins the device id to the
// command-signing public key (R-DEV.1), so regenerating them would invalidate every command
// the phone signs and every grant addressed to it.
//
// An empty dir has nowhere to persist, so the keys are EPHEMERAL (generated per Resume,
// written to a temp directory that is removed before this returns -- crypto.KeyStore has no
// in-memory constructor and internal/remote/crypto is frozen). That wiring is for a caller
// that injects its own Store; production always provisions a state directory.
func openKeyStore(dir string) (crypto.KeyStore, error) {
	if dir == "" {
		tmp, err := os.MkdirTemp("", "phonecore-ephemeral-keys-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
		return crypto.NewFileKeyStore(tmp)
	}
	ks, err := crypto.OpenFileKeyStore(dir)
	if err == nil {
		return ks, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("open device keys: %w", err)
	}
	return crypto.NewFileKeyStore(dir)
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

// rebind rebuilds the derived components from the current durable state.
func (c *Core) rebind() {
	st := c.State()

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
	// The channels the authorities cover are no longer unverified.
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
func (c *Core) commitReceive(b Bucket, f inboundFrame, cursor uint64) (contiguous bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.st.clone()
	if st.Receive == nil {
		st.Receive = map[Bucket]uint64{}
	}
	contiguous = f.seq == st.Receive[b]+1
	if !contiguous {
		if st.Stale == nil {
			st.Stale = map[Bucket]bool{}
		}
		st.Stale[b] = true
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
	return contiguous, c.persistLocked(st)
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
	out := scratch.List()
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
