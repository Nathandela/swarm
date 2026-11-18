package protocol

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/wire"
)

const testJournalSourceA = "00000000-0000-4000-8000-000000000001"
const testJournalSourceB = "00000000-0000-4000-8000-000000000002"

type sourceJournalStub struct {
	*journalStub
	sourceID string
}

func (s *sourceJournalStub) JournalSourceID() string { return s.sourceID }
func (s *sourceJournalStub) JournalReadFrom(from uint64) (JournalResume, error) {
	res, err := s.journalStub.JournalReadFrom(from)
	res.SourceID = s.sourceID
	return res, err
}
func (s *sourceJournalStub) JournalSubscribeFrom(from uint64) (JournalResume, <-chan JournalRecord, func(), error) {
	res, err := s.JournalReadFrom(from)
	return res, s.atomicSource, func() {}, err
}

func TestJournalSourceIdentitySnapshotAndLiveWithLegacyOmission(t *testing.T) {
	js := &sourceJournalStub{journalStub: newJournalStub(), sourceID: testJournalSourceA}
	js.resume = JournalResume{Cursor: 7, Roster: []JournalRecord{{SessionID: "s", Type: "roster", SessionInstance: "instance", Role: "manager"}}, Events: []JournalRecord{{Cursor: 7, SessionID: "s", Type: "exited"}}}
	sock := tmpSock(t)
	srv, err := Serve(js, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	c := dialClient(t, sock, []string{CapJournal, CapJournalSubscribeFrom, CapJournalSourceIdentity})
	defer func() { _ = c.Close() }()
	if !slices.Contains(c.Capabilities(), CapJournalSourceIdentity) {
		t.Fatal("source-aware capability was not negotiated")
	}
	res, live, err := c.JournalSubscribeFrom(context.Background(), 0)
	if err != nil || res.SourceID != testJournalSourceA || !res.HistoryGap || res.Roster[0].SessionInstance != "instance" {
		t.Fatalf("incomplete source snapshot: %+v %v", res, err)
	}
	js.atomicSource <- JournalRecord{Cursor: 8, SessionID: "s", SessionInstance: "instance", Role: "manager", Type: "exited"}
	select {
	case rec := <-live:
		if rec.Cursor != 8 || rec.SessionInstance != "instance" {
			t.Fatalf("live identity lost: %+v", rec)
		}
	case <-time.After(recvTimeout):
		t.Fatal("live event missing")
	}
	legacy := rawDial(t, sock)
	hello := legacy.hello(Version, []string{CapJournal})
	legacy.writeControl(Control{Op: OpJournalRead, EndpointID: hello.EndpointID})
	reply := legacy.readControl()
	if reply.JournalSourceID != "" || reply.JournalHistoryGap || reply.Roster[0].SessionInstance != "" || reply.Roster[0].Role != "" {
		t.Fatalf("new fields leaked to legacy client: %+v", reply)
	}
}

func TestJournalSourceIdentityClientRejectsChangedOrMissingPageSource(t *testing.T) {
	for _, second := range []string{testJournalSourceB, "", "malformed"} {
		t.Run(second, func(t *testing.T) {
			c, peer := journalPipeClient(t)
			c.caps = append(c.caps, CapJournalSourceIdentity)
			result := make(chan error, 1)
			go func() { _, _, err := c.JournalSubscribeFrom(context.Background(), 0); result <- err }()
			if _, _, err := wire.ReadFrame(peer); err != nil {
				t.Fatal(err)
			}
			for _, page := range []Control{{Op: OpJournalSubscribeFrom, Cursor: 9, JournalMore: true, JournalSourceID: testJournalSourceA}, {Op: OpJournalSubscribeFrom, Cursor: 9, JournalSourceID: second}} {
				body, _ := EncodeControl(page)
				if err := wire.WriteFrame(peer, wire.TControl, body); err != nil {
					break
				}
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("mixed source pages accepted as one snapshot")
				}
			case <-time.After(recvTimeout):
				t.Fatal("source mismatch did not terminate snapshot")
			}
		})
	}
}

func TestJournalSourceIdentityLiveCannotSwitchWithOverlappingCursor(t *testing.T) {
	c, peer := journalPipeClient(t)
	c.caps = append(c.caps, CapJournalSourceIdentity)
	type opened struct {
		res  JournalResume
		live <-chan JournalRecord
		err  error
	}
	result := make(chan opened, 1)
	go func() {
		res, live, err := c.JournalSubscribeFrom(context.Background(), 0)
		result <- opened{res, live, err}
	}()
	if _, _, err := wire.ReadFrame(peer); err != nil {
		t.Fatal(err)
	}
	page, _ := EncodeControl(Control{Op: OpJournalSubscribeFrom, Cursor: 7, JournalSourceID: testJournalSourceA})
	if err := wire.WriteFrame(peer, wire.TControl, page); err != nil {
		t.Fatal(err)
	}
	got := <-result
	if got.err != nil || got.res.SourceID != testJournalSourceA {
		t.Fatalf("snapshot: %+v", got)
	}
	event, _ := EncodeControl(Control{Op: OpJournalEvent, Cursor: 7, JournalSourceID: testJournalSourceB, Journal: []JournalRecord{{Cursor: 7, SessionID: "s", SessionInstance: "instance", Type: "exited"}}})
	if err := wire.WriteFrame(peer, wire.TControl, event); err != nil {
		t.Fatal(err)
	}
	select {
	case rec, ok := <-got.live:
		if ok {
			t.Fatalf("replacement source masqueraded at overlapping cursor: %+v", rec)
		}
	case <-time.After(recvTimeout):
		t.Fatal("replacement source did not close live feed")
	}
}

func TestJournalSourceIdentityLiveUnknownInstanceRequiresSnapshotRecovery(t *testing.T) {
	js := &sourceJournalStub{journalStub: newJournalStub(), sourceID: testJournalSourceA}
	sock := tmpSock(t)
	srv, err := Serve(js, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	c := dialClient(t, sock, []string{CapJournal, CapJournalSubscribeFrom, CapJournalSourceIdentity})
	defer func() { _ = c.Close() }()
	_, live, err := c.JournalSubscribeFrom(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	js.atomicSource <- JournalRecord{Cursor: 1, SessionID: "legacy", Type: "exited"}
	select {
	case rec, ok := <-live:
		if ok {
			t.Fatalf("unknown incarnation became a live worker event: %+v", rec)
		}
	case <-time.After(recvTimeout):
		t.Fatal("identity gap did not force snapshot recovery")
	}
}

func TestJournalSourceIdentityCapRequiresPersistentBackend(t *testing.T) {
	sock, _ := serveJournal(t, newJournalStub())
	c := dialClient(t, sock, []string{CapJournal, CapJournalSubscribeFrom, CapJournalSourceIdentity})
	defer func() { _ = c.Close() }()
	if slices.Contains(c.Capabilities(), CapJournalSourceIdentity) {
		t.Fatal("legacy source advertised unverifiable identity")
	}
}
