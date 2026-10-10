package skeleton

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/status"
)

func newBackendAttentionRig(t *testing.T) (*Daemon, *status.Status) {
	t.Helper()
	r := newResumeIdentityBackendRig(t)
	got := new(status.Status)
	r.sk.eng = engine.New(engine.Config{Emit: func(_ string, s status.Status) { *got = s }})
	r.sk.eng.RegisterSession(resumeIdentityLocal, "", 0, codex.New().SignalSources())
	r.sk.adoptBackendThread(resumeIdentityLocal, resumeIdentityCodexID)
	return r.sk, got
}

func backendAttentionFrame(method, params string) string {
	return fmt.Sprintf(`{"method":%q,"params":{"threadId":%q,%s}}`, method, resumeIdentityCodexID, params)
}

func backendAttentionRequest(id int, item string, approval, blocking bool) string {
	method, extra := "item/tool/requestUserInput", fmt.Sprintf(`,"isBlocking":%t,"questions":[{"id":"q1","header":"Choice","question":"Which option?","options":null}]`, blocking)
	if approval {
		method, extra = "item/commandExecution/requestApproval", `,"command":"pwd","cwd":"/tmp","reason":null`
	}
	return fmt.Sprintf(`{"method":%q,"id":%d,"params":{"threadId":%q,"turnId":"turn-1","itemId":%q%s}}`, method, id, resumeIdentityCodexID, item, extra)
}

func backendAttentionResolved(id int) string {
	return backendAttentionFrame("serverRequest/resolved", fmt.Sprintf(`"requestId":%d`, id))
}

func backendAttentionTurn(terminal string) string {
	return backendAttentionFrame("turn/completed", fmt.Sprintf(`"turn":{"id":"turn-1","items":[],"status":%q,"error":null}`, terminal))
}

func ingestAttention(t *testing.T, sk *Daemon, got *status.Status, raw string, turn status.Turn, interaction status.Interaction) {
	t.Helper()
	sk.ingestBackendFrame(resumeIdentityLocal, []byte(raw), time.Now().UnixMilli())
	if got.Turn != turn || got.Interaction != interaction {
		t.Fatalf("frame %s: turn=%s interaction=%s, want %s/%s", raw, got.Turn, got.Interaction, turn, interaction)
	}
}

func TestBackendAttentionThreadStatusAndRecovery(t *testing.T) {
	sk, got := newBackendAttentionRig(t)
	for _, step := range []struct {
		raw         string
		turn        status.Turn
		interaction status.Interaction
	}{
		{`{"type":"active","activeFlags":[]}`, status.TurnActive, status.InteractionNone},
		{`{"type":"active","activeFlags":["waitingOnUserInput"]}`, status.TurnIdle, status.InteractionPrompt},
		{`{"type":"active","activeFlags":["waitingOnUserInput","waitingOnApproval"]}`, status.TurnIdle, status.InteractionPermission},
		{`{"type":"systemError"}`, status.TurnIdle, status.Interaction("error")},
		{`{"type":"notLoaded"}`, status.TurnIdle, status.Interaction("error")},
		{`{"type":"futureStatus"}`, status.TurnIdle, status.Interaction("error")},
		{`{"type":"idle"}`, status.TurnIdle, status.InteractionNone},
	} {
		ingestAttention(t, sk, got, backendAttentionFrame("thread/status/changed", `"status":`+step.raw), step.turn, step.interaction)
	}
}

func TestBackendAttentionConcurrentRequestsResolveOnlyTheirOwnWait(t *testing.T) {
	sk, got := newBackendAttentionRig(t)
	ingestAttention(t, sk, got, backendAttentionRequest(1, "question-1", false, true), status.TurnIdle, status.InteractionPrompt)
	ingestAttention(t, sk, got, backendAttentionRequest(2, "approval-1", true, true), status.TurnIdle, status.InteractionPermission)
	ingestAttention(t, sk, got, backendAttentionRequest(3, "question-2", false, true), status.TurnIdle, status.InteractionPermission)
	ingestAttention(t, sk, got, backendAttentionResolved(999), status.TurnIdle, status.InteractionPermission)
	ingestAttention(t, sk, got, backendAttentionResolved(2), status.TurnIdle, status.InteractionPrompt)
	ingestAttention(t, sk, got, backendAttentionResolved(1), status.TurnIdle, status.InteractionPrompt)
	ingestAttention(t, sk, got, backendAttentionResolved(1), status.TurnIdle, status.InteractionPrompt)
	ingestAttention(t, sk, got, backendAttentionResolved(3), status.TurnActive, status.InteractionNone)
}

