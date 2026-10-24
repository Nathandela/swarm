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
	codexSpawnChild(t, e, feed, "", turn, "child-thread", "child-turn")
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
	if err := codexApplyOne(e, feed, CodexExecutionEvent{ActorThreadID: "child-thread", Kind: CodexTurnCompleted, TurnID: "child-turn", Cause: CodexTerminalCompleted}); err != nil {
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
	if ok {
		t.Fatalf("uncorrelated raw input became eligible from a later generic turn: %+v", candidate)
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
	codexSpawnChild(t, e, feed, "", turn, "child", "child-turn")
	for _, event := range []CodexExecutionEvent{{Kind: CodexDecisionRequested, TurnID: turn, RequestID: `123`, Decision: CodexQuestion}} {
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
	e.storeExecution = func(r persist.ExecutionRecord) error { return r.Validate() }
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
	for i := 0; i < persist.MaxExecutionActors*4; i++ {
		id := fmt.Sprintf("reasoning-%d", i)
		for _, kind := range []CodexExecutionKind{CodexItemStarted, CodexItemCompleted} {
			if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: kind, TurnID: event.TurnID, ItemID: id, ItemKind: CodexReasoning}); err != nil {
				t.Fatalf("completed history consumed live capacity at item %d: %v", i, err)
			}
		}
	}
	snapshot, _ = e.ExecutionSnapshot("s1")
	if len(snapshot.Codex.Items) != 0 || snapshot.Codex.UnknownCustody {
		t.Fatal("completed history was not folded into bounded evidence")
	}
	for i := 0; i < persist.MaxExecutionActors; i++ {
		if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexItemStarted, TurnID: event.TurnID, ItemID: fmt.Sprintf("live-%d", i), ItemKind: CodexReasoning}); err != nil {
			t.Fatal(err)
		}
	}
	if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexItemStarted, TurnID: event.TurnID, ItemID: "live-overflow", ItemKind: CodexReasoning}); err == nil {
		t.Fatal("simultaneous live custody overflow was accepted")
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

func TestCodexExecutionFileAndMcpToolCycles(t *testing.T) {
	for _, kind := range []CodexItemKind{"fileChange", "mcpToolCall"} {
		for _, scenario := range []string{"completed", "active", "failed", "final", "null-phase", "question"} {
			t.Run(string(kind)+"/"+scenario, func(t *testing.T) {
				e, feed := codexExecutionEngine(t)
				trace := codexExecutionTrace(t, "quota")
				for i := range trace {
					if trace[i].ItemKind == CodexCommandExecution {
						trace[i].ItemKind = kind
					}
				}
				switch scenario {
				case "active":
					trace = append(trace[:len(trace)-2], trace[len(trace)-1])
				case "failed":
					trace[len(trace)-2].ToolSucceeded = false
				case "final":
					trace[3].Phase = CodexFinalAnswer
					trace[4].Phase = CodexFinalAnswer
				case "null-phase":
					trace[3].Phase = ""
					trace[4].Phase = ""
				case "question":
					terminal := trace[len(trace)-1]
					trace = append(trace[:len(trace)-1], CodexExecutionEvent{Kind: CodexDecisionRequested, TurnID: terminal.TurnID, RequestID: `"question"`, Decision: CodexQuestion}, terminal)
				}
				applyCodexTrace(t, e, feed, trace)
				if _, ok := e.CodexQuotaCandidate("s1"); ok != (scenario == "completed") {
					t.Fatalf("%s %s candidate=%v", kind, scenario, ok)
				}
			})
		}
	}
}

func TestCodexExecutionOrdinaryFailureNeedsFreshPositiveCycle(t *testing.T) {
	for _, failedKind := range []CodexItemKind{CodexCommandExecution, CodexMcpToolCall} {
		for _, scenario := range []string{"fresh-positive", "old-commentary", "overlap", "still-failed", "human", "final", "unknown"} {
			t.Run(string(failedKind)+"/"+scenario, func(t *testing.T) {
				e, feed := codexExecutionEngine(t)
				trace := codexExecutionTrace(t, "quota")
				turn := trace[0].TurnID
				terminal := trace[len(trace)-1]
				trace = trace[:len(trace)-1]
				for i := range trace {
					if trace[i].ItemKind == CodexCommandExecution {
						trace[i].ItemKind = failedKind
						trace[i].ToolSucceeded = false
					}
				}
				if scenario == "final" {
					trace[3].Phase = CodexFinalAnswer
					trace[4].Phase = CodexFinalAnswer
				}
				if scenario == "human" {
					trace = append(trace[:5], append([]CodexExecutionEvent{{Kind: CodexDecisionRequested, TurnID: turn, RequestID: `"approval"`, Decision: CodexApproval}}, trace[5:]...)...)
				}
				if scenario == "unknown" {
					trace = append(trace[:5], append([]CodexExecutionEvent{{Kind: CodexUnknownEvidence}}, trace[5:]...)...)
				}
				if scenario == "overlap" {
					failed := trace[len(trace)-1]
					trace = append(trace[:len(trace)-1], CodexExecutionEvent{Kind: CodexItemStarted, TurnID: turn, ItemID: "retry-commentary", ItemKind: CodexAgentMessage, Phase: CodexCommentary}, failed, CodexExecutionEvent{Kind: CodexItemCompleted, TurnID: turn, ItemID: "retry-commentary", ItemKind: CodexAgentMessage, Phase: CodexCommentary})
				}
				if scenario != "old-commentary" && scenario != "overlap" {
					trace = append(trace, CodexExecutionEvent{Kind: CodexItemStarted, TurnID: turn, ItemID: "retry-commentary", ItemKind: CodexAgentMessage, Phase: CodexCommentary}, CodexExecutionEvent{Kind: CodexItemCompleted, TurnID: turn, ItemID: "retry-commentary", ItemKind: CodexAgentMessage, Phase: CodexCommentary})
				}
				trace = append(trace, CodexExecutionEvent{Kind: CodexItemStarted, TurnID: turn, ItemID: "retry-tool", ItemKind: CodexFileChange}, CodexExecutionEvent{Kind: CodexItemCompleted, TurnID: turn, ItemID: "retry-tool", ItemKind: CodexFileChange, ToolSucceeded: scenario != "still-failed"}, terminal)
				applyCodexTrace(t, e, feed, trace)
				if _, ok := e.CodexQuotaCandidate("s1"); ok != (scenario == "fresh-positive") {
					t.Fatalf("%s candidate=%v", scenario, ok)
				}
			})
		}
	}
}

func TestCodexExecutionLongNativeTurnRetiresCompletedItems(t *testing.T) {
	for _, scenario := range []string{"long-tools", "reused-id", "unmatched-completion", "rejoin"} {
		t.Run(scenario, func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			e.storeExecution = func(r persist.ExecutionRecord) error { return r.Validate() }
			trace := codexExecutionTrace(t, "quota")
			terminal := trace[len(trace)-1]
			applyCodexTrace(t, e, feed, trace[:len(trace)-1])
			for i := 0; i < persist.MaxExecutionActors+2; i++ {
				id := fmt.Sprintf("later-tool-%d", i)
				if scenario == "reused-id" {
					id = "native-reused-id"
				}
				for _, kind := range []CodexExecutionKind{CodexItemStarted, CodexItemCompleted} {
					if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: kind, TurnID: terminal.TurnID, ItemID: id, ItemKind: CodexCommandExecution, ToolSucceeded: true}); err != nil {
						t.Fatalf("native tool cycle %d: %v", i, err)
					}
				}
			}
			if scenario == "unmatched-completion" {
				if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexItemCompleted, TurnID: terminal.TurnID, ItemID: "later-tool-0", ItemKind: CodexCommandExecution, ToolSucceeded: true}); err == nil {
					t.Fatal("completion without a live start was accepted")
				}
			}
			if scenario == "rejoin" {
				feed.FeedEpoch = "new-connection"
				feed.ObservedFromStart = false
				if err := e.BeginCodexExecutionFeed("s1", feed); err != nil {
					t.Fatal(err)
				}
			}
			_ = codexApplyOne(e, feed, terminal)
			_, candidate := e.CodexQuotaCandidate("s1")
			if candidate != (scenario == "long-tools" || scenario == "reused-id") {
				t.Fatalf("incorrect long-turn qualification: %v", candidate)
			}
			snapshot, _ := e.ExecutionSnapshot("s1")
			if len(snapshot.Codex.Items) != 0 {
				t.Fatal("retained completed tool history")
			}
		})
	}
}

