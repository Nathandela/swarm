package remotegw

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/wire"
)

type atomicResumeSink struct {
	mu       sync.Mutex
	cursor   uint64
	snapshot int
	events   []protocol.JournalRecord
	reseeds  []protocol.JournalReseed
}

func (s *atomicResumeSink) DeliveredCursor() uint64 { return s.cursor }
func (s *atomicResumeSink) Snapshot([]protocol.JournalRecord, uint64) error {
	s.mu.Lock()
	s.snapshot++
	s.mu.Unlock()
	return nil
}
func (s *atomicResumeSink) Event(rec protocol.JournalRecord) error {
	s.mu.Lock()
	s.events = append(s.events, rec)
	s.mu.Unlock()
	return nil
}
func (s *atomicResumeSink) Reseed(rs protocol.JournalReseed) error {
	s.mu.Lock()
	s.reseeds = append(s.reseeds, rs)
	s.mu.Unlock()
	return nil
}
func (s *atomicResumeSink) FullResync(rs protocol.JournalReseed) error { return s.Reseed(rs) }

func serveAtomicResume(t *testing.T, ln net.Listener, endpoint string, full bool, got chan<- protocol.Control) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, body, err := wire.ReadFrame(conn)
	if err != nil {
		return
	}
	hello, _ := protocol.DecodeControl(body)
	if hello.Op != protocol.OpHello {
		return
	}
	offered := map[string]bool{}
	for _, cap := range hello.Capabilities {
		offered[cap] = true
	}
	var negotiated []string
	for _, cap := range []string{protocol.CapJournal, protocol.CapJournalSubscribeFrom} {
		if offered[cap] {
			negotiated = append(negotiated, cap)
		}
	}
	reply, _ := protocol.EncodeControl(protocol.Control{
		Op: protocol.OpHello, EndpointID: endpoint, ProtocolVersion: protocol.Version,
		Capabilities: negotiated,
	})
	if wire.WriteFrame(conn, wire.TControl, reply) != nil {
		return
	}
	_, body, err = wire.ReadFrame(conn)
	if err != nil {
		return
	}
	req, _ := protocol.DecodeControl(body)
	got <- req
	res, _ := protocol.EncodeControl(protocol.Control{
		Op: protocol.OpJournalSubscribeFrom, EndpointID: endpoint, Cursor: 7, FullResync: full,
		Roster:  []protocol.JournalRecord{{SessionID: "s", Type: "roster"}},
		Journal: []protocol.JournalRecord{{Cursor: 7, SessionID: "s", Type: "session_state"}},
	})
	_ = wire.WriteFrame(conn, wire.TControl, res)
}

func TestGateway_UsesNegotiatedAtomicSubscribeFrom(t *testing.T) {
	sock, ln := journalSocket(t)
	got := make(chan protocol.Control, 1)
	go serveAtomicResume(t, ln, "m", false, got)
	sink := &atomicResumeSink{cursor: 5}
	err := New(sock, sink).RunJournal(context.Background())
	if err == nil {
		t.Fatal("one-shot daemon close unexpectedly returned nil")
	}
	req := <-got
	if req.Op != protocol.OpJournalSubscribeFrom || req.Cursor != 5 {
		t.Fatalf("atomic request = %#v, want negotiated subscribe-from cursor 5", req)
	}
	if sink.snapshot != 1 || len(sink.events) != 1 || sink.events[0].Cursor != 7 {
		t.Fatalf("sink snapshot=%d events=%#v", sink.snapshot, sink.events)
	}
}

func TestGateway_FullResyncPublishesOneAtomicReseedAndAdvances(t *testing.T) {
	sock, ln := journalSocket(t)
	got := make(chan protocol.Control, 1)
	go serveAtomicResume(t, ln, "m", true, got)
	sink := &atomicResumeSink{cursor: 1}
	g := New(sock, sink)
	_ = g.RunJournal(context.Background())
	<-got
	if sink.snapshot != 0 || len(sink.events) != 0 || len(sink.reseeds) != 1 {
		t.Fatalf("full resync snapshot=%d events=%d reseeds=%d; want only one atomic reseed", sink.snapshot, len(sink.events), len(sink.reseeds))
	}
	if rs := sink.reseeds[0]; rs.Cursor != 7 || len(rs.Roster) != 1 || len(rs.Events) != 1 {
		t.Fatalf("full resync payload = %#v", rs)
	}
	if g.Cursor() != 7 {
		t.Fatalf("gateway cursor = %d, want repaired boundary 7", g.Cursor())
	}
}

func TestRelaySink_ReseedDurablyCommitsRepairedCursor(t *testing.T) {
	app := &countingAppender{}
	sink, outbox := outboxTestSink(t, app, "", "")
	if err := sink.Reseed(protocol.JournalReseed{Cursor: 19}); err != nil {
		t.Fatal(err)
	}
	if got := outbox.Cursor(); got != 19 {
		t.Fatalf("outbox cursor = %d, want repaired boundary 19", got)
	}
}
