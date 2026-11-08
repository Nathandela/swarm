package claudeinput

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/managerproof"
)

func TestManagerNonceProjectionAndNativeCallProof(t *testing.T) {
	dir := shortDir(t)
	if _, err := InstallManager(dir, "conversation"); err != nil {
		t.Fatal(err)
	}
	b, err := Start(dir, "session", "2.1.319", 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	c := NewClient(b.Descriptor())
	ctx := context.Background()
	worker := Worker{NativeSessionID: "native", Generation: "sender", Version: "2.1.319"}
	guard := worker
	guard.Generation = "guard"
	_ = c.Guard(ctx, guard, false)
	_, _ = c.Exchange(ctx, worker, "", "", "")
	binding := managerproof.Binding{Session: "session", SessionInstance: "instance", ConversationID: "conversation", Provider: "claude", NativeSessionID: "native", NativeGeneration: "actual-native-process", ObservationGeneration: b.Descriptor().Generation + ":" + guard.Generation, LoginEpoch: "account"}
	if err := c.BindManager(ctx, worker, binding); err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("a", 64)
	message, err := c.SubmitManager(ctx, worker, "operation", "human text", managerproof.Admission{Binding: binding, Nonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("human text"))
	if message.Hash != hex.EncodeToString(digest[:]) {
		t.Fatal("human digest replaced by native projection digest")
	}
	queued, err := c.Exchange(ctx, worker, "", "", "")
	if err != nil || queued == nil || queued.NativeInputID != nonce {
		t.Fatalf("queue: %+v %v", queued, err)
	}
	if err := c.call(ctx, "/manager-prompt", request{Worker: guard, Text: queued.Text}, nil); err != nil {
		t.Fatal(err)
	}
	projection := managerProjection(b.Descriptor().SenderName, queued.Text)
	if err := c.call(ctx, "/manager-append", request{Worker: guard, NativeInputID: nonce, MessageID: "message", Projection: projection}, nil); err != nil {
		t.Fatal(err)
	}
	turn := Turn{TurnID: "main", State: "running", NativeInputID: nonce, NativeMessageID: "message"}
	if err := c.call(ctx, "/turn", request{Worker: guard, Turn: &turn, Projection: projection}, nil); err != nil {
		t.Fatal(err)
	}
	call := managerTool{CallID: "native-call", TurnID: "main", ToolName: "publish"}
	if err := c.call(ctx, "/manager-tool-step", request{Worker: guard, Call: &call}, nil); err != nil {
		t.Fatal(err)
	}
	call.Arguments = []byte(`{"text":"phrase","sequence":1}`)
	if err := c.call(ctx, "/manager-tool", request{Worker: guard, Call: &call}, nil); err != nil {
		t.Fatal(err)
	}
	hash, _ := managerproof.ArgumentsHash(call.Arguments)
	req := managerproof.CallRequest{Binding: binding, ToolName: "publish", ArgumentsHash: hash, HostMeta: []byte(`{"claudecode/toolUseId":"native-call"}`)}
	proof, err := c.ResolveManagerCall(ctx, worker, req)
	if err != nil || proof.NativeInputID != nonce || proof.NativeTurnID != "main" {
		t.Fatalf("native proof: %+v %v", proof, err)
	}
	if err := c.call(ctx, "/manager-prompt", request{Worker: guard, Text: queued.Text}, nil); err == nil {
		t.Fatal("repeated machine envelope admitted")
	}
	if err := c.Revoke(ctx, worker); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResolveManagerCall(ctx, worker, req); err == nil {
		t.Fatal("late call borrowed retired manager")
	}
}
func TestManagerInstallUniqueAndWorkersUnchanged(t *testing.T) {
	a, b := shortDir(t), shortDir(t)
	_, _ = InstallManager(a, "conversation")
	_, _ = InstallManager(b, "conversation")
	one, err := readManagerSetup(a)
	if err != nil {
		t.Fatal(err)
	}
	two, err := readManagerSetup(b)
	if err != nil || one.SenderName == two.SenderName {
		t.Fatal("manager source identity reused across invocations")
	}
	_, _ = Install(shortDir(t))
	r, _ := managerproof.NewRegistry(managerproof.Binding{Session: "s", SessionInstance: "i", ConversationID: "c", Provider: "claude", NativeSessionID: "n", NativeGeneration: "g", ObservationGeneration: "o", LoginEpoch: "l"})
	r.Retire()
	_, err = r.Snapshot()
	if !errors.Is(err, managerproof.ErrStale) {
		t.Fatal(err)
	}
}

func TestManagerProjectionUsesQualifiedNativeDisplayName(t *testing.T) {
	// Installed Claude 2.1.319 truncates the plugin display name at 23 ASCII
	// characters. Authentication still uses the complete unique origin name.
	sender := "swarm-manager-input-0123456789abcdef0123456789abcdef"
	want := "The swarm-manager-input-012… plugin sent a message:\nenvelope\n\nThis is how Claude Code surfaces a prompt a plugin submits between turns — it starts this turn in the user's place. Address the message above."
	if got := managerProjection(sender, "envelope"); got != want {
		t.Fatal("native projection does not match the qualified display wrapper")
	}
}
