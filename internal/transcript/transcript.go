// Package transcript is the append-only transcript writer for a session's PTY
// byte stream (Epic 3, build-plan.md). It carries invariant S9 for the write
// path: Write must never block the caller, even when the underlying disk is
// slow, wedged, or permanently failing, because the caller here is the PTY
// drain loop and it must never stall on transcript I/O.
//
// The public surface (Config, New, Writer.Write, Writer.Close, plus
// Writer.Dropped and Writer.Flush required by E3.4/E3.5) is intentionally
// small. Internally, Write only ever appends to a bounded in-memory buffer and
// signals a background goroutine; that goroutine is the sole caller of the
// sink (see sink.go), so disk latency or failure never propagates back to the
// Write call.
package transcript

import (
	"bytes"
	"errors"
	"sync"
)

// Config controls rotation for a Writer.
type Config struct {
	MaxBytes int64 // rotation threshold per file
	MaxFiles int   // rotated generations kept (current + N-1 older)
}

// bufCap is the bounded internal buffer capacity, in bytes, that a Writer
// queues ahead of the sink before it starts dropping the incoming tail
// (E3.5). Tests shrink it to get a small, deterministic capacity.
var bufCap int64 = 64 * 1024

// sink is the destination a Writer drains its internal buffer into.
// Production wraps the rotating on-disk file(s); tests substitute a fake.
type sink interface {
	Write(p []byte) (int, error)
	Sync() error
	Close() error
}

// newSink constructs the production sink for New(path, cfg). It is a var so
// tests can point it at a fake sink for the duration of one test.
var newSink = newFileSink

// errClosing is returned by Flush once Close has been requested: the drain
// goroutine that would service a Flush request may already be gone (R-1/F2).
var errClosing = errors.New("transcript: Flush called on a closing or closed Writer")

// Writer is an append-only, non-blocking transcript writer. All exported
// methods are safe for concurrent use.
type Writer struct {
	sink sink

	mu   sync.Mutex
	cond *sync.Cond
	// pending holds queued writes not yet handed to the sink, one slice per
	// accepted Write (or committed held frame), bounded in total bytes by
	// bufCap. Chunk boundaries are preserved deliberately: the sink decides
	// rotation by the cumulative size *after a call returns*, so dispatching
	// one chunk per sink.Write call gives each accepted write its own
	// rotation decision (E3.1). Concatenating chunks before dispatch would
	// collapse several writes' worth of rotations into one.
	pending      [][]byte
	pendingBytes int64         // sum of len() across pending, for O(1) capacity checks
	held         []byte        // most recent repaint frame, not yet folded into pending (E3.2 collapse)
	dropped      int64         // cumulative bytes never handed to the sink
	closing      bool          // Close has been requested
	flushWaiters []chan error  // Flush callers waiting for pending to fully drain
	drainDone    chan struct{} // closed once the drain goroutine has exited
	closeErr     error         // result of the sink's Close, set once before drainDone closes
}

// New opens (creating if needed) the transcript at path and starts its
// background drain goroutine. cfg.MaxBytes and cfg.MaxFiles must both be
// positive: a zero-value Config must never silently produce an uncapped,
// never-rotating transcript (R-1).
func New(path string, cfg Config) (*Writer, error) {
	if cfg.MaxBytes <= 0 || cfg.MaxFiles <= 0 {
		return nil, errors.New("transcript: MaxBytes/MaxFiles must be positive")
	}
	s, err := newSink(path, cfg)
	if err != nil {
		return nil, err
	}
	w := &Writer{sink: s, drainDone: make(chan struct{})}
	w.cond = sync.NewCond(&w.mu)
	go w.drainLoop()
	return w, nil
}

// isRepaintFrame reports whether p is a "repaint frame" per the E3.2
// heuristic: it begins with ESC[H (cursor-home), or it begins with \r and
// contains no \n (a carriage-return-only redraw).
func isRepaintFrame(p []byte) bool {
	if bytes.HasPrefix(p, []byte("\x1b[H")) {
		return true
	}
	return len(p) > 0 && p[0] == '\r' && !bytes.Contains(p, []byte("\n"))
}

// Write queues p for the drain goroutine and always reports (len(p), nil) to
// the caller (S9): it never blocks on the sink and never surfaces a sink
// error. Bytes the internal buffer has no room for are dropped from the tail
// and counted in Dropped.
//
// Consecutive repaint frames collapse: while the previously written frame is
// still held (not yet folded into pending), a new repaint frame replaces it
// outright rather than being appended. Any other frame (or Flush/Close)
// commits whatever repaint frame is currently held.
func (w *Writer) Write(p []byte) (int, error) {
	n := len(p)
	if n == 0 {
		return 0, nil
	}
	cp := append([]byte(nil), p...) // own copy: caller may reuse p after Write returns
	repaint := isRepaintFrame(cp)

	w.mu.Lock()
	if repaint && w.held != nil {
		w.held = cp // collapse: replace the still-unflushed previous frame
	} else {
		if w.held != nil {
			w.enqueueLocked(w.held)
			w.held = nil
		}
		if repaint {
			w.held = cp
		} else {
			w.enqueueLocked(cp)
		}
	}
	w.cond.Signal()
	w.mu.Unlock()
	return n, nil
}