func TestBackendAttentionWaitingSnapshotPreservesKnownRequests(t *testing.T) {
	sk, got := newBackendAttentionRig(t)
	ingestAttention(t, sk, got, backendAttentionRequest(1, "approval", true, true), status.TurnIdle, status.InteractionPermission)
	ingestAttention(t, sk, got, backendAttentionFrame("thread/status/changed", `"status":{"type":"active","activeFlags":["waitingOnApproval"]}`), status.TurnIdle, status.InteractionPermission)
	ingestAttention(t, sk, got, backendAttentionRequest(2, "question", false, true), status.TurnIdle, status.InteractionPermission)
	ingestAttention(t, sk, got, backendAttentionResolved(1), status.TurnIdle, status.InteractionPrompt)
	ingestAttention(t, sk, got, backendAttentionResolved(2), status.TurnActive, status.InteractionNone)
}

func TestBackendAttentionResolutionPreservesUncorrelatedSnapshotWait(t *testing.T) {
	sk, got := newBackendAttentionRig(t)
	ingestAttention(t, sk, got, backendAttentionRequest(1, "approval", true, true), status.TurnIdle, status.InteractionPermission)
	ingestAttention(t, sk, got, backendAttentionFrame("thread/status/changed", `"status":{"type":"active","activeFlags":["waitingOnUserInput"]}`), status.TurnIdle, status.InteractionPrompt)
	ingestAttention(t, sk, got, backendAttentionResolved(1), status.TurnIdle, status.InteractionPrompt)
	ingestAttention(t, sk, got, backendAttentionFrame("thread/status/changed", `"status":{"type":"active","activeFlags":[]}`), status.TurnActive, status.InteractionNone)
}

func TestBackendAttentionBothSnapshotFlagsRetainUnobservedQuestion(t *testing.T) {
	sk, got := newBackendAttentionRig(t)
	ingestAttention(t, sk, got, backendAttentionRequest(1, "approval", true, true), status.TurnIdle, status.InteractionPermission)
	ingestAttention(t, sk, got, backendAttentionFrame("thread/status/changed", `"status":{"type":"active","activeFlags":["waitingOnApproval","waitingOnUserInput"]}`), status.TurnIdle, status.InteractionPermission)
	ingestAttention(t, sk, got, backendAttentionResolved(1), status.TurnIdle, status.InteractionPrompt)
}

func TestBackendAttentionObservedRequestCorrelatesEarlierSnapshot(t *testing.T) {
	sk, got := newBackendAttentionRig(t)
	ingestAttention(t, sk, got, backendAttentionFrame("thread/status/changed", `"status":{"type":"active","activeFlags":["waitingOnUserInput"]}`), status.TurnIdle, status.InteractionPrompt)
	ingestAttention(t, sk, got, backendAttentionRequest(1, "question", false, true), status.TurnIdle, status.InteractionPrompt)
	ingestAttention(t, sk, got, backendAttentionResolved(1), status.TurnActive, status.InteractionNone)
}

func TestBackendAttentionNonblockingRequestsNeverClearOtherAttention(t *testing.T) {
	for _, pending := range []string{"working", "question", "approval", "error"} {
		t.Run(pending, func(t *testing.T) {
			sk, got := newBackendAttentionRig(t)
			raw := backendAttentionFrame("turn/started", `"turn":{"id":"turn-1","status":"inProgress","items":[]}`)
			turn, interaction := status.TurnActive, status.InteractionNone
			switch pending {
			case "question":
				raw, turn, interaction = backendAttentionRequest(1, "pending", false, true), status.TurnIdle, status.InteractionPrompt
			case "approval":
				raw, turn, interaction = backendAttentionRequest(1, "pending", true, true), status.TurnIdle, status.InteractionPermission
			case "error":
				raw, turn, interaction = backendAttentionTurn("failed"), status.TurnIdle, status.Interaction("error")
			}
			ingestAttention(t, sk, got, raw, turn, interaction)
			ingestAttention(t, sk, got, backendAttentionRequest(2, "nonblocking", false, false), turn, interaction)
			ingestAttention(t, sk, got, backendAttentionResolved(2), turn, interaction)
			ingestAttention(t, sk, got, backendAttentionResolved(999), turn, interaction)
		})
	}
}

