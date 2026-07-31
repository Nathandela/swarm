package remotegw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// ReconcileSource supplies the rollback authorities (PB-STATE-4) the sink cannot know
// itself. Every method returns an ERROR rather than a fabricated zero: a record carrying
// a made-up 0 is WORSE than no record at all, because the phone adopts it as truth,
// raises no high-water, and keeps accepting every retained pre-rollback frame -- a silent
// rollback re-opening dressed as a successful reconcile.
//
// The journal/terminal half of (b) is deliberately absent: it is the sink's OWN seq,
// which only the sink knows (see Reconcile).
type ReconcileSource interface {
	InboundHighWater() (uint64, error)                     // (a) the gateway's durable inbound accepted high-water (PB-GW-1)
	ReplyCeiling() (uint64, error)                         // (b) highest seq ISSUED on the command-reply bucket
	GrantWatermark() (epoch uint32, seq uint64, err error) // (c) the daemon's grant-issuance coordinate
}

// RelayConfig configures a RelaySink.
type RelayConfig struct {
	Appender       MailboxAppender
	Target         string            // the phone's relay routing id (mailbox target)
	Machine        string            // this machine's endpoint id, stamped on the reconcile record
	EpochID        uint32            // the current epoch the content key belongs to
	Key            crypto.ContentKey // K_epoch content key the phone also holds (R-CRY.11)
	RecipientKeyID [8]byte           // routing key id of the phone (recipient)
	SenderKeyID    [8]byte           // routing key id of this machine (sender)
	Now            func() time.Time  // envelope issued-at clock (nil => time.Now)
	AppendTimeout  time.Duration     // per-append upper bound (nil/0 => defaultAppendTimeout)
	Seq            SeqSource         // durable outbound seq high-water (nil => in-memory, non-durable)
	Authorities    ReconcileSource   // rollback authorities for the reconcile record (nil => bootstrap disabled)
	Outbox         Outbox            // durable outbound journal custody, PB-GW-8 (nil => in-memory)
}

// defaultAppendTimeout bounds a single MailboxAppend. seal holds s.mu across the append to keep
// concurrent producers in seq order (R-GW.3), so an UNBOUNDED append against a hung relay would
// pin that lock forever and wedge every producer AND Err(). Bounding it means a hung relay
// surfaces a deadline error via Err() within this window instead (Blocker 2).
const defaultAppendTimeout = 5 * time.Second

// RelaySink is a JournalSink that forwards the daemon's journal to the phone via the
// untrusted relay (R-GW.3): it seals each record under the epoch content key
// (XChaCha20-Poly1305, so the relay sees only ciphertext) and appends it to the phone's
// mailbox. Envelope Seq is a strictly increasing per-sink counter (cfg.Seq) so the phone
// can order and dedup; a durable Seq resumes above the phone's high-water after a gateway
// restart (C2b) instead of resetting to 1 and being stale-dropped. Append failures are
// surfaced via Err(); the durable-cursor / relay-ack backpressure (R-GW.5) is a later
// refinement.
type RelaySink struct {
	cfg    RelayConfig
	now    func() time.Time
	seq    SeqSource
	outbox Outbox

	mu      sync.Mutex
	lastErr error
	// reuse holds a seq whose frame PROVABLY never crossed the process boundary -- the
	// marshal, the seal or the outbox reservation failed, all of them BEFORE the append --
	// so the next frame carries it instead of leaving a gap (see sealAtSeqLocked). A seq the
	// appender was handed is never held here, however the relay answered for it (B127).
	// 0 means none held.
	reuse uint64
	// resumed is the journal cursor high-water this process RECOVERED: the outbox's durable
	// committed cursor at construction, raised by each replayed entry. A record at or below
	// it was delivered by a previous run, so re-offering it appends nothing (PB-GW-8). It is
	// deliberately NOT raised by live deliveries -- ordering and dedup within one run belong
	// to Gateway.deliver, and a live high-water would suppress an out-of-order record the
	// gateway legitimately handed down.
	resumed uint64
	// daemonMachine is the endpoint id the daemon reported at hello (SetMachine), which
	// overrides cfg.Machine. It is a separate field rather than a write into cfg so nothing
	// that reads cfg lock-free on the seal path can race with the stamp.
	daemonMachine string
}

