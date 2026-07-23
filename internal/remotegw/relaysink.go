package remotegw

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// The production relay client is a MailboxAppender: the gateway forwards sealed
// envelopes through it. This assertion pins the seam so a relay-client signature
// change is caught at compile time.
var _ MailboxAppender = (*relay.Client)(nil)

// MailboxAppender stores an opaque envelope in a target's relay mailbox. The relay
// Client (internal/remote/relay) satisfies it; the gateway depends only on this narrow
// seam so the sink is testable without a live relay.
type MailboxAppender interface {
	MailboxAppend(ctx context.Context, target string, env []byte) (uint64, error)
}

// RelayConfig configures a RelaySink.
type RelayConfig struct {
	Appender       MailboxAppender
	Target         string            // the phone's relay routing id (mailbox target)
	EpochID        uint32            // the current epoch the content key belongs to
	Key            crypto.ContentKey // K_epoch content key the phone also holds (R-CRY.11)
	RecipientKeyID [8]byte           // routing key id of the phone (recipient)
	SenderKeyID    [8]byte           // routing key id of this machine (sender)
	Now            func() time.Time  // envelope issued-at clock (nil => time.Now)
}

// RelaySink is a JournalSink that forwards the daemon's journal to the phone via the
// untrusted relay (R-GW.3): it seals each record under the epoch content key
// (XChaCha20-Poly1305, so the relay sees only ciphertext) and appends it to the phone's
// mailbox. Envelope Seq is a strictly increasing per-sink counter so the phone can
// order and dedup. Append failures are surfaced via Err(); the durable-cursor /
// relay-ack backpressure (R-GW.5) is a later refinement.
type RelaySink struct {
	cfg RelayConfig
	now func() time.Time

	mu      sync.Mutex
	seq     uint64
	lastErr error
}

// NewRelaySink returns a sink that seals records under cfg.Key and appends them to
// cfg.Target's mailbox via cfg.Appender.
func NewRelaySink(cfg RelayConfig) *RelaySink {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &RelaySink{cfg: cfg, now: now}
}

// Snapshot seals and forwards each roster record as-of the read cursor, returning on
// the first record that fails so the gateway can gate its cursor on delivery (R-GW.5).
func (s *RelaySink) Snapshot(roster []protocol.JournalRecord, _ uint64) error {
	for _, rec := range roster {
		if err := s.forward(rec); err != nil {
			return err
		}
	}
	return nil
}

// Event seals and forwards one live journal record, returning any seal/append error so
// the gateway declines to advance its cursor past an undelivered record (R-GW.5).
func (s *RelaySink) Event(rec protocol.JournalRecord) error {
	return s.forward(rec)
}

// forward seals rec under the content key and appends it to the phone's mailbox. The
// seal/append error is returned (authoritative for the gateway's cursor gating) and also
// stashed as the first error surfaced via Err().
func (s *RelaySink) forward(rec protocol.JournalRecord) error {
	plaintext, err := json.Marshal(rec)
	if err != nil {
		s.setErr(err)
		return err
	}
	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()

	env, err := crypto.SealMailbox(s.cfg.Key, crypto.EnvelopeHeader{
		Version:        crypto.VersionV1,
		EpochID:        s.cfg.EpochID,
		Seq:            seq,
		RecipientKeyID: s.cfg.RecipientKeyID,
		SenderKeyID:    s.cfg.SenderKeyID,
		IssuedAt:       s.now().UnixMilli(),
	}, plaintext)
	if err != nil {
		s.setErr(err)
		return err
	}
	if _, err := s.cfg.Appender.MailboxAppend(context.Background(), s.cfg.Target, env.Marshal()); err != nil {
		s.setErr(err)
		return err
	}
	return nil
}

// Err returns the first append/seal error encountered, or nil.
func (s *RelaySink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *RelaySink) setErr(err error) {
	s.mu.Lock()
	if s.lastErr == nil {
		s.lastErr = err
	}
	s.mu.Unlock()
}
