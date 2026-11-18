package claudeinput

import (
	"context"
	"testing"
)

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
