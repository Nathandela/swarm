package remotegw

// The wake-obligation machine (ADR-015 P9, docs/specifications/push-gateway-api.md §6):
// the durable, coalescible obligation that survives a swarm-remote crash between "a wake
// was owed" and "FCM accepted it". The obligation lives on the machine, not the gateway
// -- the gateway has no hidden delivery queue (P9).
//
// NOTE ON THE "abandoned" STATE: the spec's own header records this as an open
// divergence from ADR-015 P9's literal, unqualified "every non-2xx leaves the obligation
// retryable" (push-gateway-api.md §14.1 row 7 / §13.8). This implementation pins the
// spec's §6.4 three-way table (retryable / abandoned / terminal) as the contract it
// builds against, matching the RED suite's own documented choice (obligation_test.go's
// header) rather than P9's literal fallback.
//
// NOTE ON A SECOND, RELATED §6.4 READING (bodyless gateway responses): see
// wakesubmitter.go's header for how a response with no parseable error body is mapped --
// §6.4's own row text ("handled here, not as a code") and PG-ERR-3 override PG-ERR-1's
// literal status-based fallback for this specific machine, so that case is unconditionally
// pending, never abandoned, regardless of status.
//
// NOTE ON PG-OBL-2's ORDERING: PG-OBL-2 requires the obligation recorded "before or
// atomically with" mailbox publication. Trigger below is the durable write with no
// network call that makes the "before" half possible, and PushNotifier.Event
// (push.go's preAppendObligation) now calls it -- through TransportRouter.
// PreAppendObligation, pushtransport.go -- BEFORE n.inner.Event(rec) publishes, for a
// gateway-transport pairing specifically. The legacy_relay path's own ordering guarantee
// ("the wake follows a SUCCESSFUL append", push.go:Event) is untouched: preAppendObligation
// is a no-op unless the Pusher implements the optional pre-append capability AND the
// live selection is TransportGateway. Filed and closed as bd issue
// agents-tracker-hggx.4.2; see push.go's Event/preAppendObligation/wouldWakeNow and
// pushtransport.go's TransportRouter.PreAppendObligation for the full mechanism, and
// TestPushNotifier_GatewayTransportAppendsTheObligationBeforePublishingTheMailboxRecord
// (push_obligation_order_test.go) for the regression proof.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// ObligationState is one of the five states of spec §6.3's diagram.
type ObligationState string

const (
	ObligationPending   ObligationState = "pending"
	ObligationInFlight  ObligationState = "in_flight"
	ObligationDelivered ObligationState = "delivered"
	ObligationExpired   ObligationState = "expired"
	ObligationAbandoned ObligationState = "abandoned"
)

// WakeObligation is the durable record keyed (push_address, wake_seq) -- PG-OBL-1. It
// persists the SEALED bytes (sealed once, PG-WAKE-12): a retry replays Envelope
// verbatim and never re-seals.
type WakeObligation struct {
	Address     PushAddress
	WakeSeq     uint64
	Envelope    []byte
	IssuedAt    time.Time
	ExpiresAt   time.Time
	State       ObligationState
	Attempts    int
	Coalesced   int // PG-OBL-5/6: triggers that landed on this obligation while it was live
	LastOutcome string
}

// nonTerminal reports whether ob is still live (pending or in_flight).
func (ob WakeObligation) nonTerminal() bool {
	return ob.State == ObligationPending || ob.State == ObligationInFlight
}

