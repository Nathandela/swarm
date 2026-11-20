package voice

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectManagerMCP(t *testing.T, call ManagerToolCaller) *mcp.ClientSession {
	t.Helper()
	return connectTestManagerServer(t, NewManagerMCP(call))
}
func connectTestManagerServer(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	a, b := mcp.NewInMemoryTransports()
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

func TestVMManagerBridgeIncludesScopedCallDelegation(t *testing.T) {
	client := connectTestManagerServer(t, NewVMDiscussionMCP(nil))
	list, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"swarm_create": true, "swarm_read": true, "swarm_read_note": true, "swarm_reply": true, "swarm_discussion_history": true, "swarm_discussion_answer": true, "publish": true, "ask": true}
	for _, tool := range list.Tools {
		if !want[tool.Name] {
			t.Fatal("unexpected discussion tool", tool.Name)
		}
		delete(want, tool.Name)
	}
	if len(want) != 0 {
		t.Fatal("missing discussion tools", want)
	}
}

func TestVMManagerScopedWorkerToolsRequireVerifiedCall(t *testing.T) {
	_, store, runtime, binding := newControllerFixture(t)
	binding.ConversationID = DiscussionConversationPrefix + "call-vm"
	binding, err := store.BindManager(binding)
	if err != nil {
		t.Fatal(err)
	}
	runtime.status.Binding = runtimeBinding(binding)
	refs := ManagerBridgeRefs{Socket: runtime.status.BridgeSocket, AuthFile: runtime.status.AuthFile}
	c, err := NewController(store, runtime, binding, refs)
	if err != nil {
		t.Fatal(err)
	}
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	grant := true
	scope := DiscussionScope{VMProjectAccess: true, InstallationID: "install", DiscussionID: "bound", GuildID: "guild", ThreadID: "thread",
		SupervisorSocket: "/historic/supervisor.sock",
		VerifyAudience:   func(context.Context) error { return nil },
		VerifyOrigin:     func(context.Context, schema.MessageOrigin) error { return nil },
		VerifyGrant: func(context.Context, schema.MessageOrigin) error {
			if !grant {
				return ErrDiscussionSourceChanged
			}
			return nil
		},
	}
	c.discussion = &scope
	workers := &discussionEvidenceWorker{}
	workers.rows = []protocol.SessionView{{ID: "machine/created", Agent: "codex", Name: "created", SpawnedFrom: "session",
		SpawnIntent: schema.SpawnIntentDelegate, Capabilities: &schema.SessionCapabilities{SessionInstance: "created-instance"}}}
	c.workers = workers
	d := &DiscussionController{scope: scope, controller: c, store: store, cfg: Config{ConversationID: binding.ConversationID}, now: time.Now}
	client := connectTestManagerServer(t, NewVMDiscussionMCP(c.ManagerCall))
	const body = "Check the native worker marker."
	hash := hashValue(body)
	origin := schema.MessageOrigin{Kind: "external", InstallationID: scope.InstallationID, DiscussionID: scope.DiscussionID, GuildID: scope.GuildID,
		ChannelID: scope.ThreadID, RequestID: "dv_vm", ActorID: "speaker", RequesterID: "speaker", Revision: hash,
		Voice: schema.VoiceOrigin{CallID: "call", TurnID: "call-turn", SpeakerID: "speaker", SupervisorSocket: scope.SupervisorSocket, BodyHash: hash, Generation: 1}}
	run := func(origin schema.MessageOrigin, nativeTurn string) {
		t.Helper()
		if err := d.Admit(t.Context(), DiscussionInput{Origin: origin, Text: body, Answer: body, Project: func(string) (string, bool) { return project, false }}); err != nil {
			t.Fatal(err)
		}
		runtime.status.ReadyForInput = true
		if err := c.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		nonce := runtime.sent[len(runtime.sent)-1].ManagerAdmission.Nonce
		runtime.status.ReadyForInput = false
		runtime.status.Turn = protocol.ManagerNativeTurn{State: "running", NativeInputID: nonce, NativeTurnID: nativeTurn}
		runtime.proof = protocol.ManagerCallProof{Main: true, NativeInputID: nonce, NativeTurnID: nativeTurn, NativeCallID: "native-call"}
	}
	call := func(name string, args any, allowed bool) {
		t.Helper()
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.IsError == allowed {
			t.Fatalf("%s allowed %t: %s", name, allowed, mustToolResultText(result))
		}
	}
	run(origin, "voice-turn")
	create := CreateInput{ActionID: "created", Agent: "codex", Cwd: project, Prompt: "Read the fixture marker and return it."}
	read := ReadInput{SessionInput: SessionInput{Session: "machine/created", SessionInstance: "created-instance"}}
	call("swarm_create", create, true)
	call("swarm_read", read, true)
	grant = false
	call("swarm_read", read, false)
	grant = true
	runtime.status.Turn.State = "completed"
	if err := c.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}

	origin.Voice = schema.VoiceOrigin{}
	origin.RequestID, origin.MessageID, origin.Revision = "text", "message", "text-revision"
	run(origin, "text-turn")
	create.ActionID = "denied"
	call("swarm_create", create, false)
	call("swarm_read", read, false)
	call("publish", PublishInput{OutputID: "written", Sequence: 1, Text: "Written reply.", Final: true}, true)
	if workers.launches != 1 || workers.nativeReads != 1 {
		t.Fatalf("worker effects: launches=%d, reads=%d", workers.launches, workers.nativeReads)
	}
}

func mustToolResultText(result *mcp.CallToolResult) string {
	for _, item := range result.Content {
		if text, ok := item.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	return ""
}
