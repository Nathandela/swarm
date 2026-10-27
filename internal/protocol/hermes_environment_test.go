package protocol

import (
	"strings"
	"testing"
)

func TestHermesLaunchCapturesOnlyOwnerNativeOriginsBeforeAllowlist(t *testing.T) {
	req := &LaunchReq{Agent: "hermes", Env: []string{"HERMES_HOME=/native/home", "HERMES_TUI_GATEWAY_URL=ws://127.0.0.1/?secret=private-token", "UNRELATED_SECRET=hidden"}}
	local := daemonLaunchSpec(req, false, "")
	if len(local.HermesOrigins) != 2 || local.HermesOrigins[0] != "HERMES_HOME=/native/home" {
		t.Fatalf("Hermes origins lost: %v", local.HermesOrigins)
	}
	if strings.Contains(strings.Join(local.HermesOrigins, " "), "private-token") || strings.Contains(strings.Join(local.HermesOrigins, " "), "hidden") {
		t.Fatal("native origin capture retained secret contents")
	}
	remote := daemonLaunchSpec(req, true, "")
	if remote.HermesOrigins != nil {
		t.Fatal("remote launch injected native home")
	}
}
