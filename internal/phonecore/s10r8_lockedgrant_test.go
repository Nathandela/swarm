package phonecore

// FAILING-FIRST (TDD RED, GG-5) for the round-8 review's B1: a LOCKED CONTENT TIER makes the
// phone ack -- and the relay DELETE -- the only copy of its epoch key.
//
// THE CROSS-SLICE SEAM. S10 wrote acceptBootstrap's ack rule when the only way installGrant
// could return (opened == false) was a grant that can never become usable: a replay, a
// forgery, a seal for another device. S14a then made the open FAILABLE for a reason that is
// neither: crypto.ErrKeyAuthRequired from the content-tier KEK, which is not a verdict on the
// grant at all -- it is the DESIGNED locked-handset steady state. openSealedDeviceKeys
// deliberately tolerates a locked content tier so the wake tier keeps the relay dialled and
// the drain running with nobody present, and mobile/relay.go keeps retrying on it.
//
// So the phone wakes on a push with the screen locked, drains the mailbox, refuses the
// bootstrap grant it cannot open -- and ACKS it. relay.ackItems DELETES acked items and
// deliver.go appends the bootstrap frame ONCE per gateway session, so the only copy of the
// epoch key is gone. The user unlocks to errNoContentKey on every send, forever. Recovery is
// an owner-side regrant plus a gateway bounce, and if the phone is locked when THAT lands it
// repeats.
//
// THE RULE THESE TESTS PIN: a custody sentinel is TRANSIENT with respect to the grant. The
// frame is perfectly good and will open the moment the tier does, so it must stay in the
// mailbox exactly as an opened-but-uncommitted grant does. Neither of the two acking cases
// s10_bootstrapack_test.go pins is weakened: an UNOPENABLE grant is still acked, and one
// whose commit failed is still not.

