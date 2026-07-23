// Package idempotency is the daemon's two-phase durable idempotency store (ADR-007
// D6, plan R-IDP, amendment D.0-A3): a crash-safe `prepared -> executing ->
// completed/failed` request record keyed by operation_id, fsync'd BEFORE the side
// effect, so a duplicate request replays the cached outcome and executes nothing,
// and a crash between the side effect and the commit never double-executes.
// `interrupt` is at-most-once: a mid-op crash resolves to a terminal
// `outcome_unknown`, never a claimed exactly-once.
//
// Durability is a fsync'd append-only log replayed on Open (last-write-wins per
// operation_id); Compact rewrites it atomically (tmp+rename) dropping expired /
// over-cap records.
package idempotency

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Phase is the lifecycle of a two-phase idempotency record.
type Phase string

const (
	PhasePrepared       Phase = "prepared"
	PhaseExecuting      Phase = "executing"
	PhaseCompleted      Phase = "completed"
	PhaseFailed         Phase = "failed"
	PhaseOutcomeUnknown Phase = "outcome_unknown"
)

// MaxOperationIDLen bounds the opaque operation_id key.
const MaxOperationIDLen = 128

// logFile is the append-only durable log within the store dir.
const logFile = "idempotency.log"

// Record is one operation's durable idempotency state.
type Record struct {
	OperationID string          `json:"operation_id"`
	Action      string          `json:"action"`
	SessionID   string          `json:"session_id"`
	Phase       Phase           `json:"phase"`
	Outcome     json.RawMessage `json:"outcome,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Options tune retention (TTL / cap) and the injected clock.
type Options struct {
	TTL        time.Duration    // drop records older than this on Compact (0 = no TTL)
	MaxEntries int              // keep at most this many records on Compact (0 = no cap)
	Clock      func() time.Time // record timestamp source (defaults to time.Now)
}

// Store is the durable two-phase idempotency store.
type Store struct {
	dir   string
	opts  Options
	clock func() time.Time

	mu      sync.Mutex
	records map[string]Record
	f       *os.File // append-only log handle
}

// Open opens (or creates) the store at dir with default (no-TTL) retention.
func Open(dir string) (*Store, error) { return OpenWithOptions(dir, Options{}) }

// OpenWithOptions opens the store, replaying the durable log so every record —
// including an in-flight `executing` one fsync'd before a crash — is restored.
func OpenWithOptions(dir string, opts Options) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, opts: opts, clock: opts.Clock, records: make(map[string]Record)}
	if s.clock == nil {
		s.clock = time.Now
	}
	if err := s.replay(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, logFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	s.f = f
	return s, nil
}

// replay loads the log, last-write-wins per operation_id.
func (s *Store) replay() error {
	f, err := os.Open(filepath.Join(s.dir, logFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if json.Unmarshal(line, &rec) != nil {
			continue // torn/corrupt line: tolerate
		}
		if rec.OperationID != "" {
			s.records[rec.OperationID] = rec
		}
	}
	return nil
}

// Prepare durably records `prepared` (fsync) BEFORE any side effect. If a record
// for operationID already exists it is returned with existed=true (the replay
// path): the caller MUST execute nothing and return the cached outcome.
func (s *Store) Prepare(operationID, action, sessionID string) (Record, bool, error) {
	if operationID == "" {
		return Record{}, false, errors.New("idempotency: empty operation_id")
	}
	if len(operationID) > MaxOperationIDLen {
		return Record{}, false, fmt.Errorf("idempotency: operation_id length %d exceeds %d", len(operationID), MaxOperationIDLen)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.records[operationID]; ok {
		return rec, true, nil
	}
	now := s.clock()
	rec := Record{
		OperationID: operationID,
		Action:      action,
		SessionID:   sessionID,
		Phase:       PhasePrepared,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.appendLocked(rec); err != nil {
		return Record{}, false, err
	}
	s.records[operationID] = rec
	return rec, false, nil
}

// Begin transitions prepared -> executing (fsync), immediately before the side
// effect.
func (s *Store) Begin(operationID string) error {
	return s.transition(operationID, PhaseExecuting, nil, false)
}

// Complete transitions -> completed (fsync), after the side effect commits.
func (s *Store) Complete(operationID string, outcome []byte) error {
	return s.transition(operationID, PhaseCompleted, outcome, true)
}

// Fail transitions -> failed (fsync).
func (s *Store) Fail(operationID string, outcome []byte) error {
	return s.transition(operationID, PhaseFailed, outcome, true)
}

// ResolveOutcomeUnknown terminates an unresolved at-most-once op (interrupt) whose
// side effect cannot be verified after a crash (A3): -> outcome_unknown (fsync).
func (s *Store) ResolveOutcomeUnknown(operationID string) error {
	return s.transition(operationID, PhaseOutcomeUnknown, nil, false)
}

// transition advances an existing record to phase (fsync), optionally recording an
// outcome.
func (s *Store) transition(operationID string, phase Phase, outcome []byte, setOutcome bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[operationID]
	if !ok {
		return fmt.Errorf("idempotency: unknown operation_id %q", operationID)
	}
	rec.Phase = phase
	rec.UpdatedAt = s.clock()
	if setOutcome {
		rec.Outcome = append(json.RawMessage(nil), outcome...)
	}
	if err := s.appendLocked(rec); err != nil {
		return err
	}
	s.records[operationID] = rec
	return nil
}

// Get returns a record by operation_id.
func (s *Store) Get(operationID string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[operationID]
	return rec, ok
}

// List returns a snapshot of every record (order unspecified). The daemon's
// Open-time stale-launch resolver walks it to re-drivably fail launch records
// whose reserved session did not survive a crash.
func (s *Store) List() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, rec)
	}
	return out
}

// Redrive re-points operationID at newSessionID and resets it to `prepared`
// (fsync), so a launch whose reserved session was lost to a mid-launch crash can
// be re-driven under the SAME operation_id instead of poisoning the key. It force-
// writes (last-write-wins), unlike Prepare which refuses an existing key; the
// original CreatedAt is preserved when a prior record exists. Durable before the
// re-driven side effect, exactly as Prepare is.
func (s *Store) Redrive(operationID, action, newSessionID string) (Record, error) {
	if operationID == "" {
		return Record{}, errors.New("idempotency: empty operation_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	created := now
	if old, ok := s.records[operationID]; ok {
		created = old.CreatedAt
	}
	rec := Record{
		OperationID: operationID,
		Action:      action,
		SessionID:   newSessionID,
		Phase:       PhasePrepared,
		CreatedAt:   created,
		UpdatedAt:   now,
	}
	if err := s.appendLocked(rec); err != nil {
		return Record{}, err
	}
	s.records[operationID] = rec
	return rec, nil
}

// Compact drops TTL-expired and over-cap records (R-IDP.4), rewriting the log
// atomically (tmp+rename).
func (s *Store) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()
	kept := make([]Record, 0, len(s.records))
	for _, rec := range s.records {
		if s.opts.TTL > 0 && now.Sub(rec.UpdatedAt) > s.opts.TTL {
			continue
		}
		kept = append(kept, rec)
	}
	// Newest first so an over-cap trim drops the oldest.
	sort.Slice(kept, func(a, b int) bool { return kept[a].UpdatedAt.After(kept[b].UpdatedAt) })
	if s.opts.MaxEntries > 0 && len(kept) > s.opts.MaxEntries {
		kept = kept[:s.opts.MaxEntries]
	}

	if err := s.rewriteLocked(kept); err != nil {
		return err
	}
	s.records = make(map[string]Record, len(kept))
	for _, rec := range kept {
		s.records[rec.OperationID] = rec
	}
	return nil
}

// appendLocked writes one record as a JSON line and fsyncs BEFORE returning, so the
// durable record precedes any side effect the caller performs next.
func (s *Store) appendLocked(rec Record) error {
	line := append(mustMarshal(rec), '\n')
	if _, err := s.f.Write(line); err != nil {
		return err
	}
	return s.f.Sync()
}

// rewriteLocked replaces the log with kept, atomically (tmp+fsync+rename), and
// reopens the append handle on the fresh file.
func (s *Store) rewriteLocked(kept []Record) error {
	tmp, err := os.CreateTemp(s.dir, logFile+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	for _, rec := range kept {
		if _, err := tmp.Write(append(mustMarshal(rec), '\n')); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if s.f != nil {
		_ = s.f.Close()
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, logFile)); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, logFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	s.f = f
	return nil
}

func mustMarshal(rec Record) []byte {
	b, _ := json.Marshal(rec) // Record is always marshalable
	return b
}
