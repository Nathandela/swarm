package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/logins"
)

type claudeUsageWindow struct {
	Used  *float64 `json:"used_percentage"`
	Reset *int64   `json:"resets_at"`
}

func claudeUsagePayload(body []byte, now time.Time) map[string]string {
	var input struct {
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
		Limits    struct {
			FiveHour *claudeUsageWindow `json:"five_hour"`
			SevenDay *claudeUsageWindow `json:"seven_day"`
		} `json:"rate_limits"`
	}
	if len(body) > hookStdinLimit || json.Unmarshal(body, &input) != nil || input.AgentID != "" || !adapter.IsCanonicalConversationID(input.SessionID) {
		return nil
	}
	out := map[string]string{"session_id": input.SessionID, "observed_at": now.UTC().Format(time.RFC3339Nano)}
	for name, window := range map[string]*claudeUsageWindow{"five_hour": input.Limits.FiveHour, "seven_day": input.Limits.SevenDay} {
		if window == nil || window.Used == nil || *window.Used < 0 || *window.Used > 100 || window.Reset == nil || *window.Reset <= now.Unix() || *window.Reset > now.Add(8*24*time.Hour).Unix() {
			continue
		}
		out[name+"_used_percentage"] = strconv.FormatFloat(*window.Used, 'f', -1, 64)
		out[name+"_resets_at"] = strconv.FormatInt(*window.Reset, 10)
	}
	if len(out) == 2 {
		return nil
	}
	return out
}

func postUsagePayload(payload map[string]string) {
	if payload == nil {
		return
	}
	cb := engine.Callback{SessionID: os.Getenv(hookclient.EnvSessionID), Token: os.Getenv(hookclient.EnvToken), Event: "SwarmUsage", Payload: payload}
	if cb.SessionID == "" || cb.Token == "" {
		return
	}
	// Advisory telemetry must never hold up the native status line or enter its durable spool.
	deadline := time.Now().Add(100 * time.Millisecond)
	conn, err := net.DialTimeout("unix", os.Getenv(hookclient.EnvSocket), time.Until(deadline))
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(deadline)
	_ = json.NewEncoder(conn).Encode(cb)
}

func runUsageHook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	body, _ := io.ReadAll(io.LimitReader(stdin, hookStdinLimit+1))
	postUsagePayload(claudeUsagePayload(body, time.Now()))
	command := ""
	if len(args) == 1 {
		command = args[0]
	}
	if home := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(home) {
		line, present, err := logins.ReadClaudeStatusLine(filepath.Join(home, "settings.json"))
		if err == nil {
			if !present {
				command = ""
			} else {
				var kind, current string
				if json.Unmarshal(line["type"], &kind) == nil && kind == "command" && json.Unmarshal(line["command"], &current) == nil {
					command = current
				}
			}
		}
	}
	if command == "" || strings.HasPrefix(command, "swarm hook SwarmUsage") {
		return 0
	}
	// Claude's POSIX status-line runner uses spawn(shell:true), whose shell is /bin/sh.
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
