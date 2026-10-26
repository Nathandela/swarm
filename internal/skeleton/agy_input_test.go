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
