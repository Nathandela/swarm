package shim

// Item 1.3 (agents-tracker-445), T1.3.d — hub.attach must tear down the prior
// subscriber before installing a new one. Under concurrent serving a second
// attach (from a different connection) would otherwise clobber h.sub, leaving the
// superseded writer goroutine blocked forever on its never-closed queue and the
// superseded client silently frozen (R1.3.3). White-box: drives the hub directly.

import (
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/transcript"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/Nathandela/swarm/internal/wire"
)

// attachConn adapts the hub.attach call for the tests; it is the single site that
// changes if attach's writer-serialization parameter type changes.
func attachConn(h *hub, conn net.Conn) *subscriber { return h.attach(&connWriter{conn: conn}) }

// frameSink records every frame read off one end of a pipe, in order.
type frameSink struct {
	mu     sync.Mutex
	frames []frameRec
}

func (s *frameSink) add(typ wire.Type, p []byte) {
	s.mu.Lock()
	s.frames = append(s.frames, frameRec{typ, append([]byte(nil), p...)})
	s.mu.Unlock()
}

func (s *frameSink) count(typ wire.Type) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, f := range s.frames {
		if f.typ == typ {
			n++
		}
	}
	return n
}

// newDrainedConn returns one end of a pipe for the hub writer to write to, with
// its peer continuously drained into a frameSink so a writer goroutine never
// blocks on a full pipe.
func newDrainedConn(t *testing.T) (net.Conn, *frameSink) {
	t.Helper()
	cl, sv := net.Pipe()
	sink := &frameSink{}
	go func() {
		for {
			typ, p, err := wire.ReadFrame(cl)
			if err != nil {
				return
			}
			sink.add(typ, p)
		}
	}()
	t.Cleanup(func() { _ = cl.Close(); _ = sv.Close() })
	return sv, sink
}

func newHubTranscript(t *testing.T) *transcript.Writer {
	t.Helper()
	tr, err := transcript.New(filepath.Join(t.TempDir(), "t.log"), transcript.Config{MaxBytes: 8 << 20, MaxFiles: 3})
	if err != nil {
		t.Fatalf("transcript.New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func waitFrameCount(t *testing.T, sink *frameSink, typ wire.Type, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sink.count(typ) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("frame type %d count = %d, want >= %d within 2s", typ, sink.count(typ), want)
}

func TestHub_SupersedeTearsDownPriorSubscriber(t *testing.T) {
	emu := vt.NewEmulator(80, 24)
	defer emu.Close()
	h := &hub{emu: emu, tr: newHubTranscript(t), metrics: &Metrics{}}

	conn1, sink1 := newDrainedConn(t)
	sub1 := attachConn(h, conn1)
	waitFrameCount(t, sink1, wire.TSnapshot, 1) // sub1's writer is up and streaming

	// A second attach (a different connection) supersedes sub1. It MUST close
	// sub1's queue so its writer exits, rather than clobbering h.sub.
	conn2, sink2 := newDrainedConn(t)
	sub2 := attachConn(h, conn2)

	select {
	case <-sub1.done:
	case <-time.After(2 * time.Second):
		t.Fatal("superseded subscriber's writer did not exit — hub.attach clobbered h.sub without teardown (R1.3.3)")
	}

	// The new subscriber gets the snapshot; a live feed reaches ONLY it (no double
	// delivery to the torn-down subscriber).
	waitFrameCount(t, sink2, wire.TSnapshot, 1)
	before1 := sink1.count(wire.TDataOut)
	h.feed([]byte("LIVE-AFTER-SUPERSEDE"))
	waitFrameCount(t, sink2, wire.TDataOut, 1)
	if extra := sink1.count(wire.TDataOut) - before1; extra != 0 {
		t.Errorf("superseded subscriber received %d live frames after supersede; want 0 (no double delivery)", extra)
	}

	h.detach(sub2)
	<-sub2.done
}
