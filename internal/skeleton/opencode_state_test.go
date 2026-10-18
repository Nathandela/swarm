package skeleton

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter/opencode"
)

// The reducer tests drive raw legacy-wire JSON through Apply and assert the
// OBSERVABLE view: dispositions, bounded fact lists, and the explicit
// degraded/uncertain states. Nothing here reaches the network or the clock.

func newOCState(t *testing.T, root string) (*OpenCodeState, uint64) {
	t.Helper()
	st, err := NewOpenCodeState(root, "gen-1")
	if err != nil {
		t.Fatalf("NewOpenCodeState(%q): %v", root, err)
	}
	return st, st.BeginEpoch()
}

func ocApply(t *testing.T, st *OpenCodeState, epoch uint64, raw []byte, want OpenCodeDisposition) {
	t.Helper()
	if got := st.Apply(raw, epoch); got.Disposition != want {
		t.Fatalf("Apply(%s) = %q (kind %q), want %q", clipForTest(raw), got.Disposition, got.Kind, want)
	}
}

// ocReadAssistant supplies the complete native row needed to qualify success.
func ocReadAssistant(t *testing.T, st *OpenCodeState, epoch uint64, raw []byte) {
	t.Helper()
	event, err := opencode.DecodeEvent(raw)
	if err != nil || event.MessageUpdated == nil {
		t.Fatalf("assistant fixture: %v", err)
	}
	row, err := json.Marshal(struct {
		Info  json.RawMessage   `json:"info"`
		Parts []json.RawMessage `json:"parts"`
	}{event.MessageUpdated.Info.Raw, []json.RawMessage{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.reconcileMessage(event.MessageUpdated.Info.ID, row, epoch); err != nil {
		t.Fatal(err)
	}
}

func clipForTest(raw []byte) string {
	if len(raw) > 160 {
		return string(raw[:160]) + "..."
	}
	return string(raw)
}

func ocInput(v OpenCodeView, id string) *OpenCodeInput {
	for i := range v.Inputs {
		if v.Inputs[i].MessageID == id {
			return &v.Inputs[i]
		}
	}
	return nil
}

func ocUserMessage(root, id string, at int64) []byte {
	return []byte(fmt.Sprintf(`{"id":"evt_u","type":"message.updated","properties":{"sessionID":%q,"info":{"id":%q,"sessionID":%q,"role":"user","time":{"created":%d}}}}`, root, id, root, at))
}

func ocUserMessageWithSummary(root, id string, at int64) []byte {
	return []byte(fmt.Sprintf(`{"id":"evt_us","type":"message.updated","properties":{"sessionID":%q,"info":{"id":%q,"sessionID":%q,"role":"user","time":{"created":%d},"summary":{"title":"ctx","body":"prev","diffs":[]}}}}`, root, id, root, at))
}

func ocAssistantMessage(root, id, parent string, created, completed int64, finish string) []byte {
	time := fmt.Sprintf(`{"created":%d`, created)
	if completed > 0 {
		time += fmt.Sprintf(`,"completed":%d`, completed)
	}
	time += "}"
	fin := ""
	if finish != "" {
		fin = fmt.Sprintf(`,"finish":%q`, finish)
	}
	return []byte(fmt.Sprintf(`{"id":"evt_a","type":"message.updated","properties":{"sessionID":%q,"info":{"id":%q,"sessionID":%q,"role":"assistant","parentID":%q,"time":%s%s}}}`, root, id, root, parent, time, fin))
}

func ocAssistantError(root, id, parent string, created, completed int64, name string) []byte {
	return []byte(fmt.Sprintf(`{"id":"evt_ae","type":"message.updated","properties":{"sessionID":%q,"info":{"id":%q,"sessionID":%q,"role":"assistant","parentID":%q,"time":{"created":%d,"completed":%d},"error":{"name":%q,"data":{"message":"native failure"}}}}}`, root, id, root, parent, created, completed, name))
}

func ocAssistantSummary(root, id, parent string, created, completed int64) []byte {
	return []byte(fmt.Sprintf(`{"id":"evt_as","type":"message.updated","properties":{"sessionID":%q,"info":{"id":%q,"sessionID":%q,"role":"assistant","parentID":%q,"time":{"created":%d,"completed":%d},"summary":true}}}`, root, id, root, parent, created, completed))
}

func ocUserTextPart(root, msgID, partID, text string) []byte {
	return []byte(fmt.Sprintf(`{"id":"evt_p","type":"message.part.updated","properties":{"sessionID":%q,"part":{"id":%q,"sessionID":%q,"messageID":%q,"type":"text","text":%q}}}`, root, partID, root, msgID, text))
}

func ocSyntheticTextPart(root, msgID, partID, text string) []byte {
	return []byte(fmt.Sprintf(`{"id":"evt_ps","type":"message.part.updated","properties":{"sessionID":%q,"part":{"id":%q,"sessionID":%q,"messageID":%q,"type":"text","text":%q,"synthetic":true}}}`, root, partID, root, msgID, text))
}

func ocToolPart(root, msgID, partID, tool string) []byte {
	return []byte(fmt.Sprintf(`{"id":"evt_pt","type":"message.part.updated","properties":{"sessionID":%q,"part":{"id":%q,"sessionID":%q,"messageID":%q,"type":"tool","callID":"call_1","tool":%q,"state":{"status":"completed","output":"ok"}}}}`, root, partID, root, msgID, tool))
}

func ocStatus(root, subtype string) []byte {
	return []byte(fmt.Sprintf(`{"id":"evt_s","type":"session.status","properties":{"sessionID":%q,"status":{"type":%q}}}`, root, subtype))
}

func ocRetryStatus(root string, attempt int, message string, next int64, label string) []byte {
	return []byte(fmt.Sprintf(`{"id":"evt_sr","type":"session.status","properties":{"sessionID":%q,"status":{"type":"retry","attempt":%d,"message":%q,"next":%d,"action":{"reason":"rate-limit","provider":"openrouter","title":"Provider busy","message":%q,"label":%q}}}}`, root, attempt, message, next, message, label))
}

func ocIdle(root string) []byte {
	return []byte(fmt.Sprintf(`{"id":"evt_i","type":"session.idle","properties":{"sessionID":%q}}`, root))
}

func ocSessionError(root, name, message string) []byte {
	return []byte(fmt.Sprintf(`{"id":"evt_e","type":"session.error","properties":{"sessionID":%q,"error":{"name":%q,"data":{"message":%q}}}}`, root, name, message))
}

func TestOpenCodeState_RequiresRootSessionID(t *testing.T) {
	if _, err := NewOpenCodeState("", "gen-1"); err == nil {
		t.Fatal("empty root session id accepted")
	}
	if _, err := NewOpenCodeState("ses_root", ""); err != nil {
		t.Fatalf("empty generation rejected: %v", err)
	}
}

func TestOpenCodeState_CanonicalTurnCorrelation(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)

	ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCApplied)
	ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_1", "Hello there"), OCApplied)

	v := st.View()
	in := ocInput(v, "msg_u1")
	if in == nil {
		t.Fatal("input msg_u1 missing")
	}
	if in.Text != "Hello there" || in.Outcome != OpenCodeOutcomeNone || in.Synthetic {
		t.Fatalf("input = %+v", *in)
	}
	if v.Current == nil || v.Current.MessageID != "msg_u1" {
		t.Fatalf("current = %+v, want msg_u1", v.Current)
	}
	if v.BusyKnown || v.Degraded {
		t.Fatalf("fresh state claims busyKnown=%v degraded=%v", v.BusyKnown, v.Degraded)
	}

	// A tool-calls step and its tool part are progress, not an outcome.
	ocApply(t, st, epoch, ocAssistantMessage(root, "msg_a1", "msg_u1", 1100, 0, "tool-calls"), OCApplied)
	ocApply(t, st, epoch, ocToolPart(root, "msg_a1", "prt_t1", "bash"), OCIgnoredUntracked)
	if got := ocInput(st.View(), "msg_u1"); got.Outcome != OpenCodeOutcomeNone {
		t.Fatalf("tool step closed the turn: %+v", *got)
	}

	// Busy and idle status observations never complete a request.
	ocApply(t, st, epoch, ocStatus(root, "busy"), OCApplied)
	if v = st.View(); !v.Busy || !v.BusyKnown || v.Degraded {
		t.Fatalf("busy status: busy=%v known=%v degraded=%v", v.Busy, v.BusyKnown, v.Degraded)
	}
	ocApply(t, st, epoch, ocIdle(root), OCApplied)
	if v = st.View(); v.Busy || !v.BusyKnown {
		t.Fatalf("idle status: busy=%v known=%v", v.Busy, v.BusyKnown)
	}
	if got := ocInput(st.View(), "msg_u1"); got.Outcome != OpenCodeOutcomeNone {
		t.Fatalf("idle closed the turn: %+v", *got)
	}

	// A complete later assistant row without continuing tools closes it.
	ocReadAssistant(t, st, epoch, ocAssistantMessage(root, "msg_final", "msg_u1", 1150, 1200, "stop"))
	v = st.View()
	in = ocInput(v, "msg_u1")
	if in.Outcome != OpenCodeOutcomeCompleted || in.OutcomeSource != "live" || in.OutcomeAt != 1200 || in.ErrorName != "" {
		t.Fatalf("outcome = %+v", *in)
	}
	if v.Current != nil {
		t.Fatalf("current = %+v, want nil", v.Current)
	}
	if len(v.Messages) != 3 || v.Messages[0].ID != "msg_u1" || v.Messages[1].ID != "msg_a1" || v.Messages[2].ID != "msg_final" {
		t.Fatalf("messages = %+v", v.Messages)
	}
}

func TestOpenCodeState_TerminalOutcomeClassification(t *testing.T) {
	const root = "ses_root"
	cases := []struct {
		name    string
		raw     func() []byte
		want    OpenCodeOutcome
		wantErr string
	}{
		{"aborted error is cancelled", func() []byte {
			return ocAssistantError(root, "msg_a1", "msg_u1", 1100, 1200, "MessageAbortedError")
		}, OpenCodeOutcomeCancelled, "MessageAbortedError"},
		{"content filter error is declined", func() []byte {
			return ocAssistantError(root, "msg_a1", "msg_u1", 1100, 1200, "ContentFilterError")
		}, OpenCodeOutcomeDeclined, "ContentFilterError"},
		{"api error is failed", func() []byte {
			return ocAssistantError(root, "msg_a1", "msg_u1", 1100, 1200, "APIError")
		}, OpenCodeOutcomeFailed, "APIError"},
		{"unknown error name is failed", func() []byte {
			return ocAssistantError(root, "msg_a1", "msg_u1", 1100, 1200, "SomethingNewError")
		}, OpenCodeOutcomeFailed, "SomethingNewError"},
		{"content-filter finish is declined", func() []byte {
			return ocAssistantMessage(root, "msg_a1", "msg_u1", 1100, 1200, "content-filter")
		}, OpenCodeOutcomeDeclined, ""},
		{"stop finish is completed", func() []byte {
			return ocAssistantMessage(root, "msg_a1", "msg_u1", 1100, 1200, "stop")
		}, OpenCodeOutcomeCompleted, ""},
		{"length finish is completed", func() []byte {
			return ocAssistantMessage(root, "msg_a1", "msg_u1", 1100, 1200, "length")
		}, OpenCodeOutcomeCompleted, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, epoch := newOCState(t, root)
			ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCApplied)
			ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_request", "request"), OCApplied)
			ocReadAssistant(t, st, epoch, tc.raw())
			in := ocInput(st.View(), "msg_u1")
			if in == nil {
				t.Fatal("input missing")
			}
			if in.Outcome != tc.want || in.ErrorName != tc.wantErr {
				t.Fatalf("outcome = %q/%q, want %q/%q", in.Outcome, in.ErrorName, tc.want, tc.wantErr)
			}
		})
	}
}

