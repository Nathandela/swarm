package remotegw

import (
	"context"
	"encoding/json"
	"errors"
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
	mu        sync.Mutex
	reseeds   []protocol.JournalReseed
	snapshots []protocol.JournalReseed
	machine   string
}

func (s *reseedCapture) Snapshot(roster []protocol.JournalRecord, cursor uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots = append(s.snapshots, protocol.JournalReseed{Roster: roster, Events: []protocol.JournalRecord{}, Cursor: cursor})
	return nil
}
func (s *reseedCapture) RecoverySnapshot(roster []protocol.JournalRecord, cursor uint64, recoveryToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots = append(s.snapshots, protocol.JournalReseed{
		Roster: roster, Events: []protocol.JournalRecord{}, Cursor: cursor, DiscardRecoveryToken: recoveryToken,
	})
	return nil
}
func (*reseedCapture) Event(protocol.JournalRecord) error     { return nil }
func (*reseedCapture) Terminal(protocol.TerminalViewV1) error { return nil }
func (s *reseedCapture) SetMachine(machine string)            { s.machine = machine }
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

func (s *orderingReseedSink) Snapshot([]protocol.JournalRecord, uint64) error {
	close(s.started)
	<-s.release
	s.mu.Lock()
	s.calls = append(s.calls, "snapshot")
	s.mu.Unlock()
	return nil
}
func (*orderingReseedSink) Terminal(protocol.TerminalViewV1) error { return nil }
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
	if err := g.Resync(ctx, 42, true, false, ""); err != nil {
		t.Fatalf("roster refresh over 7 MiB backlog: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("fake daemon: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.snapshots) != 1 || len(sink.reseeds) != 0 {
		t.Fatalf("snapshot/reseed counts = %d/%d, want one snapshot recovery anchor", len(sink.snapshots), len(sink.reseeds))
	}
	got := sink.snapshots[0]
	if got.Cursor != 42 || got.Events == nil || len(got.Events) != 0 {
		t.Fatalf("roster refresh = cursor %d, %d events; want prior cursor 42 and no backlog events", got.Cursor, len(got.Events))
	}
	if len(got.Roster) != 1 || got.Roster[0].SessionID != "machine/s1" {
		t.Fatalf("roster refresh roster = %#v, want namespaced authoritative roster", got.Roster)
	}
	if sink.machine != "machine" {
		t.Fatalf("roster refresh stamped machine %q, want daemon endpoint machine", sink.machine)
	}
}

