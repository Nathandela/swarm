package skeleton

import (
	"encoding/json"
	"fmt"
	"github.com/Nathandela/swarm/internal/adapter/opencode"
	"strings"
	"testing"
)

func TestOpenCodeRegressionOrdinaryDiffSummaryRemainsInput(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	s.Apply(ocUserMessage("ses_root", "msg_u1", 1000), e)
	s.Apply(ocUserTextPart("ses_root", "msg_u1", "prt1", "edit file"), e)
	s.Apply(ocUserMessageWithSummary("ses_root", "msg_u1", 1000), e)
	v := s.View()
	if v.Degraded || ocInput(v, "msg_u1") == nil {
		t.Fatalf("ordinary native user diff-summary treated as compaction: %+v", v)
	}
}
func TestOpenCodeRegressionCompactionPartIsNotRequestedInput(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	s.Apply(ocUserMessage("ses_root", "msg_compact", 1000), e)
	s.Apply([]byte(`{"id":"evt_c","type":"message.part.updated","properties":{"sessionID":"ses_root","part":{"id":"prt_c","messageID":"msg_compact","sessionID":"ses_root","type":"compaction","auto":true}}}`), e)
	if v := s.View(); v.Current != nil && v.Current.MessageID == "msg_compact" {
		t.Fatalf("real native compaction carrier is presented as requested Current: %+v", v.Current)
	}
}
func TestOpenCodeRegressionCompletedTimestampWithoutFinishCannotComplete(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	s.Apply(ocUserMessage("ses_root", "msg_u1", 1000), e)
	s.Apply(ocUserTextPart("ses_root", "msg_u1", "prt_g", "request"), e)
	s.Apply(ocAssistantMessage("ses_root", "msg_a1", "msg_u1", 1100, 1200, ""), e)
	if in := ocInput(s.View(), "msg_u1"); in.Outcome == OpenCodeOutcomeCompleted {
		t.Fatalf("native overflow/cleanup message manufactured completion: %+v", in)
	}
}
func TestOpenCodeRegressionConflictingNativeScopeCannotComplete(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	s.Apply(ocUserMessage("ses_root", "msg_u1", 1000), e)
	s.Apply([]byte(`{"id":"evt_foreign","type":"message.updated","properties":{"sessionID":"ses_root","info":{"id":"msg_foreign","sessionID":"ses_other","role":"assistant","parentID":"msg_u1","time":{"created":1100,"completed":1200},"finish":"stop"}}}`), e)
	if in := ocInput(s.View(), "msg_u1"); in.Outcome != "" {
		t.Fatalf("foreign native info completed root input: %+v", in)
	}
}
func TestOpenCodeRegressionEvictedInputCannotReopenFromDuplicate(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	for i := 0; i <= maxOCInputs; i++ {
		u := fmt.Sprintf("msg_u%d", i)
		a := fmt.Sprintf("msg_a%d", i)
		s.Apply(ocUserMessage("ses_root", u, int64(1000+i)), e)
		s.Apply(ocUserTextPart("ses_root", u, "prt_g", "request"), e)
		s.Apply(ocAssistantMessage("ses_root", a, u, int64(1100+i), int64(1200+i), "stop"), e)
	}
	if ocInput(s.View(), "msg_u0") != nil {
		t.Fatal("fixture did not evict oldest input")
	}
	s.Apply(ocUserMessage("ses_root", "msg_u0", 1000), e)
	if v := s.View(); v.Current != nil && v.Current.MessageID == "msg_u0" {
		t.Fatalf("late duplicate reopened evicted terminal input: %+v", v.Current)
	}
}
func TestOpenCodeRegressionSyntheticAfterTerminalStillDegrades(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	s.Apply(ocUserMessage("ses_root", "msg_auto", 1000), e)
	s.Apply(ocAssistantMessage("ses_root", "msg_a1", "msg_auto", 1100, 1200, "stop"), e)
	s.Apply(ocSyntheticTextPart("ses_root", "msg_auto", "prt_s", "continue"), e)
	if v := s.View(); !v.Degraded {
		t.Fatalf("unqualified synthetic outcome escaped degradation after reordered part: %+v", ocInput(v, "msg_auto"))
	}
}
func TestOpenCodeRegressionCurrentUsesNativeInputTime(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	s.Apply(ocUserMessage("ses_root", "msg_new", 2000), e)
	s.Apply(ocUserTextPart("ses_root", "msg_new", "prt_n", "new"), e)
	s.Apply(ocUserMessage("ses_root", "msg_old", 1000), e)
	s.Apply(ocUserTextPart("ses_root", "msg_old", "prt_o", "old"), e)
	if v := s.View(); v.Current == nil || v.Current.MessageID != "msg_new" {
		t.Fatalf("late old input became Current: %+v", v.Current)
	}
}
func TestOpenCodeRegressionMessageCacheEvictionDoesNotLoseInputParts(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	s.Apply(ocUserMessage("ses_root", "msg_u1", 1000), e)
	for i := 0; i < maxOCMessages; i++ {
		s.Apply(ocAssistantMessage("ses_root", fmt.Sprintf("msg_a%d", i), "msg_u1", int64(1100+i), 0, ""), e)
	}
	s.Apply(ocUserTextPart("ses_root", "msg_u1", "prt_p", "still authoritative"), e)
	if in := ocInput(s.View(), "msg_u1"); in.Text != "still authoritative" {
		t.Fatalf("informational cache eviction blocked live input authority: %+v", in)
	}
}

