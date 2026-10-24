package skeleton

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func TestExecutionRegistrationRestoresChildCustody(t *testing.T) {
	root := t.TempDir()
	store, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := persist.Meta{ID: "child-custody", AgentType: "claude", ShimPID: 123, ShimStartTime: 456, ConversationID: "conversation", Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}}
	makeDaemon := func() *Daemon {
		return &Daemon{executionStore: store, eng: engine.New(engine.Config{StoreExecution: store.SaveExecution})}
	}
	first := makeDaemon()
	first.registerExecutionSession(m, "token", claude.New().SignalSources(), true)
	for i, event := range []string{"UserPromptSubmit", "SubagentStart", "Stop"} {
		payload := map[string]string{}
		if event == "SubagentStart" {
			payload["agent_id"] = "child"
		}
		if err := first.eng.HandleCallback(engine.Callback{SessionID: m.ID, Token: "token", Sequence: uint64(i + 1), Event: event, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	second := makeDaemon()
	second.registerExecutionSession(m, "token", claude.New().SignalSources(), false)
	got, err := second.eng.ExecutionSnapshot(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Actors["child"].Active || got.MainTurn != status.TurnIdle {
		t.Fatalf("restored evidence lost child: %+v", got)
	}
	if got.Incomplete == "" {
		t.Fatal("daemon downtime must require native reconciliation")
	}
}

func TestExecutionCustodyFailureDoesNotAdvanceHookCursor(t *testing.T) {
	root := t.TempDir()
	fail := true
	e := engine.New(engine.Config{StoreExecution: func(persist.ExecutionRecord) error {
		if fail {
			return syscall.ENOSPC
		}
		return nil
	}})
	id := "custody"
	if err := e.RegisterExecutionSession(id, "token", 1, claude.New().SignalSources(), status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}, engine.ExecutionIdentity{Incarnation: "one", ObservedFromStart: true}, nil); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{eng: e, stateDir: root}
	callback := engine.Callback{SessionID: id, Token: "token", Sequence: 1, Event: "SubagentStart", Payload: map[string]string{"agent_id": "child"}}
	body, err := json.Marshal(callback)
	if err != nil {
		t.Fatal(err)
	}
	hd := NewHookDrainer(d, id, "", filepath.Join(root, "hook.fold"))
	reply := shim.HookDrainResponse{Records: []shim.HookRecord{{Seq: 1, Body: body}}}
	_, _, err = hd.applyLocked(reply, 0)
	var custody *engine.ExecutionCustodyError
	if !errors.As(err, &custody) {
		t.Fatalf("custody failure was discarded: %v", err)
	}
	if hd.cursor() != 0 {
		t.Fatal("failed custody advanced durable ingress")
	}
	// The exact replay is retriable in the engine without incrementing child count.
	fail = false
	if err := e.HandleCallback(callback); err != nil {
		t.Fatal(err)
	}
	record, err := e.ExecutionSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Actors) != 1 || !record.Actors["child"].Active {
		t.Fatalf("retry lost or duplicated child: %+v", record)
	}
}

func TestExecutionDuplicateAfterCrashReleasesPendingReceipt(t *testing.T) {
	store, err := persist.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "seen-before-crash"
	identity := engine.ExecutionIdentity{Incarnation: "native", ObservedFromStart: true}
	initial := status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}
	cb := engine.Callback{SessionID: id, Token: "token", Sequence: 1, Event: "SubagentStart", Payload: map[string]string{"agent_id": "child"}}
	first := engine.New(engine.Config{StoreExecution: store.SaveExecution})
	if err := first.RegisterExecutionSession(id, "token", 1, claude.New().SignalSources(), initial, identity, nil); err != nil {
		t.Fatal(err)
	}
	if err := first.HandleCallback(cb); err != nil {
		t.Fatal(err)
	}
	saved, err := store.LoadExecution(id)
	if err != nil {
		t.Fatal(err)
	}
	second := engine.New(engine.Config{StoreExecution: store.SaveExecution})
	if err := second.RegisterExecutionSession(id, "token", 1, claude.New().SignalSources(), initial, identity, &saved); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{eng: second, stateDir: t.TempDir()}
	if err := d.markHookSeqIngested(id, 1); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cb)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.ingestHookBytes(raw)
	got, err := store.LoadExecution(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.PendingCallbacks) != 0 {
		t.Fatal("durably folded callback kept pending execution receipt")
	}
	if !got.Actors["child"].Active {
		t.Fatal("receipt retirement erased child custody")
	}
}

