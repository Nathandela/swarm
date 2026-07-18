package shim

// Item 2.2 (agents-tracker-m7o), T2.2.a — hub.feed must publish to the
// subscriber queue + transcript BEFORE the (potentially slow) emulator parse,
// all still under one h.mu hold (R2.2.1). Per the committee's v2 correction,
// this is exercised with a STALLING (not panicking) real-emulator payload
// rather than a production test-double interface: a large styled chunk whose
// parse is measurably slow, so publish-latency and parse-latency are
// distinguishable by wall-clock timing alone. Fails under the pre-2.2 order
// (publish-after-parse), where the subscriber cannot observe the chunk until
// emu.Feed has already finished.

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/transcript"
	"github.com/Nathandela/swarm/internal/vt"
)

// largeStyledPayload builds a styled multi-row frame (SGR color change every 4
// cells, mirroring internal/vt's R1.4.1(a) baseline generator) repeated `reps`
// times, so the emulator's parse cost scales linearly and predictably with
// reps. At the measured baseline (~7MB/s styled 80x24, perf-baseline-2026-07-18.md)
// reps=200 yields well over 50ms of parse time even on a slow CI machine.
func largeStyledPayload(cols, rows, reps int) []byte {
	colors := []string{"31", "32", "33", "34", "35", "36", "37"}
	var frame bytes.Buffer
	ci := 0
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x += 4 {
			n := 4
			if x+n > cols {
				n = cols - x
			}
			frame.WriteString("\x1b[")
			frame.WriteString(colors[ci%len(colors)])
			frame.WriteByte('m')
			frame.Write(bytes.Repeat([]byte("x"), n))
			ci++
		}
		frame.WriteString("\x1b[0m\r\n")
	}
	one := frame.Bytes()
	return bytes.Repeat(one, reps)
}

// TestHub_FeedPublishesBeforeParse drives feed() with a large styled payload
// (measurably slow to parse) and asserts the subscriber observes the chunk on
// its queue in a tiny fraction of the time feed() itself takes to return —
// i.e. publish happens before, not after, the emulator parse (R2.2.1). Under
// the pre-fix order (emu.Feed, then transcript.Write, then publish), the
// subscriber cannot receive until parsing has already completed, so receipt
// time would track feed()'s total duration rather than being negligible next
// to it — the assertions below fail under that order.
func TestHub_FeedPublishesBeforeParse(t *testing.T) {
	emu := vt.NewEmulator(80, 24)
	defer emu.Close()
	tr, err := transcript.New(filepath.Join(t.TempDir(), "t.log"), transcript.Config{MaxBytes: 10 << 20, MaxFiles: 3})
	if err != nil {
		t.Fatalf("transcript.New: %v", err)
	}
	defer tr.Close()

	h := &hub{emu: emu, tr: tr, metrics: &Metrics{}}
	sub := &subscriber{queue: make(chan []byte, subQueueCap), done: make(chan struct{})}
	h.mu.Lock()
	h.sub = sub
	h.mu.Unlock()

	payload := largeStyledPayload(80, 24, 200)

	ready := make(chan struct{})
	recv := make(chan time.Duration, 1)
	var start time.Time
	go func() {
		close(ready)
		<-sub.queue
		recv <- time.Since(start)
	}()
	<-ready
	// Give the receiver goroutine a moment to actually reach the blocking
	// channel receive before we start timing, so scheduling jitter on launch
	// doesn't get counted against the receive-side latency budget.
	time.Sleep(5 * time.Millisecond)

	feedDone := make(chan time.Duration, 1)
	start = time.Now()
	go func() {
		h.feed(payload)
		feedDone <- time.Since(start)
	}()

	recvDur := <-recv
	feedDur := <-feedDone

	const recvBudget = 20 * time.Millisecond
	const parseFloor = 50 * time.Millisecond

	if feedDur < parseFloor {
		t.Fatalf("feed() returned in %v, want >= %v — payload too small to measure the reorder on this machine; increase largeStyledPayload reps", feedDur, parseFloor)
	}
	if recvDur > recvBudget {
		t.Errorf("subscriber received the chunk after %v, want < %v — publish is not happening before the parse (R2.2.1)", recvDur, recvBudget)
	}
	if recvDur >= feedDur {
		t.Errorf("subscriber receipt (%v) did not precede feed() return (%v) — publish is ordered after parse, not before (R2.2.1)", recvDur, feedDur)
	}
}