func TestOpenCodeState_ErrorIsNotErasedByCompletion(t *testing.T) {
	const root = "ses_root"

	t.Run("late completion keeps the failure", func(t *testing.T) {
		st, epoch := newOCState(t, root)
		ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCApplied)
		ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_request", "request"), OCApplied)
		ocApply(t, st, epoch, ocAssistantError(root, "msg_a1", "msg_u1", 1100, 1200, "APIError"), OCApplied)
		ocReadAssistant(t, st, epoch, ocAssistantMessage(root, "msg_a1", "msg_u1", 1100, 1300, "stop"))
		in := ocInput(st.View(), "msg_u1")
		if in.Outcome != OpenCodeOutcomeFailed || in.ErrorName != "APIError" || in.OutcomeAt != 1200 {
			t.Fatalf("late completion overwrote the error: %+v", *in)
		}
	})

	t.Run("late error overwrites the completion", func(t *testing.T) {
		st, epoch := newOCState(t, root)
		ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCApplied)
		ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_request", "request"), OCApplied)
		ocReadAssistant(t, st, epoch, ocAssistantMessage(root, "msg_a1", "msg_u1", 1100, 1200, "stop"))
		ocApply(t, st, epoch, ocAssistantError(root, "msg_a2", "msg_u1", 1300, 1400, "APIError"), OCApplied)
		in := ocInput(st.View(), "msg_u1")
		if in.Outcome != OpenCodeOutcomeFailed || in.ErrorName != "APIError" || in.OutcomeAt != 1400 {
			t.Fatalf("late error did not overwrite: %+v", *in)
		}
	})

	t.Run("equal-rank replay keeps the later terminal", func(t *testing.T) {
		st, epoch := newOCState(t, root)
		ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCApplied)
		ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_request", "request"), OCApplied)
		ocApply(t, st, epoch, ocAssistantError(root, "msg_a1", "msg_u1", 1100, 1500, "MessageAbortedError"), OCApplied)
		ocApply(t, st, epoch, ocAssistantError(root, "msg_a1", "msg_u1", 1100, 1400, "APIError"), OCApplied)
		in := ocInput(st.View(), "msg_u1")
		if in.Outcome != OpenCodeOutcomeCancelled || in.ErrorName != "MessageAbortedError" || in.OutcomeAt != 1500 {
			t.Fatalf("out-of-order equal rank overwrote: %+v", *in)
		}
	})
}

