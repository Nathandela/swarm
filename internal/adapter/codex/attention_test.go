package codex

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

// Fixtures follow Codex 806d973's v2 ThreadStatus, ToolRequestUserInputParams,
// TurnCompletedNotification and RequestId definitions, including the legacy blocking default.
const attentionThread = "019a0033-bd0b-77e1-88e7-584ddeea562d"

type attentionStatusSource interface {
	EventStatus(adapter.HookPayload, string) (adapter.TypedStatus, bool)
}

func attentionSource(t *testing.T) attentionStatusSource {
	t.Helper()
	source, ok := New().(attentionStatusSource)
	if !ok {
		t.Fatal("Codex must expose the optional pure EventStatus normalizer")
	}
	return source
}

func attentionFrame(method, params string) string {
	return fmt.Sprintf(`{"method":%q,"params":{"threadId":%q,%s}}`, method, attentionThread, params)
}

func attentionQuestion() string {
	return `{"method":"item/tool/requestUserInput","id":7,"params":{"threadId":"` + attentionThread + `","turnId":"turn-1","itemId":"item-1","questions":[{"id":"q1","header":"Choice","question":"Which option?","options":null}]}}`
}

func TestAttentionThreadStatus(t *testing.T) {
	source := attentionSource(t)
	for _, tc := range []struct {
		name, status, turn, interaction string
	}{
		{"working", `{"type":"active","activeFlags":[]}`, "active", "none"},
		{"approval", `{"type":"active","activeFlags":["waitingOnApproval"]}`, "idle", "permission"},
		{"question", `{"type":"active","activeFlags":["waitingOnUserInput"]}`, "idle", "prompt"},
		{"both flags", `{"type":"active","activeFlags":["waitingOnApproval","waitingOnUserInput"]}`, "idle", "permission"},
		{"both flags reversed", `{"type":"active","activeFlags":["waitingOnUserInput","waitingOnApproval"]}`, "idle", "permission"},
		{"idle", `{"type":"idle"}`, "idle", "none"},
		{"system error", `{"type":"systemError"}`, "idle", "error"},
		{"not loaded", `{"type":"notLoaded"}`, "", ""},
		{"future type", `{"type":"futureStatus"}`, "", ""},
		{"future flag", `{"type":"active","activeFlags":["futureWait"]}`, "", ""},
		{"known and future flag", `{"type":"active","activeFlags":["waitingOnApproval","futureWait"]}`, "", ""},
		{"missing flags", `{"type":"active"}`, "", ""},
		{"null flags", `{"type":"active","activeFlags":null}`, "", ""},
		{"wrong flags type", `{"type":"active","activeFlags":"waitingOnApproval"}`, "", ""},
		{"wrong flag element", `{"type":"active","activeFlags":[1]}`, "", ""},
		{"future fields", `{"type":"idle","futureField":{"value":true}}`, "idle", "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := adapter.HookPayload{Event: "thread/status/changed", Raw: []byte(attentionFrame("thread/status/changed", `"status":`+tc.status))}
			got, claimed := source.EventStatus(p, attentionThread)
			assertAttentionDimensions(t, got.Dimensions, claimed, tc.turn, tc.interaction)
		})
	}
}

func TestAttentionUserInput(t *testing.T) {
	source := attentionSource(t)
	base := attentionQuestion()
	for _, tc := range []struct {
		name, raw string
		blocking  bool
	}{
		{"legacy omitted blocking", base, true},
		{"explicit blocking", strings.Replace(base, `"questions":`, `"isBlocking":true,"questions":`, 1), true},
		{"explicit nonblocking", strings.Replace(base, `"questions":`, `"isBlocking":false,"questions":`, 1), false},
		{"string request id", strings.Replace(base, `"id":7`, `"id":"request-7"`, 1), true},
		{"future fields", strings.Replace(base, `"questions":`, `"futureField":{"value":true},"questions":`, 1), true},
		{"missing request id", strings.Replace(base, `"id":7,`, "", 1), false},
		{"null request id", strings.Replace(base, `"id":7`, `"id":null`, 1), false},
		{"object request id", strings.Replace(base, `"id":7`, `"id":{}`, 1), false},
		{"fractional request id", strings.Replace(base, `"id":7`, `"id":1.5`, 1), false},
		{"overflow request id", strings.Replace(base, `"id":7`, `"id":9223372036854775808`, 1), false},
		{"empty turn", strings.Replace(base, `"turn-1"`, `""`, 1), false},
		{"empty item", strings.Replace(base, `"item-1"`, `""`, 1), false},
		{"empty questions", strings.Replace(base, `[{"id":"q1","header":"Choice","question":"Which option?","options":null}]`, `[]`, 1), false},
		{"null questions", strings.Replace(base, `[{"id":"q1","header":"Choice","question":"Which option?","options":null}]`, `null`, 1), false},
		{"wrong questions type", strings.Replace(base, `[{"id":"q1","header":"Choice","question":"Which option?","options":null}]`, `"question"`, 1), false},
		{"wrong blocking type", strings.Replace(base, `"questions":`, `"isBlocking":"true","questions":`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, claimed := source.EventStatus(adapter.HookPayload{Event: "item/tool/requestUserInput", Raw: []byte(tc.raw)}, attentionThread)
			turn, interaction := "", ""
			if tc.blocking {
				turn, interaction = "idle", "prompt"
			}
			assertAttentionDimensions(t, got.Dimensions, claimed, turn, interaction)
		})
	}
}

