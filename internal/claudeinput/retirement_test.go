package claudeinput

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/managerproof"
)

func TestManagerBrokerTerminalTurnsReleaseLiveSlots(t *testing.T) {
	dir := shortDir(t)
	id, err := managerproof.InitializeReplay(dir, "session", "conversation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = InstallManager(dir, "conversation", id); err != nil {
		t.Fatal(err)
	}
	b, err := StartManager(dir, "session", "2.1.319", 123, 456, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	c := NewClient(b.Descriptor())
	ctx := t.Context()
	worker := Worker{NativeSessionID: "native", Generation: "sender", Version: "2.1.319"}
	guard := worker
	guard.Generation = "guard"
	if err = c.Guard(ctx, guard, false); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exchange(ctx, worker, "", "", ""); err != nil {
		t.Fatal(err)
	}
	binding := managerproof.Binding{Session: "session", SessionInstance: "instance", ConversationID: "conversation", Provider: "claude", NativeSessionID: "native", NativeGeneration: "physical", ObservationGeneration: b.Descriptor().Generation + ":" + guard.Generation, LoginEpoch: "account"}
	if err = c.BindManager(ctx, worker, binding); err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		if err = c.Guard(ctx, guard, false); err != nil {
			t.Fatal(err)
		}
		nonce := fmt.Sprintf("%064x", i+1)
		op, turn := fmt.Sprint("op-", i), fmt.Sprint("turn-", i)
		if _, err = c.SubmitManager(ctx, worker, op, "neutral human text", managerproof.Admission{Binding: binding, Nonce: nonce}); err != nil {
			t.Fatalf("input%d: %v", i, err)
		}
		message, err := c.Exchange(ctx, worker, "", "", "")
		if err != nil || message == nil {
			t.Fatal("missing queued input", err)
		}
		if err = c.call(ctx, "/manager-prompt", request{Worker: guard, Text: message.Text}, nil); err != nil {
			t.Fatal(err)
		}
		projection := managerProjection(b.Descriptor().SenderName, message.Text)
		messageID := fmt.Sprint("message-", i)
		if err = c.call(ctx, "/manager-append", request{Worker: guard, NativeInputID: nonce, MessageID: messageID, Projection: projection}, nil); err != nil {
			t.Fatal(err)
		}
		native := Turn{TurnID: turn, State: "running", NativeInputID: nonce, NativeMessageID: messageID}
		if err = c.call(ctx, "/turn", request{Worker: guard, Turn: &native, Projection: projection}, nil); err != nil {
			t.Fatal(err)
		}
		for j := range 3 {
			call := managerTool{CallID: fmt.Sprint("call-", i, "-", j), TurnID: turn, ToolName: "publish"}
			if err = c.call(ctx, "/manager-tool-step", request{Worker: guard, Call: &call}, nil); err != nil {
				t.Fatal(err)
			}
			call.Arguments = []byte(`{"text":"neutral phrase"}`)
			if err = c.call(ctx, "/manager-tool", request{Worker: guard, Call: &call}, nil); err != nil {
				t.Fatal(err)
			}
			hash, _ := managerproof.ArgumentsHash(call.Arguments)
			if _, err = c.ResolveManagerCall(ctx, worker, managerproof.CallRequest{Binding: binding, ToolName: "publish", ArgumentsHash: hash, HostMeta: []byte(fmt.Sprintf(`{"claudecode/toolUseId":%q}`, call.CallID))}); err != nil {
				t.Fatal(err)
			}
			if err = c.call(ctx, "/manager-tool-complete", request{Worker: guard, Call: &call}, nil); err != nil {
				t.Fatal(err)
			}
		}
		native.State, native.Reason = "completed", "answer"
		if err = c.call(ctx, "/turn", request{Worker: guard, Turn: &native}, nil); err != nil {
			t.Fatal(err)
		}
		if err = b.custody.Do(ctx, 0, func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
		outcome, err := c.ManagerInputStatus(ctx, worker, binding, nonce)
		if err != nil || outcome.State != "completed" {
			t.Fatal("compact outcome missing", err)
		}
		b.mu.Lock()
		live := len(b.operations) + len(b.managerInputs)
		b.mu.Unlock()
		if live != 0 {
			t.Fatal("terminal broker detail retained", live)
		}
	}
	old, err := c.Operation(ctx, worker, "op-0")
	if err != nil || old.Text != "" || old.NativeTurnID != "turn-0" {
		t.Fatal("retired receipt lost or retained text", err)
	}
}

func TestManagerBrokerRejectedInputReleasesWithoutInventingTurn(t *testing.T) {
	dir := shortDir(t)
	id, err := managerproof.InitializeReplay(dir, "session", "conversation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = InstallManager(dir, "conversation", id); err != nil {
		t.Fatal(err)
	}
	b, err := StartManager(dir, "session", "2.1.319", 123, 456, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	c := NewClient(b.Descriptor())
	ctx := t.Context()
	worker := Worker{NativeSessionID: "native", Generation: "sender", Version: "2.1.319"}
	guard := worker
	guard.Generation = "guard"
	_ = c.Guard(ctx, guard, false)
	_, _ = c.Exchange(ctx, worker, "", "", "")
	binding := managerproof.Binding{Session: "session", SessionInstance: "instance", ConversationID: "conversation", Provider: "claude", NativeSessionID: "native", NativeGeneration: "physical", ObservationGeneration: b.Descriptor().Generation + ":" + guard.Generation, LoginEpoch: "account"}
	if err = c.BindManager(ctx, worker, binding); err != nil {
		t.Fatal(err)
	}
	for i := range 140 {
		_ = c.Guard(ctx, guard, false)
		_, _ = c.Exchange(ctx, worker, "", "", "")
		nonce, op := fmt.Sprintf("%064x", i+1), fmt.Sprint("reject-", i)
		if _, err = c.SubmitManager(ctx, worker, op, "neutral", managerproof.Admission{Binding: binding, Nonce: nonce}); err != nil {
			t.Fatal(err)
		}
		if _, err = c.Exchange(ctx, worker, "", "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err = c.Exchange(ctx, worker, op, "rejected", "synthetic native drop"); err != nil {
			t.Fatal(err)
		}
		if err = b.custody.Do(ctx, 0, func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
		outcome, err := c.ManagerInputStatus(ctx, worker, binding, nonce)
		if err != nil || outcome.State != "rejected" || outcome.NativeTurnID != "" {
			t.Fatal("native rejection lost or invented turn", outcome, err)
		}
	}
	old, err := c.Operation(ctx, worker, "reject-0")
	if err != nil || old.State != "rejected" || old.NativeTurnID != "" {
		t.Fatal(old, err)
	}
}

func TestManagerStartRequiresMatchingSetup(t *testing.T) {
	dir := shortDir(t)
	id, err := managerproof.InitializeReplay(dir, "session", "conversation")
	if err != nil {
		t.Fatal(err)
	}
	if broker, err := StartManager(dir, "session", "2.1.319", 123, 456, id); !errors.Is(err, managerproof.ErrQuarantined) {
		if broker != nil {
			_ = broker.Close()
		}
		t.Fatalf("missing setup became ordinary broker: %v", err)
	}
	if _, err := InstallManager(dir, "conversation", id); err != nil {
		t.Fatal(err)
	}
	if broker, err := Start(dir, "session", "2.1.319", 123, 456); err == nil {
		_ = broker.Close()
		t.Fatal("ordinary startup adopted manager setup")
	}
}

func TestManagerCustodyCountsFieldsOutsideTextAndProjection(t *testing.T) {
	dir := shortDir(t)
	id, err := managerproof.InitializeReplay(dir, "session", "conversation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallManager(dir, "conversation", id); err != nil {
		t.Fatal(err)
	}
	b, err := StartManager(dir, "session", "2.1.319", 123, 456, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if err := b.custody.Enqueue(0, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	c := NewClient(b.Descriptor())
	for i := range 3 {
		err := c.call(t.Context(), "/turn", request{Reason: strings.Repeat("x", 96<<10)}, nil)
		if (i == 2) != (err != nil) {
			t.Fatalf("queued request %d returned %v", i, err)
		}
	}
}

func TestManagerInterruptRetainsExactTerminalDisposition(t *testing.T) {
	dir := shortDir(t)
	id, err := managerproof.InitializeReplay(dir, "session", "conversation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallManager(dir, "conversation", id); err != nil {
		t.Fatal(err)
	}
	b, err := StartManager(dir, "session", "2.1.319", 123, 456, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	c := NewClient(b.Descriptor())
	ctx := t.Context()
	worker := Worker{NativeSessionID: "native", Generation: "sender", Version: "2.1.319"}
	guard := worker
	guard.Generation = "guard"
	if err := c.Guard(ctx, guard, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exchange(ctx, worker, "", "", ""); err != nil {
		t.Fatal(err)
	}
	binding := managerproof.Binding{Session: "session", SessionInstance: "instance", ConversationID: "conversation", Provider: "claude", NativeSessionID: "native", NativeGeneration: "physical", ObservationGeneration: b.Descriptor().Generation + ":guard", LoginEpoch: "account"}
	if err := c.BindManager(ctx, worker, binding); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.turn = Turn{TurnID: "turn", State: "running"}
	b.mu.Unlock()
	if got, err := c.Interrupt(ctx, worker, "operation", "turn"); err != nil || got.State != "prepared" {
		t.Fatalf("prepare = %+v, %v", got, err)
	}
	b.mu.Lock()
	b.interrupts["operation"] = Interrupt{OperationID: "operation", TurnID: "turn", State: "dispatched"}
	b.mu.Unlock()
	if err := c.call(ctx, "/interrupt-result", request{Worker: guard, OperationID: "operation", TurnID: "turn", State: "accepted"}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Interrupt(ctx, worker, "operation", "turn"); err != nil || got.State != "accepted" {
		t.Fatalf("terminal retry = %+v, %v", got, err)
	}
	b.mu.Lock()
	_, retained := b.interrupts["operation"]
	b.mu.Unlock()
	if retained {
		t.Fatal("terminal interrupt still occupies a live slot")
	}
	if _, err := c.Interrupt(ctx, worker, "operation", "other-turn"); err == nil {
		t.Fatal("mismatched retry succeeded")
	}
}
