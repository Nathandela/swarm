package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-d0b8's second half: WHICH transport states end
// a pairing, and the one reading of a terminal state that must not.
//
// WHY THE GATE READS THE TRANSPORT AT ALL. `App.PurgeKeys` has exactly one production caller --
// the Settings "Replace this computer" press -- so the durable unpair flag covers the path the
// PHONE takes and no other. The path an OWNER takes is `swarm remote revoke <device-id>` on the
// Mac, which is the documented one and the only mitigation ADR-007 B133 leaves for a lost
// handset; nothing on the phone runs for it. What the phone gets is a refused handshake, and
// `connRevoked` is where that lands.
//
// WHY THE READING IS NOT WRITTEN DOWN. `relay.ErrRevoked` comes from the RELAY, which this design
// trusts for nothing else, and PB-STATE-10 records that a terminal revoked verdict can be made
// STALE by a pairing. A verdict on disk could not be: it would outlive the recovery that
// disproves it, which is "the brick reached through the remedy" the requirement is named for. The
// durable flag stays what it is -- the record of an act the OWNER performed on this device -- and
// the transport reading is what it is: a live opinion, re-formed on every dial.
//
// It is a function of (state, grace, now) and nothing else, so it is decided here rather than
// raced through the harness.

import (
	"testing"
	"time"
)

func TestD0B8_OnlyTheTerminalTransportStatesEndAPairing(t *testing.T) {
	now := time.Now()
	noPairing := time.Time{}

	for _, terminal := range []string{connRevoked, connRepairRequired} {
		if !transportEndsPairing(terminal, noPairing, now) {
			t.Errorf("agents-tracker-d0b8: the transport state %q leaves the phone reading as paired. "+
				"Both states are documented TERMINAL -- nothing on this device recovers either -- and "+
				"both carry Remedy.RE_PAIR, so this is exactly the population that has to reach the "+
				"pairing screen. Their shipped banners already tell the user to pair again", terminal)
		}
	}

	// EVERY OTHER STATE IS A LINK CONDITION, and a gate that ended a pairing on one would take the
	// app away from a phone whose relay is merely down. relay_untrusted and relay_insecure are the
	// two that look terminal and are not: ADR-007 B58 has relay_untrusted arriving on the ORDINARY
	// FIRST PAIRING, because a handset holding no pin yet is refused on every dial for the whole
	// time the user is comparing SAS symbols -- and relay_insecure is the MACHINE's configuration,
	// which pairing again does not change.
	for _, live := range []string{
		connOffline, connConnecting, connOnline, connReconnecting,
		connRelayUntrusted, connRelayInsecure, "",
	} {
		if transportEndsPairing(live, noPairing, now) {
			t.Errorf("agents-tracker-d0b8: the transport state %q unpairs the phone. Only a state "+
				"nothing on the device can recover from may, or a dropped socket takes the app away "+
				"from a handset that is perfectly paired", live)
		}
	}
}

// TestPBSTATE10_AStaleRevokedVerdictDoesNotUnpairAFreshlyPairedPhone is the window that makes the
// reading above safe to act on.
//
// The two ends of a recovery cannot be ordered: the phone learns the pairing succeeded the instant
// the machine's acceptance frame lands, and the machine opens this device's relay route just
// after, over a connection of its own. So the phone's first dial after a re-pair can legitimately
// arrive while the ban is still in place, and a gate that believed it would send the handset back
// to the screen it has just come from -- the recovery working only if the phone lost a race.
func TestPBSTATE10_AStaleRevokedVerdictDoesNotUnpairAFreshlyPairedPhone(t *testing.T) {
	now := time.Now()

	if transportEndsPairing(connRevoked, now.Add(time.Minute), now) {
		t.Error("PB-STATE-10: a revoked verdict inside the post-pairing grace unpairs the phone. The " +
			"owner has just paired from that very screen, and rearmAfterPairing opened this window " +
			"precisely because the machine's authorization races the phone's first dial")
	}
	if !transportEndsPairing(connRevoked, now.Add(-time.Second), now) {
		t.Error("agents-tracker-d0b8: an EXPIRED grace still suppresses the verdict. The window is " +
			"bounded because a relay that is genuinely still refusing must eventually be believed; " +
			"one that never closes is a phone that can never be revoked again")
	}
	// THE GRACE IS THE REVOKE'S, NOT A GENERAL AMNESTY. It exists for a machine-side authorization
	// that races a dial; nothing about that makes a destroyed Keystore entry come back, and a
	// pairing cannot complete over a key that will not sign.
	if !transportEndsPairing(connRepairRequired, now.Add(time.Minute), now) {
		t.Error("PB-KEY-6: the post-pairing grace suppressed a DESTROYED RELAY-AUTH KEY, which it has " +
			"no bearing on. The phone would be shown an app it cannot connect to, with the one screen " +
			"its own banner names out of reach")
	}
}
