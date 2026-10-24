package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func codexExecutionEngine(t *testing.T) (*Engine, CodexFeedIdentity) {
	t.Helper()
	store, err := persist.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{StoreExecution: store.SaveExecution})
	initial := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	identity := ExecutionIdentity{Incarnation: "shim-1", ConversationID: "thread-1", ObservedFromStart: true, OwnerRevision: 1}
	if err := e.RegisterExecutionSession("s1", "", 1, codex.New().SignalSources(), initial, identity, nil); err != nil {
		t.Fatal(err)
	}
	feed := CodexFeedIdentity{NativeIncarnation: "backend-pid-start-1", FeedEpoch: "dial-1", ThreadID: "thread-1", OwnerRevision: 1, ObservedFromStart: true}
	if err := e.BeginCodexExecutionFeed("s1", feed); err != nil {
		t.Fatal(err)
	}
	return e, feed
}

func codexExecutionTrace(t *testing.T, scenario string) []CodexExecutionEvent {
	t.Helper()
	raw, err := os.ReadFile("../../docs/verification/provider-logins/native-handover-capabilities-2026-10-24.json")
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Codex struct {
			Results []struct {
				Scenario string `json:"scenario"`
				Items    []struct {
					Event, Type, ID string
					Phase           *string
					Turn            string
					Exit            *int
				} `json:"item_summary"`
			} `json:"results"`
		} `json:"codex"`
	}
	if err = json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	for _, result := range receipt.Codex.Results {
		if result.Scenario != scenario {
			continue
		}
		turn := result.Items[0].Turn
		events := []CodexExecutionEvent{{Kind: CodexTurnStarted, TurnID: turn}}
		for _, item := range result.Items {
			if item.Turn != turn {
				continue
			}
			phase := CodexAssistantPhase("")
			if item.Phase != nil {
				phase = CodexAssistantPhase(*item.Phase)
			}
			kind := CodexItemStarted
			if item.Event == "item/completed" {
				kind = CodexItemCompleted
			}
			events = append(events, CodexExecutionEvent{Kind: kind, TurnID: turn, ItemID: item.ID, ItemKind: CodexItemKind(item.Type), Phase: phase, ToolSucceeded: item.Exit != nil && *item.Exit == 0})
		}
		return append(events, CodexExecutionEvent{Kind: CodexTurnCompleted, TurnID: turn, Cause: CodexTerminalQuota})
	}
	t.Fatalf("missing native scenario %s", scenario)
	return nil
}

