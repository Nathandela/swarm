package skeleton

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/protocol"
)

const maxAgySettingsBytes = 1 << 20

func agySupportsStatusLine(version string) bool {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if v == "" {
		return false
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return false
	}
	nums := make([]int, len(parts))
	for i, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return false
		}
		nums[i] = n
	}
	return nums[0] > 1 || (nums[0] == 1 && nums[1] >= 4)
}

func agyStatusLineArgs(argv, env []string) []string {
	binary, err := os.Executable()
	if err != nil || !filepath.IsAbs(binary) {
		return argv
	}
	return agyStatusLineArgsWithBinary(argv, env, binary)
}

func agyStatusLineArgsWithBinary(argv, env []string, binary string) []string {
	if len(argv) == 0 || !filepath.IsAbs(binary) {
		return argv
	}
	line := agyStatusLineConfig(env, binary)
	if line == nil {
		return argv
	}
	index := -1
	for i := 1; i+1 < len(argv); i++ {
		if argv[i] == "--settings" {
			if index != -1 {
				return argv
			}
			index = i + 1
			i++
		}
	}
	if index != -1 {
		var settings map[string]json.RawMessage
		if json.Unmarshal([]byte(argv[index]), &settings) != nil || settings == nil {
			return argv
		}
		if _, present := settings["statusLine"]; present {
			return argv
		}
		rawLine, err := json.Marshal(line)
		if err != nil {
			return argv
		}
		settings["statusLine"] = rawLine
		raw, err := json.Marshal(settings)
		if err != nil {
			return argv
		}
		out := append([]string(nil), argv...)
		out[index] = string(raw)
		return out
	}
	raw, err := json.Marshal(struct {
		StatusLine map[string]any `json:"statusLine"`
	}{StatusLine: line})
	if err != nil {
		return argv
	}
	out := make([]string, 0, len(argv)+2)
	out = append(out, argv[0], "--settings", string(raw))
	out = append(out, argv[1:]...)
	return out
}

func agyStatusLineConfig(env []string, binary string) map[string]any {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	defaultLine := map[string]any{
		"type":               "command",
		"command":            quote(binary) + " hook AgyStatusLine ''",
		"enabled":            true,
		"stack_with_default": true,
	}
	home := homeFromEnv(env)
	if home == "" {
		return defaultLine
	}
	settingsPath := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	raw := readAgySettingsBounded(settingsPath)
	if len(raw) == 0 {
		return defaultLine
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return defaultLine
	}
	slRaw, ok := root["statusLine"]
	if !ok {
		return defaultLine
	}
	var line map[string]any
	if json.Unmarshal(slRaw, &line) != nil || line == nil {
		return defaultLine
	}
	if enabled, hasEnabled := line["enabled"].(bool); hasEnabled && !enabled {
		return defaultLine
	}
	kind, _ := line["type"].(string)
	cmd, _ := line["command"].(string)
	if kind != "command" || cmd == "" || strings.Contains(cmd, " hook AgyStatusLine ") {
		return defaultLine
	}
	line["command"] = quote(binary) + " hook AgyStatusLine " + quote(cmd)
	return line
}

func homeFromEnv(env []string) string {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "HOME="); ok && value != "" {
			return value
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

func readAgySettingsBounded(path string) []byte {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxAgySettingsBytes))
	if err != nil {
		return nil
	}
	return data
}

func (d *Daemon) observeAgyStatusLine(cb engine.Callback) {
	if d.core != nil {
		if convID := cb.Payload["conversation_id"]; adapter.IsCanonicalConversationID(convID) {
			_ = d.core.SetConversationID(cb.SessionID, convID)
		}
		d.observeAgyTitle(cb.SessionID, cb.Payload["conversation_title"], cb.Payload["observed_at"])
	}
	turn, interaction, ok := agyStatusDims(cb.Payload["agent_state"])
	if ok && d.eng != nil && cb.Sequence > 0 {
		_ = d.eng.HandleCallback(engine.Callback{
			SessionID: cb.SessionID,
			Token:     cb.Token,
			Sequence:  cb.Sequence,
			Event:     "AgyStatusLine",
			Payload: map[string]string{
				engine.PayloadKeyTurn:        turn,
				engine.PayloadKeyInteraction: interaction,
			},
		})
	}
}

func (d *Daemon) observeAgyTitle(sessionID, rawTitle, rawObservedAt string) {
	if rawTitle == "" || d.core == nil {
		return
	}
	name := protocol.SanitizeName(rawTitle)
	if name == "" {
		return
	}
	d.agyTitlesMu.Lock()
	if d.agyTitles == nil {
		d.agyTitles = make(map[string]string)
	}
	if d.agyTitles[sessionID] == name {
		d.agyTitlesMu.Unlock()
		return
	}
	d.agyTitles[sessionID] = name
	d.agyTitlesMu.Unlock()

	at, err := time.Parse(time.RFC3339Nano, rawObservedAt)
	if err != nil {
		at = time.Now()
	}
	m, ok := d.core.Get(sessionID)
	if !ok || !at.After(m.NameSetAt) || name == m.Name {
		return
	}
	if err := d.core.RenameAt(sessionID, name, at); err == nil && d.api != nil {
		d.api.pokeWatch()
	}
}

func agyStatusDims(state string) (turn, interaction string, ok bool) {
	switch state {
	case "working", "tool_use", "initializing":
		return "active", "none", true
	case "idle":
		return "idle", "none", true
	case "reviewing":
		return "idle", "permission", true
	case "authenticating":
		return "idle", "prompt", true
	case "error":
		return "idle", "error", true
	default:
		return "", "", false
	}
}
