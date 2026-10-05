//go:build linux

package skeleton

import (
	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"reflect"
	"testing"
)

func TestNewClaudeNativeContextRejectsLegacyVersionBeforeWrites(t *testing.T) {
	m := accountTestManager(t, accountTestState(t))
	candidate := accountTestCandidate(t, m, accounts.ProviderClaude, "native-version")
	account := accountTestAdmit(t, m, candidate)
	binding := accountTestBinding(t, m, account)
	_, cwd, env := accountTestLaunchEnvironment(t, m.stateRoot)
	before := configurationFenceSnapshot(t, candidate.ProfilePath)
	d := &Daemon{accounts: m}
	_, err := d.prepareAccountLaunch("new", daemon.LaunchSpec{AgentType: "claude", Argv: []string{"/fixture/claude", "--settings", `{"hooks":{}}`}, Cwd: cwd, ClientEnv: env, AccountBinding: &binding, CLIIdentity: &persist.CLIIdentity{Version: "2.1.288", Path: "/fixture/claude"}})
	if err != accountconfig.Conflict("native-version-not-characterized") || !reflect.DeepEqual(before, configurationFenceSnapshot(t, candidate.ProfilePath)) {
		t.Fatal("new native context used an uncharacterized legacy contract or wrote before refusing", err)
	}
}