func TestOpenCodeState_ToolCallsStepIsNeverTerminal(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)
	ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCApplied)
	ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_request", "request"), OCApplied)
	// Completion metadata present, but finish tool-calls means the message is a
	// step, not the turn's terminal answer.
	ocApply(t, st, epoch, ocAssistantMessage(root, "msg_a1", "msg_u1", 1100, 1200, "tool-calls"), OCApplied)
	if in := ocInput(st.View(), "msg_u1"); in.Outcome != OpenCodeOutcomeNone {
		t.Fatalf("tool-calls step with completion metadata closed the turn: %+v", *in)
	}
	ocReadAssistant(t, st, epoch, ocAssistantMessage(root, "msg_a1", "msg_u1", 1100, 1300, "stop"))
	if in := ocInput(st.View(), "msg_u1"); in.Outcome != OpenCodeOutcomeCompleted || in.OutcomeAt != 1300 {
		t.Fatalf("final stop did not complete: %+v", *in)
	}
}

func TestOpenCodeState_ForeignEventsAreIgnored(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)
	for _, raw := range [][]byte{
		ocStatus("ses_other", "busy"),
		ocIdle("ses_other"),
		ocUserMessage("ses_other", "msg_f1", 1000),
		ocUserTextPart("ses_other", "msg_f1", "prt_f1", "x"),
		ocAssistantMessage("ses_other", "msg_fa", "msg_f1", 1000, 1100, "stop"),
		ocSessionError("ses_other", "APIError", "other session"),
	} {
		ocApply(t, st, epoch, raw, OCIgnoredForeign)
	}
	v := st.View()
	if v.BusyKnown || len(v.Inputs) != 0 || len(v.Messages) != 0 || len(v.Uncertain) != 0 || v.Error != nil {
		t.Fatalf("foreign events leaked into the view: %+v", v)
	}
}

