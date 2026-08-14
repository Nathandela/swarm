package remotegw

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// InboundStream identifies one phone -> machine mailbox stream by exactly the coordinate
// crypto.MailboxReceiver keys its own high-water map by: the envelope's sender key id and
// epoch id. It is per-EPOCH, not a single scalar, because a RevokeDevice rotates the epoch
// key: the first frame of the new epoch is a seq 1 that a scalar high-water would
// stale-drop, bricking the phone after every revoke.
type InboundStream struct {
	Sender [8]byte
	Epoch  uint32
}

// InboundCheckpoint is the gateway's INBOUND consumption point (PB-GW-1): the highest
// mailbox seq consumed on each (sender, epoch) stream, plus the relay mailbox read cursor
// the next poll resumes from. The two halves are independent defences -- the read cursor
// stops an honest relay's un-purged items being re-read, the per-stream high-water refuses
// a retained frame however the relay chooses to re-serve it (a hostile or buggy relay may
// re-append the identical envelope at a fresh storage cursor).
type InboundCheckpoint struct {
	Cursor  uint64
	Highest map[InboundStream]uint64
}

// InboundState is the durable custody of an InboundCheckpoint. Load cannot fail: custody
// is validated once at OpenInboundState, which fails closed, so a bridge that was
// constructed has already proved its state is readable (this keeps NewCommandBridge
// infallible, mirroring OpenSeqSource / SeqSource).
type InboundState interface {
	Load() InboundCheckpoint
	Save(InboundCheckpoint) error
}

// inboundSchemaVersion stamps the on-disk file so the format can migrate forward. A file
// stamped with anything else is refused rather than reinterpreted.
const inboundSchemaVersion = 1

// inboundFile is the on-disk shape. It is JSON rather than the packed big-endian uint64 of
// the outbound seq file (seqstore.go) because a checkpoint is a variable-length map under a
// compound key, and it is written at most once per CONSUMED inbound frame -- a human-rate
// path, unlike the outbound seq's per-journal-record hot path -- so a self-describing,
// versioned, inspectable encoding costs nothing. It matches the device registry's precedent
// for structured durable state.
type inboundFile struct {
	SchemaVersion int                   `json:"schema_version"`
	Machine       string                `json:"machine"`
	Cursor        uint64                `json:"cursor"`
	Streams       []inboundStreamRecord `json:"streams"`
}

// inboundStreamRecord is one (sender, epoch) high-water. Sender is hex because a [8]byte
// key id is not a JSON map key.
type inboundStreamRecord struct {
	Sender string `json:"sender"`
	Epoch  uint32 `json:"epoch"`
	Seq    uint64 `json:"seq"`
}

// fileInboundState is an InboundState backed by one JSON file, held in memory and
// rewritten atomically on every Save. An empty path makes it purely in-memory (no
// durability): the default for callers that do not provision a state dir (unit tests, the
// skeleton integration harness), mirroring durableSeq.
type fileInboundState struct {
	mu      sync.Mutex
	path    string
	machine string // the identity both coordinates are only meaningful under
	ck      InboundCheckpoint
}

// errCorruptInboundState flags an unreadable or unsupported inbound-state file. Custody
// fails closed exactly like the outbound seq ceiling: silently starting from an empty
// checkpoint would leave the replay guard blind (a fresh receiver skips the staleness check
// entirely) and re-open every frame the relay still retains.
var errCorruptInboundState = errors.New("remotegw: corrupt inbound-state file")

// OpenInboundState opens the durable inbound checkpoint at path, loading any previously
// persisted state. A missing file starts fresh (first run); a present-but-malformed or
// wrongly-versioned file is an error, never a silent reset. An empty path returns a purely
// in-memory state (no durability).
//
// machineID is the identity BOTH coordinates are only meaningful under (production passes
// the machine's relay routing id). A file stamped with a different one is another machine's
// checkpoint and is discarded, not reused: `swarm remote init` regenerates machine.key --
// epoch id back to 1 -- without touching its siblings in <stateDir>/remote, and a reused
// epoch-1 high-water would then stale-drop the freshly paired phone's first frames
// (take_control included) while a reused cursor would point past the end of a mailbox that
// restarted at 1. Both read as a permanently deaf gateway with no error surfaced anywhere.
func OpenInboundState(path, machineID string) (InboundState, error) {
	s := &fileInboundState{path: path, machine: machineID, ck: InboundCheckpoint{Highest: map[InboundStream]uint64{}}}
	if path == "" {
		return s, nil
	}
	ck, err := loadInboundCheckpoint(path, machineID)
	if err != nil {
		return nil, err
	}
	s.ck = ck
	return s, nil
}

// Load returns a copy of the checkpoint, so a caller (the bridge's seeding) can neither
// observe later mutations nor mutate the custody's own map.
func (s *fileInboundState) Load() InboundCheckpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneCheckpoint(s.ck)
}