func codexActorEvent(t *testing.T, actor string, event CodexExecutionEvent) CodexExecutionEvent {
	t.Helper()
	event.ActorThreadID = actor
	return event
}

func TestCodexExecutionNativeActorCustody(t *testing.T) {
	for _, scenario := range []string{"child-terminal", "grandchild-live", "rollover", "reopened-stale-terminal", "same-item-id", "same-request-id"} {
		t.Run(scenario, func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			trace := codexExecutionTrace(t, "quota")
			terminal := trace[len(trace)-1]
			applyCodexTrace(t, e, feed, trace[:len(trace)-1])
			apply := func(actor string, event CodexExecutionEvent) error {
				return codexApplyOne(e, feed, codexActorEvent(t, actor, event))
			}
			must := func(actor string, event CodexExecutionEvent) {
				t.Helper()
				if err := apply(actor, event); err != nil {
					t.Fatal(err)
				}
			}
			codexSpawnChild(t, e, feed, "", terminal.TurnID, "child", "child-turn")
			if scenario == "grandchild-live" || scenario == "rollover" {
				codexSpawnChild(t, e, feed, "child", "child-turn", "grandchild", "grandchild-turn")
			}
			if scenario == "same-item-id" {
				for _, actor := range []string{"", "child"} {
					turn := terminal.TurnID
					if actor != "" {
						turn = "child-turn"
					}
					must(actor, CodexExecutionEvent{Kind: CodexItemStarted, TurnID: turn, ItemID: "shared-id", ItemKind: CodexReasoning})
				}
				for _, actor := range []string{"child", ""} {
					turn := terminal.TurnID
					if actor != "" {
						turn = "child-turn"
					}
					must(actor, CodexExecutionEvent{Kind: CodexItemCompleted, TurnID: turn, ItemID: "shared-id", ItemKind: CodexReasoning})
				}
			}
			if scenario == "same-request-id" {
				must("", CodexExecutionEvent{Kind: CodexDecisionRequested, TurnID: terminal.TurnID, RequestID: `"shared"`, Decision: CodexQuestion})
				must("child", CodexExecutionEvent{Kind: CodexDecisionRequested, TurnID: "child-turn", RequestID: `"shared"`, Decision: CodexQuestion})
				must("child", CodexExecutionEvent{Kind: CodexDecisionResolved, TurnID: "child-turn", RequestID: `"shared"`})
				snapshot, _ := e.ExecutionSnapshot("s1")
				if len(snapshot.Codex.Decisions) != 1 {
					t.Fatal("child resolved parent decision")
				}
			}
			must("child", CodexExecutionEvent{Kind: CodexTurnCompleted, TurnID: "child-turn", Cause: CodexTerminalCompleted})
			if scenario == "reopened-stale-terminal" {
				reopen := CodexExecutionEvent{Kind: CodexItemStarted, TurnID: terminal.TurnID, ItemID: "reopen", ItemKind: CodexCollabAgentToolCall, CollabTool: "sendInput", ReceiverThreadIDs: []string{"child"}}
				must("", reopen)
				reopen.Kind = CodexItemCompleted
				reopen.ToolSucceeded = true
				must("", reopen)
				must("child", CodexExecutionEvent{Kind: CodexTurnStarted, TurnID: "new-child-turn"})
				if err := apply("child", CodexExecutionEvent{Kind: CodexTurnCompleted, TurnID: "child-turn", Cause: CodexTerminalCompleted}); err == nil {
					t.Fatal("stale child terminal closed reopened actor")
				}
			}
			must("", terminal)
			if scenario == "rollover" {
				for _, event := range trace {
					event.TurnID = "next-parent-turn"
					must("", event)
				}
			}
			_, candidate := e.CodexQuotaCandidate("s1")
			if candidate != (scenario == "child-terminal" || scenario == "same-item-id") {
				t.Fatalf("incorrect actor custody qualification: %v", candidate)
			}
		})
	}
}

