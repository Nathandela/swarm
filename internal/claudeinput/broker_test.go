package claudeinput

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeInputRequiresExactLiveWorkerAndNeverReissuesClaim(t *testing.T) {
	b, err := Start(shortDir(t), "session", "2.1.296", 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	client := NewClient(b.Descriptor())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Ready(ctx); err == nil {
		t.Fatal("no native worker was accepted as input capable")
	}
	worker := Worker{NativeSessionID: "native-session", Generation: "worker-generation", Version: "2.1.296"}
	if _, err := client.Exchange(ctx, worker, "", "", ""); err != nil {
		t.Fatal(err)
	}
	proof, err := client.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Submit(ctx, proof, "operation", "message"); err != nil {
		t.Fatal(err)
	}
	msg, err := client.Exchange(ctx, worker, "", "", "")
	if err != nil || msg == nil || msg.OperationID != "operation" {
		t.Fatalf("claim: %+v %v", msg, err)
	}
	msg, err = client.Exchange(ctx, worker, "", "", "")
	if err != nil || msg != nil {
		t.Fatal("native request was replayed before an outcome")
	}
	if _, err := client.Submit(ctx, proof, "operation", "changed"); err == nil {
		t.Fatal("changed payload reused a claim")
	}
	if _, err := client.Exchange(ctx, worker, "operation", "submitted", ""); err != nil {
		t.Fatal(err)
	}
	state, err := client.Operation(ctx, proof, "operation")
	if err != nil || state.State != "submitted" {
		t.Fatalf("submitted only receipt: %+v %v", state, err)
	}
	if _, err := client.Exchange(ctx, worker, "operation", "rejected", "native queue dropped request"); err != nil {
		t.Fatal(err)
	}
	state, err = client.Operation(ctx, proof, "operation")
	if err != nil || state.State != "rejected" {
		t.Fatalf("native refusal lost: %+v %v", state, err)
	}
}

func TestNativeInputRefusesWrongTokenVersionAndReplacedWorker(t *testing.T) {
	b, err := Start(shortDir(t), "session", "2.1.296", 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	ctx := context.Background()
	desc := b.Descriptor()
	wrong := desc
	wrong.Token = "wrong"
	if _, err := NewClient(wrong).Exchange(ctx, Worker{NativeSessionID: "native", Generation: "one", Version: "2.1.296"}, "", "", ""); err == nil {
		t.Fatal("wrong token registered worker")
	}
	client := NewClient(desc)
	if _, err := client.Exchange(ctx, Worker{NativeSessionID: "native", Generation: "one", Version: "2.1.297"}, "", "", ""); err == nil {
		t.Fatal("unqualified runtime registered")
	}
	first := Worker{NativeSessionID: "native", Generation: "one", Version: "2.1.296"}
	_, _ = client.Exchange(ctx, first, "", "", "")
	proof, _ := client.Ready(ctx)
	_, _ = client.Submit(ctx, proof, "operation", "exact")
	_, _ = client.Exchange(ctx, first, "", "", "")
	second := first
	second.Generation = "two"
	if _, err := client.Exchange(ctx, second, "", "", ""); err == nil {
		t.Fatal("changed plugin worker replaced the native proof")
	}
	if _, err := client.Submit(ctx, second, "later", "later"); err == nil {
		t.Fatal("stale worker proof admitted input")
	}
	if _, err := client.Exchange(ctx, second, "operation", "submitted", ""); err == nil {
		t.Fatal("superseded worker settled new generation")
	}
	state, err := client.Operation(ctx, proof, "operation")
	if err != nil || state.State != "executing" {
		t.Fatalf("lost worker input was not uncertain: %+v %v", state, err)
	}
}

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "ci")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestPluginUsesNativePromptQueueWithoutPermissionHooks(t *testing.T) {
	dir := shortDir(t)
	root, err := Install(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "hooks/input.js"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "__CONFIG_PATH__") || !strings.Contains(text, "$.prompt.submit({ text: message.text })") || !strings.Contains(text, "socketPath: config.socket_path") {
		t.Fatal("plugin does not use the exact native prompt broker")
	}
	for _, unsafe := range []string{"tool.check", "asUser: true", "$.tool.call"} {
		if strings.Contains(text, unsafe) {
			t.Fatalf("plugin unexpectedly gains %s", unsafe)
		}
	}
}

func TestNativeInputPreservesEscapedBodyAndAckCannotConsumeLaterMessage(t *testing.T) {
	b, err := Start(shortDir(t), "session", "2.1.296", 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	client := NewClient(b.Descriptor())
	ctx := context.Background()
	worker := Worker{NativeSessionID: "native", Generation: "worker", Version: "2.1.296"}
	_, _ = client.Exchange(ctx, worker, "", "", "")
	text := strings.Repeat("<", 4096)
	if _, err := client.Submit(ctx, worker, "one", text); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Submit(ctx, worker, "two", "later"); err != nil {
		t.Fatal(err)
	}
	first, err := client.Exchange(ctx, worker, "", "", "")
	if err != nil || first == nil || first.Text != text {
		t.Fatalf("escaped input: %+v %v", first, err)
	}
	ack, err := client.Exchange(ctx, worker, "one", "submitted", "")
	if err != nil || ack != nil {
		t.Fatal("ack consumed another queued input")
	}
	second, err := client.Exchange(ctx, worker, "", "", "")
	if err != nil || second == nil || second.OperationID != "two" {
		t.Fatalf("later input lost: %+v %v", second, err)
	}
}

func TestQualifiedVMVersionsUseExactNativeWorker(t *testing.T) {
	for _, version := range []string{"2.1.310", "2.1.311"} {
		t.Run(version, func(t *testing.T) {
			b, err := Start(shortDir(t), "qualified-vm", version, 123, 456)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = b.Close() }()
			client := NewClient(b.Descriptor())
			worker := Worker{NativeSessionID: "own-vm-session", Generation: "own-worker", Version: version}
			if _, err := client.Exchange(context.Background(), worker, "", "", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := client.Submit(context.Background(), worker, "vm-operation", "exact VM message"); err != nil {
				t.Fatal(err)
			}
			message, err := client.Exchange(context.Background(), worker, "", "", "")
			if err != nil || message == nil || message.Text != "exact VM message" {
				t.Fatalf("native VM admission: %+v %v", message, err)
			}
		})
	}
	for _, version := range []string{"2.1.309", "2.1.312", "2.1.311-beta.1"} {
		if SupportedVersion(version) {
			t.Fatalf("unqualified version %q enabled", version)
		}
	}
}
