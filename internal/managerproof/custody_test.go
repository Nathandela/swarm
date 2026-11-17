package managerproof

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCustodyReaderEnqueueDoesNotWaitForWriter(t *testing.T) {
	var failed atomic.Bool
	c := NewCustody(t.Context(), func(error) { failed.Store(true) }, nil)
	defer c.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	if err := c.Enqueue(1, func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}); err != nil {
		t.Fatal(err)
	}
	<-entered
	start := time.Now()
	if err := c.Enqueue(1, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 25*time.Millisecond {
		t.Fatal("native reader waited for custody I/O")
	}
	if !c.Pending() {
		t.Fatal("pending custody was hidden")
	}
	close(release)
	if err := c.Do(t.Context(), 1, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if failed.Load() {
		t.Fatal("healthy custody quarantined")
	}
}
func TestCustodyOverflowAndWriteFailureCannotRenew(t *testing.T) {
	for _, mode := range []string{"overflow", "write"} {
		t.Run(mode, func(t *testing.T) {
			var failure atomic.Bool
			c := NewCustody(t.Context(), func(error) { failure.Store(true) }, nil)
			defer c.Close()
			if mode == "overflow" {
				if err := c.Enqueue(257<<10, func(context.Context) error { return nil }); err == nil {
					t.Fatal("oversize custody accepted")
				}
			} else {
				if err := c.Enqueue(1, func(context.Context) error { return errors.New("synthetic sync failure") }); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(time.Second)
			for !failure.Load() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !failure.Load() || !errors.Is(c.Err(), ErrQuarantined) {
				t.Fatal("custody failure was not latched")
			}
			if err := c.Do(t.Context(), 1, func(context.Context) error { t.Fatal("failed owner resumed"); return nil }); !errors.Is(err, ErrQuarantined) {
				t.Fatal(err)
			}
		})
	}
}

func TestCustodyNeverReopensProofGateDuringFailure(t *testing.T) {
	published := make(chan bool, 4)
	var c *Custody
	c = NewCustody(t.Context(), nil, func() { published <- c.Pending() })
	defer c.Close()
	if err := c.Enqueue(1, func(context.Context) error { return errors.New("synthetic failure") }); err != nil {
		t.Fatal(err)
	}
	// Enqueue, quarantine, and drain notifications must all keep the gate closed.
	for range 3 {
		select {
		case gateClosed := <-published:
			if !gateClosed {
				t.Fatal("failed custody briefly exposed proof")
			}
		case <-time.After(time.Second):
			t.Fatal("missing custody notification")
		}
	}
}
