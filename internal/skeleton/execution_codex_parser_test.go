package skeleton

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/status"
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
		{`{"id":"a","type":"commandExecution","status":"completed","exitCode":0,"source":"unifiedExecStartup"}`, engine.CodexCommandExecution, "", true},
		{`{"id":"a","type":"commandExecution","status":"completed","exitCode":0,"source":"userShell"}`, engine.CodexCommandExecution, "", false},
		{`{"id":"a","type":"commandExecution","status":"completed","exitCode":0,"source":"unifiedExecInteraction"}`, engine.CodexCommandExecution, "", false},
		{`{"id":"a","type":"commandExecution","status":"completed","exitCode":0,"source":"futureSource"}`, engine.CodexCommandExecution, "", false},
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

func TestCodexExecutionParserPreservesActorIdentity(t *testing.T) {
	frame := []byte(`{"method":"turn/completed","params":{"threadId":"another-thread","turn":{"id":"turn","status":"completed"}}}`)
	event, observed := parseCodexExecutionFrame("turn/completed", frame, "thread")
	if !observed || event.ActorThreadID != "another-thread" || event.ThreadID != "thread" {
		t.Fatal("actor terminal lost its exact thread identity")
	}
	frame = []byte(`{"method":"turn/futureHumanWait","params":{"threadId":"another-thread","turnId":"turn"}}`)
	event, observed = parseCodexExecutionFrame("turn/futureHumanWait", frame, "thread")
	if !observed || event.Kind != engine.CodexUnknownEvidence {
		t.Fatal("unknown actor lifecycle silently disappeared")
	}
}

