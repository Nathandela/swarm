package phonecore

// PB-STATE-1/-5: ONE versioned, enumerated durable schema for every resume-critical
// coordinate the phone holds, and its file custody.
//
// Everything here exists because an Android process is SIGKILLed as routine OS
// behaviour: without durable custody the send-seq restarts at 1 under the same epoch
// (the gateway then stale-drops every keystroke, take_control, launch and kill for good)
// and the receive high-water resets to 0 (a retaining relay may redeliver freely). There
// is deliberately no Close: durability may never depend on a clean exit, so every Save is
// durable when it returns.
//
// internal/remote/crypto is FROZEN, so the coordinates it owns are persisted AROUND it and
// replayed back through the seams it already exposes: MailboxReceiver.SeedHighWater for the
// receive high-waters and crypto.NewGrantReceiverAt for the grant watermark.

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// StateSchemaVersion stamps the on-disk blob. A blob stamped with a HIGHER version was
// written by a newer build and is refused (ErrFutureSchema) rather than decoded with this
// build's field set: silently dropping a coordinate it does not know means resetting a
// send-seq ceiling or a receive high-water to zero, which is exactly the replay hole this
// file closes. Every shipped version keeps a byte-literal fixture in state_test.go that
// must go on loading (PB-STATE-5).
//
// v2 adds the coordinates the gomobile facade (S8) is the first consumer to need:
// MachineRelayAuthPub, PushToken, PushPreference and ReconciledEpoch. The bump is what
// makes a downgrade fail closed -- a v1 build decoding a v2 blob would drop
// MachineRelayAuthPub and leave the phone with a valid content key, a valid send-seq and
// no destination, with nothing failing loudly.
//
// v3 seals wake_key and content_key under their PB-KEY-2 tier KEKs (PB-KEY-9). The field
// SET is unchanged, which is exactly why the bump is required: a v2 build would read v3's
// sealed bytes AS the key and encrypt every frame under a wrong one, silently. A v2 blob is
// refused here for the mirror reason -- its cleartext key read as a sealed blob is the same
// confusion one direction over.
//
// v4 adds stale_streams, the per-REPAIR-CHANNEL staleness PB-SYNC-1 splits out of the
// per-BUCKET flags (see State.StaleStreams). The bump is what makes the downgrade fail
// closed: a v3 build decoding a v4 blob would drop the field and report a channel it KNOWS
// has a hole in it as live, which is the one thing PB-APP-8 forbids.
//
// v5 moves the STATE PB-STATE-9 assigns a tier to out of the cleartext fields and into three
// sealed containers -- wake_state, content_kept and content_purgeable (see stateFile). v3
// sealed the two epoch KEYS and nothing else, so a locked handset still held the decrypted
// journal, the server-rendered terminal grids and the command outcomes as plain JSON at rest.
// The bump is required in both directions: a v4 build decoding a v5 blob would find no
// send_seq and no receive array and start from an empty replay guard -- the exact reset this
// file exists to refuse -- so it must fail closed instead, and a v5 build reading a v4 blob
// must know to take those fields from the cleartext and reseal them on the next Save.
//
// v6 adds PushPreference.Version, the DEVICE-supplied monotonic counter PB-PUSH-10 makes the
// machine refuse a preference update without. The field sits inside an existing container's
// object rather than at the top level, which is precisely why the bump is not optional: the
// top-level tag set is unchanged, so nothing else would notice, and a build one version back
// would drop the counter and restart it at 1 on the next Save. The machine refuses anything
// that does not STRICTLY exceed what it holds (remotegw.filePushPrefs.SavePrefs, because the
// relay may replay a frame from before the user turned pushes off) -- so a counter that
// restarts means every toggle from that moment on is silently refused, forever, while the
// settings screen shows the user's new value. A brick with no visible symptom.
const StateSchemaVersion = 6

// StateFileName is the blob's name inside the phone's state directory.
const StateFileName = "phone-state.json"

var (
	// ErrCorruptState refuses an unreadable or unversioned blob. Starting from an empty
	// checkpoint instead would leave the replay guard blind -- a fresh crypto.MailboxReceiver
	// skips the staleness check entirely -- and re-open every frame the relay still retains.
	ErrCorruptState = errors.New("phonecore: corrupt phone-state file")
	// ErrFutureSchema refuses a blob from a newer build (an upgrade then a downgrade, or a
	// restored backup). Never a silent reinterpretation.
	ErrFutureSchema = errors.New("phonecore: phone-state file schema is newer than this build")
)

// Bucket identifies ONE machine -> phone receive stream by exactly the coordinate
// crypto.MailboxReceiver keys its own high-water map by: the envelope's sender key id and
// epoch id.
//
// There is more than one bucket per epoch, which is why this is not a scalar: the machine
// stamps its routing key id on journal, terminal AND reconcile frames (one bucket), while
// command replies leave SenderKeyID zero (a SECOND bucket with an independent seq space).
// A scalar high-water would let the reply stream's seq 4 stale-drop the journal stream's
// seq 4, silently deleting one of the two channels.
type Bucket struct {
	Sender [8]byte
	Epoch  uint32
}

