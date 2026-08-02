package pairing

// PB-PAIR-4 -- THE ACK LEG IS BOUNDED BY THE RELAY'S RENDEZVOUS DEADLINE, NOT BY acceptAckWindow.
//
// Machine.Pair detaches the wait for the acknowledgement (context.WithoutCancel + a 2 s
// acceptAckWindow) precisely so the pairing deadline cannot cancel the machine's chance to LEARN
// that the phone pinned. That detachment is correct and is NOT what this file questions.
//
// What it questions is that the detachment does not reach the relay. The park that carries the
// acknowledgement is a rendezvous_recv, and the relay cuts EVERY such park at the connection's
// rendezvous deadline -- one RendezvousTTL from the moment the machine joined
// (internal/remote/relay/server.go handleRendezvousRecv, charged against sc.rdvDeadline). The
// machine joined before the QR was drawn, and RendezvousTTL and the announced pairing window are
// the SAME 60 s (relay.DefaultConfig().RendezvousTTL, and internal/skeleton/pairing.go's
// pairWindow clamps to it). So the acknowledgement's real window is
//
//	min(acceptAckWindow, the pairing window MINUS however long the human took)
//
// and the second term goes to roughly zero for the operator who compares six emoji carefully.
// Measured against the real relay in round 6: `rendezvous expired` returned while the ack was
// still in flight. The outcome is the harmless orientation -- phone pinned, machine claiming
// nothing -- but the punishment lands on the careful operator, which is backwards, and the
// recovery is a re-pair whose cause the owner is never told (finding #8, fenced separately).
//
// THE PROPERTY. The frames that follow the human's decision are machine-speed and must not be
// charged against the human's think-time. So the SAS gate must give up early enough that the
// acceptance and its acknowledgement still fit inside the window the relay is timing.
//
// WHY THE ASSERTION IS ON THE CONFIRM CONTEXT'S DEADLINE. A ceremony test that made a human
// "take too long" would have to race the wall clock against acceptAckWindow, and would pass or
// fail on machine load rather than on the property -- this suite already has one load-sensitive
// test and does not need a second. The reserve is a STRUCTURAL property of the budget: whatever
// the human does, the gate they answer must expire with at least acceptAckWindow left. That is
// checkable exactly, with no sleeps.
//
// pbpair4_agreement_test.go records that "injecting a deadline into Pair is what made
// b52_consent_release_test.go:223 vacuous". This test injects one DELIBERATELY, because the
// deadline IS the subject, and it fails closed against that hazard: if Pair never reaches the
// confirm gate, or reaches it with no deadline at all, the test fatals rather than passing.

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestPBPAIR4_TheAckLegKeepsItsWindowHoweverLongTheHumanTook asserts the reserve.
func TestPBPAIR4_TheAckLegKeepsItsWindowHoweverLongTheHumanTook(t *testing.T) {
	// Wide enough to be an ordinary ceremony budget rather than a degenerate one: the reserve
	// must come out of a REAL window, not be an artifact of a window too small to hold it.
	const window = 30 * time.Second

	mID, dID, secret, rid := pbPair4Identities(t, 0x9C)

	var (
		mu              sync.Mutex
		confirmDeadline time.Time
		sawDeadline     bool
		confirmCalled   bool
	)
	// The careful operator: they reach the gate, and they answer it. What the gate is asked
	// here is not how long they took but how long they were ALLOWED.
	confirm := ConfirmFunc(func(ctx context.Context, sas [6]string, name string) (bool, error) {
		mu.Lock()
		confirmCalled = true
		confirmDeadline, sawDeadline = ctx.Deadline()
		mu.Unlock()
		return false, ErrConfirmTimeout
	})

	mp := newMachineParams(mID, secret, rid, confirm)
	dp := newDeviceParams(dID, secret, rid)
	mEnd, dEnd := newRendezvousPipe()

	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()
	pairDeadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("the test's own context carries no deadline; the fixture is broken")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = NewMachine(mp).Pair(ctx, mEnd) }()
	go func() { defer wg.Done(); _, _ = RunDevice(ctx, dp, dEnd) }()
	wg.Wait()

	mu.Lock()
	called, seen, gate := confirmCalled, sawDeadline, confirmDeadline
	mu.Unlock()

	// Anti-vacuity: both of these would make the assertion below trivially true.
	if !called {
		t.Fatal("the ceremony never reached the SAS gate, so this test measured nothing")
	}
	if !seen {
		t.Fatal("the SAS gate was handed a context with NO deadline, so the human may run until the " +
			"pairing window closes and the acceptance leg inherits whatever is left -- which is the defect")
	}

	if slack := pairDeadline.Sub(gate); slack < acceptAckWindow {
		t.Errorf("the SAS gate may run until %v before the pairing deadline, leaving %v for the acceptance "+
			"and its acknowledgement; acceptAckWindow is %v. The relay cuts the ack park at the rendezvous "+
			"deadline (handleRendezvousRecv, charged against rdvDeadline, one RendezvousTTL from the machine's "+
			"join -- the same 60 s as the announced window), so an operator who used the window leaves the phone "+
			"pinned and this machine claiming nothing",
			slack, slack, acceptAckWindow)
	}
}