func TestCodexExecutionParserExactUserClientReceipt(t *testing.T) {
	for _, client := range []string{`"client"`, `null`, `""`, `42`, `"client","clientId":"other"`, `"client","ClientId":"other"`} {
		raw := []byte(`{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"id":"item","type":"userMessage","clientId":` + client + `,"content":[{"text":"private input"}]}}}`)
		event, observed := parseCodexExecutionFrame("item/completed", raw, "thread")
		if !observed {
			t.Fatal("user lifecycle disappeared")
		}
		if client == `"client"` {
			if event.Kind != engine.CodexItemCompleted || event.ClientID != "client" {
				t.Fatalf("native client identity lost: %+v", event)
			}
		} else if client == `null` {
			if event.Kind != engine.CodexItemCompleted || event.ClientID != "" {
				t.Fatal("null client receipt fabricated")
			}
		} else if event.Kind != engine.CodexUnknownEvidence {
			t.Fatalf("ambiguous client identity accepted: %+v", event)
		}
		encoded, _ := json.Marshal(event)
		if strings.Contains(string(encoded), "private input") {
			t.Fatal("prompt body entered control evidence")
		}
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

func TestCodexExecutionParserFileAndMcpSuccess(t *testing.T) {
	for _, tc := range []struct {
		item    string
		success bool
	}{
		{`{"id":"patch","type":"fileChange","status":"completed","changes":[]}`, true},
		{`{"id":"patch","type":"fileChange","status":"failed","changes":[]}`, false},
		{`{"id":"patch","type":"fileChange","status":"declined","changes":[]}`, false},
		{`{"id":"mcp","type":"mcpToolCall","status":"completed","result":{"content":[],"structuredContent":null,"_meta":null},"error":null}`, true},
		{`{"id":"mcp","type":"mcpToolCall","status":"completed","result":null,"error":null}`, false},
		{`{"id":"mcp","type":"mcpToolCall","status":"completed","result":{"content":[]},"error":{"message":"failed"}}`, false},
		{`{"id":"mcp","type":"mcpToolCall","status":"failed","result":{"content":[]},"error":null}`, false},
		{`{"id":"mcp","type":"mcpToolCall","status":"completed","result":{"content":[]}}`, false},
	} {
		raw := `{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":` + tc.item + `}}`
		e, observed := parseCodexExecutionFrame("item/completed", []byte(raw), "thread")
		if !observed || e.ToolSucceeded != tc.success {
			t.Fatalf("wrong native tool success %s: %+v", tc.item, e)
		}
	}
	for _, item := range []string{
		`{"id":"patch","type":"fileChange","status":"completed","Status":"failed"}`,
		`{"id":"mcp","type":"mcpToolCall","status":"completed","result":{"content":[]},"error":null,"Error":{"message":"failed"}}`,
		`{"id":"mcp","type":"mcpToolCall","status":"completed","result":{"content":[],"Content":null},"error":null}`,
		`{"id":"mcp","type":"mcpToolCall","status":"completed","result":{"content":[],"isError":true},"error":null}`,
	} {
		raw := `{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":` + item + `}}`
		e, observed := parseCodexExecutionFrame("item/completed", []byte(raw), "thread")
		if !observed || e.Kind != engine.CodexUnknownEvidence {
			t.Fatalf("ambiguous tool result trusted: %+v", e)
		}
	}
}

func TestCodexExecutionNativeCommonToolFixturesThroughIngress(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex_execution_tools_0163.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Scenario string
			ThreadID string `json:"threadId"`
			Events   []struct {
				Method      string          `json:"method"`
				Params      json.RawMessage `json:"params"`
				EmittedAtMS int64           `json:"emittedAtMs"`
			}
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, testcase := range fixture.Cases {
		t.Run(testcase.Scenario, func(t *testing.T) {
			r := newCodexExecutionRig(t, testcase.ThreadID, true)
			for _, event := range testcase.Events {
				frame, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				if !r.feed.ingress.enqueue(event.Method, frame, time.Now()) {
					t.Fatal("native fixture rejected")
				}
			}
			r.drain(t)
			if _, ok := r.d.codexQuotaCandidate(r.local); !ok {
				snapshot, _ := r.d.eng.ExecutionSnapshot(r.local)
				t.Fatalf("qualified native %s rejected: %+v", testcase.Scenario, snapshot.Codex)
			}
		})
	}
}

func TestCodexExecutionReadStateIsObservationOnly(t *testing.T) {
	if _, observed := parseCodexExecutionFrame("thread/readState/changed", []byte(`{"method":"thread/readState/changed","params":{"threadId":"thread","readState":{"firstUnread":null,"revision":"native-revision"}}}`), "thread"); observed {
		t.Fatal("read/unread marker became execution control")
	}
}

func TestCodexExecutionNativeChildrenThroughIngress(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex_execution_children_0163.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Scenario string            `json:"scenario"`
			ThreadID string            `json:"threadId"`
			Events   []json.RawMessage `json:"events"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, testcase := range fixture.Cases {
		t.Run(testcase.Scenario, func(t *testing.T) {
			r := newCodexExecutionRig(t, testcase.ThreadID, true)
			drain := func() {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for r.feed.ingress.pending.Load() != 0 {
					if time.Now().After(deadline) {
						t.Fatal("native trace ingress did not drain")
					}
					time.Sleep(time.Millisecond)
				}
			}
			for _, frame := range testcase.Events {
				var event struct {
					Method string `json:"method"`
					Params struct {
						ThreadID string `json:"threadId"`
					} `json:"params"`
				}
				if err := json.Unmarshal(frame, &event); err != nil {
					t.Fatal(err)
				}
				if !r.feed.ingress.enqueue(event.Method, frame, time.Now()) {
					t.Fatal("native fixture rejected")
				}
				if event.Method == "turn/completed" && event.Params.ThreadID == testcase.ThreadID {
					drain()
					_, candidate := r.d.codexQuotaCandidate(r.local)
					want := testcase.Scenario != "child-timeout" && testcase.Scenario != "child-grandchild" && testcase.Scenario != "command-background" && testcase.Scenario != "child-sendinput" && testcase.Scenario != "child-sendinput-active"
					if !want {
						meta, ok := r.d.core.Get(r.local)
						if !ok || status.Derive(meta.Status) != status.GroupWorking {
							t.Fatalf("native live custody projected idle at quota: %+v", meta.Status)
						}
					}
					if candidate != want {
						snapshot, _ := r.d.eng.ExecutionSnapshot(r.local)
						t.Fatalf("native quota custody candidate=%v: %+v", candidate, snapshot.Codex)
					}
				}
			}
			drain()
			_, candidate := r.d.codexQuotaCandidate(r.local)
			if candidate != (testcase.Scenario != "child-timeout" && testcase.Scenario != "child-grandchild" && testcase.Scenario != "child-sendinput" && testcase.Scenario != "child-sendinput-active") {
				snapshot, _ := r.d.eng.ExecutionSnapshot(r.local)
				t.Fatalf("native final custody candidate=%v: %+v", candidate, snapshot.Codex)
			}
		})
	}
}

func TestCodexExecutionParserCollaborationTrustBoundary(t *testing.T) {
	const frame = `{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"id":"spawn","type":"collabAgentToolCall","tool":"spawnAgent","senderThreadId":"thread","receiverThreadIds":["child"],"agentsStates":{"child":{"status":"pendingInit"}},"status":"completed"}}}`
	e, observed := parseCodexExecutionFrame("item/completed", []byte(frame), "thread")
	if !observed || e.CollabTool != "spawnAgent" || len(e.ReceiverThreadIDs) != 1 || e.ReceiverThreadIDs[0] != "child" || !e.ToolSucceeded {
		t.Fatalf("lost native collaboration identity: %+v", e)
	}
	for _, raw := range []string{
		strings.Replace(strings.Replace(frame, `["child"]`, `[]`, 1), `"agentsStates":{"child":{"status":"pendingInit"}}`, `"agentsStates":{}`, 1),
		strings.Replace(frame, `"senderThreadId":"thread"`, `"senderThreadId":"other"`, 1),
		strings.Replace(frame, `"tool":"spawnAgent"`, `"tool":"futureTool"`, 1),
		strings.Replace(frame, `"status":"pendingInit"`, `"status":"idle"`, 1),
		strings.Replace(frame, `["child"]`, `["child","child"]`, 1),
		strings.Replace(frame, `"agentsStates":{"child":{"status":"pendingInit"}}`, `"agentsStates":{}`, 1),
		strings.Replace(frame, `"senderThreadId":"thread"`, `"SenderThreadId":"thread"`, 1),
		strings.Replace(frame, `"tool":"spawnAgent"`, `"tool":"spawnAgent","tool":"wait"`, 1),
	} {
		e, observed := parseCodexExecutionFrame("item/completed", []byte(raw), "thread")
		if !observed || e.Kind != engine.CodexUnknownEvidence {
			t.Fatalf("ambiguous native child evidence admitted: %+v", e)
		}
	}
}