// State is every coordinate the phone must recover to resume after a process death,
// enumerated in ONE schema (PB-STATE-1) rather than scattered across side files.
//
// SendSeq is keyed PER EPOCH because a revoke rotates the epoch and the phone -> machine
// stream then legitimately restarts at 1; carrying one epoch's ceiling into another would
// walk the counter away from the gateway's per-(sender,epoch) high-water. Receive is keyed
// per Bucket for the mirror reason.
type State struct {
	Machine        string // machine endpoint id these coordinates belong to
	MachineStatic  []byte // machine Noise-static public key pinned at pairing
	MachineSignPub []byte // machine Ed25519 grant-signing public key pinned at pairing
	// MachineRelayAuthPub is the machine's relay-auth Ed25519 public key, pinned at
	// pairing. Everything above records who the machine IS; this is the only coordinate
	// that says how to REACH it: relay.RoutingID(MachineRelayAuthPub) is the mailbox every
	// command and keystroke is appended to, and it is what the phone authorizes so the
	// machine may append back. Without it a restored phone holds a valid content key, a
	// valid send-seq and no destination -- and nothing fails loudly.
	MachineRelayAuthPub []byte
	RoutingID           string                    // this phone's relay routing id
	EpochID             uint32                    // current epoch the content key belongs to
	Keys                crypto.EpochKeys          // wake + content keys for EpochID
	SendSeq             map[uint32]uint64         // per-epoch DURABLE send-seq reservation ceiling (PB-STATE-3)
	Receive             map[Bucket]uint64         // per-(sender,epoch) receive high-water (replay guard)
	GrantEpoch          uint32                    // highest accepted grant epoch (PB-STATE-4(c))
	GrantSeq            uint64                    // highest accepted grant seq for GrantEpoch
	WakeReplay          uint64                    // highest accepted push-wake counter
	RelayCursor         uint64                    // relay mailbox read cursor the next poll resumes from
	Sessions            []CachedSession           // journal-derived session model
	Snapshots           []Snapshot                // server-rendered terminal grids, latest per session
	PendingOps          []QueuedOp                // offline mutating ops awaiting replay (R-PHC.4)
	OpOutcomes          map[string]schema.Control // durable operation outcomes, keyed by operation id
	Stale               map[Bucket]bool           // buckets whose content may not be trusted until reconciled
	// StaleStreams are the REPAIR CHANNELS whose content may not be trusted (PB-SYNC-1).
	// It is a second set rather than a view over Stale because marking and clearing happen
	// at different granularities and one bit cannot carry both: a gap in the SHARED bucket
	// conservatively stales journal AND terminal (crypto.MailboxResult carries a bare Gap
	// bool with no frame kind, so the skipped seq cannot be attributed to either), while a
	// reseed repairs the journal alone and a fresh grid repairs the terminal alone
	// (PB-SYNC-2). With one flag per bucket, repairing either would present the other's
	// hole as live.
	//
	// It is DURABLE because Android SIGKILLs the app as routine behaviour: a flag held only
	// in memory comes back clear, and the phone presents a gap it already knew about as live
	// on the very next launch.
	StaleStreams map[string]bool
	// PushToken is the provider push token PB-STATE-9 assigns to the WAKE tier and
	// PB-PUSH-9 requires to survive process death and app upgrade: a token held only in
	// memory is re-registered only if the app happens to be foregrounded.
	PushToken string
	// PushPreference are PB-APP-7's two coarse toggles. PB-PUSH-10 makes the MACHINE
	// authoritative for suppression, but the phone still has to render the setting the
	// user chose across a restart, or the UI contradicts what the machine is doing.
	PushPreference PushPreference
	// ReconciledEpoch is the epoch whose machine-published rollback authorities
	// (PB-SYNC-7) have been ADOPTED. It closes the S7 recorded residual "reconcile
	// adoption is not persisted, so every phone process death re-arms the fail-closed
	// refusal of mutating ops, clearable only by a gateway reconnect the phone cannot
	// trigger" -- on Android process death is routine, so that residual is PB-STATE-10's
	// brick. The authorities themselves are already folded into the coordinates above;
	// this records that the fold HAPPENED, which nothing else can witness when every
	// authority is legitimately zero.
	ReconciledEpoch uint32

	// purgeGen is the lock-purge counter this snapshot was taken at. It is custody's own
	// bookkeeping, never persisted and never set by a caller: Store stamps every State it
	// hands out, and a Save carrying an OLDER stamp is a writer that has not noticed the
	// purge in between, whose key material and decrypted caches are dropped rather than
	// re-sealed over it (PB-KEY-7). A State a caller built from a literal carries zero,
	// which is correct -- it cannot have been derived from a post-purge Load.
	purgeGen uint64

	// contentSealed records that a sealed content key for THIS epoch exists at rest, whether
	// or not this process can open it. Custody's own bookkeeping like purgeGen: stamped on
	// every State the Store hands out, never persisted, never set by a caller.
	//
	// It is what separates "this phone has no epoch key" from "this process cannot read the one
	// it has", and those became different facts when the lock purge stopped destroying the
	// sealed key (PurgeKeys, ADR-007 B35). Only grantLossDetected consumes it, and there it is
	// load-bearing: without it a bootstrap grant re-appended while the content tier is locked is
	// a replay against a phone that LOOKS keyless, so PB-KEY-3's TERMINAL state would be entered
	// by a screen lock -- the brick the lock purge was redesigned to avoid, arriving by the
	// other road.
	contentSealed bool
}

// PushPreference is PB-APP-7's pair of coarse notification toggles, persisted so the
// settings screen renders the user's choice after a restart rather than a default.
type PushPreference struct {
	Alerts   bool `json:"alerts"`
	Mentions bool `json:"mentions"`
	// Version is the DEVICE-supplied monotonic counter the machine gates the update on
	// (schema.PushPrefs.Version, PB-PUSH-10). It is durable for one reason and it is not
	// bookkeeping: the machine refuses any push_prefs whose Version does not STRICTLY exceed
	// the stored one, because the relay is the declared adversary and may replay a frame from
	// before the user turned pushes off. A counter held only in memory restarts at 1 after the
	// process death Android hands out routinely, and every toggle from then on is refused
	// while the settings screen goes on showing the value the user chose.
	//
	// Version 0 is the never-configured record, so the phone's first real update always wins.
	Version uint64 `json:"version,omitempty"`
}

// clone deep-copies the maps and slices so custody and callers can never observe or
// mutate each other's copy.
func (s State) clone() State {
	s.MachineStatic = slices.Clone(s.MachineStatic)
	s.MachineSignPub = slices.Clone(s.MachineSignPub)
	s.MachineRelayAuthPub = slices.Clone(s.MachineRelayAuthPub)
	s.SendSeq = maps.Clone(s.SendSeq)
	s.Receive = maps.Clone(s.Receive)
	s.Sessions = slices.Clone(s.Sessions)
	s.Snapshots = slices.Clone(s.Snapshots)
	s.PendingOps = slices.Clone(s.PendingOps)
	s.OpOutcomes = maps.Clone(s.OpOutcomes)
	s.Stale = maps.Clone(s.Stale)
	s.StaleStreams = maps.Clone(s.StaleStreams)
	return s
}

// Store is the phone's durable custody of State. Load cannot fail: the blob is validated
// once at OpenStore, which fails closed, so a Store that was constructed has already
// proved its state readable (mirroring remotegw.InboundState).
type Store interface {
	Load() State
	Save(State) error
	// PurgeKeys destroys the durable epoch key material -- the SEALED blobs included -- and
	// every decrypted cache derived from it (PB-KEY-7's lock purge).
	//
	// It is a method rather than a Save of a State whose keys are zero because those two
	// are not the same act and custody cannot tell them apart from the bytes: a process that
	// came up on a push holds zeros for a content key it merely could not read, and Saves
	// constantly. One signal for both means either the purge does not reach disk or the wake
	// path destroys the epoch, and S14a shipped the first.
	//
	// The IN-MEMORY half must happen whether or not the durable half succeeds: zeroizing
	// cannot fail, PB-KEY-7 lists it first, and an implementation that gates it behind the
	// write keeps the keys live on a read-only data directory. A returned error therefore
	// means "the blobs at rest survived", never "nothing was purged" -- Load answers with
	// the purged state either way.
	PurgeKeys() error

	// UnsealContent re-opens the content tier IN PLACE, through the tier KEK, and is
	// PB-KEY-7's "require a fresh unwrap before restoring content" read literally.
	//
	// It is the only way back from PurgeKeys, and it must stay the only one: the whole of
	// PB-SEC-2 is that the gate is the Keystore refusing an unwrap rather than a flag beside
	// it, so a path that restored content without consulting the sealer would make the lock
	// decoration. A refusal is returned VERBATIM -- crypto.ErrKeyAuthRequired for a locked
	// tier or a lapsed 60-second window, crypto.ErrKeyInvalidated for a destroyed KEK -- and
	// nothing is adopted unless every container opened.
	UnsealContent() error
}

// ---------------------------------------------------------------------------
// On-disk shape. JSON, versioned and self-describing: the blob is written at most once
// per consumed frame or per reserved seq BLOCK (never per keystroke), so an inspectable
// encoding costs nothing. Maps travel as arrays because a compound key is not a JSON
// object key -- the same choice remotegw/inboundstate.go made for the mirror direction.
//
// WHAT STAYS IN THE CLEAR, and why it is not an oversight (PB-STATE-9). The wake path runs
// with NO USER PRESENT, so anything the LOAD PATH itself must read before it can open a
// container has to be readable without either KEK: the machine id (a blob belonging to
// another machine is discarded wholesale before any unseal) and the epoch id (resealTier
// carries a key verbatim only into the epoch it was sealed for). The wake path then needs
// the phone's own routing id and the machine's relay-auth public key to reach the relay at
// all. The remaining cleartext fields are the ones PB-STATE-9 assigns to neither tier: the
// grant watermark, the relay read cursor, the staleness sets -- records that content is
// untrustworthy rather than the content -- and the two notification toggles the settings
// screen renders. Four public keys travel in the clear because they are public.
// ---------------------------------------------------------------------------

