package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/hookclient"
)

func agyStatusLinePayload(body []byte, now time.Time) map[string]string {
	if len(body) > hookStdinLimit {
		return nil
	}
	var input struct {
		AgentState        string `json:"agent_state"`
		ConversationID    string `json:"conversation_id"`
		SessionID         string `json:"session_id"`
		ConversationTitle string `json:"conversation_title"`
	}
	if json.Unmarshal(body, &input) != nil {
		return nil
	}
	out := make(map[string]string, 4)
	if state := strings.TrimSpace(input.AgentState); state != "" {
		out["agent_state"] = state
	}
	convID := strings.TrimSpace(input.ConversationID)
	if convID == "" {
		convID = strings.TrimSpace(input.SessionID)
	}
	if adapter.IsCanonicalConversationID(convID) {
		out["conversation_id"] = convID
	}
	if title := strings.TrimSpace(input.ConversationTitle); title != "" {
		out["conversation_title"] = title
		out["observed_at"] = now.UTC().Format(time.RFC3339Nano)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func postAgyStatusLinePayload(payload map[string]string) {
	if payload == nil {
		return
	}
	sock := os.Getenv(hookclient.EnvSocket)
	if sock == "" {
		return
	}
	cb, err := hookclient.FromEnv(os.Getenv, "AgyStatusLine", payload)
	if err != nil || cb.SessionID == "" || cb.Token == "" {
		return
	}
	// Dial the daemon socket with a short timeout and bypass the durable hook
	// spool so frequent status-bar updates never block agy's TUI or grow spool files.
	deadline := time.Now().Add(100 * time.Millisecond)
	conn, err := net.DialTimeout("unix", sock, time.Until(deadline))
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(deadline)
	_ = json.NewEncoder(conn).Encode(cb)
}

func runAgyStatusLineHook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	body, _ := io.ReadAll(io.LimitReader(stdin, hookStdinLimit+1))
	postAgyStatusLinePayload(agyStatusLinePayload(body, time.Now()))
	command := ""
	if len(args) == 1 {
		command = args[0]
	}
	if command == "" || strings.HasPrefix(command, "swarm hook AgyStatusLine") || strings.Contains(command, " hook AgyStatusLine ") {
		return 0
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = io.MultiReader(bytes.NewReader(body), stdin), stdout, stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		return 1
	}
	return 0
}
