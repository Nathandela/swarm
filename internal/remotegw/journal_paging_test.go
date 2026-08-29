package remotegw

import (
	"fmt"
	"net"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/wire"
)

func TestDaemonConnReadJournalAggregatesOneAtomicPagedRead(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	dc := &daemonConn{conn: client, endpointID: "machine"}

	serverDone := make(chan error, 1)
	go func() {
		typ, body, err := wire.ReadFrame(server)
		if err != nil {
			serverDone <- err
			return
		}
		if typ != wire.TControl {
			serverDone <- fmt.Errorf("request type = %d, want TControl", typ)
			return
		}
		req, err := protocol.DecodeControl(body)
		if err != nil {
			serverDone <- err
			return
		}
		if req.Op != protocol.OpJournalRead || req.Cursor != 0 {
			serverDone <- fmt.Errorf("request = %#v, want one journal_read from zero", req)
			return
		}
		if req.JournalMaxBytes != wire.MaxFrame-1 {
			serverDone <- fmt.Errorf("journal_max_bytes = %d, want %d", req.JournalMaxBytes, wire.MaxFrame-1)
			return
		}
		pages := []protocol.Control{
			{
				Op: protocol.OpJournalRead, Cursor: 4, JournalMore: true,
				Journal: []protocol.JournalRecord{{Cursor: 1}, {Cursor: 2}},
			},
			{
				Op: protocol.OpJournalRead, Cursor: 4,
				Journal: []protocol.JournalRecord{{Cursor: 3}, {Cursor: 4}},
				Roster:  []protocol.JournalRecord{{SessionID: "session", Type: "roster"}},
			},
		}
		for _, page := range pages {
			body, err := protocol.EncodeControl(page)
			if err != nil {
				serverDone <- err
				return
			}
			if err := wire.WriteFrame(server, wire.TControl, body); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	got, err := dc.readJournal(0)
	if err != nil {
		t.Fatalf("readJournal: %v", err)
	}
	if got.Cursor != 4 {
		t.Fatalf("cursor = %d, want 4", got.Cursor)
	}
	if len(got.Journal) != 4 {
		t.Fatalf("events = %#v, want four aggregated records", got.Journal)
	}
	for i, rec := range got.Journal {
		if want := uint64(i + 1); rec.Cursor != want {
			t.Fatalf("event %d cursor = %d, want %d", i, rec.Cursor, want)
		}
	}
	if len(got.Roster) != 1 || got.Roster[0].SessionID != "session" {
		t.Fatalf("roster = %#v, want the final-page roster", got.Roster)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server: %v", err)
	}
}
