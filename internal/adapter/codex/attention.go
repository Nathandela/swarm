package codex

import (
	"encoding/json"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
)

func (codexAdapter) EventStatus(p adapter.HookPayload, expectedThread string) (adapter.TypedStatus, bool) {
	switch p.Event {
	case "thread/status/changed", "item/tool/requestUserInput", "turn/completed", "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
	default:
		return adapter.TypedStatus{}, false
	}
	if len(p.Raw) == 0 || len(p.Raw) > maxContextGuardNotificationBytes || !adapter.IsCanonicalConversationID(expectedThread) {
		return adapter.TypedStatus{}, true
	}
	root, ok := strictJSONObject(p.Raw)
	if !ok || stringValue(root["method"]) != p.Event || statusKeyAliases(root, "method", "params", "id") {
		return adapter.TypedStatus{}, true
	}
	params, ok := root["params"].(map[string]any)
	if !ok || stringValue(params["threadId"]) != expectedThread || statusKeyAliases(params, "threadId", "turnId", "itemId", "status", "isBlocking", "questions") {
		return adapter.TypedStatus{}, true
	}
	dimensions := func(turn, interaction string) (adapter.TypedStatus, bool) {
		return adapter.TypedStatus{Dimensions: map[string]string{"turn": turn, "interaction": interaction}}, true
	}
	switch p.Event {
	case "thread/status/changed":
		st, ok := params["status"].(map[string]any)
		if !ok || statusKeyAliases(st, "type", "activeFlags") {
			return adapter.TypedStatus{}, true
		}
		switch stringValue(st["type"]) {
		case "idle":
			return dimensions("idle", "none")
		case "systemError":
			return dimensions("idle", "error")
		case "active":
			flags, ok := st["activeFlags"].([]any)
			if !ok {
				return adapter.TypedStatus{}, true
			}
			interaction := "none"
			approval, question := false, false
			for _, flag := range flags {
				switch flag {
				case "waitingOnApproval":
					interaction = "permission"
					approval = true
				case "waitingOnUserInput":
					question = true
					if interaction != "permission" {
						interaction = "prompt"
					}
				default:
					return adapter.TypedStatus{}, true
				}
			}
			if interaction != "none" {
				result, claimed := dimensions("idle", interaction)
				if approval {
					result.Waiting = append(result.Waiting, "permission")
				}
				if question {
					result.Waiting = append(result.Waiting, "prompt")
				}
				return result, claimed
			}
			return dimensions("active", "none")
		}
	case "turn/completed":
		turn, ok := params["turn"].(map[string]any)
		if !ok || stringValue(turn["id"]) == "" || statusKeyAliases(turn, "id", "status") {
			return adapter.TypedStatus{}, true
		}
		switch stringValue(turn["status"]) {
		case "completed", "interrupted":
			return dimensions("idle", "none")
		case "failed":
			return dimensions("idle", "error")
		}
	default:
		if !validStatusRequestID(root["id"]) || stringValue(params["itemId"]) == "" || stringValue(params["turnId"]) == "" {
			return adapter.TypedStatus{}, true
		}
		if p.Event != "item/tool/requestUserInput" {
			return dimensions("idle", "permission")
		}
		questions, ok := params["questions"].([]any)
		if !ok || len(questions) == 0 {
			return adapter.TypedStatus{}, true
		}
		for _, question := range questions {
			q, ok := question.(map[string]any)
			if !ok || stringValue(q["id"]) == "" || stringValue(q["question"]) == "" || statusKeyAliases(q, "id", "question") {
				return adapter.TypedStatus{}, true
			}
		}
		if value, present := params["isBlocking"]; present && value != nil {
			blocking, ok := value.(bool)
			if !ok || !blocking {
				return adapter.TypedStatus{}, true
			}
		}
		return dimensions("idle", "prompt")
	}
	return adapter.TypedStatus{}, true
}

func validStatusRequestID(value any) bool {
	switch id := value.(type) {
	case string:
		return strings.TrimSpace(id) != ""
	case json.Number:
		_, err := id.Int64()
		return err == nil
	}
	return false
}
func statusKeyAliases(object map[string]any, keys ...string) bool {
	for key := range object {
		for _, expected := range keys {
			if key != expected && strings.EqualFold(key, expected) {
				return true
			}
		}
	}
	return false
}
