package daemon

// Lifecycle plan R1: nil-ClientEnv launches (the remote/preset path) resolve
// against the environment the daemon SAVED at Open (daemon.env), not against the
// process's live environ. The two are identical at every start today -- this is
// the differential that keeps them pointing at ONE source of truth for the day a
// write policy learns to keep a good file over a bare start.

import (
	"slices"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestLaunchPolicyEnvPrefersTheSavedEnvironmentForNilClientEnv(t *testing.T) {
	saved := []string{"PATH=/saved/bin", "HOME=/home/saved"}
	d := &Daemon{savedEnv: saved}
	got := d.launchPolicyEnv(nil)
	want := persist.FilterEnv(saved)
	if !slices.Equal(got, want) {
		t.Errorf("launchPolicyEnv(nil) = %v, want the saved environment %v", got, want)
	}
}

func TestLaunchPolicyEnvKeepsASuppliedClientEnv(t *testing.T) {
	saved := []string{"PATH=/saved/bin"}
	client := []string{"PATH=/client/bin", "HOME=/home/client"}
	d := &Daemon{savedEnv: saved}
	got := d.launchPolicyEnv(client)
	want := persist.FilterEnv(client)
	if !slices.Equal(got, want) {
		t.Errorf("launchPolicyEnv(client) = %v, want the client environment %v -- ADR-006 "+
			"billing inheritance: a local launch bills as the shell that started it", got, want)
	}
}

func TestLaunchPolicyEnvFallsBackToTheLiveEnvironWithoutASavedOne(t *testing.T) {
	d := &Daemon{savedEnv: nil}
	got := d.launchPolicyEnv(nil)
	want := PolicyEnv(nil)
	if !slices.Equal(got, want) {
		t.Errorf("launchPolicyEnv(nil) with no saved env = %v, want PolicyEnv(nil) %v -- "+
			"an unreadable daemon.env must degrade to today's exact behavior", got, want)
	}
}
