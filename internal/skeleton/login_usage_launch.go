package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nathandela/swarm/internal/logins"
)

// shortcut: wrap only common native settings, add project precedence support when needed.
func loginClaudeUsageArgs(argv []string, home, cwd string, env []string) []string {
	binary, err := os.Executable()
	if err != nil || !filepath.IsAbs(binary) {
		return argv
	}
	ownerHome, _ := os.UserHomeDir()
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "HOME="); ok {
			ownerHome = value
		}
	}
	if resolved, err := filepath.EvalSymlinks(ownerHome); err == nil {
		ownerHome = resolved
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		projectDir := filepath.Join(dir, ".claude")
		resolved, _ := filepath.EvalSymlinks(projectDir)
		// The ordinary native-home alias is the same user settings, not a project override.
		if resolved != home && dir != ownerHome {
			for _, name := range []string{"settings.json", "settings.local.json"} {
				_, present, err := logins.ReadClaudeStatusLine(filepath.Join(projectDir, name))
				if err != nil || present {
					return argv
				}
			}
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	line, present, err := logins.ReadClaudeStatusLine(filepath.Join(home, "settings.json"))
	if err != nil {
		return argv
	}
	var command string
	if present {
		var kind string
		if json.Unmarshal(line["type"], &kind) != nil || kind != "command" || json.Unmarshal(line["command"], &command) != nil || command == "" {
			return argv
		}
	} else {
		line = map[string]json.RawMessage{"type": json.RawMessage(`"command"`)}
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
	if index == -1 {
		return argv
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal([]byte(argv[index]), &settings) != nil || settings == nil {
		return argv
	}
	if _, present := settings["statusLine"]; present {
		return argv
	}
	// The fallback preserves an already loaded command if its source becomes unreadable.
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	line["command"], err = json.Marshal(quote(binary) + " hook SwarmUsage " + quote(command))
	if err != nil {
		return argv
	}
	settings["statusLine"], err = json.Marshal(line)
	if err != nil {
		return argv
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return argv
	}
	out := append([]string(nil), argv...)
	out[index] = string(raw)
	return out
}