func TestOpenCodeState_UnknownEventKindIsUntracked(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)
	for _, kind := range []string{"permission.updated", "session.created", "session.turn.started", "message.updated.v2"} {
		raw := []byte(fmt.Sprintf(`{"id":"evt_q","type":%q,"properties":{"sessionID":%q,"anything":true}}`, kind, root))
		ocApply(t, st, epoch, raw, OCIgnoredUntracked)
	}
	v := st.View()
	if v.Degraded || len(v.Inputs) != 0 || len(v.Messages) != 0 {
		t.Fatalf("unknown kinds degraded or leaked: %+v", v)
	}
}

func TestOpenCodeState_UnknownStatusSubtypeDegrades(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)
	ocApply(t, st, epoch, ocStatus(root, "paused"), OCIgnoredUnqualified)

	v := st.View()
	if !v.Degraded || v.DegradedReason == "" || v.BusyKnown {
		t.Fatalf("unknown subtype not degraded: %+v", v)
	}
	if !strings.Contains(v.DegradedReason, "status") {
		t.Fatalf("degraded reason = %q", v.DegradedReason)
	}

	// A successful, correctly scoped status read is authoritative again.
	if err := st.ReconcileRootStatus(epoch, map[string]opencode.SessionStatus{}); err != nil {
		t.Fatalf("ReconcileRootStatus: %v", err)
	}
	if v = st.View(); v.Degraded || !v.BusyKnown || v.Busy {
		t.Fatalf("reconcile did not restore status confidence: %+v", v)
	}
}

func TestOpenCodeState_ReconcileRootStatus(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)

	if err := st.ReconcileRootStatus(epoch+1, map[string]opencode.SessionStatus{}); err == nil {
		t.Fatal("snapshot for a stale epoch accepted")
	}
	if v := st.View(); v.BusyKnown {
		t.Fatalf("stale snapshot mutated state: %+v", v)
	}

	if err := st.ReconcileRootStatus(epoch, map[string]opencode.SessionStatus{root: {Type: "busy"}}); err != nil {
		t.Fatalf("ReconcileRootStatus: %v", err)
	}
	if v := st.View(); !v.Busy || !v.BusyKnown || v.Retry != nil {
		t.Fatalf("busy snapshot: %+v", v)
	}

	snap := map[string]opencode.SessionStatus{root: {
		Type: "retry", Attempt: 3, Message: "slow down", Next: 2000,
		Action: &opencode.RetryAction{Reason: "rate-limit", Provider: "openrouter", Title: "Provider busy", Message: "slow down", Label: "wait"},
	}}
	if err := st.ReconcileRootStatus(epoch, snap); err != nil {
		t.Fatalf("ReconcileRootStatus: %v", err)
	}
	v := st.View()
	if !v.Busy || v.Retry == nil || v.Retry.Attempt != 3 || v.Retry.Action == nil || v.Retry.Action.Label != "wait" {
		t.Fatalf("retry snapshot: %+v", v)
	}

	// The view must not alias reducer state.
	v.Retry.Message = "mutated"
	v.Retry.Action.Label = "mutated"
	if kept := st.View().Retry; kept.Message != "slow down" || kept.Action.Label != "wait" {
		t.Fatalf("view aliases retry state: %+v", kept)
	}

	if err := st.ReconcileRootStatus(epoch, map[string]opencode.SessionStatus{}); err != nil {
		t.Fatalf("ReconcileRootStatus: %v", err)
	}
	if v := st.View(); v.Busy || !v.BusyKnown || v.Retry != nil {
		t.Fatalf("absent key should mean idle after a successful read: %+v", v)
	}
}

