package phonecore

// The ack ORDERING of the bootstrap grant, which s10_grant_test.go's compaction fence does
// not reach: it proves an accepted grant IS acked, and this proves the two ways a refused
// one must be treated differently.
//
// The distinction is the one AcceptCommit already states for sealed frames, and it bites
// hardest here. The bootstrap frame is appended ONCE per gateway session and it is the only
// thing that carries the epoch key, so:
//
//	UNOPENABLE (replay, forgery, sealed to another device) -> ACK. It can never become
//	    usable, and an item that is never acked pins the drain on its page for the whole
//	    7-day retention window -- the denial lever PB-SYNC-6 forbids.
//	OPENED but NOT COMMITTED (a full disk, a read-only data dir) -> NEVER ACK. The relay
//	    holds the only copy; compacting it leaves a phone that can neither send nor open
//	    anything, with nothing left to redeliver it.

import (
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestS10_AnUnopenableBootstrapGrantIsAcked pins the first half. The grant is signed by a
// machine this phone never paired with, so it can never open -- on this drain or any later
// one.
func TestS10_AnUnopenableBootstrapGrantIsAcked(t *testing.T) {
	paired := newS10Machine(t)
	c, _ := s10JustPaired(t, paired)
	stranger := newS10Machine(t)
	frame, _ := stranger.bootstrapFor(t, c.KeyStore(), 7, 1)

	rcpt, err := c.Router().AcceptCommit(frame, 700)
	if err == nil {
		t.Fatalf("a bootstrap grant signed by a machine this phone never pinned was ACCEPTED; " +
			"the pinned grant-signing key is the only thing standing between the relay and an " +
			"injected epoch key")
	}
	if !rcpt.Acked {
		t.Errorf("an unopenable bootstrap frame was not acked. It can never become usable, so the "+
			"relay mailbox never compacts it and the phone re-reads the same page for the whole "+
			"7-day retention window (err was %v)", err)
	}
	if got := c.State().Keys.ContentKey; got != (crypto.ContentKey{}) {
		t.Errorf("a refused grant installed a content key")
	}
}

// TestS10_AGrantWhoseCommitFailedIsNotAcked pins the second half, and it is the one that
// loses a working pairing if it regresses.
func TestS10_AGrantWhoseCommitFailedIsNotAcked(t *testing.T) {
	m := newS10Machine(t)
	mem := &memStore{}
	if err := mem.Save(State{Machine: "m1", MachineSignPub: m.pub, EpochID: 7}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Every Save from here on dies: the disk filled up between the pairing and the grant.
	failing := &failAfterNStore{inner: mem, n: 0}
	ack := &recordingAcker{}
	c, err := Resume(Config{State: failing, Ack: ack})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	frame, _ := m.bootstrapFor(t, c.KeyStore(), 7, 1)

	rcpt, err := c.Router().AcceptCommit(frame, 800)
	if !errors.Is(err, errStoreDied) {
		t.Fatalf("AcceptCommit with a failing commit = %v; want the persist error surfaced", err)
	}
	if rcpt.Acked {
		t.Errorf("a bootstrap grant whose durable commit FAILED was acked")
	}
	if len(ack.acked) != 0 {
		t.Errorf("the relay was acked at cursor(s) %v for a grant that never reached disk. The "+
			"bootstrap frame is appended ONCE per gateway session and it is the only thing that "+
			"carries the epoch key: compacting it here leaves a phone that can neither send nor "+
			"open anything, with nothing left to deliver a key again", ack.acked)
	}
}
