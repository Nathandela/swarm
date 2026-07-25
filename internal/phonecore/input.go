package phonecore

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// InputFrame is the plaintext the phone seals into a mailbox envelope for a
// keystroke burst ("data") or a terminal resize ("resize"). It shares the phone
// -> machine mailbox with commands; the discriminating `t` field lets the
// machine side tell an input frame from a RemoteCommand (which carries no `t`).
//
// Session names the target namespaced session id and is bound INSIDE the sealed
// envelope, so it is authentic end to end: the untrusted relay can drop or reorder
// sealed frames but cannot alter their contents. The machine routes the keystroke
// by THIS field, never by mutable focus state -- an input for a session whose
// take_control the relay dropped then finds no lease and is dropped, never riding
// another session's live lease (A7 cross-session misroute).
type InputFrame struct {
	T       string `json:"t"`           // "data" or "resize"
	Session string `json:"s,omitempty"` // target namespaced session id
	Data    []byte `json:"data,omitempty"`
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
}

// SeqReserveBlock is how many seq values one durable reservation covers (§6.0 binds the
// value). Persisting every seq would cost an fsync per keystroke -- measured at 13-15 ms
// for the equivalent gateway write, against a p50 <= 150 ms typing budget -- so a CEILING
// is persisted once per block and the block is handed out from memory. The cost is that a
// restart burns the unused tail of the block; PB-STATE-8 says which frame absorbs it.
const SeqReserveBlock uint64 = 256

// ErrGapPending refuses an INPUT frame while a burned reservation block is unaccounted
// for. The gateway DROPS a gapped input frame silently (routeInput), so the first
// post-restart keystroke would vanish with no signal anywhere; commands ignore the Gap
// bit, so the re-lease command must absorb it first (PB-STATE-8).
var ErrGapPending = errors.New("phonecore: send-seq gap outstanding; a command must absorb it before input")

// Sequencer hands out the strictly increasing seq numbers (1, 2, 3, ...) that
// stamp EVERY phone -> machine mailbox envelope. Commands AND input frames draw
// from ONE Sequencer per epoch because they share a single MailboxReceiver key
// (SenderKeyID stays zero), so a private per-kind counter would collide.
//
// Bound to a Core it is DURABLE: it resumes at the persisted reservation ceiling for its
// epoch, so no seq is ever re-issued after a process death -- which the gateway's durable
// inbound high-water (PB-GW-1) would otherwise refuse as stale forever. Unbound (the zero
// value) it is the plain in-memory allocator.
type Sequencer struct {
	mu       sync.Mutex
	epoch    uint32
	issued   uint64
	reserved uint64
	gap      bool
	reserve  func(epoch uint32, ceiling uint64) error
}

// bind resumes the sequencer for epoch at its persisted reservation ceiling. A non-zero
// ceiling means a previous generation reserved a block it may not have spent, so a gap is
// owed (PB-STATE-8). Nothing is reserved here: most Android launches never type, and
// reserving at open would burn a block per launch.
func (s *Sequencer) bind(epoch uint32, ceiling uint64, reserve func(uint32, uint64) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch, s.issued, s.reserved, s.gap, s.reserve = epoch, ceiling, ceiling, ceiling > 0, reserve
}

// Next returns the next seq (1 on first call) WITHOUT a durable reservation. It is the
// in-memory allocator; a durable caller uses NextCommand/NextInput, which can report a
// failed reservation. Safe for concurrent use.
func (s *Sequencer) Next() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issued++
	if s.issued > s.reserved {
		s.reserved = s.issued
	}
	return s.issued
}

// NextCommand allocates the next seq for a COMMAND frame and absorbs any outstanding gap:
// the gateway ignores the Gap bit on commands, so the re-lease that follows a restart is
// the frame that pays for the burned block (PB-STATE-8).
func (s *Sequencer) NextCommand() (uint64, error) { return s.next(true) }

// NextInput allocates the next seq for an INPUT frame. While a gap is outstanding it
// refuses with ErrGapPending rather than emit a keystroke the gateway would drop silently.
func (s *Sequencer) NextInput() (uint64, error) { return s.next(false) }

// GapPending reports whether a burned reservation block is still unaccounted for.
func (s *Sequencer) GapPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gap
}

func (s *Sequencer) next(absorbsGap bool) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gap && !absorbsGap {
		return 0, ErrGapPending
	}
	if s.issued >= s.reserved {
		ceiling := s.issued + SeqReserveBlock
		if s.reserve != nil {
			if err := s.reserve(s.epoch, ceiling); err != nil {
				// Fail closed: a seq that was not durably reserved is never issued, because
				// handing one out anyway is how a seq gets reused across a restart.
				return 0, fmt.Errorf("phonecore: reserve send seq: %w", err)
			}
		}
		s.reserved = ceiling
	}
	s.issued++
	s.gap = false
	return s.issued, nil
}

// SeedFrom resumes the sequencer above n, so the next seq is n+1. It is PB-STATE-4(a):
// a rolled-back phone restarts its send-seq low and every command it seals is stale-
// dropped by the gateway's durable inbound high-water (PB-GW-1) -- a permanent refusal
// with no local symptom -- until the reconcile record's InboundHighWater resumes it here.
//
// MONOTONIC: a stale or lower authority (or a reserved-but-unused seq block, PB-STATE-3)
// can never rewind an already-advanced counter, which would re-issue a seq the gateway
// has already consumed. Safe for concurrent use with Next.
func (s *Sequencer) SeedFrom(n uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > s.issued {
		s.issued = n
	}
	if n > s.reserved {
		s.reserved = n
	}
}

// SealInputData seals a keystroke burst as a mailbox INPUT-FRAME envelope under
// the epoch content key so it travels through the untrusted relay as ciphertext,
// mirroring SealCommandEnvelope. session is the target namespaced session id, bound
// inside the sealed frame so the machine routes by it. seq must be unique per epoch
// (from a Sequencer shared with commands).
func SealInputData(key crypto.ContentKey, epochID uint32, seq uint64, session string, data []byte) ([]byte, error) {
	return sealInputFrame(key, epochID, seq, InputFrame{T: "data", Session: session, Data: data})
}

// SealInputResize seals a terminal resize as a mailbox INPUT-FRAME envelope,
// mirroring SealInputData. session is the target namespaced session id; seq must be
// unique per epoch.
func SealInputResize(key crypto.ContentKey, epochID uint32, seq uint64, session string, cols, rows int) ([]byte, error) {
	return sealInputFrame(key, epochID, seq, InputFrame{T: "resize", Session: session, Cols: cols, Rows: rows})
}

func sealInputFrame(key crypto.ContentKey, epochID uint32, seq uint64, f InputFrame) ([]byte, error) {
	plaintext, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version:  crypto.VersionV1,
		EpochID:  epochID,
		Seq:      seq,
		IssuedAt: issuedAt(),
	}, plaintext)
	if err != nil {
		return nil, err
	}
	return env.Marshal(), nil
}

// issuedAt stamps a phone -> machine seal with the wall clock in unix millis (PB-GW-6).
// The field is AAD-covered, so leaving it 0 authenticates a ZERO: PB-GW-2's bounded-age
// check then computes an age of ~56 years and rejects every legitimate command and
// keystroke. The phone stamps first; the toggle is enabled after.
func issuedAt() int64 { return time.Now().UnixMilli() }
