package protocol

import (
	"strings"
	"testing"
	"time"
)

func TestProtocol_JournalSubscribeFromReturnsSnapshotBeforeLive(t *testing.T) {
	js := newJournalStub()
	js.resume = JournalResume{
		Cursor: 3,
		Roster: []JournalRecord{{SessionID: "s", Type: "roster"}},
		Events: []JournalRecord{{Cursor: 3, SessionID: "s", Type: "session_state"}},
	}
	sock, _ := serveJournal(t, js)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapJournal, CapJournalSubscribeFrom})
	rc.writeControl(Control{
		Op: OpJournalSubscribeFrom, EndpointID: rep.EndpointID, Cursor: 2,
		JournalMaxBytes: 64 << 10,
	})
	snapshot := rc.readControl()
	if snapshot.Op != OpJournalSubscribeFrom || snapshot.Cursor != 3 || len(snapshot.Roster) != 1 || len(snapshot.Journal) != 1 {
		t.Fatalf("subscribe-from snapshot = %#v", snapshot)
	}
	js.atomicSource <- JournalRecord{Cursor: 4, SessionID: "s", Type: "exited"}
	_ = rc.conn.SetReadDeadline(time.Now().Add(time.Second))
	live := rc.readControl()
	if live.Op != OpJournalEvent || live.Cursor != 4 || len(live.Journal) != 1 {
		t.Fatalf("subscribe-from live = %#v", live)
	}
}

func TestProtocol_JournalSubscribeFromSourceEOFDisconnectsForReplay(t *testing.T) {
	js := newJournalStub()
	sock, _ := serveJournal(t, js)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapJournal, CapJournalSubscribeFrom})
	rc.writeControl(Control{Op: OpJournalSubscribeFrom, EndpointID: rep.EndpointID})
	if got := rc.readControl(); got.Op != OpJournalSubscribeFrom {
		t.Fatalf("snapshot = %#v", got)
	}
	close(js.atomicSource)
	_ = rc.conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := rc.readFrame(); err == nil {
		t.Fatal("atomic source EOF left subscriber connected with a silent gap")
	}
}

func TestProtocol_JournalSubscribeFromOversizeDoesNotConsumeSubscription(t *testing.T) {
	js := newJournalStub()
	js.resume = JournalResume{
		Cursor: 1,
		Events: []JournalRecord{{Cursor: 1, SessionID: strings.Repeat("x", 2048), Type: "session_state"}},
	}
	sock, _ := serveJournal(t, js)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapJournal, CapJournalSubscribeFrom})
	rc.writeControl(Control{Op: OpJournalSubscribeFrom, EndpointID: rep.EndpointID, JournalMaxBytes: 256})
	if got := rc.readControl(); got.Op != OpError {
		t.Fatalf("oversized snapshot = %#v, want refusal", got)
	}
	// The failed page validation happened before jSubOnce/writer ownership. A
	// corrected request on the same connection must still be able to subscribe.
	rc.writeControl(Control{Op: OpJournalSubscribeFrom, EndpointID: rep.EndpointID, JournalMaxBytes: 8 << 10})
	if got := rc.readControl(); got.Op != OpJournalSubscribeFrom || got.Cursor != 1 {
		t.Fatalf("retry after oversized snapshot = %#v, want successful subscription", got)
	}
}

func TestProtocol_JournalSubscribeFromRequiresItsNegotiatedCapability(t *testing.T) {
	js := newJournalStub()
	sock, _ := serveJournal(t, js)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapJournal})
	rc.writeControl(Control{Op: OpJournalSubscribeFrom, EndpointID: rep.EndpointID})
	if got := rc.readControl(); got.Op != OpError {
		t.Fatalf("unnegotiated subscribe-from = %#v, want refusal", got)
	}
}
