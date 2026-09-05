package pushgw_test

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestV2DeploymentHasNoLegacyStartup(t *testing.T) {
	for _, path := range []string{"docker-compose.yml", "swarm-pushgw.service", "Caddyfile", "pushgw.env.example"} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("obsolete VM startup %s must not remain an executable deployment path: %v", path, err)
		}
	}
	for _, path := range []string{
		"../../docs/operations/push-gateway-deploy.md",
		"../../docs/operations/push-gateway-runbook.md",
		"../../docs/operations/push-gateway-incident-response.md",
	} {
		content := read(t, path)
		for _, old := range []string{"-db ", "docker compose", "systemctl", "push-swarm.dsfactory.org"} {
			if strings.Contains(content, old) {
				t.Errorf("active v2 operations document %s still advertises %q", path, old)
			}
		}
	}
}
