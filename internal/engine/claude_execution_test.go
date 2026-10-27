package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func claudeControlFrame() claudeinput.ControlEnvelope {
	return claudeinput.ControlEnvelope{Event: claudeinput.NativeControlRuntime, RequiredRuntime: claudeinput.NativeControlRuntime, Version: 1, SessionID: "local", Token: strings.Repeat("a", 64), DescriptorGeneration: strings.Repeat("b", 32), ShimPID: 123, ShimStartTime: 456, Worker: claudeinput.Worker{NativeSessionID: "native", Generation: "guard", Version: "2.1.310"}, Sequence: 1, ReportHash: strings.Repeat("c", 64), Invocation: 1, NativeEvent: "turn.step", Phase: "entered", Context: json.RawMessage(`{"turn_id":"turn","index":0,"message_count":1,"origin":{"plugin":"engine","tier":"core"}}`), Data: json.RawMessage(`{}`)}
}
func TestClaudeControlRequiresDurableExecution(t *testing.T) {
	for _, kind := range []string{"missing-store", "disabled", "legacy-record", "ended"} {
		t.Run(kind, func(t *testing.T) {
			cfg := Config{}
			if kind != "missing-store" {
				cfg.StoreExecution = func(persist.ExecutionRecord) error { return nil }
			}
			e := New(cfg)
			identity := ExecutionIdentity{Incarnation: "123:456", ConversationID: "native", ClaudeNativeControl: true}
			if kind == "legacy-record" {
				identity.ClaudeNativeControl = false
			}
			if kind == "disabled" {
				e.RegisterSession("local", strings.Repeat("a", 64), 123, nil, status.Status{Process: status.ProcessRunning})
			} else {
				if err := e.RegisterExecutionSession("local", strings.Repeat("a", 64), 123, nil, status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}, identity, nil); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "ended" {
				e.EndSession("local")
			}
			if err := e.ApplyClaudeControlEnvelope("local", claudeControlFrame()); err == nil {
				t.Fatal("unqualified/missing durable execution accepted control")
			}
		})
	}
}
func TestClaudeControlRegistrationAlwaysUnknown(t *testing.T) {
	e := New(Config{StoreExecution: func(persist.ExecutionRecord) error { return nil }})
	if err := e.RegisterExecutionSession("local", strings.Repeat("a", 64), 123, nil, status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}, ExecutionIdentity{Incarnation: "123:456", ConversationID: "native", ObservedFromStart: true, ClaudeNativeControl: true}, nil); err != nil {
		t.Fatal(err)
	}
	record, err := e.ExecutionSnapshot("local")
	if err != nil || record.Claude == nil || record.Version != persist.ClaudeExecutionVersion || !record.UnknownActors || record.Incomplete != "claude_observation_subset" {
		t.Fatalf("registration invented complete scope: %+v %v", record, err)
	}
	if !record.Claude.StartupPrebootstrapUnknown || record.Claude.ObservationComplete {
		t.Fatal("birth flag created startup coverage")
	}
}

type claudeScript struct {
	t        *testing.T
	e        *Engine
	sequence uint64
}

