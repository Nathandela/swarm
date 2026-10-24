package loginworker

import (
	"bytes"
	"encoding/json"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClaudeAuthInitializeRoutingRequiresExactUnambiguousKeys(t *testing.T) {
	valid := `{"type":"control_response","response":{"subtype":"success","request_id":"swarm-auth-init","response":{"commands":[]}}}`
	if !claudeAuthInitialized([]byte(valid + "\n")) {
		t.Fatal("actual initialize success rejected")
	}
	for _, raw := range []string{
		strings.Replace(valid, `"type":`, `"Type":`, 1),
		strings.Replace(valid, `"subtype":"success"`, `"subtype":"error","subtype":"success"`, 1),
		strings.Replace(valid, `"request_id":`, `"Request_ID":`, 1),
		strings.Replace(valid, "swarm-auth-init", "foreign", 1),
		strings.Replace(valid, "control_response", "assistant", 1),
	} {
		if claudeAuthInitialized([]byte(raw + "\n")) {
			t.Fatalf("ambiguous/non-initialize event accepted: %s", raw)
		}
	}
}

func TestClaudeAuthArgumentsCannotSubmitUserTurn(t *testing.T) {
	args := claudeAuthArguments()
	joined := strings.Join(args, "\x00")
	for _, required := range []string{"--safe-mode", "--await-claim", "--strict-mcp-config", "--tools"} {
		if !strings.Contains(joined, required) {
			t.Fatal("missing qualified native restriction")
		}
	}
	for _, forbidden := range []string{"--resume", "--continue", "--dangerously-skip-permissions"} {
		if strings.Contains(joined, forbidden) {
			t.Fatal("turn-producing/recovery argument")
		}
	}
	if args[len(args)-1] != "" {
		t.Fatal("trailing prompt")
	}
}

func TestClaudeAuthConfigRejectsBeforeCreatingState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 8193), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RunClaudeAuthConfig(t.Context(), path); err == nil {
		t.Fatal("oversized config accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("unclaimed config produced state")
	}
}

func TestClaudeAuthUnboundRevocationReleasesGrantWithBoundedMetadataDiagnostic(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("advanced helper profile is Linux-only; do not touch Darwin Keychain")
	}
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := logins.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-access", "refreshToken": "synthetic-refresh", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "scopes": []string{"user:profile", "user:inference"}}})
	snapshot, login, err := store.Add(snapshot.Revision, logins.ProviderClaude, "synthetic", logins.Credential{Auth: auth, Identity: json.RawMessage(`{"oauthAccount":{"accountUuid":"synthetic-B","organizationUuid":"synthetic-org-B"},"userID":"synthetic-B"}`)})
	if err != nil {
		t.Fatal(err)
	}
	ref, created, err := store.BeginClaudeAuth(snapshot.Revision, login.ID)
	if err != nil || !created {
		t.Fatalf("prepare: %v %v", created, err)
	}
	result, err := RecoverClaudeAuth(t.Context(), ClaudeAuthConfig{StateRoot: state, Ref: ref})
	if err != nil || !result.Retired || !result.MetadataCleanupUnknown {
		t.Fatalf("unadmitted auth intent held for harmless unknown metadata: %+v %v", result, err)
	}
	snapshot, err = store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	next, created, err := store.BeginClaudeAuth(snapshot.Revision, login.ID)
	if err != nil || !created || next.Nonce == ref.Nonce {
		t.Fatalf("no-admission revocation permanently excluded grant: %v %v", created, err)
	}
	started, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ClaimClaudeAuthAttempt(ref, processcontain.Identity{PID: os.Getpid(), StartTime: started}); err == nil {
		t.Fatal("old unbound worker claimed replacement operation")
	}
}

type claudeAuthPipeGate struct {
	sink             *boundedBuffer
	entered, release chan struct{}
	once             sync.Once
}

func (g *claudeAuthPipeGate) Write(raw []byte) (int, error) {
	g.once.Do(func() {
		split := min(3, len(raw))
		_, _ = g.sink.Write(raw[:split])
		close(g.entered)
		<-g.release
		_, _ = g.sink.Write(raw[split:])
	})
	return len(raw), nil
}
func TestClaudeAuthFinalBoundsWaitForExecPipeCopies(t *testing.T) {
	output := &boundedBuffer{limit: 8}
	gate := &claudeAuthPipeGate{sink: output, entered: make(chan struct{}), release: make(chan struct{})}
	cmd := exec.Command("/bin/sh", "-c", "printf acknowledgement; read -r gate")
	cmd.Stdout = gate
	cmd.Stderr = io.Discard
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	child, err := startChild(cmd)
	if err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	var release sync.Once
	t.Cleanup(func() {
		_ = input.Close()
		release.Do(func() { close(gate.release) })
		_ = child.cmd.Process.Kill()
		select {
		case <-child.done:
		case <-time.After(time.Second):
			t.Error("fixture output process did not finish")
		}
	})
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("fixture never supplied candidate prefix")
	}
	if _, overflow := output.Bytes(); overflow {
		t.Fatal("tail unexpectedly copied before release")
	}
	_ = input.Close() // Native exits; os/exec's output copy remains blocked.
	done := make(chan error, 1)
	go func() { done <- waitClaudeAuthOutput(child) }()
	select {
	case err := <-done:
		t.Fatalf("pipe drain declared complete before final bytes: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	release.Do(func() { close(gate.release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("bounded pipe drain stalled")
	}
	if _, overflow := output.Bytes(); !overflow {
		t.Fatal("late output overflow invisible at final bounds check")
	}
}

func TestClaudeAuthUnpublishedPIDCannotBlockBeforeScopeContainment(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exec 3<&0; (read -r gate <&3; printf tail) & exit 0")
	cmd.Stdout = &boundedBuffer{limit: 32}
	cmd.Stderr = io.Discard
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	type observed struct {
		child *nativeChild
		err   error
	}
	returned := make(chan observed, 1)
	go func() {
		child, err := observeClaudeAuthChild(cmd, func(int) (int64, error) { return 0, ErrUnavailable })
		returned <- observed{child, err}
	}()
	var child *nativeChild
	select {
	case result := <-returned:
		child = result.child
		if result.err == nil || child.identity.PID != 0 {
			t.Fatal("unpublished PID invented identity")
		}
	case <-time.After(time.Second):
		t.Fatal("PID failure blocked before scope containment could run")
	}
	select {
	case <-child.done:
		t.Fatal("retained-pipe fixture exited prematurely")
	default:
	}
	_ = input.Close() // Release the exact synthetic descendant, not native proof.
	if err := waitClaudeAuthOutput(child); err != nil {
		t.Fatal(err)
	}
}
