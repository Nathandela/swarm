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
	got := d.LaunchPolicyEnv(nil)
	want := persist.FilterEnv(saved)
	if !slices.Equal(got, want) {
		t.Errorf("LaunchPolicyEnv(nil) = %v, want the saved environment %v", got, want)
	}
}

func TestLaunchPolicyEnvKeepsASuppliedClientEnv(t *testing.T) {
	saved := []string{"PATH=/saved/bin"}
	client := []string{"PATH=/client/bin", "HOME=/home/client"}
	d := &Daemon{savedEnv: saved}
	got := d.LaunchPolicyEnv(client)
	want := persist.FilterEnv(client)
	if !slices.Equal(got, want) {
		t.Errorf("LaunchPolicyEnv(client) = %v, want the client environment %v -- ADR-006 "+
			"billing inheritance: a local launch bills as the shell that started it", got, want)
	}
}

func TestLaunchPolicyEnvTreatsAnEmptySavedEnvironmentAsAbsent(t *testing.T) {
	// The empty survivor of a failed write must never become a launch's whole
	// environment (R1 audit M2): no PATH, no HOME is the round-4 BLOCKER-1
	// failure. Empty degrades to the live environ exactly like nil.
	d := &Daemon{savedEnv: []string{}}
	got := d.LaunchPolicyEnv(nil)
	want := PolicyEnv(nil)
	if !slices.Equal(got, want) {
		t.Errorf("LaunchPolicyEnv(nil) with an EMPTY saved env = %v, want PolicyEnv(nil) %v", got, want)
	}
}

func TestLaunchPolicyEnvFallsBackToTheLiveEnvironWithoutASavedOne(t *testing.T) {
	d := &Daemon{savedEnv: nil}
	got := d.LaunchPolicyEnv(nil)
	want := PolicyEnv(nil)
	if !slices.Equal(got, want) {
		t.Errorf("LaunchPolicyEnv(nil) with no saved env = %v, want PolicyEnv(nil) %v -- "+
			"an unreadable daemon.env must degrade to today's exact behavior", got, want)
	}
}
