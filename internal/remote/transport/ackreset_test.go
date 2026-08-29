package transport

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAckBatcher_ResetDropsThePriorMailboxCoordinate(t *testing.T) {
	var mu sync.Mutex
	var got []uint64
	a := NewAckBatcher(func(_ context.Context, cursor uint64) error {
		mu.Lock()
		got = append(got, cursor)
		mu.Unlock()
		return nil
	})
	a.Record(53)
	a.Reset()
	a.Record(1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.Run(ctx) }()
	time.Sleep(2 * time.Second / MaxDrainAcksPerSec)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("acks after reset = %v, want only the new mailbox cursor 1", got)
	}
}

func TestAckBatcher_ResetDoesNotResurrectAnOldFailedAck(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var got []uint64
	a := NewAckBatcher(func(_ context.Context, cursor uint64) error {
		mu.Lock()
		got = append(got, cursor)
		mu.Unlock()
		if cursor == 53 {
			once.Do(func() { close(started) })
			<-release
			return errors.New("old store rejected stale ack")
		}
		return nil
	})
	a.Record(53)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old ack never started")
	}
	resetDone := make(chan struct{})
	go func() { a.Reset(); close(resetDone) }()
	close(release)
	select {
	case <-resetDone:
	case <-time.After(time.Second):
		t.Fatal("Reset did not finish after the old ack returned")
	}
	a.Record(1)
	time.Sleep(2 * time.Second / MaxDrainAcksPerSec)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	for i, cursor := range got {
		if i > 0 && cursor == 53 {
			t.Fatalf("failed old ack was resurrected after reset: %v", got)
		}
	}
	if got[len(got)-1] != 1 {
		t.Fatalf("last ack after reset = %v, want new mailbox cursor 1", got)
	}
}

func TestAckBatcher_ResetWaitsForOldGenerationAckBeforeCallerSwitchesIncarnation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	a := NewAckBatcher(func(_ context.Context, _ uint64) error {
		close(started)
		<-release
		return nil
	})
	a.Record(53)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old-generation ack did not start")
	}

	resetDone := make(chan struct{})
	go func() { a.Reset(); close(resetDone) }()
	select {
	case <-resetDone:
		t.Fatal("Reset returned while an old-generation ack was still in flight; caller could now switch incarnation under it")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	select {
	case <-resetDone:
	case <-time.After(time.Second):
		t.Fatal("Reset did not cross the generation barrier after old ack completed")
	}
}

func TestAckBatcher_ResetRejectsAnOldDeliveryRecordedAfterTheReset(t *testing.T) {
	var mu sync.Mutex
	var got []uint64
	a := NewAckBatcher(func(_ context.Context, cursor uint64) error {
		mu.Lock()
		got = append(got, cursor)
		mu.Unlock()
		return nil
	})
	oldGeneration := a.Generation()
	a.Reset()
	if a.RecordGeneration(53, oldGeneration) {
		t.Fatal("late old-generation cursor was accepted after Reset")
	}
	if !a.RecordGeneration(1, a.Generation()) {
		t.Fatal("current-generation cursor was refused")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.Run(ctx) }()
	time.Sleep(2 * time.Second / MaxDrainAcksPerSec)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("acks after late old delivery = %v, want only replacement cursor 1", got)
	}
}