func (s *claudeScript) frame(event, phase string, inv uint64, context, data any) uint64 {
	s.t.Helper()
	s.sequence++
	if inv == 0 {
		inv = s.sequence
	}
	frame := claudeControlFrame()
	frame.Sequence = s.sequence
	frame.Invocation = inv
	frame.NativeEvent = event
	frame.Phase = phase
	frame.Context, _ = json.Marshal(context)
	frame.Data, _ = json.Marshal(data)
	if err := s.e.ApplyClaudeControlEnvelope("local", frame); err != nil {
		s.t.Fatalf("frame %d %s/%s: %v", s.sequence, event, phase, err)
	}
	return inv
}
func coreContext(turn string) map[string]any {
	c := map[string]any{"origin": map[string]string{"plugin": "engine", "tier": "core"}}
	if turn != "" {
		c["turn_id"] = turn
	}
	return c
}
func claudeTestScript(t *testing.T) (*claudeScript, *persist.Store) {
	return claudeTestScriptInitial(t, status.InteractionNone)
}
func claudeTestScriptInitial(t *testing.T, initial status.Interaction) (*claudeScript, *persist.Store) {
	t.Helper()
	store, err := persist.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := New(Config{StoreExecution: store.SaveExecution})
	if err := e.RegisterExecutionSession("local", strings.Repeat("a", 64), 123, claudeSignalSources(t), status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: initial}, ExecutionIdentity{Incarnation: "123:456", ConversationID: "native", ClaudeNativeControl: true}, nil); err != nil {
		t.Fatal(err)
	}
	return &claudeScript{t: t, e: e}, store
}
func (s *claudeScript) question() string {
	ctx := coreContext("main")
	step := coreContext("main")
	step["index"] = 0
	step["message_count"] = 1
	i := s.frame("turn.start", "entered", 0, ctx, map[string]any{})
	s.frame("turn.start", "returned", i, ctx, map[string]any{"turn_id": "main"})
	i = s.frame("turn.step", "entered", 0, step, map[string]any{})
	s.frame("turn.step", "returned", i, step, map[string]any{"turn_id": "main", "index": 0, "stop_reason": "tool_use", "tool_uses": []any{map[string]string{"id": "question", "name": "AskUserQuestion"}}})
	call := coreContext("main")
	call["tool"] = "AskUserQuestion"
	call["tool_use_id"] = "question"
	call["index"] = 0
	s.frame("tool.call", "entered", 0, call, map[string]any{})
	check := s.frame("tool.check", "entered", 0, call, map[string]any{"approver": "person"})
	s.frame("tool.check", "returned", check, call, map[string]any{"approver": "person", "decision": "ask"})
	return persist.ClaudeCustodyKey("", "main", "question")
}
func TestClaudeControlHumanGuardRequiresExactPendingUI(t *testing.T) {
	s, store := claudeTestScript(t)
	key := s.question()
	before, _ := s.e.ExecutionSnapshot("local")
	if len(before.Claude.Calls) != 1 || before.Claude.Calls[key].HumanRequestID != "" {
		t.Fatal("pending tool/ask-person missing or promoted to human guard")
	}
	ui := map[string]any{"request_id": "question", "question_count": 1, "component": "AskUserQuestion", "surface": "terminal"}
	inv := s.frame("ui.render", "entered", 0, coreContext(""), ui)
	s.frame("ui.render", "returned", inv, coreContext(""), ui)
	saved, err := store.LoadExecution("local")
	if err != nil || saved.Claude.Calls[key].HumanRequestID != "question" {
		t.Fatalf("live human guard not durably retained independently of native history: %+v %v", saved.Claude, err)
	}
	after := New(Config{StoreExecution: store.SaveExecution})
	if err := after.RegisterExecutionSession("local", strings.Repeat("a", 64), 123, claudeSignalSources(t), status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}, ExecutionIdentity{Incarnation: "123:456", ConversationID: "native", ClaudeNativeControl: true}, &saved); err != nil {
		t.Fatal(err)
	}
	restored, _ := after.ExecutionSnapshot("local")
	if restored.Claude.Calls[key].HumanRequestID != "question" || !restored.UnknownActors {
		t.Fatal("restored record lost exact human exclusion")
	}
}
func TestClaudeControlCancellationRequiresReturnedAbortedWitness(t *testing.T) {
	s, _ := claudeTestScript(t)
	context := coreContext("cancel-turn")
	data := map[string]any{"reason": "aborted", "is_aborted": true}
	inv := s.frame("turn.complete", "entered", 0, context, data)
	prefix, _ := s.e.ExecutionSnapshot("local")
	if len(prefix.Claude.Cancelled) != 0 {
		t.Fatal("entry-only created cancellation")
	}
	s.frame("turn.complete", "returned", inv, context, data)
	record, _ := s.e.ExecutionSnapshot("local")
	if len(record.Claude.Cancelled) != 1 {
		t.Fatal("matching explicit native aborted witness not retained")
	}
}
func knownChildTrace(s *claudeScript, completeParent bool) {
	knownChildTraceFlags(s, completeParent, nil)
}
func knownChildTraceFlags(s *claudeScript, completeParent bool, flags map[string]any) {
	parent := coreContext("parent")
	start := s.frame("turn.start", "entered", 0, parent, map[string]any{})
	s.frame("turn.start", "returned", start, parent, map[string]any{"turn_id": "parent"})
	call := coreContext("parent")
	call["tool"] = "Agent"
	call["tool_use_id"] = "agent-call"
	callInv := s.frame("tool.call", "entered", 0, call, map[string]any{})
	spawn := coreContext("")
	spawn["tool_use_id"] = "agent-call"
	data := map[string]any{"background": true, "fork": false, "subagent_type": "general-purpose", "provider": map[string]string{"plugin": "engine", "tier": "core"}, "is_teammate_present": false, "workflow_present": false, "resume_present": false, "name_present": false}
	for key, value := range flags {
		data[key] = value
	}
	spawnInv := s.frame("agent.spawn", "entered", 0, spawn, data)
	child := coreContext("child-turn")
	child["agent_id"] = "child"
	child["index"] = 0
	child["message_count"] = 1
	s.frame("turn.step", "entered", 0, child, map[string]any{}) // Real native ordering: child before spawn return.
	data["agent_id"] = "child"
	s.frame("agent.spawn", "returned", spawnInv, spawn, data)
	s.frame("tool.call", "returned", callInv, call, map[string]any{"agent_status": "async_launched", "agent_id": "child"})
	if completeParent {
		complete := map[string]any{"reason": "answer", "is_aborted": false}
		inv := s.frame("turn.complete", "entered", 0, parent, complete)
		s.frame("turn.complete", "returned", inv, parent, complete)
	}
}
func TestClaudeControlKnownChildSurvivesParentAnswer(t *testing.T) {
	s, _ := claudeTestScript(t)
	knownChildTrace(s, true)
	record, _ := s.e.ExecutionSnapshot("local")
	if len(record.Claude.Children) != 1 || record.Claude.Children["child"].ToolID != "agent-call" || !record.Claude.Turns[persist.ClaudeCustodyKey("child", "child-turn")].ActiveStep {
		t.Fatalf("parent answer lost exact known live child: %+v", record.Claude)
	}
	if !record.UnknownActors || record.Incomplete != persist.ClaudeObservationSubset || record.Claude.ObservationComplete {
		t.Fatal("known child falsely completed actor census")
	}
}

