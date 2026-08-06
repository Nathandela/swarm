package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-2x4e: "the panic command is gated by a
// cleanup that cannot answer for it".
//
// WHAT HAPPENS. RevokeThisDevice's first act is dropPushToken, whose last act is
// `cl.TokenDelete(ctx)` -- and it RETURNS that error. The relay refuses a TokenDelete on three
// paths that have nothing to do with the revoke: `meterOp` (the per-connection quota),
// `requireAuth`, and a `deleteToken` persistence failure (internal/remote/relay/server.go's
// handleTokenDelete). Any of them ends RevokeThisDevice before a single byte of the signed
// revoke is authored, and the owner pressing the phone's only panic control is told the relay's
// answer to a housekeeping call.
//
// AND THE HOP IS THE REDUNDANT HALF. The machine-side revoke drops the token inside
// revokeAndPurge's own transaction (S12), so the relay learns of the deletion from the very
// command this gate is stopping. What is NOT redundant is the local durable clear -- a phone
// that keeps the token in durable state re-registers it on the next connection (PB-PUSH-9) --
// and that half writes to this handset's own disk, needs no relay, and stays first.
//
// WHY THE ORDER IS A FUNCTION AND NOT A TEST OF THE VERB. Both halves are unreachable from a
// unit test: sealSignedCommand resolves through `sendContext`, whose awaitConn polls for a live
// relay connection, and the cleanup's failure arrives from a relay client this package holds as
// a concrete type. What can be stated, and is the whole of the decision, is the ORDER and what
// each half is allowed to decide -- so that is what the code names and this exercises.

import (
	"errors"
	"testing"
	"time"
)

// errRelayRefusedTokenDelete stands for every refusal handleTokenDelete can seal.
var errRelayRefusedTokenDelete = errors.New("relay: token delete refused (quota exceeded)")

// hopWait is how long a test waits for the relay hop, which no longer runs on the caller's
// goroutine (agents-tracker-j4pi). It is generous because it bounds a scheduling delay and
// nothing else: the assertions it guards are about whether the hop happens AT ALL.
const hopWait = 5 * time.Second

// awaitHop returns the next thing the hop recorded, or fails the test.
//
// IT IS A WAIT AND NOT AN ASSUMPTION, which is what changed when the hop stopped blocking the
// verb. What it must not become is an assertion that quietly passes when nothing arrives -- the
// deletion the phone owes would then be owed to nobody, with the test agreeing.
func awaitHop(t *testing.T, ran <-chan string) string {
	t.Helper()
	select {
	case what := <-ran:
		return what
	case <-time.After(hopWait):
		t.Fatal("agents-tracker-2x4e: the relay was never told. PB-PUSH-9's deletion on revoke is " +
			"owed by durable state and carried on the next authenticated reconnect, but the hop " +
			"that would spare a revoked handset that wait did not run at all")
		return ""
	}
}

func TestRevokeOrder_ARelayRefusalDoesNotSuppressTheSignedRevoke(t *testing.T) {
	issued := &Op{Action: "device_revoke", OperationID: "op-2x4e"}

	op, err := issueRevokeThenDropTokenAtRelay(
		func() (*Op, error) { return issued, nil },
		func() error { return errRelayRefusedTokenDelete },
	)

	if err != nil {
		t.Fatalf("agents-tracker-2x4e: a refused push-token cleanup failed the revoke with %v. "+
			"The relay's answer to a housekeeping call is not the machine's answer to the owner's "+
			"panic action, and the machine-side revoke deletes that token itself", err)
	}
	if op != issued {
		t.Fatalf("agents-tracker-2x4e: the revoke's own operation did not reach the caller (%v), "+
			"so the screen has no id to claim the machine's verdict by (PB-SYNC-2)", op)
	}
}

