package agyinput

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (*Broker, *Client, Source) {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "ai")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfg := Config{SessionID: "swarm-session", Executable: "/native/agy", Version: "1.4.0", Fingerprint: "stamp", Cwd: dir, BootstrapCommand: "swarm native-input register-agy " + filepath.Join(dir, DescriptorFile)}
	b, err := Start(dir, cfg, 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	b.fingerprint = func(string) (string, error) { return "stamp", nil }
	b.run = func(_ context.Context, _ Source, args ...string) ([]byte, error) {
		if args[0] == "--version" {
			return []byte("1.4.0\n"), nil
		}
		if args[1] == "get-conversation-metadata" {
			return []byte(`{"response":{"conversationMetadata":{"metadata":{"rootConversationId":"11111111-1111-4111-8111-111111111111","projectId":"project"}}}}`), nil
		}
		return json.Marshal(map[string]any{"response": map[string]any{"sendMessage": map[string]string{"recipientId": args[2], "content": args[3]}}})
	}
	metadata, _ := json.Marshal(map[string]any{"tool": map[string]any{"conversationId": "11111111-1111-4111-8111-111111111111", "stepIndex": 2, "toolCall": map[string]any{"name": "run_command", "argumentsJson": `{"Cwd":"` + dir + `","CommandLine":"` + cfg.BootstrapCommand + `"}`}}})
	source := Source{SwarmSessionID: cfg.SessionID, ConversationID: "11111111-1111-4111-8111-111111111111", ProjectID: "project", TrajectoryID: "trajectory", Address: "localhost:1234", CSRFToken: "native-token", Executable: cfg.Executable, SourceMetadata: string(metadata)}
	return b, NewClient(b.Descriptor()), source
}

func TestAgyRegistrationRequiresOwnNativeSource(t *testing.T) {
	_, client, source := fixture(t)
	ctx := context.Background()
	if _, err := client.Ready(ctx); err == nil {
		t.Fatal("unregistered source advertised native input")
	}
	for _, change := range []func(*Source){
		func(s *Source) { s.SwarmSessionID = "another-session" }, func(s *Source) { s.ConversationID = "22222222-2222-4222-8222-222222222222" }, func(s *Source) { s.Address = "remote.invalid:443" }, func(s *Source) { s.Executable = "/other/agy" }, func(s *Source) { s.ProjectID = "other-project" }, func(s *Source) { s.SourceMetadata = "{}" },
	} {
		invalid := source
		change(&invalid)
		if err := client.Register(ctx, invalid); err == nil {
			t.Fatalf("invalid source registered: %+v", invalid)
		}
	}
	if err := client.Register(ctx, source); err != nil {
		t.Fatal(err)
	}
	proof, err := client.Ready(ctx)
	if err != nil || proof.ConversationID != source.ConversationID {
		t.Fatalf("exact proof: %+v %v", proof, err)
	}
}

func TestAgyLostReplyCannotReplayAndReceiptBindsExactPayload(t *testing.T) {
	b, client, source := fixture(t)
	ctx := context.Background()
	if err := client.Register(ctx, source); err != nil {
		t.Fatal(err)
	}
	proof, _ := client.Ready(ctx)
	calls := 0
	b.run = func(_ context.Context, _ Source, args ...string) ([]byte, error) {
		calls++
		return nil, errors.New("reply lost after native write")
	}
	for range 2 {
		result, err := client.Submit(ctx, proof, "unknown-operation", "exact text")
		if err != nil || result.State != "uncertain" {
			t.Fatalf("unknown native result: %+v %v", result, err)
		}
	}
	if calls != 1 {
		t.Fatalf("lost reply caused %d native sends", calls)
	}
	if _, err := client.Submit(ctx, proof, "unknown-operation", "changed"); err == nil {
		t.Fatal("different text reused native receipt")
	}
}

func TestAgyBootstrapPreservesOwnerPromptAndAddsNoPermissionBypass(t *testing.T) {
	command := "/owned/swarm native-input register-agy /owned/agy-input.json"
	original := "Keep all of my\noriginal words <exactly>."
	got := LaunchArgs([]string{"agy", "--model", "model", "--prompt-interactive", original}, command)
	if strings.Join(got, "\x00") == "" || !strings.Contains(got[len(got)-1], command) || !strings.HasSuffix(got[len(got)-1], original) {
		t.Fatalf("owner prompt changed: %q", got)
	}
	if strings.Contains(strings.Join(got, " "), "dangerously") {
		t.Fatal("registration bypassed native permission")
	}
}

func TestAgyNativeRejectsUnencodableTextBeforeEffect(t *testing.T) {
	b, client, source := fixture(t)
	ctx := context.Background()
	if err := client.Register(ctx, source); err != nil {
		t.Fatal(err)
	}
	proof, _ := client.Ready(ctx)
	calls := 0
	b.run = func(context.Context, Source, ...string) ([]byte, error) { calls++; return nil, nil }
	if _, err := client.Submit(ctx, proof, "nul", "cannot\x00pass argv"); !errors.Is(err, ErrRejected) {
		t.Fatalf("unencodable native input was not a definite refusal: %v", err)
	}
	if calls != 0 {
		t.Fatal("unencodable input reached native execution")
	}
}

func TestAgyBootstrapCommandQuotesApostrophes(t *testing.T) {
	want := "'/tmp/swarm'\\''helper' native-input register-agy '/tmp/space path/agy-input.json'"
	if got := BootstrapCommand("/tmp/swarm'helper", "/tmp/space path/agy-input.json"); got != want {
		t.Fatalf("unsafe bootstrap quoting: %q want %q", got, want)
	}
}

func TestAgyNewSourceSchemaRequiresKernelWorkingDirectory(t *testing.T) {
	b, _, source := fixture(t)
	ctx := context.Background()
	var metadata map[string]any
	if err := json.Unmarshal([]byte(source.SourceMetadata), &metadata); err != nil {
		t.Fatal(err)
	}
	tool := metadata["tool"].(map[string]any)["toolCall"].(map[string]any)
	tool["argumentsJson"] = `{"CommandLine":"` + b.desc.BootstrapCommand + `"}`
	raw, _ := json.Marshal(metadata)
	source.SourceMetadata = string(raw)
	source.WorkingDirectory = b.desc.Cwd
	if b.validate(ctx, source) == nil {
		t.Fatal("legacy1.4.0 source silently lost its required native Cwd field")
	}
	b.desc.Version = "1.4.7"
	previous := b.run
	b.run = func(ctx context.Context, source Source, args ...string) ([]byte, error) {
		if args[0] == "--version" {
			return []byte("1.4.7\n"), nil
		}
		return previous(ctx, source, args...)
	}
	if err := b.validate(ctx, source); err != nil {
		t.Fatal(err)
	}
	source.WorkingDirectory = t.TempDir()
	if b.validate(ctx, source) == nil {
		t.Fatal("different helper workspace claimed the native owner")
	}
}