func TestExecutionGapCustodyFailureKeepsCursor(t *testing.T) {
	e := engine.New(engine.Config{StoreExecution: func(persist.ExecutionRecord) error { return syscall.ENOSPC }})
	initial := status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}
	if err := e.RegisterExecutionSession("gap", "token", 1, claude.New().SignalSources(), initial, engine.ExecutionIdentity{Incarnation: "native", ObservedFromStart: true}, nil); err != nil {
		t.Fatal(err)
	}
	hd := NewHookDrainer(&Daemon{eng: e}, "gap", "", filepath.Join(t.TempDir(), "hook.fold"))
	_, _, err := hd.applyLocked(shim.HookDrainResponse{Gap: true, GapBoundary: 1}, 0)
	var custody *engine.ExecutionCustodyError
	if !errors.As(err, &custody) || hd.cursor() != 0 {
		t.Fatalf("gap custody was discarded: %v", err)
	}
	record, err := e.ExecutionSnapshot("gap")
	if err != nil {
		t.Fatal(err)
	}
	if !record.UnknownActors || record.Incomplete == "" {
		t.Fatal("observed gap did not invalidate custody")
	}
}

func TestExecutionConversationBindingRetriesFailedWrite(t *testing.T) {
	store, err := persist.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	e := engine.New(engine.Config{StoreExecution: func(persist.ExecutionRecord) error {
		calls++
		if calls == 1 {
			return syscall.ENOSPC
		}
		return nil
	}})
	m := persist.Meta{ID: "bind", AgentType: "claude", ShimPID: 1, ShimStartTime: 2, ConversationID: "native"}
	initial := status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}
	if err := e.RegisterExecutionSession(m.ID, "token", 1, claude.New().SignalSources(), initial, engine.ExecutionIdentity{Incarnation: executionIncarnation(m), ObservedFromStart: true}, nil); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{eng: e, executionStore: store}
	var custody *engine.ExecutionCustodyError
	if err := d.bindExecutionConversation(m); !errors.As(err, &custody) {
		t.Fatalf("binding disk failure must retain ingress: %v", err)
	}
	if err := d.bindExecutionConversation(m); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("matching in-memory identity skipped durable binding retry")
	}
}

func TestExecutionNormalAckFailureRemainsRetriable(t *testing.T) {
	calls := 0
	e := engine.New(engine.Config{StoreExecution: func(persist.ExecutionRecord) error {
		calls++
		if calls == 2 {
			return syscall.ENOSPC
		}
		return nil
	}})
	initial := status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}
	if err := e.RegisterExecutionSession("ack", "token", 1, claude.New().SignalSources(), initial, engine.ExecutionIdentity{Incarnation: "native", ObservedFromStart: true}, nil); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{eng: e, core: &daemon.Daemon{}, stateDir: t.TempDir()}
	cb := engine.Callback{SessionID: "ack", Token: "token", Sequence: 1, Event: "SubagentStart", Payload: map[string]string{"agent_id": "child"}}
	raw, err := json.Marshal(cb)
	if err != nil {
		t.Fatal(err)
	}
	var custody *engine.ExecutionCustodyError
	if err := d.ingestHookBytes(raw); !errors.As(err, &custody) {
		t.Fatalf("ack failure was acknowledged: %v", err)
	}
	_ = d.ingestHookBytes(raw)
	record, err := e.ExecutionSnapshot("ack")
	if err != nil {
		t.Fatal(err)
	}
	if len(record.PendingCallbacks) != 0 || !record.Actors["child"].Active {
		t.Fatal("retry failed to retire receipt while preserving child")
	}
}
