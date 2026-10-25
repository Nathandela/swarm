package protocol

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/wire"
)

func tryGateControl(t *testing.T, st *shimStream, keys []byte) error {
	t.Helper()
	w, ok := any(st).(interface{ TryControlInput([]byte) error })
	if !ok {
		t.Fatal("shim stream has no bounded launch-gate control writer")
	}
	return w.TryControlInput(keys)
}

type gateWriteConn struct {
	net.Conn
	writes       [][]byte
	deadlines    []time.Time
	failDeadline int
	shortWrite   int
	writeErr     error
	closed       bool
}

func (c *gateWriteConn) Write(p []byte) (int, error) {
	if c.closed {
		return 0, net.ErrClosed
	}
	n := len(p)
	if c.shortWrite > 0 && c.shortWrite < n {
		n = c.shortWrite
	}
	c.writes = append(c.writes, bytes.Clone(p[:n]))
	return n, c.writeErr
}

func (c *gateWriteConn) SetWriteDeadline(d time.Time) error {
	c.deadlines = append(c.deadlines, d)
	if len(c.deadlines) == c.failDeadline {
		return errors.New("deadline failed")
	}
	return nil
}

func (c *gateWriteConn) Close() error { c.closed = true; return nil }

func gateShimStream(c net.Conn) *shimStream {
	return &shimStream{conn: c, caps: shimwire.Caps{ControlInput: true}, done: make(chan struct{})}
}

func TestLaunchGateTransportSuccessWritesOneControlFrameAndClearsDeadline(t *testing.T) {
	c := &gateWriteConn{}
	st := gateShimStream(c)
	before := time.Now()
	if err := tryGateControl(t, st, []byte("\x1b[B")); err != nil {
		t.Fatal(err)
	}
	if len(c.deadlines) != 2 || c.deadlines[0].Before(before.Add(100*time.Millisecond)) ||
		c.deadlines[0].After(time.Now().Add(100*time.Millisecond)) || !c.deadlines[1].IsZero() {
		t.Fatalf("deadlines = %v; want one fixed 100ms budget then clear", c.deadlines)
	}
	if c.closed || len(c.writes) != 1 {
		t.Fatalf("closed=%v writes=%d", c.closed, len(c.writes))
	}
	reader := bytes.NewReader(c.writes[0])
	typ, payload, err := wire.ReadFrame(reader)
	if err != nil || typ != wire.TControl || reader.Len() != 0 {
		t.Fatalf("control frame: type=%v remaining=%d err=%v", typ, reader.Len(), err)
	}
	control, err := shimwire.Decode(payload)
	if err != nil || control.Type != shimwire.TypeControlInput || control.Keys != "\x1b[B" {
		t.Fatalf("control = %+v, err=%v", control, err)
	}
	if err := st.Input([]byte("ordinary")); err != nil {
		t.Fatalf("successful gate left ordinary input unusable: %v", err)
	}
}

func TestLaunchGateTransportBusyAndUnsupportedWriteNothing(t *testing.T) {
	for _, name := range []string{"busy", "unsupported", "closed"} {
		t.Run(name, func(t *testing.T) {
			c := &gateWriteConn{}
			st := gateShimStream(c)
			switch name {
			case "busy":
				st.writeMu.Lock()
				defer st.writeMu.Unlock()
			case "unsupported":
				st.caps.ControlInput = false
			case "closed":
				_ = st.Close()
			}
			start := time.Now()
			err := tryGateControl(t, st, []byte("\r"))
			if err == nil || time.Since(start) > 250*time.Millisecond {
				t.Fatalf("refusal = %v after %v", err, time.Since(start))
			}
			if name == "unsupported" && !errors.Is(err, ErrControlInputUnsupported) {
				t.Fatalf("unsupported = %v", err)
			}
			if len(c.writes) != 0 || len(c.deadlines) != 0 {
				t.Fatalf("refusal attempted I/O: writes=%d deadlines=%v", len(c.writes), c.deadlines)
			}
		})
	}
}

func TestLaunchGateTransportFailureClosesBeforeAnyLaterFrame(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failDeadline int
		shortWrite   int
		writeErr     error
		wantWrites   int
	}{
		{name: "set_deadline", failDeadline: 1},
		{name: "partial_header", shortWrite: 2, writeErr: io.ErrUnexpectedEOF, wantWrites: 1},
		{name: "partial_body", shortWrite: 8, writeErr: io.ErrUnexpectedEOF, wantWrites: 1},
		{name: "short_write_without_error", shortWrite: 2, wantWrites: 1},
		{name: "full_frame_with_error", writeErr: io.ErrUnexpectedEOF, wantWrites: 1},
		{name: "clear_after_full_frame", failDeadline: 2, wantWrites: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &gateWriteConn{failDeadline: tc.failDeadline, shortWrite: tc.shortWrite, writeErr: tc.writeErr}
			st := gateShimStream(c)
			if err := tryGateControl(t, st, []byte("\r")); err == nil {
				t.Fatal("failed gate write reported success")
			}
			if !c.closed || len(c.writes) != tc.wantWrites {
				t.Fatalf("closed=%v writes=%d, want closed and %d", c.closed, len(c.writes), tc.wantWrites)
			}
			select {
			case <-st.done:
			default:
				t.Fatal("failed gate transport left stream open")
			}
			if err := tryGateControl(t, st, []byte("\r")); err == nil {
				t.Fatal("failed transport accepted another gate")
			}
			if err := st.Input([]byte("later")); err == nil || len(c.writes) != tc.wantWrites {
				t.Fatalf("failed transport carried later frame: writes=%d err=%v", len(c.writes), err)
			}
		})
	}
}

func TestLaunchGateTransportStalledPipeReturnsWithinBudget(t *testing.T) {
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	st := gateShimStream(a)
	start := time.Now()
	if err := tryGateControl(t, st, []byte("\r")); err == nil {
		t.Fatal("unread pipe accepted gate write")
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond || elapsed > 250*time.Millisecond {
		t.Fatalf("stalled gate returned after %v, want fixed 100ms deadline within 250ms", elapsed)
	}
	select {
	case <-st.done:
	default:
		t.Fatal("stalled gate did not close its transport")
	}
}