func TestClaudeControlPersistenceReplayAndConflict(t *testing.T) {
	s, store := claudeTestScript(t)
	frame := claudeControlFrame()
	if err := s.e.ApplyClaudeControlEnvelope("local", frame); err != nil {
		t.Fatal(err)
	}
	saved, err := store.LoadExecution("local")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), frame.Token) {
		t.Fatal("credential retained in execution snapshot")
	}
	after := New(Config{StoreExecution: store.SaveExecution})
	if err := after.RegisterExecutionSession("local", frame.Token, 123, claudeSignalSources(t), status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}, ExecutionIdentity{Incarnation: "123:456", ConversationID: "native", ClaudeNativeControl: true}, &saved); err != nil {
		t.Fatal(err)
	}
	if err := after.ApplyClaudeControlEnvelope("local", frame); err != nil {
		t.Fatal("exact crash replay not idempotent", err)
	}
	changed := frame
	changed.Context = json.RawMessage(`{"turn_id":"foreign","index":0,"message_count":1,"origin":{"plugin":"engine","tier":"core"}}`)
	if err := after.ApplyClaudeControlEnvelope("local", changed); err == nil {
		t.Fatal("opaque report_hash hid changed native context")
	}
	record, _ := after.ExecutionSnapshot("local")
	if record.Claude.Sequence != 1 || record.Claude.Failure == "" || !record.UnknownActors {
		t.Fatal("conflict lost prefix/unknown", record.Claude)
	}
}
func TestClaudeControlPersistenceFailureAndStopFence(t *testing.T) {
	t.Run("ENOSPC-retry", func(t *testing.T) {
		fail := true
		store, err := persist.NewStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		e := New(Config{StoreExecution: func(r persist.ExecutionRecord) error {
			if fail {
				return syscall.ENOSPC
			}
			return store.SaveExecution(r)
		}})
		if err := e.RegisterExecutionSession("local", strings.Repeat("a", 64), 123, nil, status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}, ExecutionIdentity{Incarnation: "123:456", ConversationID: "native", ClaudeNativeControl: true}, nil); err != nil {
			t.Fatal(err)
		}
		if err := e.ApplyClaudeControlEnvelope("local", claudeControlFrame()); !errors.Is(err, syscall.ENOSPC) {
			t.Fatal("failed store ACKed", err)
		}
		fail = false
		if err := e.ApplyClaudeControlEnvelope("local", claudeControlFrame()); err != nil {
			t.Fatal("same retained fact could not become durable", err)
		}
		saved, err := store.LoadExecution("local")
		if err != nil || saved.Claude.Sequence != 1 || saved.Claude.Failure != "persistence_failure" {
			t.Fatal("retry erased failure/receipt", err, saved.Claude)
		}
	})
	t.Run("Stop-held-store", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		emits := 0
		e := New(Config{StoreExecution: func(persist.ExecutionRecord) error { close(entered); <-release; return nil }, Emit: func(string, status.Status) { emits++ }})
		if err := e.RegisterExecutionSession("local", strings.Repeat("a", 64), 123, nil, status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}, ExecutionIdentity{Incarnation: "123:456", ConversationID: "native", ClaudeNativeControl: true}, nil); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- e.ApplyClaudeControlEnvelope("local", claudeControlFrame()) }()
		<-entered
		stopped := make(chan struct{})
		go func() { e.EndSession("local"); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			close(release)
			<-done
			t.Fatal("Stop waited behind execution persistence")
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal("genuine pre-Stop fact could not retain", err)
		}
		if emits != 0 {
			t.Fatal("late commit resurrected retired display")
		}
		if err := e.ApplyClaudeControlEnvelope("local", claudeControlFrame()); err == nil {
			t.Fatal("late commit restored input/effect admission")
		}
	})
}
func TestClaudeControlPartialSnapshotMapsRejected(t *testing.T) {
	s, _ := claudeTestScript(t)
	if err := s.e.ApplyClaudeControlEnvelope("local", claudeControlFrame()); err != nil {
		t.Fatal(err)
	}
	record, _ := s.e.ExecutionSnapshot("local")
	for _, which := range []string{"invocations", "calls", "turns", "children", "cancelled"} {
		copy := record
		copy.Claude = cloneClaudeExecution(record.Claude)
		switch which {
		case "invocations":
			copy.Claude.Invocations = nil
		case "calls":
			copy.Claude.Calls = nil
		case "turns":
			copy.Claude.Turns = nil
		case "children":
			copy.Claude.Children = nil
		case "cancelled":
			copy.Claude.Cancelled = nil
		}
		if copy.Validate() == nil {
			t.Fatal("partial snapshot treated missing custody as empty", which)
		}
	}
}