func TestOpenCodeRegressionEpochCannotHealGapOrRetainBusyProof(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	s.Apply(ocStatus("ses_root", "busy"), e)
	s.Degrade("missing history")
	next := s.BeginEpoch()
	if next == e {
		t.Fatal("epoch did not advance")
	}
	if v := s.View(); v.BusyKnown || !v.Degraded {
		t.Fatalf("new epoch promoted stale authority: %+v", v)
	}
	if err := s.ReconcileRootStatus(next, map[string]opencode.SessionStatus{}); err != nil {
		t.Fatal(err)
	}
	if !s.View().Degraded {
		t.Fatal("status-only snapshot healed a history gap")
	}
}
func TestOpenCodeRegressionNilSnapshotDoesNotProveIdle(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	if err := s.ReconcileRootStatus(e, nil); err == nil {
		t.Fatal("nil snapshot accepted")
	}
	if s.View().BusyKnown {
		t.Fatal("nil snapshot proved idle")
	}
}
func TestOpenCodeRegressionDuplicateUnknownTerminalDoesNotExhaustCapacity(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	for i := 0; i < maxOCUncertain+5; i++ {
		s.Apply(ocAssistantMessage("ses_root", "msg_a1", "msg_missing", 1100, 1200, "stop"), e)
	}
	if v := s.View(); len(v.Uncertain) != 1 {
		t.Fatalf("duplicate terminal consumed %d uncertainty entries", len(v.Uncertain))
	}
}
func TestOpenCodeRegressionPendingUpdatesCoalesce(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	for i := 1; i < maxOCPendingParts+5; i++ {
		s.Apply(ocUserTextPart("ses_root", "msg_u1", "prt_1", strings.Repeat("x", i)), e)
	}
	s.Apply(ocUserMessage("ses_root", "msg_u1", 1000), e)
	if v := s.View(); v.Degraded || ocInput(v, "msg_u1").Text != strings.Repeat("x", maxOCPendingParts+4) {
		t.Fatalf("pending part duplicates exhausted capacity: %+v", v)
	}
}
func TestOpenCodeRegressionMissingPartsHaveNoOwnerAuthority(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	s.Apply(ocUserMessage("ses_root", "msg_u1", 1000), e)
	s.Apply(ocAssistantMessage("ses_root", "msg_a1", "msg_u1", 1100, 1200, "stop"), e)
	if v := s.View(); v.Current != nil || ocInput(v, "msg_u1").Outcome != "" {
		t.Fatalf("unclassified user record claimed owner authority: %+v", v)
	}
	s.Apply(ocUserTextPart("ses_root", "msg_u1", "prt_1", "hello"), e)
	if got := ocInput(s.View(), "msg_u1").Outcome; got != OpenCodeOutcomeCompleted {
		t.Fatalf("known genuine input did not gain its exact outcome: %q", got)
	}
}
func TestOpenCodeRegressionHistoryDoesNotBecomeLiveCompletion(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"info":{"id":"msg_u1","sessionID":"ses_root","role":"user","time":{"created":1000}},"parts":[{"id":"prt_1","messageID":"msg_u1","sessionID":"ses_root","type":"text","text":"hello"}]}`),
		json.RawMessage(`{"info":{"id":"msg_a1","sessionID":"ses_root","role":"assistant","parentID":"msg_u1","time":{"created":1100,"completed":1200},"finish":"stop"},"parts":[]}`),
	}
	s, e := newOCState(t, "ses_root")
	if err := s.ImportHistory(rows, e); err != nil {
		t.Fatal(err)
	}
	if got := ocInput(s.View(), "msg_u1").OutcomeSource; got != OpenCodeOutcomeSourceImported {
		t.Fatalf("old history source=%q", got)
	}
	s.Apply(ocAssistantMessage("ses_root", "msg_a1", "msg_u1", 1100, 1200, "stop"), e)
	if got := ocInput(s.View(), "msg_u1").OutcomeSource; got != OpenCodeOutcomeSourceImported {
		t.Fatalf("duplicate live event woke imported completion: %q", got)
	}
	live, e2 := newOCState(t, "ses_root")
	live.Apply(ocUserMessage("ses_root", "msg_u1", 1000), e2)
	live.Apply(ocUserTextPart("ses_root", "msg_u1", "prt_1", "hello"), e2)
	if err := live.ImportHistory(rows, e2); err != nil {
		t.Fatal(err)
	}
	if got := ocInput(live.View(), "msg_u1").OutcomeSource; got != "reconciled" {
		t.Fatalf("exact previously live input source=%q", got)
	}
}