// enqueueLocked appends data to pending as its own chunk, dropping (and
// counting in dropped) whatever tail of data would push pending past bufCap.
// Callers must hold mu.
func (w *Writer) enqueueLocked(data []byte) {
	avail := bufCap - w.pendingBytes
	if avail <= 0 {
		w.dropped += int64(len(data))
		return
	}
	if int64(len(data)) > avail {
		w.dropped += int64(len(data)) - avail
		data = data[:avail]
	}
	w.pending = append(w.pending, data)
	w.pendingBytes += int64(len(data))
}

// Dropped returns the cumulative number of bytes that were never handed to
// the sink, either because the internal buffer was full (E3.5 tail-drop) or
// because a dispatch to the sink failed or was short.
func (w *Writer) Dropped() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dropped
}

// Flush blocks until every byte accepted by a Write call that returned before
// this call reaches the sink, then best-effort fsyncs. It is the deterministic
// sync point an otherwise async Writer needs for tests (and for callers that
// need a known-durable checkpoint).
//
// Flush returns errClosing immediately once Close has been requested, rather
// than registering a wait that the drain goroutine — possibly already
// exited — would never service (F2).
func (w *Writer) Flush() error {
	w.mu.Lock()
	if w.closing {
		w.mu.Unlock()
		return errClosing
	}
	if w.held != nil {
		w.enqueueLocked(w.held)
		w.held = nil
	}
	result := make(chan error, 1)
	w.flushWaiters = append(w.flushWaiters, result)
	w.cond.Signal()
	w.mu.Unlock()
	return <-result
}

// Close commits any held repaint frame, lets the drain goroutine finish
// draining pending into the sink, then closes the sink and returns its error.
func (w *Writer) Close() error {
	w.mu.Lock()
	if w.held != nil {
		w.enqueueLocked(w.held)
		w.held = nil
	}
	w.closing = true
	w.cond.Signal()
	w.mu.Unlock()
	<-w.drainDone
	return w.closeErr
}

// drainLoop is the sole goroutine that ever calls sink methods, so the sink
// never needs its own internal locking. It runs until Close has been
// requested and pending is fully drained, then closes the sink and exits.
//
// Two different draining strategies apply depending on why there is work to
// do:
//
//   - Normal operation: exactly one pending chunk is dispatched per sink.Write
//     call, oldest first, so each originally accepted Write keeps its own
//     rotation decision (E3.1) and its own contribution to Dropped (E3.5) —
//     nothing here ever merges two callers' writes into one sink call.
//   - Shutdown (Close already requested): whatever remains in pending is
//     merged into a single best-effort final sink.Write instead of draining
//     chunk by chunk. This bounds Close to at most one extra sink call
//     regardless of backlog size — required so Close cannot be made to take
//     time proportional to the number of prior Writes under a slow sink.
//     Flush (the documented deterministic sync point) is unaffected: it
//     always waits for genuine per-chunk drainage, never this shortcut.
func (w *Writer) drainLoop() {
	for {
		w.mu.Lock()
		for len(w.pending) == 0 && len(w.flushWaiters) == 0 && !w.closing {
			w.cond.Wait()
		}

		if len(w.pending) > 0 {
			var batch []byte
			if w.closing {
				for _, chunk := range w.pending {
					batch = append(batch, chunk...)
				}
				w.pending = nil
				w.pendingBytes = 0
			} else {
				batch = w.pending[0]
				w.pending = w.pending[1:]
				w.pendingBytes -= int64(len(batch))
			}
			w.mu.Unlock()

			n, err := w.sink.Write(batch)
			lost := int64(len(batch) - n)
			if lost < 0 {
				lost = 0
			}
			_ = err // failure is reflected purely as a short write in n; nothing else to surface here

			w.mu.Lock()
			w.dropped += lost
			w.mu.Unlock()
			continue
		}

		if len(w.flushWaiters) > 0 {
			waiters := w.flushWaiters
			w.flushWaiters = nil
			w.mu.Unlock()

			err := w.sink.Sync()
			for _, ch := range waiters {
				ch <- err
			}
			continue
		}

		// w.closing is true and pending/flushWaiters are both empty.
		err := w.sink.Close()
		w.closeErr = err
		w.mu.Unlock()
		close(w.drainDone)
		return
	}
}
