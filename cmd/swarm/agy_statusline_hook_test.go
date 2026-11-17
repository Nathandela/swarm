package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/hookclient"
)

const agyStatusConversation = "10000000-0000-4000-8000-000000000011"
const agyFallbackConversation = "20000000-0000-4000-8000-000000000022"

func TestAgyStatusLineHook_ParsesAndPostsStateTitleAndConversationID(t *testing.T) {
	sock, next := hookSink(t)
	hookEnv(t, sock, nil)

	body := fmt.Sprintf(`{"agent_state":"reviewing","conversation_id":%q,"session_id":%q,"conversation_title":"Fix parser bug"}`, agyStatusConversation, agyStatusConversation)
	var output, diagnostics bytes.Buffer
	if code := runAgyStatusLineHook(nil, strings.NewReader(body), &output, &diagnostics); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, diagnostics.String())
	}

	cb := next()
	if cb.Event != "AgyStatusLine" || cb.SessionID != "sid-1" || cb.Token != "tok-abc" || cb.Sequence != 1 || len(cb.Raw) != 0 {
		t.Fatalf("unexpected callback metadata: %#v", cb)
	}
	if cb.Payload["agent_state"] != "reviewing" {
		t.Errorf("agent_state = %q, want %q", cb.Payload["agent_state"], "reviewing")
	}
	if cb.Payload["conversation_id"] != agyStatusConversation {
		t.Errorf("conversation_id = %q, want %q", cb.Payload["conversation_id"], agyStatusConversation)
	}
	if cb.Payload["conversation_title"] != "Fix parser bug" {
		t.Errorf("conversation_title = %q, want %q", cb.Payload["conversation_title"], "Fix parser bug")
	}
	if _, err := time.Parse(time.RFC3339Nano, cb.Payload["observed_at"]); err != nil {
		t.Errorf("observed_at = %q: %v", cb.Payload["observed_at"], err)
	}
	if output.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatalf("quiet observer output = %q, diagnostics = %q", output.String(), diagnostics.String())
	}
}

func TestAgyStatusLineHook_FallsBackToSessionIDAndDropsInvalidID(t *testing.T) {
	sock, next := hookSink(t)
	hookEnv(t, sock, nil)

	body1 := fmt.Sprintf(`{"agent_state":"working","session_id":%q}`, agyFallbackConversation)
	if code := runAgyStatusLineHook(nil, strings.NewReader(body1), io.Discard, io.Discard); code != 0 {
		t.Fatalf("first exit = %d", code)
	}
	cb1 := next()
	if cb1.Payload["conversation_id"] != agyFallbackConversation {
		t.Errorf("fallback conversation_id = %q, want %q", cb1.Payload["conversation_id"], agyFallbackConversation)
	}
	if cb1.Sequence != 1 {
		t.Errorf("first sequence = %d, want 1", cb1.Sequence)
	}

	body2 := `{"agent_state":"idle","conversation_id":"not-a-uuid","session_id":"also-bad"}`
	if code := runAgyStatusLineHook(nil, strings.NewReader(body2), io.Discard, io.Discard); code != 0 {
		t.Fatalf("second exit = %d", code)
	}
	cb2 := next()
	if cb2.Payload["agent_state"] != "idle" {
		t.Errorf("second agent_state = %q, want idle", cb2.Payload["agent_state"])
	}
	if _, ok := cb2.Payload["conversation_id"]; ok {
		t.Errorf("non-canonical conversation_id survived: %#v", cb2.Payload)
	}
	if cb2.Sequence != 2 {
		t.Errorf("second sequence = %d, want 2", cb2.Sequence)
	}
}

func TestAgyStatusLineHook_ChainsExistingCommandAndSurvivesDaemonTimeout(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "agysl-sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	hookEnv(t, socket, nil)
	body := `{"agent_state":"idle","conversation_id":"` + agyStatusConversation + `"}` + strings.Repeat(" ", hookStdinLimit+64)
	var output, diagnostics bytes.Buffer
	start := time.Now()
	code := runAgyStatusLineHook([]string{"cat; printf '|retained'; exit 5"}, strings.NewReader(body), &output, &diagnostics)
	if code != 5 {
		t.Fatalf("chained exit = %d, want 5 (stderr=%q)", code, diagnostics.String())
	}
	if output.String() != body+"|retained" {
		t.Fatalf("chained output length = %d, want %d", output.Len(), len(body)+len("|retained"))
	}
	if time.Since(start) > time.Second {
		t.Fatalf("daemon socket timeout held statusLine for %s", time.Since(start))
	}
}

func TestRunHook_DispatchesAgyStatusLine(t *testing.T) {
	sock, next := hookSink(t)
	hookEnv(t, sock, nil)

	body := fmt.Sprintf(`{"agent_state":"tool_use","conversation_id":%q}`, agyStatusConversation)
	var diagnostics bytes.Buffer
	if code := runHook([]string{"AgyStatusLine"}, strings.NewReader(body), &diagnostics); code != 0 {
		t.Fatalf("runHook exit = %d, stderr = %q", code, diagnostics.String())
	}
	cb := next()
	if cb.Event != "AgyStatusLine" || cb.Payload["agent_state"] != "tool_use" {
		t.Fatalf("unexpected callback from runHook: %#v", cb)
	}
	if os.Getenv(hookclient.EnvSessionID) != "sid-1" {
		t.Fatal("test environment mutated unexpectedly")
	}
}
