package main

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestVoiceMCPDispatchRequiresExactManagerContext(t *testing.T) {
	old := voiceMCPRun
	t.Cleanup(func() { voiceMCPRun = old })
	calls := 0
	voiceMCPRun = func(_ context.Context, socket, auth, conversation, session string, _ io.Writer) error {
		calls++
		if socket != "/private/service.sock" || auth != "/private/auth" || conversation != "conversation" || session != "manager" {
			t.Fatal("wrong manager context")
		}
		return nil
	}
	t.Setenv("SWARM_SESSION_ID", "manager")
	var out, errout bytes.Buffer
	args := []string{"voice", "mcp", "--socket", "/private/service.sock", "--auth-file", "/private/auth", "--conversation", "conversation"}
	if code := dispatch(args, &out, &errout); code != 0 || calls != 1 || out.Len() != 0 {
		t.Fatalf("code=%d calls=%d stdout=%q stderr=%q", code, calls, out.String(), errout.String())
	}
	t.Setenv("SWARM_SESSION_ID", "")
	if code := dispatch(args, &out, &errout); code == 0 || calls != 1 {
		t.Fatal("unscoped MCP process accepted")
	}
	if code := dispatch([]string{"voice", "mcp", "--socket", "/private/service.sock"}, &out, &errout); code == 0 || calls != 1 {
		t.Fatal("incomplete bridge config accepted")
	}
}

func TestVoiceMCPRejectsCredentialArgument(t *testing.T) {
	var out, errout bytes.Buffer
	if code := dispatch([]string{"voice", "mcp", "--api-key", "SENTINEL_PRIVATE_KEY"}, &out, &errout); code == 0 {
		t.Fatal("accepted credential in argv")
	}
	if bytes.Contains(out.Bytes(), []byte("SENTINEL_PRIVATE_KEY")) || bytes.Contains(errout.Bytes(), []byte("SENTINEL_PRIVATE_KEY")) {
		t.Fatal("credential argument echoed")
	}
}
