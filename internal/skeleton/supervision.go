package skeleton

// THE PASSIVE SUPERVISOR (ADR-010 Amendment 3 C2..C4): the daemon-side component that
// wakes a handoff SOURCE when its passive child needs attention, so the source neither
// runs a foreground watch loop nor is interrupted while it is busy.
//
// WHY IT LIVES HERE. Like the approval and capability components, it needs the assembly's
// two seams -- the engine's status emission and the session-end hook (serve.go emitStatus
// / endSession) -- plus the owner-tier Server's exported SendInput (C3: the same serialized
// path `swarm send` uses; no new op) and IsControlled (a human holding the source's lease
// is never interrupted). internal/skeleton is the only package that holds all of them.
//
// SIGNAL IS SYNCHRONOUS, DELIVERY IS NOT. signal is called from the engine's status path
// under the engine's own cadence, so it must be cheap: it evaluates the child's CURRENT
// meta under s.mu, records at most one pending event, and returns. Typing the notification
// holds the source's input serialization for at least submitframe.Gap and dials the shim
// (protocol.sendMessage), so it runs on ONE background goroutine that is woken by signal
// and by a retry ticker (C3: a human detach emits no status signal, so only a cadence can
// notice the lease is gone). s.mu is never held across send. Delivery is idempotent per
// event sequence: the goroutine re-checks the record's pending seq under s.mu right before
// and right after the send, so a replaced or retired event is dropped, never typed twice.
//
// LEVEL-TRIGGERED (ADR-008; C2): at most ONE pending event per child. A newer attention
// state replaces an undelivered older one, and the per-child seq still increases so ids
// stay distinct.
//
// DURABLE (C4): one 0600 JSON record per child under <stateDir>/supervision (0700), so a
// pending event survives a daemon restart and is delivered exactly once across it: the
// next incarnation loads the records and its retry cadence delivers them with no signal.
//
// THE NOTIFICATION CARRIES NO SESSION-AUTHORED TEXT (C3): ids, seq, group and interaction
// only. The source retrieves output deliberately with `swarm peek`, so an untrusted child
// cannot inject instructions into its supervisor through this channel.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// supervisionRetry is the cadence at which pending events re-check the source (C3).
const supervisionRetry = 2 * time.Second

// supervisionRecord is one passive child's durable supervision state (C4).
type supervisionRecord struct {
	Child     string `json:"child"`  // local id
	Source    string `json:"source"` // local id of the session that receives notifications
	LastGroup string `json:"last_group"`
	// SeenWorking gates the first ready_for_review (C2): it is set only once an OBSERVED
	// status carries raw Turn == active. The launch baseline (Turn unknown, which Derive
	// also maps to working) must NOT count, or a heuristic idle read right after launch
	// would wake the source before the child has done anything.
	SeenWorking bool              `json:"seen_working"`
	Seq         uint64            `json:"seq"`
	Pending     *supervisionEvent `json:"pending,omitempty"`
}

// supervisionEvent is one attention event: the child entered Group (with Interaction,
// which tells a prompt from a permission request) as the record's Seq-th event.
type supervisionEvent struct {
	Seq         uint64             `json:"seq"`
	Group       status.Group       `json:"group"`
	Interaction status.Interaction `json:"interaction"`
}

