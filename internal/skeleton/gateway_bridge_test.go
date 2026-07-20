package skeleton

// Integration test for the R-GW daemon-facing journal bridge (internal/remotegw): the
// gateway dials the assembled daemon's remote.sock, takes the atomic roster+cursor
// snapshot, then streams live journal events -- exercised against a REAL daemon (real
// journal, real remote-tier server), not a stub. It proves a session live before the
// gateway connects is enumerable via the snapshot, and a session launched after is
// delivered live, with the gateway's cursor advancing.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// collectSink records every session id the gateway bridges (from the roster snapshot
// and from live events) and signals each arrival.
type collectSink struct {
	mu       sync.Mutex
	seen     map[string]bool
	snapshot uint64
	changed  chan struct{}
}

func newCollectSink() *collectSink {
	return &collectSink{seen: map[string]bool{}, changed: make(chan struct{}, 64)}
}

func (s *collectSink) Snapshot(roster []protocol.JournalRecord, cursor uint64) {
	s.mu.Lock()
	s.snapshot = cursor
	for _, r := range roster {
		if r.SessionID != "" {
			s.seen[r.SessionID] = true
		}
	}
	s.mu.Unlock()
	s.signal()
}

func (s *collectSink) Event(rec protocol.JournalRecord) {
	s.mu.Lock()
	if rec.SessionID != "" {
		s.seen[rec.SessionID] = true
	}
	s.mu.Unlock()
	s.signal()
}

func (s *collectSink) signal() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *collectSink) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[id]
}

// waitFor blocks until the sink has recorded id or the deadline elapses.
func (s *collectSink) waitFor(t *testing.T, id string, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		if s.has(id) {
			return
		}
		select {
		case <-s.changed:
		case <-deadline:
			t.Fatalf("gateway did not bridge session %s within %s", id, within)
		}
	}
}

func TestRGW_GatewayBridgesRosterThenLiveEvents(t *testing.T) {
	sk, rsock := assembleWithRemote(t)

	// A session live BEFORE the gateway connects: it must be enumerable via the atomic
	// roster snapshot (or the snapshot's launched event).
	before := launchFake(t, sk, "print BEFORE\nidle 60s\n")

	sink := newCollectSink()
	gw := remotegw.New(rsock, sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- gw.RunJournal(ctx) }()

	// The gateway namespaces journal ids at its remote egress (agents-tracker-p1b), so
	// the phone-facing sink sees <endpoint>/<local>, matching command targets.
	sink.waitFor(t, protocol.NamespacedID(sk.api.endpointID, before.ID), 10*time.Second)

	// A session launched AFTER the gateway subscribed must arrive as a live event.
	after := launchFake(t, sk, "print AFTER\nidle 60s\n")
	sink.waitFor(t, protocol.NamespacedID(sk.api.endpointID, after.ID), 10*time.Second)

	// The gateway advanced its resume cursor past the initial snapshot.
	if got := gw.Cursor(); got == 0 {
		t.Fatalf("gateway cursor did not advance past 0 after bridging live events")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("RunJournal did not return after cancel")
	}
}