func TestOpenCodeState_FailedStatusReadNeverClaimsIdle(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)
	if err := st.ReconcileRootStatus(epoch, map[string]opencode.SessionStatus{root: {Type: "busy"}}); err != nil {
		t.Fatalf("ReconcileRootStatus: %v", err)
	}

	st.MarkRootStatusUnknown("root status read failed")
	v := st.View()
	if v.BusyKnown || v.Busy || !v.Degraded || v.DegradedReason == "" {
		t.Fatalf("failed read must not claim a state: %+v", v)
	}

	ocApply(t, st, epoch, ocStatus(root, "busy"), OCApplied)
	if v = st.View(); !v.BusyKnown || !v.Busy || v.Degraded {
		t.Fatalf("live status did not restore confidence: %+v", v)
	}

	st.MarkRootStatusUnknown("root status read failed again")
	if err := st.ReconcileRootStatus(epoch, map[string]opencode.SessionStatus{}); err != nil {
		t.Fatalf("ReconcileRootStatus: %v", err)
	}
	if v = st.View(); !v.BusyKnown || v.Busy || v.Degraded {
		t.Fatalf("successful read did not restore idle: %+v", v)
	}
}

func TestOpenCodeState_EpochMismatchRefused(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)
	ocApply(t, st, epoch+1, ocUserMessage(root, "msg_u1", 1000), OCRefusedEpoch)
	if len(st.View().Inputs) != 0 {
		t.Fatal("stale-epoch event mutated state")
	}
}

func TestOpenCodeState_ParseFailureDesyncsUntilNewEpoch(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)

	ocApply(t, st, epoch, []byte(`{"type":"message.updated","properties":`), OCRefusedParse)
	v := st.View()
	if !v.Degraded || !strings.Contains(v.DegradedReason, "desynchronized") {
		t.Fatalf("parse refusal did not desynchronize: %+v", v)
	}

	// No live delta is trusted while the stream position is unknown.
	ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCRefusedDegraded)

	next := st.BeginEpoch()
	if next != epoch+1 {
		t.Fatalf("BeginEpoch = %d, want %d", next, epoch+1)
	}
	ocApply(t, st, next, ocUserMessage(root, "msg_u2", 1100), OCApplied)
	ocApply(t, st, epoch, ocUserMessage(root, "msg_u3", 1200), OCRefusedEpoch)
	v = st.View()
	if !v.Degraded || v.BusyKnown || ocInput(v, "msg_u2") == nil || ocInput(v, "msg_u1") != nil {
		t.Fatalf("new epoch lost input custody or silently healed history: %+v", v)
	}
}

func TestOpenCodeState_TerminalWithoutKnownInputBecomesUncertain(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)

	ocReadAssistant(t, st, epoch, ocAssistantMessage(root, "msg_g0", "msg_ghost0", 1000, 1100, "stop"))
	v := st.View()
	if len(v.Inputs) != 0 {
		t.Fatalf("uncertain terminal fabricated an input: %+v", v.Inputs)
	}
	if len(v.Uncertain) != 1 {
		t.Fatalf("uncertain = %+v", v.Uncertain)
	}
	u := v.Uncertain[0]
	if u.MessageID != "msg_g0" || u.InputID != "msg_ghost0" || u.Outcome != OpenCodeOutcomeCompleted || u.At != 1100 || u.Reason == "" {
		t.Fatalf("uncertain record = %+v", u)
	}

	for i := 1; i < maxOCUncertain+1; i++ {
		ocReadAssistant(t, st, epoch, ocAssistantMessage(root, fmt.Sprintf("msg_g%d", i), fmt.Sprintf("msg_ghost%d", i), 1000, int64(1100+i), "stop"))
	}
	v = st.View()
	if len(v.Uncertain) != maxOCUncertain {
		t.Fatalf("uncertain list = %d, want %d", len(v.Uncertain), maxOCUncertain)
	}
	if v.Uncertain[0].InputID != "msg_ghost1" {
		t.Fatalf("oldest uncertain record not dropped: %+v", v.Uncertain[0])
	}
	if !v.Degraded {
		t.Fatal("uncertain-capacity drops were not degraded")
	}
}

