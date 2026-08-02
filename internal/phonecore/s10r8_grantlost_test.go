package phonecore

// FAILING-FIRST (TDD RED, GG-5) for the round-8 review's B2: PB-KEY-3's terminal state had
// no PRODUCTION entry. Core.MarkGrantLost was called by its own test and by nothing else, so
// a handset that reached the state presented exactly the pre-S10 symptom -- every send
// failing with errNoContentKey and nothing saying why -- which the method's own doc calls the
// indefinite decrypt-failure loop PB-KEY-3 forbids.
//
// THE ARGUMENT THAT SAID IT COULD NOT BE DETECTED, and what is wrong with it. The claim was
// that PB-KEY-3 describes "drained, no grant, retention cap passed" and the phone can measure
// none of those: it has no pairing timestamp, the retention cap is RELAY configuration
// asserted by the party the design treats as hostile, and every inbound frame that could
// speak is sealed under the key it lacks.
//
// Two of those three are still true and neither matters, because the phone does not have to
// infer the terminal state -- it is HANDED PROOF of it, unsealed, on the one inbound path it
// already has:
//
//	the bootstrap frame is TAGGED PLAINTEXT (that is its whole purpose: it is what DELIVERS
//	the content key), it is signed by the machine key pinned at pairing, and the gateway
//	re-appends it from its persistent sidecar ONCE PER GATEWAY SESSION.
//
// So a bootstrap frame refused as crypto.ErrGrantReplay while the phone holds NO content key
// is not a guess. It says, with the machine's own signature on it: the gateway is connected
// and delivering; the coordinates it can deliver are ones this phone has already consumed;
// and consuming them again is refused by the strict (epoch, seq) monotonicity that exists to
// keep a retaining relay from rewinding the phone. Ordinary re-delivery can therefore never
// help, however long anyone waits. Only a machine-side re-grant, which advances the seq, can
// -- and that is PB-KEY-3's terminal state in its own terms.
//
// THE TRIGGER USED TO BE THE SCREEN LOCK, AND THAT WAS THE BUG. This file originally reached
// the state through PB-KEY-7's purge: the purge destroyed the content key and deliberately kept
// GrantEpoch/GrantSeq (the watermark is the replay defence), so the phone came back from a
// screen lock keyless with its watermark at the exact coordinates of the sidecar the gateway
// was about to re-send -- and every re-delivery after that was a replay, forever. ADR-007 B35
// established that this is a BRICK rather than a feature: PB-KEY-10 delivers the epoch key
// inside Go, so nothing on the handset could put it back, and the only exit was physical access
// to the machine.
//
// THE SCREEN LOCK IS GONE (ADR-007 B133) and the purge's trigger is now revoke/unpair, where
// destroying the sealed key is the point rather than the cost -- re-pairing is the way back.
// What the argument above still rules out is a KEYLESS-LOOKING process being marked terminal by
// ordinary re-delivery, and that state is still reachable without any purge at all: the push
// path holds the wake key and never opens the content tier.
// TestS10_R8_AnUnopenedContentTierIsNotTheTerminalState below is the fence on that.
//
// WHAT REMAINS REACHABLE is PB-KEY-3's own scenario: a phone that has moved to an epoch it
// holds no key for, with a relay that can still deliver only the retired one. mobile.App.pin
// zeroes State.Keys when a pairing lands in a different epoch, and resealTier carries a sealed
// key only into the epoch it was sealed for -- so the phone is genuinely keyless, not merely
// locked, and the sidecar the gateway re-appends is refused by the watermark.