func TestGatewayDiscardedBacklogRosterRefreshFastForwardsWithoutReplayingEvents(t *testing.T) {
	sock, ln := journalSocket(t)
	done := make(chan error, 1)
	go servePagedResync(t, ln, 42, 2, done)
	sink := &reseedCapture{}
	g := New(sock, sink)
	const recoveryToken = "0123456789abcdef0123456789abcdef"
	if err := g.Resync(context.Background(), 42, true, true, recoveryToken); err != nil {
		t.Fatalf("discard recovery roster refresh: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("fake daemon: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.snapshots) != 1 || len(sink.reseeds) != 0 {
		t.Fatalf("snapshot/reseed counts = %d/%d, want one bounded snapshot", len(sink.snapshots), len(sink.reseeds))
	}
	got := sink.snapshots[0]
	if got.Cursor != 44 || got.Events == nil || len(got.Events) != 0 || got.DiscardRecoveryToken != recoveryToken {
		t.Fatalf("discard recovery = cursor %d, %d events, token %q; want daemon final cursor 44, no discarded events, exact token", got.Cursor, len(got.Events), got.DiscardRecoveryToken)
	}
}

func TestGatewayDiscardRecoveryFlagAndTokenMustAgree(t *testing.T) {
	g := New("unused", &reseedCapture{})
	for _, tc := range []struct {
		rosterOnly bool
		discarded  bool
		token      string
	}{
		{rosterOnly: true, discarded: true},
		{rosterOnly: true, token: "0123456789abcdef0123456789abcdef"},
		{discarded: true, token: "0123456789abcdef0123456789abcdef"},
		{rosterOnly: true, discarded: true, token: "short"},
		{rosterOnly: true, discarded: true, token: "0123456789ABCDEF0123456789ABCDEF"},
		{rosterOnly: true, discarded: true, token: "0123456789abcdef0123456789abcdeg"},
	} {
		if err := g.Resync(context.Background(), 42, tc.rosterOnly, tc.discarded, tc.token); !errors.Is(err, errInvalidDiscardRecovery) {
			t.Fatalf("Resync(roster_only=%t, discarded=%t, token=%q) = %v, want errInvalidDiscardRecovery", tc.rosterOnly, tc.discarded, tc.token, err)
		}
	}
}

func TestGatewayOrdinaryJournalRepairStillCarriesEventsAndFinalCursor(t *testing.T) {
	sock, ln := journalSocket(t)
	done := make(chan error, 1)
	go servePagedResync(t, ln, 42, 2, done)
	sink := &reseedCapture{}
	g := New(sock, sink)
	if err := g.Resync(context.Background(), 42, false, false, ""); err != nil {
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

func TestGatewayOrdinaryJournalRepairBoundsAnOversizedReseedAtTheFinalCursor(t *testing.T) {
	sock, ln := journalSocket(t)
	done := make(chan error, 1)
	const records = 480 // >7 MiB across bounded daemon pages; one relay append cannot carry it.
	go servePagedResync(t, ln, 42, records, done)
	sink := &reseedCapture{}
	g := New(sock, sink)
	if err := g.Resync(context.Background(), 42, false, false, ""); err != nil {
		t.Fatalf("ordinary oversized journal repair: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("fake daemon: %v", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.reseeds) != 1 {
		t.Fatalf("reseed count = %d, want one atomic replacement", len(sink.reseeds))
	}
	got := sink.reseeds[0]
	if got.Cursor != 42+records || len(got.Roster) != 1 {
		t.Fatalf("bounded reseed lost final authority: cursor=%d roster=%#v", got.Cursor, got.Roster)
	}
	if len(got.Events) == 0 || len(got.Events) >= records {
		t.Fatalf("bounded reseed retained %d/%d events, want a non-empty newest tail", len(got.Events), records)
	}
	body, err := json.Marshal(reseedFrame{Kind: kindJournalReseed, JournalReseed: got})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > wire.MaxFrame-1 {
		t.Fatalf("bounded reseed plaintext = %d bytes, still over the relay's 1 MiB frame", len(body))
	}
}

func TestGatewayRosterRefreshOrdersNewerLiveEventAfterReseed(t *testing.T) {
	sock, ln := journalSocket(t)
	serverDone := make(chan error, 1)
	go servePagedResync(t, ln, 42, 0, serverDone)
	sink := &orderingReseedSink{started: make(chan struct{}), release: make(chan struct{})}
	g := New(sock, sink)
	resyncDone := make(chan error, 1)
	go func() { resyncDone <- g.Resync(context.Background(), 42, true, false, "") }()
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
	if len(sink.calls) != 2 || sink.calls[0] != "snapshot" || sink.calls[1] != "event" {
		t.Fatalf("delivery order = %v, want [snapshot event]", sink.calls)
	}
}

func TestGatewayRosterRefreshRetriesAReconcileWhoseReseedAppendWasRefused(t *testing.T) {
	sock, ln := journalSocket(t)
	serverDone := make(chan error, 1)
	go servePagedResync(t, ln, 42, 0, serverDone)

	key := reconcileTestKey()
	app := &refuseNthAppender{refuse: 2}
	sink := newReconcileSink(app, key, testAuthorities, nil)
	g := New(sock, sink)
	if err := g.Resync(context.Background(), 42, true, false, ""); err != nil {
		t.Fatalf("roster refresh after transient second append refusal: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("fake daemon: %v", err)
	}

	app.mu.Lock()
	stored := append([][]byte(nil), app.stored...)
	calls := app.calls
	app.mu.Unlock()
	if calls != 4 || len(stored) != 3 {
		t.Fatalf("append calls/stored = %d/%d, want 4/3 (reconcile, refused reseed, reconcile, reseed)", calls, len(stored))
	}

	kinds := make([]string, 0, len(stored))
	seqs := make([]uint64, 0, len(stored))
	for _, raw := range stored {
		hdr, plain := openPlaintext(t, key, raw)
		var tagged struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(plain, &tagged); err != nil {
			t.Fatalf("decode stored plaintext: %v", err)
		}
		kinds = append(kinds, tagged.Kind)
		seqs = append(seqs, hdr.Seq)
	}
	if got := strings.Join(kinds, ","); got != "reconcile,reconcile,journal_reseed" {
		t.Fatalf("stored kinds = %s, want reconcile,reconcile,journal_reseed", got)
	}
	if seqs[2] != seqs[1]+1 {
		t.Fatalf("replacement reconcile/reseed seqs = %v, want final pair contiguous", seqs)
	}

	_, reconcilePlain := openPlaintext(t, key, stored[1])
	var rec reconcileFrame
	if err := json.Unmarshal(reconcilePlain, &rec); err != nil {
		t.Fatalf("decode replacement reconcile: %v", err)
	}
	if rec.JournalCeiling != seqs[1] {
		t.Fatalf("replacement reconcile ceiling = %d, want own seq %d", rec.JournalCeiling, seqs[1])
	}
	_, reseedPlain := openPlaintext(t, key, stored[2])
	var reseed reseedFrame
	if err := json.Unmarshal(reseedPlain, &reseed); err != nil {
		t.Fatalf("decode replacement reseed: %v", err)
	}
	if reseed.Cursor != 42 || reseed.Events == nil || len(reseed.Events) != 0 {
		t.Fatalf("replacement reseed = cursor %d, events %#v; want prior cursor 42 and explicit empty events", reseed.Cursor, reseed.Events)
	}
}
