package phonecore

// Failing-first tests for the phone offline op queue (R-PHC.4 / amendment D.0-A4): a
// bounded, durable FIFO of already-signed mutating ops (interrupt/kill/approve/launch;
// NOT input) that is replayed in order on reconnect. The idempotency key (the signed
// command's operation_id) is generated once and NEVER regenerated, so replay is safe
// against the daemon's dedup. RED is undefined-only.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func qop(opID string) QueuedOp {
	return QueuedOp{
		Op:        protocol.OpKill,
		SessionID: "m/s1",
		Cmd:       protocol.DeviceCommandAuth{OperationID: opID, DeviceID: "d", Sig: "s"},
	}
}

func TestOpQueue_FIFOReplayPreservesOrderAndKeys(t *testing.T) {
	q := NewOpQueue(8)
	for _, id := range []string{"op-1", "op-2", "op-3"} {
		if err := q.Enqueue(qop(id)); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	if q.Len() != 3 {
		t.Fatalf("Len = %d, want 3", q.Len())
	}
	drained := q.Drain()
	got := []string{drained[0].Cmd.OperationID, drained[1].Cmd.OperationID, drained[2].Cmd.OperationID}
	want := []string{"op-1", "op-2", "op-3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("drain order = %v, want %v (FIFO, keys never regenerated)", got, want)
		}
	}
	if q.Len() != 0 {
		t.Fatalf("Len after Drain = %d, want 0", q.Len())
	}
}

func TestOpQueue_BoundedOverflowRejects(t *testing.T) {
	q := NewOpQueue(2)
	if err := q.Enqueue(qop("op-1")); err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	if err := q.Enqueue(qop("op-2")); err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}
	// The third exceeds the bound: rejected, and the queue is unchanged (no silent drop
	// of a queued command).
	if err := q.Enqueue(qop("op-3")); err != ErrQueueFull {
		t.Fatalf("enqueue over cap = %v, want ErrQueueFull", err)
	}
	if q.Len() != 2 {
		t.Fatalf("Len after rejected enqueue = %d, want 2", q.Len())
	}
	ids := []string{q.Peek()[0].Cmd.OperationID, q.Peek()[1].Cmd.OperationID}
	if ids[0] != "op-1" || ids[1] != "op-2" {
		t.Fatalf("queue contents = %v, want [op-1 op-2] (overflow preserved the queued ops)", ids)
	}
}

// TestOpQueue_DurableAcrossRestart pins that the queue survives an app restart via
// Marshal/Load (R-PHC.4 durable): replay after a reload is byte-identical and in order.
func TestOpQueue_DurableAcrossRestart(t *testing.T) {
	q := NewOpQueue(8)
	_ = q.Enqueue(qop("op-1"))
	_ = q.Enqueue(qop("op-2"))
	blob, err := q.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	restored := NewOpQueue(8)
	if err := restored.Load(blob); err != nil {
		t.Fatalf("load: %v", err)
	}
	d := restored.Drain()
	if len(d) != 2 || d[0].Cmd.OperationID != "op-1" || d[1].Cmd.OperationID != "op-2" {
		t.Fatalf("restored drain = %+v, want op-1, op-2 in order", d)
	}
}
