package remotegw

// Unit coverage for the gateway's remote-egress id namespacing (agents-tracker-p1b):
// the raw local journal id the daemon emits is rewritten to the endpoint-scoped id the
// phone commands against, with session-neutral and already-namespaced records left
// untouched.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestNamespaceRecord(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		in       string
		want     string
	}{
		{"raw local id is namespaced", "ep1", "s7", "ep1/s7"},
		{"session-neutral record untouched", "ep1", "", ""},
		{"already-namespaced untouched", "ep1", "ep1/s7", "ep1/s7"},
		{"empty endpoint untouched", "", "s7", "s7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := namespaceRecord(tc.endpoint, protocol.JournalRecord{SessionID: tc.in, Cursor: 3})
			if got.SessionID != tc.want {
				t.Errorf("SessionID = %q, want %q", got.SessionID, tc.want)
			}
			if got.Cursor != 3 {
				t.Errorf("Cursor mutated to %d, want 3", got.Cursor)
			}
		})
	}
}

func TestNamespaceRoster_DoesNotMutateInput(t *testing.T) {
	in := []protocol.JournalRecord{{SessionID: "s1"}, {SessionID: "s2"}}
	out := namespaceRoster("ep", in)
	if in[0].SessionID != "s1" || in[1].SessionID != "s2" {
		t.Fatalf("input roster was mutated: %+v", in)
	}
	if out[0].SessionID != "ep/s1" || out[1].SessionID != "ep/s2" {
		t.Fatalf("roster not namespaced: %+v", out)
	}
}
