package shim

// FAILING-FIRST (TDD RED) for W2.1 of the phone refit
// (docs/specifications/phone-refit-playbook.md §3, bead agents-tracker-d45a.2): daemon-authored
// control keys carry their provenance.
//
// THE DEFECT. The phone's Stop (ESC) and its Allow/Deny answers ("1"/"3") reach the shim as
// wire.TDataIn, the frame for bytes somebody TYPED, so ptyWriter.WriteInput counts them as a
// dirty input line and every later submit is refused input_busy until someone presses Enter at
// the machine. The shim may not judge what a byte does to the input line (ptyWriter's own
// comment; internal/skeleton/chat.go, THE QUESTION THAT IS ANSWERABLE): an owner's Escape at
// the terminal is the identical byte. What differs is WHO SENT THE FRAME, so provenance rides
// the frame: shimwire.TypeControlInput carries daemon-authored keys and the shim writes them
// through the non-counting path. A typed draft still refuses byte-for-byte.

import (
	"bytes"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/wire"
)

// controlInputRig is a bare server whose PTY is the write end of a pipe and whose one client
// connection is an in-memory pipe, so every byte the shim writes "to the PTY" is read back
// exactly, with no agent process and no socket in the way.
type controlInputRig struct {
	t     *testing.T
	conn  net.Conn // the daemon's end of the connection
	pty   *os.File // the read end of the PTY
	hello shimwire.Control
}

func newControlInputRig(t *testing.T) *controlInputRig {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pty pipe: %v", err)
	}
	s := &server{ptyIn: &ptyWriter{f: pw}, conns: map[net.Conn]struct{}{}}
	cl, sv := net.Pipe()
	go s.serveConn(sv)
	t.Cleanup(func() { _ = cl.Close(); _ = pr.Close(); _ = pw.Close() })

	r := &controlInputRig{t: t, conn: cl, pty: pr}
	r.write(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
	r.hello = r.readControl()
	if r.hello.Type != shimwire.TypeHello {
		t.Fatalf("hello reply = %+v, want a hello", r.hello)
	}
	return r
}

func (r *controlInputRig) write(c shimwire.Control) {
	r.t.Helper()
	b, err := shimwire.Encode(c)
	if err != nil {
		r.t.Fatalf("encode %+v: %v", c, err)
	}
	if err := wire.WriteFrame(r.conn, wire.TControl, b); err != nil {
		r.t.Fatalf("write %+v: %v", c, err)
	}
}

func (r *controlInputRig) readControl() shimwire.Control {
	r.t.Helper()
	_ = r.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	typ, payload, err := wire.ReadFrame(r.conn)
	if err != nil {
		r.t.Fatalf("read control frame: %v", err)
	}
	if typ != wire.TControl {
		r.t.Fatalf("read frame type %d, want control", typ)
	}
	c, err := shimwire.Decode(payload)
	if err != nil {
		r.t.Fatalf("decode control frame: %v", err)
	}
	return c
}

// control sends daemon-authored keys on the provenance-carrying frame.
func (r *controlInputRig) control(keys string) {
	r.t.Helper()
	r.write(shimwire.Control{Type: shimwire.TypeControlInput, Keys: keys})
}

// typed sends bytes the way an owner's keystrokes arrive.
func (r *controlInputRig) typed(b string) {
	r.t.Helper()
	if err := wire.WriteFrame(r.conn, wire.TDataIn, []byte(b)); err != nil {
		r.t.Fatalf("write typed bytes: %v", err)
	}
}

// submit runs one submit transaction and returns the shim's refusal token ("" on success).
func (r *controlInputRig) submit(text string) string {
	r.t.Helper()
	r.write(shimwire.Control{Type: shimwire.TypeSubmit, Text: text})
	res := r.readControl()
	if res.Type != shimwire.TypeSubmitResult {
		r.t.Fatalf("submit answered %+v, want a submit_result", res)
	}
	return res.Refused
}

// ptyBytes reads exactly len(want) bytes off the PTY and then proves nothing else follows
// within a short quiet window, so the assertion is byte-EXACT in both directions.
func (r *controlInputRig) ptyBytes(want string) {
	r.t.Helper()
	got := make([]byte, len(want))
	_ = r.pty.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := io.ReadFull(r.pty, got); err != nil {
		r.t.Fatalf("the PTY received %q (%d bytes) and then nothing: %v; want exactly %q", got[:n], n, err, want)
	}
	if !bytes.Equal(got, []byte(want)) {
		r.t.Fatalf("the PTY received %q, want %q", got, want)
	}
	_ = r.pty.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	extra := make([]byte, 64)
	if n, _ := r.pty.Read(extra); n > 0 {
		r.t.Fatalf("the PTY received %q after %q; want nothing more", extra[:n], want)
	}
}

// TestHello_AdvertisesControlInput: the capability is negotiated at hello, exactly as
// SubmitTransaction is, so a daemon facing an old shim degrades knowingly.
func TestHello_AdvertisesControlInput(t *testing.T) {
	r := newControlInputRig(t)
	if !r.hello.ControlInput {
		t.Fatalf("hello reply %+v does not advertise control_input", r.hello)
	}
	if !r.hello.Caps().ControlInput {
		t.Fatalf("Caps() of %+v drops ControlInput", r.hello)
	}
}

// TestControlInput_ReachesThePTYByteExact: the frame's keys land on the PTY verbatim -- an
// interrupt and a dialog answer, in order, and nothing else.
func TestControlInput_ReachesThePTYByteExact(t *testing.T) {
	r := newControlInputRig(t)
	r.control("\x1b")
	r.control("3")
	r.ptyBytes("\x1b3")
}

// TestSubmitMessage_AfterInterruptKeys_IsAccepted is phone Stop then phone send: the ESC
// reaches the PTY and the message is still delivered, because a daemon-authored key is not
// somebody typing.
func TestSubmitMessage_AfterInterruptKeys_IsAccepted(t *testing.T) {
	r := newControlInputRig(t)
	r.control("\x1b")
	if refused := r.submit("ship it"); refused != "" {
		t.Fatalf("submit after a daemon-authored interrupt was refused %q; want delivered. "+
			"The phone's Stop counted as typing, and every later send is input_busy until "+
			"someone presses Enter at the machine", refused)
	}
	r.ptyBytes("\x1bship it\r")
}

// TestSubmitMessage_AfterApprovalKeys_IsAccepted is the same for a dialog answer.
func TestSubmitMessage_AfterApprovalKeys_IsAccepted(t *testing.T) {
	r := newControlInputRig(t)
	r.control("1")
	if refused := r.submit("ship it"); refused != "" {
		t.Fatalf("submit after a daemon-authored dialog answer was refused %q; want delivered", refused)
	}
	r.ptyBytes("1ship it\r")
}

// TestSubmitMessage_AfterTypedText_IsRefused is the negative control: an owner's draft, sent
// the way keystrokes arrive, still refuses the submit and survives byte-for-byte.
func TestSubmitMessage_AfterTypedText_IsRefused(t *testing.T) {
	r := newControlInputRig(t)
	r.typed("half a thought")
	if refused := r.submit("ship it"); refused != shimwire.RefusedInputBusy {
		t.Fatalf("submit over an owner's draft answered %q, want %q", refused, shimwire.RefusedInputBusy)
	}
	r.ptyBytes("half a thought")
}