// SetMachine adopts the endpoint id the DAEMON assigned, which is the only correct value for
// the reconcile record's machine: it is the id every session id the phone sees is namespaced
// with (Gateway.namespaceRecord) and the id the phone signs into every command tuple, and the
// phone REFUSES a record naming anything else -- the same permanent fail-closed refusal of
// mutating ops as publishing no record at all. Only the daemon knows it (it is derived from
// the daemon's state dir and arrives in the hello), so it cannot be supplied at assembly
// time; Gateway.RunJournal stamps it the moment it has it, before the run's Snapshot.
//
// An empty id is ignored, so a caller that DOES know its machine id (tests, an embedder)
// keeps whatever it configured.
func (s *RelaySink) SetMachine(machine string) {
	if machine == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.daemonMachine = machine
}

// machine is the endpoint id to stamp: the daemon's when known, else the configured one.
func (s *RelaySink) machine() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.daemonMachine != "" {
		return s.daemonMachine
	}
	return s.cfg.Machine
}

// The gateway's outbound seam: the sink is both halves of an OutboundSink and the durable
// resume point New seeds a restarted gateway from. Pinned so a signature drift is a compile
// error rather than a silently un-seeded cursor.
var (
	_ OutboundSink = (*RelaySink)(nil)
	_ CursorSource = (*RelaySink)(nil)
)

// NewRelaySink returns a sink that seals records under cfg.Key and appends them to
// cfg.Target's mailbox via cfg.Appender. A nil cfg.Seq defaults to a non-durable
// in-memory source (the seq resets on restart) -- production wires a durable one, and
// likewise for cfg.Outbox.
func NewRelaySink(cfg RelayConfig) *RelaySink {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	seq := cfg.Seq
	if seq == nil {
		seq, _ = OpenSeqSource("") // in-memory, cannot error
	}
	outbox := cfg.Outbox
	if outbox == nil {
		outbox, _ = OpenOutbox("") // in-memory, cannot error
	}
	return &RelaySink{cfg: cfg, now: now, seq: seq, outbox: outbox, resumed: outbox.Cursor()}
}

// Snapshot seals and forwards each roster record as-of the read cursor, returning on
// the first record that fails so the gateway can gate its cursor on delivery (R-GW.5).
//
// It is also PB-SYNC-7's BOOTSTRAP point: Snapshot runs exactly once per (re)connection
// (the JournalSink contract, gateway.go), so publishing the reconcile record here gets the
// phone its authorities before any journal frame it might have to reconcile against, on
// first connect AND after every reconnect or epoch rotation -- with no new lifecycle hook.
// A failed reconcile forwards NOTHING: the phone is never fed a stream it has no authority
// to reconcile against. A nil Authorities disables the bootstrap (unit-test wiring only;
// production must always provision one).
//
// Roster records are deliberately EXEMPT from the outbox dedup Event applies (PB-GW-8): they
// are CURRENT STATE re-sent as of the read cursor, sharing cursors with journal records they
// do not duplicate, so suppressing them would leave a restarted phone with no roster at all.
func (s *RelaySink) Snapshot(roster []protocol.JournalRecord, _ uint64) error {
	if s.cfg.Authorities != nil {
		if err := s.Reconcile(); err != nil {
			return err
		}
	}
	for _, rec := range roster {
		if err := s.forward(rec); err != nil {
			return err
		}
	}
	return nil
}

