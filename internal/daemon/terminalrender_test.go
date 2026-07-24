package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/vt"
)

// stubTerminalStream is a read-only session stream driven entirely by the test:
// a fixed initial snapshot and a caller-controlled Frames() channel. It satisfies
// the render loop's TerminalStream dependency (a structural subset of
// protocol.SessionStream) without dragging in the protocol package.
type stubTerminalStream struct {
	snap   []byte
	frames chan []byte
}

func (s *stubTerminalStream) Snapshot() []byte      { return s.snap }
func (s *stubTerminalStream) Frames() <-chan []byte { return s.frames }

// snapBytes renders feed through a real emulator of the given size and returns
// the versioned snapshot bytes a live SessionStream would carry.
func snapBytes(t *testing.T, cols, rows int, feed []byte) []byte {
	t.Helper()
	emu := vt.NewEmulator(cols, rows)
	defer emu.Close()
	if feed != nil {
		emu.Feed(feed)
	}
	b, err := emu.Snapshot()
	if err != nil {
		t.Fatalf("build snapshot bytes: %v", err)
	}
	return b
}

// collector accumulates pushed renders under a lock (the render loop pushes from
// its own goroutine when driven by the ticker path).
type collector struct {
	mu  sync.Mutex
	got []TerminalRender
}

func (c *collector) push(r TerminalRender) {
	c.mu.Lock()
	c.got = append(c.got, r)
	c.mu.Unlock()
}

func (c *collector) snapshots() []TerminalRender {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]TerminalRender(nil), c.got...)
}

// assertSanitized is the core security invariant: no rendered line may carry any
// terminal control character. It checks at the RUNE level (the exact granularity
// SnapText sanitizes at) so a legitimate multi-byte UTF-8 rune whose continuation
// bytes fall in 0x80-0x9f is never a false positive, while every real C0/C1/DEL
// control and embedded newline is caught.
func assertSanitized(t *testing.T, renders []TerminalRender) {
	t.Helper()
	for i, r := range renders {
		for j, line := range r.Lines {
			for _, ru := range line {
				switch {
				case ru < 0x20:
					t.Errorf("render %d line %d: C0 control %#x leaked (incl. embedded newline)", i, j, ru)
				case ru == 0x7f:
					t.Errorf("render %d line %d: DEL 0x7f leaked", i, j)
				case ru >= 0x80 && ru <= 0x9f:
					t.Errorf("render %d line %d: C1 control %#x leaked", i, j, ru)
				}
			}
			if strings.ContainsRune(line, '\n') {
				t.Errorf("render %d line %d: embedded newline leaked", i, j)
			}
		}
	}
}

// TestRenderLoop_HostilePTYCannotEscape is the security choke-point test: raw
// HOSTILE PTY bytes (CSI cursor control, C0 NUL/BEL/BS, embedded LF/CR, raw C1,
// DEL, an OSC title hijack) are fed through the REAL vt.Emulator + SnapText
// pipeline. Every pushed snapshot must be free of control characters, and the
// visible letters of hostile-but-printable runs must survive (proving the
// pipeline actually rendered the stream rather than dropping it).
func TestRenderLoop_HostilePTYCannotEscape(t *testing.T) {
	hostile := [][]byte{
		[]byte("\x1b[2J\x1b[H"),        // CSI: clear screen + cursor home
		[]byte("X\x00Y\x07Z"),          // C0: NUL + BEL between printable -> "XYZ"
		[]byte("\x1b[31mRED\x1b[0m"),   // SGR color escape -> "RED"
		[]byte("a\nb\rc"),              // embedded LF + CR
		{0x80, 0x9b, 0x9c, 0x9f, 0x7f}, // raw C1 controls + DEL
		[]byte("\x1b]0;pwned\x07"),     // OSC window-title hijack
		[]byte("TAIL"),                 // plain trailing text -> "TAIL"
	}
	frames := make(chan []byte, len(hostile))
	for _, h := range hostile {
		frames <- h
	}
	close(frames)

	stream := &stubTerminalStream{snap: snapBytes(t, 80, 24, nil), frames: frames}
	var c collector

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	RenderTerminal(ctx, "hostile", stream, c.push) // returns once frames closes

	renders := c.snapshots()
	if len(renders) == 0 {
		t.Fatal("no snapshots pushed")
	}
	assertSanitized(t, renders)

	// The final render reflects the fully-fed hostile stream. Its visible text
	// must retain the printable letters (controls stripped, letters kept),
	// proving the emulator+SnapText pipeline ran end to end.
	joined := strings.Join(renders[len(renders)-1].Lines, "")
	for _, want := range []string{"XYZ", "RED", "TAIL"} {
		if !strings.Contains(joined, want) {
			t.Errorf("final render missing sanitized text %q; grid=%q", want, joined)
		}
	}
}

// TestRenderLoop_InitialSnapshotFromStream verifies the stream's initial
// Snapshot() is rendered and pushed as the first TerminalSnapshot, carrying the
// session id, the SnapText lines, and the grid dimensions.
func TestRenderLoop_InitialSnapshotFromStream(t *testing.T) {
	frames := make(chan []byte)
	close(frames) // no live frames: the loop pushes the initial snapshot and returns

	stream := &stubTerminalStream{snap: snapBytes(t, 40, 10, []byte("READY")), frames: frames}
	var c collector

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	RenderTerminal(ctx, "s1", stream, c.push)

	renders := c.snapshots()
	if len(renders) == 0 {
		t.Fatal("no initial snapshot pushed")
	}
	first := renders[0]
	if first.Session != "s1" {
		t.Errorf("session = %q, want %q", first.Session, "s1")
	}
	if first.Cols != 40 || first.Rows != 10 {
		t.Errorf("dims = %dx%d, want 40x10", first.Cols, first.Rows)
	}
	if len(first.Lines) != 10 {
		t.Fatalf("lines = %d, want 10 (one per row)", len(first.Lines))
	}
	if !strings.HasPrefix(first.Lines[0], "READY") {
		t.Errorf("first line = %q, want prefix %q", first.Lines[0], "READY")
	}
	assertSanitized(t, renders)
}

// TestRenderLoop_CoalescesBurst verifies a burst of many frames within the
// debounce window yields FEWER pushed snapshots than frames (coalescing), and
// the final snapshot reflects the latest accumulated state.
func TestRenderLoop_CoalescesBurst(t *testing.T) {
	const n = 50
	frames := make(chan []byte, n)
	for i := 0; i < n; i++ {
		frames <- []byte("x")
	}
	close(frames)

	stream := &stubTerminalStream{snap: snapBytes(t, 80, 24, nil), frames: frames}
	var c collector

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	RenderTerminal(ctx, "burst", stream, c.push)

	renders := c.snapshots()
	if len(renders) == 0 {
		t.Fatal("no snapshots pushed")
	}
	if len(renders) >= n {
		t.Errorf("no coalescing: %d snapshots for %d frames", len(renders), n)
	}
	// Final snapshot reflects the latest state: all n 'x' on the first row.
	last := renders[len(renders)-1]
	if !strings.HasPrefix(last.Lines[0], strings.Repeat("x", n)) {
		t.Errorf("final render missing latest state; first line = %q", last.Lines[0])
	}
	assertSanitized(t, renders)
}