func TestCodexExecutionProvisionalActorsAndNativeReopen(t *testing.T) {
	for _, scenario := range []string{"provisional-complete", "unattached", "sendInput", "resumeAgent", "grandchild-late"} {
		t.Run(scenario, func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			trace := codexExecutionTrace(t, "quota")
			terminal := trace[len(trace)-1]
			applyCodexTrace(t, e, feed, trace[:len(trace)-1])
			apply := func(actor string, event CodexExecutionEvent) error {
				return codexApplyOne(e, feed, codexActorEvent(t, actor, event))
			}
			must := func(actor string, event CodexExecutionEvent) {
				t.Helper()
				if err := apply(actor, event); err != nil {
					t.Fatal(err)
				}
			}
			spawn := CodexExecutionEvent{Kind: CodexItemStarted, TurnID: terminal.TurnID, ItemID: "spawn", ItemKind: CodexCollabAgentToolCall, CollabTool: "spawnAgent"}
			must("", spawn)
			must("child", CodexExecutionEvent{Kind: CodexTurnStarted, TurnID: "child-turn"})
			if scenario == "grandchild-late" {
				codexSpawnChild(t, e, feed, "child", "child-turn", "grandchild", "grandchild-turn")
			}
			must("child", CodexExecutionEvent{Kind: CodexTurnCompleted, TurnID: "child-turn", Cause: CodexTerminalCompleted})
			spawn.Kind = CodexItemCompleted
			spawn.ToolSucceeded = scenario != "unattached"
			if scenario != "unattached" {
				spawn.ReceiverThreadIDs = []string{"child"}
			}
			must("", spawn)
			if scenario == "sendInput" || scenario == "resumeAgent" {
				reopen := CodexExecutionEvent{Kind: CodexItemStarted, TurnID: terminal.TurnID, ItemID: "reopen", ItemKind: CodexCollabAgentToolCall, CollabTool: scenario, ReceiverThreadIDs: []string{"child"}}
				must("", reopen)
				reopen.Kind = CodexItemCompleted
				reopen.ToolSucceeded = true
				must("", reopen)
				must("", terminal)
				if _, ok := e.CodexQuotaCandidate("s1"); ok != (scenario == "resumeAgent") {
					t.Fatal("native input and runtime resume custody were conflated")
				}
				must("child", CodexExecutionEvent{Kind: CodexTurnStarted, TurnID: "next-child-turn"})
				must("child", CodexExecutionEvent{Kind: CodexTurnCompleted, TurnID: "next-child-turn", Cause: CodexTerminalCompleted})
			} else {
				must("", terminal)
			}
			if scenario == "grandchild-late" {
				if _, ok := e.CodexQuotaCandidate("s1"); ok {
					t.Fatal("parent and child terminal erased live grandchild")
				}
				for _, event := range trace {
					event.TurnID = "next-parent-turn"
					must("", event)
				}
				must("grandchild", CodexExecutionEvent{Kind: CodexTurnCompleted, TurnID: "grandchild-turn", Cause: CodexTerminalCompleted})
			}
			_, candidate := e.CodexQuotaCandidate("s1")
			if candidate != (scenario != "unattached" && scenario != "sendInput") {
				t.Fatalf("incorrect exact actor qualification: %v", candidate)
			}
		})
	}
}