type stateFile struct {
	SchemaVersion       int    `json:"schema_version"`
	Machine             string `json:"machine"`
	MachineStatic       []byte `json:"machine_static,omitempty"`
	MachineSignPub      []byte `json:"machine_sign_pub,omitempty"`
	MachineRelayAuthPub []byte `json:"machine_relay_auth_pub,omitempty"`
	RoutingID           string `json:"routing_id"`
	EpochID             uint32 `json:"epoch_id"`

	PushPreference  PushPreference `json:"push_preference,omitzero"`
	ReconciledEpoch uint32         `json:"reconciled_epoch,omitempty"`

	// WakeKey and ContentKey are SEALED blobs from v3 on, each under its own tier KEK
	// (PB-KEY-9): one file cannot be gated two ways, and a content key recoverable
	// without the biometric collapses the tier split the design exists for.
	WakeKey    []byte `json:"wake_key,omitempty"`
	ContentKey []byte `json:"content_key,omitempty"`

	// WakeState is the wake tier's STATE, sealed under the wake KEK from v5 on: the push
	// token and the push dedup coordinate. It is a container rather than two sealed scalars
	// because the tier is opened once, at load, and a per-field seal would multiply the
	// Keystore round trips by the field count for no gain.
	WakeState []byte `json:"wake_state,omitempty"`
	// ContentKept and ContentPurgeable are the content tier, in TWO containers, because the
	// two halves have opposite lifetimes and one blob cannot have both.
	//
	// A Save taken while the tier is LOCKED must carry what it cannot read VERBATIM, or the
	// send-seq ceiling goes to zero and the phone renumbers from 1 under an epoch the gateway
	// already holds a high-water for -- every frame it sends stale-dropped for the life of
	// that epoch. A lock PURGE runs at exactly the same moment, with the tier locked by
	// definition, and PB-KEY-7 requires it to destroy the decrypted caches, which
	// carry-verbatim cannot do. So the replay-guard coordinates and the offline op queue go
	// in ContentKept, which a purge carries through untouched, and the three decrypted caches
	// go in ContentPurgeable, which a purge simply drops -- destroying a blob has never
	// required being able to read it.
	ContentKept      []byte `json:"content_kept,omitempty"`
	ContentPurgeable []byte `json:"content_purgeable,omitempty"`

	GrantEpoch  uint32         `json:"grant_epoch"`
	GrantSeq    uint64         `json:"grant_seq"`
	RelayCursor uint64         `json:"relay_cursor"`
	Stale       []bucketRecord `json:"stale,omitempty"`
	// StaleStreams travels as a sorted array of channel names (see State.StaleStreams).
	StaleStreams []string `json:"stale_streams,omitempty"`

	// Everything below is READ ONLY, and only from a blob written before v5. These fields
	// carried the tiered state in the clear up to v4; a v5 Save writes none of them and puts
	// the same coordinates in the containers above. They are kept so an upgraded app loads an
	// installed blob rather than starting from an empty replay guard (PB-STATE-5's forward
	// migration), and the reseal happens on the first Save after the upgrade.
	LegacyPushToken  string                    `json:"push_token,omitempty"`
	LegacyWakeReplay uint64                    `json:"wake_replay,omitempty"`
	LegacySendSeq    []sendSeqRecord           `json:"send_seq,omitempty"`
	LegacyReceive    []receiveRecord           `json:"receive,omitempty"`
	LegacySessions   []CachedSession           `json:"sessions,omitempty"`
	LegacySnapshots  []Snapshot                `json:"snapshots,omitempty"`
	LegacyPendingOps []QueuedOp                `json:"pending_ops,omitempty"`
	LegacyOpOutcomes map[string]schema.Control `json:"op_outcomes,omitempty"`
}

// wakeContainer is the plaintext of stateFile.WakeState.
type wakeContainer struct {
	PushToken  string `json:"push_token,omitempty"`
	WakeReplay uint64 `json:"wake_replay,omitempty"`
}

// keptContainer is the plaintext of stateFile.ContentKept: content-tier state a lock purge
// must PRESERVE. The replay-guard coordinates are not decrypted content -- they are the
// record of how far the streams have got -- and PendingOps is user content that no
// machine-sealed frame produced, so PB-KEY-7's purge leaves all three.
type keptContainer struct {
	SendSeq    []sendSeqRecord `json:"send_seq,omitempty"`
	Receive    []receiveRecord `json:"receive,omitempty"`
	PendingOps []QueuedOp      `json:"pending_ops,omitempty"`
}

// purgeableContainer is the plaintext of stateFile.ContentPurgeable: the three decrypted
// caches PB-KEY-7 names, and nothing else.
type purgeableContainer struct {
	Sessions   []CachedSession           `json:"sessions,omitempty"`
	Snapshots  []Snapshot                `json:"snapshots,omitempty"`
	OpOutcomes map[string]schema.Control `json:"op_outcomes,omitempty"`
}

// sendSeqRecord is one epoch's durable send-seq reservation ceiling.
type sendSeqRecord struct {
	Epoch   uint32 `json:"epoch"`
	Ceiling uint64 `json:"ceiling"`
}

// receiveRecord is one bucket's receive high-water. Sender is hex because a [8]byte is
// not a JSON map key.
type receiveRecord struct {
	Sender string `json:"sender"`
	Epoch  uint32 `json:"epoch"`
	Seq    uint64 `json:"seq"`
}

// bucketRecord names a bucket with no payload (the stale set).
type bucketRecord struct {
	Sender string `json:"sender"`
	Epoch  uint32 `json:"epoch"`
}

// fileStore is a Store backed by ONE JSON blob, held in memory and rewritten atomically on
// every Save. An empty path makes it purely in-memory (no durability): the default for a
// caller that provisions no state directory.
type fileStore struct {
	mu      sync.Mutex
	path    string
	machine string
	st      State

	// wake and content are PB-KEY-2's tier KEKs. The two epoch keys are sealed under
	// SEPARATE ones because a single file cannot be gated two ways.
	wake, content         Sealer
	wakeTier, contentTier sealedTier

	// The three sealed STATE containers, in the same shape and for the same reason as the
	// two key tiers above (PB-STATE-9): wakeState under the wake KEK, kept and purgeable
	// under the content KEK.
	wakeState, kept, purgeable stateTier

	// purgeGen counts the lock purges this store has taken. It stamps every State handed
	// out, so a Save can tell a snapshot from before a purge from one after it.
	purgeGen uint64
}

// stateTier is one sealed STATE container as it stands on disk, plus whether this process
// could open it. A container it did NOT open is carried VERBATIM by every Save, exactly as
// sealedTier's second case carries an unreadable key: the empty maps a locked process holds
// are coordinates it could not READ, not coordinates that are not there.
//
// There is deliberately no epoch here, where sealedTier has one. A key is meaningful only
// for its own epoch, so carrying one across a rotation writes a plausible key that decrypts
// nothing; these containers carry their own epoch keying INSIDE them -- SendSeq is a map
// keyed by epoch and Receive by a bucket that names one -- and are merged rather than
// replaced, so a rotation neither invalidates them nor is misled by them.
type stateTier struct {
	blob   []byte
	opened bool
}

// ErrContentTierLocked refuses a Save that would CHANGE content-tier state this process
// cannot read. It is the fail-closed half of the carry-verbatim rule: carrying the previous
// blob is right when the caller has nothing to add, and silently discards the caller's write
// when it does, so the two cases must be told apart rather than merged.
//
// PB-STATE-3's send-seq reservation is the path that meets it. reserveSendSeq is the
// Sequencer's only durable dependency and issues NO seq when it fails, which is exactly the
// honest outcome here: a process that cannot read the ceiling cannot raise it, and numbering
// from 1 instead would have the gateway stale-drop every keystroke, take_control, launch and
// kill for the life of the epoch. No production send path reaches it -- mobile's resolveSend
// refuses with errNoContentKey before any seq is drawn, because a frame cannot be sealed
// without the epoch content key, which is itself content tier -- so this is the fence rather
// than the normal case.
var ErrContentTierLocked = errors.New("phonecore: the content tier is locked; state sealed under it cannot be updated")

