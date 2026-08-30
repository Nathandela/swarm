package appserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/appserver"
)

// TestCallAtWriteBoundary_HoldsTheRequestAndReleasesBeforeReply pins the production seam
// used by the composer/Stop coordinator. beforeWrite must run before any request bytes cross
// the socket, while afterWrite must run after that write but before Call waits for the reply.
func TestCallAtWriteBoundary_HoldsTheRequestAndReleasesBeforeReply(t *testing.T) {
	f := newFakeServer(t)
	requestSeen := make(chan struct{})
	releaseReply := make(chan struct{})
	f.setHandler(func(c *fakeConn, fr rpcFrame) {
		if fr.Method != "turn/start" {
			return
		}
		close(requestSeen)
		<-releaseReply
		c.send(t, map[string]any{
			"id":     json.RawMessage(fr.ID),
			"result": map[string]any{"ok": true},
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := appserver.Dial(ctx, f.sock, appserver.Options{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	boundaryEntered := make(chan struct{})
	releaseWrite := make(chan struct{})
	afterWrite := make(chan struct{})
	callDone := make(chan error, 1)
	go func() {
		callDone <- c.CallAtWriteBoundary(ctx, "turn/start", map[string]any{}, nil,
			func() error {
				close(boundaryEntered)
				<-releaseWrite
				return nil
			},
			func() { close(afterWrite) })
	}()

	select {
	case <-boundaryEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("beforeWrite was not reached")
	}
	select {
	case <-requestSeen:
		t.Fatal("server received request while beforeWrite still held the boundary")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseWrite)
	select {
	case <-requestSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive request after beforeWrite returned")
	}
	select {
	case <-afterWrite:
	case <-time.After(5 * time.Second):
		t.Fatal("afterWrite did not run while the server reply was held")
	}
	select {
	case err := <-callDone:
		t.Fatalf("Call returned before the server reply was released: %v", err)
	default:
	}
	close(releaseReply)
	select {
	case err := <-callDone:
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Call did not return after reply")
	}
}

// TestCallAtWriteBoundary_BeforeWriteRefusalWritesNothing proves the callback is a true
// pre-write gate, not merely a notification adjacent to the write.
func TestCallAtWriteBoundary_BeforeWriteRefusalWritesNothing(t *testing.T) {
	f := newFakeServer(t)
	requestSeen := make(chan struct{}, 1)
	f.setHandler(func(*fakeConn, rpcFrame) { requestSeen <- struct{}{} })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := appserver.Dial(ctx, f.sock, appserver.Options{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	want := errors.New("Stop already published")
	afterCalled := false
	err = c.CallAtWriteBoundary(ctx, "turn/start", map[string]any{}, nil,
		func() error { return want },
		func() { afterCalled = true })
	if !errors.Is(err, want) {
		t.Fatalf("Call error = %v, want %v", err, want)
	}
	if afterCalled {
		t.Fatal("afterWrite ran even though beforeWrite refused the write")
	}
	select {
	case <-requestSeen:
		t.Fatal("server received request after beforeWrite refused it")
	case <-time.After(100 * time.Millisecond):
	}
}