func applyCodexTrace(t *testing.T, e *Engine, feed CodexFeedIdentity, events []CodexExecutionEvent) {
	t.Helper()
	for i, event := range events {
		event.NativeIncarnation = feed.NativeIncarnation
		event.FeedEpoch = feed.FeedEpoch
		event.ThreadID = feed.ThreadID
		event.Sequence = uint64(i + 1)
		if err := e.ApplyCodexExecutionEvent("s1", event); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCodexExecutionNativePhaseQualification(t *testing.T) {
	for _, scenario := range []string{"quota", "finalquota", "unknownquota"} {
		t.Run(scenario, func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			events := codexExecutionTrace(t, scenario)
			applyCodexTrace(t, e, feed, events)
			candidate, ok := e.CodexQuotaCandidate("s1")
			if ok != (scenario == "quota") {
				t.Fatalf("native %s candidate=%+v admitted=%v", scenario, candidate, ok)
			}
			if ok && (candidate.TurnID != events[0].TurnID || candidate.NativeIncarnation != feed.NativeIncarnation || candidate.OwnerRevision != 1 || candidate.TerminalSequence != uint64(len(events))) {
				t.Fatalf("candidate lost exact native identity: %+v", candidate)
			}
		})
	}
}

func codexApplyOne(e *Engine, feed CodexFeedIdentity, event CodexExecutionEvent) error {
	snapshot, _ := e.ExecutionSnapshot("s1")
	event.Sequence = snapshot.Codex.Sequence + 1
	event.NativeIncarnation = feed.NativeIncarnation
	event.FeedEpoch = feed.FeedEpoch
	event.ThreadID = feed.ThreadID
	return e.ApplyCodexExecutionEvent("s1", event)
}

func TestCodexExecutionRejectsUnsafeQuotaCandidates(t *testing.T) {
	for _, scenario := range []string{"missing-start", "missing-user", "missing-commentary", "tool-active", "tool-failed", "trailing-question", "approval", "question-resolved", "maintenance", "unknown-item", "completed", "cancelled", "active-retry", "wrong-turn"} {
		t.Run(scenario, func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			trace := codexExecutionTrace(t, "quota")
			turn := trace[0].TurnID
			switch scenario {
			case "missing-start":
				trace = trace[1:]
			case "missing-user":
				trace = append(trace[:1], trace[3:]...)
			case "missing-commentary":
				trace = append(trace[:3], trace[5:]...)
			case "tool-active":
				trace = append(trace[:len(trace)-2], trace[len(trace)-1])
			case "tool-failed":
				trace[len(trace)-2].ToolSucceeded = false
			case "trailing-question":
				trace = append(trace[:len(trace)-1], CodexExecutionEvent{Kind: CodexItemStarted, TurnID: turn, ItemID: "question", ItemKind: CodexAgentMessage, Phase: CodexCommentary}, CodexExecutionEvent{Kind: CodexItemCompleted, TurnID: turn, ItemID: "question", ItemKind: CodexAgentMessage, Phase: CodexCommentary}, trace[len(trace)-1])
			case "approval", "question-resolved":
				kind := CodexApproval
				if scenario == "question-resolved" {
					kind = CodexQuestion
				}
				terminal := trace[len(trace)-1]
				trace = append(trace[:len(trace)-1], CodexExecutionEvent{Kind: CodexDecisionRequested, TurnID: turn, RequestID: `"request-1"`, Decision: kind})
				if scenario == "question-resolved" {
					trace = append(trace, CodexExecutionEvent{Kind: CodexDecisionResolved, RequestID: `"request-1"`})
				}
				trace = append(trace, terminal)
			case "maintenance", "unknown-item":
				kind := CodexContextCompaction
				if scenario == "unknown-item" {
					kind = "futureTool"
				}
				terminal := trace[len(trace)-1]
				trace = append(trace[:len(trace)-1], CodexExecutionEvent{Kind: CodexItemStarted, TurnID: turn, ItemID: "extra", ItemKind: kind}, CodexExecutionEvent{Kind: CodexItemCompleted, TurnID: turn, ItemID: "extra", ItemKind: kind}, terminal)
			case "completed":
				trace[len(trace)-1].Cause = CodexTerminalCompleted
			case "cancelled":
				trace[len(trace)-1].Cause = CodexTerminalCancelled
			case "active-retry":
				trace[len(trace)-1] = CodexExecutionEvent{Kind: CodexRetryActive, TurnID: turn}
			case "wrong-turn":
				trace[len(trace)-1].TurnID = "previous-native-turn"
			}
			for _, event := range trace {
				_ = codexApplyOne(e, feed, event)
			}
			if candidate, ok := e.CodexQuotaCandidate("s1"); ok {
				t.Fatalf("unsafe %s qualified: %+v", scenario, candidate)
			}
		})
	}
}

func TestCodexExecutionNeutralReasoningAndPlanNeverSupplyPositiveWork(t *testing.T) {
	for _, positive := range []bool{false, true} {
		t.Run(fmt.Sprintf("tool-cycle-%v", positive), func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			trace := codexExecutionTrace(t, "quota")
			turn := trace[0].TurnID
			terminal := trace[len(trace)-1]
			if positive {
				trace = trace[:len(trace)-1]
			} else {
				trace = trace[:3]
			}
			for i, kind := range []CodexItemKind{CodexReasoning, CodexPlan} {
				id := fmt.Sprintf("neutral-%d", i)
				trace = append(trace, CodexExecutionEvent{Kind: CodexItemStarted, TurnID: turn, ItemID: id, ItemKind: kind}, CodexExecutionEvent{Kind: CodexItemCompleted, TurnID: turn, ItemID: id, ItemKind: kind})
			}
			trace = append(trace, terminal)
			applyCodexTrace(t, e, feed, trace)
			_, ok := e.CodexQuotaCandidate("s1")
			if ok != positive {
				t.Fatalf("neutral items changed positive-work requirement: %v", ok)
			}
		})
	}
}