func TestCodexExecutionManyCompletedChildrenRetainOnlyBoundedCustody(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	e.storeExecution = func(r persist.ExecutionRecord) error { return r.Validate() }
	trace := codexExecutionTrace(t, "quota")
	terminal := trace[len(trace)-1]
	applyCodexTrace(t, e, feed, trace[:len(trace)-1])
	for i := 0; i < persist.MaxExecutionActors*3; i++ {
		child := fmt.Sprintf("child-%d", i)
		spawn := CodexExecutionEvent{Kind: CodexItemStarted, TurnID: terminal.TurnID, ItemID: "native-spawn-call", ItemKind: CodexCollabAgentToolCall, CollabTool: "spawnAgent"}
		if err := codexApplyOne(e, feed, spawn); err != nil {
			t.Fatal(err)
		}
		spawn.Kind = CodexItemCompleted
		spawn.ReceiverThreadIDs = []string{child}
		spawn.ToolSucceeded = true
		if err := codexApplyOne(e, feed, spawn); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []CodexExecutionKind{CodexTurnStarted, CodexTurnCompleted} {
			if err := codexApplyOne(e, feed, CodexExecutionEvent{ActorThreadID: child, Kind: kind, TurnID: "child-turn", Cause: CodexTerminalCompleted}); err != nil {
				t.Fatal(err)
			}
		}
		snapshot, _ := e.ExecutionSnapshot("s1")
		if len(snapshot.Codex.Children) > persist.MaxExecutionActors || snapshot.Codex.UnknownCustody {
			t.Fatal("completed child history exhausted live custody")
		}
	}
	if err := codexApplyOne(e, feed, terminal); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.CodexQuotaCandidate("s1"); !ok {
		t.Fatal("bounded completed child retirement prevented quota qualification")
	}
}

