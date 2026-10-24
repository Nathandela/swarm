package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecutionChildCustodySurvivesRestart(t *testing.T) {
	clk, rec := newClock(), &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
	for i, event := range []string{"UserPromptSubmit", "SubagentStart", "Stop"} {
		p := map[string]string{}
		if event == "SubagentStart" {
			p["agent_id"] = "child"
		}
		if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: uint64(i + 1), Event: event, Payload: p}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := e.ExecutionSnapshot("s1")
	if err != nil {
		t.Fatal(err)
	}
	after := newEngine(clk, constCPU(0), rec, 30*time.Second, time.Second)
	after.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
	if err := after.RestoreExecution("s1", snapshot); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Minute)
	after.OnOutput("s1", claudeIdleScreen())
	got, _ := rec.last()
	if got.s.Turn != status.TurnActive {
		t.Fatalf("live child lost on restart: %+v", got.s)
	}
	if err := after.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 4, Event: "SubagentStop", Payload: map[string]string{"agent_id": "child"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = rec.last()
	if got.s.Turn != status.TurnIdle {
		t.Fatalf("exact child completion did not release custody: %+v", got.s)
	}
}

func TestExecutionPersistenceFailureRejectsAcknowledgement(t *testing.T) {
	e := New(Config{StoreExecution: func(persist.ExecutionRecord) error { return syscall.ENOSPC }})
	registerManagedExecution(t, e, "s1", "tok1", 1)
	err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "SubagentStart", Payload: map[string]string{"agent_id": "child"}})
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("acknowledged non-durable evidence: %v", err)
	}
	r, _ := e.ExecutionSnapshot("s1")
	if r.Incomplete != "persistence_failure" || !r.Actors["child"].Active {
		t.Fatalf("failure lost custody: %+v", r)
	}
}

func TestExecutionDiskDoesNotBlockOtherSession(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	e := New(Config{StoreExecution: func(r persist.ExecutionRecord) error {
		if r.SessionID == "s1" {
			close(entered)
			<-release
		}
		return nil
	}})
	for _, id := range []string{"s1", "s2"} {
		registerManagedExecution(t, e, id, "tok1", 1)
	}
	done := make(chan error, 1)
	go func() {
		done <- e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "UserPromptSubmit"})
	}()
	<-entered
	pending, _ := e.ExecutionSnapshot("s1")
	if pending.Incomplete != "persistence_pending" {
		close(release)
		t.Fatal("pending disk exposed committed evidence")
	}
	other := make(chan error, 1)
	go func() {
		other <- e.HandleCallback(Callback{SessionID: "s2", Token: "tok1", Sequence: 1, Event: "UserPromptSubmit"})
	}()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("global engine mutex held during disk write")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestExecutionPendingDecisionSurvivesHeuristicAndRestart(t *testing.T) {
	clk := newClock()
	e := newEngine(clk, constCPU(0), &emitRecorder{}, time.Second, time.Second)
	e.RegisterSession("s1", "tok1", 1, claudeSignalSources(t))
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "PermissionRequest", Payload: map[string]string{"agent_id": "child"}}); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Minute)
	e.OnOutput("s1", claudeIdleScreen())
	r, _ := e.ExecutionSnapshot("s1")
	if r.Interaction != status.InteractionPermission || r.InputActor != "child" {
		t.Fatalf("heuristic erased decision custody: %+v", r)
	}
}

func TestExecutionRestoreExactIdentityAndGap(t *testing.T) {
	e := New(Config{})
	identity := ExecutionIdentity{Incarnation: "native-1", ConversationID: "thread-1", OwnerRevision: 7}
	initial := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	r := persist.ExecutionRecord{Version: persist.ExecutionVersion, SessionID: "s1", Incarnation: "native-1", ConversationID: "thread-1", OwnerRevision: 7, MainTurn: status.TurnIdle, Interaction: status.InteractionNone}
	if err := e.RegisterExecutionSession("s1", "tok", 1, claudeSignalSources(t), initial, identity, &r); err != nil {
		t.Fatal(err)
	}
	got, _ := e.ExecutionSnapshot("s1")
	if got.Incomplete != "reconciliation_required" {
		t.Fatal("restart silently trusts offline observation gap")
	}
	r.Incarnation = "other-native"
	if err := e.RegisterExecutionSession("s1", "tok", 1, claudeSignalSources(t), initial, identity, &r); err == nil {
		t.Fatal("accepted other incarnation")
	}
	got, _ = e.ExecutionSnapshot("s1")
	if got.Incomplete != "identity_mismatch" {
		t.Fatalf("missing mismatch fence: %+v", got)
	}
}