func controlLease(t *testing.T, e *Engine) claudeinput.ObservationLease {
	t.Helper()
	record, err := e.ExecutionSnapshot("local")
	if err != nil {
		t.Fatal(err)
	}
	c := record.Claude
	return claudeinput.ObservationLease{Generation: c.DescriptorGeneration, Worker: claudeinput.Worker{NativeSessionID: c.NativeSessionID, Generation: c.ObserverEpoch, Version: "2.1.310"}, Captured: c.Sequence, Durable: c.Sequence, Live: true, SinkAvailable: true, LeaseRemainingMS: 3000}
}
func controlStatus(e *Engine) status.Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sessions["local"].status
}
func TestClaudeControlLeaseBoundsKnownWorking(t *testing.T) {
	s, _ := claudeTestScript(t)
	knownChildTrace(s, true)
	if controlStatus(s.e).Turn == status.TurnActive {
		t.Fatal("durable child snapshot invented live lease")
	}
	view := controlLease(t, s.e)
	if err := s.e.SetClaudeObservationLease("local", view, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if controlStatus(s.e).Turn != status.TurnActive {
		t.Fatal("exact live known child lost after parent answer")
	}
	for _, kind := range []string{"expired", "revoked", "epoch", "lag", "sink"} {
		changed := view
		elapsed := time.Duration(0)
		switch kind {
		case "expired":
			elapsed = 3100 * time.Millisecond
		case "revoked":
			changed.Revoked = true
		case "epoch":
			changed.Worker.Generation = "foreign"
		case "lag":
			changed.Captured++
		case "sink":
			changed.SinkAvailable = false
		}
		if err := s.e.SetClaudeObservationLease("local", changed, elapsed); err != nil {
			t.Fatal(err)
		}
		if controlStatus(s.e).Turn == status.TurnActive {
			t.Fatal("stale producer retained affirmative Working", kind)
		}
		if err := s.e.SetClaudeObservationLease("local", view, 0); err != nil {
			t.Fatal(err)
		}
	}
}
func TestClaudeControlHumanProjectionSurvivesLegacyAndSettlesOwnPrompt(t *testing.T) {
	s, _ := claudeTestScript(t)
	key := s.question()
	ui := map[string]any{"request_id": "question", "question_count": 1, "component": "AskUserQuestion", "surface": "terminal"}
	inv := s.frame("ui.render", "entered", 0, coreContext(""), ui)
	s.frame("ui.render", "returned", inv, coreContext(""), ui)
	if got := controlStatus(s.e); got.Interaction != status.InteractionPrompt || !got.Attention {
		t.Fatal("human guard not projected", got)
	}
	if err := s.e.HandleCallback(Callback{SessionID: "local", Token: strings.Repeat("a", 64), Sequence: 1, Event: "Stop"}); err != nil {
		t.Fatal(err)
	}
	s.e.OnOutput("local", claudeIdleScreen())
	if got := controlStatus(s.e); got.Interaction != status.InteractionPrompt || !got.Attention {
		t.Fatal("legacy stop/grid erased durable human guard", got)
	}
	record, _ := s.e.ExecutionSnapshot("local")
	call := record.Claude.Calls[key]
	context := coreContext("main")
	context["index"] = 0
	context["tool"] = "AskUserQuestion"
	context["tool_use_id"] = "question"
	s.frame("tool.call", "returned", call.Invocation, context, map[string]any{})
	if got := controlStatus(s.e); got.Interaction != status.InteractionNone || got.Attention || got.Turn == status.TurnIdle {
		t.Fatal("exact result left owned prompt or invented complete idle", got)
	}
}

func TestClaudeControlFailedEpochCannotSettlePreauthenticatedGuards(t *testing.T) {
	s, _ := claudeTestScript(t)
	cancel := map[string]any{"reason": "aborted", "is_aborted": true}
	inv := s.frame("turn.complete", "entered", 0, coreContext("cancel-old"), cancel)
	s.frame("turn.complete", "returned", inv, coreContext("cancel-old"), cancel)
	key := s.question()
	ui := map[string]any{"request_id": "question", "question_count": 1, "component": "AskUserQuestion", "surface": "terminal"}
	inv = s.frame("ui.render", "entered", 0, coreContext(""), ui)
	s.frame("ui.render", "returned", inv, coreContext(""), ui)
	before, _ := s.e.ExecutionSnapshot("local")
	call := before.Claude.Calls[key]
	terminal := claudeControlFrame()
	terminal.Sequence = s.sequence + 1
	terminal.Invocation = call.Invocation
	terminal.NativeEvent = "tool.call"
	terminal.Phase = "returned"
	ctx := coreContext("main")
	ctx["index"] = 0
	ctx["tool"] = "AskUserQuestion"
	ctx["tool_use_id"] = "question"
	terminal.Context, _ = json.Marshal(ctx)
	terminal.Data = json.RawMessage(`{}`)
	if err := s.e.AuthenticateClaudeControl(terminal); err != nil {
		t.Fatal("preauthentication prerequisite", err)
	}
	conflict := claudeControlFrame()
	conflict.Sequence = s.sequence
	conflict.Invocation = s.sequence
	if err := s.e.ApplyClaudeControlEnvelope("local", conflict); err == nil {
		t.Fatal("conflict did not invalidate epoch")
	}
	if err := s.e.ApplyClaudeControlEnvelope("local", terminal); err == nil {
		t.Error("preauthenticated control bypassed failed-feed Apply fence")
	}
	after, _ := s.e.ExecutionSnapshot("local")
	if after.Claude.Sequence != before.Claude.Sequence || after.Claude.Calls[key].HumanRequestID != "question" || len(after.Claude.Cancelled) != 1 {
		t.Fatal("failed epoch settled human/tool/cancel custody", after.Claude)
	}
}

func TestClaudeControlCancelledTurnCannotRegainWorking(t *testing.T) {
	s, _ := claudeTestScript(t)
	data := map[string]any{"reason": "aborted", "is_aborted": true}
	context := coreContext("cancelled")
	inv := s.frame("turn.complete", "entered", 0, context, data)
	s.frame("turn.complete", "returned", inv, context, data)
	step := coreContext("cancelled")
	step["index"] = 1
	step["message_count"] = 2
	s.frame("turn.step", "entered", 0, step, map[string]any{})
	if err := s.e.SetClaudeObservationLease("local", controlLease(t, s.e), 0); err != nil {
		t.Fatal(err)
	}
	if controlStatus(s.e).Turn == status.TurnActive {
		t.Error("late step resurrected exact cancelled turn")
	}
	newer := coreContext("new-task")
	newer["index"] = 0
	newer["message_count"] = 1
	s.frame("turn.step", "entered", 0, newer, map[string]any{})
	if err := s.e.SetClaudeObservationLease("local", controlLease(t, s.e), 0); err != nil {
		t.Fatal(err)
	}
	if controlStatus(s.e).Turn != status.TurnActive {
		t.Fatal("old cancel blanket-blocked distinct newer native task")
	}
	record, _ := s.e.ExecutionSnapshot("local")
	if len(record.Claude.Cancelled) != 1 {
		t.Fatal("new task erased old cancellation intent")
	}
}

func TestClaudeControlPendingWriteAndLossWithdrawImmediately(t *testing.T) {
	s, store := claudeTestScript(t)
	knownChildTrace(s, true)
	if err := s.e.SetClaudeObservationLease("local", controlLease(t, s.e), 0); err != nil {
		t.Fatal(err)
	}
	if controlStatus(s.e).Turn != status.TurnActive {
		t.Fatal("positive fixture did not become Working")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	public := make(chan status.Status, 16)
	s.e.emit = func(_ string, next status.Status) { public <- next }
	var once sync.Once
	s.e.storeExecution = func(record persist.ExecutionRecord) error {
		once.Do(func() { close(entered) })
		<-release
		return store.SaveExecution(record)
	}
	frame := claudeControlFrame()
	frame.Sequence = s.sequence + 1
	frame.Invocation = frame.Sequence
	frame.NativeEvent = "turn.start"
	frame.Context, _ = json.Marshal(coreContext("later"))
	done := make(chan error, 1)
	go func() { done <- s.e.ApplyClaudeControlEnvelope("local", frame) }()
	<-entered
	if controlStatus(s.e).Turn == status.TurnActive {
		t.Error("new uncommitted prefix retained old caught-up Working")
	}
	select {
	case got := <-public:
		if got.Turn != status.TurnUnknown {
			t.Error("public negative was not Unknown", got)
		}
	case <-time.After(time.Second):
		t.Error("public Working survived held execution storage")
	}

	revoked := controlLease(t, s.e)
	revoked.Revoked = true
	returned := make(chan error, 1)
	go func() { returned <- s.e.SetClaudeObservationLease("local", revoked, 0) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("lease loss blocked behind persistence")
	}
	if controlStatus(s.e).Turn == status.TurnActive {
		t.Error("revoked producer still projected Working during held write")
	}
	gapDone := make(chan error, 1)
	go func() { gapDone <- s.e.InvalidateExecution("local") }()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-gapDone; err != nil {
		t.Fatal(err)
	}
	if controlStatus(s.e).Turn == status.TurnActive {
		t.Fatal("late persistence resurrected Working")
	}
	close(public)
	for got := range public {
		if got.Turn == status.TurnActive {
			t.Fatal("late earlier writer publicly resurrected Working", got)
		}
	}

	saved, err := store.LoadExecution("local")
	if err != nil || saved.Claude.Failure == "" || len(saved.Claude.Children) != 1 {
		t.Fatal("loss lost durable reason/child custody", saved.Claude, err)
	}
}

func TestClaudeControlRestoreSettlesOnlyOwnedPrompt(t *testing.T) {
	for _, base := range []status.Interaction{status.InteractionNone, status.InteractionPermission, status.InteractionError} {
		t.Run(string(base), func(t *testing.T) {
			s, store := claudeTestScriptInitial(t, base)
			key := s.question()
			ui := map[string]any{"request_id": "question", "question_count": 1, "component": "AskUserQuestion", "surface": "terminal"}
			inv := s.frame("ui.render", "entered", 0, coreContext(""), ui)
			s.frame("ui.render", "returned", inv, coreContext(""), ui)
			saved, err := store.LoadExecution("local")
			if err != nil {
				t.Fatal(err)
			}
			// A top-level projected status copy must not become the independent base.
			saved.Interaction = status.InteractionPrompt
			after := New(Config{StoreExecution: store.SaveExecution})
			if err := after.RegisterExecutionSession("local", strings.Repeat("a", 64), 123, claudeSignalSources(t), status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}, ExecutionIdentity{Incarnation: "123:456", ConversationID: "native", ClaudeNativeControl: true}, &saved); err != nil {
				t.Fatal(err)
			}
			s.e = after
			call := saved.Claude.Calls[key]
			context := coreContext("main")
			context["index"] = 0
			context["tool"] = "AskUserQuestion"
			context["tool_use_id"] = "question"
			s.frame("tool.call", "returned", call.Invocation, context, map[string]any{})
			if got := controlStatus(after); got.Interaction != base {
				t.Fatalf("restored control prompt replaced independent %s base: %+v", base, got)
			}
		})
	}
}

func TestClaudeControlWrongOrLateUIDoesNotCreateHumanGuard(t *testing.T) {
	for _, kind := range []string{"wrong-request", "foreign-turn", "foreign-actor", "late-ui", "ask-person-only"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := claudeTestScript(t)
			key := s.question()
			request := "question"
			if kind == "foreign-turn" || kind == "foreign-actor" {
				request = "foreign-question"
				context := coreContext("main")
				if kind == "foreign-turn" {
					context["turn_id"] = "foreign"
				} else {
					context["agent_id"] = "foreign"
				}
				context["tool"] = "AskUserQuestion"
				context["tool_use_id"] = request
				s.frame("tool.call", "entered", 0, context, map[string]any{})
			}
			if kind == "wrong-request" {
				request = "different"
			}
			if kind == "late-ui" {
				record, _ := s.e.ExecutionSnapshot("local")
				call := record.Claude.Calls[key]
				context := coreContext("main")
				context["index"] = 0
				context["tool"] = "AskUserQuestion"
				context["tool_use_id"] = "question"
				s.frame("tool.call", "returned", call.Invocation, context, map[string]any{})
			}
			if kind != "ask-person-only" {
				ui := map[string]any{"request_id": request, "question_count": 1, "component": "AskUserQuestion", "surface": "terminal"}
				inv := s.frame("ui.render", "entered", 0, coreContext(""), ui)
				s.frame("ui.render", "returned", inv, coreContext(""), ui)
			}
			record, _ := s.e.ExecutionSnapshot("local")
			for _, call := range record.Claude.Calls {
				if call.HumanRequestID != "" {
					t.Fatal("ambiguous/stale UI created affirmative human guard", kind)
				}
			}
			if controlStatus(s.e).Interaction == status.InteractionPrompt {
				t.Fatal("pending/query-only state promoted to NeedInput", kind)
			}
		})
	}
}
func TestClaudeControlGenericOrThrownCompletionNotCancellation(t *testing.T) {
	for _, kind := range []string{"answer", "error", "threw"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := claudeTestScript(t)
			data := map[string]any{"reason": kind, "is_aborted": false}
			if kind == "threw" {
				data["reason"] = "aborted"
				data["is_aborted"] = true
			}
			ctx := coreContext("turn")
			inv := s.frame("turn.complete", "entered", 0, ctx, data)
			if kind == "threw" {
				s.frame("turn.complete", "threw", inv, ctx, map[string]any{})
			} else {
				s.frame("turn.complete", "returned", inv, ctx, data)
			}
			record, _ := s.e.ExecutionSnapshot("local")
			if len(record.Claude.Cancelled) != 0 {
				t.Fatal("non-aborted-return created cancellation", kind)
			}
		})
	}
}
func TestClaudeControlUnqualifiedChildRemainsUnknown(t *testing.T) {
	variants := map[string]map[string]any{"workflow": {"workflow_present": true}, "resume": {"resume_present": true, "resume_agent_id": "child"}, "teammate": {"is_teammate_present": true}, "fork": {"fork": true}, "foreground": {"background": false}, "plugin-definition": {"provider": map[string]string{"plugin": "unqualified", "tier": "user"}}}
	for kind, flags := range variants {
		t.Run(kind, func(t *testing.T) {
			s, _ := claudeTestScript(t)
			knownChildTraceFlags(s, true, flags)
			if err := s.e.SetClaudeObservationLease("local", controlLease(t, s.e), 0); err != nil {
				t.Fatal(err)
			}
			record, _ := s.e.ExecutionSnapshot("local")
			if len(record.Claude.Children) != 0 || controlStatus(s.e).Turn == status.TurnActive || !record.UnknownActors {
				t.Fatal("unqualified actor became a known working child", kind)
			}
		})
	}
}
func TestClaudeControlActorStepPredatingSpawnIsNotFreshChild(t *testing.T) {
	s, _ := claudeTestScript(t)
	old := coreContext("old-child-turn")
	old["agent_id"] = "child"
	old["index"] = 0
	old["message_count"] = 1
	s.frame("turn.step", "entered", 0, old, map[string]any{})
	parent := coreContext("parent")
	start := s.frame("turn.start", "entered", 0, parent, map[string]any{})
	s.frame("turn.start", "returned", start, parent, map[string]any{"turn_id": "parent"})
	call := coreContext("parent")
	call["tool"] = "Agent"
	call["tool_use_id"] = "spawn"
	callInv := s.frame("tool.call", "entered", 0, call, map[string]any{})
	spawn := coreContext("")
	spawn["tool_use_id"] = "spawn"
	data := map[string]any{"background": true, "fork": false, "subagent_type": "general-purpose", "provider": map[string]string{"plugin": "engine", "tier": "core"}, "is_teammate_present": false, "workflow_present": false, "resume_present": false, "name_present": false}
	inv := s.frame("agent.spawn", "entered", 0, spawn, data)
	data["agent_id"] = "child"
	s.frame("agent.spawn", "returned", inv, spawn, data)
	s.frame("tool.call", "returned", callInv, call, map[string]any{"agent_status": "async_launched", "agent_id": "child"})
	if err := s.e.SetClaudeObservationLease("local", controlLease(t, s.e), 0); err != nil {
		t.Fatal(err)
	}
	if controlStatus(s.e).Turn == status.TurnActive {
		t.Error("old actor step was reassigned to fresh spawn")
	}
	fresh := coreContext("fresh-child-turn")
	fresh["agent_id"] = "child"
	fresh["index"] = 0
	fresh["message_count"] = 1
	s.frame("turn.step", "entered", 0, fresh, map[string]any{})
	if err := s.e.SetClaudeObservationLease("local", controlLease(t, s.e), 0); err != nil {
		t.Fatal(err)
	}
	if controlStatus(s.e).Turn != status.TurnActive {
		t.Fatal("fresh joined actor step did not work")
	}
}

