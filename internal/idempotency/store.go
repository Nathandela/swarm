// Package idempotency is the daemon's two-phase durable idempotency store (ADR-007
// D6, plan R-IDP, amendment D.0-A3): a crash-safe `prepared -> executing ->
// completed/failed` request record keyed by operation_id, fsync'd BEFORE the side
// effect, so a duplicate request replays the cached outcome and executes nothing,
// and a crash between the side effect and the commit never double-executes.
// `interrupt` is at-most-once: a mid-op crash resolves to a terminal
// `outcome_unknown`, never a claimed exactly-once.
//
// Durability is a fsync'd append-only log replayed on Open (last-write-wins per
// operation_id); Compact rewrites it atomically (tmp+fsync+rename+dirsync)
// dropping expired records, degrading to "not compacted this cycle"
// rather than losing the store if any step fails.
package idempotency

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// ErrCapacity refuses new effects without evicting an existing replay guard.
var ErrCapacity = errors.New("idempotency: capacity reached; retry after protected operations expire")

// Record is one operation's durable idempotency state.
type Record struct {
	OperationID string `json:"operation_id"`
	Action      string `json:"action"`
	SessionID   string `json:"session_id"`
	// SessionInstance optionally fences a mutating operation to one incarnation of a
	// reusable session id. Empty preserves records written before incarnation binding.
	SessionInstance string `json:"session_instance,omitempty"`
	// RequestHash binds an idempotency key to the exact signed request body. Without it,
	// reusing one key for different text could replay the first request's cached success
	// for words that were never delivered.
	RequestHash string          `json:"request_hash,omitempty"`
	Request     json.RawMessage `json:"request,omitempty"`
	Evidence    json.RawMessage `json:"evidence,omitempty"`
	Phase       Phase           `json:"phase"`
	Outcome     json.RawMessage `json:"outcome,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Options tune retention (TTL / cap) and the injected clock.
type Options struct {
	TTL        time.Duration    // drop records older than this on Compact (0 = no TTL)
	MaxEntries int              // new-operation admission limit; unexpired guards are never evicted (0 = no cap)
	Clock      func() time.Time // record timestamp source (defaults to time.Now)
}

// Store is the durable two-phase idempotency store.
type Store struct {
	dir   string
	opts  Options
	clock func() time.Time

	mu             sync.Mutex
	records        map[string]Record
	f              *os.File // append-only log handle
	logRecords     int
	pendingDirSync bool
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
	// The supplied parent is established state; persist this directory and log name.
	for _, namespace := range []string{dir, filepath.Dir(filepath.Clean(dir))} {
		if err := syncDir(namespace); err != nil {
			_ = f.Close()
			return nil, err
		}
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
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		s.logRecords++
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
	return s.PrepareBound(operationID, action, sessionID, "", "")
}

// PrepareBound is Prepare with optional session-incarnation and exact-request fences. The
// bindings are part of the durable claim, so a delayed message cannot be applied to a
// replacement process and a colliding id cannot inherit another body's terminal outcome.
func (s *Store) PrepareBound(operationID, action, sessionID, sessionInstance, requestHash string) (Record, bool, error) {
	if operationID == "" {
		return Record{}, false, errors.New("idempotency: empty operation_id")
	}
	if len(operationID) > MaxOperationIDLen {
		return Record{}, false, fmt.Errorf("idempotency: operation_id length %d exceeds %d", len(operationID), MaxOperationIDLen)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncPendingDirectoryLocked(); err != nil {
		return Record{}, false, err
	}
	if rec, ok := s.records[operationID]; ok {
		return rec, true, nil
	}
	if err := s.admitLocked(); err != nil {
		return Record{}, false, err
	}
	now := s.clock()
	rec := Record{
		OperationID:     operationID,
		Action:          action,
		SessionID:       sessionID,
		SessionInstance: sessionInstance,
		RequestHash:     requestHash,
		Phase:           PhasePrepared,
		CreatedAt:       now,
		UpdatedAt:       now,
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
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[operationID]
	if !ok {
		return fmt.Errorf("idempotency: unknown operation_id %q", operationID)
	}
	if rec.Phase != PhasePrepared {
		return fmt.Errorf("idempotency: operation_id %q is %s, not prepared", operationID, rec.Phase)
	}
	rec.Phase = PhaseExecuting
	rec.UpdatedAt = s.clock()
	if err := s.appendLocked(rec); err != nil {
		return err
	}
	s.records[operationID] = rec
	return nil
}

// SetRequest preserves immutable reconciliation coordinates before an operation can execute.
func (s *Store) SetRequest(operationID string, request []byte) error {
	if len(request) == 0 || len(request) > 64<<10 || !json.Valid(request) {
		return errors.New("idempotency: invalid request descriptor")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[operationID]
	if !ok {
		return errors.New("idempotency: unknown request operation")
	}
	if len(rec.Request) != 0 {
		if bytes.Equal(rec.Request, request) {
			return nil
		}
		return errors.New("idempotency: request descriptor conflict")
	}
	if rec.Phase != PhasePrepared {
		return errors.New("idempotency: request must precede execution")
	}
	rec.Request = append(json.RawMessage(nil), request...)
	rec.UpdatedAt = s.clock()
	if err := s.appendLocked(rec); err != nil {
		return err
	}
	s.records[operationID] = rec
	return nil
}

// SetEvidence retains the first qualified native receipt without replacing the protocol outcome.
func (s *Store) SetEvidence(operationID string, evidence []byte) error {
	if len(evidence) == 0 || len(evidence) > 64<<10 || !json.Valid(evidence) {
		return errors.New("idempotency: invalid native evidence")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[operationID]
	if !ok {
		return errors.New("idempotency: unknown evidence operation")
	}
	if len(rec.Evidence) != 0 {
		if bytes.Equal(rec.Evidence, evidence) {
			return nil
		}
		return errors.New("idempotency: native evidence conflict")
	}
	if rec.Phase != PhaseExecuting && rec.Phase != PhaseOutcomeUnknown {
		return errors.New("idempotency: native evidence requires unresolved execution")
	}
	rec.Evidence = append(json.RawMessage(nil), evidence...)
	rec.UpdatedAt = s.clock()
	if err := s.appendLocked(rec); err != nil {
		return err
	}
	s.records[operationID] = rec
	return nil
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
	if phase == PhaseFailed && (rec.Action == "semantic_send" || rec.Action == "native_decide") && len(rec.Evidence) > 0 {
		return errors.New("idempotency: native evidence cannot become a failed delivery")
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
	rec.Request = append(json.RawMessage(nil), rec.Request...)
	rec.Evidence = append(json.RawMessage(nil), rec.Evidence...)
	rec.Outcome = append(json.RawMessage(nil), rec.Outcome...)
	return rec, ok
}

// UnobservedExecution reports delivery that still requires a qualified terminal commit.
func (s *Store) UnobservedExecution(action, session, except string) bool {
	return s.UnobservedOperation(action, session, except) != ""
}

// UnobservedOperation selects one unresolved delivery without copying retained payloads.
func (s *Store) UnobservedOperation(action, session, except string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, rec := range s.records {
		if id != except && rec.Action == action && rec.SessionID == session && unresolvedPhase(rec.Phase) {
			return id
		}
	}
	return ""
}

func unresolvedPhase(phase Phase) bool {
	return phase != PhasePrepared && phase != PhaseCompleted && phase != PhaseFailed
}

// List returns a snapshot of every record (order unspecified). The daemon's
// Open-time stale-launch resolver walks it to re-drivably fail launch records
// whose reserved session did not survive a crash.
func (s *Store) List() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for _, rec := range s.records {
		rec.Request = append(json.RawMessage(nil), rec.Request...)
		rec.Evidence = append(json.RawMessage(nil), rec.Evidence...)
		rec.Outcome = append(json.RawMessage(nil), rec.Outcome...)
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
	} else if err := s.admitLocked(); err != nil {
		return Record{}, err
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

// Compact drops only TTL-expired records (R-IDP.4), rewriting the log
// atomically (tmp+rename).
func (s *Store) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compactLocked(s.clock())
}

// Capacity refuses new effects; an exact retry keeps its existing guard and outcome.
func (s *Store) admitLocked() error {
	if s.opts.MaxEntries <= 0 || len(s.records) < s.opts.MaxEntries {
		return nil
	}
	if s.opts.TTL <= 0 {
		return ErrCapacity
	}
	now := s.clock()
	protected := 0
	for _, rec := range s.records {
		if !s.expired(rec, now) {
			protected++
			if protected >= s.opts.MaxEntries {
				return ErrCapacity
			}
		}
	}
	return s.compactLocked(now)
}

func (s *Store) compactLocked(now time.Time) error {
	kept := make([]Record, 0, len(s.records))
	for _, rec := range s.records {
		if s.expired(rec, now) {
			continue
		}
		kept = append(kept, rec)
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

func (s *Store) expired(rec Record, now time.Time) bool {
	// An unresolved native write must never age into a fresh owner semantic send.
	if (rec.Action == "semantic_send" || rec.Action == "native_decide") && unresolvedPhase(rec.Phase) {
		return false
	}
	return s.opts.TTL > 0 && now.Sub(rec.UpdatedAt) > s.opts.TTL
}

// appendLocked writes one record as a JSON line and fsyncs BEFORE returning, so the
// durable record precedes any side effect the caller performs next.
func (s *Store) appendLocked(rec Record) error {
	if err := s.syncPendingDirectoryLocked(); err != nil {
		return err
	}
	if s.opts.MaxEntries > 0 && s.logRecords >= 2*s.opts.MaxEntries {
		if err := s.compactLocked(s.clock()); err != nil {
			return err
		}
	}
	line := append(mustMarshal(rec), '\n')
	if _, err := s.f.Write(line); err != nil {
		return err
	}
	if err := s.f.Sync(); err != nil {
		return err
	}
	s.logRecords++
	return nil
}

func (s *Store) syncPendingDirectoryLocked() error {
	if s.pendingDirSync {
		if err := syncDir(s.dir); err != nil {
			return err
		}
		s.pendingDirSync = false
	}
	return nil
}

// renameLog is os.Rename, a test seam so a Compact failure at the rename step can
// be injected to verify the store degrades to "not compacted this cycle" rather
// than being left dead.
var renameLog = os.Rename

// syncDir fsyncs dir so a preceding rename within it is durable across a crash
// (mirrors remotegw.persistSeqCeiling's tmp+fsync+rename+dirsync idiom). Without
// this a power loss can lose the rename and resurrect the pre-compaction log,
// reopening replay risk. A package var so tests can spy on / inject a failure
// into this step.
var syncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// rewriteLocked replaces the log with kept, atomically (tmp+fsync+rename+dirsync),
// swapping the append handle onto the renamed file. Until the rename lands, the OLD
// handle s.f is untouched, so a create/write/sync/rename failure returns an error
// while the store keeps working on the pre-compaction log -- degrading to "not
// compacted this cycle" rather than leaving s.f closed/nil. Once the rename lands the
// store swaps s.f onto the tmp handle (which now names logPath) BEFORE the dir sync:
// a successful rename unlinks the old inode, so continuing to write to the old handle
// would fsync into a ghost that is gone on restart -- silently dropping records and
// letting a still-valid replay execute twice ("usable" is not "durable"). A dir-sync
// failure after the swap leaves the new handle installed, but further writes and
// acknowledgements wait for the pending directory durability barrier.
func (s *Store) rewriteLocked(kept []Record) error {
	tmp, err := os.CreateTemp(s.dir, logFile+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename below succeeds
	for _, rec := range kept {
		if _, err := tmp.Write(append(mustMarshal(rec), '\n')); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	logPath := filepath.Join(s.dir, logFile)
	if err := renameLog(tmpName, logPath); err != nil {
		_ = tmp.Close()
		return err // rename never landed: s.f is still the valid pre-compaction handle
	}
	s.pendingDirSync = true
	// Rename landed: logPath now names tmp's inode, and the OLD s.f names the unlinked
	// (renamed-over) inode. tmp already names logPath, so swap onto it NOW -- before
	// the dir sync -- so even a syncDir failure leaves the store writing to the live
	// at-logPath inode rather than the ghost. tmp is positioned at end-of-file and
	// every append runs single-writer under s.mu, so it appends exactly like the
	// O_APPEND handle it replaces.
	if s.f != nil {
		_ = s.f.Close()
	}
	s.f = tmp
	s.logRecords = len(kept)
	// tmp's data is already fsync'd; syncDir makes the rename (the dir entry) durable.
	return s.syncPendingDirectoryLocked()
}

func mustMarshal(rec Record) []byte {
	b, _ := json.Marshal(rec) // Record is always marshalable
	return b
}

// CompareEvidence checkpoints newer qualified evidence without changing delivery state.
func (s *Store) CompareEvidence(operationID string, expected, next []byte) error {
	if len(expected) == 0 || len(next) == 0 || len(next) > 64<<10 || !json.Valid(next) {
		return errors.New("idempotency: invalid evidence checkpoint")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[operationID]
	if !ok || !bytes.Equal(rec.Evidence, expected) {
		return errors.New("idempotency: stale evidence checkpoint")
	}
	if rec.Phase != PhaseExecuting && rec.Phase != PhaseOutcomeUnknown && rec.Phase != PhaseCompleted {
		return errors.New("idempotency: evidence checkpoint has no native execution")
	}
	if bytes.Equal(expected, next) {
		return nil
	}
	rec.Evidence = append(json.RawMessage(nil), next...)
	rec.UpdatedAt = s.clock()
	if err := s.appendLocked(rec); err != nil {
		return err
	}
	s.records[operationID] = rec
	return nil
}

// RequestOperations builds a worker's derived request index once, without copying payloads.
func (s *Store) RequestOperations(action, session string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, rec := range s.records {
		if rec.Action == action && rec.SessionID == session && len(rec.Request) > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

// PendingRequestOperations skips confirmed history before callers inspect request bodies.
func (s *Store) PendingRequestOperations(action, session, instance string, limit int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, rec := range s.records {
		if rec.Action == action && rec.SessionID == session && rec.SessionInstance == instance && len(rec.Request) > 0 && len(rec.Evidence) == 0 && unresolvedPhase(rec.Phase) {
			ids = append(ids, id)
			if len(ids) > limit {
				break
			}
		}
	}
	return ids
}