func TestExecutionBoundedActors(t *testing.T) {
	e := New(Config{})
	e.RegisterSession("s1", "tok", 1, claudeSignalSources(t))
	for i := 0; i < persist.MaxExecutionActors+1; i++ {
		_ = e.HandleCallback(Callback{SessionID: "s1", Token: "tok", Sequence: uint64(i + 1), Event: "SubagentStart", Payload: map[string]string{"agent_id": fmt.Sprintf("child-%d", i)}})
	}
	r, _ := e.ExecutionSnapshot("s1")
	if len(r.Actors) != persist.MaxExecutionActors || r.Incomplete != "actor_overflow" {
		t.Fatalf("actor overflow was not bounded/fenced: %+v", r)
	}
}

func TestExecutionFailedCallbackRetryAcknowledgesWithoutReplay(t *testing.T) {
	fail := true
	e := New(Config{StoreExecution: func(persist.ExecutionRecord) error {
		if fail {
			return syscall.ENOSPC
		}
		return nil
	}})
	registerManagedExecution(t, e, "s1", "tok", 1)
	cb := Callback{SessionID: "s1", Token: "tok", Sequence: 1, Event: "SubagentStart", Payload: map[string]string{"agent_id": "child"}}
	if err := e.HandleCallback(cb); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	fail = false
	if err := e.HandleCallback(cb); err != nil {
		t.Fatalf("cannot acknowledge exact retry after disk recovery: %v", err)
	}
	r, _ := e.ExecutionSnapshot("s1")
	if len(r.Actors) != 1 || !r.Actors["child"].Active {
		t.Fatalf("retry replayed/lost actor: %+v", r)
	}
}

