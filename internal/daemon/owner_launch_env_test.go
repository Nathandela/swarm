package daemon

import (
	"reflect"
	"strings"
	"testing"
)

func TestOwnerLaunchEnvironmentPreservesSafeOriginsAndRefusals(t *testing.T) {
	for _, test := range []struct {
		agent     string
		env, want []string
	}{
		{"codex", []string{"PATH=/bin", "AWS_SECRET_ACCESS_KEY=dummy"}, []string{"PATH=/bin"}},
		{"hermes", []string{"HOME=/home", "HERMES_HOME=/profile", "HERMES_TUI_GATEWAY_URL=https://dummy-secret"}, []string{"HOME=/home", "HERMES_HOME=/profile", "SWARM_HERMES_UNSUPPORTED_ORIGIN=1"}},
		{"opencode", []string{"HOME=/home", "XDG_CONFIG_HOME=/config", "OPENCODE_CONFIG_DIR=/profile", "OPENCODE_DISABLE_AUTOUPDATE=true", "OPENCODE_CONFIG_CONTENT=dummy-secret"}, []string{"HOME=/home", "XDG_CONFIG_HOME=/config", "OPENCODE_CONFIG_DIR=/profile", "OPENCODE_DISABLE_AUTOUPDATE=true", "SWARM_OPENCODE_UNSUPPORTED_ORIGIN=1"}},
	} {
		t.Run(test.agent, func(t *testing.T) {
			got := OwnerLaunchEnvironment(test.agent, test.env)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("safe launch environment = %v, want %v", got, test.want)
			}
			if strings.Contains(strings.Join(got, "\n"), "dummy") {
				t.Fatal("secret survived origin refusal")
			}
		})
	}
	if OwnerLaunchEnvironment("codex", nil) != nil {
		t.Fatal("nil request lost daemon-policy semantics")
	}
}
