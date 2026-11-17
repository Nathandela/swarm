package managerproof

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPersistentRegistryReleasesTerminalDetailWithoutRestoringAuthority(t *testing.T) {
	store, _, _ := replayFixture(t)
	b := proofBinding("claude")
	r, err := NewPersistentRegistry(b, store)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("b", 64)
	started := time.Now()
	for i := range 600 {
		nonce := fmt.Sprintf("%064x", i+1)
		turn := fmt.Sprint("turn-", i)
		call := fmt.Sprint("call-", i)
		if err = r.Prepare(nonce, fmt.Sprint("op-", i), hash, 0); err != nil {
			t.Fatalf("prepare%d: %v", i, err)
		}
		if err = r.StartTurn(turn, ""); err != nil {
			t.Fatal(err)
		}
		if err = r.BindInput(nonce, turn, fmt.Sprint("message-", i)); err != nil {
			t.Fatal(err)
		}
		if err = r.ToolStep(call, turn, "", "publish"); err != nil {
			t.Fatal(err)
		}
		if err = r.AttestTool(call, turn, "", "publish", hash); err != nil {
			t.Fatal(err)
		}
		if err = r.CompleteTool(call, turn, "", "publish", hash); err != nil {
			t.Fatal(err)
		}
		r.CloseTurn(turn, "answer")
		if _, err = r.InputStatus(nonce); err != nil {
			t.Fatal("terminal history unavailable", err)
		}
		if len(r.inputs)+len(r.turns)+len(r.calls) > 4 {
			t.Fatal("completed native detail retained against live capacity")
		}
	}
	stats, e := store.Stats()
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("terminal turns=600 guards=%d records=%d physical_bytes=%d elapsed=%s", stats.GuardKeys, stats.Records, stats.PhysicalBytes, time.Since(started))
	if _, err = r.Resolve(context.Background(), CallRequest{Binding: b, ToolName: "publish", ArgumentsHash: hash, HostMeta: []byte(`{"claudecode/toolUseId":"call-0"}`)}); err == nil {
		t.Fatal("retired old call borrowed latest turn")
	}
	if err = r.StartTurn("turn-0", ""); err == nil {
		t.Fatal("closed turn reopened after detail release")
	}
}
func TestPersistentRegistryNeverEvictsInflightOrUncertainDetail(t *testing.T) {
	store, _, _ := replayFixture(t)
	b := proofBinding("claude")
	r, err := NewPersistentRegistry(b, store)
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("a", 64)
	hash := strings.Repeat("b", 64)
	_ = r.Prepare(nonce, "op", hash, 0)
	_ = r.StartTurn("turn", "")
	_ = r.BindInput(nonce, "turn", "message")
	_ = r.ToolStep("call", "turn", "", "publish")
	_ = r.AttestTool("call", "turn", "", "publish", hash)
	r.CloseTurn("turn", "answer")
	if _, ok := r.inputs[nonce]; !ok {
		t.Fatal("inflight native call detail was evicted")
	}
	if err := r.CompleteTool("call", "turn", "", "publish", hash); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.inputs[nonce]; ok {
		t.Fatal("terminal input detail remained after final native closure")
	}
	uncertain := strings.Repeat("c", 64)
	_ = r.Prepare(uncertain, "uncertain-op", hash, 0)
	_ = r.StartTurn("uncertain", "")
	_ = r.BindInput(uncertain, "uncertain", "message-u")
	_ = r.StartTurn("superseding", "")
	if _, ok := r.inputs[uncertain]; !ok {
		t.Fatal("supersession invented terminal completion")
	}
}

func TestPersistentRejectedInputIsCompactAndNeverCompletesTurn(t *testing.T) {
	store, _, _ := replayFixture(t)
	b := proofBinding("claude")
	r, err := NewPersistentRegistry(b, store)
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("a", 64)
	if err = r.Prepare(nonce, "rejected", strings.Repeat("b", 64), 0); err != nil {
		t.Fatal(err)
	}
	if err = r.RejectInput(nonce); err != nil {
		t.Fatal(err)
	}
	outcome, err := r.InputStatus(nonce)
	if err != nil || outcome.State != "rejected" || outcome.NativeTurnID != "" {
		t.Fatal("rejection invented native completion", outcome, err)
	}
	if r.InputDetailRetained(nonce) {
		t.Fatal("proven rejection retained live detail")
	}
}
func TestPersistentPendingCustodyWithholdsProofAndReadiness(t *testing.T) {
	store, _, _ := replayFixture(t)
	b := proofBinding("claude")
	r, err := NewPersistentRegistry(b, store)
	if err != nil {
		t.Fatal(err)
	}
	nonce, hash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	_ = r.Prepare(nonce, "op", hash, 0)
	_ = r.StartTurn("turn", "")
	_ = r.BindInput(nonce, "turn", "message")
	_ = r.ToolStep("call", "turn", "", "publish")
	_ = r.AttestTool("call", "turn", "", "publish", hash)
	var pending atomic.Bool
	pending.Store(true)
	r.SetProofPending(pending.Load)
	if _, err = r.Snapshot(); !errors.Is(err, ErrUnavailable) {
		t.Fatal("pending source published current status", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	req := CallRequest{Binding: b, ToolName: "publish", ArgumentsHash: hash, HostMeta: []byte(`{"claudecode/toolUseId":"call"}`)}
	if _, err = r.Resolve(ctx, req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("uncommitted queue granted native proof", err)
	}
	pending.Store(false)
	r.NotifyCustodyProgress()
	if _, err = r.Resolve(t.Context(), req); err != nil {
		t.Fatal(err)
	}
}
