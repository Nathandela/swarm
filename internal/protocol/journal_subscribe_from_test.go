package protocol

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type legacyJournalBackend struct {
	*stubDaemon
	source chan JournalRecord
}

func newLegacyJournalBackend() *legacyJournalBackend {
	return &legacyJournalBackend{stubDaemon: newStubDaemon(), source: make(chan JournalRecord, 1)}
}

func (j *legacyJournalBackend) JournalReadFrom(uint64) (JournalResume, error) {
	return JournalResume{}, nil
}

func (j *legacyJournalBackend) JournalSubscribe() (<-chan JournalRecord, func()) {
	return j.source, func() {}
}

var _ JournalBackend = (*legacyJournalBackend)(nil)

type restartableLegacyJournalBackend struct {
	*stubDaemon
	mu      sync.Mutex
	sources []chan JournalRecord
	calls   chan int
}

func newRestartableLegacyJournalBackend(sources ...chan JournalRecord) *restartableLegacyJournalBackend {
	return &restartableLegacyJournalBackend{
		stubDaemon: newStubDaemon(),
		sources:    sources,
		calls:      make(chan int, len(sources)),
	}
}

func (j *restartableLegacyJournalBackend) JournalReadFrom(uint64) (JournalResume, error) {
	return JournalResume{}, nil
}

func (j *restartableLegacyJournalBackend) JournalSubscribe() (<-chan JournalRecord, func()) {
	j.mu.Lock()
	call := len(j.sources)
	var source chan JournalRecord
	if len(j.sources) > 0 {
		call = cap(j.calls) - len(j.sources) + 1
		source = j.sources[0]
		j.sources = j.sources[1:]
	} else {
		source = make(chan JournalRecord)
	}
	j.mu.Unlock()
	j.calls <- call
	return source, func() {}
}

var _ JournalBackend = (*restartableLegacyJournalBackend)(nil)

func TestProtocol_JournalSubscribeFromCapabilityRequiresBackendSupport(t *testing.T) {
	legacy := newLegacyJournalBackend()
	sock := tmpSock(t)
	srv, err := Serve(legacy, sock)
	if err != nil {
		t.Fatalf("Serve(legacy journal backend): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapJournal, CapJournalSubscribeFrom})
	for _, got := range rep.Capabilities {
		if got == CapJournalSubscribeFrom {
			t.Fatalf("legacy backend negotiated %q without JournalSubscribeFromBackend", got)
		}
	}
	if !containsCap(rep.Capabilities, CapJournal) {
		t.Fatalf("legacy backend lost ordinary journal capability: %v", rep.Capabilities)
	}
	rc.writeControl(Control{Op: OpJournalSubscribe, EndpointID: rep.EndpointID})
	if got := rc.readControl(); got.Op != OpOK {
		t.Fatalf("legacy journal subscribe = %#v", got)
	}
}

func TestProtocol_LegacyJournalFanoutResubscribesAfterSourceOverflow(t *testing.T) {
	first := make(chan JournalRecord, 1)
	second := make(chan JournalRecord, 1)
	legacy := newRestartableLegacyJournalBackend(first, second)
	sock := tmpSock(t)
	srv, err := Serve(legacy, sock)
	if err != nil {
		t.Fatalf("Serve(restartable legacy journal backend): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	if call := <-legacy.calls; call != 1 {
		t.Fatalf("initial JournalSubscribe call = %d, want 1", call)
	}
	firstClient := rawDial(t, sock)
	firstHello := firstClient.hello(Version, []string{CapJournal})
	firstClient.writeControl(Control{Op: OpJournalSubscribe, EndpointID: firstHello.EndpointID})
	if got := firstClient.readControl(); got.Op != OpOK {
		t.Fatalf("first legacy subscribe = %#v", got)
	}

	first <- JournalRecord{Cursor: 1, SessionID: "s", Type: "session_state"}
	if got := firstClient.readControl(); got.Op != OpJournalEvent || got.Cursor != 1 {
		t.Fatalf("first legacy event = %#v", got)
	}
	// A bounded journal source closes on overflow. Every legacy client on that
	// source must reconnect and replay, but the Server itself must establish a new
	// backend source so the replacement connection has a live feed.
	close(first)
	_ = firstClient.conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := firstClient.readFrame(); err == nil {
		t.Fatal("legacy source EOF left the old subscriber connected")
	}
	select {
	case call := <-legacy.calls:
		if call != 2 {
			t.Fatalf("replacement JournalSubscribe call = %d, want 2", call)
		}
	case <-time.After(time.Second):
		t.Fatal("legacy source EOF permanently stopped the server fanout")
	}

	secondClient := rawDial(t, sock)
	secondHello := secondClient.hello(Version, []string{CapJournal})
	secondClient.writeControl(Control{Op: OpJournalSubscribe, EndpointID: secondHello.EndpointID})
	if got := secondClient.readControl(); got.Op != OpOK {
		t.Fatalf("replacement legacy subscribe = %#v", got)
	}
	second <- JournalRecord{Cursor: 2, SessionID: "s", Type: "session_state"}
	_ = secondClient.conn.SetReadDeadline(time.Now().Add(time.Second))
	if got := secondClient.readControl(); got.Op != OpJournalEvent || got.Cursor != 2 {
		t.Fatalf("replacement legacy event = %#v", got)
	}
}

func containsCap(caps []string, want string) bool {
	for _, cap := range caps {
		if cap == want {
			return true
		}
	}
	return false
}

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
