package codex

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
)

// SupportsNativeTurns names the version qualified for manager input and MCP provenance.
func SupportsNativeTurns(version string) bool { return version == "0.167.0" }

func managerFlags(manager *adapter.ManagerSpec) []string {
	if manager == nil {
		return nil
	}
	args, _ := json.Marshal(manager.Args)
	return []string{"-c", "mcp_servers.swarm_voice={command=" + strconv.Quote(manager.Command) + ",args=" + string(args) + ",required=true}", "-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`}
}

func managerOverride(value string) bool {
	for _, key := range []string{"mcp_servers.swarm_voice", "approval_policy", "sandbox_mode"} {
		if strings.HasPrefix(value, key+"=") {
			return true
		}
	}
	return false
}
