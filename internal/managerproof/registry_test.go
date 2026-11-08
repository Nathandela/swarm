package managerproof

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func proofBinding(provider string) Binding {
	return Binding{Session: "place/session", SessionInstance: "incarnation", ConversationID: "conversation", Provider: provider, NativeSessionID: "native-session", NativeGeneration: "native-generation", ObservationGeneration: "observer-generation", LoginEpoch: "account-epoch"}
}
func TestProofRequiresExactNativeInputAndMCPJoin(t *testing.T) {
	b := proofBinding("claude")
	r, err := NewRegistry(b)
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("a", 64)
	hash, _ := ArgumentsHash([]byte(`{"text":"hello","sequence":9007199254740993}`))
	if err := r.Prepare(nonce, "operation", strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if err := r.StartTurn("main", ""); err != nil {
		t.Fatal(err)
	}
	if err := r.BindInput(nonce, "main", "native-message"); err != nil {
		t.Fatal(err)
	}
	if err := r.ToolStep("call", "main", "", "publish"); err != nil {
		t.Fatal(err)
	}
	if err := r.AttestTool("call", "main", "", "publish", hash); err != nil {
		t.Fatal(err)
	}
	req := CallRequest{Binding: b, ToolName: "publish", ArgumentsHash: hash, HostMeta: []byte(`{"claudecode/toolUseId":"call","turn_id":"model-spoof"}`)}
	p, err := r.Resolve(context.Background(), req)
	if err != nil || !p.Main || p.NativeInputID != nonce || p.NativeTurnID != "main" {
		t.Fatalf("proof: %+v %v", p, err)
	}
	req.ArgumentsHash = strings.Repeat("c", 64)
	if _, err := r.Resolve(context.Background(), req); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed args inherited proof: %v", err)
	}
	req.ArgumentsHash = hash
	req.Binding.NativeGeneration = "replacement"
	if _, err := r.Resolve(context.Background(), req); !errors.Is(err, ErrStale) {
		t.Fatal("replacement inherited proof")
	}
	req.Binding = b
	req.Binding.ObservationGeneration = "replacement-observer"
	if _, err := r.Resolve(context.Background(), req); !errors.Is(err, ErrStale) {
		t.Fatal("replacement observer inherited proof")
	}
	req.Binding = b
	r.CloseTurn("main")
	if _, err := r.Resolve(context.Background(), req); !errors.Is(err, ErrStale) {
		t.Fatal("late MCP call inherited closed turn")
	}
}
func TestChildDirectTUIAndCallIDReuseCannotBorrowAdmission(t *testing.T) {
	r, _ := NewRegistry(proofBinding("claude"))
	nonce := strings.Repeat("a", 64)
	hash, _ := ArgumentsHash([]byte(`{}`))
	_ = r.Prepare(nonce, "operation", strings.Repeat("b", 64))
	_ = r.StartTurn("main", "")
	_ = r.BindInput(nonce, "main", "message")
	_ = r.StartTurn("child", "agent")
	_ = r.ToolStep("child-call", "child", "agent", "publish")
	_ = r.AttestTool("child-call", "child", "agent", "publish", hash)
	_, err := r.Resolve(context.Background(), CallRequest{Binding: proofBinding("claude"), ToolName: "publish", ArgumentsHash: hash, HostMeta: []byte(`{"claudecode/toolUseId":"child-call","agentId":""}`)})
	if !errors.Is(err, ErrUnbound) {
		t.Fatalf("child inherited main admission: %v", err)
	}
	_ = r.StartTurn("external", "")
	_ = r.ToolStep("external-call", "external", "", "publish")
	_ = r.AttestTool("external-call", "external", "", "publish", hash)
	_, err = r.Resolve(context.Background(), CallRequest{Binding: proofBinding("claude"), ToolName: "publish", ArgumentsHash: hash, HostMeta: []byte(`{"claudecode/toolUseId":"external-call"}`)})
	if !errors.Is(err, ErrUnbound) {
		t.Fatal("direct TUI turn inherited controller admission")
	}
	if err := r.ToolStep("child-call", "external", "", "publish"); !errors.Is(err, ErrConflict) {
		t.Fatal("reused native call ID accepted")
	}
	if _, err := r.Snapshot(); !errors.Is(err, ErrStale) {
		t.Fatal("conflicting native IDs did not retire proof")
	}
}
func TestCodexUsesCallIDNotResponsesItemIDAndWaitsWithoutLock(t *testing.T) {
	b := proofBinding("codex")
	r, _ := NewRegistry(b)
	nonce := strings.Repeat("a", 64)
	hash, _ := ArgumentsHash([]byte(`{"a":1}`))
	_ = r.Prepare(nonce, "operation", strings.Repeat("b", 64))
	_ = r.StartTurn("turn", "")
	_ = r.BindInput(nonce, "turn", "")
	req := CallRequest{Binding: b, ToolName: "publish", ArgumentsHash: hash, HostMeta: []byte(`{"callId":"native-call","itemId":"response-item","threadId":"native-session","sessionId":"native-session","x-codex-turn-metadata":{"turn_id":"turn","thread_id":"native-session","session_id":"native-session"}}`)}
	done := make(chan error, 1)
	go func() { _, err := r.Resolve(context.Background(), req); done <- err }()
	_ = r.ToolStep("native-call", "turn", "", "publish")
	_ = r.AttestTool("native-call", "turn", "", "publish", hash)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP race wait blocked native event registration")
	}
	bad := req
	bad.HostMeta = []byte(`{"callId":"response-item","itemId":"native-call","threadId":"native-session","sessionId":"native-session","x-codex-turn-metadata":{"turn_id":"turn","thread_id":"native-session","session_id":"native-session"}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := r.Resolve(ctx, bad); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Responses itemId stood in for native callId")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := r.Resolve(ctx2, bad); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait ignored caller cancellation: %v", err)
	}
	r.Retire()
	if _, err := r.Resolve(context.Background(), req); !errors.Is(err, ErrStale) {
		t.Fatal("restart generation left old proof usable")
	}
}

func TestExternalSteeringPoisonsExistingTurnWithoutCancellingAdmission(t *testing.T) {
	b := proofBinding("claude")
	r, _ := NewRegistry(b)
	nonce := strings.Repeat("a", 64)
	hash, _ := ArgumentsHash([]byte(`{}`))
	_ = r.Prepare(nonce, "operation", strings.Repeat("b", 64))
	_ = r.StartTurn("same-turn", "")
	_ = r.BindInput(nonce, "same-turn", "message")
	_ = r.ToolStep("before-steer", "same-turn", "", "publish")
	_ = r.AttestTool("before-steer", "same-turn", "", "publish", hash)
	req := CallRequest{Binding: b, ToolName: "publish", ArgumentsHash: hash, HostMeta: []byte(`{"claudecode/toolUseId":"before-steer"}`)}
	if _, err := r.Resolve(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	r.ExternalActivity()
	_ = r.ToolStep("after-steer", "same-turn", "", "publish")
	_ = r.AttestTool("after-steer", "same-turn", "", "publish", hash)
	for _, id := range []string{"before-steer", "after-steer"} {
		req.HostMeta = []byte(`{"claudecode/toolUseId":"` + id + `"}`)
		if _, err := r.Resolve(context.Background(), req); !errors.Is(err, ErrStale) {
			t.Fatalf("manual steering borrowed voice admission: %v", err)
		}
	}
	snapshot, err := r.Snapshot()
	if err != nil || snapshot.Turn.NativeInputID != nonce || !snapshot.Turn.ExternallySteered || snapshot.Turn.ExternalInput != 1 {
		t.Fatalf("external steer rewrote/cancelled original admission: %+v %v", snapshot, err)
	}
}

func TestNativeReadinessAndCompletedCallsNeverReopen(t *testing.T) {
	b := proofBinding("codex")
	r, _ := NewRegistry(b)
	snapshot, _ := r.Snapshot()
	if snapshot.Turn.State != "unknown" || r.ReadyForInput() {
		t.Fatal("empty registry invented idle readiness")
	}
	if err := r.ObserveThreadState("idle"); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = r.Snapshot()
	if snapshot.Turn.State != "idle" || !r.ReadyForInput() {
		t.Fatal("native idle not observed")
	}
	_ = r.ObserveThreadState("active")
	if r.ReadyForInput() {
		t.Fatal("busy thread accepted a new admission")
	}
	nonce := strings.Repeat("a", 64)
	hash, _ := ArgumentsHash([]byte(`{}`))
	_ = r.Prepare(nonce, "op", strings.Repeat("b", 64))
	_ = r.StartTurn("turn", "")
	_ = r.BindInput(nonce, "turn", "")
	_ = r.ToolStep("call", "turn", "", "publish")
	_ = r.AttestTool("call", "turn", "", "publish", hash)
	if err := r.CompleteTool("call", "turn", "", "publish", hash); err != nil {
		t.Fatal(err)
	}
	_ = r.ToolStep("call", "turn", "", "publish")
	_ = r.AttestTool("call", "turn", "", "publish", hash)
	_, err := r.Resolve(context.Background(), CallRequest{Binding: b, ToolName: "publish", ArgumentsHash: hash, HostMeta: []byte(`{"callId":"call","threadId":"native-session","sessionId":"native-session","x-codex-turn-metadata":{"turn_id":"turn","thread_id":"native-session","session_id":"native-session"}}`)})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("completed native call reopened: %v", err)
	}
	r.CloseTurn("turn", "answer")
	if r.ReadyForInput() {
		t.Fatal("turn complete invented a missing thread-idle snapshot")
	}
	_ = r.ObserveThreadState("idle")
	if !r.ReadyForInput() {
		t.Fatal("native idle did not release next-input gate")
	}
}

func TestDelayedReceiptCannotResurrectSupersededTurn(t *testing.T) {
	r, _ := NewRegistry(proofBinding("codex"))
	nonce := strings.Repeat("a", 64)
	_ = r.Prepare(nonce, "op", strings.Repeat("b", 64))
	_ = r.StartTurn("old", "")
	r.CloseTurn("old")
	_ = r.StartTurn("manual", "")
	if err := r.RecordTurnReceipt(nonce, "old"); err != nil {
		t.Fatal(err)
	}
	if err := r.BindInput(nonce, "old", "message"); err == nil {
		t.Fatal("late receipt revived a closed turn")
	}
	snapshot, _ := r.Snapshot()
	if snapshot.Turn.NativeTurnID != "manual" || snapshot.Turn.NativeInputID != "" {
		t.Fatalf("receipt changed native ordering: %+v", snapshot)
	}
}

func TestNativeCallCanRaceExactReceiptWithoutBorrowingUnknownInput(t *testing.T) {
	b := proofBinding("claude")
	r, _ := NewRegistry(b)
	nonce := strings.Repeat("a", 64)
	hash, _ := ArgumentsHash([]byte(`{}`))
	_ = r.Prepare(nonce, "op", strings.Repeat("b", 64))
	_ = r.StartTurn("turn", "")
	_ = r.ToolStep("call", "turn", "", "publish")
	_ = r.AttestTool("call", "turn", "", "publish", hash)
	result := make(chan error, 1)
	go func() {
		_, err := r.Resolve(context.Background(), CallRequest{Binding: b, ToolName: "publish", ArgumentsHash: hash, HostMeta: []byte(`{"claudecode/toolUseId":"call"}`)})
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("exact call race was not held for native binding: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	_ = r.RecordTurnReceipt(nonce, "turn")
	_ = r.BindInput(nonce, "turn", "message")
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("receipt binding could not wake waiting MCP channel")
	}
}

func TestInputHistoryKeepsExactCompletionAfterLaterTUI(t *testing.T) {
	r, _ := NewRegistry(proofBinding("claude"))
	nonce := strings.Repeat("a", 64)
	_ = r.Prepare(nonce, "op", strings.Repeat("b", 64))
	_ = r.StartTurn("owned", "")
	_ = r.BindInput(nonce, "owned", "message")
	r.CloseTurn("owned", "answer")
	_ = r.StartTurn("later-manual", "")
	outcome, err := r.InputStatus(nonce)
	if err != nil || outcome.NativeInputID != nonce || outcome.NativeTurnID != "owned" || outcome.State != "completed" || outcome.Reason != "answer" {
		t.Fatalf("history borrowed later current turn: %+v %v", outcome, err)
	}
	if _, err := r.InputStatus(strings.Repeat("b", 64)); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unknown input invented an outcome")
	}
}

func TestUnknownCompletionCannotReopenAuthority(t *testing.T) {
	r, _ := NewRegistry(proofBinding("claude"))
	nonce := strings.Repeat("a", 64)
	_ = r.Prepare(nonce, "op", strings.Repeat("b", 64))
	r.CloseTurn("already-ended", "answer")
	if err := r.StartTurn("already-ended", ""); err == nil {
		t.Fatal("late start reopened an unknown completed native turn")
	}
	if err := r.BindInput(nonce, "already-ended", "message"); err == nil {
		t.Fatal("late echo borrowed input authority after unknown completion")
	}
}

func TestUnknownCompletionAtCapacityRetiresInsteadOfForgetting(t *testing.T) {
	r, _ := NewRegistry(proofBinding("codex"))
	for i := range MaxRecords {
		if err := r.ToolStep(strconv.Itoa(i), "unobserved", "", "publish"); err != nil {
			t.Fatal(err)
		}
	}
	r.CloseTurn("unrecordable-completion", "answer")
	if _, err := r.Snapshot(); !errors.Is(err, ErrStale) {
		t.Fatal("unknown completion was forgotten at evidence capacity")
	}
}

func TestPrepareReplayCannotChangeExternalBaseline(t *testing.T) {
	r, _ := NewRegistry(proofBinding("claude"))
	nonce, hash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if err := r.Prepare(nonce, "op", hash, 0); err != nil {
		t.Fatal(err)
	}
	r.ExternalActivity()
	_ = r.ObserveThreadState("idle")
	if err := r.Prepare(nonce, "op", hash, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("replay changed immutable native baseline")
	}
	if err := r.Prepare(nonce, "op", hash); !errors.Is(err, ErrConflict) {
		t.Fatal("replay removed native baseline")
	}
	if err := r.Prepare(strings.Repeat("c", 64), "new-op", hash, 0); !errors.Is(err, ErrStale) {
		t.Fatal("new admission used stale external baseline after native idle")
	}
	if err := r.CheckExternalInput(0); !errors.Is(err, ErrStale) {
		t.Fatal("actual write accepted changed external baseline")
	}
}