func TestCodexExecutionCloseSnapshotCannotReleaseChildCustody(t *testing.T) {
	for _, openTool := range []bool{false, true} {
		t.Run(fmt.Sprint(openTool), func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			trace := codexExecutionTrace(t, "quota")
			terminal := trace[len(trace)-1]
			applyCodexTrace(t, e, feed, trace[:len(trace)-1])
			must := func(event CodexExecutionEvent) {
				t.Helper()
				if err := codexApplyOne(e, feed, event); err != nil {
					t.Fatal(err)
				}
			}
			codexSpawnChild(t, e, feed, "", terminal.TurnID, "child", "child-turn")
			if openTool {
				must(CodexExecutionEvent{ActorThreadID: "child", Kind: CodexItemStarted, TurnID: "child-turn", ItemID: "tool", ItemKind: CodexCommandExecution})
			}
			closeEvent := CodexExecutionEvent{Kind: CodexItemStarted, TurnID: terminal.TurnID, ItemID: "close", ItemKind: CodexCollabAgentToolCall, CollabTool: "closeAgent", ReceiverThreadIDs: []string{"child"}}
			must(closeEvent)
			closeEvent.Kind = CodexItemCompleted
			closeEvent.ToolSucceeded = true
			must(closeEvent)
			must(terminal)
			if _, ok := e.CodexQuotaCandidate("s1"); ok {
				t.Fatal("close tool result erased independently live child")
			}
			must(CodexExecutionEvent{ActorThreadID: "child", Kind: CodexTurnCompleted, TurnID: "child-turn", Cause: CodexTerminalCancelled})
			_, candidate := e.CodexQuotaCandidate("s1")
			if candidate == openTool {
				t.Fatalf("child terminal lost exact live tool custody: %v", candidate)
			}
		})
	}
}

