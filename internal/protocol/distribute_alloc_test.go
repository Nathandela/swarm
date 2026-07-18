package protocol

// R3.3.1 (agents-tracker-tbw) — marshal-once fan-out. Production always runs
// with ONE stable endpoint id (skeleton's NewServer(d.api, epID) assembly), so
// every subscriber's OpEvent Control is byte-identical; distribute() must
// marshal it ONCE per event, not once per subscriber (before this item,
// server.go:294's per-subscriber Control + eventWriter's per-subscriber
// EncodeControl repeated the identical marshal N times). The per-connection
// ep-<seq> fallback (Serve, or NewServer(d, "")) has no stable id to share,
// so it must keep marshaling individually — pinned by
// TestDistributeFallbackNamespacesPerConnection below.

import (
	"net"
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

// TestDistributeAllocsPerEvent pins the marshal-once budget: distribute()
// against many subscribers sharing one stable endpoint id (the production
// topology) must not allocate proportionally to subscriber count. Before
// R3.3.1, this measured ~256 allocs/op at 128 subscribers (2/subscriber,
// docs/verification/perf-baseline-2026-07-18.md, BenchmarkDistribute_128Subs);
// the budget below sits well under that and comfortably above the handful of
// allocs one shared marshal costs.
func TestDistributeAllocsPerEvent(t *testing.T) {
	const numSubs = 128
	const allocBudget = 20

	s := NewServer(newStubDaemon(), "ep-daemon") // production: one stable endpoint id
	defer s.Close()

	conns := make([]*clientConn, numSubs)
	for i := range conns {
		serverSide, clientSide := net.Pipe()
		defer clientSide.Close()
		conns[i] = &clientConn{
			endpointID: s.endpointID, // every connection shares the daemon's stable id
			eventQ:     make(chan []byte, eventQueueCap),
			conn:       serverSide,
			done:       make(chan struct{}),
		}
	}
	s.subMu.Lock()
	for _, cc := range conns {
		s.subs[cc] = struct{}{}
	}
	s.subMu.Unlock()

	m := statusMeta("sess1", status.TurnActive, status.InteractionNone)

	allocs := testing.AllocsPerRun(50, func() {
		s.distribute(m)
		for _, cc := range conns {
			<-cc.eventQ
		}
	})
	if allocs > allocBudget {
		t.Fatalf("distribute() allocated %.1f allocs/op fanning out to %d subscribers sharing one endpoint id, want <= %d "+
			"(marshal must happen ONCE per event, not once per subscriber — R3.3.1)", allocs, numSubs, allocBudget)
	}
}

// TestDistributeFallbackNamespacesPerConnection pins that the per-connection
// ep-<seq> fallback (Serve, or NewServer(d, "")) — where each connection has
// its OWN endpoint id, so the marshal-once shortcut must NOT apply — still
// gives each subscriber its own correctly namespaced event, never another
// subscriber's shared payload.
func TestDistributeFallbackNamespacesPerConnection(t *testing.T) {
	s := newServer(newStubDaemon()) // fallback: no stable endpoint id (endpointID == "")
	defer s.Close()

	makeConn := func(epID string) *clientConn {
		serverSide, clientSide := net.Pipe()
		t.Cleanup(func() { clientSide.Close() })
		return &clientConn{
			endpointID: epID,
			eventQ:     make(chan []byte, eventQueueCap),
			conn:       serverSide,
			done:       make(chan struct{}),
		}
	}
	cc1 := makeConn("ep-1")
	cc2 := makeConn("ep-2")
	s.subMu.Lock()
	s.subs[cc1] = struct{}{}
	s.subs[cc2] = struct{}{}
	s.subMu.Unlock()

	m := statusMeta("sess1", status.TurnActive, status.InteractionNone)
	s.distribute(m)

	for _, tc := range []struct {
		cc   *clientConn
		want string
	}{{cc1, "ep-1"}, {cc2, "ep-2"}} {
		body := <-tc.cc.eventQ
		ctrl, err := DecodeControl(body)
		if err != nil {
			t.Fatalf("DecodeControl: %v", err)
		}
		if ctrl.EndpointID != tc.want {
			t.Errorf("EndpointID = %q, want %q (fallback path must not share the shared-mode payload)", ctrl.EndpointID, tc.want)
		}
		wantID := NamespacedID(tc.want, "sess1")
		if ctrl.Session == nil || ctrl.Session.ID != wantID {
			t.Errorf("Session.ID = %v, want %q", ctrl.Session, wantID)
		} else if ctrl.Session.EndpointID != tc.want {
			t.Errorf("Session.EndpointID = %q, want %q", ctrl.Session.EndpointID, tc.want)
		}
	}
}