// supervisor is the component. get, controlled and send are the assembly seams
// (d.core.Get, d.srv.IsControlled, d.srv.SendInput in production; fakes in tests).
type supervisor struct {
	endpointID string
	dir        string
	retry      time.Duration
	get        func(local string) (persist.Meta, bool)
	controlled func(local string) bool
	send       func(local string, req protocol.SendInputReq) error

	mu      sync.Mutex
	records map[string]*supervisionRecord // by child local id

	wake     chan struct{} // buffered 1: a coalescing nudge for the delivery goroutine
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// newSupervisor opens (creating if needed) the record dir, loads every record a prior
// incarnation left for a child still on the roster, and starts the delivery goroutine.
// Anything loaded pending is delivered by the retry cadence with no signal (C4).
func newSupervisor(endpointID, dir string, retry time.Duration,
	get func(local string) (persist.Meta, bool),
	controlled func(local string) bool,
	send func(local string, req protocol.SendInputReq) error) (*supervisor, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil { // persist.NewStore precedent: umask-proof
		return nil, err
	}
	s := &supervisor{
		endpointID: endpointID, dir: dir, retry: retry,
		get: get, controlled: controlled, send: send,
		records: map[string]*supervisionRecord{},
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var r supervisionRecord
		if err := json.Unmarshal(data, &r); err != nil || r.Child == "" {
			log.Printf("skeleton: supervision record %s is unreadable, skipped: %v", e.Name(), err)
			continue
		}
		if _, ok := get(r.Child); !ok {
			// The child was deleted while the daemon was down (a live delete signals
			// and retires): nothing to supervise, and its source must not hear of it.
			_ = os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		s.records[r.Child] = &r
		if r.Pending != nil {
			s.nudge()
		}
	}
	s.wg.Add(1)
	go s.run()
	return s, nil
}

// arm creates the record for a passive handoff child (C2). Idempotent: an existing
// record -- armed earlier, or loaded from a prior incarnation -- is kept with its seq and
// pending event, so a reconcile re-arm never resets the sequence. Any other session is
// ignored. LastGroup starts at the armed meta's own group so the first signal reports
// only a CHANGE from the state the child was armed in.
func (s *supervisor) arm(m persist.Meta) {
	if m.SpawnIntent != protocol.SpawnIntentHandoff || m.Supervision != protocol.SupervisionPassive {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[m.ID]; ok {
		return
	}
	r := &supervisionRecord{Child: m.ID, Source: m.SpawnedFrom, LastGroup: string(status.Derive(m.Status))}
	s.records[m.ID] = r
	s.persistLocked(r)
}

// signal is the assembly's seam: a status emission or session end for local. As a CHILD
// it is evaluated now; as a SOURCE (its state may have turned safe) delivery of its
// children's pending events is nudged. A session with no record is a no-op.
func (s *supervisor) signal(local string) {
	s.mu.Lock()
	nudge := false
	if r, ok := s.records[local]; ok {
		nudge = s.evaluateLocked(r)
	}
	for _, r := range s.records {
		if r.Source == local && r.Pending != nil {
			nudge = true
			break
		}
	}
	s.mu.Unlock()
	if nudge {
		s.nudge()
	}
}

// pending reports whether local has an undelivered attention event
// (SessionView.SupervisionPending, C5).
func (s *supervisor) pending(local string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[local]
	return ok && r.Pending != nil
}

// close stops the delivery goroutine and waits for it: no send starts after it returns.
// Idempotent.
func (s *supervisor) close() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.wg.Wait()
}

// evaluateLocked reads the child's CURRENT meta (never a stale signal) and records a new
// attention event when the child ENTERS an attention group. Reports whether a delivery
// nudge is due. A child gone from the roster retires its record (C4). Caller holds s.mu.
func (s *supervisor) evaluateLocked(r *supervisionRecord) bool {
	m, ok := s.get(r.Child)
	if !ok {
		s.retireLocked(r)
		return false
	}
	changed := false
	if m.Status.Turn == status.TurnActive && !r.SeenWorking {
		r.SeenWorking = true // an OBSERVED active turn, never the launch baseline
		changed = true
	}
	g := status.Derive(m.Status)
	if string(g) != r.LastGroup {
		r.LastGroup = string(g)
		changed = true
		if g == status.GroupNeedsInput || g == status.GroupCompleted || (g == status.GroupReadyForReview && r.SeenWorking) {
			r.Seq++ // a newer attention state REPLACES an undelivered one; the seq still advances
			r.Pending = &supervisionEvent{Seq: r.Seq, Group: g, Interaction: m.Status.Interaction}
		}
	}
	if changed {
		s.persistLocked(r)
	}
	return r.Pending != nil
}

// nudge wakes the delivery goroutine without blocking; a nudge while one is already
// queued coalesces into it.
func (s *supervisor) nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// pendingDelivery is one event sampled under s.mu for an attempt outside it.
type pendingDelivery struct {
	child, source string
	ev            supervisionEvent
}

// run is the ONE delivery goroutine: on every wake or retry tick it attempts every
// pending event, serially, so an event is never in flight twice.
func (s *supervisor) run() {
	defer s.wg.Done()
	t := time.NewTicker(s.retry)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
		case <-t.C:
		}
		s.mu.Lock()
		var due []pendingDelivery
		for _, r := range s.records {
			if r.Pending != nil {
				due = append(due, pendingDelivery{r.Child, r.Source, *r.Pending})
			}
		}
		s.mu.Unlock()
		for _, p := range due {
			select {
			case <-s.stop:
				return
			default:
			}
			s.deliver(p.child, p.source, p.ev)
		}
	}
}

