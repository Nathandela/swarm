package swarmmobile_test

// FAILING-FIRST (TDD RED, GG-5). THREE DURABLE COORDINATES THE FACADE NEEDS AND
// phonecore.State DOES NOT CARRY. Said loudly, because the brief for this slice permits
// adding a phonecore seam only when one is genuinely missing -- and the test author's job
// is to prove it is missing, not to add it.
//
// PB-STATE-1 enumerates "everything resume-critical ... in ONE persisted schema", and S7
// delivered exactly that for the coordinates it knew about. The facade is the FIRST
// consumer that has to reach the machine after a restart, and at that point the schema is
// short by three fields:
//
//  1. MachineRelayAuthPub -- the machine's relay-auth public key. The phone derives the
//     machine's relay ROUTING id from it (relay.RoutingID), which is the mailbox target
//     every command and keystroke is appended to, and it is also what the phone must
//     AuthorizeDevice for anything to be delivered. State records the machine's ENDPOINT
//     id, its Noise static and its grant-signing key -- who the machine IS -- and nothing
//     about how to REACH it. A restored phone therefore has a valid content key, a valid
//     send-seq and no destination. That is requirements 4.3 one field lower: nothing
//     fails loudly, the app simply never delivers anything again.
//
//  2. PushToken -- PB-STATE-9 assigns the push token to the WAKE tier of persisted state
//     and PB-PUSH-9 requires it to survive process death and app upgrade, with
//     re-registration on every authenticated reconnect. A token held only in memory is
//     re-registered only if the app happens to be foregrounded.
//
//  3. PushPreference -- PB-APP-7's toggles must persist. PB-PUSH-10 makes the MACHINE
//     authoritative for suppression, but the phone still has to render the setting the
//     user chose across a restart, or the UI shows a default that contradicts the
//     machine's behaviour.
//
// This test does not add them. It fails until they exist.

import (
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
)

func TestPBSTATE1_DurableStateCarriesWhatTheFacadeNeedsToReachTheMachine(t *testing.T) {
	st := reflect.TypeOf(phonecore.State{})

	// Non-vacuity: the fields S7 did ship must still be there, so a renamed schema does
	// not make this test silently pass by measuring nothing.
	for _, present := range []string{"Machine", "MachineSignPub", "RoutingID", "EpochID", "SendSeq", "Receive"} {
		if _, ok := st.FieldByName(present); !ok {
			t.Fatalf("phonecore.State lost the field %q; this guard is measuring the wrong schema", present)
		}
	}

	missing := []struct{ field, why string }{
		{
			"MachineRelayAuthPub",
			"the machine's relay-auth pub, from which the phone derives the machine's relay ROUTING " +
				"id (the mailbox target for every command and keystroke) and which it must authorize " +
				"to receive anything. Without it a restored phone knows who the machine is and not " +
				"how to reach it, and nothing fails loudly",
		},
		{
			"PushToken",
			"PB-STATE-9 puts the push token in the wake tier of persisted state and PB-PUSH-9 " +
				"requires it to survive process death and app upgrade",
		},
		{
			"PushPreference",
			"PB-APP-7's two coarse toggles must persist, or after a restart the settings screen " +
				"renders a default that contradicts what the machine is actually doing",
		},
	}
	for _, m := range missing {
		if _, ok := st.FieldByName(m.field); !ok {
			t.Errorf("PB-STATE-1: phonecore.State has no %s -- %s", m.field, m.why)
		}
	}
}
