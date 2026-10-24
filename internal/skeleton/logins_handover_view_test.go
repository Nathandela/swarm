package skeleton

import (
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
)

func TestProviderLoginViewProjectsPartialHandover(t *testing.T) {
	r := logins.Registry{
		Providers: map[string]logins.ProviderState{logins.ProviderCodex: {Provider: logins.ProviderCodex, ActiveID: "to"}},
		Handovers: map[string]logins.Handover{logins.ProviderCodex: {Provider: logins.ProviderCodex, Members: []logins.HandoverMember{
			{Phase: "adopted"}, {Phase: "acknowledged"}, {Phase: "pending"}, {Phase: "retired"},
		}}},
	}
	got := providerLoginView(r, logins.ProviderCodex)
	if got.State != "adopting" || got.Message != "1/3 sessions switched" {
		t.Fatalf("partial handover projection = %+v", got)
	}
}

func TestProviderLoginViewProjectsNoRunningHandoverMembers(t *testing.T) {
	for _, members := range [][]logins.HandoverMember{
		{{Phase: "retired"}, {Phase: "retired"}},
		{},
	} {
		r := logins.Registry{Handovers: map[string]logins.Handover{logins.ProviderCodex: {Provider: logins.ProviderCodex, Members: members}}}
		got := providerLoginView(r, logins.ProviderCodex)
		if got.State != "adopting" || got.Message != "No running sessions to switch." {
			t.Fatalf("empty handover projection = %+v", got)
		}
	}
}

func TestProviderLoginViewMigrationPendingOverridesHandover(t *testing.T) {
	r := logins.Registry{
		MigrationPending: true,
		Handovers:        map[string]logins.Handover{logins.ProviderCodex: {Provider: logins.ProviderCodex, Members: []logins.HandoverMember{{Phase: "pending"}}}},
	}
	got := providerLoginView(r, logins.ProviderCodex)
	if got.State != "waiting" || got.Message != "Offline login migration is unfinished." {
		t.Fatalf("migration override = %+v", got)
	}
}