// ObligationStore is durable custody for wake obligations, mirroring Outbox's
// reserve-before-effect discipline (outbox.go): Put must be durable before it returns.
//
// NARROWED KEYING, STATED RATHER THAN HIDDEN: PG-OBL-1 keys the obligation
// (push_address, wake_seq), but PG-OBL-5 also bounds every address to AT MOST ONE
// outstanding obligation at a time, so this package's stores (fileObligationStore,
// fakeObligationStore) key durable custody on push_address alone and keep only the
// address's CURRENT record -- there is never a second live wake_seq to distinguish it
// from. The consequence: a PG-OBL-6 re-mint, or a fresh Trigger after a prior record
// went terminal, replaces that prior record rather than appending beside it, so Get only
// ever answers "the current or most recent obligation for this address", never "every
// obligation this address has ever had". That is sufficient for PG-OBL-10 (a pairing's
// health reads its LAST obligation's outcome, which is exactly what Get returns) but not
// for a full delivery history; a future diagnostics need for the latter is a distinct
// feature (a separate append-only log), not a widening of this store's contract.
type ObligationStore interface {
	Put(ob WakeObligation) error
	Get(addr PushAddress) (WakeObligation, bool, error)
	// Pending returns every non-terminal obligation, oldest issued_at first, for
	// restart re-drive (PG-OBL-8).
	Pending() ([]WakeObligation, error)
}

// WakeSubmitError is the gateway's typed refusal (spec §4, §3.6's Error schema).
// Retryable -- never the HTTP status -- is the only field the obligation machine
// transitions on (PG-ERR-3).
type WakeSubmitError struct {
	Code      string
	Retryable bool
	Message   string
}

func (e *WakeSubmitError) Error() string {
	return fmt.Sprintf("remotegw: gateway refused wake: %s (retryable=%v): %s", e.Code, e.Retryable, e.Message)
}

// WakeSubmitter is the gateway HTTP seam (spec §3.5). A nil error means FCM accepted
// the byte-identical request (200 provider_accepted). A *WakeSubmitError is a parsed
// gateway refusal. Any other non-nil error is a transport failure -- no response was
// ever received -- which ADR-015 P9 makes unconditionally retryable.
type WakeSubmitter interface {
	SubmitWake(ctx context.Context, envelope []byte) error
}

// WakeObligationConfig configures one address's WakeObligationMachine.
type WakeObligationConfig struct {
	Store     ObligationStore
	Submitter WakeSubmitter
	WakeKey   crypto.WakeKey
	Address   PushAddress
	Seq       SeqSource // durable wake_seq (PG-WAKE-16): starts at 1, never reused after restart
	Now       func() time.Time
}

// WakeObligationMachine is the per-address obligation machine of spec §6: it mints,
// coalesces, and drives one address's wake obligation through the state diagram of
// §6.3.
//
// mu serialises every durable read-modify-write against this address's record, but is
// deliberately released before the ONE network call Drive makes (Submitter.SubmitWake),
// so a Trigger landing mid-submit can still coalesce (durably) instead of blocking on the
// network. Drive re-reads the record after the call and merges the outcome onto whatever
// is durable NOW, never onto its own pre-call copy, so a concurrent coalesce's Coalesced
// increment (and Attempts, if a restart-driven Drive raced in) is never clobbered. driving
// is a same-process guard, separate from the persisted state: it stops two goroutines
// (e.g. push.go's deferred-wake timer and an immediate trigger both landing on
// TransportRouter for the same address) from starting a SECOND concurrent submit for an
// obligation the first already marked in_flight -- the persisted in_flight state alone
// cannot distinguish "a submit is running right now" from "a submit was interrupted by a
// crash and needs re-driving", which restart re-drive (PG-OBL-8) still must do.
type WakeObligationMachine struct {
	cfg     WakeObligationConfig
	now     func() time.Time
	mu      sync.Mutex
	driving bool
}

// errNilObligationSeq is returned when a WakeObligationMachine is asked to mint a wake
// with no configured SeqSource. Failing closed here -- rather than the nil-pointer panic
// a missing interface would otherwise produce on the first mint -- matches the rest of
// this package's custody discipline (errCorruptObligationStore, errCorruptTransportStore):
// a misassembled machine must refuse to mint, not crash the process that owns every other
// live session's journal bridge.
var errNilObligationSeq = errors.New("remotegw: wake-obligation machine has no durable Seq configured")

// NewWakeObligationMachine returns a machine for cfg.Address.
func NewWakeObligationMachine(cfg WakeObligationConfig) *WakeObligationMachine {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &WakeObligationMachine{cfg: cfg, now: now}
}