// deliver types ev's notification into source if source is safe to interrupt (C3), then
// clears the event; a delivered `completed` retires the record (C4). On an unsafe source
// or a failed send the event simply stays pending for the next attempt. Never holds s.mu
// across send.
func (s *supervisor) deliver(child, source string, ev supervisionEvent) {
	if !s.sourceSafe(source) || !s.stillPending(child, ev.Seq) {
		return
	}
	req := protocol.SendInputReq{Text: supervisionNotification(protocol.NamespacedID(s.endpointID, child), ev), Submit: true}
	if err := s.send(source, req); err != nil {
		log.Printf("skeleton: supervision notification %s#%d to %s not delivered, will retry: %v", child, ev.Seq, source, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[child]
	if !ok || r.Pending == nil || r.Pending.Seq != ev.Seq {
		return // replaced or retired while in flight; the newer state has its own delivery
	}
	r.Pending = nil
	if ev.Group == status.GroupCompleted {
		s.retireLocked(r)
		return
	}
	s.persistLocked(r)
}

// sourceSafe is C3's gate: the source is running, its turn is idle, it is waiting on
// neither a permission dialog nor a question of its own (a typed notification would be
// read as the human's answer to either), and no controller lease is held on it. A source
// gone from the roster is unsafe forever: the record stays pending as the orphan the
// roster shows (C4).
func (s *supervisor) sourceSafe(source string) bool {
	m, ok := s.get(source)
	return ok && m.Status.Process == status.ProcessRunning &&
		m.Status.Turn == status.TurnIdle &&
		m.Status.Interaction != status.InteractionPermission &&
		m.Status.Interaction != status.InteractionPrompt &&
		!s.controlled(source)
}

// stillPending reports whether child's pending event is still seq -- the re-check right
// before a send, so an event replaced or retired since the goroutine sampled it is dropped.
func (s *supervisor) stillPending(child string, seq uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[child]
	return ok && r.Pending != nil && r.Pending.Seq == seq
}

// retireLocked drops the record and its file. Caller holds s.mu.
func (s *supervisor) retireLocked(r *supervisionRecord) {
	delete(s.records, r.Child)
	if err := os.Remove(s.path(r.Child)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("skeleton: remove supervision record for %s: %v", r.Child, err)
	}
}

// persistLocked writes the record atomically (temp + fsync + rename, the capability
// store's pattern) as a 0600 file. Logged, never fatal: an unwritable record leaves this
// incarnation governed by the in-memory copy. Caller holds s.mu.
func (s *supervisor) persistLocked(r *supervisionRecord) {
	if err := s.write(r); err != nil {
		log.Printf("skeleton: persist supervision record for %s: %v", r.Child, err)
	}
}

func (s *supervisor) write(r *supervisionRecord) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, r.Child+".json.tmp*") // os.CreateTemp creates 0600
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
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
	return os.Rename(tmpName, s.path(r.Child))
}

func (s *supervisor) path(child string) string { return filepath.Join(s.dir, child+".json") }

// supervisionNotification is the exact one-line message typed into the source (C3): the
// event id, the child's NAMESPACED id (the form `swarm peek` / `swarm send` take), its
// state, and how to act. A pure function of its arguments, and well under
// protocol.MaxSendInputText. child is the namespaced id; the local part names the event.
func supervisionNotification(child string, ev supervisionEvent) string {
	local := child
	if _, l, ok := protocol.ParseID(child); ok {
		local = l
	}
	var state string
	switch {
	case ev.Group == status.GroupNeedsInput && ev.Interaction == status.InteractionPermission:
		state = "needs_input (permission request - ask the human, never approve it yourself)"
	case ev.Group == status.GroupNeedsInput:
		state = "needs_input (prompt)"
	case ev.Group == status.GroupCompleted:
		state = "completed - do a final review of its work and report"
	default:
		state = string(ev.Group)
	}
	return fmt.Sprintf("[swarm supervision %s#%d] child %s is %s. Inspect: swarm peek %s · steer: swarm send %s --text \"...\" · never approve permissions yourself.",
		local, ev.Seq, child, state, child, child)
}