func TestBackendAttentionOldResolutionPreservesReplacementAnswerability(t *testing.T) {
	sk, got := newBackendAttentionRig(t)
	ingestAttention(t, sk, got, backendAttentionRequest(1, "same-item", true, true), status.TurnIdle, status.InteractionPermission)
	ingestAttention(t, sk, got, backendAttentionRequest(2, "same-item", true, true), status.TurnIdle, status.InteractionPermission)
	ingestAttention(t, sk, got, backendAttentionResolved(1), status.TurnIdle, status.InteractionPermission)
	id, ok := sk.takeServerRequest(resumeIdentityLocal, "same-item")
	if !ok || string(id) != "2" {
		t.Fatalf("old resolution consumed replacement answerability: id=%s ok=%v", id, ok)
	}
	// Answering consumes answerability; the observed resolution still retires the wait.
	ingestAttention(t, sk, got, backendAttentionResolved(2), status.TurnActive, status.InteractionNone)
}

func TestBackendAttentionQuestionNeverAcquiresApprovalAnswerability(t *testing.T) {
	sk, got := newBackendAttentionRig(t)
	ingestAttention(t, sk, got, backendAttentionRequest(1, "same-item", true, true), status.TurnIdle, status.InteractionPermission)
	ingestAttention(t, sk, got, backendAttentionRequest(2, "same-item", false, true), status.TurnIdle, status.InteractionPrompt)
	if id, ok := sk.takeServerRequest(resumeIdentityLocal, "same-item"); ok {
		t.Fatalf("question acquired the old approval card's answering route: id=%s", id)
	}
	ingestAttention(t, sk, got, backendAttentionResolved(1), status.TurnIdle, status.InteractionPrompt)
	ingestAttention(t, sk, got, backendAttentionResolved(2), status.TurnActive, status.InteractionNone)
}

func TestBackendAttentionTurnCompletionEndsRequestLifetime(t *testing.T) {
	for _, terminal := range []string{"completed", "interrupted", "failed"} {
		t.Run(terminal, func(t *testing.T) {
			sk, got := newBackendAttentionRig(t)
			ingestAttention(t, sk, got, backendAttentionRequest(1, "old-question", false, true), status.TurnIdle, status.InteractionPrompt)
			interaction := status.InteractionNone
			if terminal == "failed" {
				interaction = status.Interaction("error")
			}
			ingestAttention(t, sk, got, backendAttentionTurn(terminal), status.TurnIdle, interaction)
			ingestAttention(t, sk, got, backendAttentionResolved(1), status.TurnIdle, interaction)
			ingestAttention(t, sk, got, backendAttentionFrame("turn/started", `"turn":{"id":"turn-2","status":"inProgress","items":[]}`), status.TurnActive, status.InteractionNone)
			fresh := strings.Replace(backendAttentionRequest(2, "new-question", false, true), "turn-1", "turn-2", 1)
			ingestAttention(t, sk, got, fresh, status.TurnIdle, status.InteractionPrompt)
			ingestAttention(t, sk, got, backendAttentionResolved(1), status.TurnIdle, status.InteractionPrompt)
			ingestAttention(t, sk, got, backendAttentionResolved(2), status.TurnActive, status.InteractionNone)
		})
	}
}

func TestBackendAttentionMalformedAndForeignFramesCannotClearWait(t *testing.T) {
	sk, got := newBackendAttentionRig(t)
	ingestAttention(t, sk, got, backendAttentionRequest(1, "pending", false, true), status.TurnIdle, status.InteractionPrompt)
	for _, raw := range []string{
		strings.Replace(backendAttentionTurn("failed"), resumeIdentityCodexID, resumeIdentityOtherCodex, 1),
		backendAttentionFrame("turn/completed", `"turn":{"id":"turn-1","status":"futureStatus"}`),
		backendAttentionFrame("turn/completed", `"turn":{"id":"turn-1","status":"completed","status":"failed"}`),
		strings.Replace(backendAttentionResolved(1), `"requestId":1`, `"requestId":999,"requestId":1`, 1),
		strings.Replace(backendAttentionResolved(1), resumeIdentityCodexID, resumeIdentityOtherCodex, 1),
		strings.Replace(backendAttentionRequest(2, "bad-question", false, true), `"id":2,`, "", 1),
		backendAttentionFrame("item/completed", `"turnId":"turn-1","item":{"id":"tool-1","type":"commandExecution","status":"failed","exitCode":1}`),
	} {
		ingestAttention(t, sk, got, raw, status.TurnIdle, status.InteractionPrompt)
	}
	ingestAttention(t, sk, got, backendAttentionResolved(2), status.TurnIdle, status.InteractionPrompt)
	ingestAttention(t, sk, got, backendAttentionResolved(1), status.TurnActive, status.InteractionNone)
}
