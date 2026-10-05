package protocol

import (
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestLaunchConfigurationEnvironmentPreservesOwnerPresence(t *testing.T) {
	for _, env := range [][]string{nil, {}, {"HOME=/owner", "CODEX_HOME=/custom", "CLAUDE_CONFIG_DIR=/claude", "XDG_CONFIG_HOME=0", "CLAUDE_CODE_MANAGED_SETTINGS_PATH=false", "OPENAI_API_KEY=synthetic-secret", "UNRELATED=discard"}} {
		spec := daemonLaunchSpec(&LaunchReq{Env: env}, false, "")
		if (spec.ClientEnv == nil) != (env == nil) || (spec.AccountOriginalConfigurationEnv == nil) != (env == nil) {
			t.Fatal("owner nil and explicit empty environment became indistinguishable")
		}
		if len(env) != 0 {
			want := []string{"CODEX_HOME=/custom", "CLAUDE_CONFIG_DIR=/claude", "XDG_CONFIG_HOME=0", "CLAUDE_CODE_MANAGED_SETTINGS_PATH=false"}
			if !reflect.DeepEqual(spec.AccountOriginalConfigurationEnv, want) {
				t.Fatal("owner configuration capture lost origins/policy or captured credentials")
			}
			if !reflect.DeepEqual(spec.ClientEnv, []string{"HOME=/owner", "OPENAI_API_KEY=synthetic-secret"}) {
				t.Fatal("owner child allowlist changed")
			}
		}
	}
}

func TestLaunchConfigurationEnvironmentRemoteAndPresetIgnoreInjectedOrigins(t *testing.T) {
	poison := []string{"CODEX_HOME=/phone", "CLAUDE_CONFIG_DIR=/phone", "CLAUDE_CODE_MANAGED_SETTINGS_PATH=0", "CLAUDE_CODE_NO_MODEL_FALLBACK=true", "HOME=/phone", "PATH=/phone"}
	t.Run("remote-launch", func(t *testing.T) {
		stub := newStubDaemon()
		rc := rawDial(t, serveRemoteAPI(t, allowAllLaunchPolicy{stub}))
		hello := rc.hello(Version, []string{CapRemoteGateway})
		req := policyLaunchReq(t)
		req.Env = poison
		rc.writeControl(remoteLaunchControl(hello.EndpointID, req))
		if got := rc.readControl(); got.Op == OpError {
			t.Fatal(got.Error)
		}
		specs := stub.launchSpecs()
		if len(specs) != 1 || specs[0].ClientEnv != nil || specs[0].AccountOriginalConfigurationEnv != nil {
			t.Fatal("unauthenticated remote origins reached managed launch admission")
		}
	})
	t.Run("signed-preset", func(t *testing.T) {
		backend := newR5Backend(r5PresetView(t, "configuration-preset"))
		rc := rawDial(t, serveRemoteAPI(t, backend))
		hello := rc.hello(Version, []string{CapRemoteGateway})
		frame := r5Frame(hello, "devA:01JCONFIGURATION", &schema.SessionLaunchReq{PresetID: "configuration-preset", PresetRevision: "rev-1", Cols: 80, Rows: 24})
		frame.Launch = &LaunchReq{Env: poison}
		rc.writeControl(frame)
		if got := rc.readControl(); got.Op != OpSessionLaunch || got.Session == nil {
			t.Fatal("valid signed preset refused", got.Error)
		}
		specs := backend.launchSpecs()
		if len(specs) != 1 || specs[0].ClientEnv != nil || specs[0].AccountOriginalConfigurationEnv != nil {
			t.Fatal("unbound launch fields changed signed preset environment")
		}
	})
}
