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
const StateSchemaVersion = 3

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
}

// PushPreference is PB-APP-7's pair of coarse notification toggles, persisted so the
// settings screen renders the user's choice after a restart rather than a default.
type PushPreference struct {
	Alerts   bool `json:"alerts"`
	Mentions bool `json:"mentions"`
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
	return s
}

// Store is the phone's durable custody of State. Load cannot fail: the blob is validated
// once at OpenStore, which fails closed, so a Store that was constructed has already
// proved its state readable (mirroring remotegw.InboundState).
type Store interface {
	Load() State
	Save(State) error
}

// ---------------------------------------------------------------------------
// On-disk shape. JSON, versioned and self-describing: the blob is written at most once
// per consumed frame or per reserved seq BLOCK (never per keystroke), so an inspectable
// encoding costs nothing. Maps travel as arrays because a compound key is not a JSON
// object key -- the same choice remotegw/inboundstate.go made for the mirror direction.
// ---------------------------------------------------------------------------

type stateFile struct {
	SchemaVersion       int    `json:"schema_version"`
	Machine             string `json:"machine"`
	MachineStatic       []byte `json:"machine_static,omitempty"`
	MachineSignPub      []byte `json:"machine_sign_pub,omitempty"`
	MachineRelayAuthPub []byte `json:"machine_relay_auth_pub,omitempty"`
	RoutingID           string `json:"routing_id"`
	EpochID             uint32 `json:"epoch_id"`

	PushToken       string         `json:"push_token,omitempty"`
	PushPreference  PushPreference `json:"push_preference,omitzero"`
	ReconciledEpoch uint32         `json:"reconciled_epoch,omitempty"`

	// WakeKey and ContentKey are SEALED blobs from v3 on, each under its own tier KEK
	// (PB-KEY-9): one file cannot be gated two ways, and a content key recoverable
	// without the biometric collapses the tier split the design exists for.
	WakeKey     []byte                    `json:"wake_key,omitempty"`
	ContentKey  []byte                    `json:"content_key,omitempty"`
	SendSeq     []sendSeqRecord           `json:"send_seq"`
	Receive     []receiveRecord           `json:"receive"`
	GrantEpoch  uint32                    `json:"grant_epoch"`
	GrantSeq    uint64                    `json:"grant_seq"`
	WakeReplay  uint64                    `json:"wake_replay"`
	RelayCursor uint64                    `json:"relay_cursor"`
	Sessions    []CachedSession           `json:"sessions,omitempty"`
	Snapshots   []Snapshot                `json:"snapshots,omitempty"`
	PendingOps  []QueuedOp                `json:"pending_ops,omitempty"`
	OpOutcomes  map[string]schema.Control `json:"op_outcomes,omitempty"`
	Stale       []bucketRecord            `json:"stale,omitempty"`
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
}

