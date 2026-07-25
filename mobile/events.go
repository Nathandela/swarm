package swarmmobile

// PB-BIND-6's delivery plane: a bounded, drop-oldest queue between the core's relay
// drain and the app's EventListener, with the overflow SURFACED.
//
// The listener runs Java code on a Go goroutine. If it blocks -- a UI thread that
// marshals badly, a dialog on the main looper -- the core must keep draining the relay,
// or one slow frame stalls the keystroke path. So delivery is a separate goroutine and
// the queue is bounded: at CallbackQueueSize the OLDEST event is discarded, the discard
// is counted, and the next delivery is preceded by an "overflow" event carrying the
// count. A silent drop would let the app render a stale roster forever believing it live.

import "sync"

type dispatcher struct {
	mu       sync.Mutex
	queue    []*Event
	dropped  int
	listener EventListener
	wake     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
}

func newDispatcher() *dispatcher {
	d := &dispatcher{
		wake: make(chan struct{}, 1),
		stop: make(chan struct{}),
	}
	go d.loop()
	return d
}

// setListener installs (or clears) the app's listener.
func (d *dispatcher) setListener(l EventListener) {
	d.mu.Lock()
	d.listener = l
	d.mu.Unlock()
}

// emit enqueues one event, dropping the OLDEST when the bound is reached.
func (d *dispatcher) emit(e *Event) {
	d.mu.Lock()
	if len(d.queue) >= CallbackQueueSize {
		d.queue = d.queue[1:]
		d.dropped++
	}
	d.queue = append(d.queue, e)
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// next pops the head of the queue, or reports an overflow first when events were
// discarded since the last one was reported.
func (d *dispatcher) next() (EventListener, *Event, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.listener == nil || len(d.queue) == 0 {
		return nil, nil, false
	}
	if d.dropped > 0 {
		n := d.dropped
		d.dropped = 0
		return d.listener, &Event{Kind: "overflow", Dropped: n}, true
	}
	e := d.queue[0]
	d.queue = d.queue[1:]
	return d.listener, e, true
}

func (d *dispatcher) loop() {
	for {
		for {
			l, e, ok := d.next()
			if !ok {
				break
			}
			deliver(l, e)
		}
		select {
		case <-d.stop:
			return
		case <-d.wake:
		}
	}
}

// deliver calls the listener with its own panic barrier. The callback is JAVA code on a
// GO goroutine: gomobile turns a Java throw into a Go panic here, and an unrecovered
// panic on ANY goroutine takes the whole process down, not just the one that called in.
func deliver(l EventListener, e *Event) {
	defer func() { _ = recover() }()
	l.OnEvent(e)
}

// close detaches the listener and stops the delivery goroutine. It is idempotent and it
// deliberately does NOT wait: a wedged OnEvent is app code, and a Close that blocked on
// it would turn a slow UI thread into a hung teardown on the Android main looper.
func (d *dispatcher) close() {
	d.setListener(nil)
	d.stopOnce.Do(func() { close(d.stop) })
	select {
	case d.wake <- struct{}{}:
	default:
	}
}