func TestClaudeControlOrdinaryChildMergeCannotOwnHumanPrompt(t *testing.T) {
	for _, kind := range []string{"hook", "typed"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := claudeTestScript(t)
			key := s.question()
			ui := map[string]any{"request_id": "question", "question_count": 1, "component": "AskUserQuestion", "surface": "terminal"}
			inv := s.frame("ui.render", "entered", 0, coreContext(""), ui)
			s.frame("ui.render", "returned", inv, coreContext(""), ui)
			if kind == "hook" {
				if err := s.e.HandleCallback(Callback{SessionID: "local", Token: strings.Repeat("a", 64), Sequence: 1, Event: "SubagentStart", Payload: map[string]string{"agent_id": "other"}}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.e.ApplyTypedEvent("local", "SubagentStart", map[string]string{"agent_id": "other"}); err != nil {
					t.Fatal(err)
				}
			}
			record, _ := s.e.ExecutionSnapshot("local")
			call := record.Claude.Calls[key]
			context := coreContext("main")
			context["index"] = 0
			context["tool"] = "AskUserQuestion"
			context["tool_use_id"] = "question"
			s.frame("tool.call", "returned", call.Invocation, context, map[string]any{})
			if got := controlStatus(s.e); got.Interaction != status.InteractionNone || got.Attention {
				t.Fatal("inherited projected Prompt contaminated ordinary base", kind, got)
			}
		})
	}
}