// sealedTier is one tier's key field as it stands on disk, plus whether this process was
// able to OPEN it. A tier it could not open is rewritten VERBATIM by the next Save:
// re-sealing the zero value the process is holding would destroy a key it merely could not
// read, and that is the wake path's normal condition -- it runs with the content tier locked
// while any send reserves a seq and therefore Saves.
type sealedTier struct {
	blob   []byte
	opened bool
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
// wake and content are PB-KEY-2's tier KEKs and are REQUIRED for any real path: writing the
// epoch keys in the clear is the defect PB-SEC-1 names, so their absence is ErrNoSealer
// rather than a silent cleartext blob (ADR-007 B18(c)). An empty path persists nothing, so
// there is nothing at rest to seal and no sealer is needed.
func OpenStore(path, machineID string, wake, content Sealer) (Store, error) {
	s := &fileStore{path: path, machine: machineID, wake: wake, content: content}
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

// Load returns a copy of the persisted state.
func (s *fileStore) Load() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.clone()
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

	merged := mergeGuards(s.st, st.clone())
	if s.path != "" {
		wake, err := resealTier(s.wake, merged.Keys.WakeKey[:], s.wakeTier)
		if err != nil {
			return fmt.Errorf("seal wake key: %w", err)
		}
		content, err := resealTier(s.content, merged.Keys.ContentKey[:], s.contentTier)
		if err != nil {
			return fmt.Errorf("seal content key: %w", err)
		}
		if err := persistState(s.path, merged, wake.blob, content.blob); err != nil {
			return err
		}
		s.wakeTier, s.contentTier = wake, content
	}
	s.st = merged
	return nil
}

// resealTier returns the tier field to write. An all-zero key is written as NO field at all
// -- a lock purge must take the durable copy with it (PB-KEY-7) -- while a tier this process
// could not open is carried through untouched (see sealedTier).
func resealTier(sl Sealer, key []byte, prev sealedTier) (sealedTier, error) {
	if !prev.opened && len(prev.blob) > 0 {
		return prev, nil
	}
	zero := true
	for _, b := range key {
		if b != 0 {
			zero = false
			break
		}
	}
	if zero {
		return sealedTier{opened: true}, nil
	}
	blob, err := sl.Seal(key)
	if err != nil {
		return sealedTier{}, err
	}
	return sealedTier{blob: blob, opened: true}, nil
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
		return nil
	}
	// Before v3 the two epoch keys were CLEARTEXT in these same fields. Reading them as
	// sealed blobs would be exactly the silent reinterpretation the version guard exists
	// to refuse, so a pre-seal blob that carries either one is refused outright. Checked
	// AFTER the machine test: another machine's blob is discarded wholesale either way,
	// and erroring on it would brick the re-pair that case exists to keep working.
	if f.SchemaVersion < 3 && (len(f.WakeKey) > 0 || len(f.ContentKey) > 0) {
		return fmt.Errorf("%w: %s: schema version %d holds unsealed epoch keys (PB-SEC-1); re-pair the device",
			ErrCorruptState, path, f.SchemaVersion)
	}

	st := State{
		Machine:             f.Machine,
		MachineStatic:       f.MachineStatic,
		MachineSignPub:      f.MachineSignPub,
		MachineRelayAuthPub: f.MachineRelayAuthPub,
		RoutingID:           f.RoutingID,
		EpochID:             f.EpochID,
		PushToken:           f.PushToken,
		PushPreference:      f.PushPreference,
		ReconciledEpoch:     f.ReconciledEpoch,
		GrantEpoch:          f.GrantEpoch,
		GrantSeq:            f.GrantSeq,
		WakeReplay:          f.WakeReplay,
		RelayCursor:         f.RelayCursor,
		Sessions:            f.Sessions,
		Snapshots:           f.Snapshots,
		PendingOps:          f.PendingOps,
		OpOutcomes:          f.OpOutcomes,
	}
	// The WAKE tier opens with no user present -- that is its whole purpose -- so one that
	// will not open means this blob is not ours, and starting from an empty checkpoint
	// would leave the replay guard blind. The CONTENT tier legitimately refuses (the phone
	// comes up on a push before any biometric): the phone then holds no content key, and
	// the durable blob is carried through every Save untouched until a tier that can read
	// it arrives.
	if len(f.WakeKey) > 0 {
		s.wakeTier.blob = f.WakeKey
		plain, oerr := s.wake.Open(f.WakeKey)
		if oerr != nil {
			return fmt.Errorf("%w: %s: unseal wake key: %v", ErrCorruptState, path, oerr)
		}
		copy(st.Keys.WakeKey[:], plain)
		s.wakeTier.opened = true
	}
	if len(f.ContentKey) > 0 {
		s.contentTier.blob = f.ContentKey
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
	if len(f.SendSeq) > 0 {
		st.SendSeq = make(map[uint32]uint64, len(f.SendSeq))
		for _, rec := range f.SendSeq {
			if rec.Ceiling > st.SendSeq[rec.Epoch] {
				st.SendSeq[rec.Epoch] = rec.Ceiling
			}
		}
	}
	if len(f.Receive) > 0 {
		st.Receive = make(map[Bucket]uint64, len(f.Receive))
		for _, rec := range f.Receive {
			b, err := decodeBucket(rec.Sender, rec.Epoch)
			if err != nil {
				return fmt.Errorf("%w: %s: %v", ErrCorruptState, path, err)
			}
			if rec.Seq > st.Receive[b] {
				st.Receive[b] = rec.Seq
			}
		}
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

// persistState writes the blob atomically. The record slices are ordered so the file is
// byte-stable across rewrites (Go map iteration is randomized), keeping a diff of the state
// dir meaningful.
func persistState(path string, st State, wakeKey, contentKey []byte) error {
	f := stateFile{
		SchemaVersion:       StateSchemaVersion,
		Machine:             st.Machine,
		MachineStatic:       st.MachineStatic,
		MachineSignPub:      st.MachineSignPub,
		MachineRelayAuthPub: st.MachineRelayAuthPub,
		RoutingID:           st.RoutingID,
		EpochID:             st.EpochID,
		PushToken:           st.PushToken,
		PushPreference:      st.PushPreference,
		ReconciledEpoch:     st.ReconciledEpoch,
		WakeKey:             wakeKey,
		ContentKey:          contentKey,
		GrantEpoch:          st.GrantEpoch,
		GrantSeq:            st.GrantSeq,
		WakeReplay:          st.WakeReplay,
		RelayCursor:         st.RelayCursor,
		Sessions:            st.Sessions,
		Snapshots:           st.Snapshots,
		PendingOps:          st.PendingOps,
		OpOutcomes:          st.OpOutcomes,
	}
	for epoch, ceiling := range st.SendSeq {
		f.SendSeq = append(f.SendSeq, sendSeqRecord{Epoch: epoch, Ceiling: ceiling})
	}
	sort.Slice(f.SendSeq, func(i, j int) bool { return f.SendSeq[i].Epoch < f.SendSeq[j].Epoch })
	for b, seq := range st.Receive {
		f.Receive = append(f.Receive, receiveRecord{Sender: hex.EncodeToString(b.Sender[:]), Epoch: b.Epoch, Seq: seq})
	}
	sort.Slice(f.Receive, func(i, j int) bool {
		if f.Receive[i].Sender != f.Receive[j].Sender {
			return f.Receive[i].Sender < f.Receive[j].Sender
		}
		return f.Receive[i].Epoch < f.Receive[j].Epoch
	})
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
