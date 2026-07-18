package protocol

// R1.4.1(e) perf baseline benchmark (docs/verification/perf-baseline-2026-07-18.md):
// the status-fanout distribute() path (server.go), at increasing subscriber
// counts, to characterize L1 fanout cost as it scales.

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
func benchDistribute(b *testing.B, numSubs int) {
	s := newServer(newStubDaemon())
	defer s.Close()

	conns := make([]*clientConn, numSubs)
	for i := range conns {
		serverSide, clientSide := net.Pipe()
		defer clientSide.Close()
		conns[i] = &clientConn{
			endpointID: fmt.Sprintf("ep-%d", i),
			eventQ:     make(chan Control, eventQueueCap),
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

func BenchmarkDistribute_1Sub(b *testing.B)    { benchDistribute(b, 1) }
func BenchmarkDistribute_16Subs(b *testing.B)  { benchDistribute(b, 16) }
func BenchmarkDistribute_128Subs(b *testing.B) { benchDistribute(b, 128) }
