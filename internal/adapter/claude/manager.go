package claude

import (
	"encoding/json"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
)

func managerFlags(manager *adapter.ManagerSpec, options map[string]string) []string {
	if manager == nil {
		return nil
	}
	server := map[string]any{"command": manager.Command, "args": manager.Args}
	if manager.NativeCLI {
		server["env"] = map[string]string{"SWARM_MANAGER_VM_CLI": "1"}
	}
	body, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"swarm_voice": server}})
	flags := []string{"--mcp-config", string(body)}
	if manager.NativeCLI {
		var allowed []string
		for _, tool := range adapter.DiscordTransportTools() {
			allowed = append(allowed, "mcp__swarm_voice__"+tool)
		}
		flags = append(flags, "--allowedTools="+strings.Join(allowed, ","))
	} else if options["dangerously-skip-permissions"] != "true" {
		flags = append(flags, "--dangerously-skip-permissions")
	}
	return flags
}