func TestRevokeOrder_TheRevokeIsIssuedBeforeTheRelayIsTold(t *testing.T) {
	// A channel rather than a slice, because the hop no longer runs on this goroutine: the order
	// is still the subject, and appending to a slice from two goroutines would race the test
	// rather than test the code.
	ran := make(chan string, 2)

	if _, err := issueRevokeThenDropTokenAtRelay(
		func() (*Op, error) { ran <- "revoke"; return &Op{}, nil },
		func() error { ran <- "relay"; return nil },
	); err != nil {
		t.Fatalf("a clean run refused the revoke: %v", err)
	}

	if first := awaitHop(t, ran); first != "revoke" {
		t.Fatalf("agents-tracker-2x4e: %q ran first. The signed revoke is the security-critical "+
			"half and goes before anything that could stop it", first)
	}
	if second := awaitHop(t, ran); second != "relay" {
		t.Fatalf("agents-tracker-2x4e: %q ran second, so the two halves are not the two this "+
			"function is named for", second)
	}
}

// TestRevokeOrder_TheVerbDoesNotWaitOnTheRelayHop is agents-tracker-j4pi.
//
// THE HOP IS BOUNDED AND THE BOUND IS THE PROBLEM. relay.DefaultCallTimeout is ten seconds, and
// the phone's Kotlin side destroys both key tiers in the `finally` that runs when this verb
// RETURNS -- so a hop this function waited on put a ten-second network delay in front of the
// local destruction of key material. Android kills a backgrounded app freely and the user has
// just confirmed a destructive dialog, so the window is one the product hands out: process death
// inside it leaves the machine revoked and both key tiers still on the handset, which is exactly
// the state ADR-007 B133 makes revoke-from-the-computer the mitigation for.
//
// The hop's own error was already going nowhere -- it decides nothing, by 2x4e -- so waiting for
// it bought the caller no information at any price, let alone this one.
func TestRevokeOrder_TheVerbDoesNotWaitOnTheRelayHop(t *testing.T) {
	// The hop hangs for as long as this test wants it to, which is what a relay behind a dead
	// network does for DefaultCallTimeout.
	release := make(chan struct{})
	defer close(release)

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		_, _ = issueRevokeThenDropTokenAtRelay(
			func() (*Op, error) { return &Op{Action: "device_revoke"}, nil },
			func() error { <-release; return nil },
		)
	}()

	select {
	case <-returned:
	case <-time.After(hopWait):
		t.Fatal("agents-tracker-j4pi: the revoke has not returned while the relay hop is still in " +
			"flight, so the phone's own purge is queued behind a network round trip -- up to " +
			"relay.DefaultCallTimeout of it. A process death in that window leaves the machine " +
			"revoked with both key tiers still on the handset")
	}
}

// TestRevokeOrder_TheRelayIsToldEvenWhenTheRevokeItselfFailed.
//
// THE DELETION IS OWED IN BOTH DIRECTIONS. A revoke that never reached the machine leaves a
// handset whose owner has already confirmed the destructive dialog -- SettingsSurface purges
// both key tiers in a `finally` for exactly that reason -- so the token must go whether or not
// the command did. And the error that comes back is the REVOKE'S, because that is the one the
// screen has to report.
func TestRevokeOrder_TheRelayIsToldEvenWhenTheRevokeItselfFailed(t *testing.T) {
	refused := errors.New("swarmmobile: offline")
	ran := make(chan string, 1)

	op, err := issueRevokeThenDropTokenAtRelay(
		func() (*Op, error) { return nil, refused },
		func() error { ran <- "relay"; return nil },
	)

	// Waited for rather than read, since agents-tracker-j4pi took the hop off this goroutine. The
	// property is unchanged and so is its strength: the hop must RUN.
	awaitHop(t, ran)
	if !errors.Is(err, refused) {
		t.Errorf("agents-tracker-2x4e: the caller was told %v rather than why the revoke failed", err)
	}
	if op != nil {
		t.Errorf("agents-tracker-2x4e: a revoke that failed handed back an operation (%v) for a "+
			"screen to wait on an answer to", op)
	}
}
