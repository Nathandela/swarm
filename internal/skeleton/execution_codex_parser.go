package skeleton

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
)

// Normalize only control facts; prompt, answer and tool-output bodies never leave this parser.
func parseCodexExecutionFrame(method string, raw []byte, thread string) (engine.CodexExecutionEvent, bool) {
	unknown := engine.CodexExecutionEvent{Kind: engine.CodexUnknownEvidence, ThreadID: thread}
	unsupported := false
	switch method {
	case "thread/started", "thread/name/updated", "thread/tokenUsage/updated", "thread/readState/changed", "turn/plan/updated", "turn/diff/updated",
		"item/agentMessage/delta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta", "item/reasoning/summaryPartAdded",
		"item/commandExecution/outputDelta", "item/fileChange/outputDelta", "item/fileChange/patchUpdated", "item/mcpToolCall/progress":
		return engine.CodexExecutionEvent{}, false
	case "turn/started", "turn/completed", "item/started", "item/completed", "error", "serverRequest/resolved",
		"item/tool/requestUserInput", "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "thread/status/changed":
	default:
		if strings.HasPrefix(method, "turn/") || strings.HasPrefix(method, "item/") || strings.HasPrefix(method, "thread/") {
			unsupported = true
		} else {
			return engine.CodexExecutionEvent{}, false
		}
	}
	if len(raw) == 0 || len(raw) > 1<<20 || !utf8.Valid(raw) {
		return unknown, true
	}
	root, ok := codexExecutionObject(raw, "method", "params", "id")
	if !ok || executionString(root["method"]) != method {
		return unknown, true
	}
	params, ok := codexExecutionObject(root["params"], "threadId", "turnId", "turn", "item", "itemId", "requestId", "willRetry", "status")
	if !ok {
		return unknown, true
	}
	actor := ""
	if raw, present := params["threadId"]; present {
		id := executionString(raw)
		if id == "" {
			return unknown, true
		}
		actor = id
	} else if method != "serverRequest/resolved" {
		return unknown, true
	}
	if unsupported {
		return unknown, true
	}
	e := engine.CodexExecutionEvent{ThreadID: thread, ActorThreadID: actor, TurnID: executionString(params["turnId"])}
	switch method {
	case "turn/started", "turn/completed":
		turn, ok := codexExecutionObject(params["turn"], "id", "status", "error")
		if !ok {
			return unknown, true
		}
		e.TurnID = executionString(turn["id"])
		if e.TurnID == "" {
			return unknown, true
		}
		if method == "turn/started" {
			if executionString(turn["status"]) != "inProgress" {
				return unknown, true
			}
			e.Kind = engine.CodexTurnStarted
			return e, true
		}
		e.Kind, e.Cause = engine.CodexTurnCompleted, engine.CodexTerminalUnknown
		switch executionString(turn["status"]) {
		case "completed":
			e.Cause = engine.CodexTerminalCompleted
		case "interrupted":
			e.Cause = engine.CodexTerminalCancelled
		case "failed":
			e.Cause = engine.CodexTerminalFailed
			failure, valid := codexExecutionObject(turn["error"], "codexErrorInfo")
			if valid && executionString(failure["codexErrorInfo"]) == "usageLimitExceeded" {
				e.Cause = engine.CodexTerminalQuota
			}
		}
		return e, true
	case "item/started", "item/completed":
		if e.TurnID == "" {
			return unknown, true
		}
		item, ok := codexExecutionObject(params["item"], "id", "type", "phase", "status", "exitCode", "source", "questions", "result", "error", "tool", "senderThreadId", "receiverThreadIds", "agentsStates")
		if !ok {
			return unknown, true
		}
		e.ItemID, e.ItemKind = executionString(item["id"]), engine.CodexItemKind(executionString(item["type"]))
		if e.ItemID == "" || e.ItemKind == "" {
			return unknown, true
		}
		e.Kind = engine.CodexItemStarted
		if method == "item/completed" {
			e.Kind = engine.CodexItemCompleted
		}
		if e.ItemKind == engine.CodexAgentMessage {
			if questions, present := item["questions"]; present && !bytes.Equal(bytes.TrimSpace(questions), []byte("null")) {
				var items []json.RawMessage
				if json.Unmarshal(questions, &items) != nil || len(items) != 0 {
					return unknown, true
				}
			}
			e.Phase = engine.CodexAssistantPhase(executionString(item["phase"]))
		}
		if e.ItemKind == engine.CodexCommandExecution && e.Kind == engine.CodexItemCompleted {
			var exit *int
			err := json.Unmarshal(item["exitCode"], &exit)
			source, present := item["source"]
			producer := executionString(source)
			e.ToolSucceeded = err == nil && exit != nil && *exit == 0 && executionString(item["status"]) == "completed" && (!present || producer == "agent" || producer == "unifiedExecStartup")
		}
		if e.ItemKind == engine.CodexFileChange || e.ItemKind == engine.CodexMcpToolCall {
			status := executionString(item["status"])
			if e.Kind == engine.CodexItemStarted {
				if status != "inProgress" {
					return unknown, true
				}
			} else {
				switch status {
				case "completed":
					e.ToolSucceeded = true
					if e.ItemKind == engine.CodexMcpToolCall {
						// Native core maps is_error to failed before v2 drops that field.
						result, valid := codexExecutionObject(item["result"], "content", "structuredContent", "_meta", "isError")
						if !valid {
							return unknown, true
						}
						if _, present := result["isError"]; present {
							return unknown, true
						}
						var content []json.RawMessage
						if json.Unmarshal(result["content"], &content) != nil || content == nil {
							return unknown, true
						}
						if !bytes.Equal(bytes.TrimSpace(item["error"]), []byte("null")) {
							return unknown, true
						}
					}
				case "failed":
				case "declined":
					if e.ItemKind != engine.CodexFileChange {
						return unknown, true
					}
				default:
					return unknown, true
				}
			}
		}

		if e.ItemKind == engine.CodexCollabAgentToolCall {
			if !parseCodexCollabItem(item, &e) {
				return unknown, true
			}
		}
		return e, true
	case "item/tool/requestUserInput", "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		e.RequestID = executionRequestID(root["id"])
		if e.RequestID == "" || e.TurnID == "" || executionString(params["itemId"]) == "" {
			return unknown, true
		}
		e.Kind, e.Decision = engine.CodexDecisionRequested, engine.CodexApproval
		if method == "item/tool/requestUserInput" {
			e.Decision = engine.CodexQuestion
		}
		return e, true
	case "serverRequest/resolved":
		e.RequestID = executionRequestID(params["requestId"])
		if e.RequestID == "" {
			return unknown, true
		}
		e.Kind = engine.CodexDecisionResolved
		return e, true
	case "thread/status/changed":
		state, valid := codexExecutionObject(params["status"], "type", "activeFlags")
		if !valid {
			return unknown, true
		}
		switch executionString(state["type"]) {
		case "idle", "systemError", "notLoaded":
			if _, present := state["activeFlags"]; present {
				return unknown, true
			}
			return engine.CodexExecutionEvent{}, false
		}
		if executionString(state["type"]) == "active" {
			var flags []string
			if json.Unmarshal(state["activeFlags"], &flags) == nil && flags != nil && len(flags) == 0 {
				return engine.CodexExecutionEvent{}, false
			}
		}
		return unknown, true // A wait without its exact native request cannot grant authority.
	case "error":
		var retry *bool
		if json.Unmarshal(params["willRetry"], &retry) != nil || retry == nil {
			return unknown, true
		}
		if !*retry {
			return engine.CodexExecutionEvent{}, false
		}
		if e.TurnID == "" {
			return unknown, true
		}
		e.Kind = engine.CodexRetryActive
		return e, true
	}
	return unknown, true
}

