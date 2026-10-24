package skeleton

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/engine"
)

func TestCodexExecutionParserTerminalQuota(t *testing.T) {
	const frame = `{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"failed","error":{"codexErrorInfo":"usageLimitExceeded"}}}}`
	e, observed := parseCodexExecutionFrame("turn/completed", []byte(frame), "thread")
	if !observed || e.Kind != engine.CodexTurnCompleted || e.Cause != engine.CodexTerminalQuota || e.TurnID != "turn" {
		t.Fatalf("missing exact terminal quota: %+v", e)
	}
	for _, raw := range []string{
		strings.Replace(frame, `"status":"failed"`, `"status":"completed"`, 1),
		strings.Replace(frame, `"codexErrorInfo":"usageLimitExceeded"`, `"codexErrorInfo":"rateLimitExceeded"`, 1),
		strings.Replace(frame, `"status":"failed"`, `"status":"failed","status":"completed"`, 1),
		strings.Replace(frame, `"status":"failed"`, `"Status":"failed"`, 1),
	} {
		e, _ := parseCodexExecutionFrame("turn/completed", []byte(raw), "thread")
		if e.Cause == engine.CodexTerminalQuota {
			t.Fatal("ambiguous or non-subscription failure became quota")
		}
	}
	e, observed = parseCodexExecutionFrame("error", []byte(`{"method":"error","params":{"threadId":"thread","turnId":"turn","willRetry":false,"error":{"codexErrorInfo":"usageLimitExceeded"}}}`), "thread")
	if observed && e.Kind == engine.CodexTurnCompleted {
		t.Fatal("standalone error fabricated terminal turn")
	}
}

func TestCodexExecutionParserToolAndFinalFacts(t *testing.T) {
	for _, tc := range []struct {
		item    string
		kind    engine.CodexItemKind
		phase   engine.CodexAssistantPhase
		success bool
	}{
		{`{"id":"a","type":"agentMessage","phase":"commentary","text":"private words"}`, engine.CodexAgentMessage, engine.CodexCommentary, false},
		{`{"id":"a","type":"agentMessage","phase":"final_answer","text":"private words"}`, engine.CodexAgentMessage, engine.CodexFinalAnswer, false},
		{`{"id":"a","type":"agentMessage","phase":null}`, engine.CodexAgentMessage, "", false},
		{`{"id":"a","type":"commandExecution","status":"completed","exitCode":0,"aggregatedOutput":"private output"}`, engine.CodexCommandExecution, "", true},
		{`{"id":"a","type":"commandExecution","status":"completed","exitCode":null}`, engine.CodexCommandExecution, "", false},
		{`{"id":"a","type":"commandExecution","status":"failed","exitCode":1}`, engine.CodexCommandExecution, "", false},
	} {
		raw := []byte(`{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":` + tc.item + `}}`)
		e, observed := parseCodexExecutionFrame("item/completed", raw, "thread")
		if !observed || e.ItemKind != tc.kind || e.Phase != tc.phase || e.ToolSucceeded != tc.success {
			t.Fatalf("wrong bounded execution facts: %+v", e)
		}
	}
}

func TestCodexExecutionParserHumanAndUnknownEvidence(t *testing.T) {
	for _, raw := range []string{
		`{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"id":"a","type":"agentMessage","phase":"commentary","questions":[{"question":"approve?"}]}}}`,
		`{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"id":"a","Type":"commandExecution","status":"completed","exitCode":0}}}`,
		`{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"id":"a","type":"commandExecution","status":"completed","exitCode":0,"ExitCode":1}}}`,
	} {
		e, observed := parseCodexExecutionFrame("item/completed", []byte(raw), "thread")
		if !observed || e.Kind != engine.CodexUnknownEvidence {
			t.Fatalf("ambiguous evidence did not deny recovery: %+v", e)
		}
	}
	e, observed := parseCodexExecutionFrame("item/tool/requestUserInput", []byte(`{"id":123,"method":"item/tool/requestUserInput","params":{"threadId":"thread","turnId":"turn","itemId":"tool"}}`), "thread")
	if !observed || e.Kind != engine.CodexDecisionRequested || e.RequestID != "123" || e.Decision != engine.CodexQuestion {
		t.Fatalf("missing pending native question: %+v", e)
	}
	e, observed = parseCodexExecutionFrame("serverRequest/resolved", []byte(`{"method":"serverRequest/resolved","params":{"requestId":"123"}}`), "thread")
	if !observed || e.Kind != engine.CodexDecisionResolved || e.RequestID != `"123"` {
		t.Fatalf("request ID type was lost: %+v", e)
	}
}

func TestCodexExecutionParserForeignThreadCannotCloseTurn(t *testing.T) {
	frame := []byte(`{"method":"turn/completed","params":{"threadId":"another-thread","turn":{"id":"turn","status":"completed"}}}`)
	if _, observed := parseCodexExecutionFrame("turn/completed", frame, "thread"); observed {
		t.Fatal("foreign thread completion became parent evidence")
	}
	frame = []byte(`{"method":"turn/futureHumanWait","params":{"threadId":"another-thread","turnId":"turn"}}`)
	if _, observed := parseCodexExecutionFrame("turn/futureHumanWait", frame, "thread"); observed {
		t.Fatal("unrelated thread's unknown lifecycle became parent evidence")
	}
}

func TestCodexExecutionParserUnknownLifecycleCannotDisappear(t *testing.T) {
	e, observed := parseCodexExecutionFrame("turn/futureHumanWait", []byte(`{"method":"turn/futureHumanWait","params":{"threadId":"thread","turnId":"turn"}}`), "thread")
	if !observed || e.Kind != engine.CodexUnknownEvidence {
		t.Fatal("unknown lifecycle silently discarded")
	}
	if _, observed := parseCodexExecutionFrame("item/agentMessage/delta", []byte(`{"delta":"private text"}`), "thread"); observed {
		t.Fatal("text delta entered control evidence")
	}
}
