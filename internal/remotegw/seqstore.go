package remotegw

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
)

// SeqSource hands out the strictly increasing per-(sender,epoch) sequence numbers the
// gateway stamps on OUTBOUND mailbox frames (RelaySink journal/terminal, CommandBridge
// replies). It is the seam that makes the outbound seq DURABLE across a gateway restart
// (committee finding C2b): a file-backed source resumes STRICTLY ABOVE the highest seq the
// phone already accepted, so a restarted gateway never re-emits a seq the phone's durable
// per-(sender,epoch) high-water would stale-drop (crypto.MailboxReceiver.Accept).
type SeqSource interface {
	// Next returns the next sequence number (strictly greater than every prior Next on
	// this and any predecessor over the same durable file). It errors -- and issues NO
	// seq -- if it cannot durably reserve, so a disk fault fails closed rather than risk
	// reusing a seq across a restart.
	Next() (uint64, error)

	// Issued is a SAFE WATERMARK to publish to the phone as a receive high-water
	// (PB-STATE-4(b)): >= every seq already handed out, and < every seq that will ever
	// be handed out, ACROSS A RESTART. It is NOT the durable reservation ceiling, which
	// sits up to a full block above the last frame actually sent -- a phone seeded there
	// would stale-drop every legitimate frame until the gateway caught up.
	Issued() uint64
}

// seqReserveBlock is how many seq values a durableSeq reserves per fsync. seq allocation
// is on the gateway's outbound hot path (every journal record and every terminal
// snapshot), so persisting each one individually would fsync per frame. Instead we persist
// a reservation CEILING once per block and hand out that block from memory; on restart we
// resume at the persisted ceiling (see durableSeq). A larger block trades a wider
// post-restart seq skip (one phone resync, harmless -- a skipped seq is a Gap, not a drop)
// for fewer fsyncs.
const seqReserveBlock uint64 = 64

// durableSeq is a SeqSource backed by a single file holding one big-endian uint64: the
// reservation CEILING (the largest seq value promised never to be reissued on restart).
//
// Correctness (never reuse a seq across a restart): issued only ever advances up to
// reserved, and reserved is only raised AFTER the new ceiling is durably persisted. So at
// every moment issued <= reserved == the last persisted ceiling. On restart we load that
// ceiling C and resume at C+1, which is > every seq ever issued (hence > every seq the
// phone accepted) -- INCLUDING the unflushed tail of the last in-memory block, because
// those were all <= reserved == C. The cost is skipping the unused remainder of a block on
// each restart, which the phone absorbs as a single Gap (resync), never a stale-drop.
//
// A path of "" makes it purely in-memory (no durability): the default for callers that do
// not provision a state dir (existing unit tests, the skeleton integration harness).
type durableSeq struct {
	mu       sync.Mutex
	path     string
	block    uint64
	issued   uint64 // last seq handed out
	reserved uint64 // durable ceiling: issued may advance to here with no new persist
}

// OpenSeqSource opens a durable outbound-seq source at path, resuming above any
// previously persisted reservation ceiling. A missing file starts fresh at 0 (first run);
// a present-but-malformed file fails closed rather than silently resetting to 0 (which
// could reuse a seq). An empty path returns a purely in-memory source (no durability).
func OpenSeqSource(path string) (SeqSource, error) {
	s := &durableSeq{path: path, block: seqReserveBlock}
	if path == "" {
		return s, nil
	}
	ceiling, err := loadSeqCeiling(path)
	if err != nil {
		return nil, err
	}
	// Resume at the durable ceiling: issued == reserved forces the first Next to reserve a
	// fresh block (persisting ceiling+block) and hand out ceiling+1.
	s.issued = ceiling
	s.reserved = ceiling
	return s, nil
}

// Next hands out the next seq. When the in-memory block is exhausted it durably reserves
// the next block (fsync) BEFORE advancing, so every issued seq is <= a persisted ceiling.
func (s *durableSeq) Next() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.issued >= s.reserved {
		next := s.issued + s.block
		if s.path != "" {
			if err := persistSeqCeiling(s.path, next); err != nil {
				return 0, err
			}
		}
		s.reserved = next
	}
	s.issued++
	return s.issued, nil
}

// Issued is the highest seq handed out. Across a restart OpenSeqSource resumes issued at
// the persisted reservation ceiling, so the watermark JUMPS (the unused tail of the last
// block is burned) but never falls below an already-issued seq -- which is exactly the
// two-sided property the phone needs to seed a receive high-water on.
func (s *durableSeq) Issued() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issued
}

// errCorruptSeqFile flags a seq file whose length is not the expected 8 bytes. Custody of
// the outbound-seq ceiling fails closed: a truncated/garbage file is an error, never a
// silent reset to 0 (which would reuse seqs and re-freeze the phone -- the very bug C2b).
var errCorruptSeqFile = errors.New("remotegw: corrupt outbound-seq file")

// loadSeqCeiling reads the persisted 8-byte big-endian reservation ceiling. A missing file
// is 0 (first run). Any other read error, or a wrong length, is returned.
func loadSeqCeiling(path string) (uint64, error) {
	buf, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read outbound-seq: %w", err)
	}
	if len(buf) != 8 {
		return 0, fmt.Errorf("%w: %s (%d bytes)", errCorruptSeqFile, path, len(buf))
	}
	return binary.BigEndian.Uint64(buf), nil
}

// persistSeqCeiling atomically writes the 8-byte big-endian ceiling through
// writeFileAtomic (temp + fsync + rename + dir fsync, inboundstate.go). Without the dir
// fsync a power loss could resurrect an OLDER ceiling and reuse seqs -- so the durability
// guarantee the resume-higher scheme relies on would not hold.
func persistSeqCeiling(path string, ceiling uint64) error {
	var val [8]byte
	binary.BigEndian.PutUint64(val[:], ceiling)
	return writeFileAtomic(path, ".outbound-seq-*", val[:])
}