func TestOpenCodeState_AutoContinueDoesNotFinishTheRequestedInput(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)

	ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCApplied)
	ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_request", "request"), OCApplied)
	ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_1", "do the thing"), OCApplied)
	ocReadAssistant(t, st, epoch, ocAssistantMessage(root, "msg_a1", "msg_u1", 1100, 1200, "stop"))

	// The auto-continue user message is synthetic: recorded, not the owner.
	ocApply(t, st, epoch, ocUserMessage(root, "msg_u2", 1400), OCApplied)
	ocApply(t, st, epoch, ocSyntheticTextPart(root, "msg_u2", "prt_s2", "continue"), OCApplied)
	v := st.View()
	in := ocInput(v, "msg_u2")
	if in == nil || !in.Synthetic || in.Text != "continue" {
		t.Fatalf("synthetic input = %+v", in)
	}
	if v.Current != nil {
		t.Fatalf("synthetic input became current: %+v", v.Current)
	}

	// Its terminal outcome lands on the synthetic record and flags the epoch;
	// the requested input is already complete and is not touched.
	ocReadAssistant(t, st, epoch, ocAssistantMessage(root, "msg_a2", "msg_u2", 1450, 1500, "stop"))
	v = st.View()
	if got := ocInput(v, "msg_u2"); got.Outcome != OpenCodeOutcomeCompleted || got.OutcomeSource != "live" || got.OutcomeAt != 1500 {
		t.Fatalf("synthetic outcome = %+v", got)
	}
	if got := ocInput(v, "msg_u1"); got.Outcome != OpenCodeOutcomeCompleted || got.OutcomeAt != 1200 {
		t.Fatalf("requested input mutated: %+v", got)
	}
	if !v.Degraded || !strings.Contains(v.DegradedReason, "auto-continue") {
		t.Fatalf("unqualified continuation not degraded: %+v", v)
	}
}

func TestOpenCodeState_CompactionArtifactsAreNotInputOrCompletion(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)
	// Native compaction emits a user row followed by a compaction part, not user.summary metadata.
	ocApply(t, st, epoch, ocUserMessage(root, "msg_c1", 900), OCApplied)
	raw := []byte(`{"type":"message.part.updated","properties":{"sessionID":"ses_root","part":{"id":"prt_c","messageID":"msg_c1","sessionID":"ses_root","type":"compaction","auto":true}}}`)
	ocApply(t, st, epoch, raw, OCIgnoredUnqualified)
	v := st.View()
	if len(v.Inputs) != 0 || v.Current != nil || !v.Degraded {
		t.Fatalf("compaction invented requested input authority: %+v", v)
	}
	if len(v.Messages) != 1 || !v.Messages[0].Summary {
		t.Fatalf("compaction message = %+v", v.Messages)
	}
	ocApply(t, st, epoch, ocUserTextPart(root, "msg_c1", "prt_c1", "summary body"), OCIgnoredUntracked)
	ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCApplied)
	ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_request", "request"), OCApplied)
	ocApply(t, st, epoch, ocAssistantSummary(root, "msg_s1", "msg_u1", 1100, 1200), OCIgnoredUnqualified)
	v = st.View()
	if got := ocInput(v, "msg_u1"); got.Outcome != OpenCodeOutcomeNone {
		t.Fatalf("summary finished requested input: %+v", got)
	}
	if len(v.Messages) != 3 || !v.Messages[2].Summary {
		t.Fatalf("summary message not recorded: %+v", v.Messages)
	}
}

func TestOpenCodeState_PartsReorderedBeforeMessage(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)

	ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_1", "hello"), OCApplied)
	if len(st.View().Inputs) != 0 {
		t.Fatal("part alone fabricated an input")
	}
	ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCApplied)
	in := ocInput(st.View(), "msg_u1")
	if in == nil || in.Text != "hello" {
		t.Fatalf("buffered part not drained: %+v", in)
	}

	// A tool part that was buffered before its assistant message drains too.
	ocApply(t, st, epoch, ocToolPart(root, "msg_a1", "prt_t1", "bash"), OCIgnoredUntracked)
	ocApply(t, st, epoch, ocAssistantMessage(root, "msg_a1", "msg_u1", 1100, 0, "tool-calls"), OCApplied)
	if len(st.View().Messages) != 2 {
		t.Fatalf("assistant message with buffered part = %+v", st.View().Messages)
	}
}

