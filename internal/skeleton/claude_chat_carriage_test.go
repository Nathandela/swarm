package skeleton

import (
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func a2PrivateChatSpec() daemon.LaunchSpec {
	return daemon.LaunchSpec{AgentType: "fake", Cwd: "/fixture", ClientEnv: []string{"PATH=/usr/bin"}, Options: map[string]string{"claude_interface": "swarm-chat"}}
}

func TestClaudeChatCarriageDerivesOnlyConfiguredFixture(t *testing.T) {
	api := &coreAPI{fakeAgentBin: "/fixture/fake"}
	for _, operation := range []string{"", "dev-fixture-operation"} {
		spec := a2PrivateChatSpec()
		spec.OperationID = operation
		if err := api.prepareClaudeChatFixtureLaunch(&spec); err != nil {
			t.Fatal(err)
		}
		if spec.RequiredRuntime != "claude-chat" || spec.ClaudeChatFixture != "/fixture/fake" || spec.ConversationID != "" || !reflect.DeepEqual(spec.Argv, []string{"/fixture/fake", "--claude-primary-fixture"}) || spec.OperationID != operation {
			t.Fatalf("derived fixture/replay inputs changed: %+v", spec)
		}
	}
	ordinary := daemon.LaunchSpec{AgentType: "claude", Cwd: "/ordinary"}
	before := ordinary
	if err := api.prepareClaudeChatFixtureLaunch(&ordinary); err != nil || !reflect.DeepEqual(before, ordinary) {
		t.Fatal("ordinary launch changed", err)
	}
}

func TestClaudeChatCarriageRefusesUnownedSelectorsWithoutMutation(t *testing.T) {
	for _, kind := range []string{"empty", "unknown", "no-dev-fixture", "remote-preset", "initial-prompt", "import", "resume-empty", "handoff-empty", "lineage", "argv", "managed", "cli", "backend", "raw-runtime", "raw-fixture"} {
		t.Run(kind, func(t *testing.T) {
			api := &coreAPI{fakeAgentBin: "/fixture/fake"}
			spec := a2PrivateChatSpec()
			switch kind {
			case "empty":
				spec.Options["claude_interface"] = ""
			case "unknown":
				spec.Options["claude_interface"] = "other"
			case "no-dev-fixture":
				api.fakeAgentBin = ""
			case "remote-preset":
				spec.ClientEnv = nil
			case "initial-prompt":
				spec.InitialPrompt = "must not submit"
			case "import":
				spec.ConversationID = "10000000-0000-4000-8000-000000000001"
			case "resume-empty":
				spec.Options[protocol.OptionResumeFrom] = ""
			case "handoff-empty":
				spec.Options[protocol.OptionHandoffFrom] = ""
			case "lineage":
				spec.SpawnedFrom = "source"
			case "argv":
				spec.Argv = []string{"/untrusted"}
			case "managed":
				spec.LoginRuntime = &logins.Runtime{Provider: "claude"}
			case "cli":
				spec.CLIIdentity = &persist.CLIIdentity{Path: "/untrusted"}
			case "backend":
				spec.Backend = &daemon.BackendSpec{Program: "/untrusted"}
			case "raw-runtime":
				delete(spec.Options, "claude_interface")
				spec.RequiredRuntime = "claude-chat"
			case "raw-fixture":
				delete(spec.Options, "claude_interface")
				spec.ClaudeChatFixture = "/untrusted"
			}
			before := spec
			if err := api.prepareClaudeChatFixtureLaunch(&spec); err == nil {
				t.Fatal("unowned chat launch admitted")
			}
			if !reflect.DeepEqual(spec, before) {
				t.Fatal("refused launch changed caller state")
			}
		})
	}
}
