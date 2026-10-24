package engine

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func codexPublishedStatus(e *Engine) status.Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sessions["s1"].status
}

func TestCodexCustodyProjectsWorkAfterRootTerminal(t *testing.T) {
	for _, scenario := range []string{"child", "background", "stimulus", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			trace := codexExecutionTrace(t, "quota")
			terminal := trace[len(trace)-1]
			applyCodexTrace(t, e, feed, trace[:len(trace)-1])
			var completion CodexExecutionEvent
			switch scenario {
			case "child", "stimulus":
				codexSpawnChild(t, e, feed, "", terminal.TurnID, "child", "child-turn")
				completion = CodexExecutionEvent{ActorThreadID: "child", Kind: CodexTurnCompleted, TurnID: "child-turn", Cause: CodexTerminalCompleted}
				if scenario == "stimulus" {
					if err := codexApplyOne(e, feed, completion); err != nil {
						t.Fatal(err)
					}
					send := CodexExecutionEvent{Kind: CodexItemStarted, TurnID: terminal.TurnID, ItemID: "send", ItemKind: CodexCollabAgentToolCall, CollabTool: "sendInput", ReceiverThreadIDs: []string{"child"}}
					if err := codexApplyOne(e, feed, send); err != nil {
						t.Fatal(err)
					}
					send.Kind = CodexItemCompleted
					send.ToolSucceeded = true
					if err := codexApplyOne(e, feed, send); err != nil {
						t.Fatal(err)
					}
				}
			case "background":
				completion = CodexExecutionEvent{Kind: CodexItemStarted, TurnID: terminal.TurnID, ItemID: "background", ItemKind: CodexCommandExecution}
				if err := codexApplyOne(e, feed, completion); err != nil {
					t.Fatal(err)
				}
				completion.Kind = CodexItemCompleted
				completion.ToolSucceeded = true
			case "unknown":
				if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexUnknownEvidence}); err != nil {
					t.Fatal(err)
				}
			}
			if err := codexApplyOne(e, feed, terminal); err != nil {
				t.Fatal(err)
			}
			expected := status.TurnActive
			if scenario == "stimulus" || scenario == "unknown" {
				expected = status.TurnUnknown
			}
			if got := codexPublishedStatus(e); got.Turn != expected {
				t.Fatalf("root terminal hid native %s custody: %+v", scenario, got)
			}
			if scenario == "child" || scenario == "background" {
				if err := codexApplyOne(e, feed, completion); err != nil {
					t.Fatal(err)
				}
				if got := codexPublishedStatus(e); got.Turn != status.TurnIdle {
					t.Fatalf("exact native release kept work active: %+v", got)
				}
			}
		})
	}
}

func TestCodexCustodyHumanDecisionOutranksConcurrentChildWork(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	terminal := trace[len(trace)-1]
	applyCodexTrace(t, e, feed, trace[:len(trace)-1])
	codexSpawnChild(t, e, feed, "", terminal.TurnID, "child", "child-turn")
	request := CodexExecutionEvent{ActorThreadID: "child", Kind: CodexDecisionRequested, TurnID: "child-turn", RequestID: `"permission"`, Decision: CodexApproval}
	if err := codexApplyOne(e, feed, request); err != nil {
		t.Fatal(err)
	}
	if got := codexPublishedStatus(e); status.Derive(got) != status.GroupNeedsInput || got.Interaction != status.InteractionPermission {
		t.Fatalf("native human decision hidden by concurrent work: %+v", got)
	}
	request.Kind = CodexDecisionResolved
	if err := codexApplyOne(e, feed, request); err != nil {
		t.Fatal(err)
	}
	if got := codexPublishedStatus(e); status.Derive(got) != status.GroupWorking {
		t.Fatalf("resolved decision retained stale attention: %+v", got)
	}
}

