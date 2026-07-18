package shim

// R1.4.1(d) perf baseline benchmark (docs/verification/perf-baseline-2026-07-18.md):
// hub.feed fanout cost — the PTY drain loop's per-chunk emulator Feed +
// transcript Write + subscriber publish, all under h.mu (server.go).

import (
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/transcript"
	"github.com/Nathandela/swarm/internal/vt"
)

func BenchmarkHubFeed(b *testing.B) {
	emu := vt.NewEmulator(80, 24)
	defer emu.Close()
	tr, err := transcript.New(filepath.Join(b.TempDir(), "transcript.log"), transcript.Config{MaxBytes: 10 << 20, MaxFiles: 3})
	if err != nil {
		b.Fatal(err)
	}
	defer tr.Close()

	h := &hub{emu: emu, tr: tr, metrics: &Metrics{}}
	sub := &subscriber{queue: make(chan []byte, subQueueCap), done: make(chan struct{})}
	h.mu.Lock()
	h.sub = sub
	h.mu.Unlock()

	// A live-reader stand-in: drains the queue exactly as a connection's writer
	// goroutine would, so feed's fanout send has a consumer instead of always
	// hitting the drop path.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-sub.queue:
			case <-stop:
				return
			}
		}
	}()
	defer close(stop)

	payload := []byte("agent output line, some of it \x1b[32mcolored\x1b[0m, some plain\r\n")

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.feed(payload)
	}
}
