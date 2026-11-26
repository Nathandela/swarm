package skeleton

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/idempotency"
)

// A scheduled opening prompt reaches a native-input Claude worker: the scheduler
// must take the same durable custody an owner `swarm message` takes.
func TestScheduledPromptReachesNativeClaudeInput(t *testing.T) {
	r := newClaudeReceiptRig(t)
	const op = "schedule-prompt:attempt"
	got := make(chan string, 1)
	go func() {
		for {
			msg, err := r.client.Exchange(context.Background(), r.worker, "", "", "")
			if err != nil {
				return
			}
			if msg != nil {
				_, _ = r.client.Exchange(context.Background(), r.worker, msg.OperationID, "submitted", "")
				got <- msg.OperationID
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	began := false
	if err := r.sk.api.sendOwnerPrompt(r.local, op, "scheduled work", func() error { began = true; return nil }); err != nil || !began {
		t.Fatalf("scheduled prompt refused: began=%v err=%v", began, err)
	}
	select {
	case id := <-got:
		if id != op {
			t.Fatalf("native queue got %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled prompt never reached the native queue")
	}
	if rec, _ := r.sk.core.ComposerOperation(op); rec.Phase != idempotency.PhaseCompleted {
		t.Fatalf("scheduled prompt custody left %s", rec.Phase)
	}
}

// A refusal before the durable boundary leaves the operation retryable.
func TestScheduledPromptRefusalBeforeBeginStaysRetryable(t *testing.T) {
	r := newClaudeReceiptRig(t)
	const op = "schedule-prompt:refused"
	refused := func() error { return errors.New("attempt changed") }
	if err := r.sk.api.sendOwnerPrompt(r.local, op, "scheduled work", refused); err == nil {
		t.Fatal("refused begin reported success")
	}
	if rec, _ := r.sk.core.ComposerOperation(op); rec.Phase != idempotency.PhasePrepared {
		t.Fatalf("refused prompt left %s; a retry could never deliver", rec.Phase)
	}
}