func TestCodexOwnerInputContaminationSurvivesRootReset(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	terminal := trace[len(trace)-1]
	applyCodexTrace(t, e, feed, trace[:len(trace)-1])
	codexSpawnChild(t, e, feed, "", terminal.TurnID, "child", "child-turn")
	if err := codexApplyOne(e, feed, terminal); err != nil {
		t.Fatal(err)
	}
	if err := e.ObserveExecutionOwnerRevision("s1", 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := codexApplyOne(e, feed, CodexExecutionEvent{ActorThreadID: "child", Kind: CodexTurnCompleted, TurnID: "child-turn", Cause: CodexTerminalCompleted}); err != nil {
		t.Fatal(err)
	}
	for _, event := range trace {
		event.TurnID = "next-root-turn"
		if err := codexApplyOne(e, feed, event); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _ := e.ExecutionSnapshot("s1")
	if !snapshot.Codex.UnknownCustody {
		t.Fatal("owner child input attribution was revived by next root turn")
	}
	if _, ok := e.CodexQuotaCandidate("s1"); ok {
		t.Fatal("contaminated child graph qualified a later root turn")
	}
}

func TestCodexCustodyUnsafeUsesNativeEvidence(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	if !e.ExecutionCustodyUnsafe("s1") {
		t.Fatal("missing native turn proved a safe boundary")
	}
	trace := codexExecutionTrace(t, "quota")
	applyCodexTrace(t, e, feed, trace)
	if e.ExecutionCustodyUnsafe("s1") {
		t.Fatal("complete native terminal remained unsafe")
	}
	if err := e.InvalidateExecution("s1"); err != nil {
		t.Fatal(err)
	}
	if !e.ExecutionCustodyUnsafe("s1") {
		t.Fatal("observation loss proved a safe boundary")
	}
	if got := codexPublishedStatus(e); got.Turn != status.TurnUnknown {
		t.Fatalf("gap was invisible until another frame: %+v", got)
	}
}

func TestCodexOwnerInputSerializesConcurrentWritersAndRetainsFailedFence(t *testing.T) {
	e, _ := codexExecutionEngine(t)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.ObserveExecutionOwnerInput("s1"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	snapshot, _ := e.ExecutionSnapshot("s1")
	if snapshot.OwnerRevision != 17 {
		t.Fatalf("concurrent writers lost input fences: %d", snapshot.OwnerRevision)
	}
	e.storeExecution = func(persist.ExecutionRecord) error { return errors.New("synthetic durable refusal") }
	if err := e.ObserveExecutionOwnerInput("s1"); err == nil {
		t.Fatal("failed durability granted input effect")
	}
	snapshot, _ = e.ExecutionSnapshot("s1")
	if snapshot.OwnerRevision != 18 || snapshot.Codex.Incomplete != "persistence_failure" {
		t.Fatal("durability failure lost observed owner fence")
	}
}

func TestCodexCustodySurvivesTypedIdleAndRestore(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	terminal := trace[len(trace)-1]
	applyCodexTrace(t, e, feed, trace[:len(trace)-1])
	codexSpawnChild(t, e, feed, "", terminal.TurnID, "child", "child-turn")
	if err := e.ApplyTypedEvent("s1", "turn/completed", nil); err != nil {
		t.Fatal(err)
	}
	if got := codexPublishedStatus(e); got.Turn != status.TurnActive {
		t.Fatalf("typed root completion hid child: %+v", got)
	}
	snapshot, _ := e.ExecutionSnapshot("s1")
	if err := e.RestoreExecution("s1", snapshot); err != nil {
		t.Fatal(err)
	}
	if got := codexPublishedStatus(e); got.Turn != status.TurnUnknown {
		t.Fatalf("restored native custody looked idle: %+v", got)
	}
}

func TestCodexCustodyHumanPrioritySurvivesUnknownAndTick(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	terminal := trace[len(trace)-1]
	applyCodexTrace(t, e, feed, trace[:len(trace)-1])
	codexSpawnChild(t, e, feed, "", terminal.TurnID, "child", "child-turn")
	if err := codexApplyOne(e, feed, CodexExecutionEvent{ActorThreadID: "child", Kind: CodexDecisionRequested, TurnID: "child-turn", RequestID: `"question"`, Decision: CodexQuestion}); err != nil {
		t.Fatal(err)
	}
	e.now = func() time.Time { return time.Now().Add(time.Hour) }
	e.sampler = func(int) (float64, error) { return 0, nil }
	e.Tick()
	if err := e.InvalidateExecution("s1"); err != nil {
		t.Fatal(err)
	}
	e.Tick()
	got := codexPublishedStatus(e)
	if got.Turn != status.TurnUnknown || got.Interaction != status.InteractionPrompt || status.Derive(got) != status.GroupNeedsInput {
		t.Fatalf("unknown/tick hid exact human question: %+v", got)
	}
}

func TestCodexCustodyDurabilityFailureSurvivesNextTurn(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	applyCodexTrace(t, e, feed, trace)
	e.storeExecution = func(persist.ExecutionRecord) error { return errors.New("synthetic durable refusal") }
	if err := e.ObserveExecutionOwnerInput("s1"); err == nil {
		t.Fatal("owner input accepted without durable custody")
	}
	e.storeExecution = func(persist.ExecutionRecord) error { return nil }
	for _, event := range trace {
		event.TurnID = "after-durability-failure"
		if err := codexApplyOne(e, feed, event); err != nil {
			t.Fatal(err)
		}
	}
	if !e.ExecutionCustodyUnsafe("s1") {
		t.Fatal("later terminal erased unresolved durability failure")
	}
	if got := codexPublishedStatus(e); got.Turn != status.TurnUnknown {
		t.Fatalf("unresolved durability failure looked idle: %+v", got)
	}
}

func TestCodexCustodyFailureBeforeFeedCannotLookIdle(t *testing.T) {
	e := New(Config{StoreExecution: func(persist.ExecutionRecord) error { return errors.New("synthetic durable refusal") }})
	initial := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	identity := ExecutionIdentity{Incarnation: "shim-1", ConversationID: "thread-1", ObservedFromStart: true, OwnerRevision: 1}
	if err := e.RegisterExecutionSession("s1", "", 1, codex.New().SignalSources(), initial, identity, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.ObserveExecutionOwnerInput("s1"); err == nil {
		t.Fatal("owner input accepted without durable custody")
	}
	e.storeExecution = func(persist.ExecutionRecord) error { return nil }
	if err := e.ApplyTypedEvent("s1", "turn/completed", nil); err != nil {
		t.Fatal(err)
	}
	if got := codexPublishedStatus(e); got.Turn != status.TurnUnknown {
		t.Fatalf("pre-feed persistence failure looked idle: %+v", got)
	}
}

func TestCodexCustodyStartupPublicationForcesRestoredStatus(t *testing.T) {
	first, feed := codexExecutionEngine(t)
	applyCodexTrace(t, first, feed, codexExecutionTrace(t, "quota"))
	saved, err := first.ExecutionSnapshot("s1")
	if err != nil {
		t.Fatal(err)
	}
	var restored *Engine
	published := 0
	restored = New(Config{Emit: func(id string, state status.Status) {
		published++
		if state.Turn != status.TurnUnknown {
			t.Fatalf("startup published stale idle state: %+v", state)
		}
		// The status sink may read execution evidence; the engine mutex must be free.
		if _, err := restored.ExecutionSnapshot(id); err != nil {
			t.Fatal(err)
		}
	}})
	identity := ExecutionIdentity{Incarnation: saved.Incarnation, ConversationID: saved.ConversationID, OwnerRevision: saved.OwnerRevision}
	idle := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	if err := restored.RegisterExecutionSession("s1", "", 1, nil, idle, identity, &saved); err != nil {
		t.Fatal(err)
	}
	restored.mu.Lock()
	session := restored.sessions["s1"]
	restored.mu.Unlock()
	session.emitMu.Lock()
	restored.publishCodexCustody("s1", session)
	session.emitMu.Unlock()
	if published != 0 {
		t.Fatal("fixture did not reproduce changed-only startup gap")
	}
	restored.PublishCodexExecutionStatus("s1")
	if published != 1 {
		t.Fatalf("restored status was not force-published once: %d", published)
	}
}
