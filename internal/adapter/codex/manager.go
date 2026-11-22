package codex

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
)

// SupportsNativeTurns admits stable releases with the manager input and MCP provenance baseline.
func SupportsNativeTurns(version string) bool {
	return adapter.StableVersionAtLeast(version, "0.167.0")
}

func managerFlags(manager *adapter.ManagerSpec) []string {
	if manager == nil {
		return nil
	}
	args, _ := json.Marshal(manager.Args)
	if manager.NativeCLI {
		var approvals []string
		for _, tool := range adapter.DiscordTransportTools() {
			approvals = append(approvals, tool+`={approval_mode="approve"}`)
		}
		return []string{"-c", "mcp_servers.swarm_voice={command=" + strconv.Quote(manager.Command) + ",args=" + string(args) + `,env_vars=["SWARM_SESSION_ID"],env={SWARM_MANAGER_VM_CLI="1"},required=true,tools={` + strings.Join(approvals, ",") + "}}"}
	}
	return []string{"-c", "mcp_servers.swarm_voice={command=" + strconv.Quote(manager.Command) + ",args=" + string(args) + `,env_vars=["SWARM_SESSION_ID"],required=true}`, "-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`}
}

func managerOverride(value string) bool {
	for _, key := range []string{"mcp_servers.swarm_voice", "approval_policy", "sandbox_mode"} {
		if strings.HasPrefix(value, key+"=") {
			return true
		}
	}
	return false
}
