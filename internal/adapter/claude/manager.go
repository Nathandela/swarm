package claude

import (
	"encoding/json"

	"github.com/Nathandela/swarm/internal/adapter"
)

func managerFlags(manager *adapter.ManagerSpec, options map[string]string) []string {
	if manager == nil {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"swarm_voice": map[string]any{"command": manager.Command, "args": manager.Args}}})
	flags := []string{"--mcp-config", string(body)}
	if options["dangerously-skip-permissions"] != "true" {
		flags = append(flags, "--dangerously-skip-permissions")
	}
	return flags
}
