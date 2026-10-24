package skeleton

import (
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/contextguard"
)

const (
	backendIngressMaxFrames = 64
	backendIngressMaxBytes  = 1 << 20
)

type backendIngressFrame struct {
	method string
	raw    []byte
	at     time.Time
}

// One physical feed owns this FIFO. Count/bytes include the frame in processing;
// the socket callback retains its already-owned raw copy and never runs disk work.
type backendIngress struct {
	mu                  sync.Mutex
	frames              [backendIngressMaxFrames]backendIngressFrame
	head, size, bytes   int
	pending             atomic.Int64
	received, completed atomic.Uint64
	lost, stopped       atomic.Bool
	wake                chan struct{}
	done                chan struct{}
	process             func(backendIngressFrame)
	invalidate          func() bool
	current             func() bool
	finished            func(*backendIngress)
	settled             func()
	closeSource         func() error
}

func newBackendIngress(process func(backendIngressFrame), invalidate func() bool, current func() bool, finished func(*backendIngress), settled func()) *backendIngress {
	q := &backendIngress{wake: make(chan struct{}, 1), done: make(chan struct{}), process: process, invalidate: invalidate, current: current, finished: finished, settled: settled}
	go q.run()
	return q
}

func (q *backendIngress) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
func (q *backendIngress) markLoss() { q.lost.Store(true); q.signal() }
func (q *backendIngress) enqueue(method string, raw []byte, at time.Time) bool {
	q.pending.Add(1)
	q.mu.Lock()
	if q.stopped.Load() {
		q.pending.Add(-1)
		q.mu.Unlock()
		return false
	}
	q.received.Add(1)
	if q.pending.Load() > backendIngressMaxFrames || len(raw) > backendIngressMaxBytes-q.bytes {
		q.lost.Store(true)
		q.pending.Add(-1)
		q.mu.Unlock()
		q.signal()
		return false
	}
	q.frames[(q.head+q.size)%backendIngressMaxFrames] = backendIngressFrame{method: method, raw: raw, at: at}
	q.size++
	q.bytes += len(raw)
	q.mu.Unlock()
	q.signal()
	return true
}
func (q *backendIngress) automationSafe() bool {
	return q != nil && !q.stopped.Load() && !q.lost.Load() && q.pending.Load() == 0
}
func (q *backendIngress) stop() {
	q.mu.Lock()
	q.stopped.Store(true)
	if q.pending.Load() != 0 {
		q.lost.Store(true)
	}
	for q.size > 0 {
		f := q.frames[q.head]
		q.frames[q.head] = backendIngressFrame{}
		q.head = (q.head + 1) % backendIngressMaxFrames
		q.size--
		q.bytes -= len(f.raw)
		q.pending.Add(-1)
	}
	q.mu.Unlock()
	q.signal()
}
func (q *backendIngress) run() {
	defer close(q.done)
	if q.finished != nil {
		defer q.finished(q)
	}
	lossAttempted := false
	for {
		q.mu.Lock()
		if q.lost.Load() && !lossAttempted {
			q.mu.Unlock()
			if q.current() && q.invalidate() {
				lossAttempted = true
				continue
			}
			q.mu.Lock()
			stopped := q.stopped.Load()
			q.mu.Unlock()
			if stopped {
				return
			}
			<-q.wake
			continue
		}

		if q.stopped.Load() {
			q.mu.Unlock()
			return
		}
		if q.size == 0 {
			q.mu.Unlock()
			<-q.wake
			continue
		}
		frame := q.frames[q.head]
		q.frames[q.head] = backendIngressFrame{}
		q.head = (q.head + 1) % backendIngressMaxFrames
		q.size--
		q.mu.Unlock()
		if q.current() {
			q.process(frame)
		}
		q.mu.Lock()
		q.bytes -= len(frame.raw)
		q.pending.Add(-1)
		q.completed.Add(1)
		q.mu.Unlock()
		if q.automationSafe() && q.current() && q.settled != nil {
			q.settled()
		}
	}
}

// Nil is reserved for existing synchronous/manual test feeds. Production dials
// install ingress before the first socket callback.
func (f *backendFeed) ingressAutomationSafe() bool {
	return f != nil && !f.retired.Load() && (f.ingress == nil || f.ingress.automationSafe())
}
func (f *backendFeed) retireIngress() {
	f.retired.Store(true)
	if f.ingress != nil {
		f.ingress.stop()
	}
}