import (
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// s10r8Provisioned is a handset that has completed a real pairing under real tier KEKs: the
// device key container is sealed on disk, the machine's grant-signing key is pinned, and the
// epoch id is known. It returns the state dir, both sealers, and the bootstrap frame the
// gateway would append for epoch 7 together with the keys it carries.
func s10r8Provisioned(t *testing.T, m s10Machine) (dir string, wake, content *s14aSealer, frame []byte, keys crypto.EpochKeys) {
	t.Helper()
	dir = t.TempDir()
	wake, content = s14aNewSealer(t), s14aNewSealer(t)
	s14aSeedDeviceKeys(t, dir, wake, content)

	c, err := Resume(Config{Dir: dir, Machine: "m1", Ack: &recordingAcker{}, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume (provisioning launch): %v", err)
	}
	st := c.State()
	st.MachineSignPub, st.EpochID = m.pub, 7
	if err := c.Save(st); err != nil {
		t.Fatalf("persist the paired state: %v", err)
	}
	if c.State().Keys.ContentKey != (crypto.ContentKey{}) {
		t.Fatalf("precondition: the provisioned fixture already holds a content key; it must be " +
			"ZERO, or this test proves nothing about how the key ARRIVES")
	}
	frame, keys = m.bootstrapFor(t, c.KeyStore(), 7, 1)
	return dir, wake, content, frame, keys
}

// s10r8Resume opens a Core over an already-provisioned directory, as a fresh process would.
func s10r8Resume(t *testing.T, dir string, wake, content Sealer, ack Acker) *Core {
	t.Helper()
	c, err := Resume(Config{Dir: dir, Machine: "m1", Ack: ack, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume(dir=%s): %v", dir, err)
	}
	return c
}

// TestS10_R8_ALockedContentTierDoesNotAckTheBootstrapGrant is the blocking half. The phone
// comes up on a push with the screen locked -- the state the wake tier exists to support --
// and the grant frame must SURVIVE it.
func TestS10_R8_ALockedContentTierDoesNotAckTheBootstrapGrant(t *testing.T) {
	m := newS10Machine(t)
	dir, wake, content, frame, keys := s10r8Provisioned(t, m)

	// The push wake: nobody is present, so the content-tier KEK refuses. This is not a
	// failure state -- it is the steady state openSealedDeviceKeys is written to tolerate.
	content.openErr = crypto.ErrKeyAuthRequired
	ack := &recordingAcker{}
	locked := s10r8Resume(t, dir, wake, content, ack)

	rcpt, err := locked.Router().AcceptCommit(frame, 900)
	if !errors.Is(err, crypto.ErrKeyAuthRequired) {
		t.Fatalf("AcceptCommit on a bootstrap grant with the content tier locked = %v; want the "+
			"custody refusal surfaced (the sealed-box open is what needs the tier)", err)
	}
	if rcpt.Acked {
		t.Errorf("a bootstrap grant refused because the CONTENT TIER IS LOCKED was acked. " +
			"relay.ackItems DELETES acked items and cmd/swarm-remote/deliver.go appends the " +
			"bootstrap frame ONCE per gateway session, so this discards the only copy of the epoch " +
			"key -- for a refusal that says nothing about the grant and clears the moment the user " +
			"unlocks. The handset comes back to errNoContentKey on every send with no way to " +
			"recover that does not involve the owner at the machine")
	}
	if len(ack.acked) != 0 {
		t.Errorf("the relay was acked at cursor(s) %v for a grant a LOCKED tier could not open", ack.acked)
	}
	if got := locked.State().Keys.ContentKey; got != (crypto.ContentKey{}) {
		t.Errorf("a refused grant installed a content key")
	}

	// THE RECOVERY THE NON-ACK BUYS: the user authenticates, the drain re-reads the frame the
	// relay still holds, and the pairing works.
	content.openErr = nil
	unlocked := s10r8Resume(t, dir, wake, content, &recordingAcker{})
	rcpt, err = unlocked.Router().AcceptCommit(frame, 900)
	if err != nil {
		t.Fatalf("the SAME bootstrap frame after the user authenticated: %v", err)
	}
	if !rcpt.Acked {
		t.Errorf("the bootstrap grant was not acked once it opened; the mailbox never compacts it")
	}
	if got := unlocked.State().Keys.ContentKey; got != keys.ContentKey {
		t.Errorf("after unlocking, the retained bootstrap frame did not deliver the epoch content key")
	}
}

// TestS10_R8_AnInvalidatedContentTierDoesNotAckTheBootstrapGrant is the same rule for the
// PERMANENT custody sentinel. It is equally not a verdict on the grant: the frame is intact
// and the remedy is a re-pair, so discarding the machine's key on the way out adds a second,
// independent breakage to a device that already has one -- and nothing can compact behind it
// anyway, since every sealed frame in the mailbox fails to open for the same reason.
func TestS10_R8_AnInvalidatedContentTierDoesNotAckTheBootstrapGrant(t *testing.T) {
	m := newS10Machine(t)
	dir, wake, content, frame, _ := s10r8Provisioned(t, m)

	content.openErr = crypto.ErrKeyInvalidated
	ack := &recordingAcker{}
	invalidated := s10r8Resume(t, dir, wake, content, ack)

	rcpt, err := invalidated.Router().AcceptCommit(frame, 901)
	if !errors.Is(err, crypto.ErrKeyInvalidated) {
		t.Fatalf("AcceptCommit with an invalidated content tier = %v; want the custody refusal surfaced", err)
	}
	if rcpt.Acked || len(ack.acked) != 0 {
		t.Errorf("a bootstrap grant refused by a CUSTODY verdict (acked=%v, cursors=%v) was acked. "+
			"A custody refusal is a verdict on this device's tier, never on the grant: the frame "+
			"would open under any tier that works, so deleting it destroys key material the phone "+
			"may still need after the remedy", rcpt.Acked, ack.acked)
	}
}
