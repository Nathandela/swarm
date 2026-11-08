package voice

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectManagerMCP(t *testing.T, call ManagerToolCaller) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	a, b := mcp.NewInMemoryTransports()
	server := NewManagerMCP(call)
	ss, err := server.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestManagerMCPPreservesHostMetadataSeparately(t *testing.T) {
	var got ManagerToolCall
	client := connectManagerMCP(t, func(_ context.Context, call ManagerToolCall) (json.RawMessage, error) {
		got = call
		return json.RawMessage(`{"state":"published"}`), nil
	})
	args := json.RawMessage(`{"text":"Hello","output_id":"reply","sequence":1,"final":true}`)
	res, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "publish", Arguments: args, Meta: mcp.Meta{"claudecode/toolUseId": "native-tool", "x-codex-turn-metadata": map[string]any{"turn_id": "native-turn"}}})
	if err != nil || res.IsError {
		t.Fatalf("call failed: %+v %v", res, err)
	}
	if got.Name != "publish" || string(got.Arguments) != string(args) || got.Meta["claudecode/toolUseId"] != "native-tool" {
		t.Fatalf("lost native evidence: %+v", got)
	}
	if strings.Contains(string(got.Arguments), "native-turn") {
		t.Fatal("metadata entered business arguments")
	}
}

func TestManagerMCPRejectsAuthorityArgumentsAndInvalidSchema(t *testing.T) {
	calls := 0
	client := connectManagerMCP(t, func(context.Context, ManagerToolCall) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	})
	for _, args := range []json.RawMessage{
		json.RawMessage(`{"output_id":"p","sequence":1,"text":"hi","final":true,"turn_id":"claimed"}`),
		json.RawMessage(`{"output_id":"p","sequence":"one","text":"hi","final":true}`),
		json.RawMessage(`{"sequence":1,"text":"hi","final":true}`),
	} {
		res, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "publish", Arguments: args})
		if err == nil && !res.IsError {
			t.Fatal("accepted invalid schema")
		}
	}
	if calls != 0 {
		t.Fatalf("invalid call dispatched %d times", calls)
	}
}

func TestManagerMCPBoundsInputAndOutput(t *testing.T) {
	calls := 0
	client := connectManagerMCP(t, func(context.Context, ManagerToolCall) (json.RawMessage, error) {
		calls++
		return json.Marshal(map[string]string{"text": strings.Repeat("<", MaxControlFrameBytes)})
	})
	res, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "publish", Arguments: PublishInput{OutputID: "p", Sequence: 1, Text: strings.Repeat("x", MaxUtteranceBytes+1), Final: true}})
	if err == nil && !res.IsError {
		t.Fatal("oversized input accepted")
	}
	if calls != 0 {
		t.Fatal("oversized input dispatched")
	}
	res, err = client.CallTool(context.Background(), &mcp.CallToolParams{Name: "publish", Arguments: PublishInput{OutputID: "p", Sequence: 1, Text: "Hi", Final: true}})
	if err != nil || !res.IsError {
		t.Fatalf("oversized output accepted: %v", err)
	}
}

func TestManagerMCPToolSurface(t *testing.T) {
	client := connectManagerMCP(t, func(context.Context, ManagerToolCall) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	list, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"swarm_list": true, "swarm_read": true, "swarm_create": true, "swarm_resume": true, "swarm_send": true, "swarm_status": true, "swarm_interrupt": true, "swarm_current_request": true, "swarm_decision": true, "publish": true, "ask": true}
	for _, tool := range list.Tools {
		if !want[tool.Name] {
			t.Fatal("unexpected tool", tool.Name)
		}
		delete(want, tool.Name)
	}
	if len(want) > 0 {
		t.Fatal("missing tools", want)
	}
}
