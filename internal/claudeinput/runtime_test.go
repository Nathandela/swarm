package claudeinput

import (
	"context"
	"errors"
	"testing"
)

func TestGuardRevalidationRequiresFreshProofAndDiscardsOldTurn(t *testing.T) {
	b, err := Start(shortDir(t), "session", "2.1.332", 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	c := NewClient(b.Descriptor())
	ctx := t.Context()
	sender := Worker{NativeSessionID: "native", Generation: "sender", Version: "2.1.332"}
	guard := sender
	guard.Generation = "guard"
	if err := c.Guard(ctx, guard, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exchange(ctx, sender, "", "", ""); err != nil {
		t.Fatal(err)
	}
	oldTurn := Turn{TurnID: "old", State: "running"}
	if err := c.call(ctx, "/turn", request{Worker: guard, Turn: &oldTurn}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Interrupt(ctx, sender, "old-stop", oldTurn.TurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(ctx, sender, "prior", "existing input"); err != nil {
		t.Fatal(err)
	}
	if msg, err := c.Exchange(ctx, sender, "", "", ""); err != nil || msg == nil {
		t.Fatalf("prior claim: %+v %v", msg, err)
	}
	revalidate := request{Worker: guard, State: "revalidate", Observation: 1}
	var ready bool
	if err := c.call(ctx, "/guard", revalidate, &ready); err != nil || ready {
		t.Fatalf("guard alone recovered input: %v %v", ready, err)
	}
	if _, err := c.Ready(ctx); !errors.Is(err, readinessRecovering) {
		t.Fatalf("readiness during recovery: %v", err)
	}
	if _, err := c.Submit(ctx, sender, "blocked", "must not be delivered"); !errors.Is(err, ErrInputNotSubmitted) {
		t.Fatalf("broker did not explicitly refuse new input: %v", err)
	}
	if _, err := c.Submit(ctx, sender, "prior", "existing input"); err == nil || errors.Is(err, ErrInputNotSubmitted) {
		t.Fatalf("broker declared a prior claim unsubmitted: %v", err)
	}
	if _, err := c.Exchange(ctx, sender, "prior", "started", ""); err != nil {
		t.Fatal(err)
	}
	if receipt, err := c.Operation(ctx, sender, "prior"); err != nil || receipt.State != "started" {
		t.Fatalf("prior receipt lost: %+v %v", receipt, err)
	}
	if err := c.call(ctx, "/guard", revalidate, &ready); err != nil || ready {
		t.Fatalf("receipt was counted as a heartbeat: %v %v", ready, err)
	}
	other := sender
	other.NativeSessionID = "replacement"
	if _, err := c.Exchange(ctx, other, "", "", ""); err == nil {
		t.Fatal("replacement sender renewed old proof")
	}
	if msg, err := c.Exchange(ctx, sender, "", "", ""); err != nil || msg != nil {
		t.Fatalf("sender claimed input before revalidation: %+v %v", msg, err)
	}
	if err := c.call(ctx, "/guard", revalidate, &ready); err != nil || !ready {
		t.Fatalf("matching fresh proofs could not recover: %v %v", ready, err)
	}
	if _, err := c.Ready(ctx); !errors.Is(err, readinessRecovering) {
		t.Fatalf("input opened before the guard confirmed receipt: %v", err)
	}
	if _, err := c.Submit(ctx, sender, "early", "must remain fenced"); err == nil {
		t.Fatal("broker accepted input before guard confirmation")
	}
	if err := c.call(ctx, "/guard", request{Worker: guard, Observation: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if proof, err := c.Ready(ctx); err != nil || proof != sender {
		t.Fatalf("recovered identity changed: %+v %v", proof, err)
	}
	if err := c.call(ctx, "/turn", request{Worker: guard, Turn: &oldTurn}, nil); err == nil {
		t.Fatal("stale observation repopulated the turn")
	}
	if _, err := c.Turn(ctx, sender); err == nil {
		t.Fatal("stale turn became readable after recovery")
	}
	if stop, err := c.Interrupt(ctx, sender, "old-stop", oldTurn.TurnID); err != nil || stop.State != "rejected" {
		t.Fatalf("prepared stale interrupt was not retired: %+v %v", stop, err)
	}
	if _, err := c.Submit(ctx, sender, "new", "new input"); err != nil {
		t.Fatal(err)
	}
}

func TestMainTurnLifecycleAndExactInterrupt(t *testing.T) {
	b, err := Start(shortDir(t), "session", "2.1.319", 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	c := NewClient(b.Descriptor())
	w := Worker{NativeSessionID: "native", Generation: "sender", Version: "2.1.319"}
	g := w
	g.Generation = "guard"
	ctx := context.Background()
	if err := c.Guard(ctx, g, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exchange(ctx, w, "", "", ""); err != nil {
		t.Fatal(err)
	}
	report := func(turn Turn) error { return c.call(ctx, "/turn", request{Worker: g, Turn: &turn}, nil) }
	if err := c.call(ctx, "/activity", request{Worker: g, ExternalInput: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := report(Turn{TurnID: "main-1", State: "running"}); err != nil {
		t.Fatal(err)
	}
	turn, err := c.Turn(ctx, w)
	if err != nil || turn.TurnID != "main-1" || turn.State != "running" || turn.ExternalInput != 1 {
		t.Fatalf("turn: %+v %v", turn, err)
	}
	if _, err := c.Interrupt(ctx, w, "stop", "old"); err == nil {
		t.Fatal("stale turn interrupt accepted")
	}
	stop, err := c.Interrupt(ctx, w, "stop", "main-1")
	if err != nil || stop.State != "prepared" {
		t.Fatalf("stop: %+v %v", stop, err)
	}
	if _, err := c.Interrupt(ctx, w, "stop", "main-2"); err == nil {
		t.Fatal("stop payload conflict accepted")
	}
	var command *Interrupt
	if err := c.call(ctx, "/guard", request{Worker: g}, &command); err != nil || command == nil || command.TurnID != "main-1" {
		t.Fatalf("dispatch: %+v %v", command, err)
	}
	var replay *Interrupt
	if err := c.call(ctx, "/guard", request{Worker: g}, &replay); err != nil || replay != nil {
		t.Fatalf("interrupt was dispatched twice: %+v %v", replay, err)
	}
	if err := report(Turn{TurnID: "main-1", State: "completed", Reason: "aborted"}); err != nil {
		t.Fatal(err)
	}
	if err := report(Turn{TurnID: "main-2", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := report(Turn{TurnID: "main-1", State: "completed", Reason: "answer"}); err == nil {
		t.Fatal("late old completion overwrote current turn")
	}
	turn, err = c.Turn(ctx, w)
	if err != nil || turn.TurnID != "main-2" || turn.State != "running" {
		t.Fatalf("current turn: %+v %v", turn, err)
	}
	w.Generation = "replacement"
	if _, err := c.Turn(ctx, w); err == nil {
		t.Fatal("replacement worker read turn")
	}
	w.Generation = "sender"
	if _, err := c.Interrupt(ctx, w, "not-dispatched", "main-2"); err != nil {
		t.Fatal(err)
	}
	if err := c.Revoke(ctx, w); err != nil {
		t.Fatal(err)
	}
	stop, err = c.Interrupt(ctx, w, "stop", "main-1")
	if err != nil || stop.State != "dispatched" {
		t.Fatalf("retired interrupt custody lost: %+v %v", stop, err)
	}
	stop, err = c.Interrupt(ctx, w, "not-dispatched", "main-2")
	if err != nil || stop.State != "rejected" {
		t.Fatalf("undispatched interrupt was not rejected: %+v %v", stop, err)
	}
	if _, err := c.Interrupt(ctx, w, "after-retirement", "main-2"); err == nil {
		t.Fatal("new interrupt admitted after retirement")
	}
}

func TestOlderInputQualificationDoesNotAdvertiseNativeTurns(t *testing.T) {
	b, err := Start(shortDir(t), "older-session", "2.1.296", 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if b.Descriptor().NativeTurns {
		t.Fatal("older admission evidence advertised new turn capability")
	}
	c := NewClient(b.Descriptor())
	w := Worker{NativeSessionID: "native", Generation: "sender", Version: "2.1.296"}
	if _, err := c.Interrupt(context.Background(), w, "stop", "turn"); err == nil {
		t.Fatal("older runtime admitted native interrupt")
	}
}