func TestOpenCodeState_PendingPartOverflowRefusedAtCap(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)

	for i := 0; i < maxOCPendingParts; i++ {
		ocApply(t, st, epoch, ocUserTextPart(root, fmt.Sprintf("msg_p%d", i), fmt.Sprintf("prt_p%d", i), "x"), OCApplied)
	}
	ocApply(t, st, epoch, ocUserTextPart(root, "msg_p99", "prt_p99", "x"), OCRefusedCap)
	v := st.View()
	if !v.Degraded || !strings.Contains(v.DegradedReason, "capacity") {
		t.Fatalf("pending overflow not degraded: %+v", v)
	}
}

func TestOpenCodeState_InputCapacityEvictsTerminalThenRefuses(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)

	ocApply(t, st, epoch, ocUserMessage(root, "msg_1", 1000), OCApplied)
	ocApply(t, st, epoch, ocUserTextPart(root, "msg_1", "prt_request", "request"), OCApplied)
	ocReadAssistant(t, st, epoch, ocAssistantMessage(root, "msg_a1", "msg_1", 1001, 1010, "stop"))
	for i := 2; i <= maxOCInputs; i++ {
		ocApply(t, st, epoch, ocUserMessage(root, fmt.Sprintf("msg_%d", i), int64(1000+i)), OCApplied)
	}
	if got := len(st.View().Inputs); got != maxOCInputs {
		t.Fatalf("inputs = %d, want %d", got, maxOCInputs)
	}

	// A new input evicts the oldest TERMINAL record, never an open one.
	ocApply(t, st, epoch, ocUserMessage(root, fmt.Sprintf("msg_%d", maxOCInputs+1), 2000), OCApplied)
	v := st.View()
	if ocInput(v, "msg_1") != nil {
		t.Fatal("oldest terminal input not evicted")
	}
	if ocInput(v, fmt.Sprintf("msg_%d", maxOCInputs+1)) == nil {
		t.Fatal("new input missing after eviction")
	}
	if got := len(v.Inputs); got != maxOCInputs {
		t.Fatalf("inputs = %d, want %d", got, maxOCInputs)
	}

	// With every record still open there is nothing safe to evict: the input is
	// refused and the refusal is explicit.
	refused := fmt.Sprintf("msg_%d", maxOCInputs+2)
	ocApply(t, st, epoch, ocUserMessage(root, refused, 2100), OCRefusedCap)
	v = st.View()
	if ocInput(v, refused) != nil {
		t.Fatal("refused input was tracked")
	}
	if got := len(v.Inputs); got != maxOCInputs {
		t.Fatalf("inputs = %d after refusal, want %d", got, maxOCInputs)
	}
	if !v.Degraded || !strings.Contains(v.DegradedReason, "capacity") {
		t.Fatalf("input refusal not degraded: %+v", v)
	}

	// The refused message's terminal answer is explicit uncertainty.
	ocReadAssistant(t, st, epoch, ocAssistantMessage(root, "msg_ar", refused, 2110, 2120, "stop"))
	if got := st.View().Uncertain; len(got) != 1 || got[0].InputID != refused {
		t.Fatalf("uncertain = %+v", got)
	}
}

func TestOpenCodeState_MessageListStaysBounded(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)
	for i := 0; i < 200; i++ {
		ocApply(t, st, epoch, ocAssistantMessage(root, fmt.Sprintf("msg_a%d", i), "msg_unobserved", 1000, 0, ""), OCApplied)
	}
	v := st.View()
	if len(v.Messages) != maxOCMessages {
		t.Fatalf("messages = %d, want %d", len(v.Messages), maxOCMessages)
	}
	if v.Messages[0].ID != fmt.Sprintf("msg_a%d", 200-maxOCMessages) || v.Messages[len(v.Messages)-1].ID != "msg_a199" {
		t.Fatalf("messages not FIFO: first=%s last=%s", v.Messages[0].ID, v.Messages[len(v.Messages)-1].ID)
	}
}

func TestOpenCodeState_ViewIsACopy(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)
	ocApply(t, st, epoch, ocRetryStatus(root, 1, "slow", 2000, "wait"), OCApplied)
	ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCApplied)
	ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_1", "hi"), OCApplied)
	ocReadAssistant(t, st, epoch, ocAssistantMessage(root, "msg_g", "msg_ghost", 1100, 1200, "stop"))
	ocApply(t, st, epoch, ocSessionError(root, "APIError", "boom"), OCApplied)

	v := st.View()
	v.Inputs[0].Text = "mutated"
	v.Messages[0].Role = "mutated"
	v.Uncertain[0].Reason = "mutated"
	v.Error.Message = "mutated"
	v.Retry.Message = "mutated"

	v2 := st.View()
	if v2.Inputs[0].Text != "hi" || v2.Messages[0].Role != "user" || v2.Uncertain[0].Reason == "mutated" || v2.Error.Message != "boom" || v2.Retry.Message != "slow" {
		t.Fatalf("view aliases state: %+v", v2)
	}
}