// sealedTier is one tier's key field as it stands on disk, plus whether this process knows
// what is in it -- either because it opened the blob, or because it put the contents there
// itself. A tier it does NOT know is rewritten VERBATIM by the next Save that has no key to
// write: re-sealing the zero value the process is holding would destroy a key it merely
// could not read, and that is the wake path's normal condition -- it runs with the content
// tier locked while any send reserves a seq and therefore Saves.
type sealedTier struct {
	blob   []byte
	opened bool
	// epoch is the State.EpochID the blob was written under. A key is only meaningful for
	// its own epoch, so a blob is carried verbatim only into the epoch it was sealed for --
	// see resealTier.
	epoch uint32
}

// OpenStore opens the durable phone state at path, loading any previously persisted blob.
//
// machineID is the machine these coordinates are only meaningful under. A blob stamped
// with a DIFFERENT one is not corrupt -- it simply describes coordinates that do not exist
// here -- so it loads EMPTY: `swarm remote init` regenerates the machine identity (epoch
// back to 1) and the re-paired phone must work, where a retained epoch-1 high-water would
// stale-drop its first frames and an error would refuse to start at all. An EMPTY machineID
// is an unpaired caller with no expectation, and adopts whatever the blob describes.
//
// A missing file is first run. Anything unparseable or unversioned is ErrCorruptState and a
// newer schema is ErrFutureSchema: both fail closed, because starting from an empty
// checkpoint would leave the replay guard blind and re-open every retained frame.
//
// machineID is also the INITIALISER, not only the filter, and that is load-bearing rather
// than tidy. Every path that ends with no blob adopted -- first run, and the different-machine
// discard just described -- used to leave State.Machine empty, and nothing downstream ever set
// it: the pairing handshake carries no endpoint id (pairing.MachinePayload has no such field),
// so mobile.App.pin has nothing to supply one from. The empty value was then PERSISTED, and
// this same filter discarded the whole blob on the next process start -- pairing, epoch, sealed
// content key, relay cursor and send-seq ceilings -- so a phone lost its pairing on the first
// Android process death, silently, and the "loads EMPTY, re-pair self-heals" path self-healed
// into the same state. Stamping it here rather than at any caller is what makes the filter and
// the value it tests come from ONE place; a store can no longer write a blob it will itself
// refuse.
//
// wake and content are PB-KEY-2's tier KEKs and are REQUIRED for any real path: writing the
// epoch keys in the clear is the defect PB-SEC-1 names, so their absence is ErrNoSealer
// rather than a silent cleartext blob (ADR-007 B18(c)). An empty path persists nothing, so
// there is nothing at rest to seal and no sealer is needed.
func OpenStore(path, machineID string, wake, content Sealer) (Store, error) {
	s := &fileStore{path: path, machine: machineID, wake: wake, content: content,
		st: State{Machine: machineID},
		// OPENED, holding nothing, until load says otherwise. Every path that ends with no
		// blob adopted -- first run, and the different-machine discard described above --
		// leaves this store owning an empty state it is fully entitled to write, and the
		// carry-verbatim branch must not fire on a container that was never read because
		// there was never one there. load sets these from the file.
		wakeState: stateTier{opened: true},
		kept:      stateTier{opened: true},
		purgeable: stateTier{opened: true},
	}
	if path == "" {
		return s, nil
	}
	if wake == nil || content == nil {
		return nil, fmt.Errorf("%w: %s must be sealed under both tier KEKs", ErrNoSealer, StateFileName)
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Load returns a copy of the persisted state, stamped with custody's own bookkeeping (see
// State.purgeGen and State.contentSealed -- neither is persisted, and neither can be answered
// from the State alone).
func (s *fileStore) Load() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st.clone()
	st.contentSealed = s.hasSealedContentKey()
	return st
}

// hasSealedContentKey reports whether a sealed content key for the CURRENT epoch is at rest.
// The epoch test is resealTier's fourth case: a blob left over from a rotation is a key that
// decrypts nothing, so it is not one this phone has.
func (s *fileStore) hasSealedContentKey() bool {
	return len(s.contentTier.blob) > 0 && s.contentTier.epoch == s.st.EpochID
}

// Save adopts st and rewrites the blob atomically. The REPLAY-GUARD coordinates (send-seq
// ceilings, receive high-waters, grant watermark, wake replay, relay cursor) are merged
// MONOTONICALLY: durable custody never moves them backwards, so a stale in-memory caller
// cannot reuse a seq or re-open a frame already consumed. Everything else is adopted as
// given -- Save is how the app records a new pairing, epoch or op queue. The in-memory copy
// advances only once the write succeeded, so a failed Save leaves exactly what a crashed
// process would have left: nothing durable, nothing claimed.
func (s *fileStore) Save(st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// A snapshot taken BEFORE a purge belongs to a writer that has not noticed it, and round
	// 2's "a real key always wins" makes its stale keys win over the purge. Re-apply what the
	// purge destroyed instead: the rest of the snapshot still lands, because refusing the
	// whole Save would hold the purge by losing every unrelated coordinate with it.
	if st.purgeGen < s.purgeGen {
		st = dropContentMaterial(st)
	}
	// A CALLER ARRIVING WITH A REAL CONTENT KEY HAS PROVED THE TIER IS OPEN, and the containers
	// have to be told. resealTier's "a real key always wins" branch is about to seal that key
	// under s.content, so the KEK is demonstrably answering right now -- while s.kept may still
	// be marked unopened from a lock or from a push-woken start. Left that way it is carried
	// verbatim for the life of the process, and refuseUnreadableContentWrite then refuses the
	// very first send-seq reservation the restored key makes possible. That is the shape a lock
	// recovered by a re-grant rather than by UnlockContent lands in, so it is closed here rather
	// than at one caller.
	if !s.kept.opened && st.Keys.ContentKey != (crypto.ContentKey{}) {
		if err := s.reopenKept(&st); err != nil {
			return err
		}
	}
	merged := mergeGuards(s.st, st.clone())
	merged.purgeGen = s.purgeGen
	if s.path != "" {
		if err := s.refuseUnreadableContentWrite(merged); err != nil {
			return err
		}
		wake, err := resealTier(s.wake, merged.Keys.WakeKey[:], s.wakeTier, merged.EpochID)
		if err != nil {
			return fmt.Errorf("seal wake key: %w", err)
		}
		content, err := resealTier(s.content, merged.Keys.ContentKey[:], s.contentTier, merged.EpochID)
		if err != nil {
			return fmt.Errorf("seal content key: %w", err)
		}
		wakeState, err := sealContainer(s.wake, wakeContainerOf(merged), s.wakeState)
		if err != nil {
			return fmt.Errorf("seal wake state: %w", err)
		}
		kept, err := sealContainer(s.content, keptContainerOf(merged), s.kept)
		if err != nil {
			return fmt.Errorf("seal content state: %w", err)
		}
		purgeable, err := sealContainer(s.content, purgeableContainerOf(merged), s.purgeable)
		if err != nil {
			return fmt.Errorf("seal decrypted caches: %w", err)
		}
		seals := stateSeals{
			wakeKey: wake.blob, contentKey: content.blob,
			wakeState: wakeState.blob, kept: kept.blob, purgeable: purgeable.blob,
		}
		if err := persistState(s.path, merged, seals); err != nil {
			return err
		}
		s.wakeTier, s.contentTier = wake, content
		s.wakeState, s.kept, s.purgeable = wakeState, kept, purgeable
	}
	s.st = merged
	return nil
}

// refuseUnreadableContentWrite fails a Save that carries content-tier state into a container
// this process could not open. The alternative is silence: the carry-verbatim branch would
// write the previous blob back and the caller's coordinate would simply vanish, which is the
// class of defect PB-STATE-9 was written to close one level down. See ErrContentTierLocked.
func (s *fileStore) refuseUnreadableContentWrite(st State) error {
	if !s.kept.opened && (len(st.SendSeq) > 0 || len(st.Receive) > 0 || len(st.PendingOps) > 0) {
		return fmt.Errorf("%w: %d send-seq ceiling(s), %d receive high-water(s) and %d pending op(s) "+
			"cannot be recorded", ErrContentTierLocked, len(st.SendSeq), len(st.Receive), len(st.PendingOps))
	}
	if !s.purgeable.opened && (len(st.Sessions) > 0 || len(st.Snapshots) > 0 || len(st.OpOutcomes) > 0) {
		return fmt.Errorf("%w: %d session(s), %d snapshot(s) and %d outcome(s) cannot be recorded",
			ErrContentTierLocked, len(st.Sessions), len(st.Snapshots), len(st.OpOutcomes))
	}
	return nil
}

// sealContainer returns the sealed state container to write, from the same three cases
// resealTier distinguishes for a key, minus the epoch (see stateTier):
//
//	could not open it -> carry the previous blob VERBATIM. The caller holds nothing for this
//	                     container because the tier is locked, and resealing that nothing
//	                     destroys the send-seq ceiling, the receive high-waters and the op
//	                     queue. refuseUnreadableContentWrite has already established the
//	                     caller is not trying to change it.
//	nothing to write  -> no field at all, so a purge that emptied the container is not undone
//	                     by the next Save.
//	otherwise         -> seal it.
func sealContainer(sl Sealer, payload any, prev stateTier) (stateTier, error) {
	if !prev.opened {
		return prev, nil
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return stateTier{}, err
	}
	// An empty container marshals to exactly this -- every field is omitempty -- and writing a
	// sealed blob for it would put a decryptable envelope on disk saying nothing.
	if string(plain) == "{}" {
		return stateTier{opened: true}, nil
	}
	blob, err := sl.Seal(plain)
	if err != nil {
		return stateTier{}, err
	}
	return stateTier{blob: blob, opened: true}, nil
}

func wakeContainerOf(st State) wakeContainer {
	return wakeContainer{PushToken: st.PushToken, WakeReplay: st.WakeReplay}
}

func keptContainerOf(st State) keptContainer {
	c := keptContainer{PendingOps: st.PendingOps}
	for epoch, ceiling := range st.SendSeq {
		c.SendSeq = append(c.SendSeq, sendSeqRecord{Epoch: epoch, Ceiling: ceiling})
	}
	sort.Slice(c.SendSeq, func(i, j int) bool { return c.SendSeq[i].Epoch < c.SendSeq[j].Epoch })
	for b, seq := range st.Receive {
		c.Receive = append(c.Receive, receiveRecord{Sender: hex.EncodeToString(b.Sender[:]), Epoch: b.Epoch, Seq: seq})
	}
	sort.Slice(c.Receive, func(i, j int) bool {
		if c.Receive[i].Sender != c.Receive[j].Sender {
			return c.Receive[i].Sender < c.Receive[j].Sender
		}
		return c.Receive[i].Epoch < c.Receive[j].Epoch
	})
	return c
}

func purgeableContainerOf(st State) purgeableContainer {
	return purgeableContainer{Sessions: st.Sessions, Snapshots: st.Snapshots, OpOutcomes: st.OpOutcomes}
}

// dropContentMaterial returns st holding exactly what a process that could NOT OPEN the
// content tier holds: no epoch content key, and none of the coordinates or caches sealed under
// it. That equivalence is the definition of the lock, not a coincidence -- see PurgeKeys.
//
// The three DECRYPTED CACHES are what PB-KEY-7 names. OpOutcomes is among them because it IS
// the decrypted reply cache the requirement lists beside sessions and snapshots --
// MailboxRouter.rebind rebuilds r.replies from it, so leaving it behind means the purge's own
// rebind puts the replies back. The ops it resolves are the stated cost: PB-SYNC-2 settles an
// operation by its durable outcome "or the stream stays unresolved", and a queued op that
// re-sends carries the same operation id.
//
// The three NON-PURGEABLE coordinates go from MEMORY ONLY, and their durable container is
// carried verbatim (PurgeKeys). Forgetting them is not optional: this process can no longer
// re-seal what it cannot open, so a Save that still carried them would be refused wholesale by
// refuseUnreadableContentWrite -- including the wake path's own RelayCursor advance, which has
// nothing to do with the content tier. A locked load holds nothing here for exactly the same
// reason, and every Save has coped with that since S15.
//
// THE WAKE KEY IS NOT TOUCHED. A high-priority FCM push is the sole background wake path and
// arrives with nobody there (ADR-007 B9/B16), so its KEK is deliberately not auth-gated and its
// key must stay usable across a screen lock. Clearing it would stop the handset being wakeable
// at the first lock, and nothing on the device could put it back (ADR-007 B35).
//
// StaleStreams deliberately SURVIVES. It is not decrypted content -- it is the record that a
// channel has a hole in it -- and dropping it would make the first screen lock promote every
// known-holed stream back to live, which is the lie PB-APP-8 forbids.
func dropContentMaterial(st State) State {
	st.Keys.ContentKey = crypto.ContentKey{}
	st.Sessions, st.Snapshots, st.OpOutcomes = nil, nil, nil
	st.SendSeq, st.Receive, st.PendingOps = nil, nil, nil
	return st
}

// PurgeKeys is PB-KEY-7's lock purge: it returns the CONTENT tier to LOCKED and destroys the
// decrypted caches it protected, in memory and at rest. Nothing is unsealed and the content KEK
// is never consulted -- a purge that needed the biometric could not run at the screen lock that
// triggers it.
//
// IT DOES NOT DESTROY THE SEALED CONTENT KEY, and that is the decision rather than a shortcut.
// ADR-007 B35 established that destroying it is unimplementable as specified: PB-KEY-10 moved
// epoch-key delivery entirely into Go, so nothing on the handset holds those bytes, and
// GrantEpoch/GrantSeq survive any purge (they are the replay guard) -- so the machine
// re-appending the very same signed grant next session is refused as a replay, forever. Wired
// as it stood, the FIRST SCREEN LOCK landed the phone in PB-KEY-3's terminal state, exitable
// only by physical access to the machine. Against that cost, destroying the blob buys nothing:
// it is already sealed under an auth-gated Keystore KEK, so a locked handset cannot open it,
// and an attacker who has defeated Keystore already holds device.key and the COMMAND_SIGN seed.
//
// SO THE LOCK IS A TRANSITION TO A STATE THE DESIGN ALREADY MODELS -- the one a process woken
// by a push is in, having never opened the tier. The sealed content key and ContentKept are
// carried VERBATIM by every subsequent Save, which is what an unopened tier has always meant,
// and UnsealContent is the way back: a fresh Keystore unwrap, which is PB-KEY-7's own recovery
// clause and the round trip PB-SEC-2's 60-second window is enforced at.
//
// THE MEMORY HALF IS UNCONDITIONAL, and it happens first. It cannot fail, and PB-KEY-7 lists
// it first; gating it behind the durable write -- which can fail, on a full disk or a data
// directory that has gone read-only -- left the key live and bound with the screen locked.
// "In-memory advances only once the write succeeded" is right for a Save and backwards here: a
// Save must not claim what is not durable, a purge must not KEEP what it was told to destroy.
// The durable failure is still returned; the caches at rest genuinely did survive it.
//
// THE PURGEABLE CONTAINER IS DROPPED AND ITS RECORD SAYS SO -- opened, holding nothing -- so a
// later Save writes no field rather than resurrecting the blob just destroyed, and a Save after
// a failed write finishes the job. That is also why the caches are the only thing destroyed at
// rest: they cost nothing, because PB-SYNC-2 re-derives every one of them by resync.
func (s *fileStore) PurgeKeys() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeGen++
	s.st = dropContentMaterial(s.st.clone())
	s.st.purgeGen = s.purgeGen
	// LOCKED, not destroyed: the blobs stay, this process forgets what was in them.
	s.contentTier.opened = false
	s.kept.opened = false
	// Opened, holding nothing: the decrypted caches are gone for good.
	s.purgeable = stateTier{opened: true}
	if s.path == "" {
		return nil
	}
	// The wake tier is sealed from what is STILL HELD rather than carried blindly: a wake key
	// installed and not yet Saved would otherwise be lost at rest by the one lock that must not
	// touch it. Sealing under the wake KEK needs no user present, which is its whole point.
	wake, err := resealTier(s.wake, s.st.Keys.WakeKey[:], s.wakeTier, s.st.EpochID)
	if err != nil {
		return fmt.Errorf("seal wake key: %w", err)
	}
	s.wakeTier = wake
	return persistState(s.path, s.st, stateSeals{
		wakeKey: wake.blob, contentKey: s.contentTier.blob,
		wakeState: s.wakeState.blob, kept: s.kept.blob,
	})
}

