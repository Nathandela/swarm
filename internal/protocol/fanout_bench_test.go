package protocol

// R1.4.1(e) perf baseline benchmark (docs/verification/perf-baseline-2026-07-18.md):
// the status-fanout distribute() path (server.go), at increasing subscriber
// counts, to characterize L1 fanout cost as it scales.
//
// R3.3.1 (agents-tracker-tbw): production always runs with ONE stable endpoint
// id (skeleton's NewServer(d.api, epID) assembly), so every subscriber's event
// is byte-identical and distribute() marshals it ONCE per event rather than
// once per subscriber. BenchmarkDistribute_* below now mirrors that production
// topology — one shared endpoint id, exactly as hello would assign it to every
// connection — so the marshal-once win is visible in these numbers.
// BenchmarkDistribute_128Subs_FallbackEndpoint keeps the OLD per-connection
// ep-<seq> topology (Serve, or NewServer(d, "") — no stable daemon identity)
// to pin that the fallback path's per-subscriber marshal cost is unchanged.
import (
	"fmt"
	"net"
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

// benchDistribute times s.distribute(m) against numSubs fake subscribers,
// draining each subscriber's queue synchronously after every call. A
// goroutine-per-subscriber drain would race an unthrottled tight loop (this
// benchmark, unlike production's serial fanoutLoop, has no other work pacing
// it) and can lose that race, evicting subscribers (S9's wedged-subscriber
// path) and silently degrading the benchmark into iterating an emptied
// s.subs map. Draining in-loop keeps the measurement honest and eviction-free.
//
// sharedEndpoint selects the topology: true mirrors production (one stable
// endpoint id, shared by every connection); false mirrors the ep-<seq>
// fallback (each connection has its own endpoint id).
func benchDistribute(b *testing.B, numSubs int, sharedEndpoint bool) {
	var s *Server
	if sharedEndpoint {
		s = NewServer(newStubDaemon(), "ep-daemon")
	} else {
		s = newServer(newStubDaemon())
	}
	defer s.Close()

	conns := make([]*clientConn, numSubs)
	for i := range conns {
		serverSide, clientSide := net.Pipe()
		defer clientSide.Close()
		epID := s.endpointID
		if !sharedEndpoint {
			epID = fmt.Sprintf("ep-%d", i)
		}
		conns[i] = &clientConn{
			endpointID: epID,
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

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.distribute(m)
		for _, cc := range conns {
			<-cc.eventQ
		}
	}
}

func BenchmarkDistribute_1Sub(b *testing.B)    { benchDistribute(b, 1, true) }
func BenchmarkDistribute_16Subs(b *testing.B)  { benchDistribute(b, 16, true) }
func BenchmarkDistribute_128Subs(b *testing.B) { benchDistribute(b, 128, true) }

// BenchmarkDistribute_128Subs_FallbackEndpoint pins the per-connection
// ep-<seq> fallback's cost: distribute() cannot share one marshaled payload
// across subscribers whose endpoint id (and therefore namespaced session id)
// differs, so this is expected to cost roughly what BenchmarkDistribute_128Subs
// cost BEFORE R3.3.1 (one marshal per subscriber, ~256 allocs/op).
func BenchmarkDistribute_128Subs_FallbackEndpoint(b *testing.B) { benchDistribute(b, 128, false) }
