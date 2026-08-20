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
	"os"
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
// the wake KEK is the one the push path opens, so anything under it is reachable by the very
// process PB-KEY-2 keeps away from session content -- and FCM reads every push payload it
// carries (ADR-007 A15 as amended by B133), which is why the split is a WIRE property.
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

	// push is Wave R3's durable push-binding custody (pushbinding.go): the installation
	// identity, the per-address wake keys and high-waters, the machine-revoked address
	// set and the refused-wake counter, sealed under the WAKE tier KEK in its own
	// container beside the pinned State schema. Guarded by mu, like st.
	push *pushStore

	// regMu serialises EnsurePushRegistration END TO END: the durable-id read, the
	// register-or-rotate network round trip, and the write back. mu cannot do that job --
	// it must never be held across a network call -- and without this lock two concurrent
	// first runs (SwarmApplication.onCreate's getToken and SwarmMessagingService.onNewToken
	// arrive on different threads) both see no installation and both Register under
	// DISTINCT idempotency keys: PG-REG-2 does not bind across them, and the loser's
	// installation is durably orphaned holding a live FCM token for 180 days. LOCK ORDER:
	// regMu is taken with mu released and never while holding mu; the body takes mu
	// briefly on either side of the network call. The current FCM token is read INSIDE
	// regMu, at act time, from the caller's TokenSource -- a token snapshot taken before
	// the lock can be older than the one a caller ahead in the queue just installed, and
	// no phone-side rule can order two opaque token strings after the fact.
	regMu sync.Mutex

	// wakeDropPersisted latches after the FIRST refused wake this process persisted, and
	// wakeDropsUnpersisted counts the refusals since the last SUCCESSFUL write. Together
	// they are countWakeDropLocked's write budget: the first refusal of a process persists
	// (the wake-drop-die FCM receipt only ever has one), and thereafter one write per
	// wakeDropPersistEvery refusals -- so the durable counter converges in a long-lived
	// foreground process instead of standing at 1 forever, while the attacker-driveable
	// re-seal cost stays bounded. Both guarded by mu, like push.
	wakeDropPersisted    bool
	wakeDropsUnpersisted uint64

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

	// lastProfile is the RemoteProfileV1 carried by the most recently SUCCESSFUL Reconcile
	// call (ADR-016 W4.1: "a relay-supplied or unauthenticated hint is ignored"). It is set
	// ONLY inside Reconcile, next to the machine/epoch match that record already passed, so
	// LastProfile can never hand a caller a profile that check refused -- unlike reading
	// router.reconcileRecord() again, whose own field comment says only "a record has
	// ARRIVED", independent of whether Reconcile ever accepted it.
	lastProfile schema.RemoteProfileV1

	// rebindMu serialises ONE rebind's read of the durable state with its application to the
	// derived components. mu cannot do that job: it is released between the two, and every
	// component rebind feeds takes its own lock, so holding mu across them would put mu above
	// locks that callers already take below it.
	//
	// Without it the two halves of two rebinds interleave, and PB-KEY-7 is the direction that
	// matters: a Save whose rebind read PRE-purge state applies after the purge's, leaving
	// MailboxRouter bound to the content key the purge destroyed -- after PurgeKeys has
	// returned and every writer has finished, on a device the owner has revoked. That is the memory half
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
			// MM6 step 5: once the machine registry under this dir is LIVE, the singleton
			// blob here is a rollback artefact, not a resumable state -- resuming it would
			// stand up a second live send sequencer for the pairing, and a re-issued seq
			// under a retained epoch is stale-dropped by the gateway permanently.
			if _, err := os.Stat(registryPath(cfg.Dir)); err == nil {
				return nil, fmt.Errorf("phonecore: resume %s: %w", cfg.Dir, ErrStateMigrated)
			}
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
	// The push-binding container opens under the wake KEK for the reason the wake key
	// tier does: a push arrives with nobody present, and the receiver must read its
	// per-address keys and high-waters with the content tier locked. openKeyStore has
	// already refused a real Dir with no sealers, so this cannot write in the clear.
	push, err := openPushStore(cfg.Dir, cfg.WakeSealer)
	if err != nil {
		return nil, err
	}
	c := &Core{
		store:  store,
		ks:     ks,
		ack:    cfg.Ack,
		push:   push,
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

// Save ADOPTS st wholesale as the phone's durable state and REBINDS every component derived
// from it (send-seq epoch, grant watermark, epoch content key, receive high-waters, caches,
// op queue). It is the verb for a caller that holds an ENTIRE state and means all of it -- a
// reseed, a fixture, a whole-blob restore: a Save that persisted the coordinates but left the
// live objects on the old epoch would keep reserving seqs against a stream that no longer
// exists.
//
// IT IS THE WRONG VERB FOR CHANGING A FIELD, and Mutate exists for that. The adopt is blind:
// whatever the caller read is what lands, so every core-internal persist between the caller's
// read and this write is reverted. See Mutate for the transaction that made that a silent
// brick on a paired handset.
func (c *Core) Save(st State) error {
	if err := c.persist(st); err != nil {
		return err
	}
	c.rebind()
	return nil
}

// Mutate applies fn to the durable state UNDER THE CORE LOCK, persists the result and rebinds.
// It is how a caller CHANGES A FIELD, and it is the only shape that is safe to do so from
// outside the core: the read and the write are one transaction, so nothing can land in
// between and nothing that landed before it is reverted.
//
// THE HAZARD IT REPLACES was not a lost field, it was a brick. Seven gomobile facade sites
// read the state, worked with this lock released and Saved the snapshot back. State.EpochID
// and State.Keys are adopted as given -- fileStore.mergeGuards raises only the replay-guard
// coordinates -- so a snapshot taken before the drain consumed the machine's epoch grant
// carried the epoch keys back to what they were, and App.pin ZEROES them itself when the
// pairing lands in a new epoch, which is right against its own snapshot and destroys the key
// the grant just installed. resealTier then writes no content-key field at all, so the
// destruction reaches disk. The window is the normal pairing sequence: pin runs on the pairing
// goroutine, deliver.go appends the bootstrap frame once per gateway session, and the drain
// consumes it concurrently.
//
// It is TERMINAL because GrantEpoch/GrantSeq ARE merged monotonically. The watermark survives
// at the coordinates of the grant whose key was just destroyed, crypto.GrantReceiver enforces
// strict (epoch, seq) monotonicity, and the gateway re-appending the very same frame next
// session is refused as a replay -- forever. The phone holds no content key, cannot obtain
// one, and the only exit is a machine-side re-grant at a higher seq.
//
// fn RUNS WITH c.mu HELD and must touch nothing but the State it is handed. Calling back into
// the Core from inside it deadlocks.
func (c *Core) Mutate(fn func(*State)) error {
	c.mu.Lock()
	st := c.st.clone()
	fn(&st)
	err := c.persistLocked(st)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	c.rebind()
	return nil
}

// PurgeKeys is PB-KEY-7's revoke/unpair purge at the durable layer: BOTH tier keys and
// everything sealed under either one are destroyed, in memory and at rest. It REBINDS
// afterwards for the same reason Save does -- the live objects must come off the purged epoch,
// or the router keeps opening frames under a key the phone no longer holds.
//
// THE SEALED BLOBS GO. This comment has said both things: ADR-007 B44 struck the claim while
// the trigger was a screen lock, and ADR-007 B133 moves the trigger to revoke/unpair, where
// re-pairing rather than a local unwrap is the intended way back. Store.PurgeKeys carries the
// whole argument.
//
// It is a distinct verb from Save because a Save whose keys are zero is ambiguous: the wake
// path holds zeros for a content key it could not read and Saves constantly, so custody
// cannot tell "nothing to write" from "destroy this" by looking at the bytes (see
// Store.PurgeKeys).
//
// The durable error is REPORTED but never short-circuits the rest: custody purges its own
// memory unconditionally, so adopting and rebinding is what carries that through to the live
// objects. Returning early on a failed write left the keys in c.st and bound in the router
// on a device the owner has revoked -- the half of PB-KEY-7 that cannot fail, gated behind the
// half that can.
func (c *Core) PurgeKeys() error {
	c.mu.Lock()
	err := c.store.PurgeKeys()
	c.st = c.store.Load().clone()
	c.mu.Unlock()
	c.rebind()
	return err
}

// UnsealContent restores content operations by re-opening the content tier through its KEK.
// It is what a process that came up WITHOUT the content tier calls -- the push/wake path holds
// the wake key and never asks for the other (PB-KEY-2) -- and NOT a way back from PurgeKeys,
// which destroys the blob it would open.
//
// It REBINDS on success for the same reason PurgeKeys does: the live objects must come back
// onto the restored epoch key, or the router keeps refusing frames the phone can now open.
// A refusal changes nothing at all, so a core without the tier stays exactly without it.
func (c *Core) UnsealContent() error {
	c.mu.Lock()
	err := c.store.UnsealContent()
	c.st = c.store.Load().clone()
	c.mu.Unlock()
	if err != nil {
		return err
	}
	c.rebind()
	return nil
}

// RewindRelayCursor resets the relay read cursor to zero durably, so the next drain re-reads
// the mailbox from the beginning. It is the ONLY recovery from a relay-poisoned cursor --
// see the Store method for why the replay guards make it safe and what bounds the work.
//
// NO REBIND, unlike PurgeKeys and UnsealContent: nothing derived from durable state depends
// on the read cursor. The drain reads it fresh on every pass (mobile.App.drain), and the
// receiver, sequencer and router are keyed on the epoch material this does not touch.
func (c *Core) RewindRelayCursor() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.store.RewindRelayCursor(); err != nil {
		return err
	}
	c.st = c.store.Load().clone()
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
//
// THE PRODUCTION ENTRY is installGrant's replay arm; see grantLossDetected there for what
// makes the condition decidable without a clock, a threshold or a wire change.
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

// grantLossDetected reports whether a refused bootstrap grant PROVES PB-KEY-3's terminal
// state. It is the production entry MarkGrantLost had none of, and it needs no clock, no
// threshold and no new wire field.
//
// PB-KEY-3 describes the state as "drained, no grant, retention cap passed", and the phone
// can measure none of those three: it holds no pairing timestamp, and the retention cap is
// RELAY configuration asserted by the party this design treats as hostile. But it does not
// have to measure them, because the bootstrap frame is the one machine -> phone frame that
// is NOT sealed under the key at issue -- it is tagged plaintext signed by the grant-signing
// key pinned at pairing, because it is what DELIVERS the content key -- and the gateway
// re-appends it from its persistent sidecar once per gateway session.
//
// So the two conditions below are PROOF rather than inference:
//
//	crypto.ErrGrantReplay -- the machine's own signed frame arrived, so the gateway is
//	    connected and delivering, and the coordinates it is able to deliver are ones this
//	    phone has already consumed. crypto.GrantReceiver enforces strict (epoch, seq)
//	    monotonicity and the receiver is only seeded when a watermark exists, so this error
//	    cannot be reached by a phone that never accepted a grant.
//	no content key -- and the phone cannot open anything with what it has.
//
// Together: re-delivery can never help, however long anyone waits, and only a machine-side
// re-grant advancing the seq can. That is the terminal state in its own terms.
//
// BOTH CONDITIONS ARE LOAD-BEARING. A replay refused while the phone holds a WORKING key is
// the normal traffic the watermark exists for -- a retaining relay re-serving a retired
// pre-rotation grant -- and marking there would send a user with a healthy handset to the
// machine. And a phone that is merely keyless has proved nothing: the gateway may simply not
// have reconnected yet, which is answered by waiting, not by a re-grant.
//
// AN UNOPENED CONTENT TIER IS EXCLUDED TWICE, and it needs to be. The first exclusion is the
// one that was always here: the sealed-box open runs before the replay check and uses the
// RECIPIENT key, which is a content-tier device scalar unsealed per operation, so a handset
// whose Keystore is refusing never reaches this function. The second is the caller's keyless
// test, which asks whether the phone HAS a key rather than whether this process is holding one
// -- the push/wake path runs without the content tier by design (PB-KEY-2), and it would
// otherwise present a phone that is fine as one that is permanently lost.
//
// WHAT IT DELIBERATELY DOES NOT CLAIM. A
// relay serving a very old retained grant to a keyless phone marks it when the gateway may
// yet deliver a good one -- a false positive that costs one message which the real grant
// clears on arrival, in the transaction that installs the key. That direction is the safe
// one: the flag is a message, never a state change, and PB-STATE-10 forbids latching it.
func grantLossDetected(err error, keyless bool) bool {
	return keyless && errors.Is(err, crypto.ErrGrantReplay)
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
		// KEYLESS MEANS THE PHONE HAS NO KEY, not that this process is not holding one. A
		// content tier that is merely UNOPENED holds its sealed key at rest and recovers it with
		// a fresh unwrap, so treating it as keyless would make PB-KEY-3's terminal state the
		// ordinary consequence of serving a push -- see State.contentSealed.
		keyless := c.st.Keys.ContentKey == (crypto.ContentKey{}) && !c.st.contentSealed
		c.mu.Unlock()
		if grantLossDetected(err, keyless) {
			// The mark is advisory state ABOUT the refusal, never a second verdict on it: the
			// replay is still what happened and still what the caller must see, so a failed
			// persist is joined rather than substituted. errors.Is keeps matching
			// crypto.ErrGrantReplay through the join, which is what acceptBootstrap's ack rule
			// reads. Nothing is lost if the write does fail either -- the gateway re-appends
			// the same sidecar every session, so the next delivery re-derives it.
			if merr := c.MarkGrantLost(); merr != nil {
				return false, errors.Join(err, merr)
			}
		}
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
//
// WHAT IS PERSISTED IS A VERDICT AND NOT A PAYLOAD (Wave R6 review round 2). This stored the
// WHOLE Control, journal records included, into a map nothing prunes -- this package's own
// recorded residual, quoted at interaction.go: "never pruned, so every launch re-offers every
// outcome ever recorded". Wave R6's two reads answer WITH RECORDS, so every history page wrote
// up to `limit` full item bodies into the phone's durable state file permanently, and every
// detail read wrote the FULL PRE-TRUNCATION BODY -- precisely the payload that was too large
// to ship inline. The records belong to the LIVE transcript, which is where the facade folds
// them the moment the reply is claimed; what has to survive a process death is the answer:
// which op, what the machine said, and whether it said anything at all.
func (c *Core) RecordOutcome(ctrl schema.Control) error {
	if ctrl.OperationID == "" {
		return errors.New("phonecore: outcome carries no operation id")
	}
	ctrl.Journal = nil
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
	c.lastProfile = rec.Profile
	c.mu.Unlock()

	c.seq.SeedFrom(rec.InboundHighWater)
	c.router.adopt(st, bucket, reply, rec)
	return nil
}

// LastProfile returns the RemoteProfileV1 the most recently SUCCESSFUL Reconcile call
// adopted (ADR-016 W4.1). It takes no parameters and reads only what Reconcile itself just
// stored, so there is no way to call it with an unauthenticated or relay-sourced profile --
// the migration ladder (App.applyRelayTLSPolicy) has nothing else to pass. Before any
// reconcile has ever succeeded, or when the machine has published no relay policy at all,
// this is the zero value, which applyRelayTLSPolicy already treats as "no advertisement":
// the same no-op an old machine's profile-less reconcile produces.
func (c *Core) LastProfile() schema.RemoteProfileV1 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastProfile
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
// PB-APP-11 rides this transaction too, and it rides it for EVERY frame the AEAD vouched
// for -- gapped or contiguous. The frame is proof that the machine was alive at the moment
// it stamped it, which is true whether or not the frames before it arrived, so freshness and
// completeness are independent facts and only one of them is what a hole damages.
func (c *Core) commitReceive(b Bucket, f inboundFrame, cursor uint64, now time.Time) (contiguous bool, streams map[string]bool, err error) {
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
	if heard := heardAt(f.issuedAt, now); heard > st.LastHeardAt {
		st.LastHeardAt = heard
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
		// The transcript MERGES the events half rather than replacing (see MailboxRouter.apply
		// for why the two halves of one frame commit under opposite rules), and it is the
		// channel IS-LIFE-3 re-delivers an unresolved approval_request on.
		st.Items = itemsWith(c.router.Items(), f.reseed.Events...)
	case "":
		// An interaction record shapes the TRANSCRIPT alone (IS-LAYER-1, IS-SS-1); everything
		// else keeps the roster path unchanged.
		if f.record.Type == RecordTypeInteraction {
			st.Items = itemsWith(c.router.Items(), f.record)
			return
		}
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