// UnsealContent re-opens the content tier IN PLACE: PB-KEY-7's "require a fresh unwrap before
// restoring content", and the only way back from PurgeKeys.
//
// EVERY CONTAINER IS OPENED BEFORE ANYTHING IS ADOPTED. A partial restore would put the epoch
// key back while the send-seq ceiling stayed at zero, and the phone would renumber from 1 under
// an epoch the gateway already holds a high-water for -- every frame it sends stale-dropped for
// the life of that epoch. There is no useful half of this operation.
//
// A REFUSAL IS RETURNED VERBATIM and changes nothing, so a core that could not authenticate
// stays exactly locked. crypto.ErrKeyAuthRequired is what a locked handset and a lapsed
// 60-second window both produce, and it is the one PB-SEC-2 turns into a prompt; anything else
// is the permanent verdict or a blob that is not ours, and none of them may be guessed at.
//
// The epoch test is resealTier's fourth case from the other side: a key is meaningful only for
// the epoch it was sealed for, so a blob left over from a rotation is dropped rather than
// installed as a plausible key that decrypts nothing.
func (s *fileStore) UnsealContent() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.contentTier.opened && s.kept.opened && s.purgeable.opened {
		return nil
	}
	st := s.st.clone()

	key := s.contentTier
	switch {
	case key.opened:
	case len(key.blob) == 0, key.epoch != st.EpochID:
		key = sealedTier{opened: true}
	default:
		plain, err := s.content.Open(key.blob)
		if err != nil {
			return err
		}
		if len(plain) != len(crypto.ContentKey{}) {
			return fmt.Errorf("%w: %s: the sealed content key is %d bytes, want %d",
				ErrCorruptState, s.path, len(plain), len(crypto.ContentKey{}))
		}
		copy(st.Keys.ContentKey[:], plain)
		key.opened = true
	}

	if !s.kept.opened {
		if len(s.kept.blob) == 0 {
			s.kept.opened = true
		} else {
			plain, err := s.content.Open(s.kept.blob)
			if err != nil {
				return err
			}
			if err := s.adoptKeptContainer(&st, plain); err != nil {
				return err
			}
		}
	}

	// Nothing to re-open: the caches were DESTROYED rather than locked, and PB-SYNC-2's resync
	// is what brings them back. Recording the container as opened-and-empty is what stops the
	// next Save carrying a blob that no longer exists.
	s.purgeable = stateTier{opened: true}
	s.contentTier = key
	s.st = st
	return nil
}