func TestAttentionTurnCompleted(t *testing.T) {
	source := attentionSource(t)
	for _, tc := range []struct{ terminal, turn, interaction string }{
		{"completed", "idle", "none"},
		{"interrupted", "idle", "none"},
		{"failed", "idle", "error"},
		{"inProgress", "", ""},
		{"futureStatus", "", ""},
		{"", "", ""},
	} {
		t.Run(tc.terminal, func(t *testing.T) {
			raw := attentionFrame("turn/completed", fmt.Sprintf(`"turn":{"id":"turn-1","items":[],"status":%q,"error":null}`, tc.terminal))
			got, claimed := source.EventStatus(adapter.HookPayload{Event: "turn/completed", Raw: []byte(raw)}, attentionThread)
			assertAttentionDimensions(t, got.Dimensions, claimed, tc.turn, tc.interaction)
		})
	}
}

func TestAttentionRejectsUntrustedFramesWithoutStaticFallback(t *testing.T) {
	source := attentionSource(t)
	for _, method := range []string{"thread/status/changed", "item/tool/requestUserInput", "turn/completed"} {
		t.Run(method, func(t *testing.T) {
			base := attentionFrame(method, `"status":{"type":"idle"}`)
			switch method {
			case "item/tool/requestUserInput":
				base = attentionQuestion()
			case "turn/completed":
				base = attentionFrame(method, `"turn":{"id":"turn-1","items":[],"status":"completed","error":null}`)
			}
			for _, tc := range []struct{ name, raw, thread string }{
				{"nil", "", attentionThread},
				{"truncated", base[:len(base)-1], attentionThread},
				{"nonobject", `[]`, attentionThread},
				{"trailing value", base + ` {}`, attentionThread},
				{"missing expected thread", base, ""},
				{"invalid expected thread", base, "not-a-thread"},
				{"foreign thread", strings.Replace(base, attentionThread, "019a0033-bd0b-77e1-88e7-584ddeea562e", 1), attentionThread},
				{"missing thread", strings.Replace(base, `"threadId":"`+attentionThread+`",`, "", 1), attentionThread},
				{"thread alias", strings.Replace(base, `"threadId":`, `"ThreadId":`, 1), attentionThread},
				{"duplicate thread", strings.Replace(base, `"threadId":`, `"threadId":"foreign","threadId":`, 1), attentionThread},
				{"duplicate method", strings.Replace(base, `"method":`, `"method":"foreign","method":`, 1), attentionThread},
				{"method mismatch", strings.Replace(base, method, "item/started", 1), attentionThread},
				{"duplicate nested unknown key", strings.Replace(base, `"params":{`, `"future":{"x":1,"x":2},"params":{`, 1), attentionThread},
				{"deep unknown value", strings.Replace(base, `"params":{`, `"future":`+strings.Repeat("[", 40)+"0"+strings.Repeat("]", 40)+`,"params":{`, 1), attentionThread},
				{"oversized unknown value", strings.Replace(base, `"params":{`, `"future":"`+strings.Repeat("x", 2<<20)+`","params":{`, 1), attentionThread},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, claimed := source.EventStatus(adapter.HookPayload{Event: method, Raw: []byte(tc.raw)}, tc.thread)
					assertAttentionDimensions(t, got.Dimensions, claimed, "", "")
				})
			}
		})
	}
}

func TestAttentionNormalizerDoesNotClaimOtherEvents(t *testing.T) {
	source := attentionSource(t)
	for _, method := range []string{"item/completed", "future/event", ""} {
		// A failed tool is not evidence that its turn failed.
		raw := attentionFrame(method, `"item":{"id":"tool-1","type":"commandExecution","status":"failed","exitCode":1}`)
		got, claimed := source.EventStatus(adapter.HookPayload{Event: method, Raw: []byte(raw)}, attentionThread)
		if claimed || len(got.Dimensions) != 0 {
			t.Errorf("unrelated event %q: dimensions=%v claimed=%v", method, got, claimed)
		}
	}
}

func TestAttentionNormalizerIsPure(t *testing.T) {
	source := attentionSource(t)
	p := adapter.HookPayload{Event: "item/tool/requestUserInput", Raw: []byte(attentionQuestion())}
	original := string(p.Raw)
	first, claimed := source.EventStatus(p, attentionThread)
	assertAttentionDimensions(t, first.Dimensions, claimed, "idle", "prompt")
	first.Dimensions["interaction"] = "caller-owned"
	again, claimed := source.EventStatus(p, attentionThread)
	assertAttentionDimensions(t, again.Dimensions, claimed, "idle", "prompt")
	if string(p.Raw) != original {
		t.Fatal("normalization mutated the provider frame")
	}
}

func assertAttentionDimensions(t *testing.T, got map[string]string, claimed bool, turn, interaction string) {
	t.Helper()
	if !claimed {
		t.Fatal("recognized event was not claimed; static fallback could misclassify it")
	}
	if turn == "" && interaction == "" {
		if len(got) != 0 {
			t.Fatalf("unsupported frame changed status: %v", got)
		}
		return
	}
	want := map[string]string{"turn": turn, "interaction": interaction}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized dimensions = %v, want %v", got, want)
	}
}