func codexSpawnChild(t *testing.T, e *Engine, feed CodexFeedIdentity, actor, parentTurn, child, childTurn string) {
	t.Helper()
	spawn := CodexExecutionEvent{ActorThreadID: actor, Kind: CodexItemStarted, TurnID: parentTurn, ItemID: "spawn-" + child, ItemKind: CodexCollabAgentToolCall, CollabTool: "spawnAgent"}
	if err := codexApplyOne(e, feed, spawn); err != nil {
		t.Fatal(err)
	}
	spawn.Kind = CodexItemCompleted
	spawn.ReceiverThreadIDs = []string{child}
	spawn.ToolSucceeded = true
	if err := codexApplyOne(e, feed, spawn); err != nil {
		t.Fatal(err)
	}
	if err := codexApplyOne(e, feed, CodexExecutionEvent{ActorThreadID: child, Kind: CodexTurnStarted, TurnID: childTurn}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexExecutionLateToolCompletionRetiresExactCustody(t *testing.T) {
	for _, scenario := range []string{"root", "child", "root-rollover", "failed", "child-failed", "unmatched", "new-start"} {
		t.Run(scenario, func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			trace := codexExecutionTrace(t, "quota")
			terminal := trace[len(trace)-1]
			applyCodexTrace(t, e, feed, trace[:len(trace)-1])
			must := func(event CodexExecutionEvent) {
				t.Helper()
				if err := codexApplyOne(e, feed, event); err != nil {
					t.Fatal(err)
				}
			}
			actor, turn := "", terminal.TurnID
			if scenario == "child" || scenario == "child-failed" {
				codexSpawnChild(t, e, feed, "", terminal.TurnID, "child", "child-turn")
				actor, turn = "child", "child-turn"
			}
			background := CodexExecutionEvent{ActorThreadID: actor, Kind: CodexItemStarted, TurnID: turn, ItemID: "background", ItemKind: CodexCommandExecution}
			must(background)
			for _, event := range []CodexExecutionEvent{
				{Kind: CodexItemStarted, TurnID: terminal.TurnID, ItemID: "later-commentary", ItemKind: CodexAgentMessage, Phase: CodexCommentary},
				{Kind: CodexItemCompleted, TurnID: terminal.TurnID, ItemID: "later-commentary", ItemKind: CodexAgentMessage, Phase: CodexCommentary},
				{Kind: CodexItemStarted, TurnID: terminal.TurnID, ItemID: "later-success", ItemKind: CodexCommandExecution},
				{Kind: CodexItemCompleted, TurnID: terminal.TurnID, ItemID: "later-success", ItemKind: CodexCommandExecution, ToolSucceeded: true},
			} {
				must(event)
			}
			if scenario == "child" || scenario == "child-failed" {
				must(CodexExecutionEvent{ActorThreadID: actor, Kind: CodexTurnCompleted, TurnID: turn, Cause: CodexTerminalCompleted})
			}
			must(terminal)
			if scenario == "root-rollover" {
				for _, event := range trace {
					event.TurnID = "next-parent-turn"
					must(event)
				}
			}
			if _, ok := e.CodexQuotaCandidate("s1"); ok {
				t.Fatal("live native background custody was ignored")
			}
			before, _ := e.ExecutionSnapshot("s1")
			background.Kind = CodexItemCompleted
			background.ToolSucceeded = scenario != "failed" && scenario != "child-failed"
			if scenario == "unmatched" {
				background.ItemID = "unknown-background"
			}
			if scenario == "new-start" {
				background.Kind = CodexItemStarted
				background.ItemID = "new-background"
			}
			err := codexApplyOne(e, feed, background)
			invalid := scenario == "unmatched" || scenario == "new-start"
			if (err != nil) != invalid {
				t.Fatalf("late custody reduction error=%v", err)
			}
			after, _ := e.ExecutionSnapshot("s1")
			if !invalid && (after.Codex.LastItemSequence != before.Codex.LastItemSequence || after.Codex.ToolResultSequence != before.Codex.ToolResultSequence || after.Codex.TerminalSequence != before.Codex.TerminalSequence) {
				t.Fatal("late background completion advanced terminal phase")
			}
			_, candidate := e.CodexQuotaCandidate("s1")
			if candidate != (!invalid && scenario != "failed" && scenario != "child-failed") {
				t.Fatalf("incorrect late native custody candidate: %v", candidate)
			}
		})
	}
}

func TestCodexExecutionSendInputHoldsCustodyAcrossDelayedChildStart(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	terminal := trace[len(trace)-1]
	applyCodexTrace(t, e, feed, trace[:len(trace)-1])
	codexSpawnChild(t, e, feed, "", terminal.TurnID, "child", "old-child-turn")
	must := func(event CodexExecutionEvent) {
		t.Helper()
		if err := codexApplyOne(e, feed, event); err != nil {
			t.Fatal(err)
		}
	}
	send := CodexExecutionEvent{Kind: CodexItemStarted, TurnID: terminal.TurnID, ItemID: "send", ItemKind: CodexCollabAgentToolCall, CollabTool: "sendInput", ReceiverThreadIDs: []string{"child"}}
	must(send)
	must(CodexExecutionEvent{ActorThreadID: "child", Kind: CodexTurnCompleted, TurnID: "old-child-turn", Cause: CodexTerminalCompleted})
	send.Kind = CodexItemCompleted
	send.ToolSucceeded = true
	must(send)
	must(terminal)
	if _, ok := e.CodexQuotaCandidate("s1"); ok {
		t.Fatal("delayed child turn exposed a false-idle SendInput gap")
	}
	must(CodexExecutionEvent{ActorThreadID: "child", Kind: CodexTurnStarted, TurnID: "delayed-new-child-turn"})
	must(CodexExecutionEvent{ActorThreadID: "child", Kind: CodexTurnCompleted, TurnID: "delayed-new-child-turn", Cause: CodexTerminalCompleted})
	if _, ok := e.CodexQuotaCandidate("s1"); ok {
		t.Fatal("uncorrelated turn terminal retired native input admission")
	}
}

func TestCodexExecutionSuccessfulSpawnRequiresReceiver(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	applyCodexTrace(t, e, feed, trace[:len(trace)-1])
	spawn := CodexExecutionEvent{Kind: CodexItemStarted, TurnID: trace[0].TurnID, ItemID: "empty-spawn", ItemKind: CodexCollabAgentToolCall, CollabTool: "spawnAgent"}
	if err := codexApplyOne(e, feed, spawn); err != nil {
		t.Fatal(err)
	}
	spawn.Kind = CodexItemCompleted
	spawn.ToolSucceeded = true
	if err := codexApplyOne(e, feed, spawn); err == nil {
		t.Fatal("successful spawn without exact native receiver admitted")
	}
}
