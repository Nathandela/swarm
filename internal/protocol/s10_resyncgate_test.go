package protocol

// PB-SYNC-4: "Authorization is specified correctly." The v1 requirement claimed the resync
// rides requireRemoteAuthz. IT DOES NOT, and this file is where that correction becomes a
// standing fence rather than a sentence in a table.
//
// The journal repair channel bottoms out in handleJournalRead, which gates on exactly two
// things -- the negotiated `journal` capability, and the remote kill switch -- and on
// nothing else. requireRemoteAuthz guards the MUTATING ops: it demands a device signature
// over the canonical command tuple, verified against a key only the phone holds.
//
// THE CHOSEN GATE IS THE EXISTING ONE, and the fence is two-sided:
//
//   - It must stay REFUSABLE. A resync that no gate can refuse leaks session lifecycle
//     metadata to any connection that reaches the socket.
//   - It must NOT acquire requireRemoteAuthz. The gateway is what performs the journal_read,
//     and the gateway HOLDS NO DEVICE KEY -- it opens the phone's sealed commands and
//     forwards them; it cannot author a signature. Adding the mutating-op gate here would
//     refuse every repair from a device whose view is stale, permanently, which is
//     PB-STATE-10's brick reached through the requirement meant to prevent staleness.
//
// LEGITIMATELY PASSING, and labelled as such rather than counted as earned: the behaviour
// these tests pin already holds. PB-SYNC-4's acceptance is "the chosen gate is implemented
// and tested"; the implementation exists, the TEST is what was missing, and the second one
// below has no equivalent anywhere in the suite -- nothing today would notice a change that
// put the resync behind the device-signature gate.

import (
	"errors"
	"testing"
)

// s10JournalOnlyStub is a remote-tier backend that serves the journal AND refuses every
// device signature it is shown. That combination is what makes it the right probe: a
// journal_read that succeeds here is a journal_read that does not ride the device-signature
// gate, and one that fails here has acquired it.
type s10JournalOnlyStub struct {
	*stubDaemon
	source chan JournalRecord
}

var errS10Forged = errors.New("protocol test: this backend refuses every device signature")

func newS10JournalOnlyStub() *s10JournalOnlyStub {
	s := &s10JournalOnlyStub{stubDaemon: newStubDaemon(), source: make(chan JournalRecord, 4)}
	s.authzFn = func(DeviceCommandAuth) error { return errS10Forged }
	return s
}

func (s *s10JournalOnlyStub) JournalReadFrom(uint64) (JournalResume, error) {
	return JournalResume{Cursor: 9, Roster: []JournalRecord{{SessionID: "s1", Type: "roster"}}}, nil
}
func (s *s10JournalOnlyStub) JournalSubscribe() (<-chan JournalRecord, func()) {
	return s.source, func() {}
}

var _ JournalBackend = (*s10JournalOnlyStub)(nil)

// TestS10_ResyncIsRefusedWithoutTheJournalCapability is the refusable half. The gate is the
// negotiated capability, so a connection that never negotiated `journal` gets nothing.
func TestS10_ResyncIsRefusedWithoutTheJournalCapability(t *testing.T) {
	stub := newS10JournalOnlyStub()
	sock := tmpSock(t)
	srv, err := ServeRemote(stub, sock)
	if err != nil {
		t.Fatalf("ServeRemote: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	rc := rawDial(t, sock)
	rep := rc.hello(Version, nil) // no CapJournal

	rc.writeControl(Control{Op: OpJournalRead, EndpointID: rep.EndpointID, Cursor: 0})
	if got := rc.readControl(); got.Op == OpJournalRead {
		t.Errorf("journal_read was SERVED to a connection that never negotiated the %q "+
			"capability. The journal is session lifecycle metadata; the negotiated capability "+
			"is the gate PB-SYNC-4 names, and a repair channel no gate can refuse is a leak",
			CapJournal)
	}
}

// TestS10_ResyncDoesNotRideTheDeviceSignatureGate is the correction, as a fence. The
// backend's authorizer refuses every device signature it is shown, so every op behind
// requireRemoteAuthz is refused -- and journal_read must still be served.
//
// If this ever fails, the resync has been put behind a signature the party performing it
// cannot produce: the gateway opens the phone's sealed command and then does the
// journal_read itself, holding no device key. Every stale phone would then be stale forever.
func TestS10_ResyncDoesNotRideTheDeviceSignatureGate(t *testing.T) {
	stub := newS10JournalOnlyStub()
	sock := tmpSock(t)
	srv, err := ServeRemote(stub, sock)
	if err != nil {
		t.Fatalf("ServeRemote: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapJournal})

	// NON-VACUITY: a MUTATING op over this same connection must be refused -- the backend's
	// authorizer rejects every signature -- or "journal_read succeeded" says nothing about
	// which gate it did or did not pass.
	rc.writeControl(Control{Op: OpKill, EndpointID: rep.EndpointID, SessionID: rep.EndpointID + "/s1",
		OperationID: "op-s10-authz"})
	if got := rc.readControl(); got.Op != OpError {
		t.Fatalf("a mutating op over a backend that refuses every device signature returned op %q; "+
			"want a fail-closed refusal. Without it this test cannot distinguish the two gates "+
			"at all", got.Op)
	}

	rc.writeControl(Control{Op: OpJournalRead, EndpointID: rep.EndpointID, Cursor: 0})
	got := rc.readControl()
	if got.Op != OpJournalRead {
		t.Errorf("journal_read = op %q code %q over a remote server with the %q capability "+
			"negotiated and the kill switch on, refused by the DEVICE-SIGNATURE gate. It has "+
			"acquired the mutating-op authorization "+
			"gate, which the party that performs a resync cannot satisfy: the GATEWAY runs the "+
			"journal_read, and it holds no device signing key -- it opens the phone's sealed "+
			"commands and forwards them. Every phone with a stale journal would stay stale for "+
			"good", got.Op, got.ErrorCode, CapJournal)
	}
	if len(got.Roster) == 0 {
		t.Errorf("journal_read returned no roster; the repair channel PB-SYNC-2 nominates carries " +
			"the roster, so an empty one is a repair that repairs nothing")
	}
}