// Save merges ck into the durable checkpoint MONOTONICALLY -- neither the cursor nor any
// per-stream high-water is ever lowered -- and rewrites the file atomically. Monotonicity is
// fail-closed custody: a caller that regressed (a stale in-memory bridge, a reordered write)
// must not be able to re-open frames already consumed. The in-memory copy is only advanced
// once the write has succeeded, so a failed Save leaves the state exactly as a crashed
// process would have left it: nothing durable, nothing claimed.
func (s *fileInboundState) Save(ck InboundCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	merged := cloneCheckpoint(s.ck)
	if ck.Cursor > merged.Cursor {
		merged.Cursor = ck.Cursor
	}
	for st, seq := range ck.Highest {
		if seq > merged.Highest[st] {
			merged.Highest[st] = seq
		}
	}
	if s.path != "" {
		if err := persistInboundCheckpoint(s.path, s.machine, merged); err != nil {
			return err
		}
	}
	s.ck = merged
	return nil
}

func cloneCheckpoint(ck InboundCheckpoint) InboundCheckpoint {
	out := InboundCheckpoint{Cursor: ck.Cursor, Highest: make(map[InboundStream]uint64, len(ck.Highest))}
	for st, seq := range ck.Highest {
		out.Highest[st] = seq
	}
	return out
}

// loadInboundCheckpoint reads and validates the persisted checkpoint. A missing file is an
// empty checkpoint (first run); anything unparseable, wrongly-versioned, or carrying a
// malformed sender key id is an error; a file stamped with a DIFFERENT machineID is another
// machine's checkpoint, which is an empty checkpoint (not an error -- there is nothing
// corrupt about it, it simply describes coordinates that do not exist here).
func loadInboundCheckpoint(path, machineID string) (InboundCheckpoint, error) {
	empty := InboundCheckpoint{Highest: map[InboundStream]uint64{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, fmt.Errorf("read inbound state: %w", err)
	}
	var f inboundFile
	if err := json.Unmarshal(data, &f); err != nil {
		return empty, fmt.Errorf("%w: %s: %v", errCorruptInboundState, path, err)
	}
	if f.SchemaVersion != inboundSchemaVersion {
		return empty, fmt.Errorf("%w: %s: schema version %d unsupported (want %d)",
			errCorruptInboundState, path, f.SchemaVersion, inboundSchemaVersion)
	}
	if f.Machine != machineID {
		return empty, nil
	}
	ck := InboundCheckpoint{Cursor: f.Cursor, Highest: make(map[InboundStream]uint64, len(f.Streams))}
	for _, rec := range f.Streams {
		raw, err := hex.DecodeString(rec.Sender)
		if err != nil || len(raw) != 8 {
			return empty, fmt.Errorf("%w: %s: malformed sender key id %q", errCorruptInboundState, path, rec.Sender)
		}
		st := InboundStream{Epoch: rec.Epoch}
		copy(st.Sender[:], raw)
		if rec.Seq > ck.Highest[st] {
			ck.Highest[st] = rec.Seq
		}
	}
	return ck, nil
}

// persistInboundCheckpoint writes the checkpoint atomically (temp + fsync + rename + parent
// dir fsync), the same durability idiom persistSeqCeiling uses. Without the dir fsync a
// power loss could resurrect an OLDER checkpoint, and a lower high-water re-opens frames the
// relay retained -- precisely the replay this state exists to refuse.
func persistInboundCheckpoint(path, machineID string, ck InboundCheckpoint) error {
	f := inboundFile{SchemaVersion: inboundSchemaVersion, Machine: machineID, Cursor: ck.Cursor}
	for st, seq := range ck.Highest {
		f.Streams = append(f.Streams, inboundStreamRecord{
			Sender: hex.EncodeToString(st.Sender[:]), Epoch: st.Epoch, Seq: seq,
		})
	}
	// Order by (sender, epoch) so the file is byte-stable across rewrites (Go map
	// iteration is randomized), keeping a diff of the state dir meaningful.
	sort.Slice(f.Streams, func(i, j int) bool {
		if f.Streams[i].Sender != f.Streams[j].Sender {
			return f.Streams[i].Sender < f.Streams[j].Sender
		}
		return f.Streams[i].Epoch < f.Streams[j].Epoch
	})
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, ".inbound-state-*", data)
}

// writeFileAtomic writes data to path durably: a temp file in the SAME directory (so the
// rename is atomic), fsynced, renamed over the target, then the parent directory fsynced so
// the rename itself survives a power loss. tmpPattern is the os.CreateTemp pattern for the
// temp file. Shared by the outbound seq ceiling and the inbound checkpoint -- the two pieces
// of durable state whose silent regression would reuse or re-open a sequence number.
func writeFileAtomic(path, tmpPattern string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
