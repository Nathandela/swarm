package swarmmobile

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
)

func TestRelayV2MessageIDIsExactCiphertextDigest(t *testing.T) {
	ciphertext := []byte("the exact encrypted envelope")
	wantDigest := sha256.Sum256(ciphertext)
	want := base64.RawURLEncoding.EncodeToString(wantDigest[:])
	if got := relayMessageID(ciphertext); got != want {
		t.Fatalf("relayMessageID = %q, want %q", got, want)
	}
	if len(want) != 43 {
		t.Fatalf("digest id length = %d, want 43", len(want))
	}
}

func TestPairingCommitAndStopSerializeStartedIntent(t *testing.T) {
	a := freshnessApp(t)
	a.coalesce = phonecore.NewInputCoalescer(time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	old := &session{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go func() {
		<-ctx.Done()
		close(old.done)
	}()
	a.mu.Lock()
	a.sess = old
	a.mu.Unlock()

	committed := make(chan struct{})
	release := make(chan struct{})
	pinDone := make(chan error, 1)
	go func() {
		pinDone <- a.pinWithStagedPushBinding(pairedOutcome("lifecycle", 1), nil, func() {
			close(committed)
			<-release
		})
	}()
	<-committed
	stopDone := make(chan error, 1)
	go func() { stopDone <- a.Stop() }()
	select {
	case err := <-stopDone:
		t.Fatalf("Stop crossed the pairing lifecycle transaction: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-pinDone; err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := <-stopDone; err != nil {
		t.Fatalf("Stop: %v", err)
	}
	a.mu.Lock()
	live := a.sess
	a.mu.Unlock()
	if live != nil {
		t.Fatal("Stop lost to pairing's restart and left a session running")
	}
}

func TestPairingCommitRefusesAfterCloseBegins(t *testing.T) {
	a := freshnessApp(t)
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	before := a.core.State()
	err := a.pin(pairedOutcome("closed", 1))
	if !errors.Is(err, errClosed) {
		t.Fatalf("pin after close = %v, want errClosed", err)
	}
	after := a.core.State()
	if string(after.MachineRelayAuthPub) != string(before.MachineRelayAuthPub) || after.EpochID != before.EpochID {
		t.Fatal("pin mutated durable authority after close began")
	}
}

func TestCloseWaitsForNativePairingLifecycleTransaction(t *testing.T) {
	a := freshnessApp(t)
	a.coalesce = phonecore.NewInputCoalescer(time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	old := &session{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go func() {
		<-ctx.Done()
		close(old.done)
	}()
	a.mu.Lock()
	a.sess = old
	a.mu.Unlock()

	committed := make(chan struct{})
	release := make(chan struct{})
	pinDone := make(chan error, 1)
	go func() {
		pinDone <- a.pinWithStagedPushBinding(pairedOutcome("close-lifecycle", 2), nil, func() {
			close(committed)
			<-release
		})
	}()
	<-committed
	closeDone := make(chan error, 1)
	go func() { closeDone <- a.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close crossed pairing's durable lifecycle transaction: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-pinDone; err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	a.mu.Lock()
	closed, sess, stream := a.closed, a.sess, a.stream
	a.mu.Unlock()
	if !closed || sess != nil || stream != nil {
		t.Fatalf("closed lifecycle = closed:%t sess:%p stream:%p", closed, sess, stream)
	}
}