// reopenKept re-opens the non-purgeable content container once the tier has become readable
// again, and is a no-op while it has not. A custody refusal leaves the record untouched -- the
// tier is simply still locked, which is the wake path's normal condition and not an error --
// so only a blob that is not ours is reported.
func (s *fileStore) reopenKept(st *State) error {
	if len(s.kept.blob) == 0 {
		s.kept.opened = true
		return nil
	}
	plain, err := s.content.Open(s.kept.blob)
	if err != nil {
		if errors.Is(err, crypto.ErrKeyAuthRequired) || errors.Is(err, crypto.ErrKeyInvalidated) {
			return nil
		}
		return fmt.Errorf("%w: %s: unseal content state: %v", ErrCorruptState, s.path, err)
	}
	return s.adoptKeptContainer(st, plain)
}

// adoptKeptContainer decodes the non-purgeable content container into st and records the tier
// as opened.
//
// The replay guards are MERGED (applySendSeq and applyReceive both take the maximum), because
// the caller may legitimately hold a higher coordinate than the blob does. PendingOps is not a
// coordinate and cannot be merged, so a caller that is carrying its own queue keeps it: this
// runs on the path where a restored key re-opens the container, and the durable queue is only
// the answer when the caller has none.
func (s *fileStore) adoptKeptContainer(st *State, plain []byte) error {
	var c keptContainer
	if err := json.Unmarshal(plain, &c); err != nil {
		return fmt.Errorf("%w: %s: content state container: %v", ErrCorruptState, s.path, err)
	}
	applySendSeq(st, c.SendSeq)
	if err := applyReceive(st, c.Receive); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrCorruptState, s.path, err)
	}
	if len(st.PendingOps) == 0 {
		st.PendingOps = c.PendingOps
	}
	s.kept.opened = true
	return nil
}

// resealTier returns the tier field to write, from three cases that must stay distinct.
// Two of them used to share one signal -- an all-zero key meant both "I have nothing to
// write" and "destroy this" -- and the collapse was live in both directions.
//
//	a real key in hand   -> SEAL IT, whatever this process could make of the previous blob.
//	                        A content key installed once the user finally authenticates
//	                        arrives while the tier record still says "could not open", and it
//	                        must reach disk or the phone restarts on the old epoch's key and
//	                        decrypts nothing (PB-KEY-3). A real key always wins.
//	no key, unopened blob-> carry it VERBATIM, but only for the epoch it belongs to. The zero
//	                        is a key this process could not READ, not one that is not there,
//	                        and re-sealing it would destroy the epoch. This is the wake path's
//	                        NORMAL condition: it runs with the content tier locked while any
//	                        send reserves a seq and so Saves.
//	no key, nothing held -> write no field at all.
//
// THE EPOCH TEST IS THE FOURTH CASE, and it is a rotation. mobile.App.pin zeroes State.Keys
// deliberately when a pairing lands in a different epoch -- the tier keys belong to the old
// one -- while a process that came up on a push still has contentTier.opened == false. Without
// the test that Save carries the OLD epoch's sealed content key back under the NEW epoch id: a
// plausible-looking key that decrypts nothing, indistinguishable from an epoch the phone
// legitimately holds no key for. A key is only ever carried into the epoch it was sealed for.
//
// DESTROYING a KEY tier is deliberately not among them, and after ADR-007 B35 nothing does it:
// PurgeKeys returns the content tier to LOCKED (blob retained, record unopened, so the second
// case carries it) and destroys only the decrypted caches. The one path that leaves a key
// behind is the rotation above, where the blob belongs to an epoch that is over.
func resealTier(sl Sealer, key []byte, prev sealedTier, epoch uint32) (sealedTier, error) {
	zero := true
	for _, b := range key {
		if b != 0 {
			zero = false
			break
		}
	}
	if zero {
		if !prev.opened && len(prev.blob) > 0 && prev.epoch == epoch {
			return prev, nil
		}
		return sealedTier{opened: true, epoch: epoch}, nil
	}
	blob, err := sl.Seal(key)
	if err != nil {
		return sealedTier{}, err
	}
	return sealedTier{blob: blob, opened: true, epoch: epoch}, nil
}