// Trigger coalesces into the address's live (non-terminal, not-yet-expired) obligation,
// or mints and durably persists a fresh one (PG-OBL-2/4/5). This call IS "the obligation
// append": a caller that invokes it before publishing the mailbox record it announces
// satisfies PG-OBL-2's "before or atomically with" -- see this file's header for how the
// one production caller in this tree (push.go's PushNotifier.Event) does that.
//
// A live obligation whose expiry has already passed is NOT coalesced into: PG-OBL-6 mints
// a fresh replacement instead, exactly as Drive's own expiry branch would once it next
// runs. Coalescing into an expired record here would durably record a trigger that Drive
// (whenever it next runs) would otherwise correctly re-mint for anyway, so this is the
// same outcome reached earlier rather than a new rule -- and it holds even if a caller
// invokes Trigger without an immediate Drive after it, which TransportRouter always does,
// but which is not a property Trigger itself may assume of every future caller.
//
// OBSERVATION, SPEC-MANDATED RATHER THAN A DEFECT HERE: coalescing into an in_flight
// obligation (the branch below fires for in_flight exactly as for pending) means a
// trigger that lands AFTER FCM already accepted the outstanding submit, but before Drive
// gets back here to record that, is folded into a wake the phone may already have
// consumed -- the coalesced record then publishes its mailbox event with no wake behind
// it. PG-OBL-5 mandates exactly this ("WHILE an obligation ... is non-terminal"): the
// wake carries no locator, so the phone reconciles everything on any wake it receives,
// and the alternative (a second concurrent obligation per address) is what PG-OBL-5
// exists to forbid. Named here so a reader does not mistake it for an oversight.
func (m *WakeObligationMachine) Trigger() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ob, ok, err := m.cfg.Store.Get(m.cfg.Address)
	if err != nil {
		return err
	}
	if ok && ob.nonTerminal() && !m.now().After(ob.ExpiresAt) {
		ob.Coalesced++
		return m.cfg.Store.Put(ob)
	}
	return m.mintLocked(m.now())
}

