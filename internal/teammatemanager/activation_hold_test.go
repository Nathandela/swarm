package teammatemanager

import (
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/voice"
)

func TestActivationNoticePropagatesActivationHoldAndPendingStatus(t *testing.T) {
	err := activationNotice(&voice.DiscussionInputHold{
		AdmissionID: "da_1",
		Activation:  true,
		Reason:      voice.ErrDiscussionDirectInputHeld,
	})
	var hold *discord.TeammateInputHold
	if !errors.As(err, &hold) {
		t.Fatalf("expected *discord.TeammateInputHold, got %T: %v", err, err)
	}
	if !hold.DirectInput || !hold.Activation || hold.AdmissionID != "da_1" {
		t.Fatalf("unexpected hold projection: %+v", hold)
	}

	status := CheckoutStatusProjection(voice.DiscussionNativeStatus{
		State:                  "parked",
		ManagerSession:         "ep/old",
		ManagerInstance:        "old-inst",
		PendingManagerSession:  "ep/new",
		PendingManagerInstance: "new-inst",
	})
	if status.PendingManagerSession != "ep/new" || status.PendingManagerInstance != "new-inst" {
		t.Fatalf("pending manager coordinates not projected: %+v", status)
	}
}
