package phonecore_test

import (
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func TestSealRosterRefreshEnvelopeCarriesExplicitDiscardRecoveryIntent(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	auth := schema.DeviceCommandAuth{Action: schema.ActionJournalResync, Machine: "m1"}
	for _, tc := range []struct {
		name      string
		discarded bool
		token     string
	}{
		{name: "healthy refresh", discarded: false},
		{name: "completed stale-mailbox discard", discarded: true, token: "0123456789abcdef0123456789abcdef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := phonecore.SealRosterRefreshEnvelope(key, 7, 3, auth, 42, tc.discarded, tc.token)
			if err != nil {
				t.Fatalf("SealRosterRefreshEnvelope: %v", err)
			}
			rc, err := remotegw.OpenRemoteCommand(key, env)
			if err != nil {
				t.Fatalf("OpenRemoteCommand: %v", err)
			}
			if rc.Action != schema.ActionJournalResync || !rc.RosterOnly || rc.ResyncCursor != 42 || rc.DiscardedBacklog != tc.discarded || rc.DiscardRecoveryToken != tc.token {
				t.Fatalf("opened command = %#v, want journal_resync roster_only from cursor 42 with discarded_backlog=%t token=%q", rc, tc.discarded, tc.token)
			}
		})
	}
}