func parseCodexCollabItem(item map[string]json.RawMessage, e *engine.CodexExecutionEvent) bool {
	if executionString(item["senderThreadId"]) != e.ActorThreadID {
		return false
	}
	e.CollabTool = executionString(item["tool"])
	switch e.CollabTool {
	case "spawnAgent", "sendInput", "resumeAgent", "wait", "closeAgent":
	default:
		return false
	}
	if json.Unmarshal(item["receiverThreadIds"], &e.ReceiverThreadIDs) != nil || e.ReceiverThreadIDs == nil || len(e.ReceiverThreadIDs) > persist.MaxExecutionActors {
		return false
	}
	seen := make(map[string]bool, len(e.ReceiverThreadIDs))
	for _, receiver := range e.ReceiverThreadIDs {
		encoded, _ := json.Marshal(receiver)
		if executionString(encoded) == "" || seen[receiver] || receiver == e.ActorThreadID {
			return false
		}
		seen[receiver] = true
	}
	states, valid := codexExecutionObject(item["agentsStates"])
	if !valid || len(states) > persist.MaxExecutionActors {
		return false
	}
	for id, raw := range states {
		state, valid := codexExecutionObject(raw, "status")
		if !valid || !seen[id] {
			return false
		}
		switch executionString(state["status"]) {
		case "pendingInit", "running", "interrupted", "completed", "errored", "shutdown", "notFound":
		default:
			return false
		}
	}
	if e.Kind == engine.CodexItemStarted {
		return executionString(item["status"]) == "inProgress" && len(states) == 0
	}
	if len(states) != len(seen) {
		return false
	}
	switch executionString(item["status"]) {
	case "completed":
		if e.CollabTool == "spawnAgent" && len(e.ReceiverThreadIDs) != 1 {
			return false
		}
		e.ToolSucceeded = true
	case "failed":
	default:
		return false
	}
	return true
}

func codexExecutionObject(raw []byte, keys ...string) (map[string]json.RawMessage, bool) {
	obj, ok := decodeStrictObject(raw)
	if !ok {
		return nil, false
	}
	for key := range obj {
		for _, exact := range keys {
			if key != exact && strings.EqualFold(key, exact) {
				return nil, false
			}
		}
	}
	return obj, true
}

func executionString(raw []byte) string {
	s, ok := strictJSONString(raw)
	if !ok || len(s) > 256 || strings.ContainsFunc(s, unicode.IsControl) {
		return ""
	}
	return s
}

func executionRequestID(raw []byte) string {
	if len(raw) == 0 || len(raw) > 256 {
		return ""
	}
	if raw[0] == '"' {
		s := executionString(raw)
		if s == "" {
			return ""
		}
		canonical, _ := json.Marshal(s)
		if len(canonical) > 256 {
			return ""
		}
		return string(canonical)
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return ""
	}
	n, err := number.Int64()
	if err != nil {
		return ""
	}
	return strconv.FormatInt(n, 10)
}
