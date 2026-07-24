package phonecore

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// ErrQueueFull is returned by Enqueue when the bounded queue is at capacity. The queue
// is left unchanged -- an already-queued command is never silently dropped (R-PHC.4).
var ErrQueueFull = errors.New("phonecore: op queue full")

// QueuedOp is one offline mutating op awaiting replay: the wire op, its target session,
// the pre-SIGNED command (carrying the durable operation_id idempotency key), and the
// launch spec for a launch. The command is signed ONCE at enqueue time; it is never
// re-signed or re-keyed on replay, so the daemon's idempotency dedups a redelivery.
type QueuedOp struct {
	Op        string                   `json:"op"`
	SessionID string                   `json:"session_id"`
	Cmd       schema.DeviceCommandAuth `json:"cmd"`
	Launch    *schema.LaunchReq        `json:"launch,omitempty"`
}

// OpQueue is the phone's bounded, durable, in-order offline queue of mutating ops
// (R-PHC.4 / D.0-A4: interrupt/kill/approve/launch -- input is excluded and rides a
// live take-control session instead). It is replayed in FIFO order on reconnect.
type OpQueue struct {
	mu  sync.Mutex
	cap int
	ops []QueuedOp
}

// NewOpQueue returns an empty queue bounded to cap entries (cap <= 0 means unbounded).
func NewOpQueue(cap int) *OpQueue {
	return &OpQueue{cap: cap}
}

// Enqueue appends op, or returns ErrQueueFull (leaving the queue unchanged) when the
// bound is reached.
func (q *OpQueue) Enqueue(op QueuedOp) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cap > 0 && len(q.ops) >= q.cap {
		return ErrQueueFull
	}
	q.ops = append(q.ops, op)
	return nil
}

// Drain returns every queued op in FIFO order and empties the queue.
func (q *OpQueue) Drain() []QueuedOp {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.ops
	q.ops = nil
	return out
}

// Peek returns a snapshot of the queued ops in FIFO order without removing them.
func (q *OpQueue) Peek() []QueuedOp {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]QueuedOp, len(q.ops))
	copy(out, q.ops)
	return out
}

// Len is the number of queued ops.
func (q *OpQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.ops)
}

// Marshal serializes the queue for durable persistence (R-PHC.4). The persistence layer
// (R-PHC.8) writes the bytes at rest; Load restores them on next launch.
func (q *OpQueue) Marshal() ([]byte, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return json.Marshal(q.ops)
}

// Load replaces the queue contents from a Marshal blob (FIFO order preserved).
func (q *OpQueue) Load(blob []byte) error {
	var ops []QueuedOp
	if err := json.Unmarshal(blob, &ops); err != nil {
		return err
	}
	q.mu.Lock()
	q.ops = ops
	q.mu.Unlock()
	return nil
}
