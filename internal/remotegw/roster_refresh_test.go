package remotegw

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/wire"
)

type reseedCapture struct {
	mu      sync.Mutex
	reseeds []protocol.JournalReseed
}

func (*reseedCapture) Snapshot([]protocol.JournalRecord, uint64) error { return nil }
func (*reseedCapture) Event(protocol.JournalRecord) error              { return nil }
func (*reseedCapture) Terminal(protocol.TerminalViewV1) error          { return nil }
func (s *reseedCapture) Reseed(rs protocol.JournalReseed) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reseeds = append(s.reseeds, rs)
	return nil
}

type orderingReseedSink struct {
	mu      sync.Mutex
	calls   []string
	started chan struct{}
	release chan struct{}
}

func (*orderingReseedSink) Snapshot([]protocol.JournalRecord, uint64) error { return nil }
func (*orderingReseedSink) Terminal(protocol.TerminalViewV1) error          { return nil }
func (s *orderingReseedSink) Reseed(protocol.JournalReseed) error {
	close(s.started)
	<-s.release
	s.mu.Lock()
	s.calls = append(s.calls, "reseed")
	s.mu.Unlock()
	return nil
}
func (s *orderingReseedSink) Event(protocol.JournalRecord) error {
	s.mu.Lock()
	s.calls = append(s.calls, "event")
	s.mu.Unlock()
	return nil
}

func servePagedResync(t *testing.T, ln net.Listener, from uint64, count int, done chan<- error) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		done <- err
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	read := func() (protocol.Control, error) {
		typ, body, err := wire.ReadFrame(conn)
		if err != nil {
			return protocol.Control{}, err
		}
		if typ != wire.TControl {
			return protocol.Control{}, fmt.Errorf("frame type = %d, want TControl", typ)
		}
		return protocol.DecodeControl(body)
	}
	write := func(ctrl protocol.Control) error {
		body, err := protocol.EncodeControl(ctrl)
		if err != nil {
			return err
		}
		if len(body) > wire.MaxFrame-1 {
			return fmt.Errorf("test page = %d bytes, over wire cap", len(body))
		}
		return wire.WriteFrame(conn, wire.TControl, body)
	}
	hello, err := read()
	if err != nil || hello.Op != protocol.OpHello {
		done <- fmt.Errorf("hello: %#v: %w", hello, err)
		return
	}
	if err := write(protocol.Control{Op: protocol.OpHello, EndpointID: "machine", ProtocolVersion: protocol.Version}); err != nil {
		done <- err
		return
	}
	req, err := read()
	if err != nil || req.Op != protocol.OpJournalRead || req.Cursor != from || req.JournalMaxBytes != wire.MaxFrame-1 {
		done <- fmt.Errorf("journal read: %#v: %w", req, err)
		return
	}
	item := json.RawMessage(`{"text":"` + strings.Repeat("x", 16_000) + `"}`)
	const perPage = 48
	for start := 0; start < count || (count == 0 && start == 0); start += perPage {
		end := start + perPage
		if end > count {
			end = count
		}
		page := protocol.Control{Op: protocol.OpJournalRead, Cursor: from + uint64(count), JournalMore: end < count}
		for i := start; i < end; i++ {
			page.Journal = append(page.Journal, protocol.JournalRecord{
				Cursor: from + uint64(i+1), SessionID: "s1", Type: "interaction", Item: item,
			})
		}
		if !page.JournalMore {
			page.Roster = []protocol.JournalRecord{{SessionID: "s1", Type: "roster"}}
		}
		if err := write(page); err != nil {
			done <- err
			return
		}
		if count == 0 {
			break
		}
	}
	done <- nil
}

func TestGatewayRosterRefreshBoundsSevenMegabyteBacklogAndKeepsPriorCursor(t *testing.T) {
	sock, ln := journalSocket(t)
	done := make(chan error, 1)
	const records = 480 // >7 MiB of interaction JSON over bounded daemon pages.
	go servePagedResync(t, ln, 42, records, done)
	sink := &reseedCapture{}
	g := New(sock, sink)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := g.Resync(ctx, 42, true); err != nil {
		t.Fatalf("roster refresh over 7 MiB backlog: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("fake daemon: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.reseeds) != 1 {
		t.Fatalf("reseed count = %d, want one atomic roster refresh", len(sink.reseeds))
	}
	got := sink.reseeds[0]
	if got.Cursor != 42 || got.Events == nil || len(got.Events) != 0 {
		t.Fatalf("roster refresh = cursor %d, %d events; want prior cursor 42 and no backlog events", got.Cursor, len(got.Events))
	}
	if len(got.Roster) != 1 || got.Roster[0].SessionID != "machine/s1" {
		t.Fatalf("roster refresh roster = %#v, want namespaced authoritative roster", got.Roster)
	}
}

func TestGatewayOrdinaryJournalRepairStillCarriesEventsAndFinalCursor(t *testing.T) {
	sock, ln := journalSocket(t)
	done := make(chan error, 1)
	go servePagedResync(t, ln, 42, 2, done)
	sink := &reseedCapture{}
	g := New(sock, sink)
	if err := g.Resync(context.Background(), 42, false); err != nil {
		t.Fatalf("ordinary journal repair: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("fake daemon: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	got := sink.reseeds[0]
	if got.Cursor != 44 || len(got.Events) != 2 {
		t.Fatalf("ordinary repair = cursor %d, %d events; want final cursor 44 and both events", got.Cursor, len(got.Events))
	}
}

func TestGatewayRosterRefreshOrdersNewerLiveEventAfterReseed(t *testing.T) {
	sock, ln := journalSocket(t)
	serverDone := make(chan error, 1)
	go servePagedResync(t, ln, 42, 0, serverDone)
	sink := &orderingReseedSink{started: make(chan struct{}), release: make(chan struct{})}
	g := New(sock, sink)
	resyncDone := make(chan error, 1)
	go func() { resyncDone <- g.Resync(context.Background(), 42, true) }()
	<-sink.started

	eventDone := make(chan error, 1)
	go func() { eventDone <- g.deliver(protocol.JournalRecord{Cursor: 43, SessionID: "machine/s1"}) }()
	early := false
	select {
	case err := <-eventDone:
		if err != nil {
			t.Fatalf("deliver newer event: %v", err)
		}
		early = true
	case <-time.After(50 * time.Millisecond):
	}
	close(sink.release)
	if err := <-resyncDone; err != nil {
		t.Fatalf("roster refresh: %v", err)
	}
	if !early {
		if err := <-eventDone; err != nil {
			t.Fatalf("deliver newer event after reseed: %v", err)
		}
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("fake daemon: %v", err)
	}
	if early {
		t.Errorf("newer event passed while the older reseed was in flight")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.calls) != 2 || sink.calls[0] != "reseed" || sink.calls[1] != "event" {
		t.Fatalf("delivery order = %v, want [reseed event]", sink.calls)
	}
}
