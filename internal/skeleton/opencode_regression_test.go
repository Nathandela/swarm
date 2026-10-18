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
		ocReadAssistant(t, s, e, ocAssistantMessage("ses_root", a, u, int64(1100+i), int64(1200+i), "stop"))
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
	ocReadAssistant(t, s, e, ocAssistantMessage("ses_root", "msg_a1", "msg_auto", 1100, 1200, "stop"))
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
		ocReadAssistant(t, s, e, ocAssistantMessage("ses_root", "msg_a1", "msg_missing", 1100, 1200, "stop"))
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
	ocReadAssistant(t, s, e, ocAssistantMessage("ses_root", "msg_a1", "msg_u1", 1100, 1200, "stop"))
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
	ocReadAssistant(t, s, e, ocAssistantMessage("ses_root", "msg_a1", "msg_u1", 1100, 1200, "stop"))
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

func TestOpenCodeRegressionTerminalBeforeParentConverges(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	ocReadAssistant(t, s, e, ocAssistantMessage("ses_root", "msg_a1", "msg_u1", 1100, 1200, "stop"))
	s.Apply(ocUserMessage("ses_root", "msg_u1", 1000), e)
	s.Apply(ocUserTextPart("ses_root", "msg_u1", "prt_1", "hello"), e)
	v := s.View()
	if len(v.Uncertain) != 0 || ocInput(v, "msg_u1").Outcome != OpenCodeOutcomeCompleted {
		t.Fatalf("exact delayed parent did not reconcile: %+v", v)
	}
}

func TestOpenCodeRegressionImportedOrphanDoesNotBlockNewInput(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	rows := []json.RawMessage{json.RawMessage(`{"info":{"id":"msg_old","sessionID":"ses_root","role":"assistant","parentID":"msg_before_page","time":{"created":1100,"completed":1200},"finish":"stop"},"parts":[]}`)}
	if err := s.ImportHistory(rows, e); err != nil {
		t.Fatal(err)
	}
	s.Apply(ocUserMessage("ses_root", "msg_new", 2000), e)
	s.Apply(ocUserTextPart("ses_root", "msg_new", "prt_new", "new task"), e)
	v := s.View()
	if len(v.Uncertain) != 1 || v.Current == nil || v.Current.MessageID != "msg_new" || v.Degraded {
		t.Fatalf("partial imported history disabled current observation: %+v", v)
	}
	// Replaying the same old terminal through the live feed cannot promote its source.
	ocReadAssistant(t, s, e, ocAssistantMessage("ses_root", "msg_old", "msg_before_page", 1100, 1200, "stop"))
	if s.View().Degraded {
		t.Fatal("imported duplicate became a live missing-parent hold")
	}
	ocReadAssistant(t, s, e, ocAssistantMessage("ses_root", "msg_live", "msg_missing_live", 2100, 2200, "stop"))
	if !s.View().Degraded {
		t.Fatal("unseen live input lost its missing-parent hold")
	}
}

func TestOpenCodeRegressionOldCompactionHistoryDoesNotPoisonLiveState(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	rows := []json.RawMessage{
		json.RawMessage(`{"info":{"id":"msg_compact","sessionID":"ses_root","role":"user","time":{"created":1000}},"parts":[{"id":"prt_c","messageID":"msg_compact","sessionID":"ses_root","type":"compaction","auto":true}]}`),
		json.RawMessage(`{"info":{"id":"msg_summary","sessionID":"ses_root","role":"assistant","parentID":"msg_compact","summary":true,"time":{"created":1100,"completed":1200},"finish":"stop"},"parts":[]}`),
		json.RawMessage(`{"info":{"id":"msg_cleanup","sessionID":"ses_root","role":"assistant","parentID":"msg_compact","time":{"created":1201,"completed":1300}},"parts":[]}`),
	}
	if err := s.ImportHistory(rows, e); err != nil {
		t.Fatal(err)
	}
	s.Apply(ocUserMessage("ses_root", "msg_new", 2000), e)
	s.Apply(ocUserTextPart("ses_root", "msg_new", "prt_new", "new task"), e)
	if v := s.View(); v.Degraded || v.Current == nil || v.Current.MessageID != "msg_new" {
		t.Fatalf("old compaction disabled native observation: %+v", v)
	}
}

func TestOpenCodeRegressionImportedOrphanCapacityPreservesLiveFacts(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	ocReadAssistant(t, s, e, ocAssistantMessage("ses_root", "msg_live", "msg_missing", 2000, 2100, "stop"))
	var rows []json.RawMessage
	for i := 0; i < maxOCUncertain+5; i++ {
		rows = append(rows, json.RawMessage(fmt.Sprintf(`{"info":{"id":"msg_old_%d","sessionID":"ses_root","role":"assistant","parentID":"msg_before_page","time":{"created":1000,"completed":1100},"finish":"stop"},"parts":[]}`, i)))
	}
	if err := s.ImportHistory(rows, e); err != nil {
		t.Fatal(err)
	}
	s.Apply(ocUserMessage("ses_root", "msg_missing", 1900), e)
	s.Apply(ocUserTextPart("ses_root", "msg_missing", "prt_live", "task"), e)
	if v := s.View(); v.Degraded || ocInput(v, "msg_missing").Outcome != OpenCodeOutcomeCompleted {
		t.Fatalf("old orphan history evicted live correlation: %+v", v)
	}
}
func TestOpenCodeRegressionMissingPartsWithholdAuthority(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	s.Apply(ocStatus("ses_root", "idle"), e)
	s.Apply(ocUserMessage("ses_root", "msg_u1", 1000), e)
	if !s.View().Degraded {
		t.Fatal("idle map plus unclassified pending input appeared fully observed")
	}
	s.Apply(ocUserTextPart("ses_root", "msg_u1", "prt_1", "hello"), e)
	if s.View().Degraded {
		t.Fatal("observed genuine parts did not resolve temporary classification hold")
	}
}
func TestOpenCodeRegressionRetainedInputTextBudget(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	s.Apply(ocUserMessage("ses_root", "msg_u1", 1000), e)
	for i := 0; i < maxOCInputParts; i++ {
		s.Apply(ocUserTextPart("ses_root", "msg_u1", fmt.Sprintf("prt_%d", i), strings.Repeat("x", maxOCPartText)), e)
	}
	n := 0
	for _, p := range s.inputs["msg_u1"].parts {
		n += len(p.text)
	}
	if n > maxOCInputText {
		t.Fatalf("retained %d text bytes behind %d-byte input budget", n, maxOCInputText)
	}
	if !ocInput(s.View(), "msg_u1").TextTruncated {
		t.Fatal("bounded retained text lost truncation disclosure")
	}
}
func TestOpenCodeRegressionHistoryRejectsWholeForeignPage(t *testing.T) {
	s, e := newOCState(t, "ses_root")
	rows := []json.RawMessage{
		json.RawMessage(`{"info":{"id":"msg_u1","sessionID":"ses_root","role":"user","time":{"created":1000}},"parts":[]}`),
		json.RawMessage(`{"info":{"id":"msg_other","sessionID":"ses_other","role":"user","time":{"created":1001}},"parts":[]}`),
	}
	if err := s.ImportHistory(rows, e); err == nil {
		t.Fatal("foreign history page accepted")
	}
	if len(s.View().Inputs) != 0 {
		t.Fatal("failed history validation partially changed input authority")
	}
}