// mergeGuards returns next with every replay-guard coordinate raised to at least the value
// already in cur.
func mergeGuards(cur, next State) State {
	for epoch, ceiling := range cur.SendSeq {
		if ceiling > next.SendSeq[epoch] {
			if next.SendSeq == nil {
				next.SendSeq = map[uint32]uint64{}
			}
			next.SendSeq[epoch] = ceiling
		}
	}
	for b, seq := range cur.Receive {
		if seq > next.Receive[b] {
			if next.Receive == nil {
				next.Receive = map[Bucket]uint64{}
			}
			next.Receive[b] = seq
		}
	}
	if cur.GrantEpoch > next.GrantEpoch || (cur.GrantEpoch == next.GrantEpoch && cur.GrantSeq > next.GrantSeq) {
		next.GrantEpoch, next.GrantSeq = cur.GrantEpoch, cur.GrantSeq
	}
	if cur.WakeReplay > next.WakeReplay {
		next.WakeReplay = cur.WakeReplay
	}
	if cur.RelayCursor > next.RelayCursor {
		next.RelayCursor = cur.RelayCursor
	}
	return next
}

// load reads, validates and unseals the persisted blob (see OpenStore for the fail-closed
// rules) into the store's in-memory state and tier fields.
func (s *fileStore) load() error {
	path, machineID := s.path, s.machine
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// First run. The state OpenStore constructed stands, machine id included -- see the
		// initialiser paragraph there for why leaving it empty was a silent brick.
		return nil
	}
	if err != nil {
		return fmt.Errorf("read phone state: %w", err)
	}
	var f stateFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrCorruptState, path, err)
	}
	if f.SchemaVersion > StateSchemaVersion {
		return fmt.Errorf("%w: %s: schema version %d (this build knows %d)",
			ErrFutureSchema, path, f.SchemaVersion, StateSchemaVersion)
	}
	if f.SchemaVersion < 1 {
		return fmt.Errorf("%w: %s: unversioned blob", ErrCorruptState, path)
	}
	if machineID != "" && f.Machine != machineID {
		// Another machine's blob: discarded wholesale, and the state OpenStore constructed
		// stands -- same reasoning as the first-run return above. The re-pair that follows must
		// not write a blob stamped with an empty machine, or it discards itself next launch.
		return nil
	}
	// Before v3 the two epoch keys were CLEARTEXT in these same fields. Reading them as
	// sealed blobs would be exactly the silent reinterpretation the version guard exists
	// to refuse, so a pre-seal blob that carries either one is refused outright. Checked
	// AFTER the machine test: another machine's blob is discarded wholesale either way,
	// and erroring on it would brick the re-pair that case exists to keep working.
	// The remedy named here is the one the user can actually reach. "Re-pair the device" is
	// not: this error fails Resume, so the app never starts and never offers a re-pair. Only
	// clearing the app's data removes the blob that is refusing to load, and re-pairing is
	// what happens after that, not instead of it.
	if f.SchemaVersion < 3 && (len(f.WakeKey) > 0 || len(f.ContentKey) > 0) {
		return fmt.Errorf("%w: %s: schema version %d holds unsealed epoch keys (PB-SEC-1); clear the app's "+
			"data to discard them, then pair again", ErrCorruptState, path, f.SchemaVersion)
	}

	st := State{
		Machine:             f.Machine,
		MachineStatic:       f.MachineStatic,
		MachineSignPub:      f.MachineSignPub,
		MachineRelayAuthPub: f.MachineRelayAuthPub,
		RoutingID:           f.RoutingID,
		EpochID:             f.EpochID,
		PushPreference:      f.PushPreference,
		ReconciledEpoch:     f.ReconciledEpoch,
		GrantEpoch:          f.GrantEpoch,
		GrantSeq:            f.GrantSeq,
		RelayCursor:         f.RelayCursor,
		// The pre-v5 cleartext copies. A v5 blob carries none of them (the same coordinates
		// arrive from the sealed containers below), so this is the forward migration and not a
		// second source: an installed v4 blob loads with its replay guard intact and the first
		// Save after the upgrade seals it. The cleartext copy stays on disk until that Save,
		// which is inherent to migrating a file rather than rewriting it at load.
		PushToken:  f.LegacyPushToken,
		WakeReplay: f.LegacyWakeReplay,
		Sessions:   f.LegacySessions,
		Snapshots:  f.LegacySnapshots,
		PendingOps: f.LegacyPendingOps,
		OpOutcomes: f.LegacyOpOutcomes,
	}
	applySendSeq(&st, f.LegacySendSeq)
	if err := applyReceive(&st, f.LegacyReceive); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrCorruptState, path, err)
	}
	// The WAKE tier opens with no user present -- that is its whole purpose -- so one that
	// will not open means this blob is not ours, and starting from an empty checkpoint
	// would leave the replay guard blind. The CONTENT tier legitimately refuses (the phone
	// comes up on a push before any biometric): the phone then holds no content key, and
	// the durable blob is carried through every Save untouched until a tier that can read
	// it arrives.
	if len(f.WakeKey) > 0 {
		s.wakeTier.blob, s.wakeTier.epoch = f.WakeKey, f.EpochID
		plain, oerr := s.wake.Open(f.WakeKey)
		if oerr != nil {
			return fmt.Errorf("%w: %s: unseal wake key: %v", ErrCorruptState, path, oerr)
		}
		copy(st.Keys.WakeKey[:], plain)
		s.wakeTier.opened = true
	}
	if len(f.ContentKey) > 0 {
		s.contentTier.blob, s.contentTier.epoch = f.ContentKey, f.EpochID
		plain, oerr := s.content.Open(f.ContentKey)
		switch {
		case oerr == nil:
			copy(st.Keys.ContentKey[:], plain)
			s.contentTier.opened = true
		case errors.Is(oerr, crypto.ErrKeyAuthRequired), errors.Is(oerr, crypto.ErrKeyInvalidated):
		default:
			return fmt.Errorf("%w: %s: unseal content key: %v", ErrCorruptState, path, oerr)
		}
	}
	// The three sealed STATE containers, opened under the same rules as the keys above: the
	// wake one must open or the blob is not ours, the two content ones may legitimately
	// refuse and are then carried through every Save untouched (see stateTier).
	if err := s.loadWakeState(&st, f.WakeState, path); err != nil {
		return err
	}
	if err := s.loadContentState(&st, f, path); err != nil {
		return err
	}
	if len(f.Stale) > 0 {
		st.Stale = make(map[Bucket]bool, len(f.Stale))
		for _, rec := range f.Stale {
			b, err := decodeBucket(rec.Sender, rec.Epoch)
			if err != nil {
				return fmt.Errorf("%w: %s: %v", ErrCorruptState, path, err)
			}
			st.Stale[b] = true
		}
	}
	if len(f.StaleStreams) > 0 {
		st.StaleStreams = make(map[string]bool, len(f.StaleStreams))
		for _, name := range f.StaleStreams {
			st.StaleStreams[name] = true
		}
	}
	s.st = st
	return nil
}