func TestOpenCodeState_SessionErrorRecorded(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)
	ocApply(t, st, epoch, ocSessionError(root, "APIError", "quota exceeded"), OCApplied)
	v := st.View()
	if v.Error == nil || v.Error.Name != "APIError" || v.Error.Message != "quota exceeded" {
		t.Fatalf("session error = %+v", v.Error)
	}
}

func TestOpenCodeState_RetryStatusIsActionable(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)
	ocApply(t, st, epoch, ocRetryStatus(root, 2, "rate limited", 1500, "retry now"), OCApplied)
	v := st.View()
	if !v.Busy || !v.BusyKnown || v.Retry == nil {
		t.Fatalf("retry status = %+v", v)
	}
	if v.Retry.Attempt != 2 || v.Retry.Message != "rate limited" || v.Retry.Next != 1500 || v.Retry.Action == nil || v.Retry.Action.Label != "retry now" {
		t.Fatalf("retry detail = %+v", v.Retry)
	}
	ocApply(t, st, epoch, ocStatus(root, "busy"), OCApplied)
	if v = st.View(); v.Retry != nil {
		t.Fatalf("retry fact not cleared by busy: %+v", v.Retry)
	}
}

func TestOpenCodeState_PartTextIsClippedAtRuneBoundary(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)
	ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCApplied)
	ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_1", strings.Repeat("a", 20<<10)), OCApplied)
	in := ocInput(st.View(), "msg_u1")
	if !in.TextTruncated || len(in.Text) != maxOCPartText {
		t.Fatalf("clip: truncated=%v len=%d", in.TextTruncated, len(in.Text))
	}

	st2, epoch2 := newOCState(t, root)
	ocApply(t, st2, epoch2, ocUserMessage(root, "msg_u1", 1000), OCApplied)
	ocApply(t, st2, epoch2, ocUserTextPart(root, "msg_u1", "prt_1", strings.Repeat("é", 10000)), OCApplied)
	in2 := ocInput(st2.View(), "msg_u1")
	if !in2.TextTruncated || len(in2.Text) > maxOCPartText || !utf8.ValidString(in2.Text) || strings.ContainsRune(in2.Text, utf8.RuneError) {
		t.Fatalf("rune-boundary clip: truncated=%v len=%d valid=%v", in2.TextTruncated, len(in2.Text), utf8.ValidString(in2.Text))
	}
}

func TestOpenCodeState_DuplicateUpdatesConverge(t *testing.T) {
	const root = "ses_root"
	st, epoch := newOCState(t, root)
	ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCApplied)
	ocApply(t, st, epoch, ocUserMessage(root, "msg_u1", 1000), OCApplied)
	if got := len(st.View().Inputs); got != 1 {
		t.Fatalf("duplicate message created %d inputs", got)
	}
	ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_1", "a"), OCApplied)
	ocApply(t, st, epoch, ocUserTextPart(root, "msg_u1", "prt_1", "ab"), OCApplied)
	in := ocInput(st.View(), "msg_u1")
	if in.Text != "ab" {
		t.Fatalf("streamed part update = %q, want ab", in.Text)
	}
}

func TestOpenCodeState_HighRateStaysBoundedAndDeterministic(t *testing.T) {
	const root = "ses_root"
	run := func() (OpenCodeView, []byte) {
		st, epoch := newOCState(t, root)
		for i := 0; i < 5000; i++ {
			n := i / 5
			switch i % 5 {
			case 0:
				if n%2 == 0 {
					st.Apply(ocStatus(root, "busy"), epoch)
				} else {
					st.Apply(ocIdle(root), epoch)
				}
			case 1:
				st.Apply(ocUserMessage(root, fmt.Sprintf("msg_u%d", n), int64(1000+n)), epoch)
			case 2:
				st.Apply(ocUserTextPart(root, fmt.Sprintf("msg_u%d", n), fmt.Sprintf("prt_%d", i), "chunk"), epoch)
			case 3:
				ocReadAssistant(t, st, epoch, ocAssistantMessage(root, fmt.Sprintf("msg_a%d", n), fmt.Sprintf("msg_u%d", n), int64(1001+n), int64(1100+n), "stop"))
			case 4:
				st.Apply([]byte(fmt.Sprintf(`{"id":"evt_x%d","type":"session.turn.started","properties":{}}`, i)), epoch)
			}
		}
		v := st.View()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal view: %v", err)
		}
		return v, b
	}
	v, first := run()
	_, second := run()
	if !bytes.Equal(first, second) {
		t.Fatal("reducer is not deterministic across identical runs")
	}
	if len(v.Inputs) > maxOCInputs || len(v.Messages) > maxOCMessages || len(v.Uncertain) > maxOCUncertain {
		t.Fatalf("bounds exceeded: inputs=%d messages=%d uncertain=%d", len(v.Inputs), len(v.Messages), len(v.Uncertain))
	}
}
