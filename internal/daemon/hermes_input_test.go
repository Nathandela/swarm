package daemon

import (
	"github.com/Nathandela/swarm/internal/hermesinput"
	"os"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestHermesNativeLaunchIsQualifiedAndActivationGated(t *testing.T) {
	for _, tc := range []struct {
		enabled bool
		version string
		want    bool
	}{{false, "0.22.5", false}, {true, "0.20.6", false}, {true, "0.22.5", true}} {
		d := &Daemon{cfg: Config{NativeInputEnabled: func() bool { return tc.enabled }}}
		spec := LaunchSpec{AgentType: "hermes", Argv: []string{"/opt/hermes", "chat", "--cli", "-q", "initial"}, Cwd: "/project", InitialPrompt: "initial", CLIIdentity: &persist.CLIIdentity{Version: tc.version}, Options: map[string]string{"profile": "custom"}}
		got, err := d.hermesLaunchConfig(spec)
		if err != nil {
			t.Fatal(err)
		}
		if (got != nil) != tc.want {
			t.Fatalf("enabled=%t version=%s native=%+v", tc.enabled, tc.version, got)
		}
		if got != nil && (got.Program != "/opt/hermes" || got.Profile != "custom" || got.InitialPrompt != "initial") {
			t.Fatalf("native launch lost provider policy: %+v", got)
		}
	}
}

func TestHermesResumeRetainsNativeHomeWhenActivationChanges(t *testing.T) {
	d := &Daemon{cfg: Config{StateDir: t.TempDir()}, sessions: map[string]*session{}}
	m := persist.Meta{ID: "old-worker", HermesNative: true, AgentType: "hermes", ConversationID: "local-" + strings.Repeat("a", 64), ShimPID: 123, ShimStartTime: 456, CLIIdentity: &persist.CLIIdentity{Version: "0.22.5"}, LaunchOptions: map[string]string{"profile": "custom"}}
	d.sessions[m.ID] = &session{meta: m}
	desc := hermesinput.Descriptor{WorkerID: m.ID, Generation: strings.Repeat("b", 32), ProviderVersion: "0.22.5", ShimPID: 123, ShimStartTime: 456, Binding: hermesinput.Binding{Home: "/native/profiles/custom", ControlHome: "/native", OwnerPID: 42, OwnerStartTime: 99, InstanceID: "owner", SessionID: m.ConversationID, AuthorityEpoch: 1, CodeSHA: strings.Repeat("c", 40)}}
	if err := os.MkdirAll(d.sessionDir(m.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := hermesinput.WriteDescriptor(d.sessionDir(m.ID), desc); err != nil {
		t.Fatal(err)
	}
	spec := LaunchSpec{AgentType: "hermes", Argv: []string{"/opt/hermes", "chat", "--cli"}, ResumedFrom: m.ID, ConversationID: m.ConversationID, Cwd: "/project", CLIIdentity: &persist.CLIIdentity{Version: "0.22.5"}}
	got, err := d.hermesLaunchConfig(spec)
	if err != nil || got == nil || got.Profile != "custom" || len(got.Origins) == 0 || got.Origins[len(got.Origins)-1] != "HERMES_HOME="+desc.Binding.Home {
		t.Fatalf("native resume lost home: %+v %v", got, err)
	}
	spec.Options = map[string]string{"profile": "another"}
	if _, err = d.hermesLaunchConfig(spec); err == nil {
		t.Fatal("resume silently retargeted another native home")
	}
}