// Drive attempts delivery of the address's live obligation, if any, exactly once and
// applies the resulting §6.4 transition. It durably marks in_flight BEFORE calling the
// submitter, so a crash mid-call recovers as in_flight (retry-safe), not as
// never-attempted; it durably marks the terminal/back-to-pending outcome after, by
// RE-READING the record rather than reusing its pre-call copy, so a Trigger that coalesced
// while the network call was outstanding is preserved rather than overwritten. An
// obligation whose expiry has passed is marked expired without a submit attempt, and
// PG-OBL-6 re-mints immediately if triggers were coalesced into it. A Drive call that
// finds a submit already running for this address (the driving guard) is a no-op: the
// call already in flight will apply the outcome. If the record was SUPERSEDED while the
// call was outstanding -- a PG-OBL-6 re-mint replaced it with a fresh, undriven
// wake_seq -- the submit outcome is reported to the caller but never stamped onto the
// fresh record; see the WakeSeq check inside for why.
func (m *WakeObligationMachine) Drive(ctx context.Context) error {
	m.mu.Lock()
	if m.driving {
		m.mu.Unlock()
		return nil
	}
	ob, ok, err := m.cfg.Store.Get(m.cfg.Address)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if !ok || !ob.nonTerminal() {
		m.mu.Unlock()
		return nil
	}

	now := m.now()
	if now.After(ob.ExpiresAt) {
		ob.State = ObligationExpired
		ob.LastOutcome = "expired"
		putErr := m.cfg.Store.Put(ob)
		coalesced := ob.Coalesced
		if putErr != nil {
			m.mu.Unlock()
			return putErr
		}
		if coalesced > 0 {
			// PG-OBL-6: something is still waiting to wake the phone for. mintLocked
			// while still holding mu -- this is the SAME lock Trigger takes, and Drive
			// (unlike Trigger) never releases it before calling mint, so no interleaving
			// with a concurrent Trigger is possible here.
			err := m.mintLocked(now)
			m.mu.Unlock()
			return err
		}
		m.mu.Unlock()
		return nil
	}

	ob.State = ObligationInFlight
	ob.Attempts++
	if err := m.cfg.Store.Put(ob); err != nil {
		m.mu.Unlock()
		return err
	}
	envelope := ob.Envelope
	submittedSeq := ob.WakeSeq
	m.driving = true
	m.mu.Unlock()

	submitErr := m.cfg.Submitter.SubmitWake(ctx, envelope)

	m.mu.Lock()
	m.driving = false
	defer m.mu.Unlock()
	cur, ok, err := m.cfg.Store.Get(m.cfg.Address)
	if err != nil {
		return err
	}
	if !ok {
		// The record vanished while the submit was in flight. Nothing this machine's own
		// Store implementations do can cause that (Put never deletes), so this defends
		// against a future store rather than a reachable path today.
		return submitErr
	}
	if cur.WakeSeq != submittedSeq {
		// The obligation this Drive call submitted was SUPERSEDED while the network call
		// was outstanding: its expiry passed and a coalesced Trigger correctly refused to
		// coalesce into the now-expired record, minting a fresh one instead (PG-OBL-6).
		// `cur` is that fresh, wholly undriven obligation -- applying submitErr's outcome
		// to it would stamp a result the fresh wake_seq never earned: on success it would
		// be marked delivered having never been submitted, and on a non-retryable refusal
		// it would be marked abandoned, either way permanently losing the wake the re-mint
		// exists to protect (no re-mint, since Coalesced==0 on the fresh record; no
		// re-drive, since both outcomes are terminal). Leave it untouched -- it is pending
		// and will be driven by whatever calls Drive next (a trigger, a redial, or
		// PG-OBL-9's retry driver) -- and report the stale outcome as this call's own.
		return submitErr
	}

	if submitErr == nil {
		cur.State = ObligationDelivered
		cur.LastOutcome = "provider_accepted"
		return m.cfg.Store.Put(cur)
	}

	var wse *WakeSubmitError
	if errors.As(submitErr, &wse) {
		if wse.Retryable {
			cur.State = ObligationPending
		} else {
			cur.State = ObligationAbandoned
		}
		cur.LastOutcome = wse.Code
	} else {
		// No parseable gateway response at all -- either a genuine transport failure, or
		// (per this file's header note and wakesubmitter.go's) a response that carried no
		// parseable error body, which §6.4/PG-ERR-3 fold into this same row. Both are
		// unconditionally retryable per ADR-015 P9.
		cur.State = ObligationPending
		cur.LastOutcome = "transport_failure"
	}
	if err := m.cfg.Store.Put(cur); err != nil {
		return err
	}
	return submitErr
}

// mintLocked seals a fresh WakeV1 at the next durable wake_seq and persists a new pending
// obligation for cfg.Address, at issued time `at`. The caller MUST hold m.mu.
func (m *WakeObligationMachine) mintLocked(at time.Time) error {
	if m.cfg.Seq == nil {
		return errNilObligationSeq
	}
	seq, err := m.cfg.Seq.Next()
	if err != nil {
		return err
	}
	env, err := SealWakeV1(m.cfg.WakeKey, m.cfg.Address, seq, at)
	if err != nil {
		return err
	}
	ob := WakeObligation{
		Address:   m.cfg.Address,
		WakeSeq:   seq,
		Envelope:  env,
		IssuedAt:  at,
		ExpiresAt: at.Add(WakeV1Expiry),
		State:     ObligationPending,
	}
	return m.cfg.Store.Put(ob)
}

// --- ObligationStore: a single JSON file, in the same durability idiom as
// outbox.go/seqstore.go: held in memory, rewritten atomically on every change. ---

const obligationSchemaVersion = 1

type obligationFile struct {
	SchemaVersion int                         `json:"schema_version"`
	Obligations   map[string]obligationRecord `json:"obligations"` // key: hex(push_address)
}