func TestCodexExecutionIndependentChildSurvivesParentTerminal(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	turn := trace[0].TurnID
	applyCodexTrace(t, e, feed, trace[:len(trace)-1])
	if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexChildStarted, TurnID: turn, ChildID: "child-thread", ChildTurnID: "child-turn"}); err != nil {
		t.Fatal(err)
	}
	if err := codexApplyOne(e, feed, trace[len(trace)-1]); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.CodexQuotaCandidate("s1"); ok {
		t.Fatal("parent terminal erased independently active child")
	}
	snapshot, _ := e.ExecutionSnapshot("s1")
	if !snapshot.Codex.Children["child-thread"].Active {
		t.Fatal("parent terminal erased child custody")
	}
	if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexChildCompleted, TurnID: turn, ChildID: "child-thread", ChildTurnID: "child-turn"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.CodexQuotaCandidate("s1"); !ok {
		t.Fatal("exact child terminal did not release independent custody")
	}
}

func TestCodexExecutionFeedGapReconnectAndRestoreDeny(t *testing.T) {
	for _, scenario := range []string{"gap", "reconnect", "restore", "owner-input", "conflicting-event", "foreign-incarnation", "missing-birth-proof"} {
		t.Run(scenario, func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			trace := codexExecutionTrace(t, "quota")
			if scenario == "foreign-incarnation" {
				applyCodexTrace(t, e, feed, trace[:len(trace)-1])
				event := trace[len(trace)-1]
				event.NativeIncarnation = "foreign"
				event.FeedEpoch = feed.FeedEpoch
				event.ThreadID = feed.ThreadID
				event.Sequence = uint64(len(trace))
				if err := e.ApplyCodexExecutionEvent("s1", event); err == nil {
					t.Fatal("foreign native terminal accepted")
				}
			} else {
				applyCodexTrace(t, e, feed, trace)
			}
			switch scenario {
			case "gap":
				if err := e.InvalidateExecution("s1"); err != nil {
					t.Fatal(err)
				}
			case "reconnect":
				feed.FeedEpoch = "dial-2"
				if err := e.BeginCodexExecutionFeed("s1", feed); err != nil {
					t.Fatal(err)
				}
			case "restore":
				saved, _ := e.ExecutionSnapshot("s1")
				after := New(Config{StoreExecution: e.storeExecution})
				initial := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
				if err := after.RegisterExecutionSession("s1", "", 1, codex.New().SignalSources(), initial, ExecutionIdentity{Incarnation: "shim-1", ConversationID: feed.ThreadID, OwnerRevision: 1}, &saved); err != nil {
					t.Fatal(err)
				}
				e = after
			case "owner-input":
				if err := e.ObserveExecutionOwnerRevision("s1", 1, 2); err != nil {
					t.Fatal(err)
				}
			case "conflicting-event":
				event := trace[len(trace)-1]
				event.NativeIncarnation = feed.NativeIncarnation
				event.FeedEpoch = feed.FeedEpoch
				event.ThreadID = feed.ThreadID
				event.Sequence = uint64(len(trace))
				event.Cause = CodexTerminalFailed
				if err := e.ApplyCodexExecutionEvent("s1", event); err == nil {
					t.Fatal("conflicting same-position event accepted")
				}
			case "missing-birth-proof":
				e.mu.Lock()
				e.sessions["s1"].codexExecution = nil
				e.mu.Unlock()
				feed.ObservedFromStart = false
				if err := e.BeginCodexExecutionFeed("s1", feed); err != nil {
					t.Fatal(err)
				}
				applyCodexTrace(t, e, feed, trace)
			}
			if _, ok := e.CodexQuotaCandidate("s1"); ok {
				t.Fatalf("%s retained quota authority", scenario)
			}
		})
	}
}

