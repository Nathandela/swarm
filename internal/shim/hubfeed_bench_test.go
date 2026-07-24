package shim

// R1.4.1(d) perf baseline benchmark (docs/verification/perf-baseline-2026-07-18.md):
// hub.feed fanout cost — the PTY drain loop's per-chunk emulator Feed +
// transcript Write + subscriber publish, all under h.mu (server.go).
//
// R2.2.4 (agents-tracker-m7o): BenchmarkHubFeed above uses a small payload, so
// its aggregate ns/op is dominated by fixed per-call overhead (mutex, channel
// send, transcript write syscall) rather than parse cost — the 2.2 reorder
// does not change the AMOUNT of work per call, only its order, so this bench
// is not expected to move much and is kept as the fanout-cost baseline.
// BenchmarkHubFeed_PublishLatency isolates the metric the reorder actually
// changes: wall-clock time from feed() entry to the chunk being available on
// the subscriber's queue, using a large styled payload (largeStyledPayload,
// hub_feed_order_test.go) whose parse cost dominates feed()'s total duration.
import (
	"path/filepath"
	"testing"
	"time"

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

// BenchmarkHubFeed_PublishLatency measures the time from feed() entry to the
// chunk arriving on the subscriber's queue, using a payload large enough
// (largeStyledPayload, hub_feed_order_test.go) that emu.Feed's parse cost
// dominates feed()'s total duration. Pre-2.2 (publish after parse), this
// tracks feed()'s full duration; post-2.2 (publish before parse), it should
// collapse to roughly the fixed per-call overhead, independent of payload
// size (R2.2.4).
func BenchmarkHubFeed_PublishLatency(b *testing.B) {
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

	payload := largeStyledPayload(80, 24, 200)

	sendTimes := make([]time.Time, b.N)
	recvTimes := make([]time.Time, b.N)
	done := make(chan struct{})
	go func() {
		for i := 0; i < b.N; i++ {
			<-sub.queue
			recvTimes[i] = time.Now()
		}
		close(done)
	}()

	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sendTimes[i] = time.Now()
		h.feed(payload)
	}
	<-done
	b.StopTimer()

	var total time.Duration
	for i := range sendTimes {
		total += recvTimes[i].Sub(sendTimes[i])
	}
	if b.N > 0 {
		b.ReportMetric(float64(total.Nanoseconds())/float64(b.N), "ns/publish-latency")
	}
}