type obligationRecord struct {
	WakeSeq     uint64          `json:"wake_seq"`
	Envelope    []byte          `json:"envelope"`
	IssuedAt    time.Time       `json:"issued_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
	State       ObligationState `json:"state"`
	Attempts    int             `json:"attempts"`
	Coalesced   int             `json:"coalesced"`
	LastOutcome string          `json:"last_outcome"`
}

// errCorruptObligationStore flags an unreadable or unsupported obligation-store file.
// Custody fails closed exactly like the outbox and the seq ceiling: a truncated or
// wrongly-versioned file is an error, never a silent reset that would lose a live
// obligation and orphan its wake_seq.
var errCorruptObligationStore = errors.New("remotegw: corrupt wake-obligation store file")

// fileObligationStore is an ObligationStore backed by one JSON file. An empty path
// makes it purely in-memory (no durability).
type fileObligationStore struct {
	mu     sync.Mutex
	path   string
	byAddr map[PushAddress]WakeObligation
}

var _ ObligationStore = (*fileObligationStore)(nil)

// OpenObligationStore opens the durable obligation store at path, loading any
// previously persisted obligations. A missing file starts fresh; a present-but-corrupt
// file fails closed. An empty path returns a purely in-memory store.
func OpenObligationStore(path string) (ObligationStore, error) {
	s := &fileObligationStore{path: path, byAddr: map[PushAddress]WakeObligation{}}
	if path == "" {
		return s, nil
	}
	byAddr, err := loadObligations(path)
	if err != nil {
		return nil, err
	}
	s.byAddr = byAddr
	return s, nil
}

func (s *fileObligationStore) Put(ob WakeObligation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[PushAddress]WakeObligation, len(s.byAddr)+1)
	for k, v := range s.byAddr {
		next[k] = v
	}
	next[ob.Address] = ob
	if s.path != "" {
		if err := persistObligations(s.path, next); err != nil {
			return err
		}
	}
	s.byAddr = next
	return nil
}

func (s *fileObligationStore) Get(addr PushAddress) (WakeObligation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ob, ok := s.byAddr[addr]
	return ob, ok, nil
}

func (s *fileObligationStore) Pending() ([]WakeObligation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []WakeObligation
	for _, ob := range s.byAddr {
		if ob.nonTerminal() {
			out = append(out, ob)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IssuedAt.Before(out[j].IssuedAt) })
	return out, nil
}

func loadObligations(path string) (map[PushAddress]WakeObligation, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[PushAddress]WakeObligation{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read wake-obligation store: %w", err)
	}
	var f obligationFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", errCorruptObligationStore, path, err)
	}
	if f.SchemaVersion != obligationSchemaVersion {
		return nil, fmt.Errorf("%w: %s: schema version %d unsupported (want %d)",
			errCorruptObligationStore, path, f.SchemaVersion, obligationSchemaVersion)
	}
	out := make(map[PushAddress]WakeObligation, len(f.Obligations))
	for key, rec := range f.Obligations {
		raw, err := hex.DecodeString(key)
		if err != nil || len(raw) != len(PushAddress{}) {
			return nil, fmt.Errorf("%w: %s: bad push_address key %q", errCorruptObligationStore, path, key)
		}
		var addr PushAddress
		copy(addr[:], raw)
		out[addr] = WakeObligation{
			Address: addr, WakeSeq: rec.WakeSeq, Envelope: rec.Envelope,
			IssuedAt: rec.IssuedAt, ExpiresAt: rec.ExpiresAt, State: rec.State,
			Attempts: rec.Attempts, Coalesced: rec.Coalesced, LastOutcome: rec.LastOutcome,
		}
	}
	return out, nil
}

func persistObligations(path string, byAddr map[PushAddress]WakeObligation) error {
	f := obligationFile{SchemaVersion: obligationSchemaVersion, Obligations: map[string]obligationRecord{}}
	for addr, ob := range byAddr {
		f.Obligations[hex.EncodeToString(addr[:])] = obligationRecord{
			WakeSeq: ob.WakeSeq, Envelope: ob.Envelope, IssuedAt: ob.IssuedAt, ExpiresAt: ob.ExpiresAt,
			State: ob.State, Attempts: ob.Attempts, Coalesced: ob.Coalesced, LastOutcome: ob.LastOutcome,
		}
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, ".wake-obligations-*", data)
}