func TestCodexExecutionDurabilityFailureDeniesAndExactRetryRetainsIdentity(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	applyCodexTrace(t, e, feed, trace[:len(trace)-1])
	original := e.storeExecution
	e.storeExecution = func(persist.ExecutionRecord) error { return syscall.ENOSPC }
	terminal := trace[len(trace)-1]
	terminal.NativeIncarnation = feed.NativeIncarnation
	terminal.FeedEpoch = feed.FeedEpoch
	terminal.ThreadID = feed.ThreadID
	terminal.Sequence = uint64(len(trace))
	err := e.ApplyCodexExecutionEvent("s1", terminal)
	var custody *ExecutionCustodyError
	if !errors.As(err, &custody) || !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("non-durable terminal acknowledged: %v", err)
	}
	if _, ok := e.CodexQuotaCandidate("s1"); ok {
		t.Fatal("failed durable terminal qualified")
	}
	e.storeExecution = original
	if err = e.ApplyCodexExecutionEvent("s1", terminal); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := e.ExecutionSnapshot("s1")
	if snapshot.Codex.TerminalSequence != terminal.Sequence || snapshot.Codex.TurnID != terminal.TurnID {
		t.Fatal("retry replayed/dropped exact turn")
	}
	if _, ok := e.CodexQuotaCandidate("s1"); ok {
		t.Fatal("disk failure uncertainty silently repaired into authority")
	}
}

func TestCodexExecutionNewTurnAndOwnerRevisionFenceOldCandidate(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	applyCodexTrace(t, e, feed, trace)
	if err := e.ObserveExecutionOwnerRevision("s1", 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.CodexQuotaCandidate("s1"); ok {
		t.Fatal("new owner input retained old turn obligation")
	}
	for _, event := range trace {
		event.TurnID = "new-owner-turn"
		if err := codexApplyOne(e, feed, event); err != nil {
			t.Fatal(err)
		}
	}
	candidate, ok := e.CodexQuotaCandidate("s1")
	if !ok || candidate.TurnID != "new-owner-turn" || candidate.OwnerRevision != 2 {
		t.Fatalf("fresh fully observed owner turn did not bind current revision: %+v %v", candidate, ok)
	}
	if err := e.ObserveExecutionOwnerRevision("s1", 1, 2); err == nil {
		t.Fatal("stale owner revision reused")
	}
}

func TestCodexExecutionReconnectKeepsUnresolvedCustody(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	turn := trace[0].TurnID
	applyCodexTrace(t, e, feed, trace[:len(trace)-1])
	for _, event := range []CodexExecutionEvent{{Kind: CodexChildStarted, TurnID: turn, ChildID: "child", ChildTurnID: "child-turn"}, {Kind: CodexDecisionRequested, TurnID: turn, RequestID: `123`, Decision: CodexQuestion}} {
		if err := codexApplyOne(e, feed, event); err != nil {
			t.Fatal(err)
		}
	}
	feed.FeedEpoch = "dial-2"
	if err := e.BeginCodexExecutionFeed("s1", feed); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := e.ExecutionSnapshot("s1")
	if !snapshot.Codex.Children["child"].Active || len(snapshot.Codex.Decisions) != 1 || !snapshot.Codex.UnknownCustody {
		t.Fatal("reconnect dropped independent custody")
	}
	for _, event := range trace {
		event.TurnID = "later-parent-turn"
		_ = codexApplyOne(e, feed, event)
	}
	if _, ok := e.CodexQuotaCandidate("s1"); ok {
		t.Fatal("new parent start laundered reconnect uncertainty")
	}
	if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexDecisionResolved, RequestID: `"123"`}); err == nil {
		t.Fatal("string request ID resolved numeric request")
	}
	snapshot, _ = e.ExecutionSnapshot("s1")
	if len(snapshot.Codex.Decisions) != 1 {
		t.Fatal("unrelated request resolution erased pending decision")
	}
}