func TestClaudeControlTerminalAgentCustodyIsBounded(t *testing.T) {
	s, _ := claudeTestScript(t)
	parent := coreContext("parent")
	inv := s.frame("turn.start", "entered", 0, parent, map[string]any{})
	s.frame("turn.start", "returned", inv, parent, map[string]any{"turn_id": "parent"})
	for i := 0; i < 130; i++ {
		context := coreContext("parent")
		context["tool"] = "Agent"
		context["tool_use_id"] = fmt.Sprintf("sync-%d", i)
		inv := s.frame("tool.call", "entered", 0, context, map[string]any{})
		data := map[string]any{"agent_status": "completed"}
		switch i % 3 {
		case 1:
			data = map[string]any{"denied": true}
		case 2:
			data = map[string]any{"agent_status": "remote_launched"}
		}
		s.frame("tool.call", "returned", inv, context, data)
	}
	record, _ := s.e.ExecutionSnapshot("local")
	if len(record.Claude.Calls) != 0 || len(record.Claude.Invocations) != 0 || !record.UnknownActors {
		t.Fatal("terminal tool custody leaked or inferred unknown-child drain", record.Claude)
	}
}
func TestClaudeControlNestedStepPreservesStartInvocation(t *testing.T) {
	s, _ := claudeTestScript(t)
	context := coreContext("main")
	start := s.frame("turn.start", "entered", 0, context, map[string]any{})
	step := coreContext("main")
	step["index"] = 0
	step["message_count"] = 1
	inv := s.frame("turn.step", "entered", 0, step, map[string]any{})
	record, _ := s.e.ExecutionSnapshot("local")
	if len(record.Claude.Invocations) != 2 {
		t.Fatal("nested interleave dropped unresolved start")
	}
	s.frame("turn.step", "returned", inv, step, map[string]any{"turn_id": "main", "index": 0, "stop_reason": "end_turn", "tool_uses": []any{}})
	record, _ = s.e.ExecutionSnapshot("local")
	if len(record.Claude.Invocations) != 1 {
		t.Fatal("step return settled outer start")
	}
	s.frame("turn.start", "returned", start, context, map[string]any{"turn_id": "main"})
	record, _ = s.e.ExecutionSnapshot("local")
	if len(record.Claude.Invocations) != 0 {
		t.Fatal("exact start return did not settle its invocation")
	}
}

func TestClaudeControlInvalidationPublishesBeforeOccupiedWriter(t *testing.T) {
	s, _ := claudeTestScript(t)
	knownChildTrace(s, true)
	if err := s.e.SetClaudeObservationLease("local", controlLease(t, s.e), 0); err != nil {
		t.Fatal(err)
	}
	if controlStatus(s.e).Turn != status.TurnActive {
		t.Fatal("fixture lacks positive Working")
	}
	events := make(chan status.Status, 16)
	s.e.emit = func(_ string, next status.Status) { events <- next }
	s.e.mu.Lock()
	session := s.e.sessions["local"]
	s.e.mu.Unlock()
	session.emitMu.Lock()
	done := make(chan error, 1)
	go func() { done <- s.e.InvalidateExecution("local") }()
	select {
	case got := <-events:
		if got.Turn != status.TurnUnknown {
			t.Error("gap did not publish Unknown", got)
		}
	case <-time.After(time.Second):
		session.emitMu.Unlock()
		<-done
		t.Fatal("public gap withdrawal waited for occupied execution writer")
	}
	session.emitMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(events)
	for got := range events {
		if got.Turn == status.TurnActive {
			t.Fatal("later writer republished Working after gap", got)
		}
	}
}