import (
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestS10_R8_APurgedKeyAndAReplayedSidecarIsTheTerminalState drives PB-KEY-3's terminal state
// through PRODUCTION PATHS ONLY. Nothing here calls MarkGrantLost; the whole point is that
// something finally does.
func TestS10_R8_APurgedKeyAndAReplayedSidecarIsTheTerminalState(t *testing.T) {
	m := newS10Machine(t)
	dir, wake, content, frame, keys := s10r8Provisioned(t, m)
	c := s10r8Resume(t, dir, wake, content, &recordingAcker{})

	// The pairing completes: the gateway connects and the bootstrap frame lands.
	if _, err := c.Router().AcceptCommit(frame, 1000); err != nil {
		t.Fatalf("the machine's bootstrap grant: %v", err)
	}
	if c.State().Keys.ContentKey != keys.ContentKey {
		t.Fatalf("precondition: the epoch content key was not installed")
	}
	if c.StreamStale(StreamGrant) {
		t.Fatalf("precondition: a working handset reports the grant channel already lost")
	}

	// THE PHONE MOVES TO AN EPOCH IT HOLDS NO KEY FOR. This is mobile.App.pin's own act -- it
	// zeroes State.Keys when a pairing lands in a different epoch, because the tier keys belong
	// to the old one -- and resealTier then writes no content-key field at all, since a key is
	// carried only into the epoch it was sealed for. The phone is now genuinely keyless: not
	// holding one, and holding none at rest either. The grant watermark deliberately survives,
	// because it IS the replay defence.
	rotate := c.State()
	rotate.EpochID, rotate.Keys = 8, crypto.EpochKeys{}
	if err := c.Save(rotate); err != nil {
		t.Fatalf("the epoch rotation: %v", err)
	}
	if got := c.State(); got.Keys.ContentKey != (crypto.ContentKey{}) || got.GrantEpoch != 7 || got.GrantSeq != 1 {
		t.Fatalf("precondition: after the rotation the phone holds content key present=%v and watermark "+
			"(%d,%d); want no key and the watermark intact at (7,1)",
			got.Keys.ContentKey != crypto.ContentKey{}, got.GrantEpoch, got.GrantSeq)
	}
	if err := c.UnsealContent(); err != nil || c.State().Keys.ContentKey != (crypto.ContentKey{}) {
		t.Fatalf("precondition: a fresh unwrap recovered a content key, so the phone is LOCKED rather "+
			"than keyless and everything below would be measuring the wrong state (err=%v)", err)
	}

	// The gateway reconnects and re-appends the SAME sidecar -- the only thing it has.
	rcpt, err := c.Router().AcceptCommit(frame, 1001)
	if !errors.Is(err, crypto.ErrGrantReplay) {
		t.Fatalf("re-delivery of the same sidecar after a purge = %v; want crypto.ErrGrantReplay "+
			"(the watermark survives the purge on purpose)", err)
	}
	if !rcpt.Acked {
		t.Errorf("the replayed bootstrap frame was not acked. It can never become usable, so an " +
			"unacked one pins the drain on its page while the gateway appends another copy every " +
			"session")
	}
	if !c.StreamStale(StreamGrant) {
		t.Errorf("the phone holds NO content key, its watermark is at the sidecar's own coordinates, "+
			"and the machine's signed frame has just told it so -- and %q is still reported live. "+
			"PB-KEY-3 requires a DEFINED terminal state; without one every send fails with "+
			"errNoContentKey and nothing distinguishes it from a custody refusal with a completely "+
			"different remedy", StreamGrant)
	}

	// IT IS DURABLE. An Android process death is routine, and a terminal state that lives only
	// in memory is re-derived only if the gateway happens to reconnect again.
	restarted := s10r8Resume(t, dir, wake, content, &recordingAcker{})
	if !restarted.StreamStale(StreamGrant) {
		t.Errorf("the terminal state did not survive a restart; it must be persisted with the rest " +
			"of the phone's resume-critical model (PB-STATE-1)")
	}

	// AND IT IS RECOVERABLE, never latched (PB-STATE-10). The owner runs the re-grant, which
	// advances the seq, and the ordinary inbound path clears the state with the key it installs.
	regrant, regrantKeys := m.bootstrapFor(t, restarted.KeyStore(), 7, 2)
	if _, err := restarted.Router().AcceptCommit(regrant, 1002); err != nil {
		t.Fatalf("the machine's re-grant: %v", err)
	}
	if restarted.State().Keys.ContentKey != regrantKeys.ContentKey {
		t.Errorf("the re-granted content key was not installed")
	}
	if restarted.StreamStale(StreamGrant) {
		t.Errorf("the grant channel is still reported lost after a re-grant landed")
	}
}

// TestS10_R8_AnUnopenedContentTierIsNotTheTerminalState is the fence on ADR-007 B35's
// decision, at the exact seam where the brick was.
//
// The gateway re-appends its bootstrap sidecar ONCE PER GATEWAY SESSION, so a connected phone
// is handed a replay within seconds of every gateway start. If a process that is holding no
// content key looks KEYLESS, that replay is grantLossDetected's proof and the phone is marked
// PB-KEY-3 TERMINAL -- exitable only at the machine -- by ordinary traffic. That is a permanent
// exposure traded for a live-process one, and it is worse.
//
// THE STATE IS REACHED DIFFERENTLY SINCE ADR-007 B133, and it is still reached. The trigger
// used to be a screen lock, which no longer exists as an event. What produces it now is the
// push/wake path: PB-KEY-2's tier boundary is enforced by CODE DISCIPLINE on Android, so a
// process serving a push holds the wake key and never opens the content tier at all. The
// sealed content key at rest and the fresh unwrap that restores it are unchanged, which is
// what makes the replayed sidecar prove nothing.
//
// Both halves are asserted: the state is not entered, and the phone genuinely recovers.
func TestS10_R8_AnUnopenedContentTierIsNotTheTerminalState(t *testing.T) {
	m := newS10Machine(t)
	dir, wake, content, frame, keys := s10r8Provisioned(t, m)
	c := s10r8Resume(t, dir, wake, content, &recordingAcker{})

	if _, err := c.Router().AcceptCommit(frame, 1200); err != nil {
		t.Fatalf("the machine's bootstrap grant: %v", err)
	}

	// A process that holds no content key while the SEALED copy stays at rest. It is reached
	// by a restart that does not open the tier -- the push path's own condition -- rather than
	// by PurgeKeys, which is the REVOKE purge and destroys the sealed copy on purpose
	// (PB-KEY-7, ADR-007 B133). The refusal is the stimulus available to a fake sealer; what
	// it stands for in production is a process that never asked.
	content.openErr = crypto.ErrKeyAuthRequired
	c = s10r8Resume(t, dir, wake, content, &recordingAcker{})
	content.openErr = nil
	if c.State().Keys.ContentKey != (crypto.ContentKey{}) {
		t.Fatalf("precondition: the process came up holding a content key")
	}

	// The gateway reconnects and re-appends the same sidecar, which it does every session.
	if _, err := c.Router().AcceptCommit(frame, 1201); !errors.Is(err, crypto.ErrGrantReplay) {
		t.Fatalf("re-delivery of the same sidecar to a keyless process = %v; want crypto.ErrGrantReplay", err)
	}
	if c.StreamStale(StreamGrant) {
		t.Errorf("PB-KEY-3/PB-KEY-7: a process that never opened the content tier was put into the " +
			"terminal state. The phone holds its sealed content key at rest and one fresh unwrap " +
			"restores it, so a replayed sidecar proves nothing -- and the state it was put in has no " +
			"exit but physical access to the machine")
	}

	// And the recovery is local, immediate, and needs neither the machine nor the network.
	if err := c.UnsealContent(); err != nil {
		t.Fatalf("PB-KEY-7: the fresh unwrap failed: %v", err)
	}
	if got := c.State().Keys.ContentKey; got != keys.ContentKey {
		t.Errorf("PB-KEY-7: the content key was not restored by a fresh unwrap.\n got %x\nwant %x",
			got, keys.ContentKey)
	}
}

// TestS10_R8_AReplayRefusedWhileTheKeyStillWorksIsNotGrantLoss is the false-positive guard,
// and it is the reason the rule is "replay AND no content key" rather than "replay".
//
// A retaining relay re-serving a pre-rotation grant is the NORMAL, expected traffic the
// watermark exists to refuse (TestS10_AReplayedBootstrapGrantIsRefused). The phone is working
// perfectly at that moment. Telling the user their grant is lost there would send them to the
// machine to fix a device that has nothing wrong with it -- and, worse, would train them to
// ignore the one message that means something.
func TestS10_R8_AReplayRefusedWhileTheKeyStillWorksIsNotGrantLoss(t *testing.T) {
	m := newS10Machine(t)
	dir, wake, content, first, _ := s10r8Provisioned(t, m)
	c := s10r8Resume(t, dir, wake, content, &recordingAcker{})

	if _, err := c.Router().AcceptCommit(first, 1100); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	rotated, rotatedKeys := m.bootstrapFor(t, c.KeyStore(), 8, 2)
	if _, err := c.Router().AcceptCommit(rotated, 1101); err != nil {
		t.Fatalf("rotation bootstrap: %v", err)
	}
	if c.State().Keys.ContentKey != rotatedKeys.ContentKey {
		t.Fatalf("precondition: the rotation grant was not adopted")
	}

	// The relay re-serves the retired grant. Refused, as it must be -- and the phone is fine.
	if _, err := c.Router().AcceptCommit(first, 1102); !errors.Is(err, crypto.ErrGrantReplay) {
		t.Fatalf("the replayed pre-rotation grant = %v; want crypto.ErrGrantReplay", err)
	}
	if c.StreamStale(StreamGrant) {
		t.Errorf("a working handset was told its grant is LOST because the relay replayed a retired "+
			"one. %q is the state PB-KEY-3 defines for a phone that cannot decrypt anything; a phone "+
			"holding a live content key is not in it, and the remedy it points at -- go to the "+
			"machine and re-grant -- is work the user does not need to do", StreamGrant)
	}
}
