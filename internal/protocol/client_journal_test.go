package protocol

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/wire"
)

func TestClientJournalSubscribeFromSnapshotThenLive(t *testing.T) {
	js := newJournalStub()
	js.resume = JournalResume{Cursor: 20, FullResync: true,
		Roster: []JournalRecord{{SessionID: "live", Type: "roster"}}}
	item := json.RawMessage(`{"kind":"agent_message","status":"completed","text":"` + strings.Repeat("x", 64<<10) + `"}`)
	for i := uint64(1); i <= 20; i++ {
		js.resume.Events = append(js.resume.Events, JournalRecord{Cursor: i, SessionID: "live", Type: "interaction", Item: item})
	}
	sock, _ := serveJournal(t, js)
	c := dialClient(t, sock, []string{CapJournal, CapJournalSubscribeFrom})
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, live, err := c.JournalSubscribeFrom(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.FullResync || res.Cursor != 20 || len(res.Roster) != 1 || len(res.Events) != 20 {
		t.Fatalf("incomplete atomic snapshot: cursor=%d full=%v roster=%d events=%d", res.Cursor, res.FullResync, len(res.Roster), len(res.Events))
	}
	js.atomicSource <- JournalRecord{Cursor: 21, SessionID: "live", Type: "exited"}
	select {
	case rec := <-live:
		if rec.Cursor != 21 {
			t.Fatalf("live cursor=%d", rec.Cursor)
		}
	case <-time.After(recvTimeout):
		t.Fatal("missing live record")
	}
	cancel()
	select {
	case _, ok := <-live:
		if ok {
			t.Fatal("live stream remained open after cancel")
		}
	case <-time.After(recvTimeout):
		t.Fatal("cancel did not close stream")
	}
}

func TestClientJournalSubscribeFromSlowConsumerDisconnects(t *testing.T) {
	js := newJournalStub()
	sock, _ := serveJournal(t, js)
	c := dialClient(t, sock, []string{CapJournal, CapJournalSubscribeFrom})
	defer func() { _ = c.Close() }()
	_, live, err := c.JournalSubscribeFrom(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= eventQueueCap+1; i++ {
		c.dispatchControl(Control{Op: OpJournalEvent, Journal: []JournalRecord{{Cursor: i, Type: "session_state"}}})
	}
	select {
	case <-c.done:
	case <-time.After(recvTimeout):
		t.Fatal("slow consumer did not force disconnect")
	}
	for range live {
	}
}

func TestClientJournalSubscribeFromRequiresCapability(t *testing.T) {
	js := newJournalStub()
	sock, _ := serveJournal(t, js)
	c := dialClient(t, sock, []string{CapJournal})
	defer func() { _ = c.Close() }()
	if _, _, err := c.JournalSubscribeFrom(context.Background(), 0); err == nil {
		t.Fatal("unnegotiated atomic subscription succeeded")
	}
}

func TestClientJournalSubscribeFromRefusalAndMalformedPage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages []Control
	}{
		{"refusal", []Control{{Op: OpError, Error: "denied"}}},
		{"changed cursor", []Control{{Op: OpJournalSubscribeFrom, Cursor: 3, JournalMore: true}, {Op: OpJournalSubscribeFrom, Cursor: 4}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, peer := journalPipeClient(t)
			defer func() { _ = peer.Close() }()
			result := make(chan error, 1)
			go func() { _, _, err := c.JournalSubscribeFrom(context.Background(), 0); result <- err }()
			if _, _, err := wire.ReadFrame(peer); err != nil {
				t.Fatal(err)
			}
			for _, page := range tc.pages {
				body, err := EncodeControl(page)
				if err != nil {
					t.Fatal(err)
				}
				if err := wire.WriteFrame(peer, wire.TControl, body); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("invalid snapshot accepted")
				}
			case <-time.After(recvTimeout):
				t.Fatal("invalid snapshot did not fail")
			}
		})
	}
}

func TestClientJournalSubscribeFromCancelDuringSnapshot(t *testing.T) {
	c, peer := journalPipeClient(t)
	defer func() { _ = peer.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, _, err := c.JournalSubscribeFrom(ctx, 0); result <- err }()
	if _, _, err := wire.ReadFrame(peer); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("cancel error = %v", err)
		}
	case <-time.After(recvTimeout):
		t.Fatal("cancel did not interrupt snapshot")
	}
	select {
	case <-c.done:
	case <-time.After(recvTimeout):
		t.Fatal("cancel left connection open")
	}
}

func TestClientJournalSubscribeFromCancelBlockedWrite(t *testing.T) {
	c, peer := journalPipeClient(t)
	defer func() { _ = peer.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, _, err := c.JournalSubscribeFrom(ctx, 0); result <- err }()
	// net.Pipe has no buffer, so an unread request remains in writeControl.
	cancel()
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("cancel error = %v", err)
		}
	case <-time.After(recvTimeout):
		t.Fatal("cancel did not interrupt blocked write")
	}
}

func journalPipeClient(t *testing.T) (*Client, net.Conn) {
	t.Helper()
	conn, peer := net.Pipe()
	c := &Client{conn: conn, endpointID: "test", caps: []string{CapJournal, CapJournalSubscribeFrom}, respCh: make(chan Control, 1), done: make(chan struct{})}
	go c.readLoop()
	t.Cleanup(func() { _ = c.Close(); _ = peer.Close() })
	return c, peer
}