func decodeBucket(sender string, epoch uint32) (Bucket, error) {
	b := Bucket{Epoch: epoch}
	raw, err := hex.DecodeString(sender)
	if err != nil || len(raw) > 8 {
		return Bucket{}, fmt.Errorf("malformed sender key id %q", sender)
	}
	copy(b.Sender[:], raw)
	return b, nil
}

// applySendSeq folds one set of send-seq records into st, highest ceiling per epoch wins.
// It is shared by the sealed container and the pre-v5 cleartext array so a migrated blob and
// a current one cannot decode by two different rules.
func applySendSeq(st *State, recs []sendSeqRecord) {
	if len(recs) == 0 {
		return
	}
	if st.SendSeq == nil {
		st.SendSeq = make(map[uint32]uint64, len(recs))
	}
	for _, rec := range recs {
		if rec.Ceiling > st.SendSeq[rec.Epoch] {
			st.SendSeq[rec.Epoch] = rec.Ceiling
		}
	}
}

// applyReceive is applySendSeq's mirror for the per-bucket receive high-waters.
func applyReceive(st *State, recs []receiveRecord) error {
	if len(recs) == 0 {
		return nil
	}
	if st.Receive == nil {
		st.Receive = make(map[Bucket]uint64, len(recs))
	}
	for _, rec := range recs {
		b, err := decodeBucket(rec.Sender, rec.Epoch)
		if err != nil {
			return err
		}
		if rec.Seq > st.Receive[b] {
			st.Receive[b] = rec.Seq
		}
	}
	return nil
}

// loadWakeState unseals the wake-tier state container into st. An unopenable wake tier is
// ErrCorruptState for the same reason the wake KEY is: the wake KEK opens with no user
// present, so one that refuses says the blob is not ours, and coming up without the push
// dedup coordinate would re-open every wake the provider still retains (PB-PUSH-3).
func (s *fileStore) loadWakeState(st *State, blob []byte, path string) error {
	if len(blob) == 0 {
		// Nothing there, and this process knows it: a later Save writes the container from
		// what it holds rather than carrying an absent blob.
		s.wakeState = stateTier{opened: true}
		return nil
	}
	s.wakeState = stateTier{blob: blob}
	plain, err := s.wake.Open(blob)
	if err != nil {
		return fmt.Errorf("%w: %s: unseal wake state: %v", ErrCorruptState, path, err)
	}
	var w wakeContainer
	if err := json.Unmarshal(plain, &w); err != nil {
		return fmt.Errorf("%w: %s: wake state container: %v", ErrCorruptState, path, err)
	}
	st.PushToken, st.WakeReplay = w.PushToken, w.WakeReplay
	s.wakeState.opened = true
	return nil
}

// loadContentState unseals the two content-tier containers into st. Either may legitimately
// refuse -- the phone comes up on a push before any biometric -- and a container this process
// could not open stays unopened, so every Save carries it verbatim rather than resealing the
// emptiness the refusal produced. Only a refusal that is NOT a custody verdict says the blob
// is not ours.
func (s *fileStore) loadContentState(st *State, f stateFile, path string) error {
	tier, plain, err := s.openContentContainer(f.ContentKept, path, "content state")
	if err != nil {
		return err
	}
	s.kept = tier
	if plain != nil {
		var c keptContainer
		if err := json.Unmarshal(plain, &c); err != nil {
			return fmt.Errorf("%w: %s: content state container: %v", ErrCorruptState, path, err)
		}
		applySendSeq(st, c.SendSeq)
		if err := applyReceive(st, c.Receive); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrCorruptState, path, err)
		}
		st.PendingOps = c.PendingOps
	}

	tier, plain, err = s.openContentContainer(f.ContentPurgeable, path, "decrypted caches")
	if err != nil {
		return err
	}
	s.purgeable = tier
	if plain != nil {
		var c purgeableContainer
		if err := json.Unmarshal(plain, &c); err != nil {
			return fmt.Errorf("%w: %s: decrypted cache container: %v", ErrCorruptState, path, err)
		}
		st.Sessions, st.Snapshots, st.OpOutcomes = c.Sessions, c.Snapshots, c.OpOutcomes
	}
	return nil
}

// openContentContainer attempts one content-tier unseal, returning the tier record to keep
// and the plaintext -- nil when the container is absent or the tier is locked, which are the
// two cases with nothing to decode and opposite consequences for the next Save.
func (s *fileStore) openContentContainer(blob []byte, path, what string) (stateTier, []byte, error) {
	if len(blob) == 0 {
		return stateTier{opened: true}, nil, nil
	}
	plain, err := s.content.Open(blob)
	switch {
	case err == nil:
		return stateTier{blob: blob, opened: true}, plain, nil
	case errors.Is(err, crypto.ErrKeyAuthRequired), errors.Is(err, crypto.ErrKeyInvalidated):
		return stateTier{blob: blob}, nil, nil
	default:
		return stateTier{}, nil, fmt.Errorf("%w: %s: unseal %s: %v", ErrCorruptState, path, what, err)
	}
}

// stateSeals are the five sealed blobs one write puts in the file: the two epoch keys and
// the three PB-STATE-9 state containers. They are passed together rather than assembled here
// because sealing is the caller's decision -- a container this process could not open is
// carried verbatim, and persistState cannot tell that from one it sealed itself.
type stateSeals struct {
	wakeKey    []byte
	contentKey []byte
	wakeState  []byte
	kept       []byte
	purgeable  []byte
}

// persistState writes the blob atomically. The record slices INSIDE the sealed containers are
// ordered so their plaintext is stable across rewrites (Go map iteration is randomized); the
// file itself is not byte-stable from v5 on and cannot be, because a fresh AEAD nonce per
// seal is what makes the containers safe to rewrite at all.
func persistState(path string, st State, seals stateSeals) error {
	f := stateFile{
		SchemaVersion:       StateSchemaVersion,
		Machine:             st.Machine,
		MachineStatic:       st.MachineStatic,
		MachineSignPub:      st.MachineSignPub,
		MachineRelayAuthPub: st.MachineRelayAuthPub,
		RoutingID:           st.RoutingID,
		EpochID:             st.EpochID,
		PushPreference:      st.PushPreference,
		ReconciledEpoch:     st.ReconciledEpoch,
		WakeKey:             seals.wakeKey,
		ContentKey:          seals.contentKey,
		WakeState:           seals.wakeState,
		ContentKept:         seals.kept,
		ContentPurgeable:    seals.purgeable,
		GrantEpoch:          st.GrantEpoch,
		GrantSeq:            st.GrantSeq,
		RelayCursor:         st.RelayCursor,
	}
	for b, stale := range st.Stale {
		if stale {
			f.Stale = append(f.Stale, bucketRecord{Sender: hex.EncodeToString(b.Sender[:]), Epoch: b.Epoch})
		}
	}
	sort.Slice(f.Stale, func(i, j int) bool {
		if f.Stale[i].Sender != f.Stale[j].Sender {
			return f.Stale[i].Sender < f.Stale[j].Sender
		}
		return f.Stale[i].Epoch < f.Stale[j].Epoch
	})
	for name, stale := range st.StaleStreams {
		if stale {
			f.StaleStreams = append(f.StaleStreams, name)
		}
	}
	sort.Strings(f.StaleStreams)

	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, ".phone-state-*", data)
}

// writeFileAtomic writes data to path durably: a temp file in the SAME directory (so the
// rename is atomic), fsynced, renamed over the target, then the parent directory fsynced so
// the rename itself survives a power loss. It mirrors remotegw.writeFileAtomic, which
// phonecore cannot import (PB-BIND-0 binds this package's dependency closure). Without the
// dir fsync a power loss could resurrect an OLDER blob, and a lower high-water re-opens
// frames the relay retained -- precisely the replay this state exists to refuse.
func writeFileAtomic(path, tmpPattern string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