func TestCodexExecutionSnapshotAndBoundedState(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	applyCodexTrace(t, e, feed, trace)
	snapshot, _ := e.ExecutionSnapshot("s1")
	snapshot.Codex.Items = map[string]persist.CodexExecutionItem{}
	snapshot.Codex.UnknownCustody = true
	if _, ok := e.CodexQuotaCandidate("s1"); !ok {
		t.Fatal("caller mutated live evidence through snapshot")
	}
	event := CodexExecutionEvent{Kind: CodexTurnStarted, TurnID: "long-turn"}
	if err := codexApplyOne(e, feed, event); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < persist.MaxExecutionActors+1; i++ {
		id := fmt.Sprintf("reasoning-%d", i)
		for _, kind := range []CodexExecutionKind{CodexItemStarted, CodexItemCompleted} {
			_ = codexApplyOne(e, feed, CodexExecutionEvent{Kind: kind, TurnID: event.TurnID, ItemID: id, ItemKind: CodexReasoning})
		}
	}
	snapshot, _ = e.ExecutionSnapshot("s1")
	if len(snapshot.Codex.Items) > persist.MaxExecutionActors || !snapshot.Codex.UnknownCustody {
		t.Fatal("bounded execution overflow did not fail closed")
	}
}

func TestCodexExecutionTerminalPendingDurabilityCannotQualify(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	applyCodexTrace(t, e, feed, trace[:len(trace)-1])
	original := e.storeExecution
	entered, release := make(chan struct{}), make(chan struct{})
	e.storeExecution = func(r persist.ExecutionRecord) error {
		if r.Codex.State == "terminal" {
			close(entered)
			<-release
		}
		return original(r)
	}
	done := make(chan error, 1)
	go func() { done <- codexApplyOne(e, feed, trace[len(trace)-1]) }()
	<-entered
	if _, ok := e.CodexQuotaCandidate("s1"); ok {
		close(release)
		<-done
		t.Fatal("candidate visible before durable terminal acknowledgement")
	}
	e.RegisterSession("unrelated", "token", 1, claudeSignalSources(t))
	other := make(chan error, 1)
	go func() {
		other <- e.HandleCallback(Callback{SessionID: "unrelated", Token: "token", Sequence: 1, Event: "UserPromptSubmit"})
	}()
	select {
	case err := <-other:
		if err != nil {
			close(release)
			<-done
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("slow native record persistence held global engine mutex")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok := e.CodexQuotaCandidate("s1"); !ok {
		t.Fatal("durable terminal did not become inspectable")
	}
}

func TestCodexExecutionGapInvalidatesBeforeWaitingForDiskCustody(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	applyCodexTrace(t, e, feed, codexExecutionTrace(t, "quota"))
	original := e.storeExecution
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.storeExecution = func(r persist.ExecutionRecord) error {
		once.Do(func() { close(entered); <-release })
		return original(r)
	}
	writing := make(chan error, 1)
	go func() { writing <- e.BeginCodexExecutionFeed("s1", feed) }()
	<-entered
	invalidating := make(chan error, 1)
	go func() { invalidating <- e.InvalidateExecution("s1") }()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, _ := e.ExecutionSnapshot("s1")
		if snapshot.Codex.UnknownCustody {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			<-writing
			<-invalidating
			t.Fatal("observed feed gap waited behind disk before invalidating authority")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-writing; err != nil {
		t.Fatal(err)
	}
	if err := <-invalidating; err != nil {
		t.Fatal(err)
	}
	if _, ok := e.CodexQuotaCandidate("s1"); ok {
		t.Fatal("gap returned authority after concurrent write")
	}
}