// Event seals and forwards one live journal record, returning any seal/append error so
// the gateway declines to advance its cursor past an undelivered record (R-GW.5).
//
// It is OUTBOX-KEYED and therefore idempotent across a restart (PB-GW-8):
//   - a cursor at or below the RESUMED high-water was delivered by a previous run and is not
//     appended again (a restart that re-reads an overlapping range would otherwise re-flood a
//     600-per-tumbling-minute mailbox at fresh seqs), and
//   - a cursor still PENDING -- reserved, its delivery unknown -- is recovered by re-appending
//     the IDENTICAL sealed envelope, which the phone's receiver stale-drops for free.
//     Re-sealing it at a fresh seq instead would get the same record accepted twice, with
//     nothing on the wire for the phone to dedup on.
//
// Snapshot (the reconnect roster) is deliberately NOT keyed this way: see Snapshot.
func (s *RelaySink) Event(rec protocol.JournalRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.Cursor != 0 {
		if rec.Cursor <= s.resumed {
			return nil
		}
		pending, err := s.outbox.Pending()
		if err != nil {
			s.setErrLocked(err)
			return err
		}
		for _, e := range pending {
			if e.Cursor == rec.Cursor {
				return s.replayLocked(e)
			}
		}
	}
	return s.forwardLocked(rec.Cursor, rec)
}

// Replay re-appends every entry the outbox reserved but never saw acked, oldest first and
// byte-for-byte, then commits each. It is what a restarted gateway runs before any new frame
// goes out. A duplicate costs the phone nothing (crypto.MailboxReceiver.Accept stale-drops
// it), which is why an at-least-once outbox needs no relay protocol change; and a second
// Replay appends nothing, so a restart loop cannot re-flood the mailbox.
func (s *RelaySink) Replay() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, err := s.outbox.Pending()
	if err != nil {
		s.setErrLocked(err)
		return err
	}
	for _, e := range pending {
		if err := s.replayLocked(e); err != nil {
			return err
		}
	}
	return nil
}

// DeliveredCursor is the highest journal cursor whose delivery the outbox durably COMMITTED
// -- the resume point New seeds a restarted gateway's journal_read from (PB-GW-8).
func (s *RelaySink) DeliveredCursor() uint64 { return s.outbox.Cursor() }

// replayLocked re-appends one pending entry VERBATIM and commits it. The caller must hold
// s.mu (the append is serialized with every other producer, exactly like a fresh seal).
func (s *RelaySink) replayLocked(e OutboxEntry) error {
	if err := s.appendLocked(e.Envelope); err != nil {
		s.setErrLocked(err)
		return err
	}
	if err := s.outbox.Commit(e.Cursor); err != nil {
		s.setErrLocked(err)
		return err
	}
	// The in-flight tail of the previous run is now resolved, so a re-offer of it (the
	// journal_read that re-serves the un-advanced cursor) must append nothing more.
	if e.Cursor > s.resumed {
		s.resumed = e.Cursor
	}
	return nil
}

// kindTerminalSnapshot tags a mailbox plaintext as a server-rendered terminal snapshot.
// The phone decoder demuxes journal (kind-less) vs snapshot frames on this discriminator
// (phonecore.MailboxRouter); it MUST match phonecore's kindTerminalSnapshot.
const kindTerminalSnapshot = "terminal_snapshot"

// snapshotFrame is the sealed terminal-snapshot plaintext: the protocol.TerminalSnapshot
// fields (session/lines/cols/rows, promoted via anonymous embedding so its frozen json
// tags stay the single source of truth) plus a kind tag. It mirrors phonecore's
// snapshotFrame exactly -- the phone unmarshals this shape (TestSnapshotFrame_WireShape).
type snapshotFrame struct {
	Kind                      string `json:"kind"`
	protocol.TerminalSnapshot        // session, lines, cols, rows (promoted)
}

// Terminal seals a server-rendered terminal snapshot into the phone's mailbox on the SAME
// seq stream as the journal (A7 slice D): the plaintext is the committed wire shape the
// phone decoder demuxes on -- the TerminalSnapshot fields plus a kind:"terminal_snapshot"
// tag. The seal/append error is returned and stashed for Err(), mirroring the journal path.
func (s *RelaySink) Terminal(session string, lines []string, cols, rows int) error {
	plaintext, err := json.Marshal(snapshotFrame{
		Kind:             kindTerminalSnapshot,
		TerminalSnapshot: protocol.TerminalSnapshot{Session: session, Lines: lines, Cols: cols, Rows: rows},
	})
	if err != nil {
		s.setErr(err)
		return err
	}
	return s.seal(plaintext)
}

