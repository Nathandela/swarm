package skeleton

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/agyinput"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestAgyNativeInputWorksWithoutInventingStructuredChat(t *testing.T) {
	sk := assemble(t)
	dir := t.TempDir()
	program := filepath.Join(dir, "agy")
	native := `#!/usr/bin/env python3
import json,sys
if sys.argv[1]=='--version': print('1.4.7')
elif sys.argv[2]=='get-conversation-metadata': print(json.dumps({'response':{'conversationMetadata':{'metadata':{'rootConversationId':sys.argv[3],'projectId':'project'}}}}))
elif sys.argv[2]=='send-message': print(json.dumps({'response':{'sendMessage':{'recipientId':sys.argv[3],'content':sys.argv[4]}}}))
`
	if err := os.WriteFile(program, []byte(native), 0o700); err != nil {
		t.Fatal(err)
	}
	stamp, err := persist.CLIFingerprint(program)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "agent.txt")
	if err := os.WriteFile(script, []byte("idle 600s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := sk.core.Launch(daemon.LaunchSpec{AgentType: "agy", Argv: []string{fakeAgentBin, script}, Cwd: dir, ClientEnv: []string{"PATH=" + os.Getenv("PATH")}, CLIIdentity: &persist.CLIIdentity{Path: program, Version: "1.4.7", Fingerprint: stamp}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(meta.ShimPID, syscall.SIGTERM) })
	instance, _ := sk.sessionInstance(meta.ID)
	sk.registerSessionCapabilities(meta.ID, protocol.SessionCapabilities{Provider: "agy", ProviderVersion: "1.4.7", AdapterRevision: "fixture", SessionInstance: instance, TerminalFallback: true, TerminalControl: true})
	cfg := agyinput.Config{SessionID: meta.ID, Executable: program, Version: "1.4.7", Fingerprint: stamp, Cwd: dir, BootstrapCommand: "own-native-registration", Env: []string{"PATH=" + os.Getenv("PATH")}}
	broker, err := agyinput.Start(filepath.Join(sk.stateDir, meta.ID), cfg, meta.ShimPID, meta.ShimStartTime)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = broker.Close() }()
	arguments, _ := json.Marshal(map[string]string{"CommandLine": cfg.BootstrapCommand})
	sourceMetadata, _ := json.Marshal(map[string]any{"tool": map[string]any{"conversationId": "11111111-1111-4111-8111-111111111111", "stepIndex": 2, "toolCall": map[string]string{"name": "run_command", "argumentsJson": string(arguments)}}})
	source := agyinput.Source{WorkingDirectory: dir, SwarmSessionID: meta.ID, ConversationID: "11111111-1111-4111-8111-111111111111", ProjectID: "project", TrajectoryID: "trajectory", Address: "localhost:1234", CSRFToken: "own-native-fixture", Executable: program, SourceMetadata: string(sourceMetadata)}
	if err := agyinput.NewClient(broker.Descriptor()).Register(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	client, err := protocol.Dial(sk.SocketPath(), []string{protocol.CapSemanticSend, protocol.CapMessageOrigin})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	id, _ := protocol.NewSemanticOperationID()
	origin := &protocol.MessageOrigin{Kind: "external", InstallationID: "install", RequestID: id, ActorID: "colleague"}
	request := protocol.ComposerSendReq{Session: protocol.NamespacedID(sk.api.endpointID, meta.ID), SessionInstance: instance, Text: "native context\nwith exact bytes", Origin: origin}
	for range 2 {
		if err := client.SemanticSend(id, request); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := client.SemanticOperation(request.Session, id)
	if err != nil || receipt.State != "submitted" || receipt.Origin == nil || *receipt.Origin != *origin || receipt.NativeSessionID != source.ConversationID {
		t.Fatalf("native input receipt: %+v %v", receipt, err)
	}
	capabilities, _ := sk.sessionCapabilities(meta.ID)
	if capabilities.StructuredChat {
		t.Fatal("input-only registration fabricated structured history support")
	}
}

func TestPublicLaunchEnablesAgyNativeOriginInput(t *testing.T) {
	sk := assemble(t, func(cfg *Config) { cfg.NativeInputEnabled = func() bool { return true } })
	dir := t.TempDir()
	program := filepath.Join(dir, "agy")
	native := `#!/usr/bin/env python3
import json,signal,sys
if len(sys.argv)>1 and sys.argv[1]=='--version': print('1.4.5')
elif len(sys.argv)>2 and sys.argv[1]=='agentapi' and sys.argv[2]=='get-conversation-metadata': print(json.dumps({'response':{'conversationMetadata':{'metadata':{'rootConversationId':sys.argv[3],'projectId':'project'}}}}))
elif len(sys.argv)>2 and sys.argv[1]=='agentapi' and sys.argv[2]=='send-message': print(json.dumps({'response':{'sendMessage':{'recipientId':sys.argv[3],'content':sys.argv[4]}}}))
else: signal.pause()
`
	if err := os.WriteFile(program, []byte(native), 0o700); err != nil {
		t.Fatal(err)
	}
	client := dialClient(t, sk, protocol.CapSemanticSend, protocol.CapMessageOrigin)
	session, _, err := client.Launch(protocol.LaunchReq{Agent: "agy", Cwd: dir, Env: []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + dir}, Cols: 80, Rows: 24, InitialPrompt: "Preserve owner words"})
	if err != nil {
		t.Fatal(err)
	}
	metas := sk.core.List()
	if len(metas) != 1 {
		t.Fatalf("public launch roster: %+v", metas)
	}
	meta := metas[0]
	t.Cleanup(func() { _ = syscall.Kill(meta.ShimPID, syscall.SIGTERM) })
	if meta.CLIIdentity == nil || meta.CLIIdentity.Path != program || meta.CLIIdentity.Version != "1.4.5" {
		t.Fatalf("public launch lost native CLI identity: %+v", meta.CLIIdentity)
	}
	desc, err := sk.core.SessionAgyInput(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(sk.stateDir, meta.ID, "shim-launch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var launch struct {
		AgyInput *agyinput.Config `json:"agy_input"`
	}
	if err := json.Unmarshal(raw, &launch); err != nil || launch.AgyInput == nil {
		t.Fatalf("public launch lost native input: %v", err)
	}
	arguments, _ := json.Marshal(map[string]string{"CommandLine": launch.AgyInput.BootstrapCommand})
	metadata, _ := json.Marshal(map[string]any{"tool": map[string]any{"conversationId": "11111111-1111-4111-8111-111111111111", "stepIndex": 2, "toolCall": map[string]string{"name": "run_command", "argumentsJson": string(arguments)}}})
	source := agyinput.Source{WorkingDirectory: dir, SwarmSessionID: meta.ID, ConversationID: "11111111-1111-4111-8111-111111111111", ProjectID: "project", TrajectoryID: "trajectory", Address: "localhost:1234", CSRFToken: "own-public-launch-fixture", Executable: program, SourceMetadata: string(metadata)}
	if err := agyinput.NewClient(desc).Register(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	instance, _ := sk.sessionInstance(meta.ID)
	operation, _ := protocol.NewSemanticOperationID()
	origin := &protocol.MessageOrigin{Kind: "external", InstallationID: "own-installation", RequestID: operation, ActorID: "teammate"}
	if err := client.SemanticSend(operation, protocol.ComposerSendReq{Session: session, SessionInstance: instance, Text: "Public launch native message", Origin: origin}); err != nil {
		t.Fatal(err)
	}
	receipt, err := client.SemanticOperation(session, operation)
	if err != nil || receipt.State != "submitted" || receipt.Origin == nil || *receipt.Origin != *origin {
		t.Fatalf("public launch origin receipt: %+v %v", receipt, err)
	}
}
