package appserver

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestCallAtWriteBoundary_SkipsBoundaryAfterQueuedClose(t *testing.T) {
	c := &Client{done: make(chan struct{}), waiters: make(map[string]chan rpcReply)}
	c.writeMu.Lock()

	callbackErr := errors.New("beforeWrite ran")
	beforeCalled := false
	afterCalled := false
	callDone := make(chan error, 1)
	go func() {
		callDone <- c.CallAtWriteBoundary(context.Background(), "turn/start", map[string]any{}, nil,
			func() error {
				beforeCalled = true
				return callbackErr
			},
			func() { afterCalled = true })
	}()

	deadline := time.Now().Add(5 * time.Second)
	stack := make([]byte, 64<<10)
	for {
		c.mu.Lock()
		queued := len(c.waiters) == 1
		c.mu.Unlock()
		n := runtime.Stack(stack, true)
		blockedOnWriter := blockedBoundaryCall(stack[:n])
		if queued && blockedOnWriter {
			break
		}
		if time.Now().After(deadline) {
			c.writeMu.Unlock()
			t.Fatal("CallAtWriteBoundary did not queue behind writeMu")
		}
		runtime.Gosched()
	}

	// Model the read loop observing EOF while this request is already queued.
	c.finish(ErrClosed)
	c.writeMu.Unlock()

	select {
	case err := <-callDone:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("CallAtWriteBoundary error = %v, want ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CallAtWriteBoundary did not return after close")
	}
	if beforeCalled {
		t.Fatal("beforeWrite ran after finish marked the queued client's connection closed")
	}
	if afterCalled {
		t.Fatal("afterWrite ran without a write attempt")
	}
}

func blockedBoundaryCall(stack []byte) bool {
	for len(stack) > 0 {
		end := bytes.Index(stack, []byte("\n\n"))
		block := stack
		if end >= 0 {
			block = stack[:end]
		}
		if bytes.Contains(block, []byte("(*Client).callAtWriteBoundary")) &&
			bytes.Contains(block, []byte("[sync.Mutex.Lock]")) &&
			bytes.Contains(block, []byte("sync.(*Mutex).Lock")) {
			return true
		}
		if end < 0 {
			return false
		}
		stack = stack[end+2:]
	}
	return false
}

func TestCallAtWriteBoundary_SkipsBoundaryForCancelledContext(t *testing.T) {
	c := &Client{done: make(chan struct{}), waiters: make(map[string]chan rpcReply)}
	ctx, cancel := context.WithCancel(context.Background())

	callbackErr := errors.New("beforeWrite ran")
	beforeCalled := false
	afterCalled := false
	c.writeMu.Lock()
	callDone := make(chan error, 1)
	go func() {
		callDone <- c.CallAtWriteBoundary(ctx, "turn/start", map[string]any{}, nil,
			func() error {
				beforeCalled = true
				return callbackErr
			},
			func() { afterCalled = true })
	}()

	deadline := time.Now().Add(5 * time.Second)
	stack := make([]byte, 64<<10)
	for {
		c.mu.Lock()
		queued := len(c.waiters) == 1
		c.mu.Unlock()
		n := runtime.Stack(stack, true)
		if queued && blockedBoundaryCall(stack[:n]) {
			break
		}
		if time.Now().After(deadline) {
			c.writeMu.Unlock()
			cancel()
			t.Fatal("CallAtWriteBoundary did not queue behind writeMu")
		}
		runtime.Gosched()
	}
	cancel()
	c.writeMu.Unlock()

	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CallAtWriteBoundary error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CallAtWriteBoundary did not return after cancellation")
	}
	if beforeCalled || afterCalled {
		t.Fatalf("callbacks ran for cancelled context: before=%v after=%v", beforeCalled, afterCalled)
	}
	c.mu.Lock()
	waiters := len(c.waiters)
	c.mu.Unlock()
	if waiters != 0 {
		t.Fatalf("cancelled call left %d reply waiters", waiters)
	}
}
