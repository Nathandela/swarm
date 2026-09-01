package swarmmobile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

func TestRosterRefreshBudgetReservationCanBeRefundedAfterDiscardFailure(t *testing.T) {
	a := &App{resyncAt: map[string][]time.Time{}}
	reservedAt := time.Unix(1_700_000_000, 0)
	if err := a.resyncBudget(phonecore.StreamJournal, reservedAt); err != nil {
		t.Fatalf("reserve refresh budget: %v", err)
	}
	a.refundResyncBudget(phonecore.StreamJournal, reservedAt)

	// A failed discard authored no replacement command, so an immediate user retry must
	// be admitted instead of inheriting a five-second penalty from the failed recovery.
	if err := a.resyncBudget(phonecore.StreamJournal, reservedAt.Add(time.Nanosecond)); err != nil {
		t.Fatalf("immediate retry after exact refund: %v", err)
	}
}

func TestRosterRefreshBudgetRefundCannotRemoveAnotherConcurrentReservation(t *testing.T) {
	a := &App{resyncAt: map[string][]time.Time{}}
	reservedAt := time.Unix(1_700_000_000, 0)
	if err := a.resyncBudget(phonecore.StreamJournal, reservedAt); err != nil {
		t.Fatalf("reserve refresh budget: %v", err)
	}
	a.refundResyncBudget(phonecore.StreamJournal, reservedAt.Add(time.Second))

	if err := a.resyncBudget(phonecore.StreamJournal, reservedAt.Add(time.Second)); err == nil {
		t.Fatal("mismatched refund removed another refresh reservation")
	}
}

func TestPassiveRosterSyncClaimIsSingleFlightAtFacadeBoundary(t *testing.T) {
	a := &App{}
	if !a.beginPassiveRosterSync() {
		t.Fatal("first passive roster sync claim was refused")
	}
	if a.beginPassiveRosterSync() {
		t.Fatal("overlapping passive roster sync acquired a second facade claim")
	}
	a.endPassiveRosterSync()
	if !a.beginPassiveRosterSync() {
		t.Fatal("completed passive roster sync did not release its facade claim")
	}
}

func TestMailboxDiscardRequestIsCanceledWithItsStartStopSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := &App{
		sess:   &session{ctx: ctx, cancel: cancel, done: make(chan struct{})},
		client: &relay.Client{},
	}
	result := make(chan error, 1)
	go func() {
		_, err := a.requestMailboxDiscard()
		result <- err
	}()

	deadline := time.Now().Add(time.Second)
	for {
		a.mu.Lock()
		installed := a.mailboxDiscard != nil
		a.mu.Unlock()
		if installed {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("mailbox discard request was not installed")
		}
		time.Sleep(time.Millisecond)
	}

	// Stop calls this generation's cancel before joining its drain. The request must leave
	// promptly instead of keeping Stop behind the independent 15-second recovery timeout.
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("session-canceled mailbox discard = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("session cancellation did not release mailbox discard request")
	}
}
