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
)

// errRelayRefusedTokenDelete stands for every refusal handleTokenDelete can seal.
var errRelayRefusedTokenDelete = errors.New("relay: token delete refused (quota exceeded)")

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
	var order []string

	if _, err := issueRevokeThenDropTokenAtRelay(
		func() (*Op, error) { order = append(order, "revoke"); return &Op{}, nil },
		func() error { order = append(order, "relay"); return nil },
	); err != nil {
		t.Fatalf("a clean run refused the revoke: %v", err)
	}

	if len(order) != 2 || order[0] != "revoke" || order[1] != "relay" {
		t.Fatalf("agents-tracker-2x4e: the halves ran %v. The signed revoke is the security-critical "+
			"one and goes first; anything before it is something that can stop it", order)
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
	told := false

	op, err := issueRevokeThenDropTokenAtRelay(
		func() (*Op, error) { return nil, refused },
		func() error { told = true; return nil },
	)

	if !told {
		t.Error("agents-tracker-2x4e: a revoke that failed left the relay holding the token, with " +
			"nothing on this handset that still remembers it -- the local state was cleared before " +
			"the command was attempted")
	}
	if !errors.Is(err, refused) {
		t.Errorf("agents-tracker-2x4e: the caller was told %v rather than why the revoke failed", err)
	}
	if op != nil {
		t.Errorf("agents-tracker-2x4e: a revoke that failed handed back an operation (%v) for a "+
			"screen to wait on an answer to", op)
	}
}