func TestExecutionConversationBindingPreservesCustody(t *testing.T) {
	e := New(Config{})
	e.RegisterSession("s1", "tok", 1, claudeSignalSources(t))
	if err := e.HandleCallback(Callback{SessionID: "s1", Token: "tok", Sequence: 1, Event: "SubagentStart", Payload: map[string]string{"agent_id": "child"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.BindExecutionConversation("s1", "legacy-1", "thread-1"); err != nil {
		t.Fatal(err)
	}
	r, _ := e.ExecutionSnapshot("s1")
	if r.ConversationID != "thread-1" || !r.Actors["child"].Active {
		t.Fatal("binding dropped custody")
	}
	if err := e.BindExecutionConversation("s1", "legacy-1", "thread-2"); err == nil {
		t.Fatal("rebound conversation on live incarnation")
	}
	if err := e.BindExecutionConversation("s1", "other", "thread-1"); err == nil {
		t.Fatal("accepted foreign incarnation binding")
	}
}

func TestExecutionOverflowNeverProjectsFalseIdle(t *testing.T) {
	clk, rec := newClock(), &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Second, time.Second)
	e.RegisterSession("s1", "tok", 1, claudeSignalSources(t))
	for i := 0; i < persist.MaxExecutionActors+1; i++ {
		_ = e.HandleCallback(Callback{SessionID: "s1", Token: "tok", Sequence: uint64(i + 1), Event: "SubagentStart", Payload: map[string]string{"agent_id": fmt.Sprintf("child-%d", i)}})
	}
	for i := 0; i < persist.MaxExecutionActors; i++ {
		_ = e.HandleCallback(Callback{SessionID: "s1", Token: "tok", Sequence: uint64(i + 200), Event: "SubagentStop", Payload: map[string]string{"agent_id": fmt.Sprintf("child-%d", i)}})
	}
	clk.advance(time.Minute)
	e.OnOutput("s1", claudeIdleScreen())
	got, _ := rec.last()
	if got.s.Turn == status.TurnIdle {
		t.Fatal("untracked live actor projected idle")
	}
	r, _ := e.ExecutionSnapshot("s1")
	if !r.UnknownActors {
		t.Fatal("overflow custody not durable")
	}
}

func TestExecutionPendingCallbackSurvivesCrashBeforeOuterFold(t *testing.T) {
	store, err := persist.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{StoreExecution: store.SaveExecution})
	identity := ExecutionIdentity{Incarnation: "native-1", ConversationID: "thread-1"}
	initial := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	if err = e.RegisterExecutionSession("s1", "tok", 1, claudeSignalSources(t), initial, identity, nil); err != nil {
		t.Fatal(err)
	}
	cb := Callback{SessionID: "s1", Token: "tok", Sequence: 1, Event: "SubagentStart", Payload: map[string]string{"agent_id": "child"}, Raw: json.RawMessage(`{"owned":"body"}`)}
	if err = e.HandleCallback(cb); err != nil {
		t.Fatal(err)
	}
	saved, err := store.LoadExecution("s1")
	if err != nil {
		t.Fatal(err)
	}
	after := New(Config{StoreExecution: store.SaveExecution})
	if err = after.RegisterExecutionSession("s1", "tok", 1, claudeSignalSources(t), initial, identity, &saved); err != nil {
		t.Fatal(err)
	}
	wrong := cb
	wrong.Raw = json.RawMessage(`{"owned":"other-body"}`)
	if err = after.HandleCallback(wrong); err == nil {
		t.Fatal("accepted mismatched pending callback body")
	}
	if err = after.HandleCallback(cb); err != nil {
		t.Fatalf("cannot finish outer fold after crash: %v", err)
	}
	snapshot, _ := after.ExecutionSnapshot("s1")
	if len(snapshot.Actors) != 1 || !snapshot.Actors["child"].Active || len(snapshot.PendingCallbacks) != 1 {
		t.Fatalf("replay changed reducer custody: %+v", snapshot)
	}
	if err = after.AcknowledgeExecutionCallback(wrong); err == nil {
		t.Fatal("foreign body released custody")
	}
	if err = after.AcknowledgeExecutionCallback(cb); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.LoadExecution("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.PendingCallbacks) != 0 {
		t.Fatal("outer fold acknowledgement did not durably release receipt")
	}
}

func registerManagedExecution(t *testing.T, e *Engine, id, token string, pid int) {
	t.Helper()
	initial := status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionUnknown}
	if err := e.RegisterExecutionSession(id, token, pid, claudeSignalSources(t), initial, ExecutionIdentity{Incarnation: fmt.Sprintf("legacy-%d", pid)}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionObservedLaunchIsDistinctFromMissingRestart(t *testing.T) {
	e := New(Config{})
	initial := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	for _, fresh := range []bool{false, true} {
		identity := ExecutionIdentity{Incarnation: "native-1", ObservedFromStart: fresh}
		if err := e.RegisterExecutionSession("s1", "tok", 1, claudeSignalSources(t), initial, identity, nil); err != nil {
			t.Fatal(err)
		}
		r, _ := e.ExecutionSnapshot("s1")
		if fresh {
			if r.Incomplete != "unqualified_native_turn" || r.UnknownActors {
				t.Fatalf("fresh launch invented missing history: %+v", r)
			}
		} else if r.Incomplete != "missing_snapshot" || !r.UnknownActors {
			t.Fatalf("restart absence treated as fresh launch: %+v", r)
		}
	}
}

func TestExecutionReducerErrorPersistsInvalidationBeforeSkip(t *testing.T) {
	for _, backend := range []bool{false, true} {
		t.Run(fmt.Sprintf("backend-%v", backend), func(t *testing.T) {
			fail := true
			var saved persist.ExecutionRecord
			e := New(Config{StoreExecution: func(r persist.ExecutionRecord) error {
				if fail {
					return syscall.ENOSPC
				}
				saved = r
				return nil
			}})
			registerManagedExecution(t, e, "s1", "tok", 1)
			invoke := func() error {
				p := map[string]string{"agent_id": strings.Repeat("a", 257)}
				if backend {
					return e.ApplyTypedEvent("s1", "SubagentStart", p)
				}
				return e.HandleCallback(Callback{SessionID: "s1", Token: "tok", Sequence: 1, Event: "SubagentStart", Payload: p})
			}
			err := invoke()
			var custody *ExecutionCustodyError
			if !errors.As(err, &custody) || !errors.Is(err, syscall.ENOSPC) {
				t.Fatalf("lost retryable invalidation custody: %v", err)
			}
			fail = false
			err = invoke()
			custody = nil
			if err == nil || errors.As(err, &custody) {
				t.Fatalf("expected ordinary rejected event only after durable invalidation: %v", err)
			}
			if !saved.UnknownActors {
				t.Fatal("skip authorized before unknown actors durable")
			}
		})
	}
}

func TestExecutionAcknowledgementFailureRetainsRetryCustody(t *testing.T) {
	fail := false
	var saved persist.ExecutionRecord
	e := New(Config{StoreExecution: func(r persist.ExecutionRecord) error {
		if fail {
			return syscall.ENOSPC
		}
		saved = r
		return nil
	}})
	registerManagedExecution(t, e, "s1", "tok", 1)
	cb := Callback{SessionID: "s1", Token: "tok", Sequence: 1, Event: "UserPromptSubmit"}
	if err := e.HandleCallback(cb); err != nil {
		t.Fatal(err)
	}
	fail = true
	err := e.AcknowledgeExecutionCallback(cb)
	var custody *ExecutionCustodyError
	if !errors.As(err, &custody) || !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("cleanup failure lost retry custody: %v", err)
	}
	fail = false
	if err = e.AcknowledgeExecutionCallback(cb); err != nil {
		t.Fatal(err)
	}
	if len(saved.PendingCallbacks) != 0 {
		t.Fatal("cleanup retry did not durably prune receipt")
	}
}

func TestExecutionInvalidationLeavesLegacySessionsAlone(t *testing.T) {
	e := New(Config{})
	e.RegisterSession("s1", "tok", 1, claudeSignalSources(t))
	before, _ := e.ExecutionSnapshot("s1")
	if err := e.InvalidateExecution("s1"); err != nil {
		t.Fatal(err)
	}
	after, _ := e.ExecutionSnapshot("s1")
	if before.Incomplete != after.Incomplete || before.UnknownActors != after.UnknownActors {
		t.Fatal("new execution invalidation changed legacy session")
	}
}
