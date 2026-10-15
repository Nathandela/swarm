package skeleton

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func composerRefusal(t *testing.T, d *Daemon, local string) string {
	t.Helper()
	_, err := d.composerSend("", "op-"+local, protocol.ComposerSendReq{Session: protocol.NamespacedID("ep-test01", local), Text: "hello"})
	if err == nil {
		t.Fatalf("a held session %s accepted a composer send", local)
	}
	return err.Error()
}

func TestIdentityDrift_LaterAuthInvalidKeepsTheHoldReleasable(t *testing.T) {
	r := newIdentityDriftRig(t)
	r.drift(t)
	if err := r.m.reportFailure(r.m.w, r.local, "auth-invalid", "", "later-auth-invalid"); err != nil {
		t.Fatal(err)
	}
	r.writeIdentity(t, "synthetic-account-0")
	r.m.checkIdentities()
	if rec, ok := r.m.w.state.AccountRotations[r.local]; (ok && (rec.State == accountObserved || rec.State == accountBlocked)) || r.cleared == 0 {
		t.Fatalf("a later auth-invalid report made the identity hold unreleasable: %+v cleared=%d", rec, r.cleared)
	}
}

func TestIdentityDrift_RefusalNamesARemedyThatUnlocks(t *testing.T) {
	r := newIdentityDriftRig(t)
	d := &Daemon{}
	r.m.d = d
	r.m.w.restoreRecycle = func(local string) {
		r.held++
		d.restoreAuthRecycle(local)
	}
	r.drift(t)
	if msg := composerRefusal(t, d, r.local); !strings.Contains(msg, "end the session and resume it") || strings.Contains(msg, "log that account in again") {
		t.Fatalf("identity refusal names a remedy that does not release the lock: %s", msg)
	}
}

func TestIdentityDrift_OwnerEndAfterReleaseStillEndsTheSession(t *testing.T) {
	r := newIdentityDriftRig(t)
	r.drift(t)
	var ended []string
	r.m.endTarget = func(target, action string) error {
		ended = append(ended, target+":"+action)
		return nil
	}
	r.writeIdentity(t, "synthetic-account-0")
	r.m.checkIdentities()
	// The owner's kill passed handles() before the release dropped the record.
	if err := r.m.ownerEnd(r.m.w, r.local, "kill"); err != nil || len(ended) != 1 || ended[0] != r.local+":kill" {
		t.Fatalf("owner kill racing a release was refused: err=%v ended=%v", err, ended)
	}
}

func TestInputGate_RefusalNamesAnAccountMove(t *testing.T) {
	x := feasibleAccountTest(t, 2)
	d := &Daemon{}
	x.m.d = d
	x.m.w.withRecycleFence = d.withAuthRecycleFence
	x.m.w.withClaimedRecycleFence = d.withClaimedAuthRecycleFence
	if err := x.m.reportFailure(x.m.w, x.source.ID, "quota", "", "native-failure"); err != nil {
		t.Fatal(err)
	}
	x.m.step()
	x.m.step()
	if state := x.m.w.state.AccountRotations[x.source.ID].State; state != accountClaimed && state != accountStopped && state != accountPrepared && state != accountLaunched {
		t.Fatalf("setup: rotation did not claim the source: %s", state)
	}
	if msg := composerRefusal(t, d, x.source.ID); !strings.Contains(msg, "moving to another account") {
		t.Fatalf("refusal during an account move does not name it: %s", msg)
	}
}

func TestInputGate_RefusalNamesAnUnsavedObservationHold(t *testing.T) {
	r := newIdentityDriftRig(t)
	d := &Daemon{}
	r.m.d = d
	r.m.w.restoreRecycle = d.restoreAuthRecycle
	r.m.nativeInboxFailed(r.local, "")
	if msg := composerRefusal(t, d, r.local); !strings.Contains(msg, "until the session ends") {
		t.Fatalf("observation-hold refusal does not say the lock lasts until the session ends: %s", msg)
	}
}