// kindReconcile tags a mailbox plaintext as the machine's reconcile record. The phone
// decoder demuxes it off the SHARED mailbox on this discriminator; it MUST match
// phonecore's kindReconcile.
const kindReconcile = "reconcile"

// reconcileFrame is the sealed reconcile plaintext: the protocol.ReconcileRecord fields
// (promoted via anonymous embedding so its frozen json tags stay the single source of
// truth) plus a kind tag. It mirrors phonecore's reconcileFrame exactly.
type reconcileFrame struct {
	Kind                     string `json:"kind"`
	protocol.ReconcileRecord        // machine, epoch_id, the three authorities, issued_at (promoted)
}

// errNoAuthorities refuses a reconcile on a sink with no authority source. Synthesizing an
// all-zero record instead would publish three fabricated authorities the phone would adopt
// as truth (see ReconcileSource).
var errNoAuthorities = errors.New("remotegw: reconcile requires an authority source")

// Reconcile seals ONE reconcile record onto the SHARED journal/terminal stream, publishing
// the three rollback authorities PB-STATE-4 names (PB-SYNC-7). Riding the shared bucket is
// what makes JournalCeiling self-certifying: the record's authority for that bucket is the
// record's OWN envelope seq, so merely accepting the frame reseeds the phone's high-water
// there -- and, because it is the highest seq ISSUED rather than the durable RESERVATION
// ceiling, the next live frame at ceiling+1 is still accepted.
//
// Fail-closed: an unreachable authority seals nothing, appends nothing, and surfaces the
// error (also via Err()). The phone then stays unreconciled and refuses mutating ops --
// recoverably, until a later record lands.
func (s *RelaySink) Reconcile() error {
	rec, err := s.authorities()
	if err != nil {
		s.setErr(err)
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sealAtSeqLocked(0, func(seq uint64, issuedAt int64) ([]byte, error) {
		rec.JournalCeiling = seq
		// One clock, no divergence: the record's issued_at IS the envelope's issued_at, so a
		// consumer that sees only the plaintext (MailboxResult carries no header) reads the
		// same time PB-TIME-1 would skew-check.
		rec.IssuedAt = issuedAt
		return json.Marshal(reconcileFrame{Kind: kindReconcile, ReconcileRecord: rec})
	})
}

// authorities collects the record's non-self-knowable authorities, failing on the first
// unreachable one. JournalCeiling and IssuedAt are left unset: only sealAtSeq knows the
// record's own envelope coordinates.
func (s *RelaySink) authorities() (protocol.ReconcileRecord, error) {
	if s.cfg.Authorities == nil {
		return protocol.ReconcileRecord{}, errNoAuthorities
	}
	inbound, err := s.cfg.Authorities.InboundHighWater()
	if err != nil {
		return protocol.ReconcileRecord{}, fmt.Errorf("inbound high-water: %w", err)
	}
	reply, err := s.cfg.Authorities.ReplyCeiling()
	if err != nil {
		return protocol.ReconcileRecord{}, fmt.Errorf("reply ceiling: %w", err)
	}
	grantEpoch, grantSeq, err := s.cfg.Authorities.GrantWatermark()
	if err != nil {
		return protocol.ReconcileRecord{}, fmt.Errorf("grant watermark: %w", err)
	}
	return protocol.ReconcileRecord{
		Machine:          s.machine(),
		EpochID:          s.cfg.EpochID,
		InboundHighWater: inbound,
		ReplyCeiling:     reply,
		GrantEpoch:       grantEpoch,
		GrantSeq:         grantSeq,
	}, nil
}

// kindJournalReseed tags a mailbox plaintext as the atomic roster+events snapshot that
// repairs the phone's journal channel. It MUST match phonecore's kindJournalReseed.
const kindJournalReseed = "journal_reseed"

// reseedFrame is the sealed journal-reseed plaintext: the protocol.JournalReseed fields
// (promoted via anonymous embedding so its pinned json tags stay the single source of truth)
// plus a kind tag. It mirrors phonecore's reseedFrame exactly.
type reseedFrame struct {
	Kind                   string `json:"kind"`
	protocol.JournalReseed        // roster, events, cursor (promoted)
}

// Reseed seals ONE atomic roster+events snapshot onto the SHARED journal/terminal stream:
// PB-SYNC-2's designated JOURNAL repair channel, answering the phone's journal_resync.
//
// ONE FRAME, not N roster records, and that is PB-SYNC-3's requirement rather than a
// convenience: the repair must be committed atomically with the matching transport
// watermark, and N independent frames cannot be -- a death between frames leaves the phone
// with half a snapshot and a watermark claiming the whole. One frame's own arrival seq IS
// the watermark, exactly as the reconcile record's arrival certifies its JournalCeiling.
//
// It carries its own CURSOR because the phone REPLACES its cache cursor with it (PB-SYNC-8).
// The roster's own records carry Cursor 0 -- the daemon leaves it deliberately unset -- so a
// phone that merged this into a live cache would discard every one of them and the repair
// would be a silent no-op.
//
// It is deliberately NOT outbox-keyed: like the reconnect roster it is CURRENT STATE re-sent
// on demand, not a journal record whose delivery must happen exactly once.
func (s *RelaySink) Reseed(rs protocol.JournalReseed) error {
	plaintext, err := json.Marshal(reseedFrame{Kind: kindJournalReseed, JournalReseed: rs})
	if err != nil {
		s.setErr(err)
		return err
	}
	return s.seal(plaintext)
}

// forward marshals rec as a bare journal record (no kind tag, backward-compatible with the
// phone's journal path) and seals it into the phone's mailbox. The seal/append error is
// returned (authoritative for the gateway's cursor gating) and also stashed for Err().
func (s *RelaySink) forward(rec protocol.JournalRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Cursor 0: the roster path is not outbox-keyed (see Snapshot).
	return s.forwardLocked(0, rec)
}

// forwardLocked is forward inside the critical section. A non-zero cursor makes the frame
// outbox-backed (Event); 0 leaves it unrecorded (roster records, which are re-sent state).
func (s *RelaySink) forwardLocked(cursor uint64, rec protocol.JournalRecord) error {
	plaintext, err := json.Marshal(rec)
	if err != nil {
		s.setErrLocked(err)
		return err
	}
	return s.sealAtSeqLocked(cursor, func(uint64, int64) ([]byte, error) { return plaintext, nil })
}

// seal allocates the next shared seq, seals plaintext under the epoch content key, and
// appends the opaque envelope to the phone's mailbox. Journal records and terminal
// snapshots both flow through here so they share one strictly increasing seq stream
// (R-GW.3; the phone orders and dedups on that single seq).
func (s *RelaySink) seal(plaintext []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sealAtSeqLocked(0, func(uint64, int64) ([]byte, error) { return plaintext, nil })
}

// sealAtSeqLocked is seal for a plaintext that must know its OWN envelope coordinates:
// build is handed the allocated seq and the envelope's issued-at. The reconcile record needs
// both -- its journal_ceiling authority is its own seq (self-certifying, so accepting the
// frame reseeds the bucket) and its issued_at must be the header's, not a second reading of
// the clock. A non-zero cursor makes the frame outbox-backed (PB-GW-8): the sealed bytes are
// durable BEFORE the append and committed only once the relay acks.
//
// The caller must hold s.mu, and the whole seq-allocate -> append runs under it so
// RunJournal and RunTerminal (two goroutines sharing one sink) can never append out of seq
// order: releasing the lock after allocating seq would let a later seq reach the phone's
// single MailboxReceiver first, which drops the earlier one as ErrStaleSeq and forces a
// spurious resync. Appends are the gateway's outbound path (not hot), so serializing them is
// cheap. For the same reason, build MUST NOT touch the sink: any call back into a locking
// method (Err, setErr, seal, Event, Reconcile) self-deadlocks.
func (s *RelaySink) sealAtSeqLocked(cursor uint64, build func(seq uint64, issuedAt int64) ([]byte, error)) error {
	// Allocate seq inside s.mu so allocation order == append order. A durable Seq may fsync
	// here once per reservation block; on a persist fault it fails closed (no seq issued).
	seq, err := s.nextSeqLocked()
	if err != nil {
		s.setErrLocked(err)
		return err
	}
	issuedAt := s.now().UnixMilli()
	plaintext, err := build(seq, issuedAt)
	if err != nil {
		s.reuse = seq // nothing left this process: the seq is unspent, so do not burn it
		s.setErrLocked(err)
		return err
	}

	env, err := crypto.SealMailbox(s.cfg.Key, crypto.EnvelopeHeader{
		Version:        crypto.VersionV1,
		EpochID:        s.cfg.EpochID,
		Seq:            seq,
		RecipientKeyID: s.cfg.RecipientKeyID,
		SenderKeyID:    s.cfg.SenderKeyID,
		IssuedAt:       issuedAt,
	}, plaintext)
	if err != nil {
		s.reuse = seq
		s.setErrLocked(err)
		return err
	}
	raw := env.Marshal()
	if cursor != 0 {
		// Durable BEFORE the append (PB-GW-8): a crash, or a reply lost after the relay has
		// already stored the item, leaves the EXACT bytes to re-append. Re-sealing instead
		// would put a second, different envelope at this seq.
		if err := s.outbox.Reserve(cursor, raw); err != nil {
			s.reuse = seq
			s.setErrLocked(err)
			return err
		}
	}
	if err := s.appendLocked(raw); err != nil {
		// THE SEQ IS SPENT, whatever the relay says about it. Once the bytes are handed to
		// the appender, whether they were stored is the RELAY's to know and the relay's to
		// answer -- and the relay is the adversary this design names. Its refusal codes
		// (quota_exceeded, not_authorized, revoked) are replied before the store by the
		// HONEST relay, and that made them look like proof of non-commitment; but a relay
		// that STORES and then denies gets the seq reissued for a freshly sealed DIFFERENT
		// plaintext, holds both rivals, and chooses which one lands. The loser is
		// stale-dropped with NO GAP reported anywhere, because the seq was consumed by its
		// rival -- silent loss, invisible to every staleness mechanism (ADR-007 B125 F-2,
		// B127). No coordinate can stand in for the sentinel: the relay speaks last on the
		// append and authors the mailbox read too, so nothing it returns establishes
		// non-commitment. Burning costs ONE gap per CONTIGUOUS run of refusals, which
		// PB-SYNC-1 charges to both journal and terminal; silent loss costs everything.
		//
		// An outbox-backed frame is unaffected either way: its reservation already owns the
		// seq and Event's retry re-appends the IDENTICAL envelope, which the phone's
		// receiver stale-drops for free.
		s.setErrLocked(err)
		return err
	}
	if cursor != 0 {
		if err := s.outbox.Commit(cursor); err != nil {
			s.setErrLocked(err)
			return err
		}
	}
	return nil
}

// nextSeqLocked hands out the seq the next frame carries: a seq held back by a failure that
// happened before the append is reissued (nothing was ever offered at it), otherwise a fresh
// one is reserved. The caller must hold s.mu.
func (s *RelaySink) nextSeqLocked() (uint64, error) {
	if s.reuse != 0 {
		seq := s.reuse
		s.reuse = 0
		return seq, nil
	}
	return s.seq.Next()
}

// appendLocked appends one opaque envelope under a BOUNDED context: a hung relay must not
// hold s.mu forever (Blocker 2). The deadline is generous (relay round-trips are fast);
// exceeding it surfaces an error via Err() rather than wedging RunJournal + every
// RunTerminal (all serialize on s.mu here). The caller must hold s.mu.
func (s *RelaySink) appendLocked(env []byte) error {
	timeout := s.cfg.AppendTimeout
	if timeout <= 0 {
		timeout = defaultAppendTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := s.cfg.Appender.MailboxAppend(ctx, s.cfg.Target, env)
	return err
}

// Err returns the first append/seal error encountered, or nil.
func (s *RelaySink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *RelaySink) setErr(err error) {
	s.mu.Lock()
	s.setErrLocked(err)
	s.mu.Unlock()
}

// setErrLocked records the first error; the caller must hold s.mu (seal calls it inside
// its critical section, where setErr's own Lock would deadlock).
func (s *RelaySink) setErrLocked(err error) {
	if s.lastErr == nil {
		s.lastErr = err
	}
}