func (d *Daemon) startBackendIngress(local, instance string, feed *backendFeed) bool {
	feed.ingress = newBackendIngress(func(frame backendIngressFrame) {
		var envelope struct {
			Params json.RawMessage `json:"params"`
			ID     json.RawMessage `json:"id"`
		}
		if json.Unmarshal(frame.raw, &envelope) != nil {
			return
		}
		guard := rebuildFrame(frame.method, envelope.ID, envelope.Params)
		d.ingestBackendFrameForFeed(local, instance, feed, frame.method, frame.raw, frame.at, guard)
	}, func() bool { return d.invalidateBackendIngress(local, instance, feed) }, func() bool { return d.backendIngressCurrent(local, instance, feed) }, func(q *backendIngress) {
		d.backendIngressMu.Lock()
		delete(d.backendIngress, q)
		d.backendIngressMu.Unlock()
	}, func() {
		if d.contextGuards != nil {
			d.contextGuards.noteStatus(local)
		}
	})
	q := feed.ingress
	d.backendIngressMu.Lock()
	if d.backendIngressClosing {
		q.stop()
		d.backendIngressMu.Unlock()
		return false
	}
	if d.backendIngress == nil {
		d.backendIngress = make(map[*backendIngress]struct{})
	}
	d.backendIngress[q] = struct{}{}
	d.backendIngressMu.Unlock()
	return true
}
func (d *Daemon) backendIngressCurrent(local, instance string, feed *backendFeed) bool {
	if feed.retired.Load() {
		return false
	}
	current, ok := d.sessionInstance(local)
	if !ok || current != instance {
		return false
	}
	backend, live := d.sessionBackendFor(local)
	return !live || backend.feed == feed
}

// The return value proves exact feed ownership, not a durable acknowledgement.
// Loss remains fenced even if engine persistence fails or guard reduction is pending.
func (d *Daemon) invalidateBackendIngress(local, instance string, feed *backendFeed) bool {
	if d.contextGuards != nil {
		d.contextGuards.replaceMu.Lock()
		defer d.contextGuards.replaceMu.Unlock()
	}
	if !d.backendIngressCurrent(local, instance, feed) {
		return false
	}
	at := time.Now()
	sequence := feed.seq.Add(1)
	feed.guardMu.Lock()
	feed.guardDropped = true
	feed.guardDropAt = at
	if feed.guardReady && d.contextGuards != nil {
		if backend, live := d.sessionBackendFor(local); live && backend.feed == feed {
			d.contextGuards.ingestLoss(local, contextguard.Key{SessionID: local, SessionInstance: instance, BackendEpoch: feed.epoch, ProviderThreadID: backend.threadID}, sequence, at)
		}
	}
	feed.guardMu.Unlock()
	if d.eng != nil {
		if err := d.eng.InvalidateExecution(local); err != nil {
			log.Printf("skeleton: backend ingress execution-loss persistence unavailable for session %s", local)
		}
	}
	d.emitBackendGap(local, "backend_ingress_loss")
	return true
}
func (d *Daemon) stopBackendIngress() {
	d.backendIngressMu.Lock()
	d.backendIngressClosing = true
	queues := make([]*backendIngress, 0, len(d.backendIngress))
	for q := range d.backendIngress {
		queues = append(queues, q)
	}
	d.backendIngressMu.Unlock()
	for _, q := range queues {
		q.stop()
		q.closeConnection()
	}
	// Synchronous filesystem work cannot be cancelled. Join outside every lifecycle
	// lock before Close tears down the engine/stores the in-flight callback uses.
	for _, q := range queues {
		<-q.done
	}
}

func (q *backendIngress) bindConnection(close func() error) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped.Load() {
		return false
	}
	q.closeSource = close
	return true
}
func (q *backendIngress) closeConnection() {
	q.mu.Lock()
	close := q.closeSource
	q.closeSource = nil
	q.mu.Unlock()
	if close != nil {
		_ = close()
	}
}

func (d *Daemon) clearBackendIngressLoss(local string) {
	d.backend.mu.Lock()
	delete(d.backend.ingressLoss, local)
	d.backend.mu.Unlock()
}
