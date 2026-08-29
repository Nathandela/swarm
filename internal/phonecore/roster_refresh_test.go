package phonecore_test

import (
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func TestSealRosterRefreshEnvelopeCarriesRosterOnlyIntentAndPriorCursor(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	auth := schema.DeviceCommandAuth{Action: schema.ActionJournalResync, Machine: "m1"}
	env, err := phonecore.SealRosterRefreshEnvelope(key, 7, 3, auth, 42)
	if err != nil {
		t.Fatalf("SealRosterRefreshEnvelope: %v", err)
	}
	rc, err := remotegw.OpenRemoteCommand(key, env)
	if err != nil {
		t.Fatalf("OpenRemoteCommand: %v", err)
	}
	if rc.Action != schema.ActionJournalResync || !rc.RosterOnly || rc.ResyncCursor != 42 {
		t.Fatalf("opened command = %#v, want journal_resync roster_only from cursor 42", rc)
	}
}
